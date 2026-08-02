// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the widgets-painted window frame decorations
// (compositor/11_frame_widgets.rb: Compositor::FRAME_WIDGETS ->
// chrome.decoration_spec -> Widgets.decoration -> Widgets.render -> RGBA ->
// wasmboxBlitRGBAOver, rendered into a per-window buffer cached by state and
// composited OVER the window body).
//
// The compositor paints to a worker OffscreenCanvas, so we read the real
// composited pixels via the __wasmboxReadRegion test hook and assert, in a
// PAINT-AGNOSTIC way (no exact colour, no exact glyph geometry — those are the
// widget painter's business and can evolve):
//
//   1. The compositor boots cleanly through the frame-widgets path — a throw in
//      chrome.decoration_spec / Widgets.decoration / render / the blit would set
//      wasmboxError.
//   2. The top window's TITLEBAR band rendered: it is markedly brighter than the
//      bare desktop (a filled chrome band, not the dark ground) and effectively
//      opaque (the decoration painted there rather than leaving a transparent
//      hole).
//   3. The top window's CLOSE box rendered a bright button face (the widget
//      painted the title-bar buttons).
//   4. The window BODY below the titlebar is still present (non-black) — proving
//      the decoration's transparent body hole let the SAB/fill body show through
//      and the z-order (decoration over body) is intact.
//
// Boot placement is deterministic in a fresh browser (empty localStorage -> the
// default cascade in 07_boot.rb): the last-spawned, focused window "about rbgo"
// (openbox default) lands at (116,116) size 220x130, so its 22px titlebar spans
// (116,94)-(336,116) and its close box sits near the band's right edge. We probe
// those known rects rather than exact pixels.

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
      const has = await w.evaluate(() => typeof globalThis.__wasmboxReadRegion === "function");
      if (has) return w;
    } catch (_) { /* not it */ }
  }
  return null;
}

async function read(worker, x, y, w, h) {
  return worker.evaluate(
    ({ x, y, w, h }) => globalThis.__wasmboxReadRegion(x, y, w, h),
    { x, y, w, h },
  );
}

// grab returns the region's actual pixels (as a decoded PNG) via the
// __wasmboxGrabRegion test hook, so we can inspect the title glyphs directly.
async function grab(worker, x, y, w, h) {
  const dataURL = await worker.evaluate(
    ({ x, y, w, h }) => globalThis.__wasmboxGrabRegion(x, y, w, h),
    { x, y, w, h },
  );
  if (!dataURL) return null;
  return PNG.sync.read(Buffer.from(dataURL.slice(dataURL.indexOf(",") + 1), "base64"));
}

// aaRampBuckets is the anti-aliased-text SIGNATURE (no hard-coded fg/bg colours,
// so it works for any Frame palette). It bins the near-neutral pixels of a
// region into 16-wide brightness buckets and counts how many INTERMEDIATE
// buckets (between the band and the ink) hold a meaningful number of pixels. A
// shaped OpenType title scan-converted to a coverage mask spreads a CONTINUUM of
// partial-coverage tones across many buckets; a 5x7 bitmap title paints only two
// tones (band + ink) and fills essentially none. Measured 1 bucket on the
// bitmap font, ~16 once Widgets.use_opentype_text takes effect.
function aaRampBuckets(png) {
  const h = {};
  for (let i = 0; i < png.data.length; i += 4) {
    const r = png.data[i], g = png.data[i + 1], b = png.data[i + 2];
    if (Math.max(r, g, b) - Math.min(r, g, b) > 28) continue; // near-neutral only
    const s = r + g + b;
    if (s < 96 || s > 720) continue; // drop the dark and bright extremes
    const k = s >> 4;
    h[k] = (h[k] || 0) + 1;
  }
  let n = 0;
  for (const k in h) if (h[k] >= 4) n++;
  return n;
}

const { server, base } = await startServer();
console.log(`probe-frame-widgets: serving on ${base}`);

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
  ok("compositor booted (frame-widgets path did not throw at boot)");
  await page.waitForTimeout(1500); // let a few rAF frames composite the frames

  const cw = await compositorWorker(page);
  if (!cw) {
    fail("could not find the compositor worker (no __wasmboxReadRegion)");
  } else {
    // Bare-desktop reference, clear of the top-left window cascade (x >= 60) and
    // the bottom-centre dock.
    const bare = await read(cw, 5, 300, 50, 50);
    // The focused top window "about rbgo": titlebar band (avoid the buttons on
    // the far right), its close box, and its body below the bar.
    const bar  = await read(cw, 140, 98, 120, 14);
    const btn  = await read(cw, 318, 98, 14, 14);
    const body = await read(cw, 140, 140, 120, 60);

    if (!bare || !bar || !btn || !body) {
      fail("a region read returned null (OffscreenCanvas not readable)");
    } else {
      // 2. Titlebar band is a filled chrome band, brighter than bare desktop and
      //    effectively opaque (not a transparent hole).
      if (bar.brightness <= bare.brightness + 12) {
        fail(`titlebar (bright=${bar.brightness}) not brighter than desktop (bright=${bare.brightness}) — decoration band did not render`);
      } else if (bar.nonblackPct < 90) {
        fail(`titlebar only ${bar.nonblackPct}% non-black — band did not fill (transparent hole?)`);
      } else {
        ok(`titlebar band rendered: bright=${bar.brightness} vs desktop ${bare.brightness}, ${bar.nonblackPct}% filled`);
      }

      // 3. Close box painted a bright button face.
      if (btn.brightness < 110) {
        fail(`close box (bright=${btn.brightness}) too dark — title-bar button did not render`);
      } else {
        ok(`close box rendered a bright button face: bright=${btn.brightness}`);
      }

      // 4. Body still present under the decoration's transparent hole.
      if (body.nonblackPct < 80) {
        fail(`window body only ${body.nonblackPct}% non-black — body did not show through the decoration hole`);
      } else {
        ok(`window body composited under the frame: ${body.nonblackPct}% non-black (z-order intact)`);
      }

      // 5. AA signature: the window TITLE is now anti-aliased, shaped OpenType
      //    (the compositor calls Widgets.use_opentype_text before the first
      //    render). Window titles were flagged as the most visible bitmap->AA
      //    win, so assert the title band carries the partial-coverage grey ramp
      //    a 5x7 bitmap could never produce.
      const title = await grab(cw, 140, 96, 130, 18);
      const titleBuckets = title ? aaRampBuckets(title) : 0;
      if (titleBuckets < 9) {
        fail(`title AA tone-buckets=${titleBuckets} (<9) — window title is not anti-aliased (bitmap font still active?)`);
      } else {
        ok(`window title is anti-aliased: ${titleBuckets} distinct partial-coverage tone buckets along the glyph edges`);
      }
    }
  }

  // Client-asset load races (a spawned client's .wasm not yet served) are
  // unrelated to the frame decorations under test — the compositor itself booted
  // fine (asserted above). Treat only NON-client-load pageerrors as fatal.
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
