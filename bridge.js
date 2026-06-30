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
// cross-origin isolation. Main does NOT relay client traffic.
//
// Step C.1 (MessageChannel-direct, Wayland-style): immediately after
// `new Worker(url)`, the compositor creates a MessageChannel and posts
//   `{ type: "__wasmbox_port", port: <port2> }`  (transferred)
// to the freshly-spawned worker, retaining port1 for itself. The client SDK
// (clients/sdk/sdk.js) listens for that one-shot message at module load and
// swaps its outbound channel to port2. All subsequent client <-> compositor
// traffic flows over the dedicated channel; the worker's own `self.onmessage`
// stays quiet, so each client gets a private wire instead of sharing the
// implicit nested-worker channel.
//
// The wire protocol (docs/protocol.md) is unchanged -- the only difference is
// the EventTarget on which the messages flow.
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
  M2C_SPAWN_FROM_OCI:    "spawn_from_oci",
  // Spawn a "dom-window" (chrome painted by the compositor, body
  // hosted as a real <iframe> over the canvas). Used for embedding
  // browser-only apps (code-server, jupyter, etc.) -- see
  // C2M_IFRAME_* for the compositor->main side of the protocol.
  //   { type:"spawn_dom_window", url, w, h, title? }
  M2C_SPAWN_DOM_WINDOW:  "spawn_dom_window",

  // compositor -> main
  C2M_READY:             "ready",
  C2M_ERROR:             "error",
  C2M_CONSOLE:           "console",
  C2M_STORAGE_SET:       "storage_set",
  C2M_STORAGE_REMOVE:    "storage_remove",
  // dom-window iframe overlays (step E). The compositor owns the
  // decoration + z-order; the body of a dom window is a real <iframe>
  // sitting OVER the canvas at the window's body-rect. Used to embed
  // browser-only apps (code-server / vscodium, jupyter, etc.) inside
  // a wasmbox window while keeping the desktop's WM semantics.
  //   { type:"iframe_attach", window_id, url, x, y, w, h }
  //   { type:"iframe_move",   window_id, x, y, w, h }
  //   { type:"iframe_detach", window_id }
  C2M_IFRAME_ATTACH:     "iframe_attach",
  C2M_IFRAME_MOVE:       "iframe_move",
  C2M_IFRAME_DETACH:     "iframe_detach",

  // dom_event.target values
  TARGET_WINDOW: "window",
  TARGET_CANVAS: "canvas",

  // compositor -> external-client one-shot port handoff (step C.1). Not part
  // of the public docs/protocol.md surface: it is purely the transport-setup
  // message that hands a freshly-spawned client its private MessagePort.
  COMP_TO_CLIENT_PORT: "__wasmbox_port",

  // compositor -> external-client OCI asset handoff. Used only by the OCI
  // launch path (wasmboxSpawnFromOCI): the compositor pulls the app's blobs
  // out of an OCI registry, wraps each file in a Blob URL, and tells the
  // freshly-spawned worker which URLs to load wasm_exec.js + <app>.wasm
  // from. Static-path workers do not see this message + keep using their
  // hard-coded relative paths -- backward compatible.
  COMP_TO_CLIENT_ASSETS: "__wasmbox_assets",
});

// Expose for plain-script consumers (importScripts inside the worker).
if (typeof globalThis !== "undefined") {
  globalThis.WASMBOX_BRIDGE = WASMBOX_BRIDGE;
}
