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
importScripts("./ociapps-loader.js");
importScripts("./wasm_exec.js");

const B = globalThis.WASMBOX_BRIDGE;

// --- OCI launch path -----------------------------------------------------
// `wasmboxSpawnFromOCI(ref)` is the OCI twin of `wasmboxSpawnWorker(url)`:
// it pulls a wasmbox client app (worker.js + wasm_exec.js + <app>.wasm) out
// of an OCI registry using the same multi-registry resolver shape as the
// Go package github.com/wasmdesk/ociapps, then spawns a fresh Web Worker
// from the BLOB URL of the pulled worker.js. Before the worker boots its
// own client SDK we postMessage it a `__wasmbox_assets` envelope so the
// worker.js can load wasm_exec.js + the app's .wasm from blob URLs instead
// of from compositor-relative paths (the OCI app has no path of its own).
//
// Backward-compat: static-path workers (clients/hello/worker.js etc.) never
// see this message and keep working unchanged. The port handoff (step C.1)
// is identical -- spawnFromOCI just builds the Worker differently.
//
// Registries:
//   The default is a SAME-ORIGIN static OCI registry served beside the
//   desktop: the loader GETs <base>/v2/<repo>/manifests|blobs, where <base>
//   is the directory this worker was loaded from. Same-origin means no CORS,
//   no token and no proxy — which is the whole reason the artifacts are
//   mirrored next to the page (GitHub Pages) instead of pulled from ghcr,
//   which sends no CORS headers and so cannot be read cross-origin in a
//   browser. The base resolves correctly both at http://localhost:8080/ and
//   at https://<org>.github.io/<repo>/ with no configuration.
//
//   For the local live-registry dev flow (the Taskfile registry on :5000),
//   override by setting, BEFORE the worker boots,
//     globalThis.WASMBOX_OCI_REGISTRIES = [{url:"http://127.0.0.1:5000"}]
//   or by passing the M2C_BOOT message a `registries` field. We refresh the
//   loader lazily on first spawnFromOCI so a late assignment is honoured.
const SAME_ORIGIN_OCI_BASE = new URL(".", self.location.href).href.replace(/\/+$/, "");
const DEFAULT_OCI_REGISTRIES = [{ url: SAME_ORIGIN_OCI_BASE }];

let _ociLoader = null;
function ociLoader() {
  if (_ociLoader) return _ociLoader;
  const regs = (globalThis.WASMBOX_OCI_REGISTRIES && globalThis.WASMBOX_OCI_REGISTRIES.length)
    ? globalThis.WASMBOX_OCI_REGISTRIES
    : DEFAULT_OCI_REGISTRIES;
  _ociLoader = new globalThis.OCIAppsLoader(regs);
  return _ociLoader;
}

// Test seam: replace the loader (e.g. with one whose cache is a MemoryCache
// pre-seeded with canned bytes). Real callers never use this; the Playwright
// OCI probe does.
globalThis.wasmboxSetOCILoader = function (loader) { _ociLoader = loader; };

// Pick the entrypoint .wasm file out of the app's file map. Convention: the
// loader stores files keyed by their VFS-relative name; the app .wasm is the
// single key ending in ".wasm". The compositor worker does not care about the
// stem ("hello.wasm" vs "myapp.wasm") -- there is exactly one.
function pickWasmFile(files) {
  let pick = null;
  for (const name of files.keys()) {
    if (name.endsWith(".wasm")) { pick = name; break; }
  }
  return pick;
}

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

// WASMBOX_CHROME selects the window-decoration style the Ruby compositor
// applies at boot. Sourced from (in priority order): a ?chrome=NAME URL
// query param on the host page, then a value the page bootstrap stashed
// via self.WASMBOX_CHROME before importScripts'ing this file, then the
// default "openbox" (the historic wasmbox look). Known names live in
// ChromeRegistry::TABLE in compositor.rb — currently "openbox" and "aqua"
// (the latter subsumes the sibling wasmaqua project).
try {
  const url = new URL(self.location.href);
  const fromQuery = url.searchParams.get("chrome");
  globalThis.WASMBOX_CHROME = fromQuery || self.WASMBOX_CHROME || "openbox";
} catch (_) {
  globalThis.WASMBOX_CHROME = self.WASMBOX_CHROME || "openbox";
}

// requestAnimationFrame: UNCONDITIONALLY backed by setTimeout(16) in this
// worker, regardless of whether the host browser also exposes a native
// worker-side rAF. Why force the override even where native rAF exists:
//
//   - Chromium dedicated Workers do NOT expose rAF -> the polyfill is the
//     only option there.
//   - WebKit dedicated Workers behave like Chromium for our purposes -> same.
//   - Firefox 105+ DOES expose rAF in Workers, but its cadence is yoked to
//     the page's own animation-frame loop, which is in turn gated on the
//     OffscreenCanvas's compositor-side display rhythm. Empirically that
//     pipeline coalesces frames during a busy WASM run (e.g. a 180 MB OCI
//     pak fetch). The visible failure was the Quake loading bar appearing
//     to FREEZE for multi-second stretches in Firefox while updating
//     smoothly in Chromium + WebKit. With the setTimeout(16) override, the
//     Ruby compositor re-blits at a steady ~60 Hz on every browser, so the
//     animation cadence is browser-independent.
//
// Regression test: scratchpad/probe-loadingbar-xbrowser.mjs fails on
// Firefox when this override is removed; passes on all three when present.
globalThis.requestAnimationFrame = function (cb) {
  const t = performance.now();
  return setTimeout(() => cb(t), 16);
};
globalThis.cancelAnimationFrame = function (id) { clearTimeout(id); };

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

// wasmboxSpawnFromOCI(ref): the OCI-launch twin of wasmboxSpawnWorker.
//
// Pipeline:
//   1. loader.loadApp(ref): pull manifest + every annotated blob from the
//      registry cluster (sha256-verified, IndexedDB-cached by digest).
//   2. createObjectURL() per file -> {worker.js: <blob>, wasm_exec.js: <blob>,
//      <app>.wasm: <blob>}. The browser keeps blob URLs alive for the
//      lifetime of the worker -- we hold a strong ref on the wrapper so
//      they outlive the spawn even if the page revokes its own refs.
//   3. new Worker(blobURLOf("worker.js")) — Chromium + Firefox both accept a
//      blob: URL as the script source for a dedicated worker, including
//      under cross-origin isolation (the worker inherits COOP/COEP).
//   4. postMessage `{type: COMP_TO_CLIENT_ASSETS, wasm_exec_url, wasm_url}`
//      to the freshly-spawned worker BEFORE the existing port handoff. The
//      client SDK's bootPortHandler is registered at module load, but the
//      assets listener (in sdk.js) is too -- so a worker.js that opts in
//      via WasmboxClient.bootFromOCIAssets gets the URLs in time.
//   5. Hand the worker its MessageChannel port2 (same step-C.1 path as
//      wasmboxSpawnWorker), so application traffic flows over the dedicated
//      wire from message #1.
//
// Returns the same wrapper shape as wasmboxSpawnWorker so Ruby's
// wasmboxAttachWorker / wasmboxPostMessage just work.
globalThis.wasmboxSpawnFromOCI = async function (ref) {
  const app = await ociLoader().loadApp(String(ref));
  // Wrap every file in a blob URL so the spawned worker can fetch them by
  // ordinary URL. The mime-type hints help the browser pick the right
  // loader path (e.g. wasm streaming for application/wasm), though the
  // worker code typically calls fetch() + instantiateStreaming itself.
  const blobURLs = {};
  const fileNames = Array.from(app.files.keys());
  for (const name of fileNames) {
    const bytes = app.files.get(name);
    const mime = name.endsWith(".wasm")
      ? "application/wasm"
      : name.endsWith(".js")
        ? "application/javascript"
        : "application/octet-stream";
    blobURLs[name] = URL.createObjectURL(new Blob([bytes], { type: mime }));
  }
  if (!blobURLs["worker.js"]) {
    throw new Error("ociapps: app " + ref + " missing required file worker.js");
  }
  if (!blobURLs["wasm_exec.js"]) {
    throw new Error("ociapps: app " + ref + " missing required file wasm_exec.js");
  }
  const wasmName = pickWasmFile(app.files);
  if (!wasmName) {
    throw new Error("ociapps: app " + ref + " has no .wasm file");
  }

  const worker = new Worker(blobURLs["worker.js"], { type: "classic" });

  // 1. assets envelope -- delivered as message #1 so the worker's SDK has
  // the blob URLs before it tries to importScripts wasm_exec.js. We send
  // this on `self.onmessage` (the implicit nested-worker channel) rather
  // than on the MessageChannel, because the SDK's bootPortHandler also
  // listens on `self` for the port handoff -- one transport for setup,
  // the other for application traffic. The asset listener fires before
  // the SDK is even constructed, so it cannot be racy.
  worker.postMessage({
    type: B.COMP_TO_CLIENT_ASSETS,
    wasm_exec_url: blobURLs["wasm_exec.js"],
    wasm_url: blobURLs[wasmName],
    wasm_name: wasmName,
    // Forward every file so a richer client can pull additional assets
    // (icons, fonts, ...) without re-implementing the manifest walk.
    files: blobURLs,
    ref: String(ref),
  });

  // 2. port handoff -- identical to wasmboxSpawnWorker.
  const channel = new MessageChannel();
  worker.postMessage({ type: B.COMP_TO_CLIENT_PORT, port: channel.port2 }, [channel.port2]);

  return {
    _worker: worker,
    _port:   channel.port1,
    _blobURLs: blobURLs, // strong ref so the URLs survive the spawn frame
    postMessage(msg, transfer) {
      if (transfer && transfer.length) channel.port1.postMessage(msg, transfer);
      else                              channel.port1.postMessage(msg);
    },
    addEventListener(name, cb) { channel.port1.addEventListener(name, cb); },
    removeEventListener(name, cb) { channel.port1.removeEventListener(name, cb); },
    terminate() {
      try { channel.port1.close(); } catch (_) {}
      // Revoke blob URLs to free memory. Safe to call after the worker has
      // already imported them.
      for (const name of Object.keys(this._blobURLs)) {
        try { URL.revokeObjectURL(this._blobURLs[name]); } catch (_) {}
      }
      worker.terminate();
    },
  };
};

// Build an ImageData of size w*h plus a non-shared backing copy that the blit
// helper reads from. Identical to the step-A/B helper that used to live in
// index.html -- moved here because the canvas now lives in the worker.
globalThis.wasmboxNewImageData = function (sab, w, h, ctl) {
  return {
    image: new ImageData(w, h),
    src:   new Uint8ClampedArray(sab),
    // Seqlock control word, if the client supplied one (older clients send no
    // `ctl` -> seq null -> the fence is a no-op and we blit as before).
    seq:   ctl ? new Int32Array(ctl) : null,
    w: w, h: h,
  };
};

globalThis.wasmboxBlitFromSAB = function (ctx, slot, dx, dy, sx, sy, sw, sh) {
  const stride = slot.w * 4;
  // Seqlock read (when the client published a control word): an ODD seq means
  // the client is mid-paint; a seq that changes across our copy means it wrote
  // while we read (a torn copy). In either case skip this frame's blit — the
  // canvas keeps the last complete frame, and the per-frame re-composite retries
  // next raf once the client has committed (seq even, stable). Never show a
  // half-painted surface.
  let s1 = 0;
  if (slot.seq) {
    s1 = Atomics.load(slot.seq, 0);
    if (s1 & 1) return;
  }
  // Per-slot OffscreenCanvas for the source-over composite (so the dock's
  // transparent corners do not paint black). Identical to the main-thread
  // version, just using OffscreenCanvas instead of an HTMLCanvasElement.
  if (!slot.canvas) {
    slot.canvas = new OffscreenCanvas(slot.w, slot.h);
    slot.octx = slot.canvas.getContext("2d");
  }
  // SAB→ImageData copy is the expensive step (360+ TypedArray.subarray() views
  // per frame for a 480×360 surface, plus a putImageData). The compositor's
  // render loop calls us unconditionally every rAF tick because draw_desktop
  // repaints the canvas background each frame; we must re-present, but we only
  // need to RE-COPY when the client has actually committed new pixels.
  // slot.lastSeq remembers the seq we last copied from; matching seq means the
  // SAB bytes are unchanged → skip the copy + putImageData and just re-present
  // the cached OffscreenCanvas. This bounds the per-idle-window cost to one
  // drawImage instead of W×H bytes + W subarrays + putImageData per frame.
  // Without this, Firefox's GC could not keep up with the JS-side allocation
  // churn when a large external window (e.g. clients/showcase at 480×360) is
  // open, and memory grew unboundedly in idle.
  if (slot.seq && slot.lastSeq === s1) {
    ctx.drawImage(slot.canvas, sx, sy, sw, sh, dx + sx, dy + sy, sw, sh);
    return;
  }
  const dst = slot.image.data;
  for (let row = 0; row < sh; row++) {
    const srcOff = (sy + row) * stride + sx * 4;
    const dstOff = (sy + row) * stride + sx * 4;
    dst.set(slot.src.subarray(srcOff, srcOff + sw * 4), dstOff);
  }
  // The copy landed in our private ImageData; only present it if the client did
  // not write during the copy (so a torn copy is discarded, never blitted).
  if (slot.seq && Atomics.load(slot.seq, 0) !== s1) return;
  slot.octx.putImageData(slot.image, 0, 0, sx, sy, sw, sh);
  slot.lastSeq = s1;
  ctx.drawImage(slot.canvas, sx, sy, sw, sh, dx + sx, dy + sy, sw, sh);
};

// Scale-fit twin of wasmboxBlitFromSAB. Used when an external window's
// on-screen rect (dw x dh) differs from its SAB's native size (slot.w x
// slot.h) -- which happens whenever the user drags the resize grip, since the
// SAB stays at its native dimensions for the lifetime of the surface. The
// helper still does the seqlock-protected copy of the damage rect out of the
// SAB into the slot's private ImageData, but then draws the FULL native
// surface stretched into (dx, dy, dw, dh). Browser drawImage scaling is
// hardware-accelerated and respects ctx.imageSmoothingEnabled (the compositor
// leaves it on the default = true, so 320x240 -> 800x600 is bilinear).
globalThis.wasmboxBlitFromSABScaled = function (ctx, slot, sx, sy, sw, sh, dx, dy, dw, dh) {
  const stride = slot.w * 4;
  let s1 = 0;
  if (slot.seq) {
    s1 = Atomics.load(slot.seq, 0);
    if (s1 & 1) return;
  }
  if (!slot.canvas) {
    slot.canvas = new OffscreenCanvas(slot.w, slot.h);
    slot.octx = slot.canvas.getContext("2d");
  }
  // Same seq-cache trick as wasmboxBlitFromSAB: when the client has not
  // committed new pixels, skip the SAB→ImageData copy + putImageData and
  // re-present the cached OffscreenCanvas. See the long comment in
  // wasmboxBlitFromSAB for the motivation (Firefox GC pressure with idle
  // windows).
  if (slot.seq && slot.lastSeq === s1) {
    ctx.drawImage(slot.canvas, 0, 0, slot.w, slot.h, dx, dy, dw, dh);
    return;
  }
  const dst = slot.image.data;
  for (let row = 0; row < sh; row++) {
    const srcOff = (sy + row) * stride + sx * 4;
    const dstOff = (sy + row) * stride + sx * 4;
    dst.set(slot.src.subarray(srcOff, srcOff + sw * 4), dstOff);
  }
  if (slot.seq && Atomics.load(slot.seq, 0) !== s1) return;
  // Refresh the damaged region in our private OffscreenCanvas at native size,
  // then stretch the WHOLE native surface into the window's on-screen rect.
  // (Partial scale-mapping would be cheaper but accumulates fringe artifacts
  // on integer rounding -- redrawing the whole surface keeps the present
  // visually correct for any scale, at the cost of one extra drawImage.)
  slot.octx.putImageData(slot.image, 0, 0, sx, sy, sw, sh);
  slot.lastSeq = s1;
  ctx.drawImage(slot.canvas, 0, 0, slot.w, slot.h, dx, dy, dw, dh);
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

// `wasmboxSpawnExternalOCI(ref)` -- OCI twin of wasmboxSpawnExternal. Dispatches
// a `wasmbox-spawn-external-oci` CustomEvent on the bus; compositor.rb listens
// for it and runs spawn_external_oci(ref), which then calls
// wasmboxSpawnFromOCI(ref) + wires the resulting worker into the same per-
// client bus as a static spawn. Decoupled via the bus pattern so the Ruby
// side can register the bus listener up front, just like the static path.
globalThis.wasmboxSpawnExternalOCI = function (ref) {
  function dispatch() {
    const bus = fakeDocument.getElementById("__wasmbox_bus");
    if (!bus) { setTimeout(dispatch, 16); return; }
    bus.dispatchEvent(new CustomEvent("wasmbox-spawn-external-oci", { detail: ref }));
  }
  dispatch();
};

// `wasmboxSpawnFromOCIAndAttach(ref, busId)` -- the bridge Ruby calls when it
// wants the full spawn + wire-up done in one shot for an OCI app. The JS side
// awaits the OCI fetch + spawn, then attaches the resulting wrapper to the
// per-worker bus by id so subsequent compositor->client postMessages land on
// the bus's wasmbox-msg listener. Errors are surfaced through console.error
// (the relay forwards them to the page) so a fetch failure does not silently
// no-op. Returns nothing -- the Promise is awaited inside this function.
globalThis.wasmboxSpawnFromOCIAndAttach = function (ref, busId) {
  (async () => {
    let wrapper;
    try {
      wrapper = await globalThis.wasmboxSpawnFromOCI(ref);
    } catch (e) {
      console.error("wasmboxSpawnFromOCIAndAttach(" + ref + "): " + (e && e.stack ? e.stack : e));
      return;
    }
    // Register a bus mapping so route_worker_message can find the wrapper
    // by id later (the Ruby side stored the wrapper-by-bus too, but only
    // for synchronous spawns; for OCI we publish the mapping here).
    const bus = fakeDocument.getElementById(busId);
    if (!bus) {
      console.error("wasmboxSpawnFromOCIAndAttach(" + ref + "): bus " + busId + " not registered");
      return;
    }
    // Same listener shape as wasmboxAttachWorker.
    wrapper.addEventListener("message", function (e) {
      bus.dispatchEvent(new CustomEvent("wasmbox-msg", { detail: e.data }));
    });
    if (wrapper._port && typeof wrapper._port.start === "function") {
      wrapper._port.start();
    }
    // Republish the wrapper on the bus element so Ruby's
    // route_worker_message can pull it back when an inbound message lands
    // (in the static path, Ruby kept the wrapper in @workers_by_id; we
    // attach it here so the OCI path matches without Ruby having to await
    // a promise). Storing on the bus element via setAttribute is a no-op
    // for JS; we use a plain property assignment instead.
    bus._wasmboxWrapper = wrapper;
  })();
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

    case B.M2C_SPAWN_FROM_OCI:
      // OCI spawn relay. Dispatched on the Ruby-listened bus so the
      // compositor's per-worker wiring is identical to a static spawn (bus
      // listener attached up front, JS spawn finishes asynchronously).
      globalThis.wasmboxSpawnExternalOCI(String(m.ref));
      return;

    case B.M2C_SPAWN_DOM_WINDOW:
      // Dom-window spawn relay. The compositor creates a DOMWindow class
      // instance (chrome on canvas, body = iframe overlaid on top); when
      // the compositor positions / resizes / closes the window it posts
      // C2M_IFRAME_* back to the main thread, which maintains the actual
      // <iframe> DOM element.
      globalThis.wasmboxSpawnDOMWindowInternal(String(m.url),
        parseInt(m.w, 10) || 800,
        parseInt(m.h, 10) || 600,
        m.title ? String(m.title) : "dom window");
      return;
  }
});

// --- dom-window helpers used by Ruby --------------------------------------
// The compositor's WindowManager calls these via JS.global.call(...) to
// (1) request the spawn of a new DOMWindow + (2) tell the main thread to
// reposition / detach the iframe overlay. The main thread side lives in
// index.html.
//
// `wasmboxSpawnDOMWindowInternal` is invoked from the M2C_SPAWN_DOM_WINDOW
// relay above; it dispatches a `wasmbox-spawn-dom-window` CustomEvent on
// the bus the compositor listens on (same pattern as wasmboxSpawnExternal).

globalThis.wasmboxSpawnDOMWindowInternal = function (url, w, h, title) {
  function dispatch() {
    const bus = fakeDocument.getElementById("__wasmbox_bus");
    if (!bus) { setTimeout(dispatch, 16); return; }
    bus.dispatchEvent(new CustomEvent("wasmbox-spawn-dom-window", {
      detail: { url: url, w: w, h: h, title: title },
    }));
  }
  dispatch();
};

// Compositor calls these to drive the iframe overlay on the main thread.
// Each is a one-shot postMessage; the main thread index.html owns the
// actual DOM manipulation.
globalThis.wasmboxIframeAttach = function (windowID, url, x, y, w, h) {
  self.postMessage({
    type: B.C2M_IFRAME_ATTACH,
    window_id: windowID, url: url,
    x: x | 0, y: y | 0, w: w | 0, h: h | 0,
  });
};
globalThis.wasmboxIframeMove = function (windowID, x, y, w, h) {
  self.postMessage({
    type: B.C2M_IFRAME_MOVE,
    window_id: windowID,
    x: x | 0, y: y | 0, w: w | 0, h: h | 0,
  });
};
globalThis.wasmboxIframeDetach = function (windowID) {
  self.postMessage({ type: B.C2M_IFRAME_DETACH, window_id: windowID });
};

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
    deltaX: m.deltaX ?? 0,   // wheel / two-finger swipe
    deltaY: m.deltaY ?? 0,
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
    // Quake is a heavy, build-time-only client (built by `task build:quake`
    // from the go-quake1 sibling, not the default `task build`). Its worker.js
    // is committed, so gate on the .wasm it loads — otherwise the worker boots
    // and throws an uncaught WebAssembly compile error when quake.wasm 404s.
    autoSpawnIfPresent("clients/quake/worker.js", "clients/quake/quake.wasm");
    autoSpawnIfPresent("clients/dock/worker.js");
  } catch (e) {
    self.postMessage({ type: B.C2M_ERROR, message: String(e && e.stack ? e.stack : e) });
  }
}

// autoSpawnIfPresent spawns workerUrl only if probeUrl is fetchable. probeUrl
// defaults to workerUrl, but pass the app's .wasm when worker.js is committed
// yet its wasm is built on demand (e.g. quake) — worker.js being present does
// not mean the client can actually boot.
function autoSpawnIfPresent(workerUrl, probeUrl) {
  fetch(probeUrl || workerUrl, { method: "HEAD" })
    .then((r) => { if (r.ok) globalThis.wasmboxSpawnExternal(workerUrl); })
    .catch(() => {});
}
