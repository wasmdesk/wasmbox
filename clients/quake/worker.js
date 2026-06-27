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
self.addEventListener("message", function bootPortHandler(ev) {
  const m = ev.data;
  if (!m || m.type !== "__wasmbox_port" || !m.port) return;
  self.removeEventListener("message", bootPortHandler);
  _activePort = m.port;
  // Re-dispatch every message the port delivers as a MessageEvent on `self`
  // so the Go side's `self.addEventListener("message", ...)` listener sees
  // it. The MessageEvent constructor copies `data` verbatim across the SAB
  // + js.Value bridge without serializing.
  _activePort.addEventListener("message", function (e) {
    self.dispatchEvent(new MessageEvent("message", { data: e.data }));
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
