// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the new "iconbar shows ALL open windows"
// path (Fluxbox-style).
//
// Boots the compositor in headless Chrome, lets the 3 boot windows + the
// dock settle, then exercises:
//
//   - the iconbar carries 4 static launcher buttons + 3 dynamic open-window
//     buttons (one per boot window: xterm / editor / about rbgo);
//   - left-clicking the iconbar entry for a NON-focused window raises that
//     window and shifts the iconbar's focused-button indicator to it;
//   - right-clicking an iconbar entry closes the matching window (it
//     disappears from the canvas + from the iconbar);
//
// Screenshots:
//   /tmp/wasmbox-iconbar-initial.png   (3 windows + 4 launchers in iconbar)
//   /tmp/wasmbox-iconbar-focused.png   (after raising the xterm via iconbar)
//   /tmp/wasmbox-iconbar-closed.png    (after right-clicking iconbar entry)

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

// Read the RGB of one pixel.
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
const N_LAUNCHERS = 4; // terminal / editor / files / hello
const DOCK_H = 28;
const VIEW_W = 1280;
const VIEW_H = 800;

// Boot windows from compositor.rb (cascade base 60/60 step 28). The three
// in-process boot calls (xterm/editor/about rbgo) produce ids 1/2/3; the
// hello external client autoSpawned by compositor.worker.js takes id 4
// (cascade index 3). Iconbar entries follow stack order so the iconbar reads
// xterm / editor / about rbgo / hello (focused, since it spawned last).
const BOOT_WINS = [
  { title: "xterm",     idx: 0, w: 240, h: 150, fill: [31, 111, 235] },
  { title: "editor",    idx: 1, w: 300, h: 190, fill: [46, 160, 67]  },
  { title: "about rbgo",idx: 2, w: 220, h: 130, fill: [210, 153, 34] },
];
// hello is an external client; its body is a self-painted SAB so we only
// check its iconbar entry, not its canvas body fill.
const N_OPEN_WINDOWS = 4; // 3 boot + 1 hello autoSpawn
const STEP = 28;
const BASE_X = 60;
const BASE_Y = 60;
const dockTop = VIEW_H - DOCK_H;

// Iconbar geometry: the open-window row starts past the launcher row + a
// SeparatorW gap. The launcher row spans N*(W+gap)-gap pixels.
const ICONBAR_X = WORKSPACE_W;
const LAUNCHER_ROW_END = ICONBAR_X + N_LAUNCHERS * (ICONBAR_BUTTON_W + ICONBAR_BUTTON_GAP) - ICONBAR_BUTTON_GAP;
const WINDOWS_X0 = LAUNCHER_ROW_END + SEPARATOR_W;

// Center coordinate of the i-th open-window iconbar button.
function windowBtnCenter(i) {
  const x = WINDOWS_X0 + i * (ICONBAR_BUTTON_W + ICONBAR_BUTTON_GAP) + Math.floor(ICONBAR_BUTTON_W / 2);
  const y = dockTop + Math.floor(DOCK_H / 2);
  return { x, y };
}

// Iconbar button TOP row (where bevel-highlight pixels live) at mid-button.
// The button row starts at y = dockTop + ICONBAR_VPAD.
function windowBtnTopMid(i) {
  const x = WINDOWS_X0 + i * (ICONBAR_BUTTON_W + ICONBAR_BUTTON_GAP) + Math.floor(ICONBAR_BUTTON_W / 2);
  const y = dockTop + ICONBAR_VPAD;
  return { x, y };
}

// Body center of a boot window — used to verify a window covers its body
// pixels (open) or not (closed).
function bodyCenter(b) {
  const x = BASE_X + b.idx * STEP + Math.floor(b.w / 2);
  const y = BASE_Y + b.idx * STEP + Math.floor(b.h / 2);
  return { x, y, b };
}

const { server, base } = await startServer();
console.log(`probe-iconbar: serving on ${base}`);

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

  // -------- Phase 0: initial state -----------------------------------
  const shot0 = await page.screenshot({ type: "png", path: "/tmp/wasmbox-iconbar-initial.png", fullPage: false });
  const png0 = PNG.sync.read(shot0);
  ok("screenshot saved -> /tmp/wasmbox-iconbar-initial.png");

  // 1) The 3 boot windows must be visible somewhere — their titlebar/decoration
  // is painted by the compositor (not covered by the hello client which
  // overlaps the body region). Sample the TITLE strip (compositor-painted)
  // not the body (potentially covered).
  for (const b of BOOT_WINS) {
    // The title bar sits ABOVE the body anchor (y - TITLE_H .. y). The first
    // 100 px of the title strip is always visible (windows cascade by 28 px
    // so the leftmost 28 px of each lower window's title peeks past the next).
    const tx = BASE_X + b.idx * STEP;
    const ty = BASE_Y + b.idx * STEP - 11; // mid-titlebar (TITLE_H=22)
    // Just check the strip is NOT the desktop background (#11131a = 17,19,26).
    const samples = countNearColor(png0, tx, ty, 24, 8, [17, 19, 26], 4);
    if (samples > 100) {
      fail(`boot window "${b.title}" titlebar covered by desktop (${samples} desktop px)`);
    } else {
      ok(`boot window "${b.title}" titlebar visible`);
    }
  }

  // 2) The iconbar must carry N_OPEN_WINDOWS buttons. Each button has a
  // mid-top-row bevel highlight pixel. For an UNFOCUSED button the top
  // stroke is bright (white-ish, 255). For the FOCUSED button it is dark.
  // Exactly ONE button (the focused one — the last-spawned client) should
  // show the sunken (dark) top-stroke; the rest are raised (bright).
  let bright = 0, dark = 0, focusedIdx = -1;
  for (let i = 0; i < N_OPEN_WINDOWS; i++) {
    const p = windowBtnTopMid(i);
    const [r, g, b] = pix(png0, p.x, p.y);
    if (r > 0xC0 && g > 0xC0 && b > 0xC0) bright++;
    else if (r < 0x80 && g < 0x80 && b < 0x80) { dark++; focusedIdx = i; }
    console.log(`info window-btn[${i}] top-stroke = (${r},${g},${b})`);
  }
  if (bright !== N_OPEN_WINDOWS - 1 || dark !== 1) {
    fail(`expected ${N_OPEN_WINDOWS-1} raised + 1 sunken iconbar entries, got bright=${bright} dark=${dark}`);
  } else {
    ok(`iconbar shows ${bright} raised (unfocused) + 1 sunken (focused at idx ${focusedIdx}) window buttons`);
  }

  // 3) The 4 static launchers must still be there — their top-stroke is the
  // raised bright highlight. Probe their mid-top-row pixels.
  for (let i = 0; i < N_LAUNCHERS; i++) {
    const x = ICONBAR_X + i * (ICONBAR_BUTTON_W + ICONBAR_BUTTON_GAP) + Math.floor(ICONBAR_BUTTON_W / 2);
    const y = dockTop + ICONBAR_VPAD;
    const [r, g, b] = pix(png0, x, y);
    if (!(r > 0xC0 && g > 0xC0 && b > 0xC0)) {
      fail(`launcher ${i} top-stroke not raised: (${r},${g},${b})`);
    }
  }
  ok("4 static launcher buttons present (raised bevel)");

  // -------- Phase 1: focus a NON-focused window via iconbar ----------
  // The last-spawned client is currently focused. Click the iconbar entry
  // for xterm (index 0 — the first window in stack order). xterm should
  // rise to topmost AND the iconbar's focused indicator should shift to
  // index 0.
  const xtermBtn = windowBtnCenter(0);
  console.log(`info clicking xterm iconbar button @(${xtermBtn.x},${xtermBtn.y})`);
  await page.mouse.click(xtermBtn.x, xtermBtn.y);
  await page.waitForTimeout(1500);

  const shot1 = await page.screenshot({ type: "png", path: "/tmp/wasmbox-iconbar-focused.png", fullPage: false });
  const png1 = PNG.sync.read(shot1);
  ok("screenshot saved -> /tmp/wasmbox-iconbar-focused.png");

  // After focus(xterm), xterm is now topmost on the canvas — its body fill
  // (blue #1f6feb) covers the center of its window area.
  const xtermBody = BOOT_WINS[0];
  const cb = bodyCenter(xtermBody);
  const xtermCount = countNearColor(png1, cb.x - 20, cb.y - 20, 40, 40, xtermBody.fill, 12);
  if (xtermCount < 500) {
    fail(`after focus(xterm): xterm not on top (${xtermCount} blue pixels in body center, expected > 500)`);
  } else {
    ok(`after focus(xterm): xterm raised to top (${xtermCount} blue pixels visible)`);
  }

  // The iconbar's focused indicator must follow the focus shift. The snapshot
  // order mirrors the WM stack (bottom-to-top), and focus(xterm) pushed xterm
  // to the TOP of the stack — so after the click, xterm sits at the LAST
  // iconbar position (idx N_OPEN_WINDOWS-1) and is sunken. The first
  // iconbar slot (the new bottom-of-stack window) must be raised.
  const focusedIdxAfter = N_OPEN_WINDOWS - 1;
  const pFTop = windowBtnTopMid(focusedIdxAfter);
  const [rF, gF, bF] = pix(png1, pFTop.x, pFTop.y);
  if (!(rF < 0x80 && gF < 0x80 && bF < 0x80)) {
    fail(`after focus(xterm): iconbar[${focusedIdxAfter}] top-stroke not sunken: (${rF},${gF},${bF})`);
  } else {
    ok(`iconbar[${focusedIdxAfter}] now sunken (xterm, focused) after click`);
  }
  // No OTHER entry should still be sunken.
  let stillSunken = 0;
  for (let i = 0; i < N_OPEN_WINDOWS; i++) {
    if (i === focusedIdxAfter) continue;
    const p = windowBtnTopMid(i);
    const [r, g, b] = pix(png1, p.x, p.y);
    if (r < 0x80 && g < 0x80 && b < 0x80) stillSunken++;
  }
  if (stillSunken > 0) {
    fail(`after focus(xterm): ${stillSunken} other iconbar entries still sunken (expected only iconbar[${focusedIdxAfter}])`);
  } else {
    ok(`only iconbar[${focusedIdxAfter}] is sunken — focus indicator moved correctly`);
  }

  // -------- Phase 2: right-click closes a window ---------------------
  // After phase 1's focus(xterm), the iconbar reads (bottom-to-top stack):
  // [editor, about rbgo, hello, xterm]. Right-click iconbar[1] = "about
  // rbgo" — the window should close (decoration gone) and the iconbar
  // shrinks to 3 entries.
  const targetBtn = windowBtnCenter(1);
  console.log(`info right-clicking iconbar[1] (about rbgo) @(${targetBtn.x},${targetBtn.y})`);
  await page.mouse.click(targetBtn.x, targetBtn.y, { button: "right" });
  await page.waitForTimeout(1500);

  const shot2 = await page.screenshot({ type: "png", path: "/tmp/wasmbox-iconbar-closed.png", fullPage: false });
  const png2 = PNG.sync.read(shot2);
  ok("screenshot saved -> /tmp/wasmbox-iconbar-closed.png");

  // The "about rbgo" window's body fill is gold (210,153,34) — but the
  // window is partially covered by hello (overlapping cascade). The
  // strongest invariant: the iconbar now shows 3 entries, not 4. Probe the
  // 4th slot (idx 3) — it must NOT carry a sunken or raised window-button
  // bevel stroke (so neither pure white 255 nor pure dark 64).
  const lastIdx = N_OPEN_WINDOWS - 1;
  const lastTop = windowBtnTopMid(lastIdx);
  const [rg, gg, bg] = pix(png2, lastTop.x, lastTop.y);
  if ((rg > 0xF0 && gg > 0xF0 && bg > 0xF0) || (rg < 0x50 && gg < 0x50 && bg < 0x50)) {
    fail(`after close: iconbar slot[${lastIdx}] still painted as a window button (${rg},${gg},${bg})`);
  } else {
    ok(`after close: iconbar slot[${lastIdx}] empty (top-stroke=${rg},${gg},${bg}) — one window gone`);
  }

  // And the now-iconbar[N_OPEN_WINDOWS-2] slot must still be the LAST real
  // window button (sunken stroke if it was the focused xterm).
  const newLast = N_OPEN_WINDOWS - 2;
  const newLastTop = windowBtnTopMid(newLast);
  const [rn, gn, bn] = pix(png2, newLastTop.x, newLastTop.y);
  if ((rn > 0xF0 && gn > 0xF0 && bn > 0xF0) || (rn < 0x80 && gn < 0x80 && bn < 0x80)) {
    ok(`after close: iconbar slot[${newLast}] is a window button (${rn},${gn},${bn})`);
  } else {
    fail(`after close: iconbar slot[${newLast}] not a window button (${rn},${gn},${bn})`);
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
