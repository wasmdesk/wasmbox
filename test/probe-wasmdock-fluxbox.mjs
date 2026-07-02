// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the Fluxbox-style wasmdock toolbar.
//
// Boot the compositor in bundled headless Chromium, read the dock's real
// geometry from its __wasmdockGeometry hook (never a hardcoded panel height),
// sample the three fluxbox sections (workspace label / iconbar / clock) plus
// the top bevel border, assert each section carries glyph ink, then click a
// live window button (translated from the hook's surface-space rect) and
// assert it becomes focused -- proving the iconbar buttons are interactive.

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

// Read the dock's published geometry (surface-space size + window-button
// rects) from the dock worker's __wasmdockGeometry hook.
async function dockGeometry(page) {
  for (const worker of page.workers()) {
    try {
      const g = await worker.evaluate(() => globalThis.__wasmdockGeometry || null);
      if (g && Array.isArray(g.buttons)) return g;
    } catch (_) { /* not the dock worker */ }
  }
  return null;
}

const browser = await chromium.launch({ headless: true });
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

  // Read the dock's REAL geometry from its hook instead of assuming a panel
  // height: the toolbar is `geo.h` tall and pinned to the bottom of the canvas.
  const viewport = await page.viewportSize();
  const geo = await dockGeometry(page);
  if (!geo) { fail("could not read __wasmdockGeometry from the dock worker"); throw new Error("no dock geometry"); }
  const DOCK_H  = geo.h;
  const dockTop = viewport.height - geo.h;
  const dockX   = Math.floor((viewport.width - geo.w) / 2);
  console.log(`info dock geometry: w=${geo.w} h=${geo.h} -> top=${dockTop} left=${dockX}, ${geo.buttons.length} window buttons`);

  // Grab a screenshot for pixel sampling.
  const shot = await page.screenshot({ type: "png", path: SCREENSHOT_PATH, fullPage: false });
  const png = PNG.sync.read(shot);
  ok(`screenshot saved -> ${SCREENSHOT_PATH}`);

  // Per-section face sampling at the dock vertical midpoint. The window
  // buttons float on the right of the iconbar (their exact x depends on the
  // open-window count), so we sample the two fixed sections -- the workspace
  // label face on the far left and the clock OSD face on the far right -- and
  // leave button rendering to the ink count + the live-click test below.
  const sampleY = dockTop + Math.floor(DOCK_H / 2);
  const samples = [
    { name: "workspace", x: 50,                  expect: [0x90, 0x90, 0x90] }, // inactive title bg (mid gray)
    { name: "clock",     x: viewport.width - 40, expect: [0xd0, 0xd0, 0xd0] }, // OSD bg
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

  // ---- live-button test ---------------------------------------------------
  // Prove the iconbar window buttons are interactive: pick a button that is
  // NOT currently focused, translate its surface-space rect to screen coords
  // (dock is bottom-centered), click its center, and assert the hook now
  // reports that button as the focused one. This is robust to the button
  // count/positions and to any palette retune.
  if (geo.buttons.length < 1) {
    fail("no iconbar window buttons to click");
  } else {
    const target = geo.buttons.find((b) => !b.focused) || geo.buttons[0];
    const cx = dockX + target.x + Math.floor(target.w / 2);
    const cy = dockTop + target.y + Math.floor(target.h / 2);
    console.log(`info clicking window button id=${target.id} "${target.title}" @(${cx},${cy})`);
    await page.mouse.click(cx, cy);
    await page.waitForTimeout(600);
    const after = await dockGeometry(page);
    const now = after && after.buttons.find((b) => b.id === target.id);
    const focusedCount = after ? after.buttons.filter((b) => b.focused).length : -1;
    if (!now) {
      fail(`window button id=${target.id} vanished after click`);
    } else if (!now.focused) {
      fail(`clicking window button "${target.title}" did not focus it (focused=${now.focused})`);
    } else if (focusedCount !== 1) {
      fail(`expected exactly one focused button after click, got ${focusedCount}`);
    } else {
      ok(`window button "${target.title}" became focused after click (exactly 1 focused)`);
    }
  }

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
