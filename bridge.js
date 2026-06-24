// SPDX-License-Identifier: BSD-3-Clause
//
// wasmbox main <-> compositor-worker bridge protocol (step C).
//
// In step C the Ruby compositor and its embedded wasm runtime live in a
// dedicated Web Worker (compositor.worker.js). The main thread keeps only the
// <canvas>, the status overlay, the DOM event listeners and a thin relay loop;
// everything else moved off the main thread. This file declares the message
// shapes the two ends exchange so both sides import the same constants.
//
//   main -> compositor
//     { type: "boot",       canvas: OffscreenCanvas, w, h }
//     { type: "resize",     w, h }
//     { type: "dom_event",  target: "window"|"canvas",
//                            kind: "keydown"|"keyup"|"mousedown"|"mousemove"|
//                                  "mouseup"|"contextmenu",
//                            key, code, offsetX, offsetY, button, ...
//                            }
//     { type: "spawn_external", url }                  — relay of legacy
//                                                       globalThis.wasmboxSpawnExternal
//
//   compositor -> main
//     { type: "ready" }                                — boot finished, Ruby running
//     { type: "error", message }                       — Ruby raised at boot
//     { type: "console", level: "log"|"error", text }  — optional surface
//     { type: "storage_set", key, value }              — write-through localStorage
//     { type: "storage_remove", key }
//
// The compositor worker spawns external client workers directly via
// `new Worker(url)` -- nested workers work in every modern browser and inherit
// cross-origin isolation. Main does NOT relay client traffic: each client
// worker talks to the compositor worker via worker-to-worker postMessage,
// exactly as it talked to the main thread in step B. That keeps the wire
// protocol (docs/protocol.md) unchanged.
//
// The constants are exported through both ESM (for clean imports) and
// globalThis (so plain `<script>` and `importScripts` can pick them up).

"use strict";

const WASMBOX_BRIDGE = Object.freeze({
  // main -> compositor
  M2C_BOOT:              "boot",
  M2C_RESIZE:            "resize",
  M2C_DOM_EVENT:         "dom_event",
  M2C_SPAWN_EXTERNAL:    "spawn_external",

  // compositor -> main
  C2M_READY:             "ready",
  C2M_ERROR:             "error",
  C2M_CONSOLE:           "console",
  C2M_STORAGE_SET:       "storage_set",
  C2M_STORAGE_REMOVE:    "storage_remove",

  // dom_event.target values
  TARGET_WINDOW: "window",
  TARGET_CANVAS: "canvas",
});

// Expose for plain-script consumers (importScripts inside the worker).
if (typeof globalThis !== "undefined") {
  globalThis.WASMBOX_BRIDGE = WASMBOX_BRIDGE;
}
