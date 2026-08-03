// Settings wasmbox external client (worker entry). Mirrors
// clients/calculator/worker.js -- same static + OCI spawn detection, same
// bootWasm handshake. The wasm program (Go, in main.go) paints the WhiteSur
// preferences panel + handles input.

"use strict";

const isOCI = self.location.protocol === "blob:";
if (isOCI) {
  importScripts(self.location.origin + "/clients/sdk/sdk.js");
} else {
  importScripts("../sdk/sdk.js");
  importScripts("../../wasm_exec.js");
}

const client = new WasmboxClient({ title: "Settings", w: 640, h: 460 });
self.wasmboxClient = client;

client.start().then(async () => {
  // DE-spine demo: publish a system-tray status icon for Settings. A left-click
  // fires a "tray_clicked" onInput event; a right-click fires "tray_menu". The
  // compositor drops it automatically when this window closes.
  client.trayAdd({ id: "settings.app", glyph: "settings", tooltip: "Settings" });
  client.onInput((ev) => {
    if (ev && (ev.kind === "tray_clicked" || ev.kind === "tray_menu")) {
      console.log("settings: tray event", ev.kind, ev.tray_id);
    }
  });
  const assets = isOCI
    ? await WasmboxClient.bootFromOCIAssets({ fallbackMs: 2000 })
    : null;
  if (isOCI) {
    if (!assets) throw new Error("settings: no OCI assets envelope");
    importScripts(assets.wasm_exec_url);
  }
  const go = new Go();
  const wasmURL = assets ? assets.wasm_url : "./settings.wasm";
  const instance = await WasmboxClient.bootWasm(wasmURL, go.importObject, {
    bg:    [245, 245, 245],
    track: [211, 211, 211],
    fill:  [  8,  96, 242],
  });
  go.run(instance);
});
