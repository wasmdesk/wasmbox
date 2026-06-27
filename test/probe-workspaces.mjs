// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the new Fluxbox-style N=4 workspaces path.
//
// Boots the compositor in headless Chrome, lets the 3 boot windows + the
// dock settle, then exercises:
//
//   1. The 3 boot windows + hello autoSpawn land on workspace 1 (the default
//      active workspace). The iconbar reflects them.
//   2. Left-clicking the workspace section on the dock cycles to workspace 2.
//      The 3 boot windows + hello disappear from the canvas (they live on
//      workspace 1, not the active one); the dock stays visible (panels
//      ignore the workspace filter).
//   3. The iconbar empties on workspace 2 (no windows registered there).
//   4. Cycling back to workspace 1 (3 more clicks on the workspace section,
//      wrapping 2->3->4->1) restores all 3 boot windows + hello.
//
// Screenshots:
//   /tmp/wasmbox-workspace-1a.png  (ws 1 with 3 boot windows + hello)
//   /tmp/wasmbox-workspace-2a.png  (ws 2 empty, only dock visible)
//   /tmp/wasmbox-workspace-1b.png  (back on ws 1, 3 boot windows + hello)

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 20000;

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

// Count near-RGB pixels inside (x0,y0,w,h).
function countNearColor(png, x0, y0, w, h, target, tol) {
  let n = 0;
  const yEnd = Math.min(y0 + h, png.height);
  const xEnd = Math.min(x0 + w, png.width);
  const xStart = Math.max(0, x0);
  const yStart = Math.max(0, y0);
  for (let y = yStart; y < yEnd; y++) {
    for (let x = xStart; x < xEnd; x++) {
      const i = (y * png.width + x) * 4;
      if (
        Math.abs(png.data[i]   - target[0]) <= tol &&
        Math.abs(png.data[i+1] - target[1]) <= tol &&
        Math.abs(png.data[i+2] - target[2]) <= tol
      ) n++;
    }
  }
  return n;
}

function pix(png, x, y) {
  const i = (y * png.width + x) * 4;
  return [png.data[i], png.data[i+1], png.data[i+2]];
}

// Geometry mirrors compositor.rb + scene.go constants.
const WORKSPACE_W = 100;
const CLOCK_W = 80;
const ICONBAR_BUTTON_W = 120;
const ICONBAR_BUTTON_GAP = 2;
const SEPARATOR_W = 8;
const ICONBAR_VPAD = 2;
const N_LAUNCHERS = 4;
const DOCK_H = 28;
const VIEW_W = 1280;
const VIEW_H = 800;

const BOOT_WINS = [
  { title: "xterm",     idx: 0, w: 240, h: 150, fill: [31, 111, 235] },
  { title: "editor",    idx: 1, w: 300, h: 190, fill: [46, 160, 67]  },
  { title: "about rbgo",idx: 2, w: 220, h: 130, fill: [210, 153, 34] },
];
const N_OPEN_WINDOWS = 4; // 3 boot + 1 hello autoSpawn
const STEP = 28;
const BASE_X = 60;
const BASE_Y = 60;
const dockTop = VIEW_H - DOCK_H;
const DESKTOP_BG = [17, 19, 26]; // Theme::DESKTOP "#11131a"

// Workspace section center.
function workspaceCenter() {
  return { x: WORKSPACE_W / 2, y: dockTop + DOCK_H / 2 };
}

// Iconbar window button top-mid pixel.
const ICONBAR_X = WORKSPACE_W;
const LAUNCHER_ROW_END = ICONBAR_X + N_LAUNCHERS * (ICONBAR_BUTTON_W + ICONBAR_BUTTON_GAP) - ICONBAR_BUTTON_GAP;
const WINDOWS_X0 = LAUNCHER_ROW_END + SEPARATOR_W;
function windowBtnTopMid(i) {
  const x = WINDOWS_X0 + i * (ICONBAR_BUTTON_W + ICONBAR_BUTTON_GAP) + Math.floor(ICONBAR_BUTTON_W / 2);
  const y = dockTop + ICONBAR_VPAD;
  return { x, y };
}

// Body center of a boot window.
function bodyCenter(b) {
  const x = BASE_X + b.idx * STEP + Math.floor(b.w / 2);
  const y = BASE_Y + b.idx * STEP + Math.floor(b.h / 2);
  return { x, y, b };
}

// Count how many iconbar window-button slots show a button bevel stroke
// (either bright "raised" or dark "sunken"). Empty slots show the iconbar
// background gradient (~196,196,196), so we use stricter thresholds: the
// bevel highlight is pure (255,255,255); the sunken stroke is (64,64,64).
// 0xE0 / 0x60 cleanly separates the two from the mid-tone gradient.
function countIconbarEntries(png) {
  let n = 0;
  for (let i = 0; i < 8; i++) { // probe more than the max we expect
    const p = windowBtnTopMid(i);
    if (p.x >= VIEW_W - CLOCK_W) break;
    const [r, g, b] = pix(png, p.x, p.y);
    const bright = (r > 0xE0 && g > 0xE0 && b > 0xE0);
    const dark   = (r < 0x60 && g < 0x60 && b < 0x60);
    if (bright || dark) n++;
  }
  return n;
}

const { server, base } = await startServer();
console.log(`probe-workspaces: serving on ${base}`);

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

  // -------- Phase 1a: workspace 1 with 3 boot windows + hello -----------
  const shot1a = await page.screenshot({ type: "png", path: "/tmp/wasmbox-workspace-1a.png", fullPage: false });
  const png1a = PNG.sync.read(shot1a);
  ok("screenshot saved -> /tmp/wasmbox-workspace-1a.png");

  // All 3 boot windows must have a visible titlebar strip.
  for (const b of BOOT_WINS) {
    const tx = BASE_X + b.idx * STEP;
    const ty = BASE_Y + b.idx * STEP - 11;
    const desktopHits = countNearColor(png1a, tx, ty, 24, 8, DESKTOP_BG, 4);
    if (desktopHits > 100) {
      fail(`ws1: boot window "${b.title}" titlebar covered by desktop (${desktopHits} desktop px)`);
    } else {
      ok(`ws1: boot window "${b.title}" titlebar visible`);
    }
  }

  // Iconbar must carry N_OPEN_WINDOWS entries.
  const entries1a = countIconbarEntries(png1a);
  if (entries1a !== N_OPEN_WINDOWS) {
    fail(`ws1: iconbar entries = ${entries1a}, want ${N_OPEN_WINDOWS}`);
  } else {
    ok(`ws1: iconbar shows ${entries1a} window entries`);
  }

  // -------- Phase 2: click workspace section -> ws 2 -------------------
  const wsBtn = workspaceCenter();
  console.log(`info clicking workspace section @(${wsBtn.x},${wsBtn.y})`);
  await page.mouse.click(wsBtn.x, wsBtn.y);
  await page.waitForTimeout(1500);

  const shot2a = await page.screenshot({ type: "png", path: "/tmp/wasmbox-workspace-2a.png", fullPage: false });
  const png2a = PNG.sync.read(shot2a);
  ok("screenshot saved -> /tmp/wasmbox-workspace-2a.png");

  // On workspace 2 the 3 boot windows are gone — their pixel coverage area
  // is now desktop background. Test ONE titlebar strip (xterm @ idx 0,
  // most fully exposed since cascade only steps down-right).
  {
    const b = BOOT_WINS[0];
    const tx = BASE_X + b.idx * STEP;
    const ty = BASE_Y + b.idx * STEP - 11;
    // After the switch the titlebar strip is desktop bg (3 windows hidden).
    const desktopHits = countNearColor(png2a, tx, ty, 24, 8, DESKTOP_BG, 4);
    if (desktopHits < 100) {
      fail(`ws2: boot window "${b.title}" titlebar still visible (only ${desktopHits} desktop px)`);
    } else {
      ok(`ws2: boot window "${b.title}" hidden (titlebar replaced by desktop)`);
    }
  }

  // Body fills of the 3 boot windows must be gone too.
  for (const b of BOOT_WINS) {
    const cb = bodyCenter(b);
    const fillHits = countNearColor(png2a, cb.x - 10, cb.y - 10, 20, 20, b.fill, 12);
    if (fillHits > 100) {
      fail(`ws2: window "${b.title}" body fill (${b.fill}) still visible (${fillHits} px)`);
    } else {
      ok(`ws2: window "${b.title}" body fill gone (${fillHits} px)`);
    }
  }

  // Dock is still visible — sample a pixel deep inside its area where the
  // launcher row paints. We check it is NOT desktop background.
  {
    const dx = ICONBAR_X + 10;
    const dy = dockTop + ICONBAR_VPAD + 4;
    const [r, g, b] = pix(png2a, dx, dy);
    const isDesktop = (Math.abs(r - DESKTOP_BG[0]) <= 4 && Math.abs(g - DESKTOP_BG[1]) <= 4 && Math.abs(b - DESKTOP_BG[2]) <= 4);
    if (isDesktop) {
      fail(`ws2: dock pixel @(${dx},${dy}) is desktop bg — dock disappeared`);
    } else {
      ok(`ws2: dock still painted (pixel @(${dx},${dy}) = (${r},${g},${b}))`);
    }
  }

  // Iconbar empty on workspace 2.
  const entries2a = countIconbarEntries(png2a);
  if (entries2a !== 0) {
    fail(`ws2: iconbar entries = ${entries2a}, want 0 (no windows on ws 2)`);
  } else {
    ok(`ws2: iconbar empty (0 entries)`);
  }

  // -------- Phase 3: cycle back to ws 1 via 3 more clicks (2->3->4->1) --
  for (let cycle = 0; cycle < 3; cycle++) {
    await page.mouse.click(wsBtn.x, wsBtn.y);
    await page.waitForTimeout(700);
  }

  const shot1b = await page.screenshot({ type: "png", path: "/tmp/wasmbox-workspace-1b.png", fullPage: false });
  const png1b = PNG.sync.read(shot1b);
  ok("screenshot saved -> /tmp/wasmbox-workspace-1b.png");

  // All 3 boot windows reappear.
  for (const b of BOOT_WINS) {
    const tx = BASE_X + b.idx * STEP;
    const ty = BASE_Y + b.idx * STEP - 11;
    const desktopHits = countNearColor(png1b, tx, ty, 24, 8, DESKTOP_BG, 4);
    if (desktopHits > 100) {
      fail(`ws1 (after cycle): boot window "${b.title}" still hidden (${desktopHits} desktop px)`);
    } else {
      ok(`ws1 (after cycle): boot window "${b.title}" restored`);
    }
  }
  const entries1b = countIconbarEntries(png1b);
  if (entries1b !== N_OPEN_WINDOWS) {
    fail(`ws1 (after cycle): iconbar entries = ${entries1b}, want ${N_OPEN_WINDOWS}`);
  } else {
    ok(`ws1 (after cycle): iconbar shows ${entries1b} window entries`);
  }

  if (pageErrors.length) {
    fail(`pageerror(s): ${pageErrors.join(" | ")}`);
  } else {
    ok("no pageerror");
  }
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
