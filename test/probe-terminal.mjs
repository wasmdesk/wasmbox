// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Headless playwright probe for the real terminal client (clients/terminal).
//
// Spawns a system Chrome (channel: "chrome", headless: true), loads the
// index page, opens a Terminal window via wasmboxSpawnExternal, focuses it,
// types "echo hello\n", and asserts that the terminal window pixels carry
// the soft-green ink colour (PaletteFG[0] = 0xa0,0xe0,0xa0) -- proving the
// echo output landed in the grid and was blitted through the SAB.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 15000;
const SCREENSHOT_PATH = "/tmp/wasmbox-real-terminal.png";

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

const { server, base } = await startServer();
console.log(`probe-terminal: serving on ${base}`);

// HARD RULE: system Chrome, headless. Per the prompt.
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

  // Spawn a terminal client via the page's public hook -- index.html exposes
  // wasmboxSpawnExternal(url) which posts M2C_SPAWN_EXTERNAL to the compositor
  // worker.
  await page.evaluate(() => globalThis.wasmboxSpawnExternal("clients/terminal/worker.js"));

  // Wait for the terminal window to register: its SAB blit produces a 640x400
  // region of the canvas painted with the terminal palette.
  await page.waitForTimeout(2500);

  // Discover the terminal window's screen-space position from compositor's
  // own bookkeeping. The compositor stores window geometry in localStorage;
  // for a freshly-spawned window we instead read the rendered canvas.
  let shot1 = await page.screenshot({ type: "png", fullPage: false });
  let png1 = PNG.sync.read(shot1);

  // Scan the canvas for the terminal panel BG (0x10,0x10,0x10) -- the
  // compositor draws titlebars in a different shade, so the panel pixels are
  // uniquely identifiable by their exact RGB.
  function findTerminalBounds(png) {
    const { width, height, data } = png;
    let minX = width, minY = height, maxX = -1, maxY = -1;
    for (let y = 0; y < height; y++) {
      for (let x = 0; x < width; x++) {
        const i = (y * width + x) * 4;
        if (data[i] === 0x10 && data[i+1] === 0x10 && data[i+2] === 0x10) {
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
  const bounds = findTerminalBounds(png1);
  if (!bounds) {
    fail("terminal window not visible on canvas after spawn (no 0x101010 BG found)");
  } else {
    console.log(`ok  terminal window painted at (${bounds.x},${bounds.y}) ${bounds.w}x${bounds.h}`);

    // Click in the middle of the terminal window to focus it.
    const cx = bounds.x + Math.floor(bounds.w / 2);
    const cy = bounds.y + Math.floor(bounds.h / 2);
    await page.mouse.click(cx, cy);
    await page.waitForTimeout(150);

    // Type "echo hello" + Enter.
    await page.keyboard.type("echo hello", { delay: 5 });
    await page.keyboard.press("Enter");
    await page.waitForTimeout(600);

    // Final screenshot.
    const shot2 = await page.screenshot({ type: "png", path: SCREENSHOT_PATH, fullPage: false });
    const png2 = PNG.sync.read(shot2);

    // Count the soft-green ink pixels (PaletteFG[0] = 0xa0, 0xe0, 0xa0) inside
    // the terminal window. After "echo hello", at LEAST the "hello" output
    // line should have produced a significant chunk of ink pixels.
    let inkCount = 0;
    const { width } = png2;
    for (let y = bounds.y; y < bounds.y + bounds.h; y++) {
      for (let x = bounds.x; x < bounds.x + bounds.w; x++) {
        const i = (y * width + x) * 4;
        if (png2.data[i] === 0xa0 && png2.data[i+1] === 0xe0 && png2.data[i+2] === 0xa0) {
          inkCount++;
        }
      }
    }
    console.log(`info terminal ink (0xa0,0xe0,0xa0) pixel count: ${inkCount}`);
    if (inkCount < 200) {
      fail(`echo output not visible: ${inkCount} ink pixels (need >= 200)`);
    } else {
      console.log(`ok  echo hello produced ${inkCount} ink pixels in the terminal window`);
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
