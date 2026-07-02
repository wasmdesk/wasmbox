// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the new "minimize / restore via dock" path.
//
// Boots the compositor in headless Chrome, finds the title-bar minimize box
// of one of the in-process windows that spawn at boot ("xterm" / "editor" /
// "about rbgo"), captures a pre-minimize screenshot, clicks the minimize
// box, captures a minimized screenshot, asserts the window's pixels are
// gone and a new task button has appeared on the dock, clicks the task
// button, asserts the window came back, and writes:
//   /tmp/wasmbox-minimize-0.png  (pre-minimize)
//   /tmp/wasmbox-minimize-1.png  (minimized)
//   /tmp/wasmbox-minimize-2.png  (restored)

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

const VIEW_W = 1280;
const VIEW_H = 800;

// Read the dock's published window-button geometry (surface coords) from the
// dock worker's global, so we can click a specific window's iconbar entry
// without hardcoding the (evolving) dock layout. Returns { w, h, buttons:
// [{ id, title, minimized, x, y, w, h }] } or null.
async function dockGeometry(page) {
  for (const worker of page.workers()) {
    try {
      const g = await worker.evaluate(() => globalThis.__wasmdockGeometry || null);
      if (g && Array.isArray(g.buttons)) return g;
    } catch (_) { /* worker may be navigating / not the dock */ }
  }
  return null;
}

// Count pixels inside (x0,y0,w,h) whose RGB matches `target` within tol.
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

// Count ink (RGB all < 0x40) inside (x0,y0,w,h).
function countInk(png, x0, y0, w, h) {
  let n = 0;
  const yEnd = Math.min(y0 + h, png.height);
  const xEnd = Math.min(x0 + w, png.width);
  const xStart = Math.max(0, x0);
  const yStart = Math.max(0, y0);
  for (let y = yStart; y < yEnd; y++) {
    for (let x = xStart; x < xEnd; x++) {
      const i = (y * png.width + x) * 4;
      if (png.data[i] < 0x40 && png.data[i+1] < 0x40 && png.data[i+2] < 0x40) n++;
    }
  }
  return n;
}

// The in-process windows that boot at compositor start cascade-place from
// (60,60) with a 28-px step. compositor.rb spawns three: xterm (240x150),
// editor (300x190), about-rbgo (220x130). The "editor" window is the
// second spawn at base+(1*step, 1*step) = (88, 88).
const STEP = 28;
const BASE_X = 60;
const BASE_Y = 60;
const TITLE_H = 22;
const CLOSE_SZ = 14;
const MIN_SZ   = 14;
const PAD = (TITLE_H - CLOSE_SZ) / 2;

// Per the Ruby Window#minimize_rect: the minimize box sits one CLOSE_SZ+PAD
// to the left of the close box.
function minimizeBoxCenter(winX, winY, winW) {
  // close box top-left
  const cx = (winX + winW) - CLOSE_SZ - PAD;
  const cy = (winY - TITLE_H) + PAD;
  // minimize box is at cx - MIN_SZ - PAD, same cy
  const mx = cx - MIN_SZ - PAD;
  const my = cy;
  return { x: mx + Math.floor(MIN_SZ / 2), y: my + Math.floor(MIN_SZ / 2) };
}

// Body center of a window — we test for the body fill colour to detect
// whether the window is visible. The "editor" body is window index 2 in
// the spawn palette ("#2ea043" = (46,160,67)).
function bodyCenter(winX, winY, winW, winH) {
  return { x: winX + Math.floor(winW / 2), y: winY + Math.floor(winH / 2) };
}

const { server, base } = await startServer();
console.log(`probe-minimize: serving on ${base}`);

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

  // Give the dock + the three boot windows time to land.
  await page.waitForTimeout(3500);

  // The "editor" window is the 2nd boot spawn; cascade-placed at index 1.
  // x = 60 + 1*28 = 88, y = 60 + 1*28 = 88, w=300, h=190. Its fill colour
  // is PALETTE[1] = "#2ea043" = (46,160,67). We pick it because its body
  // is large enough to make pixel counts stable and its title-bar minimize
  // box is squarely inside the viewport.
  const winX = BASE_X + 1 * STEP;
  const winY = BASE_Y + 1 * STEP;
  const winW = 300;
  const winH = 190;
  const FILL_RGB = [46, 160, 67];

  // Phase 0: pre-minimize screenshot.
  const shot0 = await page.screenshot({ type: "png", path: "/tmp/wasmbox-minimize-0.png", fullPage: false });
  const png0 = PNG.sync.read(shot0);
  ok("screenshot saved -> /tmp/wasmbox-minimize-0.png");
  const before = countNearColor(png0, winX, winY, winW, winH, FILL_RGB, 12);
  console.log(`info phase0 fill-pixel count: ${before}`);
  if (before < 1000) {
    fail(`pre-minimize: editor window not visible (${before} fill pixels in body rect)`);
  } else {
    ok(`pre-minimize: editor body visible (${before} fill pixels)`);
  }
  // The dock's open-window sub-section (past the 4 launcher buttons + a
  // SeparatorW gap) NOW renders one button per open compositor window — so
  // pre-minimize it carries ink (the "xterm" + "editor" + "about rbgo"
  // labels). The compositor's register_external bypasses MIN_H clamping for
  // panels, so the granted height is exactly 28.
  const DOCK_H = 28;
  const dockTop = 800 - DOCK_H;
  // Launcher row: 4 buttons * (120 + 2) - 2 = 486 wide. The open-window row
  // starts at x = 100 + 486 + 8 (SeparatorW) = 594 and runs to the clock at
  // x = W - 80 = 1200.
  const SEPARATOR_W = 8;
  const TASKS_X0 = 100 + 4 * (120 + 2) - 2 + SEPARATOR_W;
  const TASKS_X1 = 1280 - 80;
  const phase0TaskInk = countInk(png0, TASKS_X0, dockTop, TASKS_X1 - TASKS_X0, DOCK_H);
  console.log(`info phase0 open-window-section ink: ${phase0TaskInk}`);
  // With 3 boot windows + the new "iconbar shows all" rule, the section
  // should now be inked with at least the 3 titles.
  if (phase0TaskInk < 30) {
    fail(`pre-minimize: open-window section not inked (${phase0TaskInk} px) — expected 3 entries`);
  } else {
    ok(`pre-minimize: open-window section inked (${phase0TaskInk} px, 3 windows)`);
  }

  // Click the minimize box of the editor window.
  const mbtn = minimizeBoxCenter(winX, winY, winW);
  console.log(`info clicking minimize box @(${mbtn.x},${mbtn.y})`);
  await page.mouse.click(mbtn.x, mbtn.y);
  await page.waitForTimeout(1500);

  // Phase 1: minimized screenshot.
  const shot1 = await page.screenshot({ type: "png", path: "/tmp/wasmbox-minimize-1.png", fullPage: false });
  const png1 = PNG.sync.read(shot1);
  ok("screenshot saved -> /tmp/wasmbox-minimize-1.png");
  const afterMin = countNearColor(png1, winX, winY, winW, winH, FILL_RGB, 12);
  console.log(`info phase1 fill-pixel count: ${afterMin}`);
  if (afterMin > before / 4) {
    fail(`minimized: editor still visible (${afterMin} fill pixels, was ${before})`);
  } else {
    ok(`minimized: editor pixels removed (${afterMin} fill pixels, was ${before})`);
  }
  // The dock's open-window section must still carry ink (now showing "[*]
  // editor" instead of "editor" + the unchanged xterm + about-rbgo entries).
  const phase1TaskInk = countInk(png1, TASKS_X0, dockTop, TASKS_X1 - TASKS_X0, DOCK_H);
  console.log(`info phase1 open-window-section ink: ${phase1TaskInk}`);
  if (phase1TaskInk < 30) {
    fail(`minimized: open-window section not inked (${phase1TaskInk} px) — expected entries`);
  } else {
    ok(`minimized: open-window section inked (${phase1TaskInk} px)`);
  }

  // Restore the editor by clicking its iconbar entry. Locate it precisely via
  // the dock's published button geometry (globalThis.__wasmdockGeometry on the
  // dock worker) — no hardcoded iconbar layout, so this survives dock redesigns.
  const geo = await dockGeometry(page);
  if (!geo) {
    fail("could not read __wasmdockGeometry from the dock worker");
  } else {
    const btn = geo.buttons.find((b) => b.title === "editor");
    if (!btn) {
      fail(`editor not in dock geometry (have: ${geo.buttons.map((b) => b.title).join(", ")})`);
    } else {
      // Dock surface is bottom-anchored + centered: screen =
      // ((VIEW_W - w)/2 + x, (VIEW_H - h) + y).
      const editorBtnX = Math.floor((VIEW_W - geo.w) / 2) + btn.x + Math.floor(btn.w / 2);
      const editorBtnY = (VIEW_H - geo.h) + btn.y + Math.floor(btn.h / 2);
      console.log(`info clicking editor iconbar button @(${editorBtnX},${editorBtnY}) [id ${btn.id}, minimized=${btn.minimized}]`);
      await page.mouse.click(editorBtnX, editorBtnY);
      await page.waitForTimeout(1500);
    }
  }

  // Phase 2: restored screenshot.
  const shot2 = await page.screenshot({ type: "png", path: "/tmp/wasmbox-minimize-2.png", fullPage: false });
  const png2 = PNG.sync.read(shot2);
  ok("screenshot saved -> /tmp/wasmbox-minimize-2.png");
  const afterRestore = countNearColor(png2, winX, winY, winW, winH, FILL_RGB, 12);
  console.log(`info phase2 fill-pixel count: ${afterRestore}`);
  if (afterRestore < 1000) {
    fail(`restored: editor not visible again (${afterRestore} fill pixels)`);
  } else {
    ok(`restored: editor body back (${afterRestore} fill pixels)`);
  }
  // The open-window section keeps showing the 3 entries (one per window),
  // but the editor's label loses its "[*]" prefix. Easiest invariant: ink
  // is still present in roughly the same range as phase0.
  const phase2TaskInk = countInk(png2, TASKS_X0, dockTop, TASKS_X1 - TASKS_X0, DOCK_H);
  console.log(`info phase2 open-window-section ink: ${phase2TaskInk}`);
  if (phase2TaskInk < 30) {
    fail(`restored: open-window section not inked (${phase2TaskInk} px) — expected 3 entries`);
  } else {
    ok(`restored: open-window section still inked (${phase2TaskInk} px)`);
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
