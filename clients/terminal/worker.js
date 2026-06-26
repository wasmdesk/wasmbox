// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Terminal placeholder client (worker entry).
//
// Spawned by the compositor when the dock posts {type:"launch", app:"terminal"}.
// Loads the SDK + Go's wasm_exec.js, instantiates terminal.wasm, which paints
// a "Terminal" placeholder surface and parks waiting for input. Supports both
// the static-path spawn (current dock click) and the OCI-stream spawn (the
// blob:-worker path used by demo-wasmbox-ociapps).

"use strict";

const isOCI = self.location.protocol === "blob:";
if (isOCI) {
  importScripts(self.location.origin + "/clients/sdk/sdk.js");
} else {
  importScripts("../sdk/sdk.js");
  importScripts("../../wasm_exec.js");
}

const client = new WasmboxClient({ title: "Terminal", w: 640, h: 400 });

// Expose the client to the Go program through globalThis so it can grab the
// SAB view + commit() + onInput() through syscall/js. Done BEFORE starting
// the wasm so the Go side never sees an undefined wasmboxClient.
self.wasmboxClient = client;

client.start().then(async () => {
  const assets = isOCI
    ? await WasmboxClient.bootFromOCIAssets({ fallbackMs: 2000 })
    : null;
  if (isOCI) {
    if (!assets) throw new Error("terminal: spawned from blob: URL but no OCI assets envelope");
    importScripts(assets.wasm_exec_url);
  }
  const go = new Go();
  const wasmURL = assets ? assets.wasm_url : "./terminal.wasm";
  const wasm = await WebAssembly.instantiateStreaming(
    fetch(wasmURL), go.importObject);
  // go.run() does not return until main() exits; the terminal client parks
  // on `select {}` to keep its handlers live, so we don't await it.
  go.run(wasm.instance);
});
