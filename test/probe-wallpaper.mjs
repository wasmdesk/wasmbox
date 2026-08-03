// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the selectable desktop WALLPAPER
// (compositor/05_menu.rb Wallpaper registry + 10_desktop_widgets.rb render:
// Compositor::WALLPAPER -> Widgets.wallpaper_gradient / Widgets.wallpaper,
// rendered ONCE and cached, blitted first each frame). The first Batch-3 /
// desktop-layer feature.
//
// The compositor paints to a worker OffscreenCanvas, so we read the real
// composited pixels via the __wasmboxGrabRegion test hook and drive the
// wallpaper selection through the __wasmboxSetWallpaper test hook (which posts
// the same `set_wallpaper` protocol message the root-menu Wallpaper submenu
// dispatches). We assert, in a PAINT-AGNOSTIC way:
//
//   1. The compositor boots cleanly with the wallpaper path enabled.
//   2. The bare desktop starts as the dark GRID backdrop (dominantly dark, a
//      faint mid grid, no bright chrome) — the default/fallback.
//   3. Selecting a GRADIENT preset (Aurora — a teal) CHANGES the same bare
//      region: it is no longer dominantly dark, and its mean colour shifts
//      toward the teal gradient — proving the wallpaper replaced the grid.
//   4. Selecting the bundled IMAGE preset (Photo) again changes the region —
//      proving the base64 image path renders too.
//   5. Selecting Grid restores the dark backdrop — the fallback round-trips.
//   6. Through every switch the status HUD (bottom-left) still shows bright text
//      ON TOP of the desktop — z-order (chrome over desktop) is intact.
//   7. The wallpaper is CACHED: two grabs a few frames apart, with no change in
//      between, are byte-identical (a per-frame re-render would jitter) — the
//      render-once cache holds, so there is no per-frame regression.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

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
        () => typeof globalThis.__wasmboxGrabRegion === "function" &&
              typeof globalThis.__wasmboxSetWallpaper === "function",
      );
      if (has) return w;
    } catch (_) { /* not it */ }
  }
  return null;
}

async function grab(worker, x, y, w, h) {
  const dataURL = await worker.evaluate(
    ({ x, y, w, h }) => globalThis.__wasmboxGrabRegion(x, y, w, h),
    { x, y, w, h },
  );
  if (!dataURL) return null;
  const b64 = dataURL.slice(dataURL.indexOf(",") + 1);
  return { buf: Buffer.from(b64, "base64"), png: PNG.sync.read(Buffer.from(b64, "base64")) };
}

async function setWallpaper(worker, name) {
  await worker.evaluate((n) => globalThis.__wasmboxSetWallpaper(n), name);
}

// Tally pixels by summed R+G+B into three bands: dark (the desktop fill),
// mid (grid lines / a gradient) and bright (chrome / text).
function bands(png) {
  let dark = 0, mid = 0, bright = 0;
  for (let i = 0; i < png.data.length; i += 4) {
    const s = png.data[i] + png.data[i + 1] + png.data[i + 2];
    if (s > 300) bright++;
    else if (s > 70) mid++;
    else dark++;
  }
  return { dark, mid, bright };
}

// Mean per-channel colour of a region (chrome-free desktop patch).
function meanRGB(png) {
  let r = 0, g = 0, b = 0, n = 0;
  for (let i = 0; i < png.data.length; i += 4) {
    r += png.data[i]; g += png.data[i + 1]; b += png.data[i + 2]; n++;
  }
  return { r: r / n, g: g / n, b: b / n };
}

function dist(a, b) {
  return Math.abs(a.r - b.r) + Math.abs(a.g - b.g) + Math.abs(a.b - b.b);
}

const { server, base } = await startServer();
console.log(`probe-wallpaper: serving on ${base}`);

const browser = await chromium.launch({ headless: true });
const pageErrors = [];

// A bare bottom-left patch: clear of the centred dock (bottom-centre), the
// client-window cascade (from (60,60) downward) and the HUD (bottom ~18px).
const RX = 0, RY = 560, RW = 120, RH = 180;

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
  ok("compositor booted (wallpaper path did not throw at boot)");
  await page.waitForTimeout(1500); // let a few rAF frames composite

  const cw = await compositorWorker(page);
  if (!cw) {
    fail("could not find the compositor worker (no __wasmboxGrabRegion / __wasmboxSetWallpaper)");
    throw new Error("no compositor worker");
  }

  // 2. Baseline: the default GRID backdrop — dominantly dark, a faint mid grid,
  // no bright chrome.
  const g0 = await grab(cw, RX, RY, RW, RH);
  const b0 = bands(g0.png);
  const t0 = b0.dark + b0.mid + b0.bright;
  const m0 = meanRGB(g0.png);
  if (b0.bright > t0 * 0.02) {
    fail(`baseline region has ${b0.bright}/${t0} bright pixels — chrome bled in (not bare desktop)`);
  } else if (b0.dark < t0 * 0.5) {
    fail(`baseline region is not dominantly dark (dark=${b0.dark}/${t0}) — not the grid backdrop`);
  } else {
    ok(`baseline is the dark grid: dark=${b0.dark} grid=${b0.mid} bright=${b0.bright} mean=(${m0.r.toFixed(0)},${m0.g.toFixed(0)},${m0.b.toFixed(0)})`);
  }

  // 7. Cache: two grabs a few frames apart with NO change between them must be
  // byte-identical — a per-frame re-render would differ. Proves render-once.
  await page.waitForTimeout(200);
  const g0b = await grab(cw, RX, RY, RW, RH);
  if (Buffer.compare(g0.buf, g0b.buf) === 0) {
    ok("desktop is cached: two grabs 200ms apart are byte-identical (render-once, no per-frame cost)");
  } else {
    fail("desktop background differs frame-to-frame with no wallpaper change — not cached (per-frame render)");
  }

  // 3. Select a GRADIENT preset (Aurora — teal). The bare region must change
  // and shift toward the teal gradient (no longer dominantly dark).
  await setWallpaper(cw, "Aurora");
  await page.waitForTimeout(600);
  const g1 = await grab(cw, RX, RY, RW, RH);
  const b1 = bands(g1.png);
  const t1 = b1.dark + b1.mid + b1.bright;
  const m1 = meanRGB(g1.png);
  if (dist(m0, m1) < 20) {
    fail(`Aurora gradient did not change the desktop (mean ${m0.r.toFixed(0)},${m0.g.toFixed(0)},${m0.b.toFixed(0)} -> ${m1.r.toFixed(0)},${m1.g.toFixed(0)},${m1.b.toFixed(0)})`);
  } else if (b1.dark > t1 * 0.5) {
    fail(`Aurora gradient region still dominantly dark (dark=${b1.dark}/${t1}) — gradient did not fill`);
  } else if (m1.g <= m0.g || m1.b <= m0.b) {
    fail(`Aurora gradient is not teal-shifted (g ${m0.g.toFixed(0)}->${m1.g.toFixed(0)}, b ${m0.b.toFixed(0)}->${m1.b.toFixed(0)})`);
  } else {
    ok(`Aurora gradient replaced the grid: mean=(${m1.r.toFixed(0)},${m1.g.toFixed(0)},${m1.b.toFixed(0)}) teal-shifted, dark=${b1.dark}/${t1}`);
  }

  // 4. Select the bundled IMAGE preset (Photo). Must change again from the
  // gradient (the base64 image path renders).
  await setWallpaper(cw, "Photo");
  await page.waitForTimeout(600);
  const g2 = await grab(cw, RX, RY, RW, RH);
  const m2 = meanRGB(g2.png);
  if (dist(m1, m2) < 15) {
    fail(`Photo image did not change the desktop from the gradient (mean ${m1.r.toFixed(0)},${m1.g.toFixed(0)},${m1.b.toFixed(0)} -> ${m2.r.toFixed(0)},${m2.g.toFixed(0)},${m2.b.toFixed(0)})`);
  } else if (bands(g2.png).dark > (bands(g2.png).dark + bands(g2.png).mid + bands(g2.png).bright) * 0.85) {
    fail(`Photo image region is almost all dark — image did not render`);
  } else {
    ok(`bundled Photo image rendered: mean=(${m2.r.toFixed(0)},${m2.g.toFixed(0)},${m2.b.toFixed(0)}) (base64 image path)`);
  }

  // 5. Back to Grid — the fallback round-trips to the dark backdrop.
  await setWallpaper(cw, "Grid");
  await page.waitForTimeout(600);
  const g3 = await grab(cw, RX, RY, RW, RH);
  const b3 = bands(g3.png);
  const t3 = b3.dark + b3.mid + b3.bright;
  const m3 = meanRGB(g3.png);
  if (b3.dark < t3 * 0.5) {
    fail(`Grid did not restore the dark backdrop (dark=${b3.dark}/${t3})`);
  } else if (dist(m0, m3) > 12) {
    fail(`Grid restore did not match the original backdrop (mean ${m0.r.toFixed(0)},${m0.g.toFixed(0)},${m0.b.toFixed(0)} vs ${m3.r.toFixed(0)},${m3.g.toFixed(0)},${m3.b.toFixed(0)})`);
  } else {
    ok(`Grid restored the dark backdrop: dark=${b3.dark}/${t3} — fallback round-trips`);
  }

  // 6. Z-order: the HUD (bottom-left, over the desktop) still shows bright text
  // regardless of the active wallpaper — chrome composites ON TOP.
  const hud = await grab(cw, 10, VIEW_H - 18, 190, 16);
  const hudBright = hud ? bands(hud.png).bright : 0;
  if (hudBright < 20) {
    fail(`HUD over wallpaper: only ${hudBright} bright pixels — chrome not composited on top`);
  } else {
    ok(`z-order intact: ${hudBright} bright HUD pixels on top of the desktop wallpaper`);
  }

  // Client-asset load races (a spawned client's .wasm not yet served) are
  // unrelated to the wallpaper under test — the compositor itself booted fine.
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
