// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the ALT-TAB window switcher (a Batch-4 window-
// management feature): compositor/05_alttab.rb (the AltTab cursor model) +
// compositor/15_alttab.rb (the centered thumbnail-strip overlay + the keyboard/
// probe wiring), driven from compositor/06_core.rb, gated behind
// Compositor::ALTTAB.
//
// A synthetic metaKey/Alt+Tab chord is unreliable headless, so this probe drives
// the REAL switcher transitions through the __wasmboxAltTab(cmd) test hook (the
// same code path the keyboard uses: bus -> alttab_probe) and reads the live
// overlay state the render loop publishes through __wasmboxAltTabState(). It:
//
//   1. Boots the compositor with the switcher path enabled (no throw at boot).
//   2. Spawns three real decorated, focused, compositor-painted windows
//      (__wasmboxSpawnWindow) so there is a deterministic candidate ring.
//   3. "open"  -> the overlay is up, N>=3 tiles, cursor parked on the focused
//      window (index 0), and the panel is CENTERED on the desktop.
//   4. "advance" x2 -> the highlight moves 0 -> 1 -> 2 (mod N), so the selection
//      visibly walks the strip.
//   5. "commit" -> the switcher closes AND the newly-selected window becomes the
//      focused window: __wasmboxFocusedRect matches the tile's published rect.
//   6. "open" + "advance" + "cancel" -> the switcher closes choosing nothing;
//      the focused window is unchanged.
//
// Centering + focus assertions derive from the SAME published payloads
// (__wasmboxAltTabState / __wasmboxFocusedRect), so the probe is independent of
// how many boot clients happen to be on the desktop.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 20000;
const VIEW_W = 1280;
const VIEW_H = 800;

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

async function compositorWorker(page) {
  for (const w of page.workers()) {
    try {
      const has = await w.evaluate(
        () => typeof globalThis.__wasmboxAltTab === "function" &&
              typeof globalThis.__wasmboxAltTabState === "function" &&
              typeof globalThis.__wasmboxSpawnWindow === "function" &&
              typeof globalThis.__wasmboxFocusedRect === "function",
      );
      if (has) return w;
    } catch (_) { /* not it */ }
  }
  return null;
}

const stateOf = (cw) => cw.evaluate(() => globalThis.__wasmboxAltTabState());
const focusOf = (cw) => cw.evaluate(() => globalThis.__wasmboxFocusedRect());
const drive = (cw, cmd) => cw.evaluate((c) => globalThis.__wasmboxAltTab(c), cmd);

const showR = (r) => r ? `[${r.x},${r.y} ${r.w}x${r.h}]` : "null";
async function settle(page, ms) { await page.waitForTimeout(ms); }

const { server, base } = await startServer();
console.log(`probe-alttab: serving on ${base}`);

const browser = await chromium.launch({ headless: true });
const pageErrors = [];

try {
  const page = await browser.newPage({ viewport: { width: VIEW_W, height: VIEW_H } });
  page.on("pageerror", (e) => pageErrors.push(String(e)));

  await page.goto(`${base}/index.html`, { waitUntil: "load" });
  await page.waitForFunction(
    () => {
      if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
      return globalThis.wasmboxReady === true;
    },
    { timeout: BOOT_TIMEOUT_MS },
  );
  ok("compositor booted (alt-tab path did not throw at boot)");
  // Let the auto-spawned clients (incl. the dock panel) settle.
  await settle(page, 2500);

  const cw = await compositorWorker(page);
  if (!cw) {
    fail("could not find the compositor worker (no __wasmboxAltTab / __wasmboxAltTabState / __wasmboxSpawnWindow / __wasmboxFocusedRect)");
    throw new Error("no compositor worker");
  }

  // Before opening, the switcher is inactive.
  const s0 = await stateOf(cw);
  if (!s0 || s0.active !== 0) {
    fail(`switcher should be inactive before open, got ${JSON.stringify(s0)}`);
  } else {
    ok("switcher inactive before any Alt+Tab");
  }

  // 2. Spawn three deterministic in-process windows (the last is focused).
  await drive(cw, "cancel"); // ensure closed
  await cw.evaluate(() => globalThis.__wasmboxSpawnWindow("alttab-A"));
  await settle(page, 250);
  await cw.evaluate(() => globalThis.__wasmboxSpawnWindow("alttab-B"));
  await settle(page, 250);
  await cw.evaluate(() => globalThis.__wasmboxSpawnWindow("alttab-C"));
  await settle(page, 400);

  const beforeFocus = await focusOf(cw);
  if (!beforeFocus || beforeFocus.x < 0) {
    fail("no focused window after spawning the three test windows");
    throw new Error("no focus");
  }
  const SW = beforeFocus.screen_w;
  const SH = beforeFocus.screen_h;
  ok(`spawned 3 windows on a ${SW}x${SH} desktop; focused ${showR(beforeFocus)}`);

  // 3. Open the switcher: up, N>=3 tiles, cursor on the focused window, centered.
  await drive(cw, "open");
  await settle(page, 200);
  const so = await stateOf(cw);
  if (!so || so.active !== 1) {
    fail(`open should raise the switcher, got ${JSON.stringify(so)}`);
    throw new Error("open failed");
  }
  if (so.count < 3) {
    fail(`switcher should list at least the 3 spawned windows, got count=${so.count}`);
  } else {
    ok(`switcher up with ${so.count} thumbnail tiles`);
  }
  if (so.index !== 0) {
    fail(`open should park the cursor on the focused window (index 0), got ${so.index}`);
  } else {
    ok("cursor parked on the focused window (index 0)");
  }
  const wantX = Math.floor((SW - so.panel_w) / 2);
  const wantY = Math.floor((SH - so.panel_h) / 2);
  if (so.panel_x !== wantX || so.panel_y !== wantY) {
    fail(`overlay not centered: panel at (${so.panel_x},${so.panel_y}) != expected (${wantX},${wantY}) for ${so.panel_w}x${so.panel_h}`);
  } else {
    ok(`overlay centered: ${so.panel_w}x${so.panel_h} panel at (${so.panel_x},${so.panel_y})`);
  }

  // 4. Advance twice: the highlight walks 0 -> 1 -> 2 (mod count).
  await drive(cw, "advance");
  await settle(page, 150);
  const s1 = await stateOf(cw);
  const want1 = 1 % s1.count;
  if (s1.index !== want1) {
    fail(`first advance should move the highlight to ${want1}, got ${s1.index}`);
  } else {
    ok(`advance moved the highlight to index ${s1.index}`);
  }
  await drive(cw, "advance");
  await settle(page, 150);
  const s2 = await stateOf(cw);
  const want2 = 2 % s2.count;
  if (s2.index !== want2) {
    fail(`second advance should move the highlight to ${want2}, got ${s2.index}`);
  } else {
    ok(`advance moved the highlight to index ${s2.index}`);
  }
  // Record the selected tile's window rect BEFORE committing.
  const selRect = { x: s2.sel_x, y: s2.sel_y, w: s2.sel_w, h: s2.sel_h };
  if (selRect.w <= 0 || selRect.h <= 0) {
    fail(`selected tile has no window rect: ${showR(selRect)}`);
  } else {
    ok(`selected tile maps to window ${showR(selRect)}`);
  }

  // 5. Commit: the switcher closes AND the selected window takes focus.
  await drive(cw, "commit");
  await settle(page, 300);
  const sc = await stateOf(cw);
  if (sc.active !== 0) {
    fail(`commit should close the switcher, still active: ${JSON.stringify(sc)}`);
  } else {
    ok("commit closed the switcher");
  }
  const afterFocus = await focusOf(cw);
  const gotFocus = { x: afterFocus.x, y: afterFocus.y, w: afterFocus.w, h: afterFocus.h };
  const eq = gotFocus.x === selRect.x && gotFocus.y === selRect.y &&
             gotFocus.w === selRect.w && gotFocus.h === selRect.h;
  if (!eq) {
    fail(`commit should focus the selected window ${showR(selRect)}, but focus is ${showR(gotFocus)}`);
  } else {
    ok(`commit focused the newly-selected window ${showR(gotFocus)}`);
  }

  // 6. Cancel path: open, advance, cancel -> closed, focus unchanged.
  const preCancel = await focusOf(cw);
  await drive(cw, "open");
  await settle(page, 150);
  await drive(cw, "advance");
  await settle(page, 150);
  const midCancel = await stateOf(cw);
  if (midCancel.active !== 1) {
    fail("switcher should be up before cancel");
  }
  await drive(cw, "cancel");
  await settle(page, 250);
  const sx = await stateOf(cw);
  if (sx.active !== 0) {
    fail(`cancel should close the switcher, still active: ${JSON.stringify(sx)}`);
  } else {
    ok("cancel closed the switcher");
  }
  const postCancel = await focusOf(cw);
  const focusUnchanged = preCancel.x === postCancel.x && preCancel.y === postCancel.y &&
                         preCancel.w === postCancel.w && preCancel.h === postCancel.h;
  if (!focusUnchanged) {
    fail(`cancel must not change focus: was ${showR(preCancel)}, now ${showR(postCancel)}`);
  } else {
    ok(`cancel left focus unchanged ${showR(postCancel)}`);
  }

  // Client-asset load races (a spawned client's .wasm not yet served) are
  // unrelated to the switcher under test — the compositor itself booted fine.
  const fatal = pageErrors.filter((e) => !/bootWasm: HTTP \d+ for/.test(e));
  const benign = pageErrors.length - fatal.length;
  if (benign) console.log(`info ${benign} benign client-asset load error(s) ignored`);
  if (fatal.length) {
    fail(`pageerror(s): ${fatal.join(" | ")}`);
  } else {
    ok("no fatal pageerror");
  }
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
