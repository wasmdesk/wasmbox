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

// __wasmboxReadRegion(x,y,w,h): TEST HOOK. Reads the composited desktop pixels
// straight from the worker-owned OffscreenCanvas via getImageData -- the only
// reliable way to observe the real rendered output under automation (a
// Playwright viewport screenshot does NOT capture worker OffscreenCanvas
// frames). Returns a brightness average + a cheap content hash + a non-black
// ratio for the region; sampling it over time detects animation / flicker /
// freeze deterministically. Read from the page via
// `worker.evaluate(({x,y,w,h}) => globalThis.__wasmboxReadRegion(x,y,w,h), r)`.
globalThis.__wasmboxReadRegion = function (x, y, w, h) {
  if (!offscreenScreen) return null;
  let ctx;
  try { ctx = offscreenScreen.getContext("2d"); } catch (_) { return null; }
  if (!ctx) return null;
  let img;
  try { img = ctx.getImageData(x | 0, y | 0, w | 0, h | 0); } catch (_) { return null; }
  const d = img.data;
  let sum = 0, nonblack = 0, hash = 2166136261;
  // Luma mean + variance over the region, so a caller can tell a FLAT tile
  // (a solid placeholder fill -> variance ~0) from a TEXTURED one (a live
  // window snapshot -> high variance). Rec.601 luma, integer weights.
  let lsum = 0, lsq = 0;
  for (let i = 0; i < d.length; i += 4) {
    const r = d[i], g = d[i + 1], b = d[i + 2];
    sum += r + g + b;
    if (r + g + b > 40) nonblack++;
    const luma = (r * 77 + g * 150 + b * 29) >> 8; // ~0.299/0.587/0.114
    lsum += luma;
    lsq += luma * luma;
    if ((i & 255) === 0) { // sample the hash cheaply
      hash = ((hash ^ r) * 16777619) >>> 0;
      hash = ((hash ^ g) * 16777619) >>> 0;
      hash = ((hash ^ b) * 16777619) >>> 0;
    }
  }
  const px = d.length / 4;
  const lmean = lsum / px;
  const variance = Math.max(0, lsq / px - lmean * lmean);
  return { w: w | 0, h: h | 0, brightness: Math.round(sum / px / 3), nonblackPct: Math.round((100 * nonblack) / px), hash: hash >>> 0, variance: Math.round(variance) };
};
// __wasmboxGrabRegion(x,y,w,h): TEST HOOK. Same source as __wasmboxReadRegion,
// but returns the actual pixels as a PNG data URL so a test can save a frame to
// disk and a human/agent can SEE what was composited (screenshots miss it).
// Async: OffscreenCanvas has no toDataURL, so we round-trip through convertToBlob.
globalThis.__wasmboxGrabRegion = async function (x, y, w, h) {
  if (!offscreenScreen) return null;
  let ctx;
  try { ctx = offscreenScreen.getContext("2d"); } catch (_) { return null; }
  if (!ctx) return null;
  let img;
  try { img = ctx.getImageData(x | 0, y | 0, w | 0, h | 0); } catch (_) { return null; }
  const tmp = new OffscreenCanvas(w | 0, h | 0);
  tmp.getContext("2d").putImageData(img, 0, 0);
  const blob = await tmp.convertToBlob({ type: "image/png" });
  const bytes = new Uint8Array(await blob.arrayBuffer());
  let bin = "";
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
  return "data:image/png;base64," + btoa(bin);
};
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

// WASMBOX_FRAME selects the window-decoration style ("frame", the
// UI/UX "chrome" without the browser-name overlap) the Ruby compositor
// applies at boot. Sourced from (in priority order): a ?frame=NAME URL
// query param on the host page, then a value the page bootstrap stashed
// via self.WASMBOX_FRAME before importScripts'ing this file, then the
// default "openbox" (the historic wasmbox look). Known names live in
// FrameRegistry::TABLE in compositor.rb — currently "openbox" and "aqua"
// (the latter subsumes the sibling wasmaqua project).
try {
  const url = new URL(self.location.href);
  const fromQuery = url.searchParams.get("frame");
  globalThis.WASMBOX_FRAME = fromQuery || self.WASMBOX_FRAME || "openbox";
} catch (_) {
  globalThis.WASMBOX_FRAME = self.WASMBOX_FRAME || "openbox";
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
// --- per-frame cost instrumentation (test hook) ---------------------------
// A cheap set of counters the blit helpers below bump, plus a wrapper around
// the rAF callback that times each Compositor#render. `__wasmboxFrameStats()`
// snapshots them so a headless probe (test/probe-frame-cost.mjs) can attribute
// the idle per-frame cost to a specific overlay: an overlay that RE-RENDERS
// every frame shows up as a non-zero *DecodesPerFrame (a Widgets.render +
// base64 + atob round-trip), whereas a cached one shows only *PresentsPerFrame
// (a single drawImage of a cached buffer). The counters are integer bumps
// (negligible cost) and left in permanently as a regression guard.
globalThis.__wasmboxStats = {
  frames: 0, frameMsSum: 0, frameMsMax: 0,
  sabCopies: 0, sabPresents: 0,      // external-window SAB blits (copy vs cached)
  rgbaDecodes: 0, rgbaPresents: 0,   // opaque putImageData path (desktop/menu)
  overDecodes: 0, overPresents: 0,   // translucent drawImage path (hud/tray/…)
  decodeKeys: {},                    // per-key decode count (attribution)
};
globalThis.__wasmboxStatsBumpDecode = function (key) {
  const s = globalThis.__wasmboxStats;
  s.decodeKeys[key] = (s.decodeKeys[key] || 0) + 1;
};
globalThis.__wasmboxFrameStats = function (reset) {
  const s = globalThis.__wasmboxStats;
  const n = s.frames || 1;
  const snap = {
    frames: s.frames,
    avgFrameMs: s.frameMsSum / n,
    maxFrameMs: s.frameMsMax,
    sabCopiesPerFrame: s.sabCopies / n,
    sabPresentsPerFrame: s.sabPresents / n,
    rgbaDecodesPerFrame: s.rgbaDecodes / n,
    rgbaPresentsPerFrame: s.rgbaPresents / n,
    overDecodesPerFrame: s.overDecodes / n,
    overPresentsPerFrame: s.overPresents / n,
    decodeKeys: Object.assign({}, s.decodeKeys),
  };
  if (reset) {
    s.frames = 0; s.frameMsSum = 0; s.frameMsMax = 0;
    s.sabCopies = 0; s.sabPresents = 0;
    s.rgbaDecodes = 0; s.rgbaPresents = 0;
    s.overDecodes = 0; s.overPresents = 0;
    s.decodeKeys = {};
  }
  return snap;
};

// Set once the first compositor render callback has run to completion -- i.e.
// the first frame has actually been blitted onto the (transferred) Offscreen
// canvas. We detect "first presented frame" entirely worker-side here so the
// splash can hit 100% on a real present, not merely on go.run returning.
let firstFramePresented = false;

globalThis.requestAnimationFrame = function (cb) {
  const t = performance.now();
  return setTimeout(() => {
    const s = globalThis.__wasmboxStats;
    const t0 = performance.now();
    cb(t);
    const dt = performance.now() - t0;
    s.frames++;
    s.frameMsSum += dt;
    if (dt > s.frameMsMax) s.frameMsMax = dt;
    if (!firstFramePresented) {
      firstFramePresented = true;
      // Boot stage 5/5: the desktop is on screen. Main fades + removes splash.
      self.postMessage({ type: "boot", stage: "ready" });
    }
  }, 16);
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
    globalThis.__wasmboxStats.sabPresents++;
    ctx.drawImage(slot.canvas, sx, sy, sw, sh, dx + sx, dy + sy, sw, sh);
    return;
  }
  globalThis.__wasmboxStats.sabCopies++;
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
    globalThis.__wasmboxStats.sabPresents++;
    ctx.drawImage(slot.canvas, 0, 0, slot.w, slot.h, dx, dy, dw, dh);
    return;
  }
  globalThis.__wasmboxStats.sabCopies++;
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

// wasmboxWindowSeq(slot): the content sequence of an external window's live
// surface -- the seq the blit path last COPIED into slot.canvas (slot.lastSeq),
// so it changes exactly when the client committed new pixels and we re-copied
// them. Used by the Ruby thumbnail cache (compositor/15_alttab.rb) to key a
// grabbed Alt-Tab / Exposé thumbnail by (window id, size, content-seq): an idle
// window holds this value (the tile is re-presented from cache), an animating
// one bumps it (the tile is re-grabbed + re-rendered). Reuses the SAME seqlock
// bookkeeping the blit path already maintains — no extra per-frame copy. Returns
// -1 for a slot with no surface (never built / no image_data).
globalThis.wasmboxWindowSeq = function (slot) {
  if (!slot) return -1;
  if (typeof slot.lastSeq === "number") return slot.lastSeq | 0;
  if (slot.seq) return Atomics.load(slot.seq, 0) & ~1; // clear the odd mid-paint bit
  return 0;
};

// wasmboxWindowLiveSeq(slot): the client's CURRENTLY-PUBLISHED content seq (not
// the seq the blit path last copied). Reads the live seqlock word with the odd
// mid-paint bit cleared, so it changes exactly when the client commits a new
// frame — BEFORE the compositor blits it. The idle-repaint gate
// (compositor/06_core.rb Compositor#needs_paint?) polls this per external window
// each frame to decide whether a client committed new pixels since the last
// painted frame; if none did (and nothing else is dirty), the whole frame is
// skipped. Unlike wasmboxWindowSeq (which returns lastSeq — what we COPIED), this
// must return the LIVE value or the gate would never observe a pending commit.
// Returns 0 for a slot with no seqlock (older client — its repaints ride the
// "commit" message path instead) and -1 for a missing slot.
globalThis.wasmboxWindowLiveSeq = function (slot) {
  if (!slot) return -1;
  if (slot.seq) return Atomics.load(slot.seq, 0) & ~1; // clear the odd mid-paint bit
  return 0;
};

// wasmboxGrabWindow(slot, dw, dh): grab an external window's CURRENT framebuffer
// downscaled to dw x dh and return it as base64 RGBA (top-left origin, 4 B/px,
// row-major — the exact layout Widgets.thumbnail + wasmboxBlitRGBAOver expect).
//
// The pixel source is slot.canvas — the seqlock-protected last-complete
// native-size frame the blit path (wasmboxBlitFromSAB) already produced THIS
// frame (windows draw before the Alt-Tab / Exposé overlay in Compositor#render).
// So the grab never samples a half-painted SAB and costs one hardware-accelerated
// drawImage downscale + one getImageData, NOT a fresh W x H SAB copy per tile.
// It falls back to staging the raw SAB (slot.src, seqlock-checked) only when the
// window has never been composited (slot.canvas absent). Returns { b64, w, h,
// seq } or null when there is nothing capturable (no slot / no surface / a torn
// read on the fallback path — the caller then keeps its placeholder tile).
globalThis.wasmboxGrabWindow = function (slot, dw, dh) {
  if (!slot) return null;
  dw = dw | 0; dh = dh | 0;
  if (dw < 1 || dh < 1) return null;
  const seq = globalThis.wasmboxWindowSeq(slot);
  const oc = new OffscreenCanvas(dw, dh);
  const octx = oc.getContext("2d");
  if (slot.canvas) {
    // Downscale the last complete frame (bilinear, imageSmoothingEnabled=true).
    octx.drawImage(slot.canvas, 0, 0, slot.w, slot.h, 0, 0, dw, dh);
  } else if (slot.src) {
    // Never-composited fallback: stage the raw SAB at native size (seqlock-safe:
    // discard a mid-paint or torn read), then scale it down.
    let s1 = 0;
    if (slot.seq) { s1 = Atomics.load(slot.seq, 0); if (s1 & 1) return null; }
    const img = new ImageData(slot.w, slot.h);
    img.data.set(slot.src.subarray(0, slot.w * slot.h * 4));
    if (slot.seq && Atomics.load(slot.seq, 0) !== s1) return null;
    const stage = new OffscreenCanvas(slot.w, slot.h);
    stage.getContext("2d").putImageData(img, 0, 0);
    octx.drawImage(stage, 0, 0, slot.w, slot.h, 0, 0, dw, dh);
  } else {
    return null;
  }
  let data;
  try { data = octx.getImageData(0, 0, dw, dh).data; } catch (_) { return null; }
  // Encode the RGBA bytes to base64 (ASCII survives the rbgo string bridge
  // intact, unlike raw binary — same reason the widgets blit path base64s).
  let bin = "";
  const CHUNK = 0x8000;
  for (let i = 0; i < data.length; i += CHUNK) {
    bin += String.fromCharCode.apply(null, data.subarray(i, Math.min(i + CHUNK, data.length)));
  }
  return { b64: btoa(bin), w: dw, h: dh, seq: seq };
};

// Blit an opaque RGBA buffer (top-left origin, 4 bytes/px, row-major — exactly
// the layout of the Ruby `Widgets.render` "pixels" field, and of ImageData) at
// (dx, dy). Used by the widgets-painted compositor menu
// (compositor/08_menu_widgets.rb) to prove the render -> RGBA -> overlay seam.
//
// The buffer arrives base64-encoded: the rbgo -> JS string bridge round-trips
// Go strings through TextDecoder("utf-8"), which mangles raw binary bytes
// (invalid UTF-8 -> U+FFFD), so the Ruby side base64-encodes the render buffer
// (ASCII, survives intact) and we decode it here into an ImageData.
//
// To avoid decoding + allocating a fresh ImageData every rAF frame (the menu is
// re-composited each frame because draw_desktop repaints the canvas background
// — the same Firefox-GC churn wasmboxBlitFromSAB guards against with its
// lastSeq cache), the caller passes a per-panel cache `key` and only sends a
// non-empty `b64` when that panel's content actually changed; an empty `b64`
// re-presents the cached ImageData for `key`. putImageData overwrites (no
// source-over), which is correct because a menu panel is opaque.
globalThis.wasmboxBlitRGBA = function (ctx, b64, w, h, dx, dy, key) {
  const cache = (globalThis.__wasmboxRGBACache ||= {});
  let slot = cache[key];
  if (b64 && b64.length) {
    globalThis.__wasmboxStats.rgbaDecodes++;
    globalThis.__wasmboxStatsBumpDecode(key);
    const bin = atob(b64);
    const n = bin.length;
    const buf = new Uint8ClampedArray(n);
    for (let i = 0; i < n; i++) buf[i] = bin.charCodeAt(i);
    slot = cache[key] = new ImageData(buf, w, h);
  } else {
    globalThis.__wasmboxStats.rgbaPresents++;
  }
  if (!slot) return;
  ctx.putImageData(slot, dx, dy);
};

// Blit a TRANSLUCENT RGBA buffer (same layout as wasmboxBlitRGBA) at (dx, dy),
// alpha-composited (source-over) onto whatever is already on the canvas —
// unlike wasmboxBlitRGBA, which putImageData-OVERWRITES (correct only for an
// opaque panel like the menu). The widgets HUD (compositor/09_hud_widgets.rb)
// renders bitmap-font glyphs (opaque A=0xFF) on a transparent (A=0) ground, so
// it must blend over the desktop rather than punch a rectangle through it.
//
// putImageData ignores the destination and cannot blend, so we stage the buffer
// in a per-key OffscreenCanvas and drawImage() it (which honours source-over).
// The staged canvas is cached per `key`: an empty `b64` re-draws the cached
// canvas, so a HUD whose text is unchanged costs one drawImage, not a decode +
// ImageData + base64 round-trip every rAF frame (the Firefox-GC hazard the SAB
// blit path documents). Same cache contract as wasmboxBlitRGBA.
globalThis.wasmboxBlitRGBAOver = function (ctx, b64, w, h, dx, dy, key) {
  const cache = (globalThis.__wasmboxRGBAOverCache ||= {});
  let slot = cache[key];
  if (b64 && b64.length) {
    globalThis.__wasmboxStats.overDecodes++;
    globalThis.__wasmboxStatsBumpDecode(key);
    const bin = atob(b64);
    const n = bin.length;
    const buf = new Uint8ClampedArray(n);
    for (let i = 0; i < n; i++) buf[i] = bin.charCodeAt(i);
    const oc = new OffscreenCanvas(w, h);
    oc.getContext("2d").putImageData(new ImageData(buf, w, h), 0, 0);
    slot = cache[key] = oc;
  } else {
    globalThis.__wasmboxStats.overPresents++;
  }
  if (!slot) return;
  ctx.drawImage(slot, dx, dy);
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

// `__wasmboxPostNotification(opts)` -- TEST HOOK. Injects a `notify` protocol
// message into the running compositor (as if a client posted it) by dispatching
// it on the compositor's Ruby event bus, exactly like wasmboxSpawnExternal. Used
// by test/probe-notifications.mjs to trigger toasts deterministically without
// spawning a client. `opts` carries the notify fields (title/body/kind/timeout/
// action_label/action/icon); `type:"notify"` is stamped on for the caller.
// Returns true once the bus was found (dispatch may retry until the compositor
// has created the bus element at boot).
globalThis.__wasmboxPostNotification = function (opts) {
  const detail = Object.assign({ type: "notify" }, opts || {});
  function dispatch() {
    const bus = fakeDocument.getElementById("__wasmbox_bus");
    if (!bus) { setTimeout(dispatch, 16); return; }
    bus.dispatchEvent(new CustomEvent("wasmbox-notify", { detail: detail }));
  }
  dispatch();
  return true;
};

// `__wasmboxTrayAdd(opts)` / `__wasmboxTrayRemove(id)` -- TEST HOOKS. Inject a
// `tray_add` / `tray_remove` protocol message into the running compositor (as if
// a client posted it) by dispatching it on the compositor's Ruby event bus,
// exactly like __wasmboxPostNotification. Used by test/probe-tray.mjs to populate
// + clear the status strip without spawning a client. `opts` carries the tray
// fields (id/glyph/icon/w/h/tooltip); the `type` is stamped on for the caller.
globalThis.__wasmboxTrayAdd = function (opts) {
  const detail = Object.assign({ type: "tray_add" }, opts || {});
  function dispatch() {
    const bus = fakeDocument.getElementById("__wasmbox_bus");
    if (!bus) { setTimeout(dispatch, 16); return; }
    bus.dispatchEvent(new CustomEvent("wasmbox-tray-add", { detail: detail }));
  }
  dispatch();
  return true;
};

globalThis.__wasmboxTrayRemove = function (id) {
  const detail = { type: "tray_remove", id: id };
  function dispatch() {
    const bus = fakeDocument.getElementById("__wasmbox_bus");
    if (!bus) { setTimeout(dispatch, 16); return; }
    bus.dispatchEvent(new CustomEvent("wasmbox-tray-remove", { detail: detail }));
  }
  dispatch();
  return true;
};

// `__wasmboxAppletToggle(kind)` / `__wasmboxAppletPlace(kind, x, y)` /
// `__wasmboxAppletRemove(kind)` -- TEST HOOKS. Drive the desktop applets
// (compositor/14_applets.rb) without the root menu, by dispatching on the Ruby
// event bus like the tray hooks. Toggle flips a tile on/off; Place ensures a tile
// is shown and moves it to (x, y) (a deterministic position for test/probe-
// applets.mjs); Remove hides it. The compositor owns applets (no client), so the
// bus handlers call the applet helpers directly. Inert when Compositor::APPLETS
// is off.
globalThis.__wasmboxAppletToggle = function (kind) {
  const detail = String(kind);
  function dispatch() {
    const bus = fakeDocument.getElementById("__wasmbox_bus");
    if (!bus) { setTimeout(dispatch, 16); return; }
    bus.dispatchEvent(new CustomEvent("wasmbox-applet-toggle", { detail: detail }));
  }
  dispatch();
  return true;
};

// `__wasmboxSetWallpaper(name)` -- TEST HOOK. Injects a `set_wallpaper` protocol
// message into the running compositor (as if a client posted it, or the user
// picked the root-menu Wallpaper submenu) by dispatching it on the compositor's
// Ruby event bus, exactly like __wasmboxTrayAdd. Used by test/probe-wallpaper.mjs
// to switch the desktop background (a gradient preset or the bundled image)
// deterministically without spawning a client. `name` is a Wallpaper::PRESETS
// name ("Grid"/"Midnight"/"Aurora"/"Photo"/...); an unknown or already-active
// name is a no-op compositor-side.
globalThis.__wasmboxSetWallpaper = function (name) {
  const detail = { type: "set_wallpaper", name: String(name) };
  function dispatch() {
    const bus = fakeDocument.getElementById("__wasmbox_bus");
    if (!bus) { setTimeout(dispatch, 16); return; }
    bus.dispatchEvent(new CustomEvent("wasmbox-set-wallpaper", { detail: detail }));
  }
  dispatch();
  return true;
};

globalThis.__wasmboxAppletPlace = function (kind, x, y) {
  const detail = { kind: String(kind), x: x | 0, y: y | 0 };
  function dispatch() {
    const bus = fakeDocument.getElementById("__wasmbox_bus");
    if (!bus) { setTimeout(dispatch, 16); return; }
    bus.dispatchEvent(new CustomEvent("wasmbox-applet-place", { detail: detail }));
  }
  dispatch();
  return true;
};

globalThis.__wasmboxAppletRemove = function (kind) {
  const detail = String(kind);
  function dispatch() {
    const bus = fakeDocument.getElementById("__wasmbox_bus");
    if (!bus) { setTimeout(dispatch, 16); return; }
    bus.dispatchEvent(new CustomEvent("wasmbox-applet-remove", { detail: detail }));
  }
  dispatch();
  return true;
};

// `__wasmboxCalendarNav(dir)` -- TEST HOOK. Steps the desktop CALENDAR applet
// one month back ("prev") or forward ("next") by dispatching on the Ruby event
// bus, exactly like the other applet hooks. Drives the SAME calendar_nav
// (compositor/14_applets.rb) the tile's header-arrow click path uses, so
// test/probe-applets.mjs can assert the grid changes on navigation without
// pixel-hunting the widget's own header arrows. Inert when Compositor::APPLETS
// is off or no calendar tile is shown.
globalThis.__wasmboxCalendarNav = function (dir) {
  const detail = String(dir);
  function dispatch() {
    const bus = fakeDocument.getElementById("__wasmbox_bus");
    if (!bus) { setTimeout(dispatch, 16); return; }
    bus.dispatchEvent(new CustomEvent("wasmbox-calendar-nav", { detail: detail }));
  }
  dispatch();
  return true;
};

// --- window snapping (test/probe-snapping.mjs) ----------------------------

// `wasmboxPublishFocusedRect(...)` -- called by the Ruby compositor once per
// frame (render, self-gated on Compositor::SNAPPING) to publish the focused
// window's live geometry + the work-area metrics. The probe reads the stashed
// object via __wasmboxFocusedRect(). x < 0 means "nothing focused". `state` is
// the snap zone the window occupies ("left"/"right"/"max"/""); `preview` is the
// drag-to-edge zone currently armed ("left"/"right"/"max"/"").
globalThis.__wasmboxFocusedRectData = null;
globalThis.wasmboxPublishFocusedRect = function (x, y, w, h, state, sw, sh, rtop, rbot, preview) {
  globalThis.__wasmboxFocusedRectData = {
    x: x | 0, y: y | 0, w: w | 0, h: h | 0,
    state: String(state || ""),
    screen_w: sw | 0, screen_h: sh | 0,
    reserved_top: rtop | 0, reserved_bottom: rbot | 0,
    preview: String(preview || ""),
  };
};
// `__wasmboxFocusedRect()` -- TEST HOOK. Returns the last-published focused
// window rect + work-area metrics (or null before the first frame).
globalThis.__wasmboxFocusedRect = function () {
  return globalThis.__wasmboxFocusedRectData;
};

// `__wasmboxSpawnWindow(title)` -- TEST HOOK. Puts a real decorated, focused,
// compositor-painted (in-process) window on the desktop via the Ruby bus, so the
// probe has a deterministic window to snap without racing a client's wasm load.
globalThis.__wasmboxSpawnWindow = function (title) {
  const detail = String(title == null ? "snap" : title);
  function dispatch() {
    const bus = fakeDocument.getElementById("__wasmbox_bus");
    if (!bus) { setTimeout(dispatch, 16); return; }
    bus.dispatchEvent(new CustomEvent("wasmbox-spawn-window", { detail: detail }));
  }
  dispatch();
  return true;
};

// `__wasmboxSpawnExternalStub(title, w, h)` -- TEST HOOK. Puts a real EXTERNAL
// (SharedArrayBuffer-backed) window on the desktop WITHOUT racing a client's wasm
// load, so the live-thumbnail probes (test/probe-alttab.mjs / probe-expose.mjs)
// have a deterministic external surface whose pixels the Alt-Tab / Exposé grab
// path (wasmboxGrabWindow) can capture. It allocates a SAB, paints a bold
// full-range gradient into it (a NON-uniform pattern so a grabbed thumbnail is
// visibly textured, unlike a flat placeholder), plus a seqlock control word set
// even (=2, "committed, not mid-paint"), and dispatches a `hello`-shaped detail
// on the Ruby bus -> spawn_external_stub (06_core.rb) registers it through the
// SAME register_external + build_image_data path a real client uses.
globalThis.__wasmboxSpawnExternalStub = function (title, w, h) {
  w = (w | 0) || 240;
  h = (h | 0) || 180;
  const sab = new SharedArrayBuffer(w * h * 4);
  const px = new Uint8ClampedArray(sab);
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const o = (y * w + x) * 4;
      px[o] = Math.round((x * 255) / (w - 1));            // R ramps across x
      px[o + 1] = Math.round((y * 255) / (h - 1));        // G ramps across y
      px[o + 2] = Math.round(((x + y) * 255) / (w + h - 2)); // B diagonal
      px[o + 3] = 255;
    }
  }
  const ctlSab = new SharedArrayBuffer(4);
  new Int32Array(ctlSab)[0] = 2; // even seq: a committed, non-torn surface
  const detail = {
    type: "hello",
    title: String(title == null ? "ext-stub" : title),
    w: w, h: h, stride: w * 4,
    sab: sab, ctl: ctlSab,
  };
  function dispatch() {
    const bus = fakeDocument.getElementById("__wasmbox_bus");
    if (!bus) { setTimeout(dispatch, 16); return; }
    bus.dispatchEvent(new CustomEvent("wasmbox-spawn-external-stub", { detail: detail }));
  }
  dispatch();
  return true;
};

// `__wasmboxSnap(dir)` -- TEST HOOK. Drives the REAL on_keydown -> snap path by
// dispatching a synthetic Super+arrow keydown onto the compositor's window
// event target. dir is "left"/"right"/"up"/"down" (or "max" == "up"). Both
// metaKey AND ctrl+alt are set so it fires whichever modifier the compositor's
// snap chord accepts.
globalThis.__wasmboxSnap = function (dir) {
  const map = { left: "ArrowLeft", right: "ArrowRight", up: "ArrowUp", max: "ArrowUp", down: "ArrowDown" };
  const key = map[String(dir)] || String(dir);
  function dispatch() {
    if (!fakeWindow) { setTimeout(dispatch, 16); return; }
    fakeWindow.dispatchEvent({
      type: "keydown", key: key, code: key,
      metaKey: true, ctrlKey: true, altKey: true, shiftKey: false,
      preventDefault() {},
    });
  }
  dispatch();
  return true;
};

// `__wasmboxAltTab(cmd)` -- TEST HOOK. Drives the REAL Alt-Tab switcher
// transitions (compositor/15_alttab.rb) via the bus, because a synthetic
// metaKey/Alt+Tab chord is unreliable headless. cmd is one of "open" /
// "advance" / "advance_reverse" / "commit" / "cancel"; the Ruby bus listener
// (06_core.rb) routes it to alttab_probe, the same code the keyboard uses.
globalThis.__wasmboxAltTab = function (cmd) {
  const detail = String(cmd == null ? "open" : cmd);
  function dispatch() {
    const bus = fakeDocument.getElementById("__wasmbox_bus");
    if (!bus) { setTimeout(dispatch, 16); return; }
    bus.dispatchEvent(new CustomEvent("wasmbox-alttab", { detail: detail }));
  }
  dispatch();
  return true;
};

// `wasmboxPublishAltTab(...)` -- called by the Ruby compositor once per frame
// (draw_alttab, 15_alttab.rb) to publish the switcher's live overlay geometry +
// selection. active is 0/1; count is the candidate tile count; index is the
// selected tile; (px,py,pw,ph) is the centered panel rect; (sx,sy,sw,sh) is the
// SELECTED window's body rect (so a probe can confirm a commit focuses exactly
// that window against __wasmboxFocusedRect). The probe reads the stash via
// __wasmboxAltTabState().
// sel_live is 1 when the SELECTED tile shows a LIVE grabbed framebuffer (an
// external SAB window) rather than a flat placeholder (in-process / dom), and
// (tx,ty,tw,th) is that tile's on-screen rect on the composited canvas, so a
// probe can sample it via __wasmboxReadRegion and assert real (textured) pixels.
globalThis.__wasmboxAltTabData = { active: 0 };
globalThis.wasmboxPublishAltTab = function (active, count, index, px, py, pw, ph, sx, sy, sw, sh, selLive, tx, ty, tw, th) {
  globalThis.__wasmboxAltTabData = {
    active: active | 0,
    count: count | 0,
    index: index | 0,
    panel_x: px | 0, panel_y: py | 0, panel_w: pw | 0, panel_h: ph | 0,
    sel_x: sx | 0, sel_y: sy | 0, sel_w: sw | 0, sel_h: sh | 0,
    sel_live: selLive | 0,
    tile_x: tx | 0, tile_y: ty | 0, tile_w: tw | 0, tile_h: th | 0,
  };
};
// `__wasmboxAltTabState()` -- TEST HOOK. Returns the last-published switcher
// state (or { active: 0 } before the first frame).
globalThis.__wasmboxAltTabState = function () {
  return globalThis.__wasmboxAltTabData;
};

// `__wasmboxSpotlight(cmd)` -- TEST HOOK. Drives the REAL Spotlight launcher
// transitions (compositor/16_spotlight.rb) via the bus, because a synthetic
// ⌘/Ctrl+Space chord + per-key typing is unreliable headless. cmd is one of
// "open" / "query:<text>" / "type:<chars>" / "backspace" / "down" / "up" /
// "commit" / "cancel"; the Ruby bus listener (06_core.rb) routes it to
// spotlight_probe, the same code the keyboard uses.
globalThis.__wasmboxSpotlight = function (cmd) {
  const detail = String(cmd == null ? "open" : cmd);
  function dispatch() {
    const bus = fakeDocument.getElementById("__wasmbox_bus");
    if (!bus) { setTimeout(dispatch, 16); return; }
    bus.dispatchEvent(new CustomEvent("wasmbox-spotlight", { detail: detail }));
  }
  dispatch();
  return true;
};

// `wasmboxPublishSpotlight(...)` -- called by the Ruby compositor once per frame
// (draw_spotlight, 16_spotlight.rb) to publish the launcher's live overlay
// geometry + query + selection. active is 0/1; count is the filtered result
// count; index is the highlighted row; (px,py,pw,ph) is the centered panel rect;
// query is the live search text; selLabel is the highlighted app's label;
// launched is the last-launched app id and launchSeq a monotonic launch counter
// (so a probe can confirm Enter launched the highlighted app and Escape did
// not). The probe reads the stash via __wasmboxSpotlightState().
globalThis.__wasmboxSpotlightData = { active: 0 };
globalThis.wasmboxPublishSpotlight = function (active, count, index, px, py, pw, ph, query, selLabel, launched, launchSeq) {
  globalThis.__wasmboxSpotlightData = {
    active: active | 0,
    count: count | 0,
    index: index | 0,
    panel_x: px | 0, panel_y: py | 0, panel_w: pw | 0, panel_h: ph | 0,
    query: String(query == null ? "" : query),
    sel_label: String(selLabel == null ? "" : selLabel),
    launched: String(launched == null ? "" : launched),
    launch_seq: launchSeq | 0,
  };
};
// `__wasmboxSpotlightState()` -- TEST HOOK. Returns the last-published launcher
// state (or { active: 0 } before the first frame).
globalThis.__wasmboxSpotlightState = function () {
  return globalThis.__wasmboxSpotlightData;
};

// `__wasmboxExpose(cmd)` -- TEST HOOK. Drives the REAL Exposé / "show all
// windows" transitions (compositor/17_expose.rb) via the bus, because a
// synthetic F3 chord + pixel-perfect thumbnail reads are unreliable headless.
// cmd is one of "toggle" / "open" / "left" / "right" / "up" / "down" /
// "select:<i>" / "click:<x>,<y>" / "commit" / "cancel"; the Ruby bus listener
// (06_core.rb) routes it to expose_probe, the same code the keyboard + mouse use.
globalThis.__wasmboxExpose = function (cmd) {
  const detail = String(cmd == null ? "toggle" : cmd);
  function dispatch() {
    const bus = fakeDocument.getElementById("__wasmbox_bus");
    if (!bus) { setTimeout(dispatch, 16); return; }
    bus.dispatchEvent(new CustomEvent("wasmbox-expose", { detail: detail }));
  }
  dispatch();
  return true;
};

// `wasmboxPublishExpose(...)` -- called by the Ruby compositor once per frame
// (draw_expose, 17_expose.rb) to publish the spread's live grid geometry +
// selection. active is 0/1; count is the spread tile count; cols/rows are the
// grid dimensions; index is the selected tile; (sx,sy,sw,sh) is the SELECTED
// window's body rect (so a probe can confirm a commit/click focuses exactly that
// window against __wasmboxFocusedRect); tilesJson is a JSON "[[x,y,w,h],...]" of
// every tile rectangle (so a probe can assert the tiles are non-overlapping +
// within the work area). The probe reads the stash via __wasmboxExposeState().
// sel_live is 1 when the SELECTED tile shows a LIVE grabbed framebuffer (an
// external SAB window) rather than a flat placeholder (in-process / dom); the
// selected tile's on-screen rect is tiles[index], which a probe can sample via
// __wasmboxReadRegion and assert real (textured) pixels.
globalThis.__wasmboxExposeData = { active: 0 };
globalThis.wasmboxPublishExpose = function (active, count, cols, rows, index, sx, sy, sw, sh, tilesJson, selLive) {
  let tiles = [];
  try { tiles = JSON.parse(String(tilesJson || "[]")); } catch (_) { tiles = []; }
  globalThis.__wasmboxExposeData = {
    active: active | 0,
    count: count | 0,
    cols: cols | 0,
    rows: rows | 0,
    index: index | 0,
    sel_x: sx | 0, sel_y: sy | 0, sel_w: sw | 0, sel_h: sh | 0,
    sel_live: selLive | 0,
    tiles: tiles,
  };
};
// `__wasmboxExposeState()` -- TEST HOOK. Returns the last-published spread state
// (or { active: 0 } before the first frame).
globalThis.__wasmboxExposeState = function () {
  return globalThis.__wasmboxExposeData;
};

// `wasmboxClock()` -- wall-clock + calendar math for the clock/calendar applets
// (compositor/14_applets.rb reads the fields off the returned object). Kept on
// the JS side so the Ruby applet render never does Gregorian date arithmetic.
// dow / first_dow are 0=Sunday; month is 1-12; days_in_month is the last day of
// the current month.
globalThis.wasmboxClock = function () {
  const d = new Date();
  const y = d.getFullYear();
  const mo = d.getMonth();
  return {
    h: d.getHours(),
    m: d.getMinutes(),
    s: d.getSeconds(),
    year: y,
    month: mo + 1,
    day: d.getDate(),
    dow: d.getDay(),
    first_dow: new Date(y, mo, 1).getDay(),
    days_in_month: new Date(y, mo + 1, 0).getDate(),
  };
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
    // Keyboard modifiers — forwarded so the compositor can read the Shift+Tab
    // and Super/⌘+arrow (snapping) chords off a relayed keydown, not just the
    // synthetic test-hook events.
    metaKey: m.metaKey ?? false,
    ctrlKey: m.ctrlKey ?? false,
    altKey: m.altKey ?? false,
    shiftKey: m.shiftKey ?? false,
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

// Fetch `url` while reporting byte-level download progress to the main thread,
// then compile + instantiate it. Posts:
//   {type:"boot", stage:"download", loaded, total}  as bytes stream in
//   {type:"boot", stage:"instantiate"}              before WebAssembly.instantiate
// If Content-Length is absent (total=0) the main thread shows an indeterminate
// bar; we still stream + report loaded so the labels/animation advance. Falls
// back to a buffered arrayBuffer() read when the body is not a ReadableStream.
async function instantiateWithProgress(url, importObject) {
  const resp = await fetch(url);
  if (!resp.ok) throw new Error("fetch " + url + " -> HTTP " + resp.status);
  const total = parseInt(resp.headers.get("Content-Length") || "0", 10) || 0;

  let bytes;
  if (resp.body && typeof resp.body.getReader === "function") {
    const reader = resp.body.getReader();
    const chunks = [];
    let loaded = 0;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      chunks.push(value);
      loaded += value.length;
      self.postMessage({ type: "boot", stage: "download", loaded: loaded, total: total });
    }
    bytes = new Uint8Array(loaded);
    let off = 0;
    for (const c of chunks) { bytes.set(c, off); off += c.length; }
  } else {
    // No streaming body available -- one buffered read, single progress tick.
    bytes = new Uint8Array(await resp.arrayBuffer());
    self.postMessage({ type: "boot", stage: "download", loaded: bytes.length, total: total || bytes.length });
  }

  // Boot stage 3/5: bytes are in, compiling + instantiating the module.
  self.postMessage({ type: "boot", stage: "instantiate" });
  return WebAssembly.instantiate(bytes, importObject);
}

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
    // Stream the wasm so the boot splash shows real download progress. We read
    // Content-Length + the ReadableStream to compute bytes-downloaded / total
    // and post staged {type:"boot"} messages to the main thread. This replaces
    // instantiateStreaming (which gives no byte-level progress).
    const wasm = await instantiateWithProgress("./wasmbox.wasm", go.importObject);
    // Boot stage 4/5: the Ruby runtime + compositor are about to boot inside
    // main(). Posted BEFORE go.run because go.run runs main() synchronously.
    self.postMessage({ type: "boot", stage: "runtime" });
    go.run(wasm.instance);
    // Compositor.attach_to_canvas + comp.start ran synchronously inside main()
    // before we get here; signal main that the worker is live. (The splash is
    // torn down on the first PRESENTED frame -- see the rAF wrapper above.)
    self.postMessage({ type: B.C2M_READY });
    // Auto-spawn the same demo clients as the old index.html did, so a page
    // load still ends with a populated desktop.
    globalThis.wasmboxSpawnExternal("clients/hello/worker.js");
    // Quake is intentionally NOT auto-spawned: its wasm is ~12 MB and would
    // load on every page visit. It stays launchable on demand from the root
    // menu / dock (LAUNCHABLE "quake" -> clients/quake/worker.js); the worker
    // surfaces an error window if the wasm is missing, so no boot gate needed.
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
