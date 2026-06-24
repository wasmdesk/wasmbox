// wasmdock external client (worker entry).
//
// The compositor spawns this script as a dedicated Worker. We detect the
// spawn path (static vs OCI = blob:) and load the SDK + wasm_exec.js + the
// dock wasm accordingly. On the OCI path the assets are blob URLs delivered
// by the compositor; the SDK is loaded from the host origin (the blob:
// worker preserves self.location.origin for the page that minted the blob).

"use strict";

const isOCI = self.location.protocol === "blob:";
if (isOCI) {
  importScripts(self.location.origin + "/clients/sdk/sdk.js");
} else {
  importScripts("./sdk.js");
  importScripts("../../wasm_exec.js");
}

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
  const assets = isOCI
    ? await WasmboxClient.bootFromOCIAssets({ fallbackMs: 2000 })
    : null;
  if (isOCI) {
    if (!assets) throw new Error("dock: spawned from blob: URL but no OCI assets envelope");
    importScripts(assets.wasm_exec_url);
  }
  const go = new Go();
  const wasmURL = assets ? assets.wasm_url : "./dock.wasm";
  const wasm = await WebAssembly.instantiateStreaming(
    fetch(wasmURL), go.importObject);
  // go.run() does not return until main() exits; the dock parks on `select {}`
  // to keep its handlers live, so we don't await it.
  go.run(wasm.instance);
});
