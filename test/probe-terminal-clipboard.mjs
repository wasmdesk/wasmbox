// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Headless playwright probe: the clipboard protocol (docs/protocol.md,
// "Clipboard") end to end, through the terminal client.
//
//   Paste: the page reads the browser's clipboard on a paste shortcut, the
//          compositor forwards the text to the focused window ahead of the
//          shortcut's keydown, and the terminal types it. Proof is by pixels:
//          the line a paste leaves on the screen is pixel-identical (same
//          green-ink count) to the same text typed on the keyboard.
//   Copy:  a mouse selection of the output plus Meta+C reaches
//          navigator.clipboard through the compositor and the page.
//
// Terminal paste keys are Ctrl+Shift+V and Meta+V; a plain Ctrl+V is ^V for the
// shell and must NOT paste. A paste whose text holds a newline runs the command.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 15000;
const GREEN = [0xa0, 0xe0, 0xa0];

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
console.log(`probe-terminal-clipboard: serving on ${base}`);

const browser = await chromium.launch({ headless: true });
const pageErrors = [];

try {
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin: base });
  const page = await context.newPage();
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

  async function ink() {
    const png = PNG.sync.read(await page.screenshot({ type: "png" }));
    let n = 0;
    for (let y = bounds.y; y < bounds.y + bounds.h; y++) for (let x = bounds.x; x < bounds.x + bounds.w; x++) {
      const i = (y * png.width + x) * 4;
      if (png.data[i] === GREEN[0] && png.data[i + 1] === GREEN[1] && png.data[i + 2] === GREEN[2]) n++;
    }
    return n;
  }
  async function clearLine() {
    for (let i = 0; i < 60; i++) await page.keyboard.press("Backspace");
    await page.waitForTimeout(150);
  }
  async function setClipboard(text) {
    await page.bringToFront();
    await page.evaluate((t) => navigator.clipboard.writeText(t), text);
  }

  await page.mouse.click(bounds.x + Math.floor(bounds.w / 2), bounds.y + Math.floor(bounds.h / 2));
  await page.waitForTimeout(200);

  // The reference: the line TYPED on the keyboard.
  const SAMPLE = "echo pasted-from-the-browser";
  const base0 = await ink();
  await page.keyboard.type(SAMPLE, { delay: 4 });
  await page.waitForTimeout(300);
  const typed = await ink();
  console.log(`info ink: empty prompt=${base0}, typed "${SAMPLE}"=${typed}`);
  if (typed <= base0 + 50) fail(`typing painted nothing: ${base0} -> ${typed}`);
  await clearLine();
  const cleared = await ink();
  if (cleared !== base0) fail(`clearing the line did not restore the prompt: ${base0} vs ${cleared}`);

  // 1. Ctrl+Shift+V pastes the browser's clipboard (text the PAGE wrote).
  await setClipboard(SAMPLE);
  await page.keyboard.press("Control+Shift+V");
  await page.waitForTimeout(700);
  const pasted = await ink();
  if (pasted !== typed) fail(`Ctrl+Shift+V: ink ${pasted}, typed reference ${typed} (the paste did not land)`);
  else console.log(`ok  Ctrl+Shift+V pasted the page's clipboard (ink ${pasted} == typed ${typed})`);
  await clearLine();

  // 2. Meta+V (the macOS terminal paste).
  await page.keyboard.press("Meta+V");
  await page.waitForTimeout(700);
  const pastedMeta = await ink();
  if (pastedMeta !== typed) fail(`Meta+V: ink ${pastedMeta}, typed reference ${typed}`);
  else console.log(`ok  Meta+V pasted (ink ${pastedMeta} == typed ${typed})`);
  await clearLine();

  // 3. Negative control: a plain Ctrl+V is ^V for the shell, not a paste.
  await page.keyboard.press("Control+V");
  await page.waitForTimeout(700);
  const plain = await ink();
  if (plain > base0 + 30) fail(`plain Ctrl+V pasted: ink ${base0} -> ${plain}`);
  else console.log(`ok  plain Ctrl+V did not paste (ink ${base0} -> ${plain})`);
  await clearLine();

  // 4. Another text pastes as THAT text: different pixels from the sample.
  await setClipboard(SAMPLE + " and some more words");
  await page.keyboard.press("Control+Shift+V");
  await page.waitForTimeout(700);
  const longer = await ink();
  if (longer <= pasted + 50) fail(`a longer clipboard text did not paste: ${pasted} -> ${longer}`);
  else console.log(`ok  a different clipboard text pastes as that text (ink ${pasted} -> ${longer})`);
  await clearLine();

  // 5. A pasted newline runs the command: its output is a new row of ink.
  await setClipboard("echo ran-by-paste\n");
  await page.keyboard.press("Control+Shift+V");
  await page.waitForTimeout(900);
  const ran = await ink();
  // The echoed line "echo ran-by-paste" (17 glyphs) + its output "ran-by-paste"
  // (12) + a fresh prompt: about 4 ink pixels per glyph, so ~115 over the
  // empty prompt; the line alone, had it not run, is ~68.
  if (ran < base0 + 100) fail(`a pasted newline did not run the command: ink ${base0} -> ${ran}`);
  else console.log(`ok  a pasted newline ran the command (ink ${base0} -> ${ran})`);

  // 6. Copy: select the output row with the mouse, Meta+C, read the browser's clipboard.
  // After step 5 the screen is: row0 "$ echo pasted..." is gone? Start from a known
  // screen instead: clear, echo a word, select the output row.
  await page.keyboard.type("clear", { delay: 4 });
  await page.keyboard.press("Enter");
  await page.waitForTimeout(400);
  await page.keyboard.type("echo hello", { delay: 4 });
  await page.keyboard.press("Enter");
  await page.waitForTimeout(500);
  await setClipboard("sentinel-not-copied-yet");
  // Row 1 holds "hello" (row 0 is the "$ echo hello" line). The grid is 16px cells.
  const rowY = bounds.y + 16 + 8;
  await page.mouse.move(bounds.x + 3, rowY);
  await page.mouse.down();
  await page.mouse.move(bounds.x + 4 * 16 + 4, rowY, { steps: 6 });
  await page.mouse.up();
  await page.waitForTimeout(200);
  await page.keyboard.press("Meta+C");
  await page.waitForTimeout(500);
  const copied = await page.evaluate(() => navigator.clipboard.readText());
  if (copied !== "hello") fail(`Meta+C copied ${JSON.stringify(copied)}, want "hello"`);
  else console.log(`ok  mouse selection + Meta+C reached the browser clipboard: ${JSON.stringify(copied)}`);

  if (pageErrors.length) fail(`pageerror(s): ${pageErrors.join(" | ")}`);
  else console.log("ok  no pageerror");
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
