// SPDX-License-Identifier: BSD-3-Clause
//
// Headless integration probe for client popups (xdg-popup / subsurface).
// Boots the real desktop in headless Chromium and proves, from browser
// screenshots (ground truth — not internal state), that:
//
//   1. Clicking the hello window opens a child popup that PAINTS at the click
//      point — proving parent-relative placement + the multi-surface SDK +
//      the compositor blit all work end-to-end.
//   2. Clicking OUTSIDE the popup DISMISSES it (the grab) — the menu pixels go
//      away again.
//
// (The popup's undecorated geometry, focus-exclusion and stacking are proven
// deterministically in cmd/rbtest; here we verify the live round-trip.)
//
// Run: WASMBOX_BASE_URL=http://127.0.0.1:PORT node test/probe-popup.mjs
//      (or standalone — it stands up its own COOP/COEP server like render.mjs)

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 10000;
// The popup body in clients/hello/worker.js is a light menu (236,236,240). We
// count "near-white" pixels, which neither the dark desktop nor hello's blue
// gradient produce — so the popup is unambiguous against whatever is beneath.
const isMenu = (px) => px[0] >= 225 && px[1] >= 225 && px[2] >= 225;
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
function countMenu(png, x, y, w, h) {
  let n = 0;
  for (let yy = y; yy < y + h; yy++) for (let xx = x; xx < x + w; xx++) {
    if (xx >= 0 && yy >= 0 && xx < png.width && yy < png.height && isMenu(pixel(png, xx, yy))) n++;
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
  await page.waitForTimeout(1500); // let clients spawn + persist their layout

  // Locate the hello window via its persisted layout record (title-keyed).
  const rec = await page.evaluate(() => {
    const raw = localStorage.getItem("wasmbox.layout");
    if (!raw) return null;
    for (const line of String(raw).split("\n")) {
      const p = line.split("\t");
      if (p.length >= 5 && p[0] === "hello (wasm)") return { x: +p[1], y: +p[2], w: +p[3], h: +p[4] };
    }
    return null;
  });
  if (!rec) {
    fail("could not locate the hello window in the saved layout");
  } else {
    ok(`hello window at (${rec.x},${rec.y}) ${rec.w}x${rec.h}`);
    // The top-left corner of any cascade window is always clickable (later
    // windows are offset down-right). Click there: it raises hello AND, being
    // a body click forwarded to the client, opens a popup anchored at (12,12).
    const cx = rec.x + 12, cy = rec.y + 12;
    const PW = 132, PH = 96;

    const before = await shot(page);
    const baseMenu = countMenu(before, cx, cy, PW, PH);

    await page.mouse.click(cx, cy);
    await page.waitForTimeout(500);
    const after = await shot(page);
    const popMenu = countMenu(after, cx, cy, PW, PH);
    if (popMenu > baseMenu + 1500) ok(`popup painted at the click point: ${popMenu} menu px (baseline ${baseMenu})`);
    else fail(`popup did not paint: ${popMenu} menu px (baseline ${baseMenu})`);

    // Dismiss: click far away on the empty desktop (top-left corner).
    await page.mouse.click(5, 5);
    await page.waitForTimeout(400);
    const gone = await shot(page);
    const popMenu2 = countMenu(gone, cx, cy, PW, PH);
    if (popMenu2 <= baseMenu + 1500) ok(`popup dismissed on outside click: ${popMenu2} menu px (back to ~${baseMenu})`);
    else fail(`popup not dismissed by the outside click: still ${popMenu2} menu px`);
  }

  if (pageErrors.length) fail(`pageerror(s): ${pageErrors.join("; ")}`);
  else ok("no pageerror");
} finally {
  await browser.close();
  if (server) server.close();
}

console.log(failures ? `\nRESULT: FAIL (${failures})` : "\nRESULT: PASS");
process.exit(failures ? 1 : 0);
