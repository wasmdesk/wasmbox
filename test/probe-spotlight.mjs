// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the SPOTLIGHT launcher (a Batch-4 feature):
// compositor/05_spotlight.rb (the Spotlight fuzzy-filter cursor model) +
// compositor/16_spotlight.rb (the centered search-palette overlay + the
// keyboard/probe wiring), driven from compositor/06_core.rb, gated behind
// Compositor::SPOTLIGHT.
//
// A synthetic ⌘/Ctrl+Space chord + per-key typing is unreliable headless, so
// this probe drives the REAL launcher transitions through the
// __wasmboxSpotlight(cmd) test hook (the same code path the keyboard uses:
// bus -> spotlight_probe) and reads the live overlay state the render loop
// publishes through __wasmboxSpotlightState(). It:
//
//   1. Boots the compositor with the launcher path enabled (no throw at boot).
//   2. Spawns one real window so there is a focused surface + a known desktop
//      size to assert centering against.
//   3. "open"       -> the palette is up, count>=1, panel CENTERED, and its
//      pixels actually painted (a __wasmboxReadRegion of the panel is non-black).
//   4. "query:term" -> the fuzzy filter narrows to exactly Terminal (count 1,
//      selected label "Terminal").
//   5. "commit"     -> the palette closes AND the highlighted app launches: the
//      published last-launched id becomes "terminal" and the launch counter
//      increments (the deterministic launch signal; the spawned client's window
//      registers asynchronously, so we assert the dispatch, not a race).
//   6. "open" + "query" + "cancel" -> the palette closes launching NOTHING: the
//      launch counter is unchanged.
//
// Centering + launch assertions derive from the SAME published payloads
// (__wasmboxSpotlightState / __wasmboxFocusedRect), so the probe is independent
// of how many boot clients happen to be on the desktop.

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
        () => typeof globalThis.__wasmboxSpotlight === "function" &&
              typeof globalThis.__wasmboxSpotlightState === "function" &&
              typeof globalThis.__wasmboxReadRegion === "function" &&
              typeof globalThis.__wasmboxSpawnWindow === "function" &&
              typeof globalThis.__wasmboxFocusedRect === "function",
      );
      if (has) return w;
    } catch (_) { /* not it */ }
  }
  return null;
}

const stateOf = (cw) => cw.evaluate(() => globalThis.__wasmboxSpotlightState());
const focusOf = (cw) => cw.evaluate(() => globalThis.__wasmboxFocusedRect());
const drive = (cw, cmd) => cw.evaluate((c) => globalThis.__wasmboxSpotlight(c), cmd);
const readRegion = (cw, r) => cw.evaluate((rr) => globalThis.__wasmboxReadRegion(rr.x, rr.y, rr.w, rr.h), r);

async function settle(page, ms) { await page.waitForTimeout(ms); }

const { server, base } = await startServer();
console.log(`probe-spotlight: serving on ${base}`);

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
  ok("compositor booted (spotlight path did not throw at boot)");
  await settle(page, 2500);

  const cw = await compositorWorker(page);
  if (!cw) {
    fail("could not find the compositor worker (no __wasmboxSpotlight / __wasmboxSpotlightState / __wasmboxReadRegion / __wasmboxSpawnWindow / __wasmboxFocusedRect)");
    throw new Error("no compositor worker");
  }

  // Before opening, the palette is inactive.
  await drive(cw, "cancel"); // ensure closed
  await settle(page, 150);
  const s0 = await stateOf(cw);
  if (!s0 || s0.active !== 0) {
    fail(`palette should be inactive before open, got ${JSON.stringify(s0)}`);
  } else {
    ok("palette inactive before any ⌘/Ctrl+Space");
  }

  // Spawn one window so there is a focused surface + a known desktop size.
  await cw.evaluate(() => globalThis.__wasmboxSpawnWindow("spotlight-probe"));
  await settle(page, 400);
  const focus = await focusOf(cw);
  if (!focus || focus.x < 0) {
    fail("no focused window after spawning the probe window");
    throw new Error("no focus");
  }
  const SW = focus.screen_w;
  const SH = focus.screen_h;
  ok(`desktop ${SW}x${SH}, focused window present`);

  // 3. Open the palette: up, count>=1, centered, and actually painted.
  await drive(cw, "open");
  await settle(page, 250);
  const so = await stateOf(cw);
  if (!so || so.active !== 1) {
    fail(`open should raise the palette, got ${JSON.stringify(so)}`);
    throw new Error("open failed");
  }
  if (so.count < 1) {
    fail(`open should list at least one launchable app, got count=${so.count}`);
  } else {
    ok(`palette up with ${so.count} launchable apps listed`);
  }
  const wantX = Math.floor((SW - so.panel_w) / 2);
  const wantY = Math.floor((SH - so.panel_h) / 2);
  if (so.panel_x !== wantX || so.panel_y !== wantY) {
    fail(`palette not centered: panel at (${so.panel_x},${so.panel_y}) != expected (${wantX},${wantY}) for ${so.panel_w}x${so.panel_h}`);
  } else {
    ok(`palette centered: ${so.panel_w}x${so.panel_h} panel at (${so.panel_x},${so.panel_y})`);
  }
  // The panel pixels actually painted: its region is substantially non-black
  // (the translucent plate + widget content), unlike a bare desktop corner.
  const panelRegion = await readRegion(cw, { x: so.panel_x, y: so.panel_y, w: so.panel_w, h: so.panel_h });
  if (!panelRegion) {
    fail("could not read the palette region");
  } else if (panelRegion.nonblackPct < 40) {
    fail(`palette region looks unpainted: nonblackPct=${panelRegion.nonblackPct}`);
  } else {
    ok(`palette region painted (nonblackPct=${panelRegion.nonblackPct}, brightness=${panelRegion.brightness})`);
  }

  // 4. Fuzzy-filter to Terminal.
  await drive(cw, "query:term");
  await settle(page, 200);
  const sf = await stateOf(cw);
  if (sf.query !== "term") {
    fail(`query should be 'term', got '${sf.query}'`);
  } else {
    ok(`query is '${sf.query}'`);
  }
  if (sf.count !== 1) {
    fail(`'term' should fuzzy-filter to exactly Terminal, got count=${sf.count}`);
  } else {
    ok("'term' narrowed the list to a single app");
  }
  if (sf.sel_label !== "Terminal") {
    fail(`the sole 'term' match should be Terminal, got '${sf.sel_label}'`);
  } else {
    ok(`filtered + highlighted app is '${sf.sel_label}'`);
  }

  // 5. Commit: the palette closes AND the highlighted app launches.
  const seqBefore = sf.launch_seq;
  await drive(cw, "commit");
  await settle(page, 300);
  const sc = await stateOf(cw);
  if (sc.active !== 0) {
    fail(`commit should close the palette, still active: ${JSON.stringify(sc)}`);
  } else {
    ok("commit closed the palette");
  }
  if (sc.launched !== "terminal") {
    fail(`commit should launch the highlighted app 'terminal', got launched='${sc.launched}'`);
  } else {
    ok(`commit launched '${sc.launched}'`);
  }
  if (sc.launch_seq !== seqBefore + 1) {
    fail(`commit should increment the launch counter (${seqBefore} -> ${seqBefore + 1}), got ${sc.launch_seq}`);
  } else {
    ok(`launch counter advanced to ${sc.launch_seq}`);
  }

  // 6. Cancel path: open, query, cancel -> closed, NOTHING launched.
  const seqPreCancel = sc.launch_seq;
  await drive(cw, "open");
  await settle(page, 150);
  await drive(cw, "query:calc");
  await settle(page, 150);
  const midCancel = await stateOf(cw);
  if (midCancel.active !== 1) {
    fail("palette should be up before cancel");
  }
  await drive(cw, "cancel");
  await settle(page, 250);
  const sx = await stateOf(cw);
  if (sx.active !== 0) {
    fail(`cancel should close the palette, still active: ${JSON.stringify(sx)}`);
  } else {
    ok("cancel closed the palette");
  }
  if (sx.launch_seq !== seqPreCancel) {
    fail(`cancel must NOT launch anything: counter moved ${seqPreCancel} -> ${sx.launch_seq}`);
  } else {
    ok(`cancel launched nothing (counter still ${sx.launch_seq})`);
  }

  // Client-asset load races (a launched client's .wasm not yet served) are
  // unrelated to the launcher under test — the compositor itself booted fine.
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
