// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the widgets-painted status HUD
// (compositor/09_hud_widgets.rb: Compositor::HUD_WIDGETS ->
// Widgets.label -> Widgets.render -> wasmboxBlitRGBAOver).
//
// The HUD is a translucent bottom-left text line composited over the live
// desktop, so a Playwright viewport screenshot cannot see it (the compositor
// paints to a worker OffscreenCanvas). We read the real composited pixels via
// the compositor worker's __wasmboxGrabRegion test hook and assert, in a
// PAINT-AGNOSTIC way (no exact colour / font metric):
//
//   1. The compositor boots cleanly through the widgets HUD path — a throw in
//      Widgets.render or wasmboxBlitRGBAOver would set wasmboxError / raise a
//      pageerror.
//   2. Bright text pixels are present at the HUD rect (bottom-left, over a
//      region of pure desktop to the LEFT of the centred dock), proving the
//      Label rasterised and the alpha-composited blit landed.
//   3. A control region of the same size elsewhere on the bare desktop has
//      (near) no bright pixels — so the HUD hits are the text, not the desktop.

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

// Locate the compositor worker (the one exposing the __wasmboxGrabRegion hook).
async function compositorWorker(page) {
  for (const w of page.workers()) {
    try {
      const has = await w.evaluate(() => typeof globalThis.__wasmboxGrabRegion === "function");
      if (has) return w;
    } catch (_) { /* not it */ }
  }
  return null;
}

// Grab a region off the compositor's OffscreenCanvas as a decoded PNG.
async function grab(worker, x, y, w, h) {
  const dataURL = await worker.evaluate(
    ({ x, y, w, h }) => globalThis.__wasmboxGrabRegion(x, y, w, h),
    { x, y, w, h },
  );
  if (!dataURL) return null;
  const b64 = dataURL.slice(dataURL.indexOf(",") + 1);
  return PNG.sync.read(Buffer.from(b64, "base64"));
}

// Count pixels whose R+G+B exceeds `floor` (bright HUD text vs the dark
// desktop, whose bg #11131a sums to 62).
function brightCount(png, floor) {
  let n = 0;
  for (let i = 0; i < png.data.length; i += 4) {
    if (png.data[i] + png.data[i + 1] + png.data[i + 2] > floor) n++;
  }
  return n;
}

// aaRampBuckets is the anti-aliased-text SIGNATURE. It bins the near-neutral
// pixels of a region into 16-wide brightness buckets and counts how many
// INTERMEDIATE buckets (between the dark background and the bright ink) hold a
// meaningful number of pixels. A shaped OpenType glyph scan-converted to a
// coverage mask spreads a CONTINUUM of partial-coverage tones across many
// buckets; the 5x7 bitmap paints only two tones — plus, for the translucent HUD
// panel, a single mid tone — so it fills only a few. Counting DISTINCT tone
// buckets (rather than mid-grey pixels) is what makes this robust to the HUD's
// translucent-panel compositing, which on its own produces a spike of mid-grey
// pixels a plain count would mistake for anti-aliasing. Measured: ~4 buckets on
// the bitmap font, ~14 once Widgets.use_opentype_text takes effect.
function aaRampBuckets(png) {
  const h = {};
  for (let i = 0; i < png.data.length; i += 4) {
    const r = png.data[i], g = png.data[i + 1], b = png.data[i + 2];
    if (Math.max(r, g, b) - Math.min(r, g, b) > 28) continue; // near-neutral only
    const s = r + g + b;
    if (s < 96 || s > 720) continue; // drop the dark-bg and bright-ink extremes
    const k = s >> 4;
    h[k] = (h[k] || 0) + 1;
  }
  let n = 0;
  for (const k in h) if (h[k] >= 4) n++;
  return n;
}

const { server, base } = await startServer();
console.log(`probe-hud: serving on ${base}`);

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
  ok("compositor booted (widgets HUD path did not throw at boot)");
  await page.waitForTimeout(1500); // let a few rAF frames composite the HUD

  const cw = await compositorWorker(page);
  if (!cw) fail("could not find the compositor worker (no __wasmboxGrabRegion)");

  // HUD rect: bottom-left, x in [10, ~200] which is LEFT of the centred dock,
  // so it sits over pure desktop. The Label paints its buffer at dy = H - h - 4
  // (h = 14) => the glyphs land ~ [VIEW_H-18, VIEW_H-4].
  const BRIGHT = 300; // desktop bg sums to 62; HUD ink (#e6e7ee) sums to ~690
  const hud = await grab(cw, 10, VIEW_H - 18, 190, 16);
  if (!hud) fail("could not grab the HUD region");
  const hudBright = hud ? brightCount(hud, BRIGHT) : 0;
  if (hudBright < 20) {
    fail(`HUD: only ${hudBright} bright pixels at the HUD rect — HUD text did not render`);
  } else {
    ok(`HUD: ${hudBright} bright pixels at the HUD rect — Label rendered + composited`);
  }

  // Control: a same-size patch of bare desktop, well clear of the HUD, any
  // window and the dock. Should be (near) empty of bright pixels.
  const ctrl = await grab(cw, 500, 300, 190, 16);
  const ctrlBright = ctrl ? brightCount(ctrl, BRIGHT) : 0;
  if (ctrlBright > 10) {
    console.log(`info control region has ${ctrlBright} bright pixels (expected ~0) — non-fatal`);
  } else {
    ok(`control: ${ctrlBright} bright pixels on bare desktop — HUD hits are the text`);
  }
  if (hudBright <= ctrlBright) {
    fail(`HUD (${hudBright}) not brighter than bare desktop control (${ctrlBright})`);
  } else {
    ok(`HUD region is brighter than the control (${hudBright} > ${ctrlBright})`);
  }

  // AA signature: the HUD text is now anti-aliased, shaped OpenType (the
  // compositor calls Widgets.use_opentype_text before the first render). The
  // text rect must spread its near-neutral tones across many brightness buckets
  // — the partial-coverage ramp a 5x7 bitmap (with its lone translucent-panel
  // mid tone) can never produce. Measured ~4 buckets on the bitmap, ~14 with AA.
  const hudBuckets = hud ? aaRampBuckets(hud) : 0;
  if (hudBuckets < 9) {
    fail(`HUD AA tone-buckets=${hudBuckets} (<9) — text is not anti-aliased (bitmap font still active?)`);
  } else {
    ok(`HUD text is anti-aliased: ${hudBuckets} distinct partial-coverage tone buckets`);
  }

  if (pageErrors.length) {
    fail(`pageerror(s): ${pageErrors.join(" | ")}`);
  } else {
    ok("no pageerror");
  }
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
