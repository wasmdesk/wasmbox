// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// VS Code-styled code editor client (worker entry).
//
// Spawned by the compositor when the dock posts {type:"launch", app:"code"} or
// when wasmboxSpawnExternal("clients/code/worker.js") is called directly.
// Loads the SDK + Go's wasm_exec.js, instantiates code.wasm, which paints a
// VS Code Dark+-styled editor surface and parks waiting for input. Supports
// both the static-path spawn and the OCI-stream spawn (blob: worker URL).

"use strict";

const isOCI = self.location.protocol === "blob:";
if (isOCI) {
  importScripts(self.location.origin + "/clients/sdk/sdk.js");
} else {
  importScripts("../sdk/sdk.js");
  importScripts("../../wasm_exec.js");
}

// 900x540 matches the layout constants in internal/scene/render.go: sidebar
// (200) + gutter (50) + editor pane fills the remaining width; tab strip (28)
// at the top, status bar (24) at the bottom. Keep these in sync if the
// layout constants in render.go change.
const client = new WasmboxClient({ title: "Code", w: 900, h: 540 });

// Expose the client to the Go program through globalThis so it can grab the
// SAB view + commit() + onInput() through syscall/js. Done BEFORE starting
// the wasm so the Go side never sees an undefined wasmboxClient.
self.wasmboxClient = client;

client.start().then(async () => {
  // Pre-paint via the SDK's bootWasm loader (VS Code Dark+ palette): it owns
  // the surface from start() resolution through wasm boot, painting a
  // progress bar that grows during fetch + snaps to 100% before
  // WebAssembly.instantiate(). The SAB sat at init-zeros = transparent
  // before; now the user sees a dark editor-frame window with the
  // signature blue (#007ACC) loader for the 2-3 s the multi-MB wasm takes
  // to come in. code.wasm overwrites the bar with its first render once
  // go.run() reaches paintScene().
  const assets = isOCI
    ? await WasmboxClient.bootFromOCIAssets({ fallbackMs: 2000 })
    : null;
  if (isOCI) {
    if (!assets) throw new Error("code: spawned from blob: URL but no OCI assets envelope");
    importScripts(assets.wasm_exec_url);
  }
  const go = new Go();
  const wasmURL = assets ? assets.wasm_url : "./code.wasm";
  const instance = await WasmboxClient.bootWasm(wasmURL, go.importObject, {
    // VS Code Dark+: editor BG, dim track, signature blue accent.
    bg:    [0x1E, 0x1E, 0x1E],
    track: [0x4F, 0x4F, 0x4F],
    fill:  [0x00, 0x7A, 0xCC],
  });
  // go.run() does not return until main() exits; the code client parks on
  // `select {}` to keep its handlers live, so we don't await it.
  go.run(instance);
});
