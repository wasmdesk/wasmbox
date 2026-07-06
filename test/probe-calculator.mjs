// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Headless Playwright probe for the Calculator client (clients/calculator),
// which now renders with the WhiteSur (macOS Big Sur) light palette. Boots the
// compositor, spawns a Calculator window, samples the WhiteSur roles + saves a
// screenshot to /tmp/calc-whitesur.png for visual review.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const SHOT = "/tmp/calc-whitesur.png";

// WhiteSur light roles (from clients/calculator/internal/scene/whitesur-light.css).
const WINDOW_BG = [245, 245, 245]; // #f5f5f5
const ACCENT = [8, 96, 242]; // #0860F2

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
  for (let i = 0; i < png.data.length; i += 4) {
    if (Math.abs(png.data[i] - c[0]) <= tol && Math.abs(png.data[i + 1] - c[1]) <= tol && Math.abs(png.data[i + 2] - c[2]) <= tol) n++;
  }
  return n;
};

const { server, base } = await startServer();
const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  const errs = [];
  page.on("pageerror", (e) => errs.push(String(e)));
  await page.goto(`${base}/index.html`, { waitUntil: "load" });
  await page.waitForFunction(() => {
    if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
    return globalThis.wasmboxReady === true;
  }, { timeout: 15000 });
  console.log("ok  compositor booted");
  await page.evaluate(() => globalThis.wasmboxSpawnExternal("clients/calculator/worker.js"));
  await page.waitForTimeout(3000);
  await page.screenshot({ type: "png", path: SHOT, fullPage: false });
  const png = PNG.sync.read(await readFile(SHOT));
  const winBG = countColor(png, WINDOW_BG);
  const accent = countColor(png, ACCENT);
  console.log(`window_bg #f5f5f5 px: ${winBG}`);
  console.log(`accent    #0860F2 px: ${accent}`);
  console.log(`pageerrors: ${errs.length ? errs.join(" | ") : "none"}`);
  console.log(`saved: ${SHOT}`);
  // The WhiteSur window ground must be visibly present (thousands of px).
  process.exitCode = winBG > 2000 ? 0 : 1;
  console.log(winBG > 2000 ? "PASS ✅ WhiteSur window ground present" : "FAIL ❌ no WhiteSur ground");
} finally {
  await browser.close();
  server.close();
}
