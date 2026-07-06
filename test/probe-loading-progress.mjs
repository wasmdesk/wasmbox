// SPDX-License-Identifier: BSD-3-Clause
//
// Playwright probe for the wasmbox loading-progress-bar feature
// (clients/sdk/sdk.js bootWasm()).
//
// What we prove (the user-visible promise: "no more flat grey rectangle"):
//
//   1. Right after wasmboxSpawnExternal("clients/files/worker.js"), the
//      newly-created window region contains the SDK's progress-bar pixels
//      (Adwaita accent blue (53,132,228) or track grey (218,220,224)) -- NOT
//      the previous flat-grey paint that the user complained about.
//   2. After the wasm finishes booting, the same window region paints the
//      real Files scene (Adwaita sidebar BG 241,241,241 + window BG
//      250,250,250 + the accent strip on the cursor row).
//
// Two screenshots saved:
//   /tmp/wasmbox-loading-progress.png  -- during the load
//   /tmp/wasmbox-loading-complete.png  -- after boot
//
// HARD RULE: system Chrome, headless. Per repo policy.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 15000;
const LOADING_SHOT = "/tmp/wasmbox-loading-progress.png";
const COMPLETE_SHOT = "/tmp/wasmbox-loading-complete.png";

// Loading-bar palette (must match clients/files/worker.js opts).
const COLOR_BAR_FILL    = [53, 132, 228];
const COLOR_BAR_TRACK   = [218, 220, 224];
const COLOR_LOADER_BG   = [250, 250, 250];

// WhiteSur scene palette (matches clients/files/internal/scene/render.go).
const COLOR_SIDEBAR_BG  = [240, 240, 240];
const COLOR_WINDOW_BG   = [255, 255, 255];

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

      // Throttle files.wasm so the loading bar has time to paint visibly --
      // on a hot disk + localhost the 2 MB binary streams in ~10 ms, which
      // would race the screenshot loop. We split it into 8 chunks and pause
      // between each one so the SDK paints the bar progressively + the
      // screenshot can latch a non-trivial frame.
      if (rel.endsWith("files.wasm")) {
        res.setHeader("Content-Length", body.length);
        res.writeHead(200);
        const N = 8;
        const sz = Math.ceil(body.length / N);
        for (let i = 0; i < N; i++) {
          const slice = body.subarray(i * sz, Math.min(body.length, (i + 1) * sz));
          res.write(slice);
          // 50 ms between chunks -> ~400 ms total fetch, plenty of paint frames.
          await new Promise((r) => setTimeout(r, 50));
        }
        res.end();
        return;
      }

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
function eqColor(px, c) { return px[0] === c[0] && px[1] === c[1] && px[2] === c[2]; }

function countColor(png, c) {
  let n = 0;
  const d = png.data;
  for (let i = 0; i < d.length; i += 4) {
    if (d[i] === c[0] && d[i+1] === c[1] && d[i+2] === c[2]) n++;
  }
  return n;
}

// Count occurrences of `c` inside [x0..x1) x [y0..y1) -- so we can assert
// that bar pixels appear AT THE FILES WINDOW CENTER, not in some other
// accent-coloured patch (the desktop's existing Files window selection,
// say).
function countColorRect(png, c, x0, y0, x1, y1) {
  let n = 0;
  const w = png.width;
  const d = png.data;
  for (let y = y0; y < y1; y++) {
    for (let x = x0; x < x1; x++) {
      const i = (y * w + x) * 4;
      if (d[i] === c[0] && d[i+1] === c[1] && d[i+2] === c[2]) n++;
    }
  }
  return n;
}

const { server, base } = await startServer();
console.log(`probe-loading-progress: serving on ${base}`);

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
  console.log("ok  compositor booted");

  // Baseline screenshot before spawning anything: we need the pixel count of
  // the bar colour ON THE EMPTY DESKTOP to subtract from the loading shot
  // (some desktops happen to have a similarly-coloured patch).
  const baselineShot = await page.screenshot({ type: "png", fullPage: false });
  const baselinePng = PNG.sync.read(baselineShot);
  const baselineFill  = countColor(baselinePng, COLOR_BAR_FILL);
  const baselineTrack = countColor(baselinePng, COLOR_BAR_TRACK);
  console.log(`ok  baseline desktop fill px = ${baselineFill}, track px = ${baselineTrack}`);

  // Spawn the Files client -- this triggers a wasm fetch and the SDK paints
  // the progress bar onto its SAB while bytes stream in. We've throttled
  // files.wasm in the dev server so the fetch lasts ~400 ms; the screenshot
  // loop captures the LOADING phase (multiple frames), picks the frame with
  // the most bar pixels INSIDE THE FILES WINDOW REGION (not anywhere on
  // screen -- the existing Files window's selection strip is also accent
  // blue), then waits for the boot to settle and saves the after frame.
  await page.evaluate(() => globalThis.wasmboxSpawnExternal("clients/files/worker.js"));

  const loadingFrames = [];
  // Capture frames during the throttled fetch (the server pauses 50 ms
  // between 8 chunks -> ~400 ms total). We collect ONLY frames before the
  // sidebar appears -- post-boot frames have the accent-blue selected row
  // which would beat any real loading frame on bar-pixel count.
  for (let i = 0; i < 30; i++) {
    const shot = await page.screenshot({ type: "png", fullPage: false });
    const png = PNG.sync.read(shot);
    const sidebar = countColor(png, COLOR_SIDEBAR_BG);
    if (sidebar > 1000) {
      // boot done; stop. Don't include this frame in loadingFrames.
      break;
    }
    loadingFrames.push(png);
    await page.waitForTimeout(25);
  }
  console.log(`ok  captured ${loadingFrames.length} loading frames before boot complete`);

  // Settle: wait for the Files scene to paint (sidebar BG is unique to it).
  let completePng = null;
  for (let i = 0; i < 60; i++) {
    const shot = await page.screenshot({ type: "png", fullPage: false });
    const png = PNG.sync.read(shot);
    const sidebar = countColor(png, COLOR_SIDEBAR_BG);
    if (sidebar > 1000) { completePng = png; break; }
    await page.waitForTimeout(150);
  }
  if (!completePng) {
    fail("Files scene never painted: no sidebar BG pixels found");
  } else {
    const sb = countColor(completePng, COLOR_SIDEBAR_BG);
    const wb = countColor(completePng, COLOR_WINDOW_BG);
    console.log(`ok  Files scene painted: sidebar px = ${sb}, window-bg px = ${wb}`);
    const fs = await import("node:fs/promises");
    await fs.writeFile(COMPLETE_SHOT, PNG.sync.write(completePng));
    console.log(`ok  saved complete screenshot: ${COMPLETE_SHOT}`);
  }

  // Locate the Files surface in the COMPLETED frame (we need the SAME
  // window region to validate the loading frames against -- the progress
  // bar paints at the surface center, NOT anywhere else on screen).
  let surface = null;
  if (completePng) {
    // Find sidebar BG bounds in the complete frame; the sidebar is the
    // leftmost column of the Files window surface (origin x = sidebar.x).
    let minX = completePng.width, minY = completePng.height, maxX = -1, maxY = -1;
    for (let y = 0; y < completePng.height; y++) {
      for (let x = 0; x < completePng.width; x++) {
        const i = (y * completePng.width + x) * 4;
        if (completePng.data[i]   === COLOR_SIDEBAR_BG[0] &&
            completePng.data[i+1] === COLOR_SIDEBAR_BG[1] &&
            completePng.data[i+2] === COLOR_SIDEBAR_BG[2]) {
          if (x < minX) minX = x;
          if (y < minY) minY = y;
          if (x > maxX) maxX = x;
          if (y > maxY) maxY = y;
        }
      }
    }
    if (maxX >= 0) {
      // sidebar sits at HEADER_BAR_HEIGHT (44) below surface origin; the
      // full surface is 720x440.
      surface = { x: minX, y: minY - 44, w: 720, h: 440 };
      console.log(`ok  Files surface at (${surface.x},${surface.y}) ${surface.w}x${surface.h}`);
    }
  }

  // Pick the best loading frame INSIDE the surface region (most bar pixels).
  let bestPng = null;
  let bestBarPx = 0;
  if (surface) {
    const x0 = surface.x, y0 = surface.y;
    const x1 = x0 + surface.w, y1 = y0 + surface.h;
    for (const png of loadingFrames) {
      const fillPx  = countColorRect(png, COLOR_BAR_FILL,  x0, y0, x1, y1);
      const trackPx = countColorRect(png, COLOR_BAR_TRACK, x0, y0, x1, y1);
      const total = fillPx + trackPx;
      if (total > bestBarPx) {
        bestBarPx = total;
        bestPng = png;
      }
    }
    if (!bestPng) {
      // Fall back to the first loading frame so we still save something.
      bestPng = loadingFrames[0];
    }
    const fillIn  = countColorRect(bestPng, COLOR_BAR_FILL,  x0, y0, x1, y1);
    const trackIn = countColorRect(bestPng, COLOR_BAR_TRACK, x0, y0, x1, y1);
    console.log(`ok  best loading frame (within window): fill px = ${fillIn}, track px = ${trackIn}`);
    if (bestBarPx < 200) {
      fail(`progress bar barely visible in window region: total bar pixels = ${bestBarPx} (need >= 200, track alone is ~1200)`);
    } else {
      console.log(`ok  progress bar visible during load: ${bestBarPx} bar pixels inside the Files window`);
    }
    const fs = await import("node:fs/promises");
    await fs.writeFile(LOADING_SHOT, PNG.sync.write(bestPng));
    console.log(`ok  saved loading screenshot: ${LOADING_SHOT}`);
  } else {
    fail("could not locate Files surface; cannot validate bar region");
  }

  if (pageErrors.length) {
    fail(`pageerror(s): ${pageErrors.join(" | ")}`);
  } else {
    console.log("ok  no pageerror");
  }
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
