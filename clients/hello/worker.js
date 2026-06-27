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

// --- popup demo (xdg-popup / subsurface) -----------------------------------
// A click on the hello window opens a small child popup anchored under the
// pointer -- a second surface on THIS worker, made possible by the multi-
// surface SDK. The compositor anchors it parent-relative, draws no decoration,
// and grab-dismisses it on a click outside; the `closed` that arrives then
// clears our handle, so the next click opens a fresh one.
let demoPopup = null;
client.onInput((ev) => {
  if (ev.kind !== "mousedown" || demoPopup) return;
  const p = client.openPopup({ title: "hello menu", w: 132, h: 96, rel_x: ev.x | 0, rel_y: ev.y | 0 });
  demoPopup = p;
  p.onClosed(() => { if (demoPopup === p) demoPopup = null; });
  // Repaint the menu with item `sel` highlighted (a blue accent). sel = -1 is
  // "no selection". Keyboard arrows move it; Escape dismissal is compositor-side.
  let hi = -1;
  const paint = (sel) => {
    p.fillRect(236, 236, 240, 255);                                  // light menu body
    p.fillRect(180, 182, 190, 255, { x: 0, y: 0, w: p.w, h: 1 });    // top hairline
    p.fillRect(180, 182, 190, 255, { x: 0, y: p.h - 1, w: p.w, h: 1 }); // bottom hairline
    for (let i = 0; i < 3; i++) {                                    // three menu items
      const on = i === sel;
      p.fillRect(on ? 53 : 210, on ? 132 : 214, on ? 228 : 222, 255,
        { x: 6, y: 8 + i * 28, w: p.w - 12, h: 20 });
    }
    p.commit();
  };
  p.onWelcome(() => paint(hi));
  // Nested popup: clicking the TOP item opens a submenu anchored to this menu's
  // right edge -- a popup whose parent is itself a popup. The compositor's
  // layered grab dismisses the submenu when the pointer goes back to the parent
  // menu (a click elsewhere in it) without closing the parent. Arrow keys move
  // the highlight: the compositor routes keys to the active popup (keyboard
  // grab), and Escape dismisses it.
  let sub = null;
  p.onInput((sev) => {
    if (sev.kind === "keydown") {
      if (sev.key === "ArrowDown") hi = (hi + 1) % 3;
      else if (sev.key === "ArrowUp") hi = (hi + 2) % 3;
      else return;
      paint(hi);
      return;
    }
    if (sev.kind !== "mousedown" || sub || (sev.y | 0) >= 28) return;
    const s = p.openPopup({ title: "submenu", w: 120, h: 72, rel_x: p.w - 6, rel_y: (sev.y | 0) - 4 });
    sub = s;
    s.onClosed(() => { if (sub === s) sub = null; });
    s.onWelcome(() => {
      s.fillRect(224, 232, 240, 255);                                // light submenu body
      for (let i = 0; i < 2; i++) s.fillRect(198, 210, 224, 255, { x: 6, y: 8 + i * 28, w: s.w - 12, h: 20 });
      s.commit();
    });
  });
});

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
  // bootWasm replaces the bare WebAssembly.instantiateStreaming call: it
  // paints an Adwaita-style progress bar onto the SAB while hello.wasm is
  // downloading + instantiating, then resolves to the instance. hello.wasm
  // overwrites the bar with its gradient scene on first commit.
  const instance = await WasmboxClient.bootWasm(wasmURL, go.importObject, {
    bg:    [250, 250, 250],
    track: [218, 220, 224],
    fill:  [ 53, 132, 228],
  });
  // go.run() does not return until main() exits; the hello program parks on
  // `select {}` to keep its handlers live, so we don't await it.
  go.run(instance);
});
