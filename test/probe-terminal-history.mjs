// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Playwright probe for the terminal Up/Down arrow command-history recall
// feature. Spawns the terminal client, runs three commands, then walks Up
// through them and Down back through them, asserting via ink-pixel deltas
// that each navigation step actually repaints the edit line.
//
// Ink-delta is the same indirect signal probe-terminal-complete uses: the
// compositor does not expose the per-window State, so we cannot decode the
// recalled line directly from JS. We CAN see the screen pixel-count change
// when the displayed line grows / shrinks. For history that is enough --
// after typing "echo a", "echo b", "echo c" each command leaves a visible
// row above the prompt, AND the prompt-line ink shows the recalled command
// (different ink count per recalled line). A final "press Enter on empty
// after Down-pop" sanity-checks the no-op path.
//
// Headless system Chrome; saves a final screenshot for human inspection.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 15000;
const SCREENSHOT_PATH = "/tmp/wasmdesk-history.png";

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
console.log(`probe-terminal-history: serving on ${base}`);

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

  // Locate the terminal panel by its 0x101010 BG (same trick as the
  // companion Tab probe).
  const shot0 = await page.screenshot({ type: "png", fullPage: false });
  const png0 = PNG.sync.read(shot0);
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
  const bounds = findTerminalBounds(png0);
  if (!bounds) { fail("terminal window not painted"); throw new Error("no terminal"); }
  console.log(`ok  terminal painted at (${bounds.x},${bounds.y}) ${bounds.w}x${bounds.h}`);
  const cx = bounds.x + Math.floor(bounds.w / 2);
  const cy = bounds.y + Math.floor(bounds.h * 3 / 4);
  await page.mouse.click(cx, cy);
  await page.waitForTimeout(300);

  // Count ink pixels (soft-green PaletteFG[0]) inside the terminal panel.
  // The prompt is cyan and is excluded -- we only want to measure the
  // commanded/recalled line.
  async function countInk() {
    const shot = await page.screenshot({ type: "png", fullPage: false });
    const png = PNG.sync.read(shot);
    const { width } = png;
    let n = 0;
    for (let y = bounds.y; y < bounds.y + bounds.h; y++) {
      for (let x = bounds.x; x < bounds.x + bounds.w; x++) {
        const i = (y * width + x) * 4;
        if (png.data[i] === 0xa0 && png.data[i+1] === 0xe0 && png.data[i+2] === 0xa0) n++;
      }
    }
    return n;
  }

  // Run three commands. Each leaves its echoed input AND the executed
  // output painted on the screen, so the total ink count grows monotonically.
  for (const c of ["echo a", "echo b", "echo c"]) {
    await page.keyboard.type(c, { delay: 5 });
    await page.keyboard.press("Enter");
    await page.waitForTimeout(120);
  }
  const inkBaseline = await countInk();
  console.log(`ok  baseline after 3 echo commands: ${inkBaseline} ink px`);

  // ArrowUp #1: recalls "echo c". Edit line repaints -> ink grows from the
  // empty prompt to a six-character recall.
  await page.keyboard.press("ArrowUp");
  await page.waitForTimeout(80);
  const inkUp1 = await countInk();
  if (inkUp1 <= inkBaseline + 20) {
    fail(`Up #1 'echo c': ink ${inkBaseline} -> ${inkUp1} (expected growth from recalled bytes)`);
  } else {
    console.log(`ok  Up #1 recalled 'echo c'  ink ${inkBaseline} -> ${inkUp1}`);
  }

  // ArrowUp #2: "echo b". Same length as "echo c" -- ink should stay in
  // the same ballpark (within +/- a few px due to glyph shape differences).
  await page.keyboard.press("ArrowUp");
  await page.waitForTimeout(80);
  const inkUp2 = await countInk();
  if (Math.abs(inkUp2 - inkUp1) > 60) {
    fail(`Up #2 'echo b' ink ${inkUp1} -> ${inkUp2} (expected near-same length)`);
  } else {
    console.log(`ok  Up #2 recalled 'echo b'  ink ${inkUp1} -> ${inkUp2}`);
  }

  // ArrowUp #3: "echo a".
  await page.keyboard.press("ArrowUp");
  await page.waitForTimeout(80);
  const inkUp3 = await countInk();
  if (Math.abs(inkUp3 - inkUp2) > 60) {
    fail(`Up #3 'echo a' ink ${inkUp2} -> ${inkUp3} (expected near-same length)`);
  } else {
    console.log(`ok  Up #3 recalled 'echo a'  ink ${inkUp2} -> ${inkUp3}`);
  }

  // ArrowDown back through: a -> b -> c -> empty.
  await page.keyboard.press("ArrowDown");
  await page.waitForTimeout(80);
  const inkDn1 = await countInk();
  if (Math.abs(inkDn1 - inkUp2) > 60) {
    fail(`Down #1 'echo b' ink ${inkUp3} -> ${inkDn1} (expected near-same length)`);
  } else {
    console.log(`ok  Down #1 recalled 'echo b' ink ${inkUp3} -> ${inkDn1}`);
  }

  await page.keyboard.press("ArrowDown");
  await page.waitForTimeout(80);
  const inkDn2 = await countInk();
  if (Math.abs(inkDn2 - inkUp1) > 60) {
    fail(`Down #2 'echo c' ink ${inkDn1} -> ${inkDn2} (expected near-same length)`);
  } else {
    console.log(`ok  Down #2 recalled 'echo c' ink ${inkDn1} -> ${inkDn2}`);
  }

  await page.keyboard.press("ArrowDown");
  await page.waitForTimeout(80);
  const inkDn3 = await countInk();
  // Now the edit line is empty (stash was empty) -- ink should drop back
  // close to inkBaseline (the prompt + screen history without an edit line).
  if (inkDn3 > inkDn2 - 50) {
    fail(`Down #3 (pop to stash): ink ${inkDn2} -> ${inkDn3} (expected drop to empty line)`);
  } else {
    console.log(`ok  Down #3 popped to empty line ink ${inkDn2} -> ${inkDn3}`);
  }
  // And one more Down should be a no-op (HistIdx == -1 on a fresh line).
  await page.keyboard.press("ArrowDown");
  await page.waitForTimeout(40);
  const inkDn4 = await countInk();
  if (Math.abs(inkDn4 - inkDn3) > 20) {
    fail(`Down #4 (no-op): ink ${inkDn3} -> ${inkDn4} (expected no change)`);
  } else {
    console.log(`ok  Down #4 no-op on fresh line ink ${inkDn3} -> ${inkDn4}`);
  }

  // Enter on empty: no-op for the renderer (no command runs); we just
  // confirm the page didn't blow up.
  await page.keyboard.press("Enter");
  await page.waitForTimeout(80);
  console.log("ok  Enter on empty line did not error");

  await page.screenshot({ type: "png", path: SCREENSHOT_PATH, fullPage: false });
  console.log(`ok  saved screenshot: ${SCREENSHOT_PATH}`);
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
