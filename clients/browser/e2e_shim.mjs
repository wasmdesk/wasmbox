// Node host for the browser client's wasm end-to-end test. It provides a
// minimal wasmboxClient shim (a pixel buffer + beginFrame/commit/onInput), loads
// the Go wasm_exec.js glue, instantiates browser.wasm pointed at the fake
// browserproxy server, and resolves the moment the client paints the red frame
// the server streams back — proof the whole client (gRPC over WebSocket → scene
// → commit) runs in the browser target.
//
// Env: URL (ws:// fake server), WASM (path to browser.wasm),
//      WASM_EXEC (path to the toolchain's wasm_exec.js).
import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";

const { URL: wsURL, WASM, WASM_EXEC } = process.env;
if (!wsURL || !WASM || !WASM_EXEC) {
  console.error("WASM_FAIL missing env (URL/WASM/WASM_EXEC)");
  process.exit(2);
}
if (typeof globalThis.WebSocket === "undefined") {
  console.error("WASM_FAIL node has no global WebSocket (need node >= 22)");
  process.exit(2);
}

const W = 128, H = 200;              // toolbar (46px) + content
const pixels = new Uint8Array(4 * W * H);

let painted = false;
// hasRed scans the pixel buffer for an opaque-red pixel — the frame the fake
// server streams. Its presence proves the client decoded and blitted the frame.
function hasRed() {
  for (let i = 0; i < pixels.length; i += 4) {
    if (pixels[i] > 200 && pixels[i + 1] < 80 && pixels[i + 2] < 80) return true;
  }
  return false;
}

globalThis.BROWSERPROXY_URL = wsURL;
globalThis.wasmboxClient = {
  w: W,
  h: H,
  pixels,
  beginFrame() {},
  commit() { if (hasRed()) painted = true; },
  onInput() {},
};

await import(pathToFileURL(WASM_EXEC).href); // defines globalThis.Go

const go = new globalThis.Go();
const { instance } = await WebAssembly.instantiate(await readFile(WASM), go.importObject);
go.run(instance); // main() blocks in select{}; do NOT await — poll for the paint

const deadline = Date.now() + 20000;
while (!painted && Date.now() < deadline) {
  await new Promise((r) => setTimeout(r, 50));
}

if (painted) {
  console.log("WASM_OK client painted the streamed frame");
  process.exit(0);
}
console.error("WASM_FAIL client never painted the streamed frame");
process.exit(1);
