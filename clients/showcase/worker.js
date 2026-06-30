// Showcase wasmbox external client (worker entry).
//
// Mirrors clients/hello/worker.js: detect static vs OCI spawn, load SDK +
// wasm_exec.js, instantiate showcase.wasm. The wasm program (Go, in
// main.go) handles every input event + paints the toolkit composition.

"use strict";

const isOCI = self.location.protocol === "blob:";
if (isOCI) {
  importScripts(self.location.origin + "/clients/sdk/sdk.js");
} else {
  importScripts("../sdk/sdk.js");
  importScripts("../../wasm_exec.js");
}

const client = new WasmboxClient({ title: "toolkit showcase", w: 480, h: 360 });

self.wasmboxClient = client;

client.start().then(async () => {
  const assets = isOCI
    ? await WasmboxClient.bootFromOCIAssets({ fallbackMs: 2000 })
    : null;
  if (isOCI) {
    if (!assets) throw new Error("showcase: spawned from blob: URL but no OCI assets envelope");
    importScripts(assets.wasm_exec_url);
  }
  const go = new Go();
  const wasmURL = assets ? assets.wasm_url : "./showcase.wasm";
  const instance = await WasmboxClient.bootWasm(wasmURL, go.importObject, {
    bg:    [250, 250, 250],
    track: [218, 220, 224],
    fill:  [ 53, 132, 228],
  });
  go.run(instance);
});
