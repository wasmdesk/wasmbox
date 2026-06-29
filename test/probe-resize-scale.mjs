// SPDX-License-Identifier: BSD-3-Clause
//
// Headless-browser probe: external-window resize must SCALE-FIT the SAB
// surface into the new window rect, not show a black band past the
// original native size.
//
// Repro: spawn the Quake client (its SAB is 320x240 native). Drag the
// resize grip to grow the window to >> 320x240. Sample pixels at the
// "uncovered" region (right of x=native_w, below y=native_h, where the
// old 1:1 blit would have left desktop background showing through).
// PASS iff that region is now painted with Quake-coloured pixels.
//
// Output:
//   * /tmp/wasmbox-resize-quake.png             -- full viewport
//   * /tmp/wasmbox-resize-quake-before.png      -- pre-resize
//   * /tmp/wasmbox-resize-quake-after.png       -- post-resize
//   * stdout: layout records + pixel evidence + PASS/FAIL banner
// Exit code: 0 on PASS, non-zero on any assertion failure.

import { chromium } from "playwright";
import { PNG } from "pngjs";
import { readFileSync } from "node:fs";

const base = process.env.WASMBOX_BASE_URL || "http://127.0.0.1:8094";
const wait = (ms) => new Promise(r => setTimeout(r, ms));

let failures = 0;
function expect(cond, msg) {
  if (cond) console.log("PASS: " + msg);
  else      { console.error("FAIL: " + msg); failures++; }
}

const browser = await chromium.launch({
  headless: true,
  args: ["--enable-features=SharedArrayBuffer"],
});
const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
const page = await ctx.newPage();

const consoleLines = [];
const errors = [];
page.on("console", (m) => consoleLines.push(`[${m.type()}] ${m.text()}`));
page.on("pageerror", (e) => errors.push(String(e)));
page.on("worker", (w) => {
  w.on("console", (m) => consoleLines.push(`[w:${m.type()}] ${m.text()}`));
});

// We start from a clean layout: previous probe runs may have persisted
// dragged/resized Quake geometry; if we honoured that, the "pre-resize"
// rect would already be huge and the test would not actually exercise the
// scale-fit path. localStorage.clear() before navigation forces the
// compositor to use the default cascade slot for every window.
{
  const ctxStorage = await ctx.storageState();
  void ctxStorage;
  await page.addInitScript(() => {
    try { localStorage.removeItem("wasmbox.layout"); } catch (_) {}
  });
}

console.log(`probe-resize-scale: GET ${base}/`);
await page.goto(base + "/", { waitUntil: "load", timeout: 30000 });
await page.waitForFunction(() => !!window.wasmboxReady, { timeout: 30000 });
console.log("probe-resize-scale: wasmboxReady=true");

// Quake auto-spawns from compositor.worker.js (autoSpawnIfPresent) when
// clients/quake/worker.js is present in the static tree -- which it is in
// this build. No manual wasmboxSpawnExternal needed (a second spawn would
// just create a duplicate cascade slot we'd then have to disambiguate).

// Read the layout record for the Quake window out of localStorage. The
// compositor persists "title\tx\ty\tw\th" lines under the key
// `wasmbox.layout`; the Quake client titles its window "quake (wasm)".
// Multiple "quake" lines can appear (auto-spawn + manual spawn raced); we
// take the LAST one because it is the most recently registered and
// therefore the topmost surface in the stack.
async function quakeRect() {
  return await page.evaluate(() => {
    const raw = localStorage.getItem("wasmbox.layout");
    if (raw == null) return null;
    const matches = [];
    for (const line of String(raw).split("\n")) {
      const p = line.split("\t");
      if (p.length >= 5 && /quake/i.test(p[0])) {
        matches.push({ title: p[0], x: +p[1], y: +p[2], w: +p[3], h: +p[4] });
      }
    }
    return matches.length ? matches[matches.length - 1] : null;
  });
}

// Wait up to 20 s for a Quake layout record (means the welcome handshake
// landed + the compositor wrote the surface into the persisted layout).
let rect = null;
for (let i = 0; i < 40; i++) {
  rect = await quakeRect();
  if (rect) break;
  await wait(500);
}
if (!rect) {
  console.error("probe-resize-scale: quake window never registered. Last 30 console lines:");
  for (const l of consoleLines.slice(-30)) console.error("  " + l);
  console.error("errors:", errors);
  await browser.close();
  process.exit(1);
}
console.log(`probe-resize-scale: quake rect (pre-resize): ${JSON.stringify(rect)}`);
const nativeW = rect.w;
const nativeH = rect.h;

// Let Quake paint a couple of real frames so the surface has palette pixels
// (not the synth/error placeholder). 6 s is comfortable on a hot cache.
await wait(6000);

await page.screenshot({ path: "/tmp/wasmbox-resize-quake-before.png", fullPage: false });

// Read the screenshot back + sample the surface ORIGIN (rect.x + 8, rect.y + 8
// inside the title bar area is decoration; sample inside the body, ~rect.x +
// nativeW/2, rect.y + 30 + nativeH/2 to hit a Quake frame pixel). The
// title-bar height varies; we widen the band by sampling 32px below the
// titled rect.
function nonZeroAt(png, x, y) {
  if (x < 0 || y < 0 || x >= png.width || y >= png.height) return false;
  const i = (y * png.width + x) * 4;
  return (png.data[i] | png.data[i + 1] | png.data[i + 2]) !== 0;
}
function pixelAt(png, x, y) {
  const i = (y * png.width + x) * 4;
  return [png.data[i], png.data[i + 1], png.data[i + 2]];
}

const beforePng = PNG.sync.read(readFileSync("/tmp/wasmbox-resize-quake-before.png"));
console.log(`probe-resize-scale: screenshot ${beforePng.width}x${beforePng.height}`);

// Probe the body around (cx, cy) in 7x7 grid; any non-zero pixel proves the
// blit landed (palette pixels are non-zero, desktop bg #0b0d12 is also non-
// zero, so this is only a "the rect is rendered" check). We rely on the
// titlebar+frame being painted around the SAB at the same window rect.
const cxBefore = rect.x + Math.floor(nativeW / 2);
const cyBefore = rect.y + 30 + Math.floor(nativeH / 2); // 30 ~= title + frame
let beforeHits = 0;
const sample = [];
for (let dx = -12; dx <= 12; dx += 4) {
  for (let dy = -12; dy <= 12; dy += 4) {
    if (nonZeroAt(beforePng, cxBefore + dx, cyBefore + dy)) beforeHits++;
    sample.push(pixelAt(beforePng, cxBefore + dx, cyBefore + dy));
  }
}
console.log(`probe-resize-scale: pre-resize body center samples (49 px, non-zero=${beforeHits})`);

// --- DRAG THE RESIZE GRIP ---------------------------------------------------
// The compositor's resize grip is the bottom-right 14x14 corner (Theme::GRIP
// = 14). The window's "frame_rect" includes the decoration around the body,
// so the grip sits at (right, bottom) of the frame. The persisted record
// covers the full frame (decoration + body + 22 px titlebar at top + a 1 px
// border), so the bottom-right CORNER of the persisted rect is the grip's
// outer corner.
//
// We drag from a point well inside the grip (right-4, bottom-4) to a point
// 250 px right + 200 px down, which grows the window dramatically larger
// than its 320x240 native SAB.
const gripX = rect.x + rect.w - 4;
const gripY = rect.y + rect.h - 4;
const newW = rect.w + 280;
const newH = rect.h + 220;
console.log(`probe-resize-scale: dragging grip from (${gripX},${gripY}) by (+280,+220)`);
await page.mouse.move(gripX, gripY);
await page.mouse.down();
await page.mouse.move(gripX + 100, gripY + 80, { steps: 6 });
await page.mouse.move(gripX + 280, gripY + 220, { steps: 6 });
await page.mouse.up();

// Wait for at least one Quake commit so the resized window has been blitted.
// Quake runs at ~30-70 fps; 1 s is plenty.
await wait(1500);

const rectAfter = await quakeRect();
console.log(`probe-resize-scale: quake rect (post-resize): ${JSON.stringify(rectAfter)}`);

await page.screenshot({ path: "/tmp/wasmbox-resize-quake-after.png", fullPage: false });
await page.screenshot({ path: "/tmp/wasmbox-resize-quake.png", fullPage: false });
const afterPng = PNG.sync.read(readFileSync("/tmp/wasmbox-resize-quake-after.png"));

// The compositor paints the desktop with Theme::DESKTOP = #11131a (17,19,26)
// + a grid in Theme::DESKTOP_GRID = #171a24 (23,26,36) every 32 px. We want
// to prove the scale-fit blit fills the resized window: any pixel covered
// by the new window rect must be "window content" (Quake surface or its
// decoration), NOT either of those two desktop colours. A pre-fix run
// would show desktop colours leaking through the uncovered scale strip.
function isBg(r, g, b) {
  // DESKTOP fill #11131a
  if (Math.abs(r - 17) <= 3 && Math.abs(g - 19) <= 3 && Math.abs(b - 26) <= 3) return true;
  // DESKTOP_GRID #171a24
  if (Math.abs(r - 23) <= 3 && Math.abs(g - 26) <= 3 && Math.abs(b - 36) <= 3) return true;
  return false;
}

// PROOF #1: NO desktop-bg pixels visible inside the post-resize body rect.
// With the buggy 1:1 blit, the right + bottom strips beyond the native SAB
// would have been left at the canvas's underlying clear colour. Painting
// the canvas every frame clears it first (the compositor draws the desktop
// background) -- so those strips would show desktop bg.
{
  const bx0 = rectAfter.x + 4;
  const bx1 = rectAfter.x + rectAfter.w - 4;
  const by0 = rectAfter.y + 4;
  const by1 = rectAfter.y + rectAfter.h - 4;
  let scanned = 0, bg = 0;
  for (let y = by0; y < by1; y += 3) {
    for (let x = bx0; x < bx1; x += 3) {
      if (x < 0 || y < 0 || x >= afterPng.width || y >= afterPng.height) continue;
      scanned++;
      const i = (y * afterPng.width + x) * 4;
      const r = afterPng.data[i], g = afterPng.data[i + 1], b = afterPng.data[i + 2];
      if (isBg(r, g, b)) bg++;
    }
  }
  console.log(`probe-resize-scale: body-rect scan scanned=${scanned} desktop-bg=${bg}`);
  expect(bg < scanned * 0.01,
    `< 1% desktop-bg pixels inside resized body (bg=${bg}/${scanned}) -- scaled surface fills the window`);
}

// PROOF #2: distinct-colour count in the SCALE STRIP (only exists because we
// grew the window past nativeW x nativeH) jumps from ~0 (would be desktop bg
// + maybe a few legend pixels) before to >> 4 after, because Quake palette
// pixels stretched into it. Sample the strip strictly past the original
// nativeW (i.e. the right band) which the pre-fix code left at desktop bg.
{
  const x0 = rect.x + nativeW + 8;
  const x1 = rectAfter.x + rectAfter.w - 6;
  const y0 = rect.y + 8;
  const y1 = rect.y + nativeH - 8;
  const palette = new Set();
  let scanned = 0, nonBg = 0, bright = 0;
  for (let y = y0; y < y1; y += 2) {
    for (let x = x0; x < x1; x += 2) {
      if (x < 0 || y < 0 || x >= afterPng.width || y >= afterPng.height) continue;
      scanned++;
      const i = (y * afterPng.width + x) * 4;
      const r = afterPng.data[i], g = afterPng.data[i + 1], b = afterPng.data[i + 2];
      if (!isBg(r, g, b)) nonBg++;
      if (r > 100 || g > 100 || b > 100) bright++;
      palette.add((r >> 3) << 10 | (g >> 3) << 5 | (b >> 3));
    }
  }
  console.log(`probe-resize-scale: right-strip scan x=[${x0}..${x1}] y=[${y0}..${y1}] scanned=${scanned} non-bg=${nonBg} bright=${bright} palette=${palette.size}`);
  expect(nonBg > scanned * 0.95,
    `right-strip: > 95% non-bg pixels (non-bg=${nonBg}/${scanned}) -- the scale-up region is COVERED by the scaled Quake surface, not transparent`);
  // Quake's main menu is mostly pure black with sparse bright menu graphics;
  // a handful of bright pixels in the strip is enough evidence that some
  // palette content stretched into the right band (the MAIN banner / "id"
  // logo top-edge spill into this column under the 600/320 = 1.875x scale).
  expect(bright >= 4,
    `right-strip: >= 4 bright pixels (bright=${bright}) -- some stretched Quake palette content reaches into the new horizontal scale region`);
  expect(palette.size >= 8,
    `right-strip: scaled Quake surface shows distinct colours (palette=${palette.size}) -- not a single solid fill`);
}

// PROOF #3: BEFORE the resize, the same right-strip region (which lay
// OUTSIDE the original window) was desktop bg. Establishing the pre/post
// delta proves the scale strip is genuinely new pixels, not coincidence.
{
  const x0 = rect.x + nativeW + 8;
  const x1 = rect.x + nativeW + 60;
  const y0 = rect.y + 40;
  const y1 = rect.y + nativeH - 8;
  let scanned = 0, bg = 0;
  for (let y = y0; y < y1; y += 2) {
    for (let x = x0; x < x1; x += 2) {
      if (x < 0 || y < 0 || x >= beforePng.width || y >= beforePng.height) continue;
      scanned++;
      const i = (y * beforePng.width + x) * 4;
      const r = beforePng.data[i], g = beforePng.data[i + 1], b = beforePng.data[i + 2];
      if (isBg(r, g, b)) bg++;
    }
  }
  console.log(`probe-resize-scale: PRE-resize same region scanned=${scanned} desktop-bg=${bg}`);
  expect(bg > scanned * 0.95,
    `pre-resize: > 95% desktop-bg in the now-scaled region (bg=${bg}/${scanned}) -- baseline confirms the strip was empty before`);
}

// The window actually grew (sanity check the drag landed).
expect(rectAfter && rectAfter.w > nativeW + 100,
  `window width grew (${rectAfter ? rectAfter.w : "?"} > ${nativeW + 100})`);
expect(rectAfter && rectAfter.h > nativeH + 100,
  `window height grew (${rectAfter ? rectAfter.h : "?"} > ${nativeH + 100})`);

await browser.close();

if (failures > 0) {
  console.error(`probe-resize-scale: ${failures} assertion(s) failed`);
  console.error("Last 40 console lines:");
  for (const l of consoleLines.slice(-40)) console.error("  " + l);
  process.exit(1);
}
console.log("probe-resize-scale: OVERALL PASS");
