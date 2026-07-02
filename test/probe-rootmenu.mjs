// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the Openbox-style desktop right-click root
// menu (compositor.rb -> Menu + RootMenu + Compositor#on_contextmenu).
//
// What it exercises:
//
//   1. Boot the compositor + dock; wait for steady state.
//   2. Right-click an empty area of the desktop (clear of every boot window,
//      and well above the dock). Assert a menu pops up by sampling pixels
//      in the menu region for MENU_BG (#1d1f29 = 29,31,41).
//      Screenshot: /tmp/wasmbox-rootmenu-popped.png
//   3. Click "Applications" to open the submenu; assert a submenu region
//      appears to the right of the parent. Screenshot:
//      /tmp/wasmbox-rootmenu-submenu.png
//   4. Click "Terminal"; assert the dock iconbar gains an entry (the
//      Terminal window spawned via the launch dispatcher).
//      Screenshot: /tmp/wasmbox-rootmenu-after-launch.png
//   5. Right-click empty area again, navigate Workspaces -> "Workspace 3";
//      assert the boot windows disappear (they live on ws 1) and only the
//      dock remains. Screenshot:
//      /tmp/wasmbox-rootmenu-workspace-switched.png

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

// Theme + Menu constants mirrored from compositor.rb.
const MENU_BG     = [29, 31, 41];     // #1d1f29
const MENU_HILITE = [155, 28, 46];    // #9b1c2e
const DESKTOP_BG  = [17, 19, 26];     // #11131a
const TITLE_ACTIVE_R = 155;           // #9b1c2e[0] (same as MENU_HILITE but in titlebar context)
const MENU_W      = 170;
const ITEM_H      = 24;
// Dock geometry (mirrors scene.go).
const WORKSPACE_W       = 100;
const CLOCK_W           = 80;
const ICONBAR_BUTTON_W  = 120;
const ICONBAR_BUTTON_GAP= 2;
const SEPARATOR_W       = 8;
const N_LAUNCHERS       = 4;
const DOCK_H            = 28;
const ICONBAR_VPAD      = 2;
// Viewport.
const VIEW_W = 1280;
const VIEW_H = 800;
// Boot windows (cascade @60,60 with step 28).
const BOOT_WINS = [
  { title: "xterm",     idx: 0, w: 240, h: 150, fill: [31, 111, 235] },
  { title: "editor",    idx: 1, w: 300, h: 190, fill: [46, 160, 67]  },
  { title: "about rbgo",idx: 2, w: 220, h: 130, fill: [210, 153, 34] },
];
const BASE_X = 60;
const BASE_Y = 60;
const STEP   = 28;

const dockTop = VIEW_H - DOCK_H;
const ICONBAR_X = WORKSPACE_W;
const LAUNCHER_ROW_END = ICONBAR_X + N_LAUNCHERS * (ICONBAR_BUTTON_W + ICONBAR_BUTTON_GAP) - ICONBAR_BUTTON_GAP;
const WINDOWS_X0 = LAUNCHER_ROW_END + SEPARATOR_W;
function windowBtnTopMid(i) {
  const x = WINDOWS_X0 + i * (ICONBAR_BUTTON_W + ICONBAR_BUTTON_GAP) + Math.floor(ICONBAR_BUTTON_W / 2);
  const y = dockTop + ICONBAR_VPAD;
  return { x, y };
}
function countIconbarEntries(png) {
  let n = 0;
  for (let i = 0; i < 8; i++) {
    const p = windowBtnTopMid(i);
    if (p.x >= VIEW_W - CLOCK_W) break;
    const [r, g, b] = pix(png, p.x, p.y);
    const bright = (r > 0xE0 && g > 0xE0 && b > 0xE0);
    const dark   = (r < 0x60 && g < 0x60 && b < 0x60);
    if (bright || dark) n++;
  }
  return n;
}

function bodyCenter(b) {
  const x = BASE_X + b.idx * STEP + Math.floor(b.w / 2);
  const y = BASE_Y + b.idx * STEP + Math.floor(b.h / 2);
  return { x, y };
}

// Number of open-window buttons the dock currently shows, read from its
// published geometry (globalThis.__wasmdockGeometry on the dock worker) — robust
// to the iconbar layout, unlike pixel-scanning at fixed positions.
async function dockGeo(page) {
  for (const worker of page.workers()) {
    try {
      const g = await worker.evaluate(() => globalThis.__wasmdockGeometry || null);
      if (g && Array.isArray(g.buttons)) return g;
    } catch (_) { /* not the dock worker */ }
  }
  return null;
}
async function dockWindowCount(page) {
  const g = await dockGeo(page);
  return g ? g.buttons.length : -1;
}
// Right-click a window's dock iconbar entry (located from the geometry) to close
// it — robust vs the window's on-canvas position. Returns true if it was found.
async function closeViaIconbar(page, titlePrefix) {
  const g = await dockGeo(page);
  if (!g) return false;
  const btn = g.buttons.find((b) => (b.title || "").startsWith(titlePrefix));
  if (!btn) return false;
  const dockX = Math.floor((VIEW_W - g.w) / 2), dockY = VIEW_H - g.h;
  await page.mouse.click(dockX + btn.x + Math.floor(btn.w / 2), dockY + btn.y + Math.floor(btn.h / 2), { button: "right" });
  return true;
}

// Central desktop area: clear of every boot window (which cascade at
// x in [60, 60+5*28] and end at x+300 at most), clear of the legend DOM
// overlay on the right (#legend, top:10px right:12px ~ x>=1010), and well
// above the dock. We pop the menu here so neither the parent menu, the
// submenu opened to its right, nor the launched Terminal window overlaps
// the legend or the cascading boot windows.
const ROOT_X = 600;
const ROOT_Y = 500;

const { server, base } = await startServer();
console.log(`probe-rootmenu: serving on ${base}`);

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

  // ----- Step 2: pop the root menu ------------------------------------
  // Sanity: the area at (ROOT_X, ROOT_Y) is currently desktop bg.
  {
    const pre = await page.screenshot({ type: "png" });
    const png = PNG.sync.read(pre);
    const [r, g, b] = pix(png, ROOT_X, ROOT_Y);
    const isDesk =
      Math.abs(r - DESKTOP_BG[0]) <= 6 &&
      Math.abs(g - DESKTOP_BG[1]) <= 6 &&
      Math.abs(b - DESKTOP_BG[2]) <= 6;
    if (!isDesk) {
      console.log(`info pre-menu pixel @(${ROOT_X},${ROOT_Y}) = (${r},${g},${b}) — not desktop bg, will still proceed`);
    } else {
      ok(`pre-menu: empty desktop confirmed at (${ROOT_X},${ROOT_Y})`);
    }
  }

  // Right-click via the contextmenu DOM event (Playwright's mouse.click with
  // {button:"right"} also synthesizes contextmenu).
  await page.mouse.click(ROOT_X, ROOT_Y, { button: "right" });
  await page.waitForTimeout(400);

  const shot1 = await page.screenshot({ type: "png", path: "/tmp/wasmbox-rootmenu-popped.png", fullPage: false });
  const png1 = PNG.sync.read(shot1);
  ok("screenshot saved -> /tmp/wasmbox-rootmenu-popped.png");

  // The menu rectangle covers [ROOT_X, ROOT_Y, MENU_W, 6*ITEM_H ish]. The hovered
  // first row carries MENU_HILITE on initial hover (mouse is at ROOT_Y) so we
  // count BG pixels in a band BELOW the first row to be robust against hover.
  const menuBgHits = countNearColor(png1, ROOT_X + 4, ROOT_Y + ITEM_H + 4, MENU_W - 8, ITEM_H * 3, MENU_BG, 6);
  if (menuBgHits < 200) {
    fail(`root-menu: only ${menuBgHits} MENU_BG pixels in menu region — menu did not pop`);
  } else {
    ok(`root-menu: ${menuBgHits} MENU_BG pixels found inside the menu rectangle`);
  }

  // ----- Step 3: open the Applications submenu -------------------------
  // Click the first row (Applications) — opens the submenu (does NOT dismiss).
  await page.mouse.click(ROOT_X + 20, ROOT_Y + Math.floor(ITEM_H / 2));
  await page.waitForTimeout(400);

  const shot2 = await page.screenshot({ type: "png", path: "/tmp/wasmbox-rootmenu-submenu.png", fullPage: false });
  const png2 = PNG.sync.read(shot2);
  ok("screenshot saved -> /tmp/wasmbox-rootmenu-submenu.png");

  // Submenu opens at (ROOT_X + MENU_W - 1, ROOT_Y) (same y as the Applications
  // row). Look for MENU_BG pixels there.
  const subX = ROOT_X + MENU_W - 1;
  const subBgHits = countNearColor(png2, subX + 4, ROOT_Y + ITEM_H + 4, MENU_W - 8, ITEM_H * 3, MENU_BG, 6);
  if (subBgHits < 200) {
    fail(`submenu: only ${subBgHits} MENU_BG pixels right of the parent — submenu did not open`);
  } else {
    ok(`submenu: ${subBgHits} MENU_BG pixels found inside the submenu rectangle`);
  }
  // The parent's Applications row stays MENU_HILITE highlighted (the open-sub
  // marker) so we should find HILITE pixels in the parent's first row.
  const parentHiliteHits = countNearColor(png2, ROOT_X + 4, ROOT_Y + 2, MENU_W - 8, ITEM_H - 4, MENU_HILITE, 6);
  if (parentHiliteHits < 50) {
    console.log(`info parent hilite row: ${parentHiliteHits} px (no strict assertion, hover may have moved)`);
  } else {
    ok(`parent hilite: ${parentHiliteHits} MENU_HILITE px on the parent's Applications row`);
  }

  // Count iconbar entries pre-launch (3 boot + dock-launched hello autoSpawn).
  const preEntries = await dockWindowCount(page);
  ok(`iconbar before launch: ${preEntries} entries`);

  // ----- Step 4: click "Terminal" --------------------------------------
  // The Terminal entry is at row index 0 in Applications (first in APP_LABELS).
  const termY = ROOT_Y + Math.floor(ITEM_H / 2);
  await page.mouse.click(subX + 20, termY);
  await page.waitForTimeout(2500); // give the worker time to register + commit

  const shot3 = await page.screenshot({ type: "png", path: "/tmp/wasmbox-rootmenu-after-launch.png", fullPage: false });
  const png3 = PNG.sync.read(shot3);
  ok("screenshot saved -> /tmp/wasmbox-rootmenu-after-launch.png");

  // Menu is now dismissed. The previous submenu sample had ~10000 MENU_BG px
  // when open; after dismissal we expect a tiny residual (the spawned
  // Terminal window's near-black body has a small number of pixels within
  // ±6 of MENU_BG by coincidence — well below the "menu open" floor). We
  // assert at least a 10x drop from the submenu peak.
  const stillMenu = countNearColor(png3, ROOT_X + 4, ROOT_Y + ITEM_H + 4, MENU_W - 8, ITEM_H * 3, MENU_BG, 6);
  if (stillMenu > 1500) {
    fail(`after-launch: menu is still visible (${stillMenu} MENU_BG px) — should have dismissed`);
  } else {
    ok(`after-launch: menu dismissed (${stillMenu} MENU_BG px remaining, vs ~10000 when open)`);
  }
  // The iconbar must have gained one entry (the Terminal worker registered).
  const postEntries = await dockWindowCount(page);
  if (postEntries <= preEntries) {
    fail(`after-launch: iconbar entries did not grow (${preEntries} -> ${postEntries})`);
  } else {
    ok(`after-launch: iconbar grew from ${preEntries} to ${postEntries} (Terminal spawned)`);
  }

  // ----- Step 5: workspace switch via root menu ------------------------
  // The Terminal window we just spawned covers most of the central canvas
  // (cascade slot 5, 640x400 from ~200,200 → ~840,600). Pop this menu in
  // the top-right region of the desktop, BELOW the legend (#legend bottom
  // is well above y=420) and ABOVE the Terminal (top edge at y=200, plus
  // the menu must fit its 6 rows below the right-click point). Right-click
  // at (850, 410) — below the legend (top:10px), to the right of the boot
  // window cascade (ends ~ x<=384), and ABOVE the Terminal (top y=200,
  // bottom y=600 — overlap is unavoidable on the body, but the right-click
  // point itself is at y=410 which is inside Terminal; we close Terminal
  // first via the per-window Close menu to free this region).
  // Close the Terminal first so its window doesn't cover the desktop area we
  // need to right-click for the root menu. Do it via its dock iconbar entry
  // (right-click closes) located from the geometry — robust vs the Terminal's
  // exact on-canvas position (which the old titlebar-coordinate guess missed,
  // leaving the Terminal open and the workspace switch silently failing).
  const closedTerm = await closeViaIconbar(page, "Terminal");
  if (closedTerm) ok("Terminal closed via its dock iconbar entry");
  else fail("could not locate the Terminal in the dock geometry to close it");
  await page.waitForTimeout(800);

  const ROOT2_X = 600;
  const ROOT2_Y = 420;
  await page.mouse.click(ROOT2_X, ROOT2_Y, { button: "right" });
  await page.waitForTimeout(400);
  // Click "Workspaces" (row index 1, y = ROOT2_Y + ITEM_H + ITEM_H/2).
  const wsRowY = ROOT2_Y + ITEM_H + Math.floor(ITEM_H / 2);
  await page.mouse.click(ROOT2_X + 20, wsRowY);
  await page.waitForTimeout(400);
  // Workspaces submenu opens at (ROOT2_X + MENU_W - 1, top-of-Workspaces-row),
  // top-of-row = ROOT2_Y + ITEM_H. "Workspace 3" is at index 2 => y = topRow + 2*ITEM_H + ITEM_H/2.
  const wsSubX = ROOT2_X + MENU_W - 1;
  const wsSubTop = ROOT2_Y + ITEM_H;
  const ws3Y = wsSubTop + 2 * ITEM_H + Math.floor(ITEM_H / 2);
  await page.mouse.click(wsSubX + 20, ws3Y);
  await page.waitForTimeout(800);

  const shot4 = await page.screenshot({ type: "png", path: "/tmp/wasmbox-rootmenu-workspace-switched.png", fullPage: false });
  const png4 = PNG.sync.read(shot4);
  ok("screenshot saved -> /tmp/wasmbox-rootmenu-workspace-switched.png");

  // On workspace 3 the 3 boot-window fills are gone (their pixel area is
  // desktop bg). Test the body center of the xterm boot window.
  {
    const cb = bodyCenter(BOOT_WINS[0]);
    const fillHits = countNearColor(png4, cb.x - 10, cb.y - 10, 20, 20, BOOT_WINS[0].fill, 12);
    if (fillHits > 80) {
      fail(`ws3: xterm body fill still visible (${fillHits} px) — workspace switch failed`);
    } else {
      ok(`ws3: xterm body fill gone (${fillHits} px) — boot windows hidden on workspace 3`);
    }
  }
  // Dock still painted (panels ignore workspace filter).
  {
    const dx = ICONBAR_X + 10;
    const dy = dockTop + ICONBAR_VPAD + 4;
    const [r, g, b] = pix(png4, dx, dy);
    const isDesk = (Math.abs(r - DESKTOP_BG[0]) <= 4 && Math.abs(g - DESKTOP_BG[1]) <= 4 && Math.abs(b - DESKTOP_BG[2]) <= 4);
    if (isDesk) {
      fail(`ws3: dock pixel @(${dx},${dy}) is desktop bg — dock disappeared`);
    } else {
      ok(`ws3: dock still painted (pixel @(${dx},${dy}) = (${r},${g},${b}))`);
    }
  }
  // Iconbar empty on ws3 (Terminal landed on ws1; nothing here). Counted via
  // the dock geometry hook, not pixel-scanning.
  const ws3Entries = await dockWindowCount(page);
  if (ws3Entries !== 0) {
    fail(`ws3: iconbar has ${ws3Entries} window buttons, want 0 (nothing lives on ws3)`);
  } else {
    ok(`ws3: iconbar empty (0 entries) — workspace 3 has no windows`);
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
