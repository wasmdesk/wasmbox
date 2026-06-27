// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for runtime theme switching via the root menu.
//
// Boot the compositor + dock; right-click the desktop -> Theme -> "Fluxbox
// Dark"; assert the dock's workspace label region pixel shifts from a light
// gray to a dark colour. Then Theme -> "GNOME Adwaita"; assert the pixel
// shifts to a different (warm-white) colour. Then Theme -> "Fluxbox Light";
// assert the pixel returns near the original.
//
// Screenshots:
//   /tmp/wasmbox-theme-light.png    (initial)
//   /tmp/wasmbox-theme-dark.png     (after Fluxbox Dark)
//   /tmp/wasmbox-theme-adwaita.png  (after GNOME Adwaita)
//   /tmp/wasmbox-theme-light-after.png  (back to Fluxbox Light)

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 30000;

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

function pix(png, x, y) {
  const i = (y * png.width + x) * 4;
  return [png.data[i], png.data[i + 1], png.data[i + 2]];
}

// avgPixel samples a small WxH region centred on (cx, cy) and returns the
// channel-mean RGB. We average so a single anti-aliased glyph pixel cannot
// flip the result.
function avgPixel(png, cx, cy, w = 6, h = 6) {
  let r = 0, g = 0, b = 0, n = 0;
  for (let y = cy - (h >> 1); y < cy + (h >> 1); y++) {
    if (y < 0 || y >= png.height) continue;
    for (let x = cx - (w >> 1); x < cx + (w >> 1); x++) {
      if (x < 0 || x >= png.width) continue;
      const i = (y * png.width + x) * 4;
      r += png.data[i];
      g += png.data[i + 1];
      b += png.data[i + 2];
      n++;
    }
  }
  if (n === 0) return [0, 0, 0];
  return [Math.round(r / n), Math.round(g / n), Math.round(b / n)];
}

// dist is the L2 distance between two RGB triples; we use it to gate "pixel
// changed" assertions with a sensible threshold.
function dist(a, b) {
  const dr = a[0] - b[0], dg = a[1] - b[1], db = a[2] - b[2];
  return Math.sqrt(dr * dr + dg * dg + db * db);
}

// Theme submenu navigation: right-click the desktop, click the Theme row
// (top-level index 2), then click the row matching `themeLabel` (with `*`
// prefix or without). All clicks routed through Playwright mouse so the
// compositor receives bona-fide DOM mouse events.
const VIEW_W = 1280;
const VIEW_H = 800;
// Right-click anchor: well away from boot windows + the dock + DOM legend.
const ROOT_X = 600;
const ROOT_Y = 500;
const MENU_W = 170;
const ITEM_H = 24;

async function switchTheme(page, themeIndex) {
  // Right-click desktop -> root menu pops up at (ROOT_X, ROOT_Y).
  await page.mouse.click(ROOT_X, ROOT_Y, { button: "right" });
  await page.waitForTimeout(300);
  // Click "Theme" row (top-level index 2). The Theme entry's y centre is
  // ROOT_Y + 2*ITEM_H + ITEM_H/2 = ROOT_Y + 60.
  const themeRowY = ROOT_Y + 2 * ITEM_H + Math.floor(ITEM_H / 2);
  await page.mouse.click(ROOT_X + 20, themeRowY);
  await page.waitForTimeout(300);
  // Theme submenu opens to the right of the parent at the Theme row's top.
  // top-of-Theme-row = ROOT_Y + 2*ITEM_H.
  const subX = ROOT_X + MENU_W - 1;
  const subTop = ROOT_Y + 2 * ITEM_H;
  const themeY = subTop + themeIndex * ITEM_H + Math.floor(ITEM_H / 2);
  await page.mouse.click(subX + 20, themeY);
  await page.waitForTimeout(400);
}

// Dock workspace section: left part of the toolbar, 100 px wide, 28 px tall
// at the bottom of the surface. Sample its midpoint — well clear of the
// glyph row so the gradient face is what we read.
const WORKSPACE_W = 100;
const DOCK_H = 28;
// The workspace section is 100 px wide; its centre carries the label glyph
// (anti-aliased ink) so we sample at x=10 — past the 1-px bevel on the left
// edge, well clear of the centred glyph row, painting the pure gradient face.
// The y coordinate is the dock vertical mid (28 px tall, y in [VIEW_H-28,
// VIEW_H-1]).
function workspaceSamplePoint() {
  return { x: 10, y: VIEW_H - Math.floor(DOCK_H / 2) };
}

const { server, base } = await startServer();
console.log(`probe-theme: serving on ${base}`);

const browser = await chromium.launch({ headless: true, channel: "chrome" });
const consoleLines = [];
const pageErrors = [];

try {
  const page = await browser.newPage({ viewport: { width: VIEW_W, height: VIEW_H } });
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
  await page.waitForTimeout(3500);

  // ----- Baseline: Fluxbox Light --------------------------------------
  const samplePt = workspaceSamplePoint();
  const shotLight = await page.screenshot({ type: "png", path: "/tmp/wasmbox-theme-light.png", fullPage: false });
  const pngLight = PNG.sync.read(shotLight);
  const rgbLight = avgPixel(pngLight, samplePt.x, samplePt.y);
  ok(`baseline (Fluxbox Light): workspace pixel @(${samplePt.x},${samplePt.y}) = (${rgbLight.join(",")})`);
  // Fluxbox Light inactive title bg ~ #909090 (vertical gradient face).
  if (rgbLight[0] < 0x70 || rgbLight[0] > 0xC0) {
    fail(`baseline pixel R=${rgbLight[0]} not in expected light-gray band [0x70,0xC0]`);
  }

  // ----- Switch to Fluxbox Dark --------------------------------------
  await switchTheme(page, 1);
  // Allow the wire round-trip + repaint.
  await page.waitForTimeout(600);
  const shotDark = await page.screenshot({ type: "png", path: "/tmp/wasmbox-theme-dark.png", fullPage: false });
  const pngDark = PNG.sync.read(shotDark);
  const rgbDark = avgPixel(pngDark, samplePt.x, samplePt.y);
  ok(`after Fluxbox Dark: workspace pixel = (${rgbDark.join(",")})`);
  const dLightDark = dist(rgbLight, rgbDark);
  if (dLightDark < 40) {
    fail(`Fluxbox Dark did not shift the dock pixel (dist=${dLightDark.toFixed(1)}, want >=40)`);
  } else {
    ok(`Fluxbox Dark shift: dist=${dLightDark.toFixed(1)} (light->dark)`);
  }
  // Dark inactive title bg ~ #1a1a1a — much darker than light's 0x90.
  if (rgbDark[0] > 0x60) {
    fail(`Fluxbox Dark pixel R=${rgbDark[0]} should be < 0x60 (dark colour)`);
  }

  // ----- Switch to GNOME Adwaita ------------------------------------
  await switchTheme(page, 2);
  await page.waitForTimeout(600);
  const shotAdw = await page.screenshot({ type: "png", path: "/tmp/wasmbox-theme-adwaita.png", fullPage: false });
  const pngAdw = PNG.sync.read(shotAdw);
  const rgbAdw = avgPixel(pngAdw, samplePt.x, samplePt.y);
  ok(`after GNOME Adwaita: workspace pixel = (${rgbAdw.join(",")})`);
  const dDarkAdw = dist(rgbDark, rgbAdw);
  if (dDarkAdw < 40) {
    fail(`GNOME Adwaita did not shift the dock pixel from Dark (dist=${dDarkAdw.toFixed(1)})`);
  } else {
    ok(`GNOME Adwaita shift: dist=${dDarkAdw.toFixed(1)} (dark->adwaita)`);
  }
  // Adwaita inactive title bg ~ #fafafa (warm white) — close to (250,250,250).
  if (rgbAdw[0] < 0xC0) {
    fail(`GNOME Adwaita pixel R=${rgbAdw[0]} should be >= 0xC0 (warm white)`);
  }

  // ----- Switch back to Fluxbox Light --------------------------------
  await switchTheme(page, 0);
  await page.waitForTimeout(600);
  const shotLight2 = await page.screenshot({ type: "png", path: "/tmp/wasmbox-theme-light-after.png", fullPage: false });
  const pngLight2 = PNG.sync.read(shotLight2);
  const rgbLight2 = avgPixel(pngLight2, samplePt.x, samplePt.y);
  ok(`back to Fluxbox Light: workspace pixel = (${rgbLight2.join(",")})`);
  const dAdwLight2 = dist(rgbAdw, rgbLight2);
  if (dAdwLight2 < 40) {
    fail(`Switch back to Fluxbox Light did not shift the dock pixel (dist=${dAdwLight2.toFixed(1)})`);
  } else {
    ok(`Light-return shift: dist=${dAdwLight2.toFixed(1)} (adwaita->light)`);
  }
  // Should be near the original light reading.
  const dRoundTrip = dist(rgbLight, rgbLight2);
  if (dRoundTrip > 30) {
    fail(`Round-trip drift too large: dist(light,light2)=${dRoundTrip.toFixed(1)}`);
  } else {
    ok(`Round-trip OK: dist(light,light2)=${dRoundTrip.toFixed(1)}`);
  }

  if (process.exitCode === 1) {
    console.log("\n--- page console ---");
    for (const l of consoleLines.slice(-40)) console.log(l);
    if (pageErrors.length) {
      console.log("\n--- page errors ---");
      for (const e of pageErrors) console.log(e);
    }
  }
} finally {
  await browser.close();
  server.close();
}
