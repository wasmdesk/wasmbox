// SPDX-License-Identifier: BSD-3-Clause
//
// Headless-browser probe: aspect-ratio lock during interactive resize.
// Quake's worker.js posts set_lock_aspect{ratio: 4/3} after the welcome,
// so dragging the bottom-right grip MUST yield a window whose w/h ratio
// stays at 4:3 (within 1% slack for integer rounding).
//
// Repro: spawn Quake (auto-spawns from compositor.worker.js). Drag the
// grip with two different drag shapes:
//   (A) symmetric drag +200,+200  -- without the lock the window would
//       grow by exactly 200 on each axis; with the lock h follows w/(4/3).
//   (B) wide drag +300,+50        -- a large x delta + small y delta:
//       free resize would yield w~big, h~small (broken aspect); the
//       locked variant must STILL match 4:3.
//
// Output:
//   * /tmp/wasmbox-aspect-lock.png        -- post-resize viewport
//   * /tmp/wasmbox-aspect-lock-pre.png    -- before any drag
//   * /tmp/wasmbox-aspect-lock-A.png      -- after symmetric drag
//   * /tmp/wasmbox-aspect-lock-B.png      -- after wide drag
//   * stdout: pre/post rect + ratio computation + PASS/FAIL banner
// Exit code: 0 on PASS, non-zero on any assertion failure.

import { chromium } from "playwright";

const base = process.env.WASMBOX_BASE_URL || "http://127.0.0.1:8094";
const wait = (ms) => new Promise((r) => setTimeout(r, ms));
const RATIO = 4.0 / 3.0;
const SLACK = 0.012; // ~1.2% — covers integer rounding + 1-px decoration drift

let failures = 0;
function expect(cond, msg) {
  if (cond) console.log("PASS: " + msg);
  else      { console.error("FAIL: " + msg); failures++; }
}

const browser = await chromium.launch({
  headless: true,
  args: ["--enable-features=SharedArrayBuffer"],
});
const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
const page = await ctx.newPage();

const consoleLines = [];
page.on("console", (m) => consoleLines.push(`[${m.type()}] ${m.text()}`));
page.on("pageerror", (e) => consoleLines.push(`[err] ${String(e)}`));
page.on("worker", (w) => {
  w.on("console", (m) => consoleLines.push(`[w:${m.type()}] ${m.text()}`));
});

// Clean layout so the pre-resize geometry is the default cascade slot.
await page.goto(base + "/", { waitUntil: "load", timeout: 30000 });
await page.evaluate(() => { try { localStorage.removeItem("wasmbox.layout"); } catch (_) {} });
await page.reload({ waitUntil: "load", timeout: 30000 });
await page.waitForFunction(() => !!window.wasmboxReady, { timeout: 30000 });

async function quakeRect() {
  return await page.evaluate(() => {
    const raw = localStorage.getItem("wasmbox.layout");
    if (raw == null) return null;
    const matches = [];
    for (const line of String(raw).split("\n")) {
      const p = line.split("\t");
      if (p.length >= 5 && /quake/i.test(p[0])) {
        matches.push({ title: p[0], x: +p[1], y: +p[2], w: +p[3], h: +p[4] });
      }
    }
    return matches.length ? matches[matches.length - 1] : null;
  });
}

let rect = null;
for (let i = 0; i < 40; i++) {
  rect = await quakeRect();
  if (rect) break;
  await wait(500);
}
if (!rect) {
  console.error("probe-aspect-lock: quake window never registered. Last 30 console lines:");
  for (const l of consoleLines.slice(-30)) console.error("  " + l);
  await browser.close();
  process.exit(1);
}
console.log(`probe-aspect-lock: pre-resize rect = ${JSON.stringify(rect)}`);
const preRatio = rect.w / rect.h;
console.log(`probe-aspect-lock: pre-resize w/h = ${preRatio.toFixed(4)} (target ${RATIO.toFixed(4)})`);

// Give Quake a few frames so the surface is alive and the welcome / lock
// handshake completed. The set_lock_aspect message rides on the same
// ordered port as welcome, so it lands strictly before the first commit;
// 3 s is generous.
await wait(3000);

await page.screenshot({ path: "/tmp/wasmbox-aspect-lock-pre.png", fullPage: false });

// --- (A) SYMMETRIC DRAG: +200,+200 from the grip --------------------------
// Without the lock this would change BOTH dimensions by ~200 (w=1000, h=800).
// With the lock, width is the leader (h follows w/(4/3)).
{
  const gripX = rect.x + rect.w - 2;
  const gripY = rect.y + rect.h - 2;
  console.log(`probe-aspect-lock: (A) symmetric drag grip (${gripX},${gripY}) by (+200,+200)`);
  await page.mouse.move(gripX, gripY);
  await page.mouse.down();
  await page.mouse.move(gripX + 80,  gripY + 80,  { steps: 4 });
  await page.mouse.move(gripX + 160, gripY + 160, { steps: 4 });
  await page.mouse.move(gripX + 200, gripY + 200, { steps: 4 });
  await page.mouse.up();
  await wait(1200);

  const rA = await quakeRect();
  console.log(`probe-aspect-lock: (A) post rect = ${JSON.stringify(rA)}`);
  await page.screenshot({ path: "/tmp/wasmbox-aspect-lock-A.png", fullPage: false });

  expect(rA && rA.w > rect.w + 50, `(A) width grew (${rA && rA.w} > ${rect.w + 50})`);
  const ratioA = rA ? rA.w / rA.h : 0;
  console.log(`probe-aspect-lock: (A) post w/h = ${ratioA.toFixed(4)} (target ${RATIO.toFixed(4)})`);
  expect(rA && Math.abs(ratioA - RATIO) < SLACK,
    `(A) post-resize w/h within ${SLACK} of 4:3 (got ${ratioA.toFixed(4)})`);
  // Re-baseline rect for the next pass so cumulative geometry is correct.
  if (rA) rect = rA;
}

// --- (B) WIDE DRAG: +300 x, +50 y from the (new) grip ---------------------
// Width is again the leader. Even though the y-delta is tiny, h must still
// follow w/(4/3).
{
  const gripX = rect.x + rect.w - 2;
  const gripY = rect.y + rect.h - 2;
  console.log(`probe-aspect-lock: (B) wide drag grip (${gripX},${gripY}) by (+300,+50)`);
  await page.mouse.move(gripX, gripY);
  await page.mouse.down();
  await page.mouse.move(gripX + 150, gripY + 25, { steps: 4 });
  await page.mouse.move(gripX + 300, gripY + 50, { steps: 4 });
  await page.mouse.up();
  await wait(1200);

  const rB = await quakeRect();
  console.log(`probe-aspect-lock: (B) post rect = ${JSON.stringify(rB)}`);
  await page.screenshot({ path: "/tmp/wasmbox-aspect-lock-B.png", fullPage: false });
  await page.screenshot({ path: "/tmp/wasmbox-aspect-lock.png", fullPage: false });

  expect(rB && rB.w > rect.w + 100, `(B) width grew (${rB && rB.w} > ${rect.w + 100})`);
  const ratioB = rB ? rB.w / rB.h : 0;
  console.log(`probe-aspect-lock: (B) post w/h = ${ratioB.toFixed(4)} (target ${RATIO.toFixed(4)})`);
  expect(rB && Math.abs(ratioB - RATIO) < SLACK,
    `(B) post-resize w/h within ${SLACK} of 4:3 (got ${ratioB.toFixed(4)})`);
  // The free-resize buggy case would yield h~unchanged from the small y delta
  // (i.e. h close to rect.h before this pass, NOT close to rB.w/(4/3)).
  // Document the locked-vs-free outcome explicitly:
  if (rB) {
    const freeH = rect.h + 50;
    const lockedH = Math.round(rB.w / RATIO);
    console.log(`probe-aspect-lock: (B) free-resize h would be ~${freeH}, locked h = ${lockedH}, actual h = ${rB.h}`);
    expect(Math.abs(rB.h - lockedH) <= 2,
      `(B) actual h == locked h (within 2 px): actual=${rB.h}, locked=${lockedH}`);
  }
}

await browser.close();

if (failures > 0) {
  console.error(`probe-aspect-lock: ${failures} assertion(s) failed`);
  console.error("Last 40 console lines:");
  for (const l of consoleLines.slice(-40)) console.error("  " + l);
  process.exit(1);
}
console.log("probe-aspect-lock: OVERALL PASS");
