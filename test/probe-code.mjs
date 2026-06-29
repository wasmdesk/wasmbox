// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Headless Playwright probe for the VS Code-styled editor client
// (clients/code). Spawns a system Chrome (channel: "chrome", headless: true),
// loads the index page, opens a Code window via wasmboxSpawnExternal, locates
// the surface on the canvas by sampling the unique #252526 sidebar BG, then:
//
//   - per-region pixel samples (sidebar / editor / tab strip / status bar)
//   - asserts the status bar is the signature blue #007ACC
//   - types a few chars on the canvas and checks for visible editor ink
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

// Layout constants (must match render.go).
const SIDEBAR_WIDTH    = 200;
const TAB_STRIP_HEIGHT = 28;
const GUTTER_WIDTH     = 50;
const STATUS_BAR_HEIGHT = 24;
const SURFACE_W = 900;
const SURFACE_H = 540;

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

// findSurfaceByColor locates the editor surface by its unique status-bar
// blue: VS Code's #007ACC. Returns the topmost+leftmost match in the canvas.
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
  // The status bar is the lowest StatusBarHeight band of the surface.
  // Top-left of the surface is (minX, maxY - SURFACE_H + 1).
  return { x: minX, y: maxY - SURFACE_H + 1, w: SURFACE_W, h: SURFACE_H };
}

const { server, base } = await startServer();
console.log(`probe-code: serving on ${base}`);

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

  // Spawn the code client.
  await page.evaluate(() => globalThis.wasmboxSpawnExternal("clients/code/worker.js"));
  await page.waitForTimeout(3500);

  // Many other windows are spawned by the compositor at boot (terminal,
  // editor, files, etc.). Take a first snapshot just to find the Code
  // status bar (the unique #007ACC strip), then click on the Code window
  // titlebar to raise it above the others. We compute the titlebar y as
  // (status-bar-bottom - SURFACE_H + 2) — i.e. ~2 px inside the top edge
  // of the surface, which is where the compositor's titlebar sits.
  let scoutShot = await page.screenshot({ type: "png", fullPage: false });
  let scoutPng = PNG.sync.read(scoutShot);
  const scoutSurface = findCodeSurface(scoutPng);
  if (scoutSurface) {
    // Compositor titlebar sits just above the surface origin -- click 10 px
    // above to land on it (the titlebar is ~20 px tall on this compositor).
    await page.mouse.click(scoutSurface.x + 100, scoutSurface.y - 10);
    await page.waitForTimeout(400);
    // Drag the window up + left so it doesn't extend past the canvas.
    await page.mouse.move(scoutSurface.x + 100, scoutSurface.y - 10);
    await page.mouse.down();
    await page.mouse.move(120, 40, { steps: 10 });
    await page.mouse.up();
    await page.waitForTimeout(400);
  }

  let shot1 = await page.screenshot({ type: "png", fullPage: false });
  let png1 = PNG.sync.read(shot1);

  const surface = findCodeSurface(png1);
  if (!surface) {
    fail(`Code surface not visible on canvas (no status-bar blue ${COLOR_STATUSBAR_BG} found)`);
  } else {
    console.log(`ok  Code surface located at (${surface.x},${surface.y}) ${surface.w}x${surface.h}`);

    // ---- per-region pixel samples ----
    // Sidebar BG (well past EXPLORER header, no row at y=200).
    const sbX = surface.x + 100;
    const sbY = surface.y + 200;
    const sbPx = pixelAt(png1, sbX, sbY);
    if (!eqColor(sbPx, COLOR_SIDEBAR_BG)) {
      fail(`sidebar pixel at (${sbX},${sbY}) = ${sbPx}, want ${COLOR_SIDEBAR_BG}`);
    } else {
      console.log(`ok  sidebar pixel @ (${sbX},${sbY}) = (${sbPx.join(",")}) -- COLOR_SIDEBAR_BG (#252526)`);
    }

    // Editor BG (right-pane far right, mid height).
    const ebX = surface.x + SURFACE_W - 16;
    const ebY = surface.y + 200;
    const ebPx = pixelAt(png1, ebX, ebY);
    if (!eqColor(ebPx, COLOR_WINDOW_BG)) {
      fail(`editor pixel at (${ebX},${ebY}) = ${ebPx}, want ${COLOR_WINDOW_BG}`);
    } else {
      console.log(`ok  editor pixel @ (${ebX},${ebY}) = (${ebPx.join(",")}) -- COLOR_WINDOW_BG (#1E1E1E)`);
    }

    // Status bar BG (left edge -- guaranteed not over any glyph).
    const stX = surface.x + 2;
    const stY = surface.y + SURFACE_H - 12;
    const stPx = pixelAt(png1, stX, stY);
    if (!eqColor(stPx, COLOR_STATUSBAR_BG)) {
      fail(`status bar pixel at (${stX},${stY}) = ${stPx}, want ${COLOR_STATUSBAR_BG}`);
    } else {
      console.log(`ok  status bar pixel @ (${stX},${stY}) = (${stPx.join(",")}) -- COLOR_STATUSBAR_BG (#007ACC)`);
    }

    // Tab strip BG (right side of the tab strip, well past the single active tab).
    const tsX = surface.x + SURFACE_W - 16;
    const tsY = surface.y + TAB_STRIP_HEIGHT - 4;
    const tsPx = pixelAt(png1, tsX, tsY);
    if (!eqColor(tsPx, COLOR_TABSTRIP_BG)) {
      fail(`tab strip pixel at (${tsX},${tsY}) = ${tsPx}, want ${COLOR_TABSTRIP_BG}`);
    } else {
      console.log(`ok  tab strip pixel @ (${tsX},${tsY}) = (${tsPx.join(",")}) -- COLOR_TABSTRIP_BG (#2D2D30)`);
    }

    // ---- focus the editor + type a char ----
    // Click in the editor pane (well past sidebar + gutter, well below tab strip).
    const editorX = surface.x + SIDEBAR_WIDTH + GUTTER_WIDTH + 20;
    const editorY = surface.y + TAB_STRIP_HEIGHT + 20;
    await page.mouse.click(editorX, editorY);
    await page.waitForTimeout(150);

    // Type "func main() {" -- the leading keyword should syntax-highlight
    // in keyword-blue (#569CD6), so we then sample the editor band for
    // pixels of that exact colour.
    await page.keyboard.type("func main() {");
    await page.waitForTimeout(400);

    let shot2 = await page.screenshot({ type: "png", fullPage: false });
    let png2 = PNG.sync.read(shot2);

    // Look for keyword-blue pixels in the editor pane (#569CD6).
    let kwInked = 0;
    let nonBgInked = 0;
    for (let y = surface.y + TAB_STRIP_HEIGHT; y < surface.y + SURFACE_H - STATUS_BAR_HEIGHT; y++) {
      for (let x = surface.x + SIDEBAR_WIDTH + GUTTER_WIDTH; x < surface.x + SURFACE_W; x++) {
        const p = pixelAt(png2, x, y);
        if (eqColor(p, COLOR_KEYWORD)) kwInked++;
        if (!eqColor(p, COLOR_WINDOW_BG)) nonBgInked++;
      }
    }
    if (kwInked < 20) {
      fail(`expected keyword-blue (#569CD6) pixels after typing "func main() {"; got ${kwInked} (non-BG total=${nonBgInked})`);
    } else {
      console.log(`ok  editor keyword-blue ink: ${kwInked} ColorKeyword pixels (#569CD6); non-BG total=${nonBgInked}`);
    }

    // Sidebar entry ink (EXPLORER header + at least one file row).
    let sbInk = 0;
    for (let y = surface.y; y < surface.y + SURFACE_H - STATUS_BAR_HEIGHT; y++) {
      for (let x = surface.x; x < surface.x + SIDEBAR_WIDTH; x++) {
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
