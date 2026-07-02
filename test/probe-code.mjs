// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Headless Playwright probe for the VS Code-styled editor client
// (clients/code). Launches bundled Chromium (headless), loads the index page,
// opens a Code window via wasmboxSpawnExternal, then locates + measures the
// window on the canvas from the ACTUAL extent of its unique status-bar blue
// (#007ACC) -- never a hardcoded window size -- and:
//
//   - per-region pixel samples (sidebar / editor / status bar), all anchored
//     to the measured geometry so a smaller-than-nominal window, a right-edge
//     scrollbar, or an occluded top edge can't produce a stale sample
//   - detects the tab strip (#2D2D30) by scanning up an editor column, which
//     both proves it renders and yields the real surface top
//   - types "func main() {" and asserts keyword-blue (#569CD6) ink appears,
//     proving syntax highlighting works end-to-end in the browser
//
// Saves a screenshot to /tmp/wasmdesk-code.png.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 15000;
const SCREENSHOT_PATH = "/tmp/wasmdesk-code.png";

// VS Code Dark+ palette duplicated from clients/code/internal/scene/render.go.
const COLOR_WINDOW_BG     = [0x1E, 0x1E, 0x1E];
const COLOR_SIDEBAR_BG    = [0x25, 0x25, 0x26];
const COLOR_TABSTRIP_BG   = [0x2D, 0x2D, 0x30];
const COLOR_STATUSBAR_BG  = [0x00, 0x7A, 0xCC];
const COLOR_SIDEBAR_TEXT  = [0xCC, 0xCC, 0xCC];
const COLOR_KEYWORD       = [0x56, 0x9C, 0xD6];

// Layout constants (must match render.go). Only the horizontal offsets and the
// nominal tab-strip height are used; the window's actual on-screen width/height
// are measured at runtime from the status-bar blue, not assumed.
const SIDEBAR_WIDTH    = 200;
const TAB_STRIP_HEIGHT = 28;
const GUTTER_WIDTH     = 50;

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

// findCodeSurface locates the editor surface by its unique status-bar blue
// (VS Code's #007ACC). It returns the ACTUAL on-screen geometry derived from
// the extent of that blue strip -- never a hardcoded window size. The code
// window's default size can differ from any nominal constant, so we anchor
// everything to the status bar: `left`/`right` and `statusTop`/`statusBottom`
// are measured, `w` is the true painted width. Callers derive interior sample
// points relative to the status bar (which is always the bottom band of the
// window) and detect the tab strip by scanning -- so a smaller-than-nominal
// window, a right-edge scrollbar, or an occluded top edge can't fool them.
function findCodeSurface(png) {
  const { width, height, data } = png;
  let minX = width, minY = height, maxX = -1, maxY = -1;
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const i = (y * width + x) * 4;
      if (data[i] === COLOR_STATUSBAR_BG[0] && data[i+1] === COLOR_STATUSBAR_BG[1] && data[i+2] === COLOR_STATUSBAR_BG[2]) {
        if (x < minX) minX = x;
        if (y < minY) minY = y;
        if (x > maxX) maxX = x;
        if (y > maxY) maxY = y;
      }
    }
  }
  if (maxX < 0) return null;
  // The status bar is the lowest band of the surface; its blue extent gives
  // the window's true left/right and the editor's bottom boundary.
  return {
    left: minX, right: maxX, w: maxX - minX + 1,
    statusTop: minY, statusBottom: maxY,
    // Back-compat aliases used by legacy callers below.
    x: minX, y: minY,
  };
}

// scanTabStrip walks UP an editor-pane column from just above the status bar
// and returns the first vertical run of tab-strip background (#2D2D30) at
// least 8 px tall -- proving the tab strip renders and yielding the surface's
// real top edge (band.yTop) without assuming any window height.
function scanTabStrip(png, geom) {
  const x = geom.left + SIDEBAR_WIDTH + GUTTER_WIDTH + 100;
  let runBottom = -1;
  for (let y = geom.statusTop - 1; y >= 0 && y > geom.statusTop - 600; y--) {
    if (eqColor(pixelAt(png, x, y), COLOR_TABSTRIP_BG)) {
      if (runBottom < 0) runBottom = y;
    } else if (runBottom >= 0) {
      const len = runBottom - y;
      if (len >= 8) return { yTop: y + 1, yBottom: runBottom, len };
      runBottom = -1;
    }
  }
  return null;
}

const { server, base } = await startServer();
console.log(`probe-code: serving on ${base}`);

const browser = await chromium.launch({ headless: true });
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

  // Spawn the code client.
  await page.evaluate(() => globalThis.wasmboxSpawnExternal("clients/code/worker.js"));
  await page.waitForTimeout(3500);

  // Many other windows are spawned by the compositor at boot (terminal,
  // editor, files, etc.), so the Code window may start partly occluded.
  // Scout for its status bar (the unique #007ACC strip), then click deep
  // in its editor pane -- just above the status bar, well clear of the left
  // sidebar and any occluding window -- to focus + raise it to the top.
  let scoutShot = await page.screenshot({ type: "png", fullPage: false });
  let scoutPng = PNG.sync.read(scoutShot);
  const scoutSurface = findCodeSurface(scoutPng);
  if (scoutSurface) {
    const raiseX = scoutSurface.left + Math.floor(scoutSurface.w / 2);
    const raiseY = scoutSurface.statusTop - 40;
    await page.mouse.click(raiseX, raiseY);
    await page.waitForTimeout(400);
  }

  let shot1 = await page.screenshot({ type: "png", fullPage: false });
  let png1 = PNG.sync.read(shot1);

  const surface = findCodeSurface(png1);
  if (!surface) {
    fail(`Code surface not visible on canvas (no status-bar blue ${COLOR_STATUSBAR_BG} found)`);
  } else {
    console.log(`ok  Code surface located: left=${surface.left} right=${surface.right} w=${surface.w} status y[${surface.statusTop}..${surface.statusBottom}]`);

    // Detect the tab strip by scanning up an editor column; this both proves
    // the strip renders and gives the real surface top (no hardcoded height).
    const tab = scanTabStrip(png1, surface);
    if (!tab) {
      fail(`tab strip (#2D2D30) not found scanning up the editor column`);
    } else {
      console.log(`ok  tab strip band @ y[${tab.yTop}..${tab.yBottom}] (${tab.len}px) -- COLOR_TABSTRIP_BG (#2D2D30)`);
    }
    // Real surface top from the tab strip (fallback: just above status bar).
    const surfTop = tab ? tab.yTop : surface.statusTop - 100;
    // The editor body region: right of sidebar+gutter, above the status bar,
    // and 16px clear of the right edge to dodge the scrollbar track.
    const edLeft  = surface.left + SIDEBAR_WIDTH + GUTTER_WIDTH;
    const edRight = surface.right - 16;
    const edTop   = (tab ? tab.yBottom : surfTop + TAB_STRIP_HEIGHT) + 2;
    const edBot   = surface.statusTop - 1;

    // ---- per-region pixel samples (all anchored to real geometry) ----
    // Sidebar BG: near the bottom-left of the window, below the file tree and
    // clear of any occluding window that sits higher up.
    const sbX = surface.left + 40;
    const sbY = surface.statusTop - 30;
    const sbPx = pixelAt(png1, sbX, sbY);
    if (!eqColor(sbPx, COLOR_SIDEBAR_BG)) {
      fail(`sidebar pixel at (${sbX},${sbY}) = ${sbPx}, want ${COLOR_SIDEBAR_BG}`);
    } else {
      console.log(`ok  sidebar pixel @ (${sbX},${sbY}) = (${sbPx.join(",")}) -- COLOR_SIDEBAR_BG (#252526)`);
    }

    // Editor BG: interior of the empty editor, just above the status bar.
    const ebX = edLeft + 60;
    const ebY = surface.statusTop - 30;
    const ebPx = pixelAt(png1, ebX, ebY);
    if (!eqColor(ebPx, COLOR_WINDOW_BG)) {
      fail(`editor pixel at (${ebX},${ebY}) = ${ebPx}, want ${COLOR_WINDOW_BG}`);
    } else {
      console.log(`ok  editor pixel @ (${ebX},${ebY}) = (${ebPx.join(",")}) -- COLOR_WINDOW_BG (#1E1E1E)`);
    }

    // Status bar BG (left edge -- guaranteed not over any glyph).
    const stX = surface.left + 2;
    const stY = (surface.statusTop + surface.statusBottom) >> 1;
    const stPx = pixelAt(png1, stX, stY);
    if (!eqColor(stPx, COLOR_STATUSBAR_BG)) {
      fail(`status bar pixel at (${stX},${stY}) = ${stPx}, want ${COLOR_STATUSBAR_BG}`);
    } else {
      console.log(`ok  status bar pixel @ (${stX},${stY}) = (${stPx.join(",")}) -- COLOR_STATUSBAR_BG (#007ACC)`);
    }

    // ---- focus the editor + type a keyword ----
    // Click deep in the empty editor (above the status bar) to place the cursor.
    const editorX = edLeft + 40;
    const editorY = surface.statusTop - 40;
    await page.mouse.click(editorX, editorY);
    await page.waitForTimeout(150);

    // Type "func main() {" -- the leading keyword should syntax-highlight
    // in keyword-blue (#569CD6), so we then sample the editor band for
    // pixels of that exact colour.
    await page.keyboard.type("func main() {");
    await page.waitForTimeout(400);

    let shot2 = await page.screenshot({ type: "png", fullPage: false });
    let png2 = PNG.sync.read(shot2);

    // Look for keyword-blue pixels in the editor pane (#569CD6), bounded to
    // the real editor region so no off-window desktop pixels leak in.
    let kwInked = 0;
    let nonBgInked = 0;
    for (let y = edTop; y <= edBot; y++) {
      for (let x = edLeft; x <= edRight; x++) {
        const p = pixelAt(png2, x, y);
        if (eqColor(p, COLOR_KEYWORD)) kwInked++;
        if (!eqColor(p, COLOR_WINDOW_BG)) nonBgInked++;
      }
    }
    if (kwInked < 12) {
      fail(`expected keyword-blue (#569CD6) pixels after typing "func main() {"; got ${kwInked} (non-BG total=${nonBgInked})`);
    } else {
      console.log(`ok  editor keyword-blue ink: ${kwInked} ColorKeyword pixels (#569CD6); non-BG total=${nonBgInked}`);
    }

    // Sidebar entry ink (EXPLORER header + at least one file row).
    let sbInk = 0;
    for (let y = surfTop; y <= surface.statusTop; y++) {
      for (let x = surface.left; x < surface.left + SIDEBAR_WIDTH; x++) {
        if (eqColor(pixelAt(png2, x, y), COLOR_SIDEBAR_TEXT)) sbInk++;
      }
    }
    if (sbInk < 20) {
      fail(`sidebar text ink = ${sbInk}, want >= 20 (file tree empty?)`);
    } else {
      console.log(`ok  sidebar text ink: ${sbInk} ColorSidebarText pixels`);
    }

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
