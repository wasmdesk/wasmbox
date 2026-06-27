// SPDX-License-Identifier: BSD-3-Clause
//
// Headless integration probe for popup KEYBOARD navigation. Boots the desktop
// and proves, from browser screenshots, that while a menu popup is open:
//
//   1. an arrow key is routed to the popup (not the window beneath) — the menu
//      paints a highlight on an item (a blue accent that wasn't there before);
//   2. Escape dismisses the popup (compositor-handled keyboard grab).
//
// (The routing policy — popup grabs the keyboard, else the focused window — is
// unit-tested as WindowManager#key_target in cmd/rbtest.)
//
// Run: WASMBOX_BASE_URL=http://127.0.0.1:PORT node test/probe-keyboard.mjs

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 10000;
const isMenu = (px) => px[0] >= 220 && px[1] >= 220 && px[2] >= 220;          // light menu body
const isHi = (px) => px[0] < 110 && px[1] >= 100 && px[1] <= 170 && px[2] >= 190; // blue highlight
const MIME = {
  ".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8", ".wasm": "application/wasm",
  ".css": "text/css; charset=utf-8", ".json": "application/json; charset=utf-8",
  ".rb": "text/plain; charset=utf-8", ".map": "application/json; charset=utf-8",
};

let failures = 0;
const ok = (l) => console.log("ok  " + l);
const fail = (l) => { failures++; console.error("FAIL: " + l); };

function startServer() {
  const server = createServer(async (req, res) => {
    try {
      const urlPath = decodeURIComponent((req.url || "/").split("?")[0]);
      let rel = normalize(urlPath).replace(/^(\.\.[/\\])+/, "");
      if (rel === "/" || rel === "" || rel === "\\") rel = "/index.html";
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

let server = null;
let base = process.env.WASMBOX_BASE_URL;
if (base) { base = base.replace(/\/+$/, ""); console.log(`using external server: ${base}`); }
else { const s = await startServer(); server = s.server; base = s.base; console.log(`using built-in fallback server: ${base}`); }

const browser = await chromium.launch({ headless: true });

const pixel = (png, x, y) => { const i = ((y | 0) * png.width + (x | 0)) * 4; return [png.data[i], png.data[i + 1], png.data[i + 2]]; };
const shot = async (page) => PNG.sync.read(await page.screenshot({ type: "png", fullPage: false }));
function count(png, pred, x0, y0, x1, y1) {
  let n = 0;
  for (let y = y0; y < y1; y++) for (let x = x0; x < x1; x++) {
    if (x >= 0 && y >= 0 && x < png.width && y < png.height && pred(pixel(png, x, y))) n++;
  }
  return n;
}

try {
  const page = await browser.newPage();
  const pageErrors = [];
  page.on("pageerror", (e) => pageErrors.push(String(e)));
  await page.goto(`${base}/index.html`, { waitUntil: "load" });
  await page.waitForFunction(
    () => { if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError)); return globalThis.wasmboxReady === true; },
    { timeout: BOOT_TIMEOUT_MS },
  );
  ok("booted (wasmboxReady === true)");
  await page.waitForTimeout(1500);

  const rec = await page.evaluate(() => {
    const raw = localStorage.getItem("wasmbox.layout");
    if (!raw) return null;
    for (const line of String(raw).split("\n")) {
      const p = line.split("\t");
      if (p.length >= 5 && p[0] === "hello (wasm)") return { x: +p[1], y: +p[2] };
    }
    return null;
  });
  if (!rec) {
    fail("could not locate the hello window in the saved layout");
  } else {
    const hx = rec.x, hy = rec.y;
    ok(`hello window at (${hx},${hy})`);
    const menu = (png) => count(png, isMenu, hx + 16, hy + 16, hx + 140, hy + 104);
    const hi0 = (png) => count(png, isHi, hx + 22, hy + 22, hx + 130, hy + 38); // top item row

    // open the menu.
    await page.mouse.click(hx + 12, hy + 12);
    await page.waitForTimeout(450);
    let png = await shot(page);
    if (menu(png) > 2000) ok(`menu open: ${menu(png)} px`); else fail(`menu missing: ${menu(png)} px`);
    const hiBefore = hi0(png);

    // ArrowDown -> the menu (the active popup) highlights item 0.
    await page.keyboard.press("ArrowDown");
    await page.waitForTimeout(350);
    png = await shot(page);
    const hiAfter = hi0(png);
    if (hiAfter > hiBefore + 800) ok(`arrow key routed to the popup: highlight painted (${hiAfter} blue px, was ${hiBefore})`);
    else fail(`arrow key did not reach the popup: ${hiAfter} blue px (baseline ${hiBefore})`);

    // Escape -> compositor dismisses the popup.
    await page.keyboard.press("Escape");
    await page.waitForTimeout(350);
    png = await shot(page);
    if (menu(png) <= 2000) ok(`Escape dismissed the menu: ${menu(png)} px`); else fail(`Escape did not dismiss: ${menu(png)} px`);
  }

  if (pageErrors.length) fail(`pageerror(s): ${pageErrors.join("; ")}`);
  else ok("no pageerror");
} finally {
  await browser.close();
  if (server) server.close();
}

console.log(failures ? `\nRESULT: FAIL (${failures})` : "\nRESULT: PASS");
process.exit(failures ? 1 : 0);
