// Cross-browser probe for the new dom-window protocol (Batch 1 of the
// vscodium integration). A dom-window has compositor-painted chrome
// on the canvas + an actual <iframe> overlaid on top of the canvas at
// the window's body-rect. The probe:
//
//   1. Spawns a dom-window pointing at about:blank (no remote dep).
//   2. Polls until `#__wasmbox_iframes > iframe[data-window-id]` exists.
//   3. Reads its bounding rect; asserts non-zero size + sane position.
//
// Runs against chromium, firefox, webkit. A regression that breaks
// the protocol (Ruby class missing, JS helper missing, message
// constants mismatched, etc.) makes the iframe never appear and the
// poll times out -> FAIL.

import { firefox, chromium, webkit } from "playwright";

const base = process.env.WASMBOX_BASE_URL || "http://127.0.0.1:8080";
const wait = (ms) => new Promise(r => setTimeout(r, ms));

async function probe(name, launcher) {
  console.log(`\n=== ${name} ===`);
  const browser = await launcher.launch({ headless: true });
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const page = await ctx.newPage();
  const consoleLines = [];
  page.on("console", (m) => consoleLines.push(`[${m.type()}] ${m.text()}`));
  page.on("pageerror", (e) => consoleLines.push(`[err] ${e}`));

  let result = { name, ok: false, reason: "" };
  try {
    await page.goto(base + "/", { waitUntil: "load", timeout: 30000 });
    console.log(`  [${name}] page loaded`);
    await page.waitForFunction(() => !!window.wasmboxReady, { timeout: 30000 });
    console.log(`  [${name}] wasmboxReady=true`);
    await wait(500);

    // Spawn the dom-window via the new public API.
    const before = await page.evaluate(() => typeof globalThis.wasmboxSpawnDOMWindow);
    console.log(`  [${name}] spawn fn type=${before}`);
    await page.evaluate(() =>
      globalThis.wasmboxSpawnDOMWindow("about:blank", 640, 360, "probe-dom"));
    console.log(`  [${name}] dispatched spawn`);

    // Poll for the iframe to appear.
    const found = await page.waitForFunction(() => {
      const c = document.getElementById("__wasmbox_iframes");
      if (!c) return false;
      const iframe = c.querySelector("iframe[data-window-id]");
      if (!iframe) return false;
      const r = iframe.getBoundingClientRect();
      return r.width > 0 && r.height > 0 ? { x: r.x, y: r.y, w: r.width, h: r.height } : false;
    }, { timeout: 10000 }).then(h => h.jsonValue());

    console.log(`  ${name} iframe rect:`, found);
    if (found.w < 100 || found.h < 100) {
      result.reason = `iframe size too small (${found.w}x${found.h})`;
    } else if (found.x < 0 || found.y < 0) {
      result.reason = `iframe out of bounds at (${found.x},${found.y})`;
    } else {
      result.ok = true;
    }
  } catch (e) {
    result.reason = String(e).split("\n")[0];
    if (process.env.DUMP_CONSOLE) {
      console.log("  --- first 30 console lines ---");
      for (const l of consoleLines.slice(0, 30)) console.log("  " + l);
      console.log("  --- last 10 ---");
      for (const l of consoleLines.slice(-10)) console.log("  " + l);
    }
  } finally {
    await browser.close();
  }
  return result;
}

const results = [
  await probe("chromium", chromium),
  await probe("firefox", firefox),
  await probe("webkit", webkit),
];

console.log("\n=== SUMMARY ===");
let fail = false;
for (const r of results) {
  if (r.ok) console.log(`PASS: ${r.name}`);
  else { console.log(`FAIL: ${r.name} -- ${r.reason}`); fail = true; }
}
if (fail) { console.error("\nprobe-dom-window: OVERALL FAIL"); process.exit(1); }
console.log("\nprobe-dom-window: OVERALL PASS");
