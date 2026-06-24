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

"use strict";

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
