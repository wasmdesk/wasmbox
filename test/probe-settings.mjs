// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Headless Playwright probe for the WhiteSur Settings client
// (clients/settings). Boots the compositor, spawns a Settings window, raises
// it, samples the WhiteSur roles, exercises a switch toggle, and saves a
// screenshot to /tmp/settings-whitesur.png.

import { createServer } from "node:http";
import { readFile, writeFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const SHOT = "/tmp/settings-whitesur.png";
const WINDOW_BG = [245, 245, 245]; // #f5f5f5 sidebar ground
const ACCENT = [8, 96, 242]; // #0860F2 selected pill + on-switch track

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

const countColor = (png, c, tol = 2) => {
  let n = 0;
  for (let i = 0; i < png.data.length; i += 4)
    if (Math.abs(png.data[i] - c[0]) <= tol && Math.abs(png.data[i + 1] - c[1]) <= tol && Math.abs(png.data[i + 2] - c[2]) <= tol) n++;
  return n;
};
// Locate the Settings window: the bounding box of its #f5f5f5 sidebar ground.
const findWindow = (png, c) => {
  let minX = png.width, minY = png.height, maxX = -1, maxY = -1;
  for (let y = 0; y < png.height; y++) for (let x = 0; x < png.width; x++) {
    const i = (y * png.width + x) * 4;
    if (png.data[i] === c[0] && png.data[i + 1] === c[1] && png.data[i + 2] === c[2]) {
      if (x < minX) minX = x; if (y < minY) minY = y; if (x > maxX) maxX = x; if (y > maxY) maxY = y;
    }
  }
  return maxX < 0 ? null : { x: minX, y: minY, w: maxX - minX + 1, h: maxY - minY + 1 };
};

const { server, base } = await startServer();
const browser = await chromium.launch({ headless: true });
const out = {};
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  const errs = [];
  page.on("pageerror", (e) => errs.push(String(e)));
  await page.goto(`${base}/index.html`, { waitUntil: "load" });
  await page.waitForFunction(() => {
    if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
    return globalThis.wasmboxReady === true;
  }, { timeout: 15000 });
  await page.evaluate(() => globalThis.wasmboxSpawnExternal("clients/settings/worker.js"));
  await page.waitForTimeout(2500);

  // Raise Settings above any demo windows by clicking its title bar (~28px
  // above the sidebar block's origin).
  let png = PNG.sync.read(await page.screenshot({ type: "png" }));
  const win = findWindow(png, WINDOW_BG);
  out.located = win;
  if (win) { await page.mouse.click(win.x + 200, win.y - 14); await page.waitForTimeout(400); }

  // Ensure the "Appearance" page (sidebar row 0) is selected -- findWindow only
  // spans the #f5f5f5 sidebar, so use the fixed window width (640, from
  // worker.js) to reach the content-pane controls.
  const FULL_W = 640, SIDEBAR_W = 180, PAD = 22, SWITCH_W = 46;
  if (win) {
    await page.mouse.click(win.x + 40, win.y + 48 + 18); // "Appearance" row
    await page.waitForTimeout(300);
  }
  png = PNG.sync.read(await page.screenshot({ type: "png" }));
  out.accentBefore = countColor(png, ACCENT);

  // Toggle the "Dark Mode" switch (content row 0, right edge). off->on turns
  // its track accent-blue, so accent px must rise.
  if (win) {
    const switchCx = win.x + FULL_W - PAD - SWITCH_W / 2;
    const switchCy = win.y + 58 + 26; // contentTop + rowH/2 (row 0)
    await page.mouse.click(switchCx, switchCy);
    await page.waitForTimeout(300);
  }
  png = PNG.sync.read(await page.screenshot({ type: "png" }));
  out.accentAfterToggle = countColor(png, ACCENT);

  await writeFile(SHOT, PNG.sync.write(png));
  out.pageerrors = errs;
  out.windowGroundPx = countColor(png, WINDOW_BG);
} finally {
  await browser.close();
  server.close();
}

console.log(JSON.stringify(out, null, 2));
const pass = out.located && out.windowGroundPx > 2000 && out.accentBefore > 100 &&
  out.accentAfterToggle > out.accentBefore && (out.pageerrors || []).length === 0;
console.log(pass ? "\nPASS ✅ Settings renders WhiteSur; switch toggle increased accent" : "\nFAIL ❌");
process.exit(pass ? 0 : 1);
