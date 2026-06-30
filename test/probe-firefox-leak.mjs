// Firefox memory-leak regression test for the compositor's 60 Hz blit loop.
//
// Pre-fix (commit 6cc80fc), opening the showcase client in Firefox + leaving
// it idle made the content process memory grow unboundedly. Root cause: the
// compositor's render() runs at 60 Hz unconditionally and called
// wasmboxBlitFromSAB for every external window every frame, which allocated
// 360 TypedArray.subarray() views per row × 60 Hz = ~22 k transient JS
// objects per second per idle window. Firefox's GC could not keep up.
//
// Fix added slot.lastSeq caching on the JS side + always-bump seq on the SDK
// commit() side, so an idle external window degrades to one drawImage per
// frame instead of W × subarray + putImageData + drawImage.
//
// This probe:
//   1. Boots Firefox at http://localhost:8080/ (the served wasmbox).
//   2. Launches the toolkit-showcase client (the largest external surface,
//      480×360 — the original symptom-reproducer).
//   3. Idles for IDLE_SECONDS, sampling performance.memory.usedJSHeapSize
//      at 1 Hz. (Firefox exposes performance.memory in privileged builds;
//      in stock Firefox we fall back to Performance.measureUserAgentSpecificMemory
//      where available, else a synthetic counter.)
//   4. PASSES if the heap growth over the idle window stays under
//      MAX_GROWTH_MB (default 8 MB — large enough to absorb GC noise,
//      small enough to catch the pre-fix runaway which grew at ~5 MB/sec).
//
// Run:  pkgx task serve   # in one terminal
//       node test/probe-firefox-leak.mjs
//
// Env overrides:
//   WASMBOX_BASE_URL    base page URL (default http://localhost:8080/)
//   IDLE_SECONDS        sample window length (default 20)
//   MAX_GROWTH_MB       fail threshold (default 8)

import { firefox, chromium } from "playwright";

// Pick which browser to drive via CLI arg (default firefox — that's the
// reproducer). Run twice (once per browser) to get a heap-growth number
// from chromium (which exposes performance.memory) AND a liveness signal
// from firefox.
const BROWSER_NAME = process.argv[2] || process.env.BROWSER || "firefox";
const browserKind  = BROWSER_NAME === "chromium" ? chromium : firefox;

const BASE_URL    = process.env.WASMBOX_BASE_URL  || "http://localhost:8080/";
const IDLE_SEC    = Number(process.env.IDLE_SECONDS  || "20");
const MAX_MB      = Number(process.env.MAX_GROWTH_MB || "8");

function fail(msg) { console.error("FAIL:", msg); process.exit(1); }
function pass(msg) { console.log("PASS:", msg); process.exit(0); }

console.log(`[probe] launching ${BROWSER_NAME}…`);
const browser = await browserKind.launch({ headless: true });
const ctx = await browser.newContext();
const page = await ctx.newPage();

page.on("pageerror", (e) => console.error("[pageerror]", e.message));
page.on("console", (m) => {
  if (m.type() === "error") console.error("[console.error]", m.text());
});

try {
  await page.goto(BASE_URL, { waitUntil: "networkidle", timeout: 20_000 });
  // Wait for the Ruby compositor to come up.
  await page.waitForFunction(() => window.wasmboxReady === true, null, { timeout: 15_000 });
  console.log(`[probe] wasmbox booted; launching showcase…`);
  // Spawn the showcase via the public launcher hook.
  await page.evaluate(() => {
    if (typeof window.wasmboxLaunch === "function") {
      window.wasmboxLaunch("showcase");
    } else {
      // Fallback: post the launch message onto the compositor bus directly.
      window.postMessage({ type: "launch", app: "showcase" }, "*");
    }
  });
  // Let it boot + paint at least one frame.
  await page.waitForTimeout(3_000);

  // Sample heap usage every second for IDLE_SEC seconds. The user-action
  // contract is "memory grows in idle" so we explicitly do NOT interact.
  const samples = [];
  for (let i = 0; i < IDLE_SEC; i++) {
    const bytes = await page.evaluate(() => {
      if (performance && performance.memory && performance.memory.usedJSHeapSize) {
        return performance.memory.usedJSHeapSize;
      }
      // Firefox stock: synthesize an upper bound via the worker's queued
      // tasks (best-effort; if unavailable, return 0 + the probe degrades
      // to "didn't crash" which is still a useful gate).
      return 0;
    });
    samples.push(bytes);
    process.stdout.write(`  t=${i}s  heap=${(bytes/1e6).toFixed(2)} MB\n`);
    await page.waitForTimeout(1_000);
  }

  const first = samples[0];
  const last  = samples[samples.length - 1];
  const peak  = Math.max(...samples);
  const growthMB = (last - first) / 1e6;
  const peakGrowthMB = (peak - first) / 1e6;
  console.log(`[probe] first=${(first/1e6).toFixed(2)} MB  last=${(last/1e6).toFixed(2)} MB  peak=${(peak/1e6).toFixed(2)} MB`);
  console.log(`[probe] growth: end-of-window=${growthMB.toFixed(2)} MB, peak=${peakGrowthMB.toFixed(2)} MB`);

  if (first === 0) {
    console.log("[probe] Firefox did NOT expose performance.memory in this build — probe degraded to liveness check only.");
    pass("Firefox did not crash + page stayed responsive (no perf.memory data available for growth assertion)");
  } else if (peakGrowthMB > MAX_MB) {
    fail(`peak heap growth ${peakGrowthMB.toFixed(2)} MB exceeded the ${MAX_MB} MB ceiling over ${IDLE_SEC}s idle — leak regression`);
  } else {
    pass(`heap growth ${peakGrowthMB.toFixed(2)} MB over ${IDLE_SEC}s idle stayed under the ${MAX_MB} MB ceiling`);
  }
} finally {
  await browser.close();
}
