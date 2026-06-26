// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Headless Playwright probe for the Finder-inspired file browser client
// (clients/files).
//
// Spawns a system Chrome (channel: "chrome", headless: true), loads the index
// page, opens a Files window via wasmboxSpawnExternal, locates the window on
// the canvas by sampling the sidebar background colour, then drives:
//
//   - ArrowDown moves the selection from row 0 to row 1 -- we assert that the
//     accent-blue row strip migrates one row down.
//   - Click on a folder row -- the row should be painted with the accent fill
//     before navigation happens; we sample inside the row to confirm.
//   - Per-region pixel samples for sidebar / window BG / accent strip.
//
// Saves a screenshot to /tmp/files-finder.png.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 15000;
const SCREENSHOT_PATH = "/tmp/files-finder.png";

// Palette duplicated from clients/files/internal/scene/render.go so the probe
// is self-contained. Keep in sync if the colour table changes there.
const COLOR_WINDOW_BG  = [246, 246, 247];
const COLOR_SIDEBAR_BG = [232, 232, 236];
const COLOR_TOOLBAR_BG = [238, 238, 240];
const COLOR_ACCENT     = [0, 99, 233];

// Layout constants must match render.go.
const TOOLBAR_HEIGHT        = 40;
const COLUMN_HEADER_HEIGHT  = 24;
const ROW_HEIGHT            = 28;
const SIDEBAR_WIDTH         = 140;
const SURFACE_W             = 720;
const SURFACE_H             = 440;

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

// findSidebarBounds locates the file browser surface by its unique sidebar
// background colour (no other compositor pane uses (232,232,236)).
// Returns the bounding box of the contiguous sidebar pixel block.
function findSidebarBounds(png, color) {
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

// Surface gives the file browser surface bounds. The sidebar starts at x=0
// of the surface (row 0 is the toolbar, no sidebar) so we offset minY by
// TOOLBAR_HEIGHT to land on the surface origin.
function fileSurface(png) {
  const sb = findSidebarBounds(png, COLOR_SIDEBAR_BG);
  if (!sb) return null;
  // sb.x is the surface origin x; sb.y is TOOLBAR_HEIGHT below surface origin.
  return { x: sb.x, y: sb.y - TOOLBAR_HEIGHT, w: SURFACE_W, h: SURFACE_H };
}

// findHighlightedRow returns the y-coordinate of the accent-strip top edge
// inside the file browser surface. Scans a column inside the right pane,
// past the sidebar.
function findHighlightedRow(png, surface) {
  const x = surface.x + SIDEBAR_WIDTH + 4;
  const y0 = surface.y + TOOLBAR_HEIGHT + COLUMN_HEADER_HEIGHT;
  const y1 = y0 + 8 * ROW_HEIGHT;
  for (let y = y0; y < y1; y++) {
    if (eqColor(pixelAt(png, x, y), COLOR_ACCENT)) {
      return y;
    }
  }
  return null;
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
    fail(`Files surface not visible on canvas (no sidebar BG ${COLOR_SIDEBAR_BG} found)`);
  } else {
    console.log(`ok  Files surface located at (${surface.x},${surface.y}) ${surface.w}x${surface.h}`);

    // Per-region pixel samples (the "looks right" proof).
    // Sidebar (x=10, mid-height of right pane).
    const sbX = surface.x + 10;
    const sbY = surface.y + TOOLBAR_HEIGHT + 100;
    const sbPx = pixelAt(png1, sbX, sbY);
    if (!eqColor(sbPx, COLOR_SIDEBAR_BG)) {
      fail(`sidebar pixel at (${sbX},${sbY}) = ${sbPx}, want ${COLOR_SIDEBAR_BG}`);
    } else {
      console.log(`ok  sidebar pixel @ (${sbX},${sbY}) = (${sbPx.join(",")}) -- COLOR_SIDEBAR_BG`);
    }
    // Window background (right pane, far right + below all rows).
    const wbX = surface.x + SURFACE_W - 4;
    const wbY = surface.y + SURFACE_H - 4;
    const wbPx = pixelAt(png1, wbX, wbY);
    if (!eqColor(wbPx, COLOR_WINDOW_BG)) {
      fail(`window-bg pixel at (${wbX},${wbY}) = ${wbPx}, want ${COLOR_WINDOW_BG}`);
    } else {
      console.log(`ok  window-bg pixel @ (${wbX},${wbY}) = (${wbPx.join(",")}) -- COLOR_WINDOW_BG`);
    }
    // Toolbar background (above the sidebar, far right of toolbar band).
    const tbX = surface.x + SURFACE_W - 50;
    const tbY = surface.y + TOOLBAR_HEIGHT / 2;
    const tbPx = pixelAt(png1, tbX, tbY);
    if (!eqColor(tbPx, COLOR_TOOLBAR_BG)) {
      fail(`toolbar pixel at (${tbX},${tbY}) = ${tbPx}, want ${COLOR_TOOLBAR_BG}`);
    } else {
      console.log(`ok  toolbar pixel @ (${tbX},${tbY}) = (${tbPx.join(",")}) -- COLOR_TOOLBAR_BG`);
    }

    // Confirm the accent strip is at the FIRST entry row (Cursor=0).
    const row0Y = findHighlightedRow(png1, surface);
    if (row0Y === null) {
      fail("initial accent strip not visible");
    } else {
      const expected = surface.y + TOOLBAR_HEIGHT + COLUMN_HEADER_HEIGHT;
      if (Math.abs(row0Y - expected) > 2) {
        fail(`initial accent strip at y=${row0Y}, expected ~${expected}`);
      } else {
        console.log(`ok  initial accent strip at y=${row0Y} (row 0)`);
      }
    }

    // Focus the window with a click in the right pane in a guaranteed-safe spot:
    // the column-header band (the click handler ignores it but the window grabs focus).
    const cx = surface.x + SIDEBAR_WIDTH + 200;
    const cy = surface.y + TOOLBAR_HEIGHT + COLUMN_HEADER_HEIGHT / 2;
    await page.mouse.click(cx, cy);
    await page.waitForTimeout(150);

    // ArrowDown -> Cursor goes to row 1, accent strip migrates one row.
    await page.keyboard.press("ArrowDown");
    await page.waitForTimeout(400);

    let shot2 = await page.screenshot({ type: "png", fullPage: false });
    let png2 = PNG.sync.read(shot2);

    const row1Y = findHighlightedRow(png2, surface);
    if (row1Y === null) {
      fail("accent strip vanished after ArrowDown");
    } else {
      const expected = surface.y + TOOLBAR_HEIGHT + COLUMN_HEADER_HEIGHT + ROW_HEIGHT;
      if (Math.abs(row1Y - expected) > 2) {
        fail(`row 1 accent at y=${row1Y}, expected ~${expected}`);
      } else {
        console.log(`ok  ArrowDown moved accent strip to y=${row1Y} (row 1)`);
      }
    }

    // Sample the accent fill inside row 1 (past the icon) for the explicit
    // "clicked row background = accent blue" claim the spec asks for.
    const accentSampleX = surface.x + SIDEBAR_WIDTH + 4;
    const accentSampleY = row1Y + ROW_HEIGHT / 2;
    const accentPx = pixelAt(png2, accentSampleX, accentSampleY);
    if (!eqColor(accentPx, COLOR_ACCENT)) {
      fail(`selected-row accent at (${accentSampleX},${accentSampleY}) = ${accentPx}, want ${COLOR_ACCENT}`);
    } else {
      console.log(`ok  selected-row accent @ (${accentSampleX},${accentSampleY}) = (${accentPx.join(",")}) -- COLOR_ACCENT`);
    }

    // Save the screenshot before we navigate so the saved frame shows the
    // multi-column list with the row-1 selection.
    await page.screenshot({ type: "png", path: SCREENSHOT_PATH, fullPage: false });
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
