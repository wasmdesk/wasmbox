// hello-world wasmbox external client (worker entry).
//
// The compositor spawns this script as a dedicated Worker. We detect the
// spawn path -- static (script URL on the host origin) vs OCI (script URL is
// a blob: URL backed by bytes pulled from an OCI registry) -- and load the
// SDK + wasm_exec.js accordingly. On either path we then instantiate
// hello.wasm, which paints into the SDK's SAB and calls client.commit().
//
// OCI spawn path: a blob: worker cannot resolve the relative paths the
// static path uses (`../sdk/sdk.js`, `../../wasm_exec.js`, `./hello.wasm`).
// The SDK is fetched from the host origin via self.location.origin (which is
// the page that built the blob URL, even though the worker's script URL is
// blob:); wasm_exec.js + hello.wasm are pulled from the OCI assets envelope
// the compositor delivers as message #1 (see wasmboxSpawnFromOCI in
// compositor.worker.js).

"use strict";

const isOCI = self.location.protocol === "blob:";
if (isOCI) {
  importScripts(self.location.origin + "/clients/sdk/sdk.js");
} else {
  importScripts("../sdk/sdk.js");
  importScripts("../../wasm_exec.js");
}

const client = new WasmboxClient({ title: "hello (wasm)", w: 200, h: 150 });

// Expose the client to the Go program through globalThis so it can grab the
// SAB view + commit() + onInput() through syscall/js. Done BEFORE starting
// the wasm so the Go side never sees an undefined wasmboxClient.
self.wasmboxClient = client;

client.start().then(async () => {
  // On the OCI path, await the assets envelope the compositor queued before
  // the port handoff. importScripts wasm_exec.js from that blob URL and
  // instantiate hello.wasm from the envelope's wasm_url. fallbackMs is
  // generous (2 s) -- the envelope arrives in microseconds, but Playwright
  // headless runs can spike under load.
  const assets = isOCI
    ? await WasmboxClient.bootFromOCIAssets({ fallbackMs: 2000 })
    : null;
  if (isOCI) {
    if (!assets) throw new Error("hello: spawned from blob: URL but no OCI assets envelope");
    importScripts(assets.wasm_exec_url);
  }
  const go = new Go();
  const wasmURL = assets ? assets.wasm_url : "./hello.wasm";
  const wasm = await WebAssembly.instantiateStreaming(
    fetch(wasmURL), go.importObject);
  // go.run() does not return until main() exits; the hello program parks on
  // `select {}` to keep its handlers live, so we don't await it.
  go.run(wasm.instance);
});
