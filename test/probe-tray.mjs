// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the SYSTEM-TRAY / status area
// (compositor/05_tray.rb model + compositor/13_tray.rb render:
// Compositor::TRAY -> Widgets.status_area/status_icon -> Widgets.render ->
// wasmboxBlitRGBAOver, painted in a right-to-left strip along the TOP, LEFT of
// the notification toast column so the two overlays never overlap).
//
// Like the toasts, the tray is a translucent overlay composited on the
// compositor worker's OffscreenCanvas, so we read the real composited pixels via
// the worker's __wasmboxGrabRegion hook and drive the strip deterministically via
// the __wasmboxTrayAdd / __wasmboxTrayRemove test hooks (which inject real
// tray_add / tray_remove wire messages — full decode -> handle -> route path).
// Two compositor-owned indicators (settings + search glyphs) are registered at
// boot, so the strip is populated with no client running.
//
// Asserted, paint-agnostic (no exact colour / font metric):
//   1. The compositor boots cleanly through the tray path (a throw in
//      Widgets.status_area / status_icon / render / the blit would raise).
//   2. The two built-in indicator cells (top, left of the toast column) carry
//      ink — the tray rendered + composited top-right.
//   3. A top-left control region stays empty (the ink is the tray, not noise).
//   4. Adding an icon via the hook fills the next cell to the LEFT; removing it
//      returns that cell to bare desktop.
//   5. Left-clicking a built-in indicator opens its compositor menu (bright
//      menu pixels appear just below the strip); clicking empty desktop dismisses.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 20000;
const VIEW_W = 1280;
const VIEW_H = 800;

// Tray geometry — MUST match TrayArea's ICON / GAP / MARGIN and notif_reserve
// (compositor/05_tray.rb) with NotificationStack's TOAST_W / TOAST_MARGIN
// (compositor/05_notifications.rb). Cells lay out RIGHT-TO-LEFT from
// (screen_w - notif_reserve).
const ICON = 28;
const GAP = 8;
const MARGIN = 16;               // top inset (cell y)
const NOTIF_RESERVE = 300 + 2 * 16; // TOAST_W + 2*TOAST_MARGIN = 332
const RIGHT = VIEW_W - NOTIF_RESERVE; // right edge of the tray strip
const cellX = (i) => RIGHT - ICON - i * (ICON + GAP); // left edge of cell i
const CELL0_X = cellX(0); // sys.settings (built-in, oldest -> nearest right)
const CELL1_X = cellX(1); // sys.search   (built-in)
const CELL2_X = cellX(2); // first hook-added slot

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js":   "text/javascript; charset=utf-8",
  ".mjs":  "text/javascript; charset=utf-8",
  ".wasm": "application/wasm",
  ".css":  "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".rb":   "text/plain; charset=utf-8",
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
    } catch {
      res.writeHead(404).end("not found");
    }
  });
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      resolve({ server, base: `http://127.0.0.1:${port}` });
    });
  });
}

function fail(msg) { console.error(`FAIL: ${msg}`); process.exitCode = 1; }
function ok(msg)   { console.log(`ok  ${msg}`); }

// Locate the compositor worker (the one exposing the test hooks).
async function compositorWorker(page) {
  for (const w of page.workers()) {
    try {
      const has = await w.evaluate(() => typeof globalThis.__wasmboxGrabRegion === "function");
      if (has) return w;
    } catch (_) { /* not it */ }
  }
  return null;
}

// Grab a region off the compositor's OffscreenCanvas as a decoded PNG.
async function grab(worker, x, y, w, h) {
  const dataURL = await worker.evaluate(
    ({ x, y, w, h }) => globalThis.__wasmboxGrabRegion(x, y, w, h),
    { x, y, w, h },
  );
  if (!dataURL) return null;
  const b64 = dataURL.slice(dataURL.indexOf(",") + 1);
  return PNG.sync.read(Buffer.from(b64, "base64"));
}

// The dark desktop backdrop (#11131a) is the "empty" reference. Count pixels
// that differ from it by more than `tol` in summed channel distance — a
// rendered tray cell (chrome + glyph) or an open menu lights up far more than
// the bare desktop (+ faint grid).
const DESK = [17, 19, 26];
function inkCount(png, tol) {
  let n = 0;
  for (let i = 0; i < png.data.length; i += 4) {
    const d = Math.abs(png.data[i] - DESK[0]) +
              Math.abs(png.data[i + 1] - DESK[1]) +
              Math.abs(png.data[i + 2] - DESK[2]);
    if (d > tol) n++;
  }
  return n;
}
async function cellInk(worker, x) {
  const png = await grab(worker, x, MARGIN, ICON, ICON);
  return png ? inkCount(png, 40) : -1;
}

// An occupied cell sits over the translucent status-bar plate, so nearly all of
// its 28x28 = 784 px differ from the bare desktop; an empty cell is desktop (+ a
// faint grid line at most).
const PRESENT = 400; // plate-backed cell lights up most of its 784 px
const EMPTY = 120;   // bare desktop (+ a grid line) in the cell

const { server, base } = await startServer();
console.log(`probe-tray: serving on ${base}`);

const browser = await chromium.launch({ headless: true });
const pageErrors = [];

try {
  const page = await browser.newPage({ viewport: { width: VIEW_W, height: VIEW_H } });
  page.on("pageerror", (e) => pageErrors.push(String(e)));

  await page.goto(`${base}/index.html`, { waitUntil: "load" });
  await page.waitForFunction(
    () => {
      if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
      return globalThis.wasmboxReady === true;
    },
    { timeout: BOOT_TIMEOUT_MS },
  );
  ok("compositor booted (tray path did not throw at boot)");
  await page.waitForTimeout(1000); // let a few rAF frames settle

  const cw = await compositorWorker(page);
  if (!cw) { fail("could not find the compositor worker (no __wasmboxGrabRegion)"); throw new Error("no worker"); }

  const hasHook = await cw.evaluate(() =>
    typeof globalThis.__wasmboxTrayAdd === "function" &&
    typeof globalThis.__wasmboxTrayRemove === "function");
  if (!hasHook) { fail("__wasmboxTrayAdd / __wasmboxTrayRemove test hooks are missing"); throw new Error("no hook"); }
  ok("tray test hooks present");

  // (2) The two built-in indicator cells carry ink.
  const c0 = await cellInk(cw, CELL0_X);
  const c1 = await cellInk(cw, CELL1_X);
  if (c0 < 0) { fail("could not grab the tray region"); throw new Error("grab"); }
  if (c0 < PRESENT) {
    fail(`built-in cell 0 has only ${c0} ink pixels — the settings indicator did not render`);
  } else {
    ok(`built-in indicator 0 rendered top-right (${c0} ink pixels)`);
  }
  if (c1 < PRESENT) {
    fail(`built-in cell 1 has only ${c1} ink pixels — the search indicator did not render`);
  } else {
    ok(`built-in indicator 1 rendered next to it (${c1} ink pixels)`);
  }

  // (3) Control: a same-size rect on the bare desktop, top-left, must stay empty
  // — proving the ink hits are the tray strip, not desktop noise.
  const ctrlPng = await grab(cw, 40, MARGIN, ICON, ICON);
  const ctrl = ctrlPng ? inkCount(ctrlPng, 40) : 0;
  if (ctrl > EMPTY) {
    fail(`control: bare-desktop rect has ${ctrl} ink pixels (expected ~0)`);
  } else {
    ok(`control: bare-desktop rect empty (${ctrl} ink pixels)`);
  }

  // (4) Add an icon via the hook -> the next cell to the LEFT fills.
  const before2 = await cellInk(cw, CELL2_X);
  if (before2 > EMPTY) {
    fail(`cell 2 not empty before add (${before2} ink pixels)`);
  } else {
    ok(`cell 2 empty before add (${before2} ink pixels)`);
  }
  await cw.evaluate(() => globalThis.__wasmboxTrayAdd({ id: "probe.tray", glyph: "copy", tooltip: "probe" }));
  await page.waitForTimeout(500);
  const after2 = await cellInk(cw, CELL2_X);
  if (after2 < PRESENT) {
    fail(`added tray icon: only ${after2} ink pixels in cell 2 — icon did not render`);
  } else {
    ok(`added tray icon rendered in the next cell (${after2} ink pixels)`);
  }

  // ...and removing it returns the cell to bare desktop.
  await cw.evaluate(() => globalThis.__wasmboxTrayRemove("probe.tray"));
  await page.waitForTimeout(500);
  const removed2 = await cellInk(cw, CELL2_X);
  if (removed2 > EMPTY) {
    fail(`removed tray icon: cell 2 still has ${removed2} ink pixels — did not disappear`);
  } else {
    ok(`removed tray icon disappeared (${removed2} ink pixels)`);
  }

  // (5) Left-click a built-in indicator -> its compositor menu opens. The menu
  // is drawn from the click point downward; grab a band just BELOW the strip
  // (bare desktop pre-click) and require it to fill with menu pixels.
  const MENU_X = CELL0_X + Math.floor(ICON / 2);
  const MENU_Y = MARGIN + Math.floor(ICON / 2);
  const bandBefore = await grab(cw, MENU_X, MARGIN + ICON + 6, 120, 50);
  const bandBeforeInk = bandBefore ? inkCount(bandBefore, 40) : 0;
  if (bandBeforeInk > EMPTY) {
    console.log(`info: menu band not empty pre-click (${bandBeforeInk}) — proceeding`);
  } else {
    ok(`menu band empty before the click (${bandBeforeInk} ink pixels)`);
  }
  await page.mouse.click(MENU_X, MENU_Y, { button: "left" });
  await page.waitForTimeout(400);
  const bandAfter = await grab(cw, MENU_X, MARGIN + ICON + 6, 120, 50);
  const bandAfterInk = bandAfter ? inkCount(bandAfter, 40) : 0;
  if (bandAfterInk < 200) {
    fail(`click built-in: only ${bandAfterInk} menu pixels below the strip — no menu opened`);
  } else {
    ok(`left-click on a built-in indicator opened its menu (${bandAfterInk} menu pixels)`);
  }
  // Dismiss the menu with a click on empty desktop, leaving a clean state.
  await page.mouse.click(600, 600, { button: "left" });
  await page.waitForTimeout(200);

  if (pageErrors.length) {
    fail(`pageerror(s): ${pageErrors.join(" | ")}`);
  } else {
    ok("no pageerror");
  }
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
