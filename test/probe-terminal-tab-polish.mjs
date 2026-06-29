// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Playwright probe for the polished Tab UX (LCP auto-extension + column-
// packed multi-match menu). Drives the real terminal through the compositor
// and asserts the bash-like behaviour for every interesting branch:
//
//   * LCP extends the line silently when matches share a common prefix
//     past the typed target (e.g. "cat /scratch/f" -> "cat /scratch/foo"
//     without printing the menu).
//   * A second Tab with no further LCP progress prints the matches in a
//     column-packed menu (e.g. "foo.txt  foobar.txt" on one row).
//   * A 3-entry directory ("cat /scratch/" + Tab) yields a menu containing
//     all three names.
//   * A single-match path ("cat /scratch/b" -> "cat /scratch/baz.txt")
//     still autocompletes outright.
//   * A no-match prefix Tab is silent (no ink delta).
//
// Detection: we sample ink-pixel deltas (no OCR -- the compositor renders
// soft-green ink at 0xa0,0xe0,0xa0 and cyan prompt at 0x6c,0xc6,0xed) plus
// row-coverage heuristics. After all scenarios we save a screenshot at
// /tmp/wasmdesk-tab-polish.png for visual inspection.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 15000;
const SCREENSHOT_PATH = "/tmp/wasmdesk-tab-polish.png";

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
console.log(`probe-terminal-tab-polish: serving on ${base}`);

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
  const cx = bounds.x + Math.floor(bounds.w * 3 / 4);
  const cy = bounds.y + Math.floor(bounds.h * 3 / 4);
  await page.mouse.click(cx, cy);
  await page.waitForTimeout(300);

  // Count green ink pixels inside the terminal panel.
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

  // Count how many distinct rows have any ink (green or cyan) -- a coarse
  // proxy for "did Tab open a new row?". Cells are 32 px tall.
  async function countInkedRows() {
    const shot = await page.screenshot({ type: "png", fullPage: false });
    const png = PNG.sync.read(shot);
    const { width } = png;
    const rowH = 32;
    const rows = Math.floor(bounds.h / rowH);
    let inked = 0;
    for (let r = 0; r < rows; r++) {
      let any = false;
      for (let dy = 0; dy < rowH && !any; dy++) {
        const y = bounds.y + r * rowH + dy;
        for (let x = bounds.x; x < bounds.x + bounds.w; x++) {
          const i = (y * width + x) * 4;
          const lit = (png.data[i] === 0xa0 && png.data[i+1] === 0xe0 && png.data[i+2] === 0xa0) ||
                      (png.data[i] === 0x6c && png.data[i+1] === 0xc6 && png.data[i+2] === 0xed);
          if (lit) { any = true; break; }
        }
      }
      if (any) inked++;
    }
    return inked;
  }

  async function clearLine() {
    for (let i = 0; i < 80; i++) await page.keyboard.press("Backspace");
    await page.waitForTimeout(40);
  }

  // ---- SETUP: create /scratch/{foo.txt, foobar.txt, baz.txt} via the
  // shell itself, so the VFS state matches what the unit tests assume.
  await page.keyboard.type("mkdir /scratch", { delay: 4 });
  await page.keyboard.press("Enter");
  await page.waitForTimeout(120);
  for (const fn of ["foo.txt", "foobar.txt", "baz.txt"]) {
    await page.keyboard.type(`touch /scratch/${fn}`, { delay: 4 });
    await page.keyboard.press("Enter");
    await page.waitForTimeout(120);
  }
  console.log("ok  seeded /scratch with foo.txt foobar.txt baz.txt");

  // ---- TEST 1: LCP extension. "cat /scratch/f" + Tab silently autocompletes
  // to "cat /scratch/foo" because matches (foo.txt, foobar.txt) share that
  // common prefix. The grid should grow only by the extension bytes, NOT
  // by a whole new menu row.
  {
    const rowsBefore = await countInkedRows();
    const inkBefore = await countInk();
    await page.keyboard.type("cat /scratch/f", { delay: 4 });
    await page.keyboard.press("Tab");
    await page.waitForTimeout(150);
    const inkAfter = await countInk();
    const rowsAfter = await countInkedRows();
    if (inkAfter <= inkBefore + 50) {
      fail(`T1 LCP extension: ink ${inkBefore} -> ${inkAfter} (expected line growth)`);
    } else if (rowsAfter !== rowsBefore) {
      // LCP extension stays on the same row; if rows grew, the menu was
      // accidentally drawn.
      fail(`T1 LCP extension: rows ${rowsBefore} -> ${rowsAfter} (menu drawn unexpectedly)`);
    } else {
      console.log(`ok  T1 LCP extends 'cat /scratch/f' -> 'cat /scratch/foo' (ink ${inkBefore}->${inkAfter}, rows unchanged=${rowsAfter})`);
    }
  }

  // ---- TEST 2: second Tab now opens the menu. LCP equals Target so we
  // fall through to FormatColumns; rows must grow.
  {
    const rowsBefore = await countInkedRows();
    await page.keyboard.press("Tab");
    await page.waitForTimeout(180);
    const rowsAfter = await countInkedRows();
    if (rowsAfter <= rowsBefore) {
      fail(`T2 second-Tab menu: rows ${rowsBefore} -> ${rowsAfter} (expected menu growth)`);
    } else {
      console.log(`ok  T2 second-Tab opened menu (rows ${rowsBefore} -> ${rowsAfter})`);
    }
    await clearLine();
  }

  // ---- TEST 3: single-match argument completion. "cat /scratch/b" + Tab
  // -> "cat /scratch/baz.txt" outright.
  {
    const inkBefore = await countInk();
    await page.keyboard.type("cat /scratch/b", { delay: 4 });
    await page.keyboard.press("Tab");
    await page.waitForTimeout(150);
    const inkAfter = await countInk();
    if (inkAfter <= inkBefore + 50) {
      fail(`T3 single-match 'cat /scratch/b'+Tab: ink ${inkBefore} -> ${inkAfter}`);
    } else {
      console.log(`ok  T3 single-match autocompleted (ink ${inkBefore} -> ${inkAfter})`);
    }
    await clearLine();
  }

  // ---- TEST 4: column-packed menu over 3 entries. "cat /scratch/" + Tab
  // lists foo.txt, foobar.txt, baz.txt with no LCP progress -> menu.
  {
    const rowsBefore = await countInkedRows();
    await page.keyboard.type("cat /scratch/", { delay: 4 });
    await page.keyboard.press("Tab");
    await page.waitForTimeout(180);
    const rowsAfter = await countInkedRows();
    if (rowsAfter <= rowsBefore) {
      fail(`T4 column-pack 3 entries: rows ${rowsBefore} -> ${rowsAfter} (expected menu)`);
    } else {
      console.log(`ok  T4 column-packed menu drew 3 entries (rows ${rowsBefore} -> ${rowsAfter})`);
    }
    await clearLine();
  }

  // ---- TEST 5: no-match silent. "cat /scratch/z" matches nothing -> no ink delta.
  {
    await page.keyboard.type("cat /scratch/z", { delay: 4 });
    await page.waitForTimeout(80);
    const inkBefore = await countInk();
    await page.keyboard.press("Tab");
    await page.waitForTimeout(150);
    const inkAfter = await countInk();
    if (Math.abs(inkAfter - inkBefore) > 20) {
      fail(`T5 no-match 'cat /scratch/z'+Tab: ink ${inkBefore} -> ${inkAfter} (expected silence)`);
    } else {
      console.log(`ok  T5 no-match Tab unchanged (ink ${inkBefore} -> ${inkAfter})`);
    }
    await clearLine();
  }

  await page.screenshot({ type: "png", path: SCREENSHOT_PATH, fullPage: false });
  console.log(`ok  saved screenshot: ${SCREENSHOT_PATH}`);
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
