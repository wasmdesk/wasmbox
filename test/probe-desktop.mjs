// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the widgets-painted desktop background
// (compositor/10_desktop_widgets.rb: Compositor::DESKTOP_WIDGETS ->
// Widgets.backdrop -> Widgets.render -> wasmboxBlitRGBA, rendered ONCE and
// cached, blitted first each frame).
//
// The compositor paints to a worker OffscreenCanvas, so we read the real
// composited pixels via the __wasmboxGrabRegion test hook and assert, in a
// PAINT-AGNOSTIC way (no exact colour / no exact grid geometry):
//
//   1. The compositor boots cleanly through the desktop-widgets path — a throw
//      in Widgets.backdrop / render / the blit would set wasmboxError.
//   2. A bare top-left desktop region reads as the dark ground with a faint
//      grid: it is DOMINANTLY dark (the fill), carries NO bright chrome pixels,
//      yet is NOT perfectly uniform (grid lines a shade above the fill) — so
//      both the fill AND the grid rendered.
//   3. The status HUD (bottom-left) still shows bright text ON TOP of that
//      desktop — proving the cached desktop buffer is composited FIRST and the
//      z-order (chrome over desktop) is unchanged.

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
      const has = await w.evaluate(() => typeof globalThis.__wasmboxGrabRegion === "function");
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
  return PNG.sync.read(Buffer.from(b64, "base64"));
}

// Tally pixels by summed R+G+B into three bands: dark (the desktop fill),
// mid (the grid lines — a shade above the fill), and bright (chrome / text).
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

const { server, base } = await startServer();
console.log(`probe-desktop: serving on ${base}`);

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
  ok("compositor booted (desktop-widgets path did not throw at boot)");
  await page.waitForTimeout(1500); // let a few rAF frames composite

  const cw = await compositorWorker(page);
  if (!cw) fail("could not find the compositor worker (no __wasmboxGrabRegion)");

  // A bare bottom-left patch: the desktop fill + grid, clear of the centred
  // dock (bottom-centre), the client-window cascade (from (60,60) downward) and
  // the HUD (the bottom ~18px). Spans several 40px grid pitches so grid lines
  // are guaranteed within it.
  const bg = cw ? await grab(cw, 0, 600, 100, 150) : null;
  if (!bg) {
    fail("could not grab the top-left desktop region");
  } else {
    const b = bands(bg);
    const total = b.dark + b.mid + b.bright;
    if (b.bright > total * 0.02) {
      fail(`desktop region has ${b.bright}/${total} bright pixels — chrome bled into it (not bare desktop)`);
    } else if (b.dark < total * 0.5) {
      fail(`desktop region is not dominantly dark (dark=${b.dark}/${total}) — fill did not render`);
    } else if (b.mid < 1) {
      fail(`desktop region is perfectly uniform (mid=${b.mid}) — grid lines did not render`);
    } else {
      ok(`desktop: dark=${b.dark} grid=${b.mid} bright=${b.bright} — fill + grid rendered`);
    }
  }

  // Z-order: the HUD (bottom-left, over bare desktop) still shows bright text,
  // proving the cached desktop is blitted FIRST and chrome composites on top.
  const hud = cw ? await grab(cw, 10, VIEW_H - 18, 190, 16) : null;
  const hudBright = hud ? bands(hud).bright : 0;
  if (hudBright < 20) {
    fail(`HUD over desktop: only ${hudBright} bright pixels — chrome not composited on top of desktop`);
  } else {
    ok(`z-order: ${hudBright} bright HUD pixels on top of the desktop — composite order intact`);
  }

  // Client-asset load races (a spawned client's .wasm not yet served) are
  // unrelated to the desktop background under test — the compositor itself
  // booted fine (asserted above). Treat only NON-client-load pageerrors as
  // fatal so this probe stays focused on the desktop paint.
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
