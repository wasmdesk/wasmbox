// doom (go-doom, pure-Go Doom engine) wasmbox external client -- worker entry.
//
// The compositor spawns this script as a dedicated Worker. Like the Quake
// client (clients/quake/worker.js), the Go side drives the wasmbox protocol
// itself (go-doom/engine backend/wasmbox): it allocates the SharedArrayBuffer
// surface + a seqlock control word, posts `hello` (with lock_aspect 4:3),
// waits for `welcome`, and posts `commit` per frame. So this worker does not
// load clients/sdk/sdk.js; it only does the two things Go cannot:
//
//  1. Port bridge (step C.1). The compositor's first message is
//     {type:"__wasmbox_port", port}; application traffic flows over that
//     port, but Go uses self.postMessage + self.addEventListener("message").
//     We re-point self.postMessage at the port (buffering anything posted
//     before it arrives) and re-dispatch every port message on `self`.
//
//  2. Asset base. The engine streams the Freedoom IWAD from the same-origin
//     /v2/doom-assets OCI mirror served beside the page. Its base is this
//     worker's URL minus "clients/doom/worker.js" -- which keeps a GitHub
//     Pages /<project>/ prefix -- published as self.__doomOCIBase. The Go
//     side checks it is on this origin before fetching anything.
//
// The WAD is Freedoom: Phase 1 (BSD-3-Clause), pinned by sha256 in the wasm.

"use strict";

const _pending = [];
let _activePort = null;
self.postMessage = function (msg, transfer) {
  if (_activePort) {
    if (Array.isArray(transfer) && transfer.length) _activePort.postMessage(msg, transfer);
    else _activePort.postMessage(msg);
    return;
  }
  _pending.push([msg, transfer]);
};

self.addEventListener("message", function bootPort(ev) {
  const m = ev.data;
  if (!m || m.type !== "__wasmbox_port" || !m.port) return;
  self.removeEventListener("message", bootPort);
  _activePort = m.port;
  _activePort.addEventListener("message", (e) => {
    self.dispatchEvent(new MessageEvent("message", { data: e.data }));
  });
  try { _activePort.start(); } catch (_) {}
  while (_pending.length) {
    const [msg, t] = _pending.shift();
    if (Array.isArray(t) && t.length) _activePort.postMessage(msg, t);
    else _activePort.postMessage(msg);
  }
});

const isOCI = self.location.protocol === "blob:";

(async () => {
  try {
    const href = String((self.location && self.location.href) || "");
    if (href && !href.startsWith("blob:")) {
      const base = href.replace(/clients\/doom\/worker\.js.*$/, "").replace(/\/+$/, "");
      if (base) self.__doomOCIBase = base;
    }
  } catch (_) {}

  let wasmExecURL = "../../wasm_exec.js";
  let wasmURL = "./doom.wasm";
  if (isOCI) {
    const assets = await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error("doom: no OCI assets envelope in 2000 ms")), 2000);
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
  const wasm = await WebAssembly.instantiateStreaming(fetch(wasmURL), go.importObject);
  // go.run resolves only when main() returns; the engine loop never does.
  go.run(wasm.instance);
})().catch((err) => {
  console.log("DOOM: FAIL worker: " + (err && err.message ? err.message : err));
});
