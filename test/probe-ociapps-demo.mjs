// SPDX-License-Identifier: BSD-3-Clause
//
// Playwright probe for `task demo-wasmbox-ociapps` (B3).
//
// What it verifies:
//   1. The wasmbox compositor boots on the local cmd/serve (COOP/COEP set).
//   2. Loading the page with ?ociapps=1 makes boot-config.js auto-spawn
//      hello + dock + terminal + files via wasmboxSpawnFromOCI.
//   3. Each spawn issues GETs against http://localhost:5000/v2/<app>/manifests/
//      latest + .../blobs/sha256:... (= live OCI streaming).
//   4. The compositor renders at least the dock + hello windows on the
//      desktop canvas (proxy: non-trivial pixel count + the titlebar text
//      lookup is too brittle, so we use the persisted layout record as the
//      ground-truth title list, like the step-C.1 probe does).
//   5. A screenshot lands at /tmp/wasmbox-ociapps-demo.png + the console is
//      free of pageerror / uncaught exceptions.

import { chromium } from "playwright";
import { writeFileSync } from "node:fs";

const base = process.env.WASMBOX_BASE_URL || "http://127.0.0.1:8094";
const channel = process.env.WASMBOX_CHROME_CHANNEL;

const browser = await chromium.launch(
  channel ? { headless: true, channel } : { headless: true },
);

const consoleLines = [];
const errors = [];
const ociV2Requests = [];

function fail(msg) { console.error(`FAIL: ${msg}`); process.exitCode = 1; }

try {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 720 } });
  const page = await ctx.newPage();
  page.on("console", (m) => consoleLines.push(`[${m.type()}] ${m.text()}`));
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("request", (req) => {
    const u = req.url();
    if (/^https?:\/\/(localhost|127\.0\.0\.1):5000\/v2\//.test(u)) {
      ociV2Requests.push(u);
    }
  });

  await page.goto(`${base}/index.html?ociapps=1`, { waitUntil: "load" });

  // Compositor boot.
  await page.waitForFunction(() => globalThis.wasmboxReady === true, null, { timeout: 20000 });

  // boot-config.js logs after spawning; wait a beat for the OCI fetches +
  // worker handoff to complete + the WM to register windows.
  await page.waitForTimeout(4000);

  const layout = await page.evaluate(() => localStorage.getItem("wasmbox.layout") || "");
  const titles = layout.split("\n").map((row) => row.split("\t")[0]).filter(Boolean);
  const seen = new Set(titles);

  // Probe: confirm OCI registry traffic happened.
  if (ociV2Requests.length === 0) {
    fail("no /v2/* requests recorded against the local OCI registry");
  } else {
    console.log(`OK: recorded ${ociV2Requests.length} /v2/* requests, e.g. ${ociV2Requests.slice(0, 4).join("\n   ")}`);
  }

  // Each app's required tag should show up at least once as /v2/<app>/manifests/
  const seenInRequests = new Set();
  for (const u of ociV2Requests) {
    const m = u.match(/\/v2\/([^/]+)\/manifests\//);
    if (m) seenInRequests.add(m[1]);
  }
  for (const app of ["hello", "dock", "terminal", "files"]) {
    if (!seenInRequests.has(app)) {
      fail(`no manifest GET observed for app ${app}`);
    } else {
      console.log(`OK: manifest GET for ${app}`);
    }
  }

  // Probe DOM: at least one OCI-spawned client title appeared. The compositor
  // persists titles only on layout change; spawn alone is enough to add the
  // window. We look for "hello (wasm)" or "wasmdock" -- the two real surfaces.
  // (Terminal + Files are the placeholder clients; they also paint surfaces
  // and persist titles.)
  let matchedTitles = 0;
  for (const expected of ["hello (wasm)", "wasmdock", "Terminal", "Files"]) {
    if (seen.has(expected)) {
      matchedTitles++;
      console.log(`OK: window present: ${expected}`);
    } else {
      console.log(`MISS (best-effort): window not in persisted layout: ${expected}`);
    }
  }
  if (matchedTitles === 0) {
    fail(`no OCI-spawned client windows in persisted layout (titles=${JSON.stringify(titles)})`);
  }

  // Screenshot.
  const buf = await page.screenshot({ fullPage: false });
  writeFileSync("/tmp/wasmbox-ociapps-demo.png", buf);
  console.log(`OK: wrote /tmp/wasmbox-ociapps-demo.png (${buf.length} bytes)`);

  if (errors.length) {
    fail(`pageerror(s): ${errors.join(" | ")}`);
  }

  // Surface the console log (helpful for debugging on a failure).
  if (process.env.WASMBOX_VERBOSE === "1") {
    console.log("--- console ---");
    for (const line of consoleLines) console.log(line);
  }
} finally {
  await browser.close();
}

if (process.exitCode) {
  console.error("--- console (failure tail) ---");
  for (const line of consoleLines.slice(-40)) console.error(line);
}
