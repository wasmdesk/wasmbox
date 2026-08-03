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

  if (workerErrors.length) { console.log("\nERRORS:"); for (const e of workerErrors.slice(0, 20)) console.log("  " + e); }
} finally {
  await browser.close();
  server.close();
}
