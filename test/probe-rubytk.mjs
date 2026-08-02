// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the Ruby Tip Calculator client (clients/rubytk)
// -- the one wasmbox client whose UI + logic are authored entirely in Ruby via
// `require "widgets"` (the go-ruby-widgets binding), running on the embedded
// go-embedded-ruby interpreter inside its own worker's wasm.
//
// It proves the Ruby-widgets path end-to-end AND that it is interactive:
//   1. boots the compositor headless,
//   2. spawns the rubytk window,
//   3. confirms the Ruby scene rendered real content into the SAB (the
//      composited window body is non-black -- Widgets.render -> base64 ->
//      __wbPresent -> SAB blit worked),
//   4. reads each rate button's surface rect from the rubytk worker global
//      (__rubytkGeometry, published by the Ruby scene) and clicks the 10% then
//      the 20% button,
//   5. asserts the composited window body HASH changes on each click and that
//      the two rates produce distinct frames -- i.e. a click routed through
//      Widgets.dispatch mutated Ruby state which re-rendered.
//
// Region pixels are read off the compositor's worker-owned OffscreenCanvas via
// the __wasmboxReadRegion / __wasmboxGrabRegion test hooks (a viewport
// screenshot cannot see worker OffscreenCanvas frames). A frame of the window
// is saved to /tmp/rubytk-*.png for visual review.

import { createServer } from "node:http";
import { readFile, writeFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 20000;

// A point inside the window body for origin calibration (see below). The
// window is cascade-placed near the top-left, 300x320, so (300, 300) lands in
// its body for every reasonable cascade slot.
const CAL_X = 300;
const CAL_Y = 300;

const MIME = {
  ".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8", ".wasm": "application/wasm",
  ".css": "text/css; charset=utf-8", ".json": "application/json; charset=utf-8",
  ".rb": "text/plain; charset=utf-8",
};

function startServer() {
  const server = createServer(async (req, res) => {
    try {
      const urlPath = decodeURIComponent((req.url || "/").split("?")[0]);
      let rel = normalize(urlPath).replace(/^(\.\.[/\\])+/, "");
      if (rel === "/" || rel === "") rel = "/index.html";
      const file = join(ROOT, rel);
      if (!file.startsWith(ROOT)) { res.writeHead(403).end("forbidden"); return; }
      const body = await readFile(file);
      res.setHeader("Content-Type", MIME[extname(file)] || "application/octet-stream");
      res.setHeader("Cross-Origin-Opener-Policy", "same-origin");
      res.setHeader("Cross-Origin-Embedder-Policy", "require-corp");
      res.writeHead(200).end(body);
    } catch { res.writeHead(404).end("not found"); }
  });
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => resolve({ server, base: `http://127.0.0.1:${server.address().port}` }));
  });
}

function fail(msg) { console.error(`FAIL: ${msg}`); process.exitCode = 1; }
function ok(msg) { console.log(`ok  ${msg}`); }

// Find the compositor worker (the one exposing __wasmboxReadRegion).
async function compositorWorker(page) {
  for (const w of page.workers()) {
    try {
      if (await w.evaluate(() => typeof globalThis.__wasmboxReadRegion === "function")) return w;
    } catch (_) { /* worker navigating */ }
  }
  return null;
}

// Read __rubytkGeometry off the rubytk worker global.
async function rubytkGeometry(page) {
  for (const w of page.workers()) {
    try {
      const g = await w.evaluate(() => globalThis.__rubytkGeometry || null);
      if (g && g.rate20 && g.rate10) return g;
    } catch (_) { /* not the rubytk worker */ }
  }
  return null;
}

const readRegion = (cw, r) => cw.evaluate(({ x, y, w, h }) => globalThis.__wasmboxReadRegion(x, y, w, h), r);

async function grabPNG(cw, r, path) {
  const dataURL = await cw.evaluate(({ x, y, w, h }) => globalThis.__wasmboxGrabRegion(x, y, w, h), r);
  if (!dataURL) return;
  await writeFile(path, Buffer.from(dataURL.split(",")[1], "base64"));
}

const { server, base } = await startServer();
const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  const errs = [];
  page.on("pageerror", (e) => errs.push(String(e)));

  await page.goto(`${base}/index.html`, { waitUntil: "load" });
  await page.waitForFunction(() => {
    if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
    return globalThis.wasmboxReady === true;
  }, { timeout: BOOT_TIMEOUT_MS });
  ok("compositor booted");

  const cw = await compositorWorker(page);
  if (!cw) { fail("no compositor worker (missing __wasmboxReadRegion)"); throw new Error("stop"); }

  await page.evaluate(() => globalThis.wasmboxSpawnExternal("clients/rubytk/worker.js"));

  // Wait until the Ruby scene has published its geometry (i.e. app.rb ran
  // require "widgets", built the tree, rendered once and called __wbSetGeom).
  let geom = null;
  for (let i = 0; i < 100 && !geom; i++) {
    geom = await rubytkGeometry(page);
    if (!geom) await page.waitForTimeout(200);
  }
  if (!geom) { fail("rubytk never published __rubytkGeometry (Ruby scene did not boot)"); throw new Error("stop"); }
  ok(`Ruby scene booted; rate20 rect=${JSON.stringify(geom.rate20)}`);

  // Calibrate the window's screen origin: click a point inside the body, then
  // read the surface-local coordinates the Ruby scene echoed back. The origin
  // is (screen - local), which is robust to however the compositor cascade-
  // placed the window.
  await page.mouse.click(CAL_X, CAL_Y);
  await page.waitForTimeout(300);
  const cal = (await rubytkGeometry(page)).lastclick;
  if (!cal) { fail("calibration click was not received by the Ruby scene (input not forwarded)"); throw new Error("stop"); }
  const originX = CAL_X - cal.x;
  const originY = CAL_Y - cal.y;
  ok(`input reaches the Ruby scene; window origin = (${originX}, ${originY})`);

  const bodyRegion = { x: originX, y: originY, w: 300, h: 320 };
  const centre = (rect) => ({ x: originX + rect.x + Math.floor(rect.w / 2), y: originY + rect.y + Math.floor(rect.h / 2) });

  const initial = await readRegion(cw, bodyRegion);
  await grabPNG(cw, bodyRegion, "/tmp/rubytk-initial.png");
  if (!initial || initial.nonblackPct < 20) {
    fail(`window body looks blank (nonblackPct=${initial ? initial.nonblackPct : "null"}); Ruby render did not reach the SAB`);
  } else {
    ok(`Ruby widget tree rendered into the SAB (nonblackPct=${initial.nonblackPct})`);
  }

  // Click the 10% button, then the 20% button; each must change the frame,
  // and 10% vs 20% must differ (state -> pixels through Widgets.dispatch).
  const c10 = centre(geom.rate10);
  await page.mouse.click(c10.x, c10.y);
  await page.waitForTimeout(400);
  const after10 = await readRegion(cw, bodyRegion);
  await grabPNG(cw, bodyRegion, "/tmp/rubytk-rate10.png");
  if (after10.hash === initial.hash) fail("clicking 10% did not change the frame (input->state->render broken)");
  else ok("clicking 10% re-rendered the Ruby scene");

  const c20 = centre(geom.rate20);
  await page.mouse.click(c20.x, c20.y);
  await page.waitForTimeout(400);
  const after20 = await readRegion(cw, bodyRegion);
  await grabPNG(cw, bodyRegion, "/tmp/rubytk-rate20.png");
  if (after20.hash === after10.hash) fail("clicking 20% did not change the frame after 10%");
  else ok("clicking 20% produced a distinct frame from 10%");

  // Only rubytk-relevant page errors are fatal; unrelated demo-app 404s
  // (e.g. dock/hello OCI assets not built in this environment) are noise.
  const relevant = errs.filter((e) => /rubytk/i.test(e));
  if (relevant.length) fail(`rubytk page errors: ${relevant.join(" | ")}`);
  else ok(`no rubytk page errors${errs.length ? ` (${errs.length} unrelated ignored)` : ""}`);

  if (process.exitCode === 1) console.log("FAIL ❌ rubytk probe");
  else console.log("PASS ✅ rubytk: Ruby require \"widgets\" client boots, renders + responds to clicks");
} finally {
  await browser.close();
  server.close();
}
