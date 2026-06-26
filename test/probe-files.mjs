// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Headless Playwright probe for the real file browser client (clients/files).
//
// Spawns a system Chrome (channel: "chrome", headless: true), loads the index
// page, opens a Files window via wasmboxSpawnExternal, locates the window on
// the canvas by sampling the cream panel-bg colour, then drives navigation:
//
//   - ArrowDown moves the selection from row 0 to row 1 -- we assert that the
//     blue highlight strip migrates one row down.
//   - Enter on row 0 (Documents/) descends into /Documents -- we assert the
//     path-bar pixels changed (different glyphs at the same coordinates).
//
// Saves a screenshot to /tmp/wasmbox-real-files.png.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 15000;
const SCREENSHOT_PATH = "/tmp/wasmbox-real-files.png";

// Palette duplicated from clients/files/internal/scene/render.go so the probe
// is self-contained. Keep in sync if the colour table changes there.
const COLOR_BG = [0xf2, 0xee, 0xe4];
const COLOR_HIGHLIGHT_BG = [0x2f, 0x6f, 0xd6];
const COLOR_PATH_BAR_BG = [0x2a, 0x2e, 0x36];

// Layout constants must match render.go.
const PATH_BAR_HEIGHT = 24;
const ROW_HEIGHT = 24;

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

// pixelAt reads the RGBA32 sample at (x,y) from a PNG.
function pixelAt(png, x, y) {
  const i = (y * png.width + x) * 4;
  return [png.data[i], png.data[i+1], png.data[i+2]];
}

function eqColor(px, c) { return px[0] === c[0] && px[1] === c[1] && px[2] === c[2]; }

// findPathBarBounds locates the file browser surface by its dark path-bar
// strip (a unique colour, since other compositor strips use different shades).
// Returns the bounding box of the contiguous path-bar pixel block.
function findPathBarBounds(png, color) {
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

// Surface gives the file browser surface bounds: x/y is the top-left of the
// internal 480x360 RGBA buffer the wasm paints into. We derive that from the
// path-bar block (the path bar sits at the very top of the surface, row 0).
function fileSurface(png) {
  const pb = findPathBarBounds(png, COLOR_PATH_BAR_BG);
  if (!pb) return null;
  return { x: pb.x, y: pb.y, w: pb.w };
}

// findHighlightedRow returns the y-coordinate of the highlight strip's top
// edge inside the file browser surface. Scans the leftmost column of the
// surface; the highlight bar spans the full row width so this is reliable.
function findHighlightedRow(png, surface) {
  const x = surface.x;
  // Entry rows start at surface.y + PATH_BAR_HEIGHT.
  for (let y = surface.y + PATH_BAR_HEIGHT; y < surface.y + PATH_BAR_HEIGHT + 8*ROW_HEIGHT; y++) {
    if (eqColor(pixelAt(png, x, y), COLOR_HIGHLIGHT_BG)) {
      return y;
    }
  }
  return null;
}

// hashPathBar returns a numeric fingerprint of the path bar's pixel content
// (a sum of ink-vs-bg bits). Used to detect a CHANGE in the path string after
// navigation, without OCR.
function hashPathBar(png, surface) {
  let acc = 0;
  const y0 = surface.y + 1;
  const y1 = surface.y + PATH_BAR_HEIGHT - 1;
  const x0 = surface.x + 4;
  const x1 = surface.x + Math.min(surface.w, 240);
  for (let y = y0; y < y1; y++) {
    for (let x = x0; x < x1; x++) {
      const px = pixelAt(png, x, y);
      // Any pixel NOT path-bar BG contributes its position to the hash.
      if (!eqColor(px, COLOR_PATH_BAR_BG)) {
        acc = (acc * 31 + ((y - y0) * 1024 + (x - x0))) | 0;
      }
    }
  }
  return acc;
}

const { server, base } = await startServer();
console.log(`probe-files: serving on ${base}`);

// HARD RULE: system Chrome, headless. Per the prompt.
const browser = await chromium.launch({ headless: true, channel: "chrome" });
const consoleLines = [];
const pageErrors = [];

try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  page.on("console",   (m) => consoleLines.push(m.text()));
  page.on("pageerror", (e) => pageErrors.push(String(e)));

  await page.goto(`${base}/index.html`, { waitUntil: "load" });
  await page.waitForFunction(
    () => {
      if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
      return globalThis.wasmboxReady === true;
    },
    { timeout: BOOT_TIMEOUT_MS },
  );
  console.log("ok  compositor booted");

  // Spawn the files client.
  await page.evaluate(() => globalThis.wasmboxSpawnExternal("clients/files/worker.js"));
  await page.waitForTimeout(2500);

  // Discover the Files window on the canvas by its unique cream panel BG.
  let shot1 = await page.screenshot({ type: "png", fullPage: false });
  let png1 = PNG.sync.read(shot1);

  const surface = fileSurface(png1);
  if (!surface) {
    fail(`Files surface not visible on canvas (no path-bar BG ${COLOR_PATH_BAR_BG} found)`);
  } else {
    console.log(`ok  Files surface painted at (${surface.x},${surface.y}) w=${surface.w}`);

    // Confirm the highlight strip is at the FIRST entry row (Cursor=0).
    const row0Y = findHighlightedRow(png1, surface);
    if (row0Y === null) {
      fail("initial highlight strip not visible");
    } else {
      const expected = surface.y + PATH_BAR_HEIGHT;
      if (Math.abs(row0Y - expected) > 2) {
        fail(`initial highlight at y=${row0Y}, expected ~${expected}`);
      } else {
        console.log(`ok  initial highlight at y=${row0Y} (row 0)`);
      }
    }

    // Snapshot the path-bar fingerprint for "/".
    const path0Hash = hashPathBar(png1, surface);
    console.log(`info path-bar hash @ "/" = ${path0Hash}`);

    // Focus the window with a click in the middle of the surface (below path bar).
    const cx = surface.x + Math.floor(surface.w / 2);
    const cy = surface.y + PATH_BAR_HEIGHT + 4 * ROW_HEIGHT; // below all entries, in panel BG
    await page.mouse.click(cx, cy);
    await page.waitForTimeout(150);

    // ArrowDown -> Cursor goes to row 1.
    await page.keyboard.press("ArrowDown");
    await page.waitForTimeout(400);

    let shot2 = await page.screenshot({ type: "png", fullPage: false });
    let png2 = PNG.sync.read(shot2);

    const row1Y = findHighlightedRow(png2, surface);
    if (row1Y === null) {
      fail("highlight strip vanished after ArrowDown");
    } else {
      const expected = surface.y + PATH_BAR_HEIGHT + ROW_HEIGHT;
      if (Math.abs(row1Y - expected) > 2) {
        fail(`row 1 highlight at y=${row1Y}, expected ~${expected}`);
      } else {
        console.log(`ok  ArrowDown moved highlight from y~${surface.y + PATH_BAR_HEIGHT} to y=${row1Y}`);
      }
    }

    // Now go back UP to row 0, then ENTER on Documents.
    await page.keyboard.press("ArrowUp");
    await page.waitForTimeout(200);
    await page.keyboard.press("Enter");
    await page.waitForTimeout(500);

    let shot3 = await page.screenshot({ type: "png", path: SCREENSHOT_PATH, fullPage: false });
    let png3 = PNG.sync.read(shot3);

    // After navigation, the surface anchor may have shifted (the path bar is
    // now wider/different) — re-locate it on the new screenshot to be safe.
    const surface3 = fileSurface(png3) || surface;

    const path1Hash = hashPathBar(png3, surface3);
    console.log(`info path-bar hash @ "/Documents" = ${path1Hash}`);
    if (path1Hash === path0Hash) {
      fail(`path-bar pixels unchanged after Enter -- navigation did not happen`);
    } else {
      console.log(`ok  Enter changed the path-bar fingerprint (${path0Hash} -> ${path1Hash})`);
    }

    // After Enter the cursor reset to 0 -> the highlight is back at row 0.
    const row0AfterY = findHighlightedRow(png3, surface3);
    if (row0AfterY === null) {
      fail("highlight strip missing after Enter into Documents");
    } else {
      const expected = surface3.y + PATH_BAR_HEIGHT;
      if (Math.abs(row0AfterY - expected) > 2) {
        fail(`post-Enter highlight at y=${row0AfterY}, expected ~${expected}`);
      } else {
        console.log(`ok  after Enter, cursor reset to row 0 (highlight at y=${row0AfterY})`);
      }
    }

    console.log(`ok  saved screenshot: ${SCREENSHOT_PATH}`);
  }

  if (pageErrors.length) {
    fail(`pageerror(s): ${pageErrors.join(" | ")}`);
  } else {
    console.log("ok  no pageerror");
  }
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
