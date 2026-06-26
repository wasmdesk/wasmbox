// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Files placeholder client (worker entry).
//
// Spawned by the compositor when the dock posts {type:"launch", app:"files"}.
// Loads the SDK + Go's wasm_exec.js, instantiates files.wasm, which paints a
// "Files" placeholder surface and parks waiting for input. Supports both the
// static-path spawn and the OCI-stream spawn (blob: worker URL).

"use strict";

const isOCI = self.location.protocol === "blob:";
if (isOCI) {
  importScripts(self.location.origin + "/clients/sdk/sdk.js");
} else {
  importScripts("../sdk/sdk.js");
  importScripts("../../wasm_exec.js");
}

// 720x440 matches the Finder-inspired layout in internal/scene/render.go:
// sidebar (140) + 4-column right pane (toolbar 40, header 24, rows 28).
// Keep these in sync if the layout constants in render.go change.
const client = new WasmboxClient({ title: "Files", w: 720, h: 440 });

// Expose the client to the Go program through globalThis so it can grab the
// SAB view + commit() + onInput() through syscall/js. Done BEFORE starting
// the wasm so the Go side never sees an undefined wasmboxClient.
self.wasmboxClient = client;

client.start().then(async () => {
  const assets = isOCI
    ? await WasmboxClient.bootFromOCIAssets({ fallbackMs: 2000 })
    : null;
  if (isOCI) {
    if (!assets) throw new Error("files: spawned from blob: URL but no OCI assets envelope");
    importScripts(assets.wasm_exec_url);
  }
  const go = new Go();
  const wasmURL = assets ? assets.wasm_url : "./files.wasm";
  const wasm = await WebAssembly.instantiateStreaming(
    fetch(wasmURL), go.importObject);
  // go.run() does not return until main() exits; the files client parks on
  // `select {}` to keep its handlers live, so we don't await it.
  go.run(wasm.instance);
});
