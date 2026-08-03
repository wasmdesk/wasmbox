// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for window SNAPPING / half-tiling (the first
// Batch-4 window-management feature): compositor/04_window_manager.rb
// snap_rect / snap_left / snap_right / maximize / restore_snap, driven from
// compositor/06_core.rb by the Super/⌘+arrow shortcuts (on_keydown) and the
// drag-to-edge gesture (on_mousemove preview + on_mouseup commit), gated behind
// Compositor::SNAPPING.
//
// The compositor paints to a worker OffscreenCanvas and owns all WM geometry in
// Ruby, so rather than reading pixels this probe reads the focused window's live
// geometry + the work-area metrics through the __wasmboxFocusedRect test hook
// (published every frame by the Ruby render loop). It:
//
//   1. Boots the compositor with the snapping path enabled (no throw at boot).
//   2. Spawns a real decorated, focused window (__wasmboxSpawnWindow) and reads
//      its original free geometry.
//   3. Super+Left  -> the window occupies the LEFT half of the WORK AREA, i.e.
//      not under the dock (y+h == screen_h - reserved_bottom) and not over the
//      titlebar strip (y == reserved_top). Exact against snap_rect math.
//   4. Super+Right -> the RIGHT half, flush to the right edge.
//   5. Super+Up    -> MAXIMIZED to the whole work area.
//   6. Super+Down  -> RESTORED to the exact original rect + un-snapped.
//   7. A drag to the LEFT edge shows a "left" preview and, on release, snaps the
//      window to the left half; the preview then clears.
//
// Every assertion is derived from the SAME payload's reserved_top/bottom +
// screen size, so it is correct whether or not the dock is present.

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
        () => typeof globalThis.__wasmboxFocusedRect === "function" &&
              typeof globalThis.__wasmboxSnap === "function" &&
              typeof globalThis.__wasmboxSpawnWindow === "function",
      );
      if (has) return w;
    } catch (_) { /* not it */ }
  }
  return null;
}

const rectOf = (cw) => cw.evaluate(() => globalThis.__wasmboxFocusedRect());

// The expected work-area body rect for a zone, from the SAME payload's metrics
// (mirrors WindowManager#snap_rect so the probe is dock-presence-agnostic).
function expectRect(zone, p) {
  const awY = p.reserved_top;
  const awW = p.screen_w;
  const awH = p.screen_h - p.reserved_top - p.reserved_bottom;
  const half = Math.floor(awW / 2);
  if (zone === "left")  return { x: 0,    y: awY, w: half,        h: awH };
  if (zone === "right") return { x: half, y: awY, w: awW - half,  h: awH };
  if (zone === "max")   return { x: 0,    y: awY, w: awW,         h: awH };
  return null;
}

const eqRect = (a, b) => a && b && a.x === b.x && a.y === b.y && a.w === b.w && a.h === b.h;
const showR = (r) => r ? `[${r.x},${r.y} ${r.w}x${r.h}]` : "null";

async function settle(page, ms) { await page.waitForTimeout(ms); }

const { server, base } = await startServer();
console.log(`probe-snapping: serving on ${base}`);

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
  ok("compositor booted (snapping path did not throw at boot)");
  // Let the auto-spawned clients (incl. the dock panel, which sets the reserved
  // bottom strip) settle so the work-area metrics are stable for the whole run.
  await settle(page, 2500);

  const cw = await compositorWorker(page);
  if (!cw) {
    fail("could not find the compositor worker (no __wasmboxFocusedRect / __wasmboxSnap / __wasmboxSpawnWindow)");
    throw new Error("no compositor worker");
  }

  // 2. Spawn a real decorated, focused window + read its original free geometry.
  await cw.evaluate(() => globalThis.__wasmboxSpawnWindow("snap-probe"));
  await settle(page, 400);
  const p0 = await rectOf(cw);
  if (!p0 || p0.x < 0) {
    fail("no focused window after __wasmboxSpawnWindow");
    throw new Error("no focused window");
  }
  const orig = { x: p0.x, y: p0.y, w: p0.w, h: p0.h };
  if (p0.state !== "") {
    fail(`freshly spawned window should be un-snapped, got state=${p0.state}`);
  } else {
    ok(`spawned focused window ${showR(orig)} on a ${p0.screen_w}x${p0.screen_h} desktop (reserved top=${p0.reserved_top} bottom=${p0.reserved_bottom})`);
  }

  // Helper: run a snap chord, then assert the focused rect + state.
  async function snapAndCheck(dir, zone, label) {
    await cw.evaluate((d) => globalThis.__wasmboxSnap(d), dir);
    await settle(page, 300);
    const p = await rectOf(cw);
    const got = { x: p.x, y: p.y, w: p.w, h: p.h };
    const want = expectRect(zone, p);
    if (p.state !== zone) {
      fail(`${label}: snap_state should be "${zone}", got "${p.state}"`);
    } else if (!eqRect(got, want)) {
      fail(`${label}: rect ${showR(got)} != expected ${showR(want)}`);
    } else if (got.y + got.h > p.screen_h - p.reserved_bottom) {
      fail(`${label}: window extends under the dock (y+h=${got.y + got.h} > ${p.screen_h - p.reserved_bottom})`);
    } else if (got.y < p.reserved_top) {
      fail(`${label}: titlebar strip not reserved (y=${got.y} < ${p.reserved_top})`);
    } else {
      ok(`${label}: ${showR(got)} (state=${p.state}, clear of dock + titlebar)`);
    }
    return p;
  }

  // 3-5. Super+Left / Right / Up.
  await snapAndCheck("left", "left", "Super+Left -> left half");
  await snapAndCheck("right", "right", "Super+Right -> right half");
  await snapAndCheck("up", "max", "Super+Up -> maximize");

  // 6. Super+Down -> restore to the exact original rect + un-snap.
  await cw.evaluate(() => globalThis.__wasmboxSnap("down"));
  await settle(page, 300);
  const pr = await rectOf(cw);
  const restored = { x: pr.x, y: pr.y, w: pr.w, h: pr.h };
  if (pr.state !== "") {
    fail(`Super+Down should un-snap (state ""), got "${pr.state}"`);
  } else if (!eqRect(restored, orig)) {
    fail(`Super+Down: restored ${showR(restored)} != original ${showR(orig)}`);
  } else {
    ok(`Super+Down -> restored to the original ${showR(restored)} + un-snapped`);
  }

  // 7. Drag to the LEFT edge: preview appears, release snaps to the left half.
  const tb = await rectOf(cw); // window is at its original free spot again
  const startX = tb.x + Math.floor(tb.w / 2);
  const startY = tb.y - Math.floor(tb.reserved_top / 2); // titlebar sits above the body
  await page.mouse.move(startX, startY);
  await page.mouse.down();
  // Move toward the left edge in a couple of steps so intermediate mousemoves fire.
  await page.mouse.move(Math.floor(startX / 2), startY, { steps: 4 });
  await page.mouse.move(4, startY, { steps: 4 });
  await settle(page, 200);
  const dp = await rectOf(cw);
  if (dp.preview !== "left") {
    fail(`drag to the left edge should preview "left", got "${dp.preview}"`);
  } else {
    ok(`drag to the left edge shows the "left" landing preview`);
  }
  await page.mouse.up();
  await settle(page, 300);
  const ap = await rectOf(cw);
  const snapped = { x: ap.x, y: ap.y, w: ap.w, h: ap.h };
  const wantLeft = expectRect("left", ap);
  if (ap.state !== "left") {
    fail(`drag-release should snap left (state "left"), got "${ap.state}"`);
  } else if (!eqRect(snapped, wantLeft)) {
    fail(`drag-release: rect ${showR(snapped)} != expected left half ${showR(wantLeft)}`);
  } else if (ap.preview !== "") {
    fail(`preview should clear after release, got "${ap.preview}"`);
  } else {
    ok(`drag-release snapped to the left half ${showR(snapped)} and cleared the preview`);
  }

  // Client-asset load races (a spawned client's .wasm not yet served) are
  // unrelated to the snapping under test — the compositor itself booted fine.
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
