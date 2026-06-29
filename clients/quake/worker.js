// quake (Go-native pure-wasm) wasmbox external client (worker entry).
//
// The compositor spawns this script as a dedicated Worker. We load Go's
// wasm_exec.js shim then instantiate quake.wasm. The wasm side
// (backend/wasmbox.NewClient) drives the wasmbox step-B protocol
// directly: it allocates the SAB, posts the `hello`, waits for the
// `welcome`, installs its own `message` listener, and posts
// `{type:"commit"}` per frame. So unlike clients/hello/worker.js we
// don't load clients/sdk/sdk.js here — Go IS the SDK.
//
// OCI spawn path: when self.location.protocol === "blob:", the worker was
// spawned by wasmboxSpawnFromOCI -- wasm_exec.js + quake.wasm are blob URLs
// delivered via the compositor's __wasmbox_assets envelope (message #1).
// Quake never loads the SDK so it cannot use WasmboxClient.bootFromOCIAssets;
// instead it listens for the envelope directly. The Go side's own port
// listener still works because it is installed before the next event-loop
// tick (postMessage delivery is microtask-deferred).
//
// Port handoff bridge (step C.1, MessageChannel-direct):
//   The compositor sends a one-shot `{type:"__wasmbox_port", port}` as the
//   first message + listens for application traffic on port1, NOT on the
//   implicit nested-worker channel. The Go side (backend/wasmbox) uses
//   `self.postMessage` + `self.addEventListener("message", ...)`, which
//   route to/from the implicit channel. Without a bridge the hello falls
//   into the void + NewClient parks forever. We install a tiny port-swap
//   shim BEFORE Go runs: when the port arrives, we re-point
//   `self.postMessage` at port.postMessage and re-dispatch every port
//   message as a `MessageEvent` on `self`, so the Go listener sees it.
//   This is what clients/sdk/sdk.js does for JS clients; we open-code it
//   here because Quake does NOT load the SDK.

"use strict";

// Saved-size restore (2026-06-29). The Go side allocates the SAB at
// hello-time at (w, h) AND the compositor will accept those as the
// granted surface dims. If we hardcode 320x240 the engine renders at
// 320x240 and the compositor scale-fits up to whatever window-rect the
// saved layout dictates -- wasted per-pixel work in the software
// rasterizer. Instead: ask the main thread for the saved window size
// (the compositor persists window geometry to localStorage under the
// "wasmbox.layout" key, but `localStorage` is window-only -- Workers
// see `self.localStorage === undefined` -- so we cannot read it here
// directly). The main-thread agent in boot-config.js answers a tiny
// BroadcastChannel request with the latest persisted layout, the
// worker parses out the Quake window's record + uses (w, h) as the
// SAB + render dims. Falls back to 800x600 (a 4:3 modern-desktop
// default that scales cleanly from Quake's 4:3 internal aspect) when
// no record exists or the channel does not answer within the small
// timeout. We clamp to a sane min/max so a malformed layout record or
// a user who shrank the window past usability cannot trigger a
// 1-pixel-wide SAB or a 16384x16384 allocation.
const QUAKE_TITLE = "quake (wasm)";
const QUAKE_FB_DEFAULT_W = 800;
const QUAKE_FB_DEFAULT_H = 600;
const QUAKE_FB_MIN_W = 320;
const QUAKE_FB_MIN_H = 240;
const QUAKE_FB_MAX_W = 1920;
const QUAKE_FB_MAX_H = 1080;
// Time we'll wait for boot-config.js's main-thread agent to answer
// the BroadcastChannel layout query before falling back to defaults.
// On a reload the main thread spends a few hundred ms instantiating
// wasmbox.wasm (the compositor) before its message loop services
// our query; 2 s is the safe upper bound that still keeps boot
// snappy when storage is empty.
const QUAKE_FB_QUERY_TIMEOUT_MS = 2000;
const QUAKE_FB_CHANNEL = "wasmbox.quake.fb";

function parseSavedSize(raw, title) {
  if (raw == null) return null;
  // Last matching record wins (the compositor appends most-recent at
  // the end of the stack, so the last "title<TAB>..." line for `title`
  // is the freshest geometry).
  let hit = null;
  for (const line of String(raw).split("\n")) {
    const parts = line.split("\t");
    if (parts.length < 5) continue;
    if (parts[0] !== title) continue;
    const w = parseInt(parts[3], 10);
    const h = parseInt(parts[4], 10);
    if (!Number.isFinite(w) || !Number.isFinite(h)) continue;
    hit = { w: w, h: h };
  }
  return hit;
}

function clamp(v, lo, hi) {
  if (!Number.isFinite(v)) return lo;
  if (v < lo) return lo;
  if (v > hi) return hi;
  return v | 0;
}

// fetchSavedLayout asks the main-thread agent (boot-config.js) for
// the persisted layout via BroadcastChannel. Returns the raw layout
// string (null when storage was empty / the agent declined to
// answer / BroadcastChannel is unsupported / the response did not
// arrive within the timeout).
function fetchSavedLayout() {
  return new Promise((resolve) => {
    let bc;
    try { bc = new BroadcastChannel(QUAKE_FB_CHANNEL); }
    catch (_) { resolve(null); return; }
    const timer = setTimeout(() => {
      try { bc.close(); } catch (_) {}
      resolve(null);
    }, QUAKE_FB_QUERY_TIMEOUT_MS);
    bc.addEventListener("message", (ev) => {
      const m = ev.data;
      if (!m || m.type !== "wasmbox.quake.fb/response") return;
      clearTimeout(timer);
      try { bc.close(); } catch (_) {}
      resolve(typeof m.layout === "string" ? m.layout : null);
    });
    try { bc.postMessage({ type: "wasmbox.quake.fb/query" }); }
    catch (_) { clearTimeout(timer); try { bc.close(); } catch (_) {} resolve(null); }
  });
}

async function chooseFB() {
  const raw = await fetchSavedLayout();
  const saved = parseSavedSize(raw, QUAKE_TITLE);
  let w = saved ? saved.w : QUAKE_FB_DEFAULT_W;
  let h = saved ? saved.h : QUAKE_FB_DEFAULT_H;
  w = clamp(w, QUAKE_FB_MIN_W, QUAKE_FB_MAX_W);
  h = clamp(h, QUAKE_FB_MIN_H, QUAKE_FB_MAX_H);
  return { w: w, h: h, source: saved ? "saved" : "default" };
}

// Stash the original postMessage + override it with a buffer-aware shim. As
// soon as the port arrives the shim flushes the buffer + forwards all future
// sends through the port. Installed at module load so the buffer is in place
// before quake.wasm starts.
const _origPostMessage = self.postMessage.bind(self);
let _activePort = null;
const _pending = [];
self.postMessage = function (msg, transferOrOpts) {
  if (_activePort) {
    if (Array.isArray(transferOrOpts) && transferOrOpts.length)
      _activePort.postMessage(msg, transferOrOpts);
    else
      _activePort.postMessage(msg);
    return;
  }
  _pending.push([msg, transferOrOpts]);
};
// Quake's intrinsic frame aspect (id Tech 1, 1996): 320x240 = 4:3. The
// compositor honours @lock_aspect during interactive resize so the user
// dragging the bottom-right grip cannot stretch Quake into 16:9 letterbox.
// Sent as a `set_lock_aspect` wire message AFTER the welcome lands -- the
// SDK is off-limits + we don't have a clean way to add lock_aspect to the
// hello payload Go composes, but the set_lock_aspect arm is purely
// additive and needs only window_id + ratio, both of which we learn from
// snooping the welcome inbound below.
const QUAKE_LOCK_ASPECT = 4.0 / 3.0;

self.addEventListener("message", function bootPortHandler(ev) {
  const m = ev.data;
  if (!m || m.type !== "__wasmbox_port" || !m.port) return;
  self.removeEventListener("message", bootPortHandler);
  _activePort = m.port;
  // Re-dispatch every message the port delivers as a MessageEvent on `self`
  // so the Go side's `self.addEventListener("message", ...)` listener sees
  // it. The MessageEvent constructor copies `data` verbatim across the SAB
  // + js.Value bridge without serializing.
  //
  // We ALSO snoop the inbound stream for the first `welcome` message: it
  // carries the compositor-assigned window_id, which we need to post the
  // additive `set_lock_aspect` declaration back. Once sent (one-shot, no
  // retries needed -- the port is ordered) we leave the re-dispatcher to
  // pass every subsequent message through to Go untouched.
  let _lockSent = false;
  _activePort.addEventListener("message", function (e) {
    const data = e.data;
    if (!_lockSent && data && data.type === "welcome" &&
        typeof data.window_id === "number") {
      _lockSent = true;
      try {
        _activePort.postMessage({
          type: "set_lock_aspect",
          window_id: data.window_id,
          ratio: QUAKE_LOCK_ASPECT,
        });
      } catch (err) {
        // Non-fatal: the lock is a UX nicety; Quake still runs without it.
        try { console.warn("quake worker: set_lock_aspect post failed: " + err); }
        catch (_) {}
      }
    }
    self.dispatchEvent(new MessageEvent("message", { data: data }));
  });
  // MessagePort needs an explicit start() when consumed via addEventListener.
  try { _activePort.start(); } catch (_) {}
  // Flush whatever Go (or the OCI assets-envelope handler below) buffered.
  while (_pending.length) {
    const [msg, t] = _pending.shift();
    if (Array.isArray(t) && t.length) _activePort.postMessage(msg, t);
    else                              _activePort.postMessage(msg);
  }
});

const isOCI = self.location.protocol === "blob:";

(async () => {
  // Pick the SAB / framebuffer dims FIRST + publish them on globals
  // before instantiating quake.wasm. The Go side reads __quake_fb_w
  // / __quake_fb_h at NewClient time; absent globals (older Go build)
  // fall back to the Go-side defaults (also 800x600).
  const fb = await chooseFB();
  self.__quake_fb_w = fb.w;
  self.__quake_fb_h = fb.h;
  self.__quake_fb_source = fb.source;
  try { console.log("quake worker: fb=" + fb.w + "x" + fb.h + " source=" + fb.source); }
  catch (_) {}

  let wasmExecURL = "../../wasm_exec.js";
  let wasmURL = "./quake.wasm";
  if (isOCI) {
    const assets = await new Promise((resolve, reject) => {
      const timer = setTimeout(
        () => reject(new Error("quake: no OCI assets envelope in 2000 ms")),
        2000);
      self.addEventListener("message", function once(ev) {
        const m = ev.data;
        if (!m || m.type !== "__wasmbox_assets") return;
        self.removeEventListener("message", once);
        clearTimeout(timer);
        resolve(m);
      });
    });
    wasmExecURL = assets.wasm_exec_url;
    wasmURL = assets.wasm_url;
  }
  importScripts(wasmExecURL);
  const go = new Go();
  const wasm = await WebAssembly.instantiateStreaming(
    fetch(wasmURL), go.importObject);
  // go.run() does not return until main() exits; the engine parks on
  // its run loop so we never await it.
  go.run(wasm.instance);
})();
