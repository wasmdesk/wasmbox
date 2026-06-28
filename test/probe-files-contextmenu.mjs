// SPDX-License-Identifier: BSD-3-Clause
//
// Regression probe for the right-click conflict: right-clicking inside the
// Files window must show ONLY the Files app's own (light, Adwaita) context menu
// — the compositor must NOT also pop its dark window/root menu on top.
//
// Files' menu bg is light (ColorButtonFace); the compositor menu bg is dark
// (#1d1f29 = 29,31,41). So, over the light Files surface, a dark menu rectangle
// at the click point is the compositor menu = the bug. We assert there are
// (almost) no such dark-menu pixels after a right-click in the Files body.
//
// Run: WASMBOX_BASE_URL=http://127.0.0.1:PORT node test/probe-files-contextmenu.mjs

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 10000;
// Compositor menu bg Theme::MENU_BG = #1d1f29. Tight tolerance so we don't
// confuse it with the (similarly dark) desktop — but the test region sits over
// the LIGHT Files surface anyway, so only the compositor menu is dark there.
const isCompMenu = (px) =>
  Math.abs(px[0] - 29) <= 6 && Math.abs(px[1] - 31) <= 6 && Math.abs(px[2] - 41) <= 6;
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
function countComp(png, x0, y0, x1, y1) {
  let n = 0;
  for (let y = y0; y < y1; y++) for (let x = x0; x < x1; x++) {
    if (x >= 0 && y >= 0 && x < png.width && y < png.height && isCompMenu(pixel(png, x, y))) n++;
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
  await page.waitForTimeout(1200);
  // Files isn't a boot window — launch it, then let it register + paint + persist.
  await page.evaluate(() => globalThis.wasmboxSpawnExternal("clients/files/worker.js"));
  await page.waitForTimeout(2500);

  const rec = await page.evaluate(() => {
    const raw = localStorage.getItem("wasmbox.layout");
    if (!raw) return null;
    for (const line of String(raw).split("\n")) {
      const p = line.split("\t");
      if (p.length >= 5 && p[0] === "Files") return { x: +p[1], y: +p[2], w: +p[3], h: +p[4] };
    }
    return null;
  });
  if (!rec) {
    fail("could not locate the Files window in the saved layout");
  } else {
    const fx = rec.x, fy = rec.y;
    ok(`Files window at (${fx},${fy}) ${rec.w}x${rec.h}`);
    // The top-left strip of a cascade window is always clickable; a right-click
    // there focuses+raises Files and (the bug) would pop the compositor menu.
    const rx = fx + 12, ry = fy + 12;
    await page.mouse.click(rx, ry, { button: "right" });
    await page.waitForTimeout(500);
    const png = await shot(page);
    // Region where the compositor menu would draw (170 wide x ~48 tall, 2 rows).
    const dark = countComp(png, rx, ry, rx + 170, ry + 48);
    if (dark < 400) ok(`no compositor menu over Files on right-click: ${dark} dark-menu px (Files shows its own)`);
    else fail(`compositor menu popped over Files (the conflict): ${dark} dark-menu px in the click region`);
  }

  if (pageErrors.length) fail(`pageerror(s): ${pageErrors.join("; ")}`);
  else ok("no pageerror");
} finally {
  await browser.close();
  if (server) server.close();
}

console.log(failures ? `\nRESULT: FAIL (${failures})` : "\nRESULT: PASS");
process.exit(failures ? 1 : 0);
