// SPDX-License-Identifier: BSD-3-Clause
//
// Headless probe for the dock iconbar — one button per open window, a visible
// focus indicator, left-click focuses/raises, right-click closes. Everything is
// located via the dock's __wasmdockGeometry hook (surface-coord button rects +
// per-window focused/minimized state), so this survives dock-layout changes
// instead of hardcoding button positions.
//
// Assertions are RELATIVE (independent of the exact boot layout / styling):
//   1. >=2 window buttons; exactly one is `focused`; the focused button's
//      top-stroke pixels differ from an unfocused one's (focus is visible).
//   2. Left-clicking a non-focused window's button makes it the focused one.
//   3. Right-clicking a window's button removes it (the window closes).
//
// Run: node test/probe-iconbar.mjs   (self-serves; bundled chromium)

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const VIEW_W = 1280, VIEW_H = 800, BOOT_TIMEOUT_MS = 10000;
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
      let rel = normalize(decodeURIComponent((req.url || "/").split("?")[0])).replace(/^(\.\.[/\\])+/, "");
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
  return new Promise((r) => server.listen(0, "127.0.0.1", () => r({ server, base: `http://127.0.0.1:${server.address().port}` })));
}

// Read the dock's published window-button geometry from the dock worker.
async function dockGeometry(page) {
  for (const worker of page.workers()) {
    try {
      const g = await worker.evaluate(() => globalThis.__wasmdockGeometry || null);
      if (g && Array.isArray(g.buttons)) return g;
    } catch (_) { /* not the dock worker */ }
  }
  return null;
}
// Screen coords for a button (dock is bottom-center anchored).
function btnScreen(geo, btn) {
  const dockX = Math.floor((VIEW_W - geo.w) / 2), dockY = VIEW_H - geo.h;
  return {
    cx: dockX + btn.x + Math.floor(btn.w / 2),
    cy: dockY + btn.y + Math.floor(btn.h / 2),
    topY: dockY + btn.y + 1, // bevel top-stroke row
  };
}
const pix = (png, x, y) => { const i = ((y | 0) * png.width + (x | 0)) * 4; return [png.data[i], png.data[i + 1], png.data[i + 2]]; };
const shot = async (page) => PNG.sync.read(await page.screenshot({ type: "png", fullPage: false }));

const { server, base } = await startServer();
console.log(`probe-iconbar: serving on ${base}`);
const browser = await chromium.launch({ headless: true });

try {
  const page = await browser.newPage({ viewport: { width: VIEW_W, height: VIEW_H } });
  const pageErrors = [];
  page.on("pageerror", (e) => pageErrors.push(String(e)));
  await page.goto(`${base}/index.html`, { waitUntil: "load" });
  await page.waitForFunction(
    () => { if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError)); return globalThis.wasmboxReady === true; },
    { timeout: BOOT_TIMEOUT_MS },
  );
  ok("compositor booted");
  await page.waitForTimeout(1500);

  // --- Phase 0: one focused button, focus visually distinct ---------------
  let geo = await dockGeometry(page);
  if (!geo) {
    fail("could not read __wasmdockGeometry from the dock worker");
  } else {
    const wins = geo.buttons;
    if (wins.length >= 2) ok(`iconbar shows ${wins.length} window buttons`);
    else fail(`need >= 2 window buttons for the test, got ${wins.length}`);

    const focused = wins.filter((b) => b.focused);
    if (focused.length === 1) ok(`exactly one focused window button ("${focused[0].title}")`);
    else fail(`expected exactly 1 focused button, got ${focused.length}`);

    const f = focused[0], u = wins.find((b) => !b.focused);
    if (f && u) {
      const png = await shot(page);
      const fs = btnScreen(geo, f), us = btnScreen(geo, u);
      const fp = pix(png, fs.cx, fs.topY), up = pix(png, us.cx, us.topY);
      const d = Math.abs(fp[0] - up[0]) + Math.abs(fp[1] - up[1]) + Math.abs(fp[2] - up[2]);
      if (d > 60) ok(`focus indicator visible: focused top-stroke (${fp}) vs unfocused (${up}), Δ${d}`);
      else fail(`focused vs unfocused buttons look identical: (${fp}) vs (${up}), Δ${d}`);
    }
  }

  // --- Phase 1: left-click a non-focused window's button -> it focuses -----
  geo = await dockGeometry(page);
  const target = geo && geo.buttons.find((b) => !b.focused);
  if (!target) {
    fail("no unfocused window button to click");
  } else {
    const s = btnScreen(geo, target);
    console.log(`info left-clicking "${target.title}" @(${s.cx},${s.cy})`);
    await page.mouse.click(s.cx, s.cy);
    await page.waitForTimeout(1200);
    const g2 = await dockGeometry(page);
    const now = g2 && g2.buttons.find((b) => b.id === target.id);
    if (now && now.focused) ok(`clicking "${target.title}" focused it (focus shifted)`);
    else fail(`clicking "${target.title}" did not focus it (focused=${now && now.focused})`);
    const nf = g2 ? g2.buttons.filter((b) => b.focused).length : -1;
    if (nf === 1) ok("still exactly one focused button after the click");
    else fail(`expected 1 focused button after the click, got ${nf}`);
  }

  // --- Phase 2: right-click a window's button -> it closes -----------------
  geo = await dockGeometry(page);
  if (geo && geo.buttons.length >= 2) {
    const before = geo.buttons.length;
    const toClose = geo.buttons.find((b) => !b.focused) || geo.buttons[0];
    const s = btnScreen(geo, toClose);
    console.log(`info right-clicking "${toClose.title}" @(${s.cx},${s.cy}) to close it`);
    await page.mouse.click(s.cx, s.cy, { button: "right" });
    await page.waitForTimeout(1200);
    const g3 = await dockGeometry(page);
    const stillThere = g3 && g3.buttons.find((b) => b.id === toClose.id);
    if (g3 && g3.buttons.length === before - 1 && !stillThere) {
      ok(`right-click closed "${toClose.title}" (${before} -> ${g3.buttons.length} window buttons)`);
    } else {
      fail(`right-click did not close "${toClose.title}" (${before} -> ${g3 ? g3.buttons.length : "?"}, present=${!!stillThere})`);
    }
  } else {
    fail("not enough window buttons to test close");
  }

  if (pageErrors.length) fail(`pageerror(s): ${pageErrors.join(" | ")}`);
  else ok("no pageerror");
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(failures ? `\nRESULT: FAIL (${failures})` : "\nRESULT: PASS");
process.exit(failures ? 1 : 0);
