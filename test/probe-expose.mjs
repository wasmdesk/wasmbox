// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for EXPOSÉ / "show all windows" (the LAST Batch-4
// window-management feature): compositor/05_expose.rb (the Expose spread model) +
// compositor/17_expose.rb (the dim + thumbnail-grid overlay + the keyboard/mouse/
// probe wiring), driven from compositor/06_core.rb, gated behind Compositor::EXPOSE.
//
// A synthetic F3 chord + pixel-perfect thumbnail reads are unreliable headless, so
// this probe drives the REAL Exposé transitions through the __wasmboxExpose(cmd)
// test hook (the same code path the keyboard + mouse use: bus -> expose_probe) and
// reads the live spread state the render loop publishes through
// __wasmboxExposeState(). It:
//
//   1. Boots the compositor with the Exposé path enabled (no throw at boot).
//   2. Spawns four real decorated, focused, compositor-painted windows
//      (__wasmboxSpawnWindow) so there is a deterministic spread.
//   3. "open"  -> the spread is up, N>=4 tiles, laid out in a grid whose tiles are
//      NON-OVERLAPPING and on-screen, selection parked on the focused window; the
//      tiles actually paint (a tile-center pixel region differs from the dim gap).
//   4. arrow move + "commit" -> the spread closes AND the newly-selected window
//      becomes focused: __wasmboxFocusedRect matches the tile's published rect.
//   5. re-open + "click:<x>,<y>" on a chosen tile -> that tile's window takes focus
//      and the spread exits (normal render resumes).
//   6. re-open + arrow + "cancel" -> the spread closes choosing nothing; the
//      focused window is unchanged.
//
// The non-overlap + focus assertions derive from the SAME published payloads
// (__wasmboxExposeState / __wasmboxFocusedRect), so the probe is independent of how
// many boot clients happen to be on the desktop.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 20000;
const VIEW_W = 1280;
const VIEW_H = 800;

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
      const has = await w.evaluate(
        () => typeof globalThis.__wasmboxExpose === "function" &&
              typeof globalThis.__wasmboxExposeState === "function" &&
              typeof globalThis.__wasmboxSpawnWindow === "function" &&
              typeof globalThis.__wasmboxSpawnExternalStub === "function" &&
              typeof globalThis.__wasmboxReadRegion === "function" &&
              typeof globalThis.__wasmboxFocusedRect === "function",
      );
      if (has) return w;
    } catch (_) { /* not it */ }
  }
  return null;
}

const stateOf  = (cw) => cw.evaluate(() => globalThis.__wasmboxExposeState());
const focusOf  = (cw) => cw.evaluate(() => globalThis.__wasmboxFocusedRect());
const drive    = (cw, cmd) => cw.evaluate((c) => globalThis.__wasmboxExpose(c), cmd);
const readRgn  = (cw, r) => cw.evaluate(
  ({ x, y, w, h }) => globalThis.__wasmboxReadRegion(x, y, w, h), r);

const showR = (r) => r ? `[${r.x},${r.y} ${r.w}x${r.h}]` : "null";
async function settle(page, ms) { await page.waitForTimeout(ms); }
// A centred sub-rect (fraction `frac` of each side) of a tile [x,y,w,h] — sampled
// to stay clear of the tile's caption strip + selection border.
function centerRegion(t, frac) {
  const rw = Math.max(6, Math.floor(t[2] * frac));
  const rh = Math.max(6, Math.floor(t[3] * frac));
  return { x: Math.floor(t[0] + (t[2] - rw) / 2), y: Math.floor(t[1] + (t[3] - rh) / 2), w: rw, h: rh };
}

// Two axis-aligned rects overlap iff they intersect on BOTH axes.
function overlaps(a, b) {
  return a[0] < b[0] + b[2] && b[0] < a[0] + a[2] &&
         a[1] < b[1] + b[3] && b[1] < a[1] + a[3];
}

const { server, base } = await startServer();
console.log(`probe-expose: serving on ${base}`);

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
  ok("compositor booted (exposé path did not throw at boot)");
  await settle(page, 2500);

  const cw = await compositorWorker(page);
  if (!cw) {
    fail("could not find the compositor worker (no __wasmboxExpose / __wasmboxExposeState / __wasmboxSpawnWindow / __wasmboxReadRegion / __wasmboxFocusedRect)");
    throw new Error("no compositor worker");
  }

  // Before opening, the spread is inactive.
  const s0 = await stateOf(cw);
  if (!s0 || s0.active !== 0) {
    fail(`spread should be inactive before open, got ${JSON.stringify(s0)}`);
  } else {
    ok("spread inactive before any F3");
  }

  // 2. Spawn four deterministic in-process windows (the last is focused).
  await drive(cw, "cancel"); // ensure closed
  for (const t of ["exp-A", "exp-B", "exp-C", "exp-D"]) {
    await cw.evaluate((title) => globalThis.__wasmboxSpawnWindow(title), t);
    await settle(page, 250);
  }
  await settle(page, 300);

  const beforeFocus = await focusOf(cw);
  if (!beforeFocus || beforeFocus.x < 0) {
    fail("no focused window after spawning the four test windows");
    throw new Error("no focus");
  }
  ok(`spawned 4 windows on a ${beforeFocus.screen_w}x${beforeFocus.screen_h} desktop; focused ${showR(beforeFocus)}`);

  // 3. Open the spread: up, N>=4 tiles, non-overlapping + on-screen, selection 0.
  await drive(cw, "open");
  await settle(page, 250);
  const so = await stateOf(cw);
  if (!so || so.active !== 1) {
    fail(`open should raise the spread, got ${JSON.stringify(so)}`);
    throw new Error("open failed");
  }
  if (so.count < 4) {
    fail(`spread should list at least the 4 spawned windows, got count=${so.count}`);
  } else {
    ok(`spread up with ${so.count} thumbnail tiles in a ${so.cols}x${so.rows} grid`);
  }
  if (so.index !== 0) {
    fail(`open should park the selection on the focused window (index 0), got ${so.index}`);
  } else {
    ok("selection parked on the focused window (index 0)");
  }
  // Grid holds every tile.
  if (so.cols * so.rows < so.count) {
    fail(`grid ${so.cols}x${so.rows} too small for ${so.count} tiles`);
  } else {
    ok(`grid ${so.cols}x${so.rows} holds all ${so.count} tiles`);
  }
  // Tiles are on-screen and non-overlapping.
  const tiles = so.tiles || [];
  if (tiles.length !== so.count) {
    fail(`published ${tiles.length} tile rects for ${so.count} tiles`);
  }
  let onScreen = true;
  for (const t of tiles) {
    if (t[0] < 0 || t[1] < 0 || t[0] + t[2] > VIEW_W || t[1] + t[3] > VIEW_H || t[2] <= 0 || t[3] <= 0) {
      onScreen = false;
    }
  }
  if (!onScreen) {
    fail(`some tiles fall off-screen or are degenerate: ${JSON.stringify(tiles)}`);
  } else {
    ok(`all ${tiles.length} tiles fit within the ${VIEW_W}x${VIEW_H} viewport`);
  }
  let anyOverlap = false;
  for (let i = 0; i < tiles.length; i++) {
    for (let j = i + 1; j < tiles.length; j++) {
      if (overlaps(tiles[i], tiles[j])) anyOverlap = true;
    }
  }
  if (anyOverlap) {
    fail(`tiles overlap: ${JSON.stringify(tiles)}`);
  } else {
    ok("no two tiles overlap (non-overlapping spread)");
  }
  // The tiles actually paint: a tile-center pixel region differs from a dim-gap
  // region (top-left corner, which is scrim while the spread is up).
  if (tiles.length) {
    const t0 = tiles[0];
    const centerRgn = { x: t0[0] + Math.floor(t0[2] / 2) - 6, y: t0[1] + Math.floor(t0[3] / 2) - 6, w: 12, h: 12 };
    const gapRgn = { x: 2, y: 2, w: 12, h: 12 };
    const cReg = await readRgn(cw, centerRgn);
    const gReg = await readRgn(cw, gapRgn);
    if (cReg && gReg && cReg.hash !== gReg.hash) {
      ok(`tile pixels rendered (tile-center region differs from the dim gap)`);
    } else {
      fail(`tile does not appear to paint: center=${JSON.stringify(cReg)} gap=${JSON.stringify(gReg)}`);
    }
  }

  // 4. Arrow move + commit: the selected window takes focus and the spread exits.
  await drive(cw, "right");
  await settle(page, 150);
  const sMoved = await stateOf(cw);
  if (sMoved.index === 0) {
    fail("arrow-right should move the selection off index 0");
  } else {
    ok(`arrow-right moved the selection to index ${sMoved.index}`);
  }
  const selRect = { x: sMoved.sel_x, y: sMoved.sel_y, w: sMoved.sel_w, h: sMoved.sel_h };
  if (selRect.w <= 0 || selRect.h <= 0) {
    fail(`selected tile has no window rect: ${showR(selRect)}`);
  } else {
    ok(`selected tile maps to window ${showR(selRect)}`);
  }
  await drive(cw, "commit");
  await settle(page, 300);
  const sc = await stateOf(cw);
  if (sc.active !== 0) {
    fail(`commit should close the spread, still active: ${JSON.stringify(sc)}`);
  } else {
    ok("commit closed the spread (normal render resumed)");
  }
  const afterFocus = await focusOf(cw);
  const gotFocus = { x: afterFocus.x, y: afterFocus.y, w: afterFocus.w, h: afterFocus.h };
  const eq = gotFocus.x === selRect.x && gotFocus.y === selRect.y &&
             gotFocus.w === selRect.w && gotFocus.h === selRect.h;
  if (!eq) {
    fail(`commit should focus the selected window ${showR(selRect)}, but focus is ${showR(gotFocus)}`);
  } else {
    ok(`commit focused the newly-selected window ${showR(gotFocus)}`);
  }

  // 5. Click path: re-open, pick tile 1, click its center -> focus that window + exit.
  await drive(cw, "open");
  await settle(page, 200);
  await drive(cw, "select:1"); // move the selection to tile 1 so we can read its window rect
  await settle(page, 150);
  const sClick = await stateOf(cw);
  const clickTiles = sClick.tiles || [];
  if (sClick.active !== 1 || clickTiles.length < 2) {
    fail(`re-open for the click test failed: ${JSON.stringify(sClick)}`);
    throw new Error("click setup failed");
  }
  const clickSel = { x: sClick.sel_x, y: sClick.sel_y, w: sClick.sel_w, h: sClick.sel_h };
  const t1 = clickTiles[1];
  const cx = t1[0] + Math.floor(t1[2] / 2);
  const cy = t1[1] + Math.floor(t1[3] / 2);
  await drive(cw, `click:${cx},${cy}`);
  await settle(page, 300);
  const sClicked = await stateOf(cw);
  if (sClicked.active !== 0) {
    fail(`click should close the spread, still active: ${JSON.stringify(sClicked)}`);
  } else {
    ok("click closed the spread");
  }
  const clickFocus = await focusOf(cw);
  const cf = { x: clickFocus.x, y: clickFocus.y, w: clickFocus.w, h: clickFocus.h };
  const clickEq = cf.x === clickSel.x && cf.y === clickSel.y && cf.w === clickSel.w && cf.h === clickSel.h;
  if (!clickEq) {
    fail(`click should focus tile-1's window ${showR(clickSel)}, but focus is ${showR(cf)}`);
  } else {
    ok(`click focused the tile under the pointer ${showR(cf)}`);
  }

  // 6. Cancel path: open, move, cancel -> closed, focus unchanged.
  const preCancel = await focusOf(cw);
  await drive(cw, "open");
  await settle(page, 150);
  await drive(cw, "down");
  await settle(page, 150);
  const midCancel = await stateOf(cw);
  if (midCancel.active !== 1) {
    fail("spread should be up before cancel");
  }
  await drive(cw, "cancel");
  await settle(page, 250);
  const sx = await stateOf(cw);
  if (sx.active !== 0) {
    fail(`cancel should close the spread, still active: ${JSON.stringify(sx)}`);
  } else {
    ok("cancel closed the spread");
  }
  const postCancel = await focusOf(cw);
  const focusUnchanged = preCancel.x === postCancel.x && preCancel.y === postCancel.y &&
                         preCancel.w === postCancel.w && preCancel.h === postCancel.h;
  if (!focusUnchanged) {
    fail(`cancel must not change focus: was ${showR(preCancel)}, now ${showR(postCancel)}`);
  } else {
    ok(`cancel left focus unchanged ${showR(postCancel)}`);
  }

  // 7. LIVE THUMBNAILS: an EXTERNAL (SAB) window's Exposé tile shows its real
  // downscaled framebuffer, not a flat placeholder. Spawn a deterministic
  // external stub (bold gradient SAB), open the spread, and walk the tiles: the
  // tile whose selection flags sel_live=1 must be textured (its grabbed content),
  // while an in-process tile flags sel_live=0 and is flat — the contrast in one run.
  await drive(cw, "cancel");
  await settle(page, 150);
  await cw.evaluate(() => globalThis.__wasmboxSpawnExternalStub("live-ext", 240, 180));
  await settle(page, 700); // register, composite + blit at least one frame

  const extFocus = await focusOf(cw);
  if (!extFocus || extFocus.x < 0 || extFocus.w <= 0) {
    fail(`external stub did not focus: ${showR(extFocus)}`);
  } else {
    ok(`external stub window focused ${showR(extFocus)}`);
  }

  await drive(cw, "open");
  await settle(page, 250);
  const sOpen = await stateOf(cw);
  if (sOpen.active !== 1 || (sOpen.tiles || []).length < 2) {
    fail(`open should raise the spread for the live-thumbnail test: ${JSON.stringify(sOpen)}`);
    throw new Error("live open failed");
  }
  // Walk every tile via select:<i>, classifying by the published sel_live flag +
  // the tile's pixel variance. Collect one live tile and one placeholder tile.
  let liveTile = null, liveVar = null, placeTile = null, placeVar = null;
  for (let i = 0; i < sOpen.count; i++) {
    await drive(cw, `select:${i}`);
    await settle(page, 100);
    const st = await stateOf(cw);
    const rgn = await readRgn(cw, centerRegion(st.tiles[i], 0.5));
    if (st.sel_live === 1 && !liveTile) { liveTile = i; liveVar = rgn; }
    if (st.sel_live === 0 && !placeTile) { placeTile = i; placeVar = rgn; }
  }
  if (liveTile === null) {
    fail("no tile flagged live (sel_live=1) — the external stub's tile should be live");
  } else if (liveVar && liveVar.variance > 40) {
    ok(`live tile #${liveTile} shows real content (textured, variance=${liveVar.variance}), not a flat placeholder`);
  } else {
    fail(`live tile #${liveTile} is not textured: ${JSON.stringify(liveVar)}`);
  }
  if (placeTile === null) {
    fail("no in-process (placeholder) tile found (sel_live=0)");
  } else if (liveVar && placeVar && placeVar.variance < liveVar.variance) {
    ok(`placeholder tile #${placeTile} is flat (variance=${placeVar.variance}) vs live tile (variance=${liveVar.variance}); sel_live=0`);
  } else {
    fail(`placeholder tile not clearly flatter than live: placeholder=${JSON.stringify(placeVar)} live=${JSON.stringify(liveVar)}`);
  }
  await drive(cw, "cancel");
  await settle(page, 200);

  // Client-asset load races (a spawned client's .wasm not yet served) are
  // unrelated to the spread under test — the compositor itself booted fine.
  const fatal = pageErrors.filter((e) => !/bootWasm: HTTP \d+ for/.test(e));
  const benign = pageErrors.length - fatal.length;
  if (benign) console.log(`info ${benign} benign client-asset load error(s) ignored`);
  if (fatal.length) {
    fail(`pageerror(s): ${fatal.join(" | ")}`);
  } else {
    ok("no fatal pageerror");
  }
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
