// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the new Fluxbox-style N=4 workspaces path.
//
// Boots the compositor in headless Chrome, lets the 3 boot windows + the
// dock settle, then exercises:
//
//   1. The 3 boot windows + hello autoSpawn land on workspace 1 (the default
//      active workspace). With the AppDock grouped-window model the open
//      windows collapse into a RUNNING indicator on their launcher instead of
//      a per-window iconbar button — so ≥1 launcher shows a running dot.
//   2. Left-clicking the workspace section on the dock cycles to workspace 2.
//      The 3 boot windows + hello disappear from the canvas (they live on
//      workspace 1, not the active one); the dock stays visible (panels
//      ignore the workspace filter).
//   3. On workspace 2 no windows are registered, so NO launcher shows a
//      running dot (0 running) — the dock reflects the active workspace.
//   4. Cycling back to workspace 1 (3 more clicks on the workspace section,
//      wrapping 2->3->4->1) restores all 3 boot windows + hello and the
//      running-launcher count is restored to its ws1 reference.
//
// The per-launcher running state is read robustly (no pixel counting) from
// the dock's exposed __wasmdockGeometry.launchers[*].running global.
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
const ICONBAR_VPAD = 2;
const DOCK_H = 28;
const VIEW_W = 1280;
const VIEW_H = 800;

const BOOT_WINS = [
  { title: "xterm",     idx: 0, w: 240, h: 150, fill: [31, 111, 235] },
  { title: "editor",    idx: 1, w: 300, h: 190, fill: [46, 160, 67]  },
  { title: "about rbgo",idx: 2, w: 220, h: 130, fill: [210, 153, 34] },
];
// The boot window count evolves as the desktop grows, so this probe asserts
// workspace behaviour RELATIVELY (isolation + reversibility) rather than against
// a hardcoded count — the ws1 running-launcher count observed at boot is the
// reference.
const STEP = 28;
const BASE_X = 60;
const BASE_Y = 60;
const dockTop = VIEW_H - DOCK_H;
const DESKTOP_BG = [17, 19, 26]; // Theme::DESKTOP "#11131a"

// Workspace section center.
function workspaceCenter() {
  return { x: WORKSPACE_W / 2, y: dockTop + DOCK_H / 2 };
}

const ICONBAR_X = WORKSPACE_W;

// Body center of a boot window.
function bodyCenter(b) {
  const x = BASE_X + b.idx * STEP + Math.floor(b.w / 2);
  const y = BASE_Y + b.idx * STEP + Math.floor(b.h / 2);
  return { x, y, b };
}

// Read the dock's published launcher geometry (with per-launcher running flag)
// from the dock WORKER's global — the dock runs in a Web Worker, so its
// globalThis.__wasmdockGeometry is not visible on the page. Returns the
// geometry object { w, h, launchers: [{ id, x, y, w, h, running }] } or null.
async function dockGeometry(page) {
  for (const worker of page.workers()) {
    try {
      const g = await worker.evaluate(() => globalThis.__wasmdockGeometry || null);
      if (g && Array.isArray(g.launchers)) return g;
    } catch (_) { /* worker may be navigating / not the dock */ }
  }
  return null;
}

// Number of launchers currently showing a RUNNING indicator (grouped-window
// model). Read robustly from the dock's exposed geometry — no pixel counting —
// which the dock republishes on every windows_changed.
async function runningCount(page) {
  const g = await dockGeometry(page);
  if (!g) return -1; // dock worker not found — caller treats as a failure
  return g.launchers.filter((l) => l.running).length;
}

const { server, base } = await startServer();
console.log(`probe-workspaces: serving on ${base}`);

const browser = await chromium.launch({ headless: true });
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

  // The dock reflects the windows on the active workspace (ws1): with the
  // grouped-window model, ≥1 launcher shows a running indicator. We don't
  // hardcode the count; use it as the reference for the switch + restore checks.
  const r1 = await runningCount(page);
  if (r1 < 1) fail('ws1: no launcher shows a running indicator');
  else ok('ws1: '+r1+' launcher(s) running');

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

  // The dock follows the workspace: ws2 has no registered windows, so NO
  // launcher shows a running dot (a global/unfiltered dock would keep ws1's
  // running launchers — this catches that regression). The compositor has
  // already sent windows_changed(nil) for ws2 by the time we screenshotted
  // above; the dock republishes its geometry from that handler, so its worker
  // global is already settled at 0 running here.
  const r2 = await runningCount(page);
  if (r2 !== 0) fail('ws2: iconbar not workspace-filtered — '+r2+' launchers running, want 0');
  else ok('ws2: iconbar workspace-filtered (0 running)');

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
  const r1b = await runningCount(page);
  if (r1b !== r1) fail('ws1 (after cycle): running not restored — '+r1b+', was '+r1);
  else ok('ws1 (after cycle): running restored ('+r1b+')');

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
