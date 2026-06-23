// hello-world wasmbox external client (worker entry).
//
// The compositor spawns this script as a dedicated Worker. We load the SDK,
// load Go's wasm_exec.js shim, then instantiate hello.wasm — which paints
// into the SDK's SAB and calls client.commit() per frame.

"use strict";

importScripts("../sdk/sdk.js");
importScripts("../../wasm_exec.js");

const client = new WasmboxClient({ title: "hello (wasm)", w: 200, h: 150 });

// Expose the client to the Go program through globalThis so it can grab the
// SAB view + commit() + onInput() through syscall/js. Done BEFORE starting
// the wasm so the Go side never sees an undefined wasmboxClient.
self.wasmboxClient = client;

client.start().then(async () => {
  const go = new Go();
  const wasm = await WebAssembly.instantiateStreaming(
    fetch("./hello.wasm"), go.importObject);
  // go.run() does not return until main() exits; the hello program parks on
  // `select {}` to keep its handlers live, so we don't await it.
  go.run(wasm.instance);
});
