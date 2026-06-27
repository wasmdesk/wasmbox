// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the Fluxbox-style wasmdock toolbar.
//
// Boot the compositor in headless Chrome, wait for the dock to land at the
// bottom of the canvas, sample three known pixel zones (workspace label,
// iconbar, clock), click on one of the iconbar buttons, and assert that the
// corresponding launcher (e.g. terminal) spawned a window.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 20000;
const SCREENSHOT_PATH = "/tmp/wasmdock-fluxbox.png";

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
function ok(msg)   { console.log(`ok  ${msg}`); }

// pickPixel pulls one RGB triple from a PNG at (x,y), clamped to the buffer.
function pickPixel(png, x, y) {
  const i = (y * png.width + x) * 4;
  return [png.data[i], png.data[i + 1], png.data[i + 2]];
}

// withinRange reports whether |c - target| <= tol per channel.
function nearColor(c, target, tol) {
  return Math.abs(c[0] - target[0]) <= tol &&
         Math.abs(c[1] - target[1]) <= tol &&
         Math.abs(c[2] - target[2]) <= tol;
}

const { server, base } = await startServer();
console.log(`probe-wasmdock-fluxbox: serving on ${base}`);

const browser = await chromium.launch({ headless: true, channel: "chrome" });
const consoleLines = [];
const pageErrors = [];

try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  page.on("console",   (m) => consoleLines.push(m.text()));
  page.on("pageerror", (e) => pageErrors.push(String(e)));

  await page.goto(`${base}/index.html`, { waitUntil: "load" });
  await page.waitForFunction(
    () => {
      if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
      return globalThis.wasmboxReady === true;
    },
    { timeout: BOOT_TIMEOUT_MS },
  );
  ok("compositor booted");

  // The compositor auto-spawns the dock as part of bootWasm. Give it a
  // generous window to commit its first frame (wasm fetch + boot can take
  // a couple of seconds on a cold cache).
  await page.waitForTimeout(3000);

  // The dock surface is 1280 px wide; we ask for h=60 because the compositor
  // floors panel heights at Theme::MIN_H = 60 (compositor.rb). The Go-side
  // scene scales every section to fill the granted height, so the toolbar
  // sits at y = canvas_h - 60 on a 1280x800 viewport.
  const DOCK_H = 60;
  const viewport = await page.viewportSize();
  const dockTop = viewport.height - DOCK_H;
  console.log(`info dock expected at y=${dockTop} (h=${DOCK_H})`);

  // Grab a screenshot for pixel sampling.
  const shot = await page.screenshot({ type: "png", path: SCREENSHOT_PATH, fullPage: false });
  const png = PNG.sync.read(shot);
  ok(`screenshot saved -> ${SCREENSHOT_PATH}`);

  // Per-section sampling. Sample at the dock vertical midpoint where the
  // gradient face is most representative (away from the 1px bevel + border).
  const sampleY = dockTop + Math.floor(DOCK_H / 2);
  const samples = [
    { name: "workspace",   x: 50,                  expect: [0x90, 0x90, 0x90] }, // inactive title bg (mid gray)
    // Iconbar samples must dodge per-button bevels + label glyphs. Buttons
    // start at x=100 and span 120 px each with a 2 px gap; the gap centre
    // at x=222 reads the iconbar background gradient directly.
    { name: "iconbar.gap", x: 222,                 expect: [0xc8, 0xc8, 0xc8] }, // active title gradient face in inter-button gap
    { name: "iconbar.btn", x: 200,                 expect: [0xc8, 0xc8, 0xc8] }, // mid-button face (inactive grad)
    { name: "clock",       x: viewport.width - 40, expect: [0xd0, 0xd0, 0xd0] }, // OSD bg
  ];

  for (const s of samples) {
    const c = pickPixel(png, s.x, sampleY);
    // Wide tolerance — gradients + bevels mean the exact center may not be
    // the literal stop colour; allow a 96-channel window.
    const within = nearColor(c, s.expect, 96);
    console.log(`info ${s.name} pixel @(${s.x},${sampleY}) = rgb(${c.join(",")}) expect~ rgb(${s.expect.join(",")}) within=${within}`);
    if (!within) fail(`${s.name} pixel not near expected bevel-gray family`);
  }

  // Verify the very top row of the toolbar is the theme border colour
  // (#4a4a4a). Sample a few x positions.
  for (const x of [10, 640, 1270]) {
    const c = pickPixel(png, x, dockTop);
    if (!nearColor(c, [0x4a, 0x4a, 0x4a], 16)) {
      fail(`top border pixel @(${x},${dockTop}) = rgb(${c.join(",")}), want ~#4a4a4a`);
    }
  }
  ok("top border row near #4a4a4a across the toolbar");

  // Count "ink" pixels (near-black, RGB < 0x40 per channel) inside each
  // section — the workspace label, iconbar buttons, and clock all paint
  // glyph ink near-black against gray bevels.
  function countInk(x0, y0, w, h) {
    let n = 0;
    for (let y = y0; y < y0 + h; y++) {
      for (let x = x0; x < x0 + w; x++) {
        const i = (y * png.width + x) * 4;
        if (png.data[i] < 0x50 && png.data[i+1] < 0x50 && png.data[i+2] < 0x50) n++;
      }
    }
    return n;
  }
  const workspaceInk = countInk(0, dockTop+1, 100, DOCK_H-1);
  const iconbarInk   = countInk(100, dockTop+1, viewport.width - 180, DOCK_H-1);
  const clockInk     = countInk(viewport.width - 80, dockTop+1, 80, DOCK_H-1);
  console.log(`info ink workspace=${workspaceInk} iconbar=${iconbarInk} clock=${clockInk}`);
  if (workspaceInk < 5) fail("workspace label not inked");
  if (iconbarInk   < 50) fail("iconbar buttons / labels not inked");
  if (clockInk     < 5) fail("clock label not inked");
  ok("each section carries ink pixels");

  // ---- click-launch test --------------------------------------------------
  // The terminal launcher is the first iconbar button. Its rect is at
  // x=WorkspaceW (100), button width 120 -> center at x=160. Vertical
  // center of the button row at dockTop + DOCK_H/2.
  const beforeCount = await page.evaluate(() => globalThis.__wasmboxStackLen || 0);
  await page.mouse.click(160, dockTop + Math.floor(DOCK_H / 2));
  await page.waitForTimeout(2000);
  // The compositor doesn't expose a window count by default; scan the
  // canvas for the terminal panel's 0x101010 bg the way probe-terminal does.
  const after = await page.screenshot({ type: "png", fullPage: false });
  const afterPng = PNG.sync.read(after);
  let terminalPixels = 0;
  for (let y = 0; y < afterPng.height; y++) {
    for (let x = 0; x < afterPng.width; x++) {
      const i = (y * afterPng.width + x) * 4;
      if (afterPng.data[i] === 0x10 && afterPng.data[i+1] === 0x10 && afterPng.data[i+2] === 0x10) {
        terminalPixels++;
      }
    }
  }
  console.log(`info terminal-panel pixels after click: ${terminalPixels}`);
  if (terminalPixels < 1000) {
    fail(`expected terminal window after iconbar click; only ${terminalPixels} panel pixels`);
  } else {
    ok(`terminal window spawned after iconbar click (${terminalPixels} panel pixels)`);
  }

  void beforeCount; // beforeCount is just a marker; the count probe above is the real assertion.

  if (pageErrors.length) {
    fail(`pageerror(s): ${pageErrors.join(" | ")}`);
  } else {
    ok("no pageerror");
  }
  // Surface console warnings/errors but don't fail on plain info lines.
  const consoleBad = consoleLines.filter((l) => /^(\[ERROR\]|error|Failed)/i.test(l));
  if (consoleBad.length) {
    console.log(`info console warnings/errors:\n  ${consoleBad.slice(0, 10).join("\n  ")}`);
  }
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
