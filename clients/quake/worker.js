// quake (Go-native pure-wasm) wasmbox external client (worker entry).
//
// The compositor spawns this script as a dedicated Worker. We load Go's
// wasm_exec.js shim then instantiate quake.wasm. The wasm side
// (backend/wasmbox.NewClient) drives the wasmbox step-B protocol
// directly: it allocates the SAB, posts the `hello`, waits for the
// `welcome`, installs its own `message` listener, and posts
// `{type:"commit"}` per frame. So unlike clients/hello/worker.js we
// don't load clients/sdk/sdk.js here — Go IS the SDK.

"use strict";

importScripts("../../wasm_exec.js");

(async () => {
  const go = new Go();
  const wasm = await WebAssembly.instantiateStreaming(
    fetch("./quake.wasm"), go.importObject);
  // go.run() does not return until main() exits; the engine parks on
  // its run loop so we never await it.
  go.run(wasm.instance);
})();
