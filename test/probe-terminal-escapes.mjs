// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Headless playwright probe: the terminal client interprets escape sequences.
//
// The client runs on go-widgets/shellterm, whose screen is a toolkit.Terminal
// (a VT model), not a character grid: a command's output may carry colours and
// cursor motion. This probe runs a coreutils command that emits SGR sequences
//
//     printf '\033[31mRED\033[0m plain\n'
//
// and asserts, on the composited canvas pixels, that
//   1. before the command there is NO red ink in the window (negative control:
//      the prompt is cyan, the default ink green);
//   2. after it, "RED" is painted in the terminal's red (0xff,0x9b,0x9b) and
//      "plain" in the default green -- the sequence was interpreted, and
//      neither shown as literal text nor lost;
//   3. a second command with a bold-cyan sequence paints cyan ink beyond the
//      prompt's (the 16-colour palette maps through, not just red).
// Literal text would be all green, so (2) fails if the sequences are not parsed.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 15000;

const GREEN = [0xa0, 0xe0, 0xa0];
const CYAN = [0x8a, 0xe1, 0xff];
const RED = [0xff, 0x9b, 0x9b];

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
console.log(`probe-terminal-escapes: serving on ${base}`);

const browser = await chromium.launch({ headless: true });
const pageErrors = [];

try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
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

  await page.evaluate(() => globalThis.wasmboxSpawnExternal("clients/terminal/worker.js"));
  await page.waitForTimeout(2500);

  // The terminal panel is the only 0x101010 region on the canvas.
  const png0 = PNG.sync.read(await page.screenshot({ type: "png" }));
  let bounds = null;
  {
    const { width, height, data } = png0;
    let minX = width, minY = height, maxX = -1, maxY = -1;
    for (let y = 0; y < height; y++) for (let x = 0; x < width; x++) {
      const i = (y * width + x) * 4;
      if (data[i] === 0x10 && data[i + 1] === 0x10 && data[i + 2] === 0x10) {
        if (x < minX) minX = x; if (y < minY) minY = y;
        if (x > maxX) maxX = x; if (y > maxY) maxY = y;
      }
    }
    if (maxX >= 0) bounds = { x: minX, y: minY, w: maxX - minX + 1, h: maxY - minY + 1 };
  }
  if (!bounds) throw new Error("terminal window not visible (no 0x101010 panel)");
  console.log(`ok  terminal window painted at (${bounds.x},${bounds.y}) ${bounds.w}x${bounds.h}`);

  async function ink(rgb) {
    const png = PNG.sync.read(await page.screenshot({ type: "png" }));
    let n = 0;
    for (let y = bounds.y; y < bounds.y + bounds.h; y++) for (let x = bounds.x; x < bounds.x + bounds.w; x++) {
      const i = (y * png.width + x) * 4;
      if (png.data[i] === rgb[0] && png.data[i + 1] === rgb[1] && png.data[i + 2] === rgb[2]) n++;
    }
    return n;
  }

  await page.mouse.click(bounds.x + Math.floor(bounds.w / 2), bounds.y + Math.floor(bounds.h / 2));
  await page.waitForTimeout(200);

  // 1. Negative control: a fresh terminal has no red.
  const red0 = await ink(RED);
  if (red0 !== 0) fail(`red ink before any command: ${red0} pixels (want 0)`);
  else console.log("ok  no red ink before the command (negative control)");
  const cyan0 = await ink(CYAN);

  // 2. SGR red + reset, output of the coreutils printf.
  await page.keyboard.type("printf '\\033[31mRED\\033[0m plain\\n'", { delay: 4 });
  await page.keyboard.press("Enter");
  await page.waitForTimeout(700);
  const red1 = await ink(RED);
  const green1 = await ink(GREEN);
  console.log(`info after the red command: red=${red1} green=${green1}`);
  if (red1 < 20) fail(`SGR red not painted: ${red1} red pixels (want >= 20)`);
  else console.log(`ok  "RED" painted in the terminal's red (${red1} pixels)`);
  if (green1 < 200) fail(`default ink missing after the reset: ${green1} green pixels`);
  else console.log(`ok  " plain" painted in the default ink after SGR 0 (${green1} green pixels)`);

  // 3. Bold cyan: the output adds cyan beyond what the prompts paint.
  const cyan1 = await ink(CYAN);
  await page.keyboard.type("printf '\\033[1;36mCYAN WORDS\\033[0m\\n'", { delay: 4 });
  await page.keyboard.press("Enter");
  await page.waitForTimeout(700);
  const cyan2 = await ink(CYAN);
  console.log(`info cyan ink: fresh=${cyan0} after red cmd=${cyan1} after cyan cmd=${cyan2}`);
  // The new prompt adds a little cyan on its own (about one prompt's worth); the
  // ten-letter output adds several times that.
  if (cyan2 - cyan1 < 2 * (cyan1 - cyan0) + 20) fail(`bold-cyan SGR not painted: cyan ${cyan1} -> ${cyan2}`);
  else console.log(`ok  bold cyan output painted (cyan ink ${cyan1} -> ${cyan2})`);

  if (pageErrors.length) fail(`pageerror(s): ${pageErrors.join(" | ")}`);
  else console.log("ok  no pageerror");
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
