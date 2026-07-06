// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Headless Playwright probe for the WhiteSur/Safari browser client
// (clients/browser). Boots the compositor, spawns a Browser window, raises it,
// verifies the Favourites start page (accent tile icons), clicks a tile to
// navigate to a placeholder site page (content turns white), and saves a
// screenshot to /tmp/browser-whitesur.png.

import { createServer } from "node:http";
import { readFile, writeFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const SHOT = "/tmp/browser-whitesur.png";
const CONTENT_BG = [245, 245, 245]; // #f5f5f5 start-page content ground
const WHITE = [255, 255, 255]; // site page ground
const ACCENT = [8, 96, 242]; // tile icon squares

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
// Locate the browser's content area by its #f5f5f5 start-page ground.
const findContent = (png, c) => {
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
  await page.evaluate(() => globalThis.wasmboxSpawnExternal("clients/browser/worker.js"));
  await page.waitForTimeout(2500);

  // Locate the content area, then raise the window by clicking its title bar
  // (~14px above the toolbar, which is ~46px above the content origin).
  let png = PNG.sync.read(await page.screenshot({ type: "png" }));
  const c = findContent(png, CONTENT_BG);
  out.content = c;
  if (c) { await page.mouse.click(c.x + 300, c.y - 60); await page.waitForTimeout(400); }

  png = PNG.sync.read(await page.screenshot({ type: "png" }));
  out.accentStart = countColor(png, ACCENT); // tile icon squares on the start page
  out.whiteStart = countColor(png, WHITE);

  // Click favourite tile 0 (grid left 40, first row 56px below content origin,
  // tile 150x98 -> centre ~ content.x+115, content.y+105).
  if (c) { await page.mouse.click(c.x + 115, c.y + 105); await page.waitForTimeout(400); }
  png = PNG.sync.read(await page.screenshot({ type: "png" }));
  out.whiteAfterNav = countColor(png, WHITE); // site page turns the content white

  await writeFile(SHOT, PNG.sync.write(png));
  out.pageerrors = errs;
} finally {
  await browser.close();
  server.close();
}

console.log(JSON.stringify(out, null, 2));
const pass = out.content && out.accentStart > 200 &&
  out.whiteAfterNav > out.whiteStart + 20000 && (out.pageerrors || []).length === 0;
console.log(pass ? "\nPASS ✅ Browser start page + navigate-to-site works" : "\nFAIL ❌");
process.exit(pass ? 0 : 1);
