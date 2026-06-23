// wasmdock external client (worker entry).
//
// The compositor spawns this script as a dedicated Worker. We load the SDK,
// load Go's wasm_exec.js shim, then instantiate dock.wasm — which paints the
// dock into the SDK's SAB and calls client.commit() per frame, and posts a
// {type:"launch", app} message when an icon is clicked.

"use strict";

importScripts("./sdk.js");
importScripts("./wasm_exec.js");

// The dock is a bottom-anchored panel spanning a wide, short surface. It asks
// for the "panel" role (anchored, always-on-top, undecorated); the compositor
// may ignore the role and treat it as an ordinary window — the dock still
// renders correctly.
const client = new WasmboxClient({
  title: "wasmdock",
  role: "panel",
  w: 480,
  h: 120,
});

// Expose the client to the Go program through globalThis so it can grab the
// SAB view + commit() + onInput() through syscall/js. Done BEFORE starting the
// wasm so the Go side never sees an undefined wasmboxClient.
self.wasmboxClient = client;

client.start().then(async () => {
  const go = new Go();
  const wasm = await WebAssembly.instantiateStreaming(
    fetch("./dock.wasm"), go.importObject);
  // go.run() does not return until main() exits; the dock parks on `select {}`
  // to keep its handlers live, so we don't await it.
  go.run(wasm.instance);
});
