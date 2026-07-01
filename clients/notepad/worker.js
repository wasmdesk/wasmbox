// Notepad wasmbox external client (worker entry). Same shape as
// clients/calculator/worker.js — static + OCI spawn detection, boot
// via WasmboxClient.bootWasm. The wasm program (Go) handles input +
// paints the toolkit composition.

"use strict";

const isOCI = self.location.protocol === "blob:";
if (isOCI) {
  importScripts(self.location.origin + "/clients/sdk/sdk.js");
} else {
  importScripts("../sdk/sdk.js");
  importScripts("../../wasm_exec.js");
}

const client = new WasmboxClient({ title: "Notepad", w: 600, h: 400 });
self.wasmboxClient = client;

client.start().then(async () => {
  const assets = isOCI
    ? await WasmboxClient.bootFromOCIAssets({ fallbackMs: 2000 })
    : null;
  if (isOCI) {
    if (!assets) throw new Error("notepad: no OCI assets envelope");
    importScripts(assets.wasm_exec_url);
  }
  const go = new Go();
  const wasmURL = assets ? assets.wasm_url : "./notepad.wasm";
  const instance = await WasmboxClient.bootWasm(wasmURL, go.importObject, {
    bg:    [250, 250, 250],
    track: [218, 220, 224],
    fill:  [ 53, 132, 228],
  });
  go.run(instance);
});
