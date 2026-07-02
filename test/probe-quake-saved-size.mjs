// SPDX-License-Identifier: BSD-3-Clause
//
// Headless-browser probe: Quake spawns at its SAVED window size, and
// the Go-side renderer allocates the SAB at THAT size (not the old
// hardcoded 320x240).
//
// Two-pass flow:
//   PASS 1 -- fresh load (localStorage cleared at boot). Quake should
//     auto-spawn at the default 800x600 and the worker console should
//     report "fb=800x600 source=default". The compositor's persisted
//     layout record for "quake (wasm)" should also be 800x600.
//   PASS 2 -- resize the Quake window by dragging the grip toward 900x700.
//     The surface is aspect-locked to 4:3, so it settles at 900x675; the
//     compositor persists THAT, then RELOAD the page. On reload the worker
//     should read 900x675 from localStorage, publish it on the globals, and
//     the Go side should log "wasmbox surface=900x675" -- proof the engine
//     renders natively at the saved dim.
//
// Output:
//   * /tmp/wasmbox-quake-saved-size.png         -- post-reload viewport
//   * /tmp/wasmbox-quake-saved-size-pass1.png   -- pass 1 (default)
//   * /tmp/wasmbox-quake-saved-size-pass2.png   -- pass 2 after resize
//   * stdout: layout records + log evidence + PASS/FAIL banner

import { chromium } from "playwright";

const base = process.env.WASMBOX_BASE_URL || "http://127.0.0.1:8094";
const wait = (ms) => new Promise((r) => setTimeout(r, ms));

let failures = 0;
function expect(cond, msg) {
  if (cond) console.log("PASS: " + msg);
  else { console.error("FAIL: " + msg); failures++; }
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

// Force a clean layout so PASS 1 is genuinely a default-size boot.
// IMPORTANT: a Playwright addInitScript would persist across reload,
// which would wipe the resized layout we want PASS 2 to read. Do the
// clean step once via a one-shot evaluate, then nuke addInitScript.
console.log("== PASS 1: fresh load, expect default 800x600 ==");
// Visit a blank page so localStorage is addressable, then clear the
// key, then navigate to the real page.
await page.goto(base + "/", { waitUntil: "load", timeout: 30000 });
await page.evaluate(() => { try { localStorage.removeItem("wasmbox.layout"); } catch (_) {} });
await page.reload({ waitUntil: "load", timeout: 30000 });
await page.waitForFunction(() => !!window.wasmboxReady, { timeout: 30000 });

async function quakeRect() {
  return await page.evaluate(() => {
    const raw = localStorage.getItem("wasmbox.layout");
    if (raw == null) return null;
    let last = null;
    for (const line of String(raw).split("\n")) {
      const p = line.split("\t");
      if (p.length >= 5 && /quake/i.test(p[0])) {
        last = { title: p[0], x: +p[1], y: +p[2], w: +p[3], h: +p[4] };
      }
    }
    return last;
  });
}

let rect = null;
for (let i = 0; i < 40; i++) {
  rect = await quakeRect();
  if (rect) break;
  await wait(500);
}
if (!rect) {
  console.error("PASS 1: quake window never registered. Last 30 console lines:");
  for (const l of consoleLines.slice(-30)) console.error("  " + l);
  await browser.close();
  process.exit(1);
}
console.log("PASS 1 rect:", JSON.stringify(rect));
expect(rect.w === 800, `pass 1 width=800 (got ${rect.w})`);
expect(rect.h === 600, `pass 1 height=600 (got ${rect.h})`);

// Worker console: confirm fb decision + Go-side backend surface log.
await wait(2000);
const pass1Logs = consoleLines.slice();
const sawDefault = pass1Logs.some((l) => /quake worker: fb=800x600 source=default/.test(l));
expect(sawDefault, "worker logs fb=800x600 source=default");
const sawSurface800 = pass1Logs.some((l) => /wasmbox surface=800x600/.test(l));
expect(sawSurface800, "engine logs wasmbox surface=800x600");

await page.screenshot({ path: "/tmp/wasmbox-quake-saved-size-pass1.png", fullPage: false });

// --- Resize the Quake window to ~900 wide via the grip --------------------
// The Quake surface is aspect-locked to 4:3, so we drag toward 900x700 but
// the compositor drives the width to ~900 and snaps the height to the 4:3
// companion (round(900 * 3/4) = 675) rather than the raw dragged 700.
console.log("== resizing Quake window toward 900 wide (4:3-locked) ==");
const targetW = 900, dragH = 700;
const dx = targetW - rect.w;
const dy = dragH - rect.h;
const gripX = rect.x + rect.w - 4;
const gripY = rect.y + rect.h - 4;
await page.mouse.move(gripX, gripY);
await page.mouse.down();
await page.mouse.move(gripX + Math.floor(dx / 2), gripY + Math.floor(dy / 2), { steps: 6 });
await page.mouse.move(gripX + dx, gripY + dy, { steps: 6 });
await page.mouse.up();
await wait(1500);

const rectAfterResize = await quakeRect();
console.log("post-resize rect:", JSON.stringify(rectAfterResize));
// Height follows the 4:3 lock off the achieved width, not the dragged 700.
const expectH = rectAfterResize ? Math.round(rectAfterResize.w * 3 / 4) : 0;
expect(rectAfterResize && Math.abs(rectAfterResize.w - targetW) <= 6,
  `resized width ~= ${targetW} (got ${rectAfterResize ? rectAfterResize.w : "?"})`);
expect(rectAfterResize && Math.abs(rectAfterResize.h - expectH) <= 6,
  `resized height 4:3-locked ~= ${expectH} (got ${rectAfterResize ? rectAfterResize.h : "?"})`);

// Capture the saved size so PASS 2 can compare apples to apples.
const savedW = rectAfterResize.w;
const savedH = rectAfterResize.h;

// --- PASS 2: reload, expect the saved size ---
console.log(`== PASS 2: reload, expect saved ${savedW}x${savedH} ==`);
// Important: do NOT add an init script that clears layout this time.
// We want the persisted layout to drive the next boot.
const consoleLinesPass2Start = consoleLines.length;
await page.reload({ waitUntil: "load", timeout: 30000 });
await page.waitForFunction(() => !!window.wasmboxReady, { timeout: 30000 });

let rect2 = null;
for (let i = 0; i < 40; i++) {
  rect2 = await quakeRect();
  if (rect2 && Math.abs(rect2.w - savedW) <= 6 && Math.abs(rect2.h - savedH) <= 6) break;
  await wait(500);
}
console.log("PASS 2 rect:", JSON.stringify(rect2));
expect(rect2 && Math.abs(rect2.w - savedW) <= 6,
  `pass 2 width restored to ~${savedW} (got ${rect2 ? rect2.w : "?"})`);
expect(rect2 && Math.abs(rect2.h - savedH) <= 6,
  `pass 2 height restored to ~${savedH} (got ${rect2 ? rect2.h : "?"})`);

// Wait long enough for the engine's "wasmbox surface=WxH" line to print.
await wait(3000);
const pass2Logs = consoleLines.slice(consoleLinesPass2Start);
const wantSurface = new RegExp(`wasmbox surface=${savedW}x${savedH}`);
const sawSurfaceSaved = pass2Logs.some((l) => wantSurface.test(l));
expect(sawSurfaceSaved, `engine logs wasmbox surface=${savedW}x${savedH} on reload`);

const wantWorker = new RegExp(`quake worker: fb=${savedW}x${savedH} source=saved`);
// The worker log may flow either through page.on("worker"...) or via
// the compositor-worker -> main bridge relay; we accept either. We
// also accept finding it in pass1 logs only when the bridge relayed
// PASS 2's worker log into the same per-page console (Playwright
// occasionally orders the worker registration after the main-thread
// boot, in which case the listener attached too late for PASS 2 but
// the bridge mirror still captured it).
const sawWorkerSaved = consoleLines.some((l) => wantWorker.test(l));
expect(sawWorkerSaved, `worker logs fb=${savedW}x${savedH} source=saved`);

await page.screenshot({ path: "/tmp/wasmbox-quake-saved-size.png", fullPage: false });
await page.screenshot({ path: "/tmp/wasmbox-quake-saved-size-pass2.png", fullPage: false });

await browser.close();

if (failures > 0) {
  console.error(`probe-quake-saved-size: ${failures} assertion(s) failed`);
  console.error("Last 60 console lines:");
  for (const l of consoleLines.slice(-60)) console.error("  " + l);
  process.exit(1);
}
console.log("probe-quake-saved-size: OVERALL PASS");
