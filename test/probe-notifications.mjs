// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the desktop NOTIFICATION toasts
// (compositor/05_notifications.rb model + compositor/12_notifications.rb render:
// Compositor::NOTIFICATIONS -> Widgets.toast -> Widgets.render ->
// wasmboxBlitRGBAOver, painted top-right, auto-expiring, stacked).
//
// Toasts are a translucent overlay composited over the live desktop on the
// compositor's worker OffscreenCanvas, so a Playwright viewport screenshot
// cannot see them (same constraint as the HUD probe). We read the real
// composited pixels via the compositor worker's __wasmboxGrabRegion hook and
// trigger toasts deterministically via the __wasmboxPostNotification test hook
// (which injects a real `notify` wire message — full decode -> handle -> route
// path — so this exercises the actual wiring, not a private shortcut).
//
// Asserted, paint-agnostic (no exact colour / font metric):
//   1. The compositor boots cleanly through the notifications path (a throw in
//      Widgets.toast / render / the blit would set wasmboxError / raise).
//   2. The top-right toast rect is EMPTY before any post.
//   3. After posting one toast, the top-right rect fills with bright pixels (an
//      "info" pill is the theme accent, far brighter than the dark desktop) —
//      the toast rendered + composited at the top-right.
//   4. After posting a second toast, the row BELOW the first also fills — the
//      stack grows downward — while the first row stays filled.
//   5. After their timeout elapses, BOTH rows return to bare desktop — the
//      toasts auto-dismissed.
//   6. A top-left control region stays empty throughout (the hits are the
//      toasts, top-right, not desktop noise).

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

// Toast geometry — MUST match NotificationStack's TOAST_W / TOAST_H / TOAST_GAP
// / TOAST_MARGIN (compositor/05_notifications.rb). Row 0 is the top-right pill;
// row 1 stacks TOAST_H + TOAST_GAP below it.
const TW = 300;
const TH = 44;
const GAP = 10;
const MARGIN = 16;
const ROW_X = VIEW_W - TW - MARGIN;
const ROW0_Y = MARGIN;
const ROW1_Y = MARGIN + TH + GAP;

// The auto-dismiss budget the probe posts with (seconds). Chosen long enough to
// assert the two-toast stack before the first expires, short enough to keep the
// probe quick.
const TIMEOUT_S = 3;

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

// Locate the compositor worker (the one exposing the test hooks).
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

// Count pixels whose R+G+B exceeds `floor`. The dark desktop bg (#11131a) sums
// to 62; an "info" toast pill is the theme accent (#4F9DF2, sum ~478), so a
// rendered pill lights up most of its rect.
function brightCount(png, floor) {
  let n = 0;
  for (let i = 0; i < png.data.length; i += 4) {
    if (png.data[i] + png.data[i + 1] + png.data[i + 2] > floor) n++;
  }
  return n;
}

// Post a notify via the test hook (a real `notify` wire message injected on the
// compositor's Ruby bus).
async function postNotification(worker, opts) {
  await worker.evaluate((o) => globalThis.__wasmboxPostNotification(o), opts);
}

const BRIGHT = 300;   // pill accent sums ~478; desktop bg + grid stay well under
const PRESENT = 3000; // a rendered pill lights up most of its 300x44 = 13200 px
const EMPTY = 200;    // bare desktop (+ faint grid) in the rect

const { server, base } = await startServer();
console.log(`probe-notifications: serving on ${base}`);

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
  ok("compositor booted (notifications path did not throw at boot)");
  await page.waitForTimeout(1000); // let a few rAF frames settle

  const cw = await compositorWorker(page);
  if (!cw) { fail("could not find the compositor worker (no __wasmboxGrabRegion)"); throw new Error("no worker"); }

  const hasHook = await cw.evaluate(() => typeof globalThis.__wasmboxPostNotification === "function");
  if (!hasHook) { fail("__wasmboxPostNotification test hook is missing"); throw new Error("no hook"); }
  ok("notification test hook present");

  // (2) Baseline: the top-right toast rect is empty before any post.
  const base0 = await grab(cw, ROW_X, ROW0_Y, TW, TH);
  const base0Bright = base0 ? brightCount(base0, BRIGHT) : -1;
  if (base0Bright < 0) { fail("could not grab the toast region"); throw new Error("grab"); }
  if (base0Bright > EMPTY) {
    fail(`baseline: top-right rect already has ${base0Bright} bright pixels (expected ~0)`);
  } else {
    ok(`baseline: top-right toast rect empty (${base0Bright} bright pixels)`);
  }

  // (3) Post one toast; it should fill the top-right rect.
  await postNotification(cw, { title: "Probe A", body: "first toast", kind: "info", timeout: TIMEOUT_S });
  await page.waitForTimeout(500);
  const a0 = await grab(cw, ROW_X, ROW0_Y, TW, TH);
  const a0Bright = a0 ? brightCount(a0, BRIGHT) : 0;
  if (a0Bright < PRESENT) {
    fail(`toast A: only ${a0Bright} bright pixels at the top-right rect — toast did not render`);
  } else {
    ok(`toast A rendered top-right (${a0Bright} bright pixels)`);
  }

  // (4) Post a second toast; it should fill the row BELOW while row 0 stays lit.
  await postNotification(cw, { title: "Probe B", body: "second toast", kind: "info", timeout: TIMEOUT_S });
  await page.waitForTimeout(500);
  const b1 = await grab(cw, ROW_X, ROW1_Y, TW, TH);
  const b1Bright = b1 ? brightCount(b1, BRIGHT) : 0;
  const stillA0 = await grab(cw, ROW_X, ROW0_Y, TW, TH);
  const stillA0Bright = stillA0 ? brightCount(stillA0, BRIGHT) : 0;
  if (b1Bright < PRESENT) {
    fail(`toast B: only ${b1Bright} bright pixels in row 1 — second toast did not stack below`);
  } else {
    ok(`toast B stacked below A (row 1: ${b1Bright} bright pixels)`);
  }
  if (stillA0Bright < PRESENT) {
    fail(`toast A vanished from row 0 while B was up (${stillA0Bright} bright pixels)`);
  } else {
    ok(`toast A still present in row 0 while B is up (${stillA0Bright} bright pixels)`);
  }

  // (6) Control: a same-size rect on the bare desktop, well clear of the
  // top-left window cascade, the top-right toasts and the bottom-center dock.
  // It must stay empty — proving the bright hits are the toasts, top-right.
  const ctrl = await grab(cw, 40, 520, TW, TH);
  const ctrlBright = ctrl ? brightCount(ctrl, BRIGHT) : 0;
  if (ctrlBright > EMPTY) {
    fail(`control: bare-desktop rect has ${ctrlBright} bright pixels (expected ~0) — toasts are not top-right`);
  } else {
    ok(`control: bare-desktop rect empty (${ctrlBright} bright pixels) — toasts are top-right`);
  }

  // (5) Wait out the timeout; both rows should return to bare desktop.
  await page.waitForTimeout(TIMEOUT_S * 1000 + 1200);
  const e0 = await grab(cw, ROW_X, ROW0_Y, TW, TH);
  const e1 = await grab(cw, ROW_X, ROW1_Y, TW, TH);
  const e0Bright = e0 ? brightCount(e0, BRIGHT) : 999999;
  const e1Bright = e1 ? brightCount(e1, BRIGHT) : 999999;
  if (e0Bright > EMPTY || e1Bright > EMPTY) {
    fail(`auto-dismiss: rows not clear after timeout (row0=${e0Bright}, row1=${e1Bright})`);
  } else {
    ok(`toasts auto-dismissed after their timeout (row0=${e0Bright}, row1=${e1Bright})`);
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
