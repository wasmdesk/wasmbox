// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the DESKTOP APPLETS feature
// (compositor/05_applets.rb model + compositor/14_applets.rb render:
// Compositor::APPLETS -> Widgets.v_box/grid/label/level_bar/badge ->
// Widgets.render -> wasmboxBlitRGBAOver, painted as the BOTTOM interactive
// stratum — right after the wallpaper and BEFORE the windows, so a window always
// composites OVER an applet).
//
// Applets are compositor-owned (no client, no SharedArrayBuffer), so we read the
// real composited pixels via the worker's __wasmboxGrabRegion hook and drive the
// board deterministically via the __wasmboxAppletPlace / __wasmboxAppletRemove
// test hooks (which show + position / hide a tile through the same helpers the
// root-menu "Applets" submenu uses).
//
// Asserted, paint-agnostic (no exact colour / font metric):
//   1. The compositor boots cleanly through the applet path (a throw in any
//      Widgets.* applet call would raise at the first render).
//   2. A placed applet renders on the desktop (a clear region lights up).
//   3. Z-ORDER: an applet placed UNDER a boot window is occluded (the region is
//      unchanged — the window composited over it); moved to a clear spot the SAME
//      tile then renders (it was a real tile, just behind the window).
//   4. Dragging the applet on the empty desktop moves it (old spot clears, new
//      spot fills).
//   5. Removing it returns its spot to bare desktop.
//   6. PERSISTENCE: after a reload the placed applet reappears (localStorage).

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

// Clear desktop spots (no boot window, no top-right tray/notification strip, no
// bottom-left HUD). Boot windows cascade from ~(60,60) with sizes up to 300x190,
// so they occupy roughly x in [60,500], y in [60,300]; the mid/lower-right is
// clear. A spot UNDER a boot window (for the z-order test) sits top-left.
const CLEAR = { x: 560, y: 420 };     // applet #1 render + drag origin
const CLEAR2 = { x: 560, y: 560 };    // moved-monitor render (z-order positive)
const DRAGGED = { x: 300, y: 600 };   // applet #1 after the drag
const PERSIST = { x: 700, y: 480 };   // survives a reload
const UNDER = { x: 140, y: 140 };     // under the boot windows (occluded)

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

async function compositorWorker(page) {
  for (const w of page.workers()) {
    try {
      const has = await w.evaluate(() => typeof globalThis.__wasmboxGrabRegion === "function");
      if (has) return w;
    } catch (_) { /* not it */ }
  }
  return null;
}

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
// that differ from it by more than `tol` in summed channel distance — a rendered
// applet tile (card plate + widgets) lights up far more than the bare desktop.
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
// A 60x60 sample inside a tile: the plate + content light up most of the 3600 px.
const SAMPLE = 60;
async function tileInk(worker, spot) {
  const png = await grab(worker, spot.x + 20, spot.y + 14, SAMPLE, SAMPLE);
  return png ? inkCount(png, 40) : -1;
}
const PRESENT = 1800; // plate-backed sample lights up well over half its 3600 px
const EMPTY = 400;    // bare desktop (+ a faint grid line or two) in the sample

// Applet plate + geometry (MUST match compositor/14_applets.rb APPLET_PAD +
// 05_applets.rb Applet::SIZE). The widget buffer is blitted at (x+PAD, y+PAD) at
// (w-2*PAD) x (h-2*PAD).
const PAD = 8;
const CLOCK_W = 232, CLOCK_H = 88;
const CAL_W = 210, CAL_H = 184;

// Count light-text pixels (a label glyph on the dark applet plate sums bright,
// the plate itself stays dark).
function brightCount(png, floor) {
  let n = 0;
  for (let i = 0; i < png.data.length; i += 4) {
    if (png.data[i] + png.data[i + 1] + png.data[i + 2] > floor) n++;
  }
  return n;
}
// Vertical extent (last - first + 1) of rows carrying light text, over the whole
// grabbed region — how tall the painted glyphs stand.
function brightVExtent(png, floor, perRow) {
  let first = -1, last = -1;
  for (let y = 0; y < png.height; y++) {
    let cnt = 0;
    for (let x = 0; x < png.width; x++) {
      const i = (y * png.width + x) * 4;
      if (png.data[i] + png.data[i + 1] + png.data[i + 2] > floor) cnt++;
    }
    if (cnt >= perRow) { if (first < 0) first = y; last = y; }
  }
  return first < 0 ? 0 : last - first + 1;
}
// Count pixels that differ between two same-size regions by more than `tol`
// (summed channel distance) — a month navigation repaints the header + grid.
function diffPixels(a, b, tol) {
  let n = 0;
  const len = Math.min(a.data.length, b.data.length);
  for (let i = 0; i < len; i += 4) {
    const d = Math.abs(a.data[i] - b.data[i]) +
              Math.abs(a.data[i + 1] - b.data[i + 1]) +
              Math.abs(a.data[i + 2] - b.data[i + 2]);
    if (d > tol) n++;
  }
  return n;
}
const TEXT_FLOOR = 300; // light glyph sum; the dark plate (#2a2d3a, ~145) stays under

const { server, base } = await startServer();
console.log(`probe-applets: serving on ${base}`);

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
  ok("compositor booted (applet path did not throw at boot)");
  await page.waitForTimeout(800);

  const cw = await compositorWorker(page);
  if (!cw) { fail("could not find the compositor worker (no __wasmboxGrabRegion)"); throw new Error("no worker"); }

  const hasHook = await cw.evaluate(() =>
    typeof globalThis.__wasmboxAppletPlace === "function" &&
    typeof globalThis.__wasmboxAppletRemove === "function");
  if (!hasHook) { fail("__wasmboxAppletPlace / __wasmboxAppletRemove test hooks are missing"); throw new Error("no hook"); }
  ok("applet test hooks present");

  // (2) Place a clock at a clear spot -> it renders on the desktop.
  const clearBefore = await tileInk(cw, CLEAR);
  if (clearBefore < 0) { fail("could not grab the applet region"); throw new Error("grab"); }
  if (clearBefore > EMPTY) {
    console.log(`info: clear spot not empty pre-place (${clearBefore}) — proceeding`);
  } else {
    ok(`clear spot empty before place (${clearBefore} ink px)`);
  }
  await cw.evaluate((s) => globalThis.__wasmboxAppletPlace("clock", s.x, s.y), CLEAR);
  await page.waitForTimeout(500);
  const clearAfter = await tileInk(cw, CLEAR);
  if (clearAfter < PRESENT) {
    fail(`placed clock: only ${clearAfter} ink px — the applet did not render on the desktop`);
  } else {
    ok(`placed clock rendered on the desktop (${clearAfter} ink px)`);
  }

  // (3) Z-ORDER: place a monitor UNDER the boot windows -> occluded (region
  // unchanged); moved to a clear spot it then renders.
  const underBefore = await grab(cw, UNDER.x, UNDER.y, SAMPLE, SAMPLE);
  const underBeforeInk = underBefore ? inkCount(underBefore, 40) : -1;
  await cw.evaluate((s) => globalThis.__wasmboxAppletPlace("monitor", s.x, s.y), UNDER);
  await page.waitForTimeout(500);
  const underAfter = await grab(cw, UNDER.x, UNDER.y, SAMPLE, SAMPLE);
  const underAfterInk = underAfter ? inkCount(underAfter, 40) : -1;
  if (Math.abs(underAfterInk - underBeforeInk) > 300) {
    fail(`z-order: region under a window changed by ${Math.abs(underAfterInk - underBeforeInk)} px after placing an applet behind it — the applet bled over the window`);
  } else {
    ok(`z-order: applet placed under a window is occluded by it (${underBeforeInk} -> ${underAfterInk} ink px)`);
  }
  // ...and the SAME monitor renders once moved to a clear spot (proves it was a
  // real tile, merely behind the window).
  await cw.evaluate((s) => globalThis.__wasmboxAppletPlace("monitor", s.x, s.y), CLEAR2);
  await page.waitForTimeout(500);
  const monInk = await tileInk(cw, CLEAR2);
  if (monInk < PRESENT) {
    fail(`z-order: monitor did not render when moved to a clear spot (${monInk} ink px)`);
  } else {
    ok(`z-order: monitor renders on the open desktop (${monInk} ink px)`);
  }

  // (4) DRAG the clock across the empty desktop: old spot clears, new spot fills.
  const startX = CLEAR.x + 116, startY = CLEAR.y + 44;      // inside the clock tile
  const endX = DRAGGED.x + 116, endY = DRAGGED.y + 44;      // same grab-offset delta
  await page.mouse.move(startX, startY);
  await page.mouse.down();
  await page.mouse.move((startX + endX) / 2, (startY + endY) / 2);
  await page.mouse.move(endX, endY);
  await page.mouse.up();
  await page.waitForTimeout(500);
  const oldSpot = await tileInk(cw, CLEAR);
  const newSpot = await tileInk(cw, DRAGGED);
  if (oldSpot > EMPTY) {
    fail(`drag: the old spot still has ${oldSpot} ink px — the applet did not leave`);
  } else {
    ok(`drag: old spot cleared (${oldSpot} ink px)`);
  }
  if (newSpot < PRESENT) {
    fail(`drag: the new spot has only ${newSpot} ink px — the applet did not arrive`);
  } else {
    ok(`drag: applet moved to the new spot (${newSpot} ink px)`);
  }

  // (5) REMOVE the clock -> its spot returns to bare desktop.
  await cw.evaluate(() => globalThis.__wasmboxAppletRemove("clock"));
  await page.waitForTimeout(500);
  const removed = await tileInk(cw, DRAGGED);
  if (removed > EMPTY) {
    fail(`remove: the spot still has ${removed} ink px — the applet did not disappear`);
  } else {
    ok(`removed applet disappeared (${removed} ink px)`);
  }

  // (6) PERSISTENCE: place a calendar, reload, and require it to reappear.
  await cw.evaluate((s) => globalThis.__wasmboxAppletPlace("calendar", s.x, s.y), PERSIST);
  await page.waitForTimeout(500);
  await page.reload({ waitUntil: "load" });
  await page.waitForFunction(
    () => {
      if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
      return globalThis.wasmboxReady === true;
    },
    { timeout: BOOT_TIMEOUT_MS },
  );
  await page.waitForTimeout(800);
  const cw2 = await compositorWorker(page);
  if (!cw2) { fail("no compositor worker after reload"); throw new Error("no worker 2"); }
  const persisted = await tileInk(cw2, PERSIST);
  if (persisted < PRESENT) {
    fail(`persistence: the calendar did not survive a reload (${persisted} ink px at its saved spot)`);
  } else {
    ok(`persistence: applet reappeared at its saved spot after a reload (${persisted} ink px)`);
  }

  // === v0.86 Calendar applet (bound Widgets.calendar) + month navigation ======
  // The persisted calendar tile (at PERSIST) is a real go-widgets Calendar. It
  // must (a) render a legible month grid and (b) REPAINT to a different grid when
  // navigated a month — driven through the __wasmboxCalendarNav test hook, the
  // same calendar_nav the header-arrow click uses.
  const hasNav = await cw2.evaluate(() => typeof globalThis.__wasmboxCalendarNav === "function");
  if (!hasNav) { fail("__wasmboxCalendarNav test hook is missing"); throw new Error("no nav hook"); }
  const CAL_IN = { x: PERSIST.x + PAD, y: PERSIST.y + PAD, w: CAL_W - 2 * PAD, h: CAL_H - 2 * PAD };
  const calBefore = await grab(cw2, CAL_IN.x, CAL_IN.y, CAL_IN.w, CAL_IN.h);
  const calBeforeText = calBefore ? brightCount(calBefore, TEXT_FLOOR) : -1;
  if (calBeforeText < 300) {
    fail(`calendar: only ${calBeforeText} text px — the month grid did not render`);
  } else {
    ok(`calendar month grid rendered (${calBeforeText} glyph px)`);
  }
  await cw2.evaluate(() => globalThis.__wasmboxCalendarNav("next"));
  await page.waitForTimeout(400);
  const calNext = await grab(cw2, CAL_IN.x, CAL_IN.y, CAL_IN.w, CAL_IN.h);
  const dNext = calNext ? diffPixels(calBefore, calNext, 60) : 0;
  if (dNext < 200) {
    fail(`calendar next-month: grid changed by only ${dNext} px — navigation did not repaint a new month`);
  } else {
    ok(`calendar 'next' repainted a new month grid (${dNext} px changed)`);
  }
  await cw2.evaluate(() => globalThis.__wasmboxCalendarNav("prev"));
  await page.waitForTimeout(400);
  const calPrev = await grab(cw2, CAL_IN.x, CAL_IN.y, CAL_IN.w, CAL_IN.h);
  const dPrev = calPrev ? diffPixels(calNext, calPrev, 60) : 0;
  if (dPrev < 200) {
    fail(`calendar prev-month: grid changed by only ${dPrev} px — navigation did not repaint`);
  } else {
    ok(`calendar 'prev' repainted back a month (${dPrev} px changed)`);
  }

  // === v0.86 big clock face (Widgets.set_font_size) ===========================
  // Place a clock at a clear spot; its HH:MM:SS line must render at the large
  // per-label font (a tall glyph band) and stay WITHIN the tile bounds.
  const CLOCKSPOT = { x: 560, y: 300 };
  await cw2.evaluate((s) => globalThis.__wasmboxAppletPlace("clock", s.x, s.y), CLOCKSPOT);
  await page.waitForTimeout(400);
  // Time band: the top flex row of the inner buffer (inner h = 72, date row = 20,
  // so the time row is the top ~52 px).
  const timeBand = await grab(cw2, CLOCKSPOT.x + PAD, CLOCKSPOT.y + PAD, CLOCK_W - 2 * PAD, 52);
  const timeText = timeBand ? brightCount(timeBand, TEXT_FLOOR) : -1;
  const timeTall = timeBand ? brightVExtent(timeBand, TEXT_FLOOR, 3) : 0;
  if (timeText < 200) {
    fail(`big clock: only ${timeText} glyph px in the time row — the clock did not render`);
  } else if (timeTall < 22) {
    fail(`big clock: time glyphs span only ${timeTall}px tall — set_font_size did not enlarge the face (base ~11px)`);
  } else {
    ok(`big clock face rendered large (${timeText} glyph px, ${timeTall}px tall)`);
  }
  // Bounds-containment: no glyph pixels bleed past the tile's bottom or right edge.
  const belowClock = await grab(cw2, CLOCKSPOT.x, CLOCKSPOT.y + CLOCK_H, CLOCK_W, 4);
  const rightClock = await grab(cw2, CLOCKSPOT.x + CLOCK_W, CLOCKSPOT.y, 4, CLOCK_H);
  const belowText = belowClock ? brightCount(belowClock, TEXT_FLOOR) : 999;
  const rightText = rightClock ? brightCount(rightClock, TEXT_FLOOR) : 999;
  if (belowText > 5 || rightText > 5) {
    fail(`big clock bounds: glyph pixels bled outside the tile (below=${belowText}, right=${rightText})`);
  } else {
    ok(`big clock stays within tile bounds (below=${belowText}, right=${rightText} glyph px)`);
  }

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
