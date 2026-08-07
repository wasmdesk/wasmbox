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
//   12. PER-BUTTON click routing: on a two-action pill, a real mousedown at the
//       SECOND button's centre (located from the rendered divider columns, driven
//       through the compositor's genuine on_mousedown path via a relayed
//       dom_event) routes to the SECOND action, and a first-button click to the
//       FIRST — proving the compositor fires the exact button hit, not always the
//       first. The compositor reports the routed action through an optional
//       observer this probe installs (globalThis.wasmboxPublishNotifyAction).
//   13. freedesktop.org NOTIFICATION SEMANTICS: a post using the spec field set
//       (summary/body/urgency/expire_timeout/actions) maps exactly like
//       go-freedesktop/notifications/toast.ToToast. A CRITICAL-urgency post paints
//       an ERROR (brick-red) pill, an expire_timeout of 0 makes it STICKY (it
//       outlasts the 5s server default), its two spec actions route per-button
//       (2nd button -> 2nd action, 1st -> 1st), and a NORMAL-urgency post with a
//       finite expire_timeout renders as an info pill that AUTO-DISMISSES.

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
const TH = 64; // MUST match NotificationStack::TOAST_H (bumped for icon/2-line/buttons)
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

// --- structural helpers for the v0.86 rich-toast assertions ------------------
// The pill body is a solid Kind colour; the icon, the two text lines and the
// action-button dividers all DIFFER from that fill. So we sample the fill once
// (a content-free corner) and count "ink" as any pixel far from it — which lets
// us measure the text's vertical extent, the full-height button dividers, and
// the leading icon precisely, without knowing the exact theme colour.

const px = (png, x, y) => {
  const i = (y * png.width + x) * 4;
  return [png.data[i], png.data[i + 1], png.data[i + 2]];
};
const dist = (a, b) => Math.abs(a[0] - b[0]) + Math.abs(a[1] - b[1]) + Math.abs(a[2] - b[2]);
const isInk = (png, x, y, bg, tol) => dist(px(png, x, y), bg) > tol;

// A 16x16 solid-red RGBA image, base64-encoded, for the leading image icon.
function redIconB64() {
  const buf = Buffer.alloc(16 * 16 * 4);
  for (let i = 0; i < buf.length; i += 4) { buf[i] = 220; buf[i + 1] = 20; buf[i + 2] = 20; buf[i + 3] = 255; }
  return buf.toString("base64");
}
// Count strongly-red pixels (the image icon) in a region — the accent-blue pill
// never matches, so this isolates the icon.
function redCount(png) {
  let n = 0;
  for (let i = 0; i < png.data.length; i += 4) {
    if (png.data[i] > 150 && png.data[i + 1] < 90 && png.data[i + 2] < 90) n++;
  }
  return n;
}
// Vertical extent (last inked row - first inked row + 1) of TEXT ink over the
// column band [x0, x1), i.e. how tall the message block is. One line spans ~one
// cap height; two lines span ~twice that plus the inter-line gap. Full-height
// columns (an action-button divider that happens to fall in the band) are
// skipped so a divider can never inflate the extent to the whole pill height.
function textVExtent(png, bg, tol, x0, x1) {
  const need = Math.floor(png.height * 0.75);
  const textCol = [];
  for (let x = x0; x < x1; x++) {
    let rows = 0;
    for (let y = 0; y < png.height; y++) if (isInk(png, x, y, bg, tol)) rows++;
    textCol[x] = rows > 0 && rows < need; // inked but not a full-height divider
  }
  // Skip the pill's 1px top/bottom border rows (a horizontal line inked across
  // every column) so the border never pins the extent to the full pill height.
  let first = -1, last = -1;
  for (let y = 3; y < png.height - 3; y++) {
    let inked = false;
    for (let x = x0; x < x1; x++) { if (textCol[x] && isInk(png, x, y, bg, tol)) { inked = true; break; } }
    if (inked) { if (first < 0) first = y; last = y; }
  }
  return first < 0 ? 0 : last - first + 1;
}
// Count full-height "divider" columns (a 1px column inked over ~the whole pill
// height) among the interior columns — the toolkit draws exactly one such
// divider before each action button, so this counts the buttons. Text/icon ink
// never spans the full height, and the pill's own 1px border is excluded by the
// interior margin.
function dividerRuns(png, bg, tol) {
  const need = Math.floor(png.height * 0.75);
  let runs = 0, inRun = false;
  for (let x = 3; x < png.width - 3; x++) {
    let rows = 0;
    for (let y = 0; y < png.height; y++) if (isInk(png, x, y, bg, tol)) rows++;
    const full = rows >= need;
    if (full && !inRun) runs++;
    inRun = full;
  }
  return runs;
}
// The pill-local X of each full-height "divider" column — the LEFT edge of each
// action button, read straight off the rendered pill. One entry per button,
// left-to-right, so a probe can click a specific button precisely: button i
// spans [cols[i], cols[i+1]) (the last runs to the pill's right edge).
function dividerCols(png, bg, tol) {
  const need = Math.floor(png.height * 0.75);
  const cols = [];
  let inRun = false;
  for (let x = 3; x < png.width - 3; x++) {
    let rows = 0;
    for (let y = 0; y < png.height; y++) if (isInk(png, x, y, bg, tol)) rows++;
    const full = rows >= need;
    if (full && !inRun) cols.push(x);
    inRun = full;
  }
  return cols;
}

// Post a notify via the test hook (a real `notify` wire message injected on the
// compositor's Ruby bus).
async function postNotification(worker, opts) {
  await worker.evaluate((o) => globalThis.__wasmboxPostNotification(o), opts);
}

const BRIGHT = 300;   // pill accent sums ~478; desktop bg + grid stay well under
const PRESENT = 3000; // a rendered pill lights up most of its 300x44 = 13200 px
const EMPTY = 200;    // bare desktop (+ faint grid) in the rect
const DEFAULT_MS = 5000; // NotificationStack::DEFAULT_TIMEOUT_MS (the server default)

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

  // === v0.86 rich toasts: leading icon, multi-line body, multi-action buttons ==
  // Post a CONTROL single-line, icon-less, action-less pill (row 0) alongside a
  // RICH pill (row 1) carrying a red image icon, a title-over-body two-line body
  // and two action buttons. Sample the pill fill from the control's content-free
  // right edge (same "info" kind, so the same colour), then assert structurally.
  const RICH_TIMEOUT_S = 4;
  await postNotification(cw, { title: "Solo", kind: "info", timeout: RICH_TIMEOUT_S });
  await postNotification(cw, {
    title: "Deploy ready", body: "3 files changed", kind: "info",
    icon: redIconB64(), icon_w: 16, icon_h: 16,
    actions: "Open|op;Later|la", timeout: RICH_TIMEOUT_S,
  });
  await page.waitForTimeout(600);

  const ctrlPng = await grab(cw, ROW_X, ROW0_Y, TW, TH); // control, row 0
  const richPng = await grab(cw, ROW_X, ROW1_Y, TW, TH); // rich, row 1
  if (!ctrlPng || !richPng) { fail("could not grab the rich-toast rows"); throw new Error("grab rich"); }
  // Pill fill reference: control's far right edge, vertically centred (no icon,
  // no buttons, short left-aligned label -> bare pill there).
  const BG = px(ctrlPng, TW - 8, Math.floor(TH / 2));
  const TOL = 90;

  // (7) ICON: strong red pixels in the rich pill's leading icon slot (a ~gh
  // square at the left), and NONE in the control pill's matching slot.
  const ICON = { x0: 4, x1: 26 };
  const richIcon = new PNG({ width: ICON.x1 - ICON.x0, height: TH });
  const ctrlIcon = new PNG({ width: ICON.x1 - ICON.x0, height: TH });
  for (let y = 0; y < TH; y++) for (let x = ICON.x0; x < ICON.x1; x++) {
    for (const [src, dst] of [[richPng, richIcon], [ctrlPng, ctrlIcon]]) {
      const si = (y * src.width + x) * 4, di = (y * dst.width + (x - ICON.x0)) * 4;
      dst.data[di] = src.data[si]; dst.data[di + 1] = src.data[si + 1];
      dst.data[di + 2] = src.data[si + 2]; dst.data[di + 3] = 255;
    }
  }
  const richRed = redCount(richIcon), ctrlRed = redCount(ctrlIcon);
  if (richRed < 60) {
    fail(`icon: only ${richRed} red icon pixels in the rich pill's left slot (expected the 16x16 image icon)`);
  } else if (ctrlRed > 10) {
    fail(`icon: control pill has ${ctrlRed} red pixels in its left slot (should have no icon)`);
  } else {
    ok(`leading image icon present (rich ${richRed} red px, control ${ctrlRed})`);
  }

  // (8) MULTI-LINE: the rich pill's text block (right of the icon, left of the
  // buttons) spans markedly taller than the control's single line.
  const TX0 = 30, TX1 = TW - 12; // right of the icon slot; dividers auto-skipped
  const ctrlExtent = textVExtent(ctrlPng, BG, TOL, 12, TW - 12);
  const richExtent = textVExtent(richPng, BG, TOL, TX0, TX1);
  if (richExtent < Math.floor(ctrlExtent * 1.6)) {
    fail(`multi-line: rich text extent ${richExtent}px is not clearly taller than the single line ${ctrlExtent}px`);
  } else {
    ok(`multi-line body: two rows span ${richExtent}px vs one row ${ctrlExtent}px`);
  }

  // (9) MULTI-ACTION: exactly two full-height button dividers in the rich pill,
  // and none in the action-less control pill.
  const richDiv = dividerRuns(richPng, BG, TOL);
  const ctrlDiv = dividerRuns(ctrlPng, px(ctrlPng, 4, 3), TOL);
  if (richDiv !== 2) {
    fail(`multi-action: expected 2 button dividers in the rich pill, found ${richDiv}`);
  } else if (ctrlDiv !== 0) {
    fail(`multi-action: action-less control pill has ${ctrlDiv} dividers (expected 0)`);
  } else {
    ok(`two action buttons rendered (rich ${richDiv} dividers, control ${ctrlDiv})`);
  }

  // (10) BOUNDS: the rich pill paints nothing below its own bottom edge (no
  // bleed past the toast rectangle).
  const belowPng = await grab(cw, ROW_X, ROW1_Y + TH + 1, TW, GAP - 2);
  const belowBright = belowPng ? brightCount(belowPng, BRIGHT) : 999999;
  if (belowBright > EMPTY) {
    fail(`bounds: ${belowBright} bright pixels just below the rich pill — it bled past its rect`);
  } else {
    ok(`rich pill stays within its bounds (${belowBright} bright px just below)`);
  }

  // (11) AUTO-DISMISS still fires for the rich pills.
  await page.waitForTimeout(RICH_TIMEOUT_S * 1000 + 1200);
  const r0 = await grab(cw, ROW_X, ROW0_Y, TW, TH);
  const r1 = await grab(cw, ROW_X, ROW1_Y, TW, TH);
  const r0b = r0 ? brightCount(r0, BRIGHT) : 999999;
  const r1b = r1 ? brightCount(r1, BRIGHT) : 999999;
  if (r0b > EMPTY || r1b > EMPTY) {
    fail(`rich auto-dismiss: rows not clear after timeout (row0=${r0b}, row1=${r1b})`);
  } else {
    ok(`rich toasts auto-dismissed after their timeout (row0=${r0b}, row1=${r1b})`);
  }

  // (12) PER-BUTTON CLICK ROUTING: a click on a two-action pill must fire the
  // button the pointer actually hit — the SECOND action for a second-button
  // click, the FIRST for a first-button click — not always the first. We read
  // each button's horizontal span from the RENDERED divider columns (pixel-
  // precise, off the real pill), inject a real mousedown at a chosen button's
  // centre through the compositor's genuine on_mousedown path, and read back the
  // action the compositor routed.
  //
  // No compositor.worker.js test hook is used (that file is owned by a concurrent
  // PR): we install an OPTIONAL observer the compositor already probes for
  // (publish_notify_action calls globalThis.wasmboxPublishNotifyAction only when
  // it is defined), and we relay the click as a `dom_event` message exactly like
  // the main thread's pointer relay — driving the real routing, not a shortcut.
  await cw.evaluate(() => {
    globalThis.__notifRouted = null;
    globalThis.wasmboxPublishNotifyAction = (action, id) => {
      globalThis.__notifRouted = { action: String(action), notification_id: id | 0 };
    };
  });

  // Inject a real mousedown at canvas-relative (x, y) via the SAME dom_event
  // message the page relays for genuine pointer input, so it flows through the
  // compositor's actual on_mousedown -> notify_at -> dismiss_notification routing.
  async function canvasMouseDown(x, y) {
    await cw.evaluate(({ x, y }) => {
      self.dispatchEvent(new MessageEvent("message", {
        data: { type: "dom_event", target: "canvas", kind: "mousedown", offsetX: x, offsetY: y, button: 0 },
      }));
    }, { x, y });
    await page.waitForTimeout(200);
    return cw.evaluate(() => globalThis.__notifRouted);
  }

  // Post a fresh, STICKY two-action toast (timeout 0 = never auto-expires, so it
  // waits for the click), read its two button spans off the rendered pill, and
  // return them. Distinct action ids let us prove WHICH button fired.
  async function postTwoActionToast() {
    await cw.evaluate(() => { globalThis.__notifRouted = null; });
    await postNotification(cw, {
      title: "Deleted file", kind: "info",
      actions: "Undo|act_undo;Dismiss|act_dismiss", timeout: 0,
    });
    await page.waitForTimeout(500);
    const png = await grab(cw, ROW_X, ROW0_Y, TW, TH);
    if (!png) { fail("routing: could not grab the two-action toast"); throw new Error("grab route"); }
    const bg = px(png, TW - 8, 3);           // pill fill near the top-right corner
    const cols = dividerCols(png, bg, TOL);  // left edge of each of the 2 buttons
    return { png, cols };
  }

  // Click the centre of button `idx` (0-based) of the toast at row 0, given its
  // divider columns, and return the routed { action, ... }.
  async function clickButtonCentre(cols, idx) {
    const left = cols[idx];
    const right = idx + 1 < cols.length ? cols[idx + 1] : TW - 1; // last runs to the edge
    const localX = Math.floor((left + right) / 2);
    const localY = Math.floor(TH / 2);
    return canvasMouseDown(ROW_X + localX, ROW0_Y + localY);
  }

  // FIRST button -> the FIRST action.
  const t1 = await postTwoActionToast();
  if (t1.cols.length !== 2) {
    fail(`routing: expected 2 button dividers, found ${t1.cols.length}`);
  } else {
    const fired1 = await clickButtonCentre(t1.cols, 0);
    if (!fired1 || fired1.action !== "act_undo") {
      fail(`routing: first-button click fired ${JSON.stringify(fired1)}, want action "act_undo"`);
    } else {
      ok(`per-button routing: first-button click fired the FIRST action (${fired1.action})`);
    }
  }

  // SECOND button -> the SECOND action (the headline: NOT the first).
  const t2 = await postTwoActionToast();
  if (t2.cols.length !== 2) {
    fail(`routing: expected 2 button dividers on the second toast, found ${t2.cols.length}`);
  } else {
    const fired2 = await clickButtonCentre(t2.cols, 1);
    if (!fired2 || fired2.action !== "act_dismiss") {
      fail(`routing: second-button click fired ${JSON.stringify(fired2)}, want action "act_dismiss" (not the first "act_undo")`);
    } else {
      ok(`per-button routing: second-button click fired the SECOND action (${fired2.action}), not the first`);
    }
    // The dismissing click also cleared the toast.
    await page.waitForTimeout(200);
    const goneP = await grab(cw, ROW_X, ROW0_Y, TW, TH);
    const goneB = goneP ? brightCount(goneP, BRIGHT) : 999999;
    if (goneB > EMPTY) {
      fail(`routing: toast still present after the dismissing click (${goneB} bright px)`);
    } else {
      ok(`per-button routing: the click also dismissed the toast (${goneB} bright px)`);
    }
  }

  // === (13) freedesktop Notification semantics =================================
  // A post using the freedesktop spec field set is mapped by
  // Notification.map_freedesktop, mirroring toast.ToToast: urgency 2 -> an ERROR
  // (brick-red 0xC0,0x30,0x30) pill, expire_timeout 0 -> STICKY, spec actions ->
  // per-button routing. Posted through the SAME __wasmboxPostNotification ->
  // decode -> handle path as a native notify, so it drives the real mapping.
  // The brick-red fill lights up redCount (R>150,G<90,B<90) while the blue info
  // accent never does, so redCount cleanly proves the error kind.

  // Post a freedesktop CRITICAL toast (summary+body+urgency+expire_timeout=0+
  // actions) and read its row-0 pixels + per-button divider columns.
  async function postFdoCritical(actions) {
    await cw.evaluate(() => { globalThis.__notifRouted = null; });
    await postNotification(cw, {
      summary: "Deploy failed", body: "2 errors\ncheck the logs",
      urgency: 2, expire_timeout: 0, actions,
    });
    await page.waitForTimeout(500);
    const png = await grab(cw, ROW_X, ROW0_Y, TW, TH);
    if (!png) { fail("fdo: could not grab the critical toast"); throw new Error("grab fdo"); }
    const bg = px(png, TW - 8, 3);           // pill fill near the top-right corner
    const cols = dividerCols(png, bg, TOL);  // left edge of each button
    return { png, cols };
  }

  const fdo = await postFdoCritical("Retry|fdo_retry;Dismiss|fdo_dismiss");

  // (13a) urgency 2 -> ERROR kind: a brick-red fill (redCount high), and clearly
  // NOT the blue info accent (whose bright-pixel count would dominate instead).
  const errRed = redCount(fdo.png);
  const errBright = brightCount(fdo.png, BRIGHT);
  if (errRed < PRESENT) {
    fail(`fdo error kind: only ${errRed} brick-red px — a Critical-urgency post did not paint an error pill`);
  } else if (errRed <= errBright) {
    fail(`fdo error kind: red ${errRed} <= accent-bright ${errBright} — looks like an info pill, not error`);
  } else {
    ok(`Critical urgency -> error pill (${errRed} brick-red px vs ${errBright} accent-bright px)`);
  }

  // (13b) the two spec actions render as two per-button dividers.
  if (fdo.cols.length !== 2) {
    fail(`fdo actions: expected 2 button dividers on the critical toast, found ${fdo.cols.length}`);
  } else {
    ok(`freedesktop actions rendered two buttons (${fdo.cols.length} dividers)`);
  }

  // (13c) expire_timeout 0 -> STICKY: it must outlast the 5s server default (a
  // toast using the default would have auto-dismissed by now).
  await page.waitForTimeout(DEFAULT_MS + 1000);
  const stickPng = await grab(cw, ROW_X, ROW0_Y, TW, TH);
  const stickRed = stickPng ? redCount(stickPng) : 0;
  if (stickRed < PRESENT) {
    fail(`fdo sticky: critical toast gone after >${DEFAULT_MS}ms (${stickRed} red px) — expire_timeout 0 did not map to sticky`);
  } else {
    ok(`expire_timeout 0 -> sticky (still ${stickRed} red px after > the ${DEFAULT_MS}ms server default)`);
  }

  // (13d) SECOND button -> the SECOND action (fdo_dismiss), and the click also
  // dismisses the (otherwise sticky) toast.
  if (fdo.cols.length === 2) {
    const fired2 = await clickButtonCentre(fdo.cols, 1);
    if (!fired2 || fired2.action !== "fdo_dismiss") {
      fail(`fdo routing: second-button click fired ${JSON.stringify(fired2)}, want "fdo_dismiss" (not the first "fdo_retry")`);
    } else {
      ok(`freedesktop per-button routing: second button fired the SECOND action (${fired2.action}), not the first`);
    }
    await page.waitForTimeout(200);
    const goneP = await grab(cw, ROW_X, ROW0_Y, TW, TH);
    const goneRed = goneP ? redCount(goneP) : 999999;
    if (goneRed > EMPTY) {
      fail(`fdo routing: the sticky critical toast is still present after the dismissing click (${goneRed} red px)`);
    } else {
      ok(`freedesktop routing: the click also dismissed the sticky toast (${goneRed} red px)`);
    }
  }

  // (13e) FIRST button -> the FIRST action (fdo_retry) on a fresh critical toast.
  const fdo2 = await postFdoCritical("Retry|fdo_retry;Dismiss|fdo_dismiss");
  if (fdo2.cols.length !== 2) {
    fail(`fdo routing: expected 2 button dividers on the second critical toast, found ${fdo2.cols.length}`);
  } else {
    const fired1 = await clickButtonCentre(fdo2.cols, 0);
    if (!fired1 || fired1.action !== "fdo_retry") {
      fail(`fdo routing: first-button click fired ${JSON.stringify(fired1)}, want "fdo_retry"`);
    } else {
      ok(`freedesktop per-button routing: first button fired the FIRST action (${fired1.action})`);
    }
    await page.waitForTimeout(200);
  }

  // (13f) a NORMAL-urgency post with a finite expire_timeout is an INFO pill that
  // AUTO-DISMISSES (not sticky) — the counterpart to the sticky critical above.
  const NORM_MS = 1200;
  await postNotification(cw, { summary: "Saved", body: "all changes written", urgency: 1, expire_timeout: NORM_MS });
  await page.waitForTimeout(500);
  const normPng = await grab(cw, ROW_X, ROW0_Y, TW, TH);
  const normBright = normPng ? brightCount(normPng, BRIGHT) : 0;
  const normRed = normPng ? redCount(normPng) : 999999;
  if (normBright < PRESENT) {
    fail(`fdo normal: only ${normBright} bright px — a Normal-urgency toast did not render as an info pill`);
  } else if (normRed > PRESENT) {
    fail(`fdo normal: ${normRed} brick-red px — a Normal-urgency toast should be an info pill, not error`);
  } else {
    ok(`Normal urgency -> info pill (${normBright} accent-bright px, ${normRed} red px)`);
  }
  await page.waitForTimeout(NORM_MS + 900);
  const normGone = await grab(cw, ROW_X, ROW0_Y, TW, TH);
  const normGoneB = normGone ? brightCount(normGone, BRIGHT) : 999999;
  if (normGoneB > EMPTY) {
    fail(`fdo normal: toast not gone after its ${NORM_MS}ms expire_timeout (${normGoneB} bright px)`);
  } else {
    ok(`finite expire_timeout -> the Normal toast auto-dismissed (${normGoneB} bright px)`);
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
