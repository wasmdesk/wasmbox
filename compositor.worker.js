// SPDX-License-Identifier: BSD-3-Clause
//
// wasmbox compositor worker -- step C.
//
// The compositor and its embedded Ruby runtime (wasmbox.wasm) used to run on
// the page's main thread. Step C lifts them into a dedicated Web Worker so the
// page chrome stays responsive even while Ruby is busy compositing. The page's
// only role is now to own the <canvas> + the DOM listeners and to relay events
// here through postMessage.
//
// To keep compositor.rb untouched, this worker installs a tiny DOM shim before
// the wasm runtime starts: enough of `window`, `document`, `localStorage` and
// `requestAnimationFrame` for the Ruby code to think it is still running on a
// page. The shim is intentionally minimal -- it implements exactly the surface
// the Ruby compositor (compositor.rb) calls:
//
//   window.innerWidth/innerHeight     (synced from main on boot + resize)
//   window.addEventListener("resize", cb)
//   window.addEventListener("keydown"|"keyup", cb)
//   document.getElementById(id)        (returns the OffscreenCanvas for "screen",
//                                       or one of the in-memory event-bus elements)
//   document.createElement("div")      (returns a synthetic event target)
//   document.body.appendChild(el)      (registers an element so getElementById
//                                       can find it later)
//   localStorage.getItem / setItem / removeItem  (write-through to main)
//   requestAnimationFrame(fn)          (polyfilled with setTimeout, since Chrome
//                                       does not expose rAF inside a dedicated
//                                       worker by default)
//
// External client workers (clients/hello, clients/dock, ...) are spawned with
// plain `new Worker(url)` from inside this worker; nested workers are universal
// and inherit cross-origin isolation, so the existing client SDK
// (clients/sdk/sdk.js) keeps talking to "the compositor" the same way as in
// step B -- the only thing that changed is the identity of the worker on the
// other end of the postMessage channel.

"use strict";

importScripts("./bridge.js");
importScripts("./wasm_exec.js");

const B = globalThis.WASMBOX_BRIDGE;

// --- DOM shim -------------------------------------------------------------
// Synthetic event target used for `document.createElement("div")` results +
// for window-level event registration. Mirrors the slice of the EventTarget
// API the Ruby compositor actually exercises.
class FakeEventTarget {
  constructor() {
    this._listeners = Object.create(null);
    this._attrs = Object.create(null);
  }
  setAttribute(k, v) { this._attrs[k] = String(v); if (k === "id") this.id = String(v); }
  getAttribute(k)    { return this._attrs[k]; }
  set(k, v) {
    // The JS bridge calls .set("id", "...") via syscall/js, which lands here.
    if (k === "id") this.id = String(v);
    this._attrs[k] = v;
  }
  get(k) {
    if (k === "id") return this.id;
    return this._attrs[k];
  }
  addEventListener(name, cb) {
    (this._listeners[name] ||= []).push(cb);
  }
  removeEventListener(name, cb) {
    const arr = this._listeners[name];
    if (!arr) return;
    const i = arr.indexOf(cb);
    if (i >= 0) arr.splice(i, 1);
  }
  dispatchEvent(ev) {
    const arr = this._listeners[ev.type];
    if (!arr) return true;
    // Copy so a listener that removes itself does not skip its peer.
    for (const cb of arr.slice()) {
      try { cb(ev); } catch (e) { postLog("error", "listener threw: " + e); }
    }
    return true;
  }
}

// Synthetic CustomEvent so document.dispatchEvent(new CustomEvent(...)) works
// inside the worker. CustomEvent exists in modern worker contexts already, but
// we provide one anyway so the shim is hermetic.
class FakeCustomEvent {
  constructor(type, init) {
    this.type = type;
    this.detail = (init && "detail" in init) ? init.detail : null;
  }
}
if (typeof globalThis.CustomEvent !== "function") {
  globalThis.CustomEvent = FakeCustomEvent;
}

// Storage shim: getItem reads from an in-worker cache the main thread seeds at
// boot via M2C_STORAGE_SNAPSHOT; setItem/removeItem write through to main so
// the real localStorage stays the source of truth across reloads.
class FakeStorage {
  constructor() { this._cache = Object.create(null); }
  getItem(k) {
    return Object.prototype.hasOwnProperty.call(this._cache, k) ? this._cache[k] : null;
  }
  setItem(k, v) {
    const s = String(v);
    this._cache[k] = s;
    self.postMessage({ type: B.C2M_STORAGE_SET, key: String(k), value: s });
  }
  removeItem(k) {
    delete this._cache[k];
    self.postMessage({ type: B.C2M_STORAGE_REMOVE, key: String(k) });
  }
  // Seed entries received from main at boot. Not part of the Storage API; the
  // worker calls it itself when it receives the snapshot.
  _seed(entries) {
    if (!entries) return;
    for (const k of Object.keys(entries)) this._cache[k] = String(entries[k]);
  }
}

const fakeStorage = new FakeStorage();

// Registry of synthetic elements by id, so getElementById finds bus elements
// that compositor.rb created via createElement+appendChild.
const elementsById = new Map();

// The OffscreenCanvas transferred from main at boot. We register it under
// "screen" so compositor.rb's getElementById("screen") returns it.
let offscreenScreen = null;
// Last known viewport size (mirrors main's window.innerWidth/innerHeight).
let viewportW = 0;
let viewportH = 0;

// Fake window: the listeners we register here are driven by dom_event messages
// from main (kind === "keydown"|"keyup"|"resize"). The Ruby compositor uses
// `JS.window` exclusively as an EventTarget + as the source of innerWidth/Height
// and of localStorage.
const fakeWindow = new FakeEventTarget();
Object.defineProperty(fakeWindow, "innerWidth",  { get: () => viewportW });
Object.defineProperty(fakeWindow, "innerHeight", { get: () => viewportH });
Object.defineProperty(fakeWindow, "localStorage", { get: () => fakeStorage });

// Fake document: getElementById covers "screen" + bus ids; createElement
// returns a FakeEventTarget so the bus pattern compositor.rb relies on works
// unchanged; body.appendChild registers the new element by id.
const fakeDocument = {
  getElementById(id) {
    if (id === "screen") return offscreenScreen;
    return elementsById.get(String(id)) || null;
  },
  createElement(_tag) { return new FakeEventTarget(); },
  body: {
    appendChild(el) {
      if (el && el.id) elementsById.set(String(el.id), el);
      return el;
    },
  },
};

// Install the shims on the worker global BEFORE the wasm runtime imports
// syscall/js -- the Ruby JS bridge resolves `js.Global().Get("window")` etc.
// at call time, so as long as the names are on `self` before the first call,
// the bridge sees them.
globalThis.window = fakeWindow;
globalThis.document = fakeDocument;
globalThis.localStorage = fakeStorage;

// requestAnimationFrame polyfill: dedicated workers in Chromium do not expose
// rAF (Firefox does in some builds, but we cannot rely on it). The Ruby render
// loop calls JS.raf which dispatches to globalThis.requestAnimationFrame, so a
// setTimeout-backed implementation at ~60 Hz keeps the loop ticking.
if (typeof globalThis.requestAnimationFrame !== "function") {
  globalThis.requestAnimationFrame = function (cb) {
    const t = performance.now();
    return setTimeout(() => cb(t), 16);
  };
}
if (typeof globalThis.cancelAnimationFrame !== "function") {
  globalThis.cancelAnimationFrame = function (id) { clearTimeout(id); };
}

// --- helpers shared with the page (used to live in index.html) ------------
// Spawn a child Web Worker by URL and hand it a dedicated MessageChannel.
//
// Step C.1 architecture: each external client gets its own MessagePort to the
// compositor (Wayland-style direct connection). Until step C, the client had
// to talk through `self.parent` -- i.e. the implicit channel between a nested
// worker and its spawner. That works, but every client message landed in the
// SAME `self.onmessage` on the compositor (we demuxed by sender via per-worker
// bus elements), which is the kind of shared knot Wayland deliberately avoids.
//
// With an explicit MessageChannel:
//   - port1 stays on the compositor side, retained per-window-worker so we can
//     postMessage input/welcome/closed events to a SPECIFIC client.
//   - port2 is transferred to the spawned worker immediately after `new Worker`
//     in a one-shot `{type:"__wasmbox_port"}` message; the SDK swaps its
//     channel from `self.parent` to that port.
//
// Returns a thin wrapper that LOOKS like a Worker to the rest of the
// compositor (Ruby calls `wasmboxAttachWorker(w, busId)` +
// `wasmboxPostMessage(w, msg)` on it) but routes message I/O through port1.
// Nested workers still inherit COOP/COEP, so SharedArrayBuffer keeps working.
globalThis.wasmboxSpawnWorker = function (url) {
  const worker = new Worker(url, { type: "classic" });
  const channel = new MessageChannel();
  // Hand the worker its end of the channel as the very first message it sees.
  // The transfer list moves the port across the worker boundary; on the other
  // side the SDK listens on `self.onmessage` for {type:"__wasmbox_port"} and
  // swaps to it before any application traffic.
  worker.postMessage({ type: B.COMP_TO_CLIENT_PORT, port: channel.port2 }, [channel.port2]);
  // We deliberately do NOT call channel.port1.start() here: an unstarted port
  // BUFFERS incoming messages until the consumer is ready. The compositor's
  // Ruby `spawn_external` calls `wasmboxAttachWorker(wrapper, busId)` AFTER
  // it returns, and only that helper knows the bus id to dispatch onto. If
  // we started the port early, the client's `hello` (sent as soon as its SDK
  // boots, racing the Ruby side) could land before any listener was attached
  // and get silently dropped. wasmboxAttachWorker now does the start().
  return {
    _worker: worker,
    _port:   channel.port1,
    postMessage(msg, transfer) {
      if (transfer && transfer.length) channel.port1.postMessage(msg, transfer);
      else                              channel.port1.postMessage(msg);
    },
    addEventListener(name, cb) { channel.port1.addEventListener(name, cb); },
    removeEventListener(name, cb) { channel.port1.removeEventListener(name, cb); },
    terminate() { try { channel.port1.close(); } catch (_) {} worker.terminate(); },
  };
};

// Build an ImageData of size w*h plus a non-shared backing copy that the blit
// helper reads from. Identical to the step-A/B helper that used to live in
// index.html -- moved here because the canvas now lives in the worker.
globalThis.wasmboxNewImageData = function (sab, w, h) {
  return {
    image: new ImageData(w, h),
    src:   new Uint8ClampedArray(sab),
    w: w, h: h,
  };
};

globalThis.wasmboxBlitFromSAB = function (ctx, slot, dx, dy, sx, sy, sw, sh) {
  const stride = slot.w * 4;
  const dst = slot.image.data;
  for (let row = 0; row < sh; row++) {
    const srcOff = (sy + row) * stride + sx * 4;
    const dstOff = (sy + row) * stride + sx * 4;
    dst.set(slot.src.subarray(srcOff, srcOff + sw * 4), dstOff);
  }
  // Per-slot OffscreenCanvas for the source-over composite (so the dock's
  // transparent corners do not paint black). Identical to the main-thread
  // version, just using OffscreenCanvas instead of an HTMLCanvasElement.
  if (!slot.canvas) {
    slot.canvas = new OffscreenCanvas(slot.w, slot.h);
    slot.octx = slot.canvas.getContext("2d");
  }
  slot.octx.putImageData(slot.image, 0, 0, sx, sy, sw, sh);
  ctx.drawImage(slot.canvas, sx, sy, sw, sh, dx + sx, dy + sy, sw, sh);
};

globalThis.wasmboxMakeObject = function () {
  const o = {};
  for (let i = 0; i < arguments.length; i += 2) o[arguments[i]] = arguments[i + 1];
  return o;
};

globalThis.wasmboxPostMessage = function (worker, msg) { worker.postMessage(msg); };

// Bridge a child worker's `message` event onto the compositor's per-worker bus
// element. Same shape as the step-B helper, but in step C.1 `worker` is the
// MessageChannel-port wrapper returned by wasmboxSpawnWorker -- so the
// listener lands on port1, and we MUST call port.start() (via the wrapper's
// _port handle) AFTER attaching so the SDK's `hello` is not dropped.
globalThis.wasmboxAttachWorker = function (worker, busId) {
  worker.addEventListener("message", function (e) {
    const bus = fakeDocument.getElementById(busId);
    if (!bus) return;
    bus.dispatchEvent(new CustomEvent("wasmbox-msg", { detail: e.data }));
  });
  // Drain any messages the client buffered before the listener existed.
  // The wrapper exposes _port (the retained port1); plain Worker objects
  // (used by tests or legacy code) have no _port -- start() is a no-op then.
  if (worker._port && typeof worker._port.start === "function") {
    worker._port.start();
  }
};

// `wasmboxSpawnExternal(url)` -- still the public hook; called from inside
// the worker (auto-spawn on ready) and indirectly from main (relayed). Walks
// the compositor's bus element exactly like step B.
globalThis.wasmboxSpawnExternal = function (url) {
  function dispatch() {
    const bus = fakeDocument.getElementById("__wasmbox_bus");
    if (!bus) { setTimeout(dispatch, 16); return; }
    bus.dispatchEvent(new CustomEvent("wasmbox-spawn-external", { detail: url }));
  }
  dispatch();
};

// --- console relay (so Ruby's JS.log surfaces on the page) ---------------
function postLog(level, text) {
  self.postMessage({ type: B.C2M_CONSOLE, level: level, text: String(text) });
}
// Keep the worker's own console as the primary sink; the relay is an extra
// signal so the headless harness sees Ruby's startup line.
const originalLog = console.log.bind(console);
const originalErr = console.error.bind(console);
console.log = function (...args) {
  try { postLog("log", args.join(" ")); } catch (_) { /* before main boot is fine */ }
  originalLog(...args);
};
console.error = function (...args) {
  try { postLog("error", args.join(" ")); } catch (_) { /* before main boot is fine */ }
  originalErr(...args);
};

// --- main <-> compositor message handler ----------------------------------
let booted = false;

self.addEventListener("message", async (ev) => {
  const m = ev.data;
  if (!m || typeof m.type !== "string") return;
  switch (m.type) {
    case B.M2C_BOOT:
      if (booted) return;
      booted = true;
      offscreenScreen = m.canvas;
      viewportW = m.w | 0;
      viewportH = m.h | 0;
      // Seed storage before the Ruby boot block runs restore_layout.
      if (m.storage) fakeStorage._seed(m.storage);
      await bootWasm();
      return;

    case B.M2C_RESIZE:
      viewportW = m.w | 0;
      viewportH = m.h | 0;
      fakeWindow.dispatchEvent({ type: "resize" });
      // The OffscreenCanvas backing store also needs to grow, otherwise the
      // compositor's fit_canvas() write to canvas.width/height is the only
      // resize handler that runs and we would render into a stale buffer. The
      // Ruby resize callback already calls fit_canvas, which writes width/
      // height on the canvas ref -- that propagates to the OffscreenCanvas
      // here, so nothing else to do.
      return;

    case B.M2C_DOM_EVENT:
      dispatchDomEvent(m);
      return;

    case B.M2C_SPAWN_EXTERNAL:
      globalThis.wasmboxSpawnExternal(String(m.url));
      return;
  }
});

// Route a dom_event message to the right shim target. Synthesises an event
// object whose fields cover everything compositor.rb reads through e.get(...):
//   key, code            -- keydown / keyup
//   offsetX, offsetY     -- mouse* (canvas-relative)
//   button               -- mousedown
//   preventDefault()     -- no-op (main already calls it on the real event)
function dispatchDomEvent(m) {
  const ev = {
    type: m.kind,
    key: m.key ?? "",
    code: m.code ?? "",
    offsetX: m.offsetX ?? 0,
    offsetY: m.offsetY ?? 0,
    button: m.button ?? 0,
    preventDefault() {},
  };
  // The Ruby `e.get("foo")` path goes through syscall/js .Get, which reads a
  // property by string name -- a plain JS object literal Just Works. We add
  // `type` so dispatchEvent finds the right listeners.
  if (m.target === B.TARGET_CANVAS) {
    canvasBus.dispatchEvent(ev);
  } else {
    fakeWindow.dispatchEvent(ev);
  }
}

// Sidecar FakeEventTarget for canvas listeners. OffscreenCanvas inside a
// worker does not receive user-input events (the real events fire on the
// HTMLCanvasElement in the page); we install all canvas listeners on this
// sidecar and the main thread relays dom_event messages that drive it.
const canvasBus = new FakeEventTarget();

async function bootWasm() {
  if (!offscreenScreen) {
    self.postMessage({ type: B.C2M_ERROR, message: "boot without OffscreenCanvas" });
    return;
  }
  // Re-route any addEventListener call the Ruby compositor makes on the
  // OffscreenCanvas onto the sidecar so the dispatch path matches the
  // keyboard one (main's dom_event messages drive both).
  offscreenScreen.addEventListener = function (name, cb) {
    canvasBus.addEventListener(name, cb);
  };
  try {
    const go = new Go();
    const wasm = await WebAssembly.instantiateStreaming(
      fetch("./wasmbox.wasm"), go.importObject);
    go.run(wasm.instance);
    // Compositor.attach_to_canvas + comp.start ran synchronously inside main()
    // before we get here; signal main that the worker is live.
    self.postMessage({ type: B.C2M_READY });
    // Auto-spawn the same demo clients as the old index.html did, so a page
    // load still ends with a populated desktop.
    globalThis.wasmboxSpawnExternal("clients/hello/worker.js");
    autoSpawnIfPresent("clients/quake/worker.js");
    autoSpawnIfPresent("clients/dock/worker.js");
  } catch (e) {
    self.postMessage({ type: B.C2M_ERROR, message: String(e && e.stack ? e.stack : e) });
  }
}

function autoSpawnIfPresent(workerUrl) {
  fetch(workerUrl, { method: "HEAD" })
    .then((r) => { if (r.ok) globalThis.wasmboxSpawnExternal(workerUrl); })
    .catch(() => {});
}
