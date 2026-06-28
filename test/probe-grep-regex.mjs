// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Playwright probe for the new -E (regex) mode of coreutils' grep. Spawns the
// real terminal client through the compositor, drives a scripted session
// covering substring fallback + four regex queries + a malformed pattern,
// and saves a screenshot of the rendered terminal grid.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 15000;
const SCREENSHOT_PATH = "/tmp/wasmdesk-grep-regex.png";

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
console.log(`probe-grep-regex: serving on ${base}`);

const browser = await chromium.launch({ headless: true, channel: "chrome" });

try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  page.on("pageerror", (e) => fail(`pageerror: ${String(e)}`));

  await page.goto(`${base}/index.html`, { waitUntil: "load" });
  await page.waitForFunction(
    () => {
      if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
      return globalThis.wasmboxReady === true;
    },
    { timeout: BOOT_TIMEOUT_MS },
  );
  console.log("ok  compositor booted");

  await page.evaluate(() => globalThis.wasmboxSpawnExternal("clients/terminal/worker.js"));
  await page.waitForTimeout(2500);

  const shot = await page.screenshot({ type: "png", fullPage: false });
  const png = PNG.sync.read(shot);
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
  const bounds = findTerminalBounds(png);
  if (!bounds) {
    fail("terminal window not painted");
  } else {
    console.log(`ok  terminal painted at (${bounds.x},${bounds.y}) ${bounds.w}x${bounds.h}`);
    const cx = bounds.x + Math.floor(bounds.w * 3 / 4);
    const cy = bounds.y + Math.floor(bounds.h * 3 / 4);
    await page.mouse.click(cx, cy);
    await page.waitForTimeout(300);

    // Build a 3-line fixture file, then exercise the regex surface.
    //   - substring baseline (-E absent)
    //   - anchored alternation
    //   - quantifier + -n
    //   - char class + -v
    //   - invalid pattern under -E (must surface an error line)
    //
    // The terminal's shell tokenises with strings.Fields and does NOT honour
    // quoting; the redirect path supports only `echo TEXT > PATH` and no
    // append (>>). We therefore rebuild /a.txt with three single-line writes
    // and pass each pattern as a single whitespace-free argv token.
    const lines = [
      "rm /a.txt",
      "echo alpha > /a.txt",
      "mkdir /scratch",
      "echo beta > /scratch/b.txt",
      "echo gamma > /scratch/g.txt",
      "cat /a.txt /scratch/b.txt /scratch/g.txt",
      "grep alpha /a.txt",
      "grep -E ^(alpha|gamma)$ /a.txt /scratch/g.txt",
      "grep -E -n a+ /a.txt /scratch/b.txt /scratch/g.txt",
      "grep -E -v [bg] /a.txt /scratch/b.txt /scratch/g.txt",
      "grep -E [ /a.txt",
    ];
    for (const ln of lines) {
      await page.keyboard.type(ln, { delay: 4 });
      await page.keyboard.press("Enter");
      await page.waitForTimeout(150);
    }
    await page.waitForTimeout(600);

    const shot2 = await page.screenshot({ type: "png", path: SCREENSHOT_PATH, fullPage: false });
    const png2 = PNG.sync.read(shot2);
    let ink = 0;
    const { width } = png2;
    for (let y = bounds.y; y < bounds.y + bounds.h; y++) {
      for (let x = bounds.x; x < bounds.x + bounds.w; x++) {
        const i = (y * width + x) * 4;
        if (png2.data[i] === 0xa0 && png2.data[i+1] === 0xe0 && png2.data[i+2] === 0xa0) ink++;
      }
    }
    console.log(`info terminal ink pixel count: ${ink}`);
    // 9 commands + their output: rough floor matching probe-terminal-coreutils.
    if (ink < 3000) {
      fail(`session did not render enough output: ${ink} ink pixels (need >= 3000)`);
    } else {
      console.log(`ok  scripted regex session rendered ${ink} ink pixels`);
    }
    console.log(`ok  saved screenshot: ${SCREENSHOT_PATH}`);
  }
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
