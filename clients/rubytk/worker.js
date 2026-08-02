// Tip Calculator (Ruby) wasmbox external client (worker entry). Mirrors
// clients/calculator/worker.js -- the same static + OCI spawn detection and the
// same bootWasm handshake -- but the wasm it loads (rubytk.wasm) is a thin
// go-embedded-ruby host shell, NOT a Go toolkit scene. The actual UI + logic
// live in Ruby (internal/scene/app.rb, baked into the wasm) and reach this
// worker through two small host-shell hooks installed below:
//
//   __wbPresent(b64)  the Ruby scene renders its widget tree with
//                     Widgets.render (RGBA bytes), base64-encodes it (raw bytes
//                     cannot cross syscall/js as a String -- UTF-8 re-encoding
//                     would corrupt any byte >= 0x80) and hands it here; we
//                     decode straight into the surface SAB under the SDK seqlock
//                     and commit full-surface damage.
//   __wbSetGeom(...)  the Ruby scene publishes each rate button's surface
//                     rectangle so the headless probe can click a precise
//                     control without hardcoding the (Ruby-side) layout.
//
// Compositor input events are re-emitted as a "wbinput" DOM event on the worker
// global; the Ruby scene subscribes with the JS bridge's JS::Ref#on (its only
// cross-language callback primitive).

"use strict";

const isOCI = self.location.protocol === "blob:";
if (isOCI) {
  importScripts(self.location.origin + "/clients/sdk/sdk.js");
} else {
  importScripts("../sdk/sdk.js");
  importScripts("../../wasm_exec.js");
}

const client = new WasmboxClient({ title: "Tip Calculator (Ruby)", w: 300, h: 320 });
self.wasmboxClient = client;

// Blit one base64-encoded RGBA frame from Widgets.render into the SAB. Opens the
// seqlock window (beginFrame) before the byte copy so the compositor never
// blits a half-decoded frame, then commits the whole surface.
self.__wbPresent = (b64) => {
  const bin = atob(b64);
  const px = client.pixels;
  const n = Math.min(bin.length, px.length);
  client.beginFrame();
  for (let i = 0; i < n; i++) px[i] = bin.charCodeAt(i);
  client.commit({ x: 0, y: 0, w: client.w, h: client.h });
};

// Publish a widget's surface rectangle for the probe (surface-local coords).
self.__rubytkGeometry = {};
self.__wbSetGeom = (label, x, y, w, h) => {
  self.__rubytkGeometry[label] = { x: x | 0, y: y | 0, w: w | 0, h: h | 0 };
};

// Re-emit each compositor input event so the Ruby scene (JS::Ref#on) receives it.
client.onInput((ev) => {
  self.dispatchEvent(new CustomEvent("wbinput", { detail: ev }));
});

client.start().then(async () => {
  const assets = isOCI
    ? await WasmboxClient.bootFromOCIAssets({ fallbackMs: 2000 })
    : null;
  if (isOCI) {
    if (!assets) throw new Error("rubytk: no OCI assets envelope");
    importScripts(assets.wasm_exec_url);
  }
  const go = new Go();
  const wasmURL = assets ? assets.wasm_url : "./rubytk.wasm";
  const instance = await WasmboxClient.bootWasm(wasmURL, go.importObject, {
    bg:    [250, 250, 250],
    track: [218, 220, 224],
    fill:  [ 53, 132, 228],
  });
  go.run(instance);
});
