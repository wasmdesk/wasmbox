// SPDX-License-Identifier: BSD-3-Clause
//
// Headless integration probe for NESTED popups (submenus) + the layered grab.
// Boots the real desktop and proves, from browser screenshots, that:
//
//   1. Clicking the hello window opens a menu popup.
//   2. Clicking the menu's top item opens a SUBMENU to the menu's right — a
//      popup whose parent is itself a popup (nesting + parent-relative placement
//      against a popup parent).
//   3. Clicking elsewhere in the parent menu dismisses ONLY the submenu (the
//      layered grab), leaving the parent menu open.
//   4. Clicking outside everything dismisses the whole chain.
//
// (Stacking/ordering + orphan-on-close for nesting are proven in cmd/rbtest;
// here we verify the live round-trip.)
//
// Run: WASMBOX_BASE_URL=http://127.0.0.1:PORT node test/probe-nested.mjs

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 10000;
// Both menu bodies are light; we count "near-white" pixels (neither the dark
// desktop nor hello's blue produce them) and separate the two popups by region.
const isMenu = (px) => px[0] >= 220 && px[1] >= 220 && px[2] >= 220;
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
function countMenu(png, x0, y0, x1, y1) {
  let n = 0;
  for (let y = y0; y < y1; y++) for (let x = x0; x < x1; x++) {
    if (x >= 0 && y >= 0 && x < png.width && y < png.height && isMenu(pixel(png, x, y))) n++;
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
    // Regions (popup1 opens at hx+12,hy+12 size 132x96; submenu opens to its right).
    const p1 = (png) => countMenu(png, hx + 16, hy + 16, hx + 130, hy + 104); // parent menu, left of submenu
    const sub = (png) => countMenu(png, hx + 150, hy + 22, hx + 250, hy + 84); // submenu, right of parent

    // 1. open the parent menu.
    await page.mouse.click(hx + 12, hy + 12);
    await page.waitForTimeout(450);
    let png = await shot(page);
    const p1Open = p1(png), subBefore = sub(png);
    if (p1Open > 2000) ok(`parent menu painted: ${p1Open} px`); else fail(`parent menu missing: ${p1Open} px`);

    // 2. click the parent's TOP item -> submenu opens to the right.
    await page.mouse.click(hx + 42, hy + 22);
    await page.waitForTimeout(450);
    png = await shot(page);
    const subOpen = sub(png), p1Still = p1(png);
    if (subOpen > subBefore + 1500) ok(`submenu opened to the menu's right (nested popup): ${subOpen} px (was ${subBefore})`);
    else fail(`submenu did not open: ${subOpen} px (baseline ${subBefore})`);
    if (p1Still > 2000) ok(`parent menu still open under the submenu: ${p1Still} px`); else fail(`parent menu vanished: ${p1Still} px`);

    // 3. click elsewhere in the parent -> layered grab dismisses ONLY the submenu.
    await page.mouse.click(hx + 42, hy + 82);
    await page.waitForTimeout(450);
    png = await shot(page);
    const subAfter = sub(png), p1After = p1(png);
    if (subAfter <= subBefore + 1500) ok(`layered grab dismissed the submenu: ${subAfter} px (back to ~${subBefore})`);
    else fail(`submenu not dismissed by clicking the parent: ${subAfter} px`);
    if (p1After > 2000) ok(`parent menu kept open by the layered grab: ${p1After} px`); else fail(`parent menu wrongly closed: ${p1After} px`);

    // 4. click outside everything -> whole chain dismissed.
    await page.mouse.click(5, 5);
    await page.waitForTimeout(400);
    png = await shot(page);
    const p1Gone = p1(png);
    if (p1Gone <= 2000) ok(`outside click dismissed the whole chain: ${p1Gone} px`); else fail(`chain not dismissed: ${p1Gone} px`);
  }

  if (pageErrors.length) fail(`pageerror(s): ${pageErrors.join("; ")}`);
  else ok("no pageerror");
} finally {
  await browser.close();
  if (server) server.close();
}

console.log(failures ? `\nRESULT: FAIL (${failures})` : "\nRESULT: PASS");
process.exit(failures ? 1 : 0);
