// SPDX-License-Identifier: BSD-3-Clause
//
// Headless probe for window-shade ("roll up" / replier-déplier): a two-finger
// swipe (wheel) over a window's titlebar folds it to just the titlebar (swipe
// up) and unfolds it (swipe down).
//
// To make the body's presence unambiguous we first drag the hello window onto
// empty desktop (dark behind it), so:
//   - unfolded -> the body shows hello's lit (blue) content;
//   - folded   -> the body area is the dark desktop (body not drawn).
//
// Run: WASMBOX_BASE_URL=http://127.0.0.1:PORT node test/probe-shade.mjs

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 10000;
// "lit" = a clearly non-desktop pixel (desktop is very dark, ~23,26,36; hello's
// body is a blue gradient with channels well above this).
const isLit = (px) => Math.max(px[0], px[1], px[2]) > 80;
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
function countLit(png, x0, y0, x1, y1) {
  let n = 0;
  for (let y = y0; y < y1; y++) for (let x = x0; x < x1; x++) {
    if (x >= 0 && y >= 0 && x < png.width && y < png.height && isLit(pixel(png, x, y))) n++;
  }
  return n;
}
const layout = (page, title) => page.evaluate((t) => {
  const raw = localStorage.getItem("wasmbox.layout");
  if (!raw) return null;
  for (const line of String(raw).split("\n")) {
    const p = line.split("\t");
    if (p.length >= 5 && p[0] === t) return { x: +p[1], y: +p[2], w: +p[3], h: +p[4] };
  }
  return null;
}, title);

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
  await page.waitForTimeout(1200);

  const rec = await layout(page, "hello (wasm)");
  if (!rec) { fail("could not locate the hello window"); }
  else {
    // Drag hello onto empty desktop (right side, clear of the cascade + dock)
    // by grabbing its titlebar (22px above the body).
    await page.mouse.move(rec.x + 20, rec.y - 11);
    await page.mouse.down();
    await page.mouse.move(720, 300, { steps: 10 });
    await page.mouse.up();
    await page.waitForTimeout(400);
    const m = await layout(page, "hello (wasm)");
    const nx = m.x, ny = m.y, nw = m.w, nh = m.h;
    ok(`hello moved to (${nx},${ny}) ${nw}x${nh} over empty desktop`);
    const body = (png) => countLit(png, nx + 6, ny + 6, nx + nw - 6, ny + nh - 6);
    const tbX = nx + 20, tbY = ny - 11; // titlebar point for the gesture

    const litOpen = body(await shot(page));
    if (litOpen > 3000) ok(`unfolded: body shows hello content (${litOpen} lit px)`);
    else fail(`expected hello body content before shading, got ${litOpen} lit px`);

    // Two-finger swipe UP over the titlebar -> fold (shade).
    await page.mouse.move(tbX, tbY);
    await page.mouse.wheel(0, 120);
    await page.waitForTimeout(400);
    const litShaded = body(await shot(page));
    if (litShaded < litOpen * 0.2) ok(`folded on swipe-up: body rolled away (${litShaded} lit px, was ${litOpen})`);
    else fail(`window did not fold: ${litShaded} lit px still in the body area`);

    // Two-finger swipe DOWN over the titlebar -> unfold (unshade).
    await page.mouse.move(tbX, tbY);
    await page.mouse.wheel(0, -120);
    await page.waitForTimeout(400);
    const litBack = body(await shot(page));
    if (litBack > litOpen * 0.6) ok(`unfolded on swipe-down: body restored (${litBack} lit px)`);
    else fail(`window did not unfold: only ${litBack} lit px back in the body`);
  }

  if (pageErrors.length) fail(`pageerror(s): ${pageErrors.join("; ")}`);
  else ok("no pageerror");
} finally {
  await browser.close();
  if (server) server.close();
}

console.log(failures ? `\nRESULT: FAIL (${failures})` : "\nRESULT: PASS");
process.exit(failures ? 1 : 0);
