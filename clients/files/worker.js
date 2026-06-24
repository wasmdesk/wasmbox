// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Files placeholder client (worker entry).
//
// Spawned by the compositor when the dock posts {type:"launch", app:"files"}.
// Loads the SDK + Go's wasm_exec.js, instantiates files.wasm, which paints a
// "Files" placeholder surface and parks waiting for input.

"use strict";

importScripts("../sdk/sdk.js");
importScripts("../../wasm_exec.js");

const client = new WasmboxClient({ title: "Files", w: 360, h: 220 });

// Expose the client to the Go program through globalThis so it can grab the
// SAB view + commit() + onInput() through syscall/js. Done BEFORE starting
// the wasm so the Go side never sees an undefined wasmboxClient.
self.wasmboxClient = client;

client.start().then(async () => {
  const go = new Go();
  const wasm = await WebAssembly.instantiateStreaming(
    fetch("./files.wasm"), go.importObject);
  // go.run() does not return until main() exits; the files client parks on
  // `select {}` to keep its handlers live, so we don't await it.
  go.run(wasm.instance);
});
