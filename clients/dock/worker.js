// SPDX-License-Identifier: BSD-3-Clause
//
// wasmdock external client (worker entry) — Fluxbox-style bottom toolbar.
//
// The compositor spawns this script as a dedicated Worker. We detect the
// spawn path (static vs OCI = blob:) and load the SDK + wasm_exec.js + the
// dock wasm accordingly. On the OCI path the assets are blob URLs delivered
// by the compositor; the SDK is loaded from the host origin (the blob:
// worker preserves self.location.origin for the page that minted the blob).
//
// The dock surface is 1280 x 28 pixels — a full-width, 28-px-tall bottom
// toolbar in the Fluxbox tradition. The compositor anchors any "panel" role
// to the bottom-center of the canvas (see compositor.rb `anchor_panel`), so
// a 1280-wide surface flushes to the bottom edge and overflows symmetrically
// on smaller viewports. The Go side does all the painting; this shell just
// kicks the wasm and pumps a clock-tick event every 30 seconds.

"use strict";

const isOCI = self.location.protocol === "blob:";
if (isOCI) {
  importScripts(self.location.origin + "/clients/sdk/sdk.js");
} else {
  importScripts("./sdk.js");
  importScripts("../../wasm_exec.js");
}

// The dock asks for the "panel" role (anchored, always-on-top, undecorated)
// at the documented Fluxbox toolbar geometry: 1280 px wide. The compositor
// enforces a minimum surface height (Theme::MIN_H = 60 in compositor.rb),
// so we request that as our surface height — the toolbar's pure-Go scene
// scales every section to whatever h the SDK grants, so the visual layout
// holds whether the panel is 28 or 60 px tall.
const client = new WasmboxClient({
  title: "wasmdock",
  role: "panel",
  w: 1280,
  h: 60,
});

// Expose the client to the Go program through globalThis so it can grab the
// SAB view + commit() + onInput() through syscall/js. Done BEFORE starting
// the wasm so the Go side never sees an undefined wasmboxClient.
self.wasmboxClient = client;

// formatClock returns the current local time as "HH:MM" with zero-padding.
function formatClock() {
  const d = new Date();
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  return hh + ":" + mm;
}

// tick injects a synthetic input event of kind "tick" carrying the current
// clock string. The dock's Go main reads ev.clock and re-renders. We feed
// the SDK's private dispatcher directly because the public `onInput` only
// surfaces events flowing in from the compositor — the clock is a worker-
// local timer with no compositor counterpart.
function tick() {
  if (!client || !client._onMessage) return;
  try {
    client._onMessage({ data: { type: "input", event: { kind: "tick", clock: formatClock() } } });
  } catch (e) {
    // Don't let a transient SDK glitch kill the timer; the next tick will
    // try again.
    console.error("wasmdock tick:", e);
  }
}

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
  // bootWasm paints an Adwaita-style loading progress bar onto the dock SAB
  // during the wasm fetch + boot, then resolves the instance. The dock
  // overwrites the bar with its toolbar render on first commit.
  const instance = await WasmboxClient.bootWasm(wasmURL, go.importObject, {
    bg:    [200, 200, 200],
    track: [144, 144, 144],
    fill:  [ 74,  74,  74],
  });
  // go.run() does not return until main() exits; the dock parks on
  // `select {}` to keep its handlers live, so we don't await it.
  go.run(instance);
  // Boot the clock immediately, then once per 30 s. 30 s is granular enough
  // for a wall-clock that only shows HH:MM.
  tick();
  setInterval(tick, 30 * 1000);
});
