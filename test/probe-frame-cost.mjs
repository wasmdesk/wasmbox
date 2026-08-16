// SPDX-License-Identifier: BSD-3-Clause
//
// Headless per-frame-cost probe for the compositor's DE overlays.
//
// Compositor#render runs every rAF (~60 Hz) and repaints the whole canvas.
// Each overlay that RE-RENDERS its widget buffer per frame costs a
// Widgets.render + base64 (Ruby) + atob + ImageData (JS) round trip; a CACHED
// overlay costs only a drawImage of a persistent buffer. The worker
// instrumentation (globalThis.__wasmboxStats) separates DECODES (real render)
// from PRESENTS (cached) per blit key and times each render(). This probe idles
// the desktop through several DE states and snapshots the counters, attributing
// the idle per-frame cost to each overlay.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 25000;
const VIEW_W = 1280;
const VIEW_H = 800;
const IDLE_MS = Number(process.env.IDLE_MS || "5000");

const MIME = {
  ".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8", ".wasm": "application/wasm",
  ".css": "text/css; charset=utf-8", ".json": "application/json; charset=utf-8",
  ".rb": "text/plain; charset=utf-8",
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
    } catch { res.writeHead(404).end("not found"); }
  });
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => resolve({ server, base: `http://127.0.0.1:${server.address().port}` }));
  });
}

function fail(msg) { console.error(`FAIL: ${msg}`); process.exitCode = 1; }
function ok(msg)   { console.log(`ok  ${msg}`); }
// Sum the per-frame DECODE rate over blit keys matching `pred` — DECODES are the
// expensive real renders (a cheap cached PRESENT does not count), so this is the
// idle cost the #88 dirty-gate must keep near zero.
function decodePerFrame(stats, pred) {
  let total = 0;
  for (const [k, v] of Object.entries(stats.decodeKeys)) if (pred(k)) total += v;
  return total / (stats.frames || 1);
}

async function compositorWorker(page) {
  for (const w of page.workers()) {
    try {
      if (await w.evaluate(() => typeof globalThis.__wasmboxFrameStats === "function")) return w;
    } catch (_) {}
  }
  return null;
}

async function measure(page, cw, label) {
  await cw.evaluate(() => globalThis.__wasmboxFrameStats(true));
  await page.waitForTimeout(IDLE_MS);
  const s = await cw.evaluate(() => globalThis.__wasmboxFrameStats(false));
  const fps = (s.frames / (IDLE_MS / 1000)).toFixed(1);
  console.log(`\n[${label}] ${s.frames} frames (${fps} fps)  avg render ${s.avgFrameMs.toFixed(3)} ms  max ${s.maxFrameMs.toFixed(2)} ms`);
  console.log(`   SAB   copy ${s.sabCopiesPerFrame.toFixed(3)} / present ${s.sabPresentsPerFrame.toFixed(3)}   ` +
              `opaque dec ${s.rgbaDecodesPerFrame.toFixed(3)} / pres ${s.rgbaPresentsPerFrame.toFixed(3)}   ` +
              `transl dec ${s.overDecodesPerFrame.toFixed(3)} / pres ${s.overPresentsPerFrame.toFixed(3)}`);
  const keys = Object.entries(s.decodeKeys).sort((a, b) => b[1] - a[1]);
  console.log(`   decode/frame by key: ${keys.length ? keys.map(([k, v]) => `${k}=${(v / (s.frames || 1)).toFixed(3)}`).join("  ") : "(none)"}`);
  return s;
}

const { server, base } = await startServer();
console.log(`probe-frame-cost: serving on ${base}`);
const browser = await chromium.launch({ headless: true });
const workerErrors = [];

try {
  const page = await browser.newPage({ viewport: { width: VIEW_W, height: VIEW_H } });
  page.on("pageerror", (e) => workerErrors.push("page: " + String(e)));
  page.on("worker", (w) => w.on("close", () => {}));
  page.on("console", (m) => { if (m.type() === "error") workerErrors.push("console: " + m.text()); });

  await page.goto(`${base}/index.html`, { waitUntil: "load" });
  await page.waitForFunction(() => {
    if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
    return globalThis.wasmboxReady === true;
  }, { timeout: BOOT_TIMEOUT_MS });
  console.log("booted");
  await page.waitForTimeout(1500);
  let cw = await compositorWorker(page);
  if (!cw) throw new Error("compositor worker not found");

  await measure(page, cw, "A: bare desktop (dock only)");

  // B: one external window.
  await cw.evaluate(() => globalThis.wasmboxSpawnExternal("clients/hello/worker.js"));
  await page.waitForTimeout(2500);
  await measure(page, cw, "B: + hello window");

  // C: a live clock applet (re-renders on its own 1 Hz cadence).
  await cw.evaluate(() => globalThis.__wasmboxAppletToggle && globalThis.__wasmboxAppletToggle("clock"));
  await page.waitForTimeout(1500);
  await measure(page, cw, "C: + clock applet");

  // D: a tray item.
  await cw.evaluate(() => globalThis.__wasmboxTrayAdd && globalThis.__wasmboxTrayAdd({ id: "probe", title: "P" }));
  await page.waitForTimeout(1500);
  await measure(page, cw, "D: + tray item");

  // E: a Calendar applet (v0.86 bound widget). Placed but not navigated, it must
  // NOT re-DECODE per frame — its dirty signature only moves on a month change,
  // so the #88 idle gate keeps its render cost ~0 (only cheap PRESENTs).
  await cw.evaluate(() => globalThis.__wasmboxAppletPlace && globalThis.__wasmboxAppletPlace("calendar", 700, 420));
  await page.waitForTimeout(800);
  const sE = await measure(page, cw, "E: + calendar applet (idle)");
  const calDec = decodePerFrame(sE, (k) => k === "applet#calendar");
  if (calDec > 0.05) {
    fail(`calendar applet re-DECODES ${calDec.toFixed(3)}/frame when idle (must be ~0; #88 dirty-gate)`);
  } else {
    ok(`calendar applet idle: ${calDec.toFixed(4)} decode/frame (~0)`);
  }

  // F: a toast shown then EXPIRED. After it auto-dismisses, no notif buffer may
  // re-DECODE per frame (the expiry mark_dirty paints the clearing frame once,
  // then the stack is empty and idle).
  await cw.evaluate(() => globalThis.__wasmboxPostNotification &&
    globalThis.__wasmboxPostNotification({ title: "cost", body: "probe", timeout: 2 }));
  await page.waitForTimeout(3500); // outlive the 2s toast so the measure window is post-expiry
  const sF = await measure(page, cw, "F: after toast shown+expired (idle)");
  const notifDec = decodePerFrame(sF, (k) => k.startsWith("notif#"));
  if (notifDec > 0.05) {
    fail(`an expired toast still re-DECODES ${notifDec.toFixed(3)}/frame (must be ~0)`);
  } else {
    ok(`toast expired -> notif idle: ${notifDec.toFixed(4)} decode/frame (~0)`);
  }

  if (workerErrors.length) { console.log("\nERRORS:"); for (const e of workerErrors.slice(0, 20)) console.log("  " + e); fail(`${workerErrors.length} worker/page error(s)`); }
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
