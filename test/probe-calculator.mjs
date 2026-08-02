// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Headless Playwright probe for the Calculator client (clients/calculator),
// which renders with the WhiteSur (macOS Big Sur) light palette AND — as the
// fleet's pilot for anti-aliased text — the toolkit's bundled AA/shaped
// OpenType face (toolkit.UseOpenTypeText, v0.77.0; enabled in scene.New).
//
// Boots the compositor, spawns a Calculator window, samples the WhiteSur roles,
// asserts the AA text signature, and saves a screenshot to /tmp/calc-whitesur.png
// for visual review.
//
// AA assertion: the built-in 5x7 bitmap font paints only fully-lit ink or
// untouched ground, so a bitmap render yields ~0 neutral mid-grey pixels. The
// AA face scan-converts glyph outlines to partial-coverage masks, so its edges
// blend ink (OnSurface 54,54,54) toward the white key ground into hundreds of
// neutral greys (R==G==B, strictly between). We locate the Calculator window via
// its accent-blue operator column, then count those neutral AA greys inside the
// window box — their presence in the thousands is proof AA text actually painted.

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

// accentBBox finds the pixel bounding box of the accent-blue operator column,
// which localises the Calculator window regardless of its cascade position.
const accentBBox = (png, c, tol = 2) => {
  const { width, height, data } = png;
  let minX = Infinity, maxX = -1, minY = Infinity, maxY = -1;
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const o = (y * width + x) * 4;
      if (Math.abs(data[o] - c[0]) <= tol && Math.abs(data[o + 1] - c[1]) <= tol && Math.abs(data[o + 2] - c[2]) <= tol) {
        if (x < minX) minX = x; if (x > maxX) maxX = x;
        if (y < minY) minY = y; if (y > maxY) maxY = y;
      }
    }
  }
  return { minX, maxX, minY, maxY };
};

// aaSignature counts, inside a box, the neutral partial-coverage greys
// (R==G==B, ink < v < ground) that ONLY an anti-aliased face produces, plus the
// solid ink pixels that confirm glyphs were painted at all.
const aaSignature = (png, box) => {
  const { width, height, data } = png;
  const x0 = Math.max(0, box.x0), x1 = Math.min(width, box.x1);
  const y0 = Math.max(0, box.y0), y1 = Math.min(height, box.y1);
  let aaGrey = 0, ink = 0;
  for (let y = y0; y < y1; y++) {
    for (let x = x0; x < x1; x++) {
      const o = (y * width + x) * 4;
      const r = data[o], g = data[o + 1], b = data[o + 2];
      if (r !== g || g !== b) continue;
      if (r > 60 && r < 245) aaGrey++;
      else if (r <= 56) ink++;
    }
  }
  return { aaGrey, ink };
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

  // Locate the Calculator window from its accent operator column, then scan the
  // window box for the anti-aliased-text signature.
  const bb = accentBBox(png, ACCENT);
  const box = { x0: bb.maxX - 260, x1: bb.maxX, y0: bb.minY - 40, y1: bb.maxY + 70 };
  const { aaGrey, ink } = aaSignature(png, box);
  console.log(`aa neutral greys: ${aaGrey}  ink px: ${ink}`);
  console.log(`pageerrors: ${errs.length ? errs.join(" | ") : "none"}`);
  console.log(`saved: ${SHOT}`);

  // The WhiteSur window ground must be visibly present (thousands of px), the
  // accent operator column must be located, and the AA face must have painted
  // hundreds+ of neutral partial-coverage greys plus a solid ink core — the
  // bitmap default would produce ~0 neutral mid-greys.
  const groundOK = winBG > 2000;
  const accentOK = bb.maxX > 0;
  const aaOK = aaGrey > 1000 && ink > 0;
  console.log(groundOK ? "ok  WhiteSur window ground present" : "FAIL ❌ no WhiteSur ground");
  console.log(accentOK ? "ok  accent operator column located" : "FAIL ❌ no accent column");
  console.log(aaOK ? "ok  AA/shaped text painted (neutral greys + ink)" : "FAIL ❌ no AA text signature");
  const pass = groundOK && accentOK && aaOK;
  process.exitCode = pass ? 0 : 1;
  console.log(pass ? "PASS ✅ Calculator renders WhiteSur + AA text" : "FAIL ❌ Calculator probe");
} finally {
  await browser.close();
  server.close();
}
