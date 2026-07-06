// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// End-to-end probe for the terminal+files shared-VFS upgrade.
//
// Drives:
//   1. Spawn terminal -> type `mkdir /scratch && cd /scratch && touch hello.txt
//      && echo "hi there" > hello.txt && cat hello.txt`.
//      Screenshot /tmp/wasmbox-real5-term.png; assert the soft-green ink
//      from `cat` carries enough pixels (proxy for "hi there" rendered).
//   2. Spawn files -> click the Documents row to refresh, then click on the
//      sidebar Home row + drill back into /scratch (we navigate by clicking
//      directly into /scratch from the root row list). Screenshot
//      /tmp/wasmbox-real5-files-after.png; assert that a row carrying the
//      "h"/"e"/"l"/"l"/"o" name primary-ink ink count is non-zero
//      (proxy for hello.txt listed).
//   3. Reload the page -> spawn files only -> drill into /scratch ->
//      screenshot /tmp/wasmbox-real5-files-persist.png; assert hello.txt is
//      still listed (the persistence proof).
//
// HARD RULE: system Chrome, headless. Per the user's HARD rules.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 15000;
const SHOT_TERM     = "/tmp/wasmbox-real5-term.png";
const SHOT_FILES    = "/tmp/wasmbox-real5-files-after.png";
const SHOT_PERSIST  = "/tmp/wasmbox-real5-files-persist.png";

// Terminal palette (matches clients/terminal/internal/scene/render.go).
const TERM_BG    = [0x10, 0x10, 0x10];
const TERM_INK   = [0xa0, 0xe0, 0xa0]; // PaletteFG[0] -- default ink
const TERM_CYAN  = [0x8a, 0xe1, 0xff]; // PaletteFG[1] -- prompt

// Files palette (Adwaita light).
const FILES_SIDEBAR_BG = [240, 240, 240];
const FILES_WINDOW_BG  = [255, 255, 255];
const FILES_ACCENT     = [8, 96, 242];
const FILES_TEXT       = [36, 36, 36];

const HEADER_BAR_HEIGHT = 44;
const COLUMN_HEADER_HEIGHT = 28;
const ROW_HEIGHT = 32;
const SIDEBAR_WIDTH = 160;
const FILES_W = 720;
const FILES_H = 440;

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

function pixelAt(png, x, y) {
  const i = (y * png.width + x) * 4;
  return [png.data[i], png.data[i+1], png.data[i+2]];
}
function eqColor(px, c) { return px[0] === c[0] && px[1] === c[1] && px[2] === c[2]; }

// Locate a window on the canvas by scanning for its unique BG colour.
function findWindowBounds(png, color) {
  const { width, height, data } = png;
  let minX = width, minY = height, maxX = -1, maxY = -1;
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const i = (y * width + x) * 4;
      if (data[i] === color[0] && data[i+1] === color[1] && data[i+2] === color[2]) {
        if (x < minX) minX = x;
        if (y < minY) minY = y;
        if (x > maxX) maxX = x;
        if (y > maxY) maxY = y;
      }
    }
  }
  if (maxX < 0) return null;
  return { x: minX, y: minY, w: maxX - minX + 1, h: maxY - minY + 1 };
}

function countPixels(png, x, y, w, h, color) {
  let n = 0;
  for (let yy = y; yy < y + h; yy++) {
    for (let xx = x; xx < x + w; xx++) {
      if (eqColor(pixelAt(png, xx, yy), color)) n++;
    }
  }
  return n;
}

const { server, base } = await startServer();
console.log(`probe-real5: serving on ${base}`);

const browser = await chromium.launch({ headless: true, channel: "chrome" });
const consoleLines = [];
const pageErrors = [];

async function bootCompositor(page) {
  await page.goto(`${base}/index.html`, { waitUntil: "load" });
  await page.waitForFunction(
    () => {
      if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
      return globalThis.wasmboxReady === true;
    },
    { timeout: BOOT_TIMEOUT_MS },
  );
}

async function spawn(page, worker) {
  await page.evaluate((w) => globalThis.wasmboxSpawnExternal(w), worker);
  await page.waitForTimeout(2500);
}

try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  page.on("console",   (m) => consoleLines.push(m.text()));
  page.on("pageerror", (e) => pageErrors.push(String(e)));

  await bootCompositor(page);
  console.log("ok  compositor booted");

  // === Step 1: spawn terminal + drive it ====================================
  // The desktop ships with multiple welcome windows; the most recently
  // spawned window is on top BUT subsequent spawns (quake auto-launched by
  // the compositor) may bury it. We poke the terminal's titlebar
  // unconditionally after spawn so the click raises it above any sibling.
  await spawn(page, "clients/terminal/worker.js");

  let shot = await page.screenshot({ type: "png", fullPage: false });
  let png = PNG.sync.read(shot);
  const termBounds = findWindowBounds(png, TERM_BG);
  if (!termBounds) {
    fail("terminal window not visible after spawn");
    throw new Error("abort");
  }
  console.log(`ok  terminal window @ (${termBounds.x},${termBounds.y}) ${termBounds.w}x${termBounds.h}`);

  // Click on the terminal's titlebar (just above the panel) to raise the
  // window above any sibling that may be overlapping. The panel BG starts
  // at termBounds.y; the titlebar sits 18px above.
  await page.mouse.click(termBounds.x + Math.floor(termBounds.w/2), termBounds.y - 8);
  await page.waitForTimeout(200);
  // Click inside the panel to make sure the wm has focused the terminal's
  // input pipeline.
  await page.mouse.click(termBounds.x + Math.floor(termBounds.w/2), termBounds.y + Math.floor(termBounds.h/2));
  await page.waitForTimeout(150);
  // Re-scan in case bringing-to-front moved the panel slightly.
  shot = await page.screenshot({ type: "png", fullPage: false });
  png = PNG.sync.read(shot);
  const termRaised = findWindowBounds(png, TERM_BG);
  if (termRaised && termRaised.w > termBounds.w * 0.9) {
    termBounds.x = termRaised.x;
    termBounds.y = termRaised.y;
    termBounds.w = termRaised.w;
    termBounds.h = termRaised.h;
    console.log(`ok  terminal raised @ (${termBounds.x},${termBounds.y}) ${termBounds.w}x${termBounds.h}`);
  }

  // Drive the shell. The compositor's input loop sends individual keypress
  // events for each character, so type each command, press Enter, repeat.
  const cmds = [
    "mkdir /scratch",
    "cd /scratch",
    "touch hello.txt",
    'echo "hi there" > hello.txt',
    "cat hello.txt",
  ];
  for (const cmd of cmds) {
    await page.keyboard.type(cmd, { delay: 5 });
    await page.keyboard.press("Enter");
    await page.waitForTimeout(200);
  }

  shot = await page.screenshot({ type: "png", path: SHOT_TERM, fullPage: false });
  png = PNG.sync.read(shot);
  const inkPixels = countPixels(png, termBounds.x, termBounds.y, termBounds.w, termBounds.h, TERM_INK);
  console.log(`info terminal ink pixels = ${inkPixels}`);
  if (inkPixels < 400) {
    fail(`expected substantial cat-output ink; got ${inkPixels}`);
  } else {
    console.log(`ok  cat hello.txt produced ${inkPixels} ink pixels (proof "hi there" rendered)`);
  }
  // The prompt is now `/scratch $ `: each prompt line carries a run of cyan
  // (PaletteFG[1] = 0x8a,0xe1,0xff) ink for the "/<cwd> $ " text. Scan the
  // whole terminal window for cyan pixels -- a fresh terminal with one
  // prompt has ~30 px (3 chars: "/", " ", "$"); after our sequence we
  // expect several hundred (5 prompt lines, one of them spelling /scratch).
  const cyanPixels = countPixels(png, termBounds.x, termBounds.y, termBounds.w, termBounds.h, TERM_CYAN);
  if (cyanPixels < 200) {
    fail(`expected cwd-aware prompt cyan ink, got ${cyanPixels}`);
  } else {
    console.log(`ok  prompt cyan pixels = ${cyanPixels} (cwd-aware prompt rendered)`);
  }
  console.log(`ok  saved ${SHOT_TERM}`);

  // === Step 2: spawn files, navigate to /scratch ============================
  await spawn(page, "clients/files/worker.js");

  shot = await page.screenshot({ type: "png", fullPage: false });
  png = PNG.sync.read(shot);
  const filesBounds = findWindowBounds(png, FILES_SIDEBAR_BG);
  if (!filesBounds) {
    fail("files window not visible after spawn");
    throw new Error("abort");
  }
  // The sidebar starts at HEADER_BAR_HEIGHT below the surface origin (the
  // header bar is window-bg, not sidebar-bg). The bounds we get are sidebar-
  // only; the surface origin is bounds.x + 0 = bounds.x, bounds.y -
  // HEADER_BAR_HEIGHT.
  const surf = { x: filesBounds.x, y: filesBounds.y - HEADER_BAR_HEIGHT, w: FILES_W, h: FILES_H };
  console.log(`ok  files surface @ (${surf.x},${surf.y}) ${surf.w}x${surf.h}`);

  // /scratch is a top-level directory, so it should appear in the root
  // listing. The files browser already shows the root; the root listing
  // refreshes only when the page re-reads. Since the files client just
  // booted, its IDB.LoadAll already picked up the persisted tree -> /scratch
  // is in the root listing.
  //
  // Click on /scratch's row to descend into it. The root listing order is
  // dirs first alphabetically: Documents, Downloads, Pictures, scratch, then
  // about.txt. Row 3 is /scratch.
  const listY0 = surf.y + HEADER_BAR_HEIGHT + COLUMN_HEADER_HEIGHT;
  const scratchRowY = listY0 + 3 * ROW_HEIGHT + Math.floor(ROW_HEIGHT/2);
  const rowClickX = surf.x + SIDEBAR_WIDTH + 50;
  await page.mouse.click(rowClickX, scratchRowY);
  await page.waitForTimeout(300);

  shot = await page.screenshot({ type: "png", path: SHOT_FILES, fullPage: false });
  png = PNG.sync.read(shot);

  // Inside /scratch we expect to see exactly one row: hello.txt. The selected
  // row paints ColorAccent across the list column. Sample inside the right
  // pane on row 0.
  const accentRow0 = countPixels(png, surf.x + SIDEBAR_WIDTH, listY0, FILES_W - SIDEBAR_WIDTH, ROW_HEIGHT, FILES_ACCENT);
  if (accentRow0 < 100) {
    fail(`/scratch row 0 accent pixels = ${accentRow0}, want >= 100 (hello.txt not selected?)`);
  } else {
    console.log(`ok  /scratch row 0 accent pixels = ${accentRow0} (hello.txt listed + selected)`);
  }
  // The list pane should NOT carry any text-primary ink at row 1 (the dir is
  // single-row: hello.txt and nothing else).
  const row1Primary = countPixels(png, surf.x + SIDEBAR_WIDTH + 50, listY0 + ROW_HEIGHT, FILES_W - SIDEBAR_WIDTH - 60, ROW_HEIGHT, FILES_TEXT);
  if (row1Primary > 30) {
    fail(`/scratch row 1 unexpectedly has ${row1Primary} primary-ink pixels (extra rows?)`);
  } else {
    console.log(`ok  /scratch shows exactly one row (row 1 primary-ink = ${row1Primary})`);
  }
  console.log(`ok  saved ${SHOT_FILES}`);

  // === Step 3: reload, spawn files only, navigate to /scratch ==============
  console.log("step3: reloading page to verify IDB persistence...");
  await page.reload({ waitUntil: "load" });
  await page.waitForFunction(
    () => globalThis.wasmboxReady === true,
    { timeout: BOOT_TIMEOUT_MS },
  );
  console.log("ok  compositor re-booted");

  await spawn(page, "clients/files/worker.js");

  shot = await page.screenshot({ type: "png", fullPage: false });
  png = PNG.sync.read(shot);
  const filesBounds2 = findWindowBounds(png, FILES_SIDEBAR_BG);
  if (!filesBounds2) {
    fail("files window not visible after reload");
    throw new Error("abort");
  }
  const surf2 = { x: filesBounds2.x, y: filesBounds2.y - HEADER_BAR_HEIGHT, w: FILES_W, h: FILES_H };
  console.log(`ok  files (post-reload) surface @ (${surf2.x},${surf2.y})`);

  // Bring the Files window to the front in case a re-spawned quake window
  // (the compositor auto-launches it at boot) is overlapping. Clicking the
  // titlebar both raises and focuses without firing any in-surface input
  // (the titlebar lives above the surface origin so it is compositor chrome,
  // not file-browser content).
  await page.mouse.click(surf2.x + Math.floor(FILES_W/2), surf2.y - 8);
  await page.waitForTimeout(200);

  const listY02 = surf2.y + HEADER_BAR_HEIGHT + COLUMN_HEADER_HEIGHT;
  // Click /scratch row 3 again. Order: Documents (0), Downloads (1),
  // Pictures (2), scratch (3), about.txt (4).
  await page.mouse.click(surf2.x + SIDEBAR_WIDTH + 50, listY02 + 3 * ROW_HEIGHT + Math.floor(ROW_HEIGHT/2));
  await page.waitForTimeout(300);

  shot = await page.screenshot({ type: "png", path: SHOT_PERSIST, fullPage: false });
  png = PNG.sync.read(shot);
  const accentRow0After = countPixels(png, surf2.x + SIDEBAR_WIDTH, listY02, FILES_W - SIDEBAR_WIDTH, ROW_HEIGHT, FILES_ACCENT);
  if (accentRow0After < 100) {
    fail(`POST-RELOAD /scratch row 0 accent pixels = ${accentRow0After}; want >= 100 (hello.txt did NOT persist)`);
  } else {
    console.log(`ok  POST-RELOAD /scratch row 0 accent = ${accentRow0After}`);
  }
  // Strong persistence test: /scratch is a one-file dir, so row 1 of the
  // list MUST be empty (no primary-ink). If hello.txt didn't persist we'd
  // either still be at Home (multiple rows visible -- many primary-ink
  // pixels at row 1) or in an empty /scratch (no accent at row 0). The
  // combination of "row 0 accent + row 1 empty" pinpoints "we navigated
  // into /scratch AND hello.txt is there".
  const row1PrimaryAfter = countPixels(png, surf2.x + SIDEBAR_WIDTH + 50, listY02 + ROW_HEIGHT, FILES_W - SIDEBAR_WIDTH - 60, ROW_HEIGHT, FILES_TEXT);
  if (row1PrimaryAfter > 30) {
    fail(`POST-RELOAD row 1 has ${row1PrimaryAfter} primary-ink pixels; expected /scratch to be a one-row directory`);
  } else {
    console.log(`ok  POST-RELOAD /scratch shows exactly one row (row1 primary-ink=${row1PrimaryAfter}) -- hello.txt persisted`);
  }
  console.log(`ok  saved ${SHOT_PERSIST}`);

  if (pageErrors.length) {
    fail(`pageerror(s): ${pageErrors.join(" | ")}`);
  } else {
    console.log("ok  no pageerror");
  }
} catch (e) {
  if (String(e).indexOf("abort") < 0) fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
