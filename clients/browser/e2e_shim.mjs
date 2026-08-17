// Node host for the browser client's wasm end-to-end test. It provides a
// minimal wasmboxClient shim (a pixel buffer + beginFrame/commit/onInput), loads
// the Go wasm_exec.js glue, instantiates browser.wasm pointed at the fake
// browserproxy server, and drives the client through its streaming lifecycle —
// proof the whole client (gRPC over WebSocket → scene → commit) runs in the
// browser target. It asserts, in order:
//
//   0. the loading SKELETON (skeletonGrey #d6d6da) paints in the content area
//      before the first streamed frame (the server holds that frame back a beat);
//   1. the client connects, resizes, and paints the streamed RED frame — which
//      CLEARS the skeleton from the content area;
//   2. an injected address-bar navigation (click the bar, type, press Enter)
//      makes the client send a Navigate, and the fresh GREEN frame the server
//      streams back paints into the content area.
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

// A realistic browser size: the toolbar's fixed controls (back/fwd/+ buttons,
// pads and gaps ≈ 148px) must fit so the flexible address bar keeps a clickable
// width. The address bar then spans x∈[92, W-56]; its centre is the click point.
const toolbarH = 46;
const W = 480, H = 320;
const addrX = ((12 + 30 + 6 + 30 + 14) + (W - (14 + 30 + 12))) >> 1; // 258
const addrY = 23; // toolbar button-row centre
const pixels = new Uint8Array(4 * W * H);

// Colour predicates, scanned over the CONTENT area only (y ≥ toolbarH) so the
// toolbar chrome never trips them.
const isRed = (r, g, b) => r > 200 && g < 80 && b < 80;
const isGreen = (r, g, b) => g > 200 && r < 80 && b < 80;
const isSkel = (r, g, b) => Math.abs(r - 0xd6) < 10 && Math.abs(g - 0xd6) < 10 && Math.abs(b - 0xda) < 10;
function contentHas(pred) {
  for (let y = toolbarH; y < H; y++) {
    for (let x = 0; x < W; x++) {
      const i = 4 * (y * W + x);
      if (pred(pixels[i], pixels[i + 1], pixels[i + 2])) return true;
    }
  }
  return false;
}

let inputCb = null;
let sawSkeleton = false, skeletonCleared = false, sawRed = false, sawGreen = false;

globalThis.BROWSERPROXY_URL = wsURL;
globalThis.wasmboxClient = {
  w: W,
  h: H,
  pixels,
  beginFrame() {},
  commit() {
    const skel = contentHas(isSkel), red = contentHas(isRed), green = contentHas(isGreen);
    if (skel && !sawRed) sawSkeleton = true; // skeleton before the first frame
    if (red) {
      sawRed = true;
      if (!skel) skeletonCleared = true; // the frame replaced the skeleton
    }
    if (green) sawGreen = true;
  },
  onInput(cb) { inputCb = cb; }, // capture the client's input handler to drive it
};

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(pred, ms, failMsg) {
  const deadline = Date.now() + ms;
  while (!pred() && Date.now() < deadline) await sleep(50);
  if (!pred()) {
    console.error("WASM_FAIL " + failMsg);
    process.exit(1);
  }
}

await import(pathToFileURL(WASM_EXEC).href); // defines globalThis.Go

const go = new globalThis.Go();
const { instance } = await WebAssembly.instantiate(await readFile(WASM), go.importObject);
go.run(instance); // main() blocks in select{}; do NOT await — drive it below

// Phase 1: the client connects, resizes, and paints the streamed RED frame.
// The skeleton is observed on the way (server holds the first frame back).
await until(() => sawRed, 30000, "client never painted the streamed frame");

// Phase 2: inject an address-bar navigation and expect a fresh GREEN frame.
if (typeof inputCb !== "function") {
  console.error("WASM_FAIL client never registered its input handler");
  process.exit(1);
}
inputCb({ kind: "mousedown", x: addrX, y: addrY }); // focus the address bar
await sleep(20);
for (const ch of "abc") { inputCb({ kind: "keydown", key: ch }); await sleep(5); }
inputCb({ kind: "keydown", key: "Enter" }); // navigate → server streams green
await until(() => sawGreen, 15000, "navigation did not stream a new frame");

if (!sawSkeleton) {
  console.error("WASM_FAIL no loading skeleton before the first frame");
  process.exit(1);
}
if (!skeletonCleared) {
  console.error("WASM_FAIL skeleton did not clear after the frame");
  process.exit(1);
}

console.log("WASM_OK skeleton → streamed red frame → navigation streamed a green frame");
process.exit(0);
