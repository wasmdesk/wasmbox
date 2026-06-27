// Headless-browser probe for the Quake-in-wasmbox OCI streaming path.
//
// Boots wasmbox at $WASMBOX_BASE_URL (default http://127.0.0.1:8094), captures
// every /v2/* network request (so the OCI manifest + blob fetches are
// observable), waits long enough for pak0 + at least one music track to land,
// then samples pixels in the Quake window region to assert the rendering is
// the REAL start.bsp scene (not the 4-colour synthbsp placeholder).
//
// Output:
//   * /tmp/wasmbox-quake-oci.png  -- screenshot
//   * stdout: network trace + distinct-colour count + PASS/FAIL banner
// Exit code: 0 on PASS, 1 on any assertion failure.

import { chromium } from "playwright";
import { writeFileSync } from "node:fs";

const base = process.env.WASMBOX_BASE_URL || "http://127.0.0.1:8094";
const wait = (ms) => new Promise(r => setTimeout(r, ms));

// SAB needs cross-origin isolation, which we get through the COOP/COEP
// headers on the served page. No special chromium flags required for that.
// We force --js-flags to drop the JIT memory cap that has historically
// kicked in for ~190 MB pak0 fetches under chromium headless.
const browser = await chromium.launch({
  headless: true,
  args: ["--enable-features=SharedArrayBuffer"],
});
const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
const page = await ctx.newPage();

const v2Requests = [];
const consoleLines = [];
const errors = [];

// Browser-context listeners catch network from ALL contexts incl. Workers.
// page.on('request') alone misses fetches initiated inside a Web Worker --
// which is exactly where the compositor + quake clients live. We attach to
// the context instead so the worker fetches are observable.
ctx.on("request", (req) => {
  const u = req.url();
  if (u.includes("/v2/") || u.includes(":5000/") || u.includes("/blobs/") || u.includes("/manifests/"))
    v2Requests.push({ when: "req", url: u });
});
ctx.on("response", (resp) => {
  const u = resp.url();
  if (u.includes("/v2/") || u.includes(":5000/") || u.includes("/blobs/") || u.includes("/manifests/")) {
    v2Requests.push({
      when: "resp",
      url: u,
      status: resp.status(),
      len: resp.headers()["content-length"] || "?",
    });
  }
});
// Track ALL request URLs for diagnostics.
const allRequests = [];
ctx.on("request", (req) => { allRequests.push(req.url()); });
page.on("console", (m) => {
  const line = `[page-${m.type()}] ${m.text()}`;
  consoleLines.push(line);
});
page.on("pageerror", (e) => { errors.push(String(e)); });
// Web Workers spawned from the page each have their own console; subscribe so
// the QUAKE: log lines surface here. Also attach the same listener to NESTED
// workers (the compositor spawns the quake worker -- its console wasn't
// surfacing via the top-level page.on("worker") listener). We do that via the
// Chrome DevTools Protocol Target.attachedToTarget event.
page.on("worker", (w) => {
  consoleLines.push(`[worker-spawned] ${w.url()}`);
  w.on("console", (m) => {
    consoleLines.push(`[worker-${m.type()}] ${m.text()}`);
  });
});
const cdp = await page.context().newCDPSession(page);
await cdp.send("Target.setDiscoverTargets", { discover: true });
await cdp.send("Target.setAutoAttach", {
  autoAttach: true, waitForDebuggerOnStart: false, flatten: true,
});
// Bus to receive console from every attached target (incl. nested workers).
cdp.on("Target.attachedToTarget", async ({ sessionId, targetInfo }) => {
  consoleLines.push(`[cdp-attached] type=${targetInfo.type} url=${targetInfo.url}`);
});
const fwd = (label) => async (params) => {
  // params has sessionId for sub-sessions; we collect via a per-session map.
};
// `cdp` is the top page session. The Target.attachedToTarget event itself
// delivers Runtime + Log events for the auto-attached child target via the
// same connection when flatten=true; we just need to listen to "Runtime.
// consoleAPICalled" and "Runtime.exceptionThrown" on the session.
cdp.on("Runtime.consoleAPICalled", (msg) => {
  const args = (msg.args || []).map((a) => a.value !== undefined ? String(a.value) : (a.description || a.unserializableValue || `[${a.type}]`)).join(" ");
  consoleLines.push(`[cdp-runtime-${msg.type}] ${args}`);
});
cdp.on("Runtime.exceptionThrown", (msg) => {
  const ex = msg.exceptionDetails;
  errors.push(`[cdp-exc] ${ex.text || ex.exception?.description || JSON.stringify(ex)}`);
});
// Need to enable Runtime on the SESSION for each attached target.
async function enableRuntimeForAttached(sessionId) {
  try {
    await cdp.send("Runtime.enable", undefined, sessionId);
  } catch (e) {
    consoleLines.push(`[cdp] Runtime.enable failed for ${sessionId}: ${e.message}`);
  }
}
cdp.on("Target.attachedToTarget", ({ sessionId }) => { enableRuntimeForAttached(sessionId); });
await cdp.send("Runtime.enable");

console.log(`probe-quake-oci: GET ${base}/`);
await page.goto(base + "/", { waitUntil: "load", timeout: 30000 });

// Wait for wasmbox to be ready + quake worker to spawn.
await page.waitForFunction(() => !!window.wasmboxReady, { timeout: 30000 });
console.log("probe-quake-oci: wasmboxReady=true");

// Give the compositor a moment to spawn its auto-started clients (including
// the quake worker). The quake worker then handshakes via SAB + the Go side
// starts streaming the OCI manifest + blobs from localhost:5000.
//
// pak0 is 180 MB on localhost so it lands in ~1-2 s; the manifest fetch +
// runloop bring-up + first PresentFrame is maybe another 2-3 s. We poll for
// up to 30 s for the FIRST /v2/ blob response so the wait scales with the
// slowest link rather than just blindly sleeping.
const deadline = Date.now() + 60000;
const start = Date.now();
let firstBlobAt = 0;
while (Date.now() < deadline) {
  await wait(500);
  const blobResp = v2Requests.find((r) => r.when === "resp" && /\/blobs\//.test(r.url) && r.status === 200);
  if (blobResp) { firstBlobAt = Date.now(); break; }
}
console.log(`probe-quake-oci: first blob response at ${firstBlobAt ? "t+" + ((firstBlobAt - start)/1000).toFixed(1) + "s" : "NEVER (no /v2/ blob came back in 60 s)"}`);

// Give the engine another ~10 s after the first blob to: finish receiving
// pak0, parse it, load start.bsp, paint frames. The Pre2DDraw walker emits a
// real BSP scene once the pak is available.
await wait(10000);

// Screenshot full viewport for debugging.
await page.screenshot({ path: "/tmp/wasmbox-quake-oci.png", fullPage: false });

// Find the quake window. The compositor's window stack is exposed via
// `wasmboxWindows` (each entry has {title, x, y, w, h}). The auto-spawn names
// the quake window from the wasmbox client's `name` arg ("quake (wasm)"),
// matching the wasmbox.NewClient call in cmd/quake-wasmbox/main.go.
const winInfo = await page.evaluate(() => {
  // Try a couple of well-known shapes. The compositor surfaces windows via
  // globalThis.wasmboxWindows (each row {title, x, y, w, h}) AND via the
  // persisted layout string. We try both.
  const out = [];
  if (Array.isArray(globalThis.wasmboxWindows)) {
    for (const w of globalThis.wasmboxWindows) {
      out.push({ title: w.title || "?", x: w.x|0, y: w.y|0, w: w.w|0, h: w.h|0 });
    }
  }
  return { windows: out, canvas: (() => {
    const c = document.getElementById("screen");
    if (!c) return null;
    return { w: c.width, h: c.height, cssW: c.clientWidth, cssH: c.clientHeight };
  })() };
});
console.log("probe-quake-oci: canvas=", JSON.stringify(winInfo.canvas));
console.log("probe-quake-oci: windows=", JSON.stringify(winInfo.windows));

// We don't have direct device-pixel access to the canvas (it's an
// OffscreenCanvas owned by the worker). Instead we crop a region of the page
// screenshot via Playwright's clip option. The quake window auto-spawn puts
// the window roughly mid-screen at the default 320x240 framebuffer; pick a
// generous central crop that almost certainly overlaps it.
const cropPath = "/tmp/wasmbox-quake-oci-crop.png";
await page.screenshot({
  path: cropPath,
  clip: { x: 200, y: 100, width: 700, height: 500 },
});

// Read the crop back + count distinct colours. We use a Node-side PNG reader.
// The synth placeholder has at most 4 face colours + 1 sky + a few HUD ramps;
// the real start.bsp renders ~hundreds of distinct colours (texture + light
// blends + console text + status bar).
const { PNG } = await import("pngjs");
const { readFileSync } = await import("node:fs");
const png = PNG.sync.read(readFileSync(cropPath));
const colours = new Set();
let nonBlack = 0;
for (let i = 0; i < png.data.length; i += 4) {
  const r = png.data[i], g = png.data[i+1], b = png.data[i+2];
  if (r || g || b) nonBlack++;
  // Bucket to 5-bit per channel so JPEG-style speckle doesn't inflate the
  // count. Synth = 4-8 distinct buckets, real BSP = >> 30.
  const bucket = (r >> 3) << 10 | (g >> 3) << 5 | (b >> 3);
  colours.add(bucket);
}
const distinct = colours.size;
console.log(`probe-quake-oci: crop=${cropPath} ${png.width}x${png.height} nonBlack=${nonBlack} distinctBuckets=${distinct}`);

// Network trace summary.
const manifestReqs = v2Requests.filter((r) => /\/manifests\//.test(r.url));
const blobReqs = v2Requests.filter((r) => /\/blobs\//.test(r.url));
console.log(`probe-quake-oci: /v2/manifests/* requests: ${manifestReqs.length}`);
for (const m of manifestReqs.slice(0, 4)) console.log("  ", JSON.stringify(m));
console.log(`probe-quake-oci: /v2/blobs/* requests: ${blobReqs.length}`);
for (const b of blobReqs.slice(0, 8)) console.log("  ", JSON.stringify(b));
console.log(`probe-quake-oci: ALL captured requests (${allRequests.length}); subset to localhost+5000+v2:`);
for (const u of allRequests.filter((u) => /5000|v2|quake|register/.test(u)).slice(0, 30))
  console.log("  ", u);

console.log(`probe-quake-oci: console lines total=${consoleLines.length}; interesting:`);
const interesting = consoleLines.filter((l) =>
  /QUAKE:|ociassets|spawn|quake|worker|error|fail/i.test(l) &&
  !/devtools/.test(l)
);
for (const l of interesting.slice(0, 60)) console.log("  " + l);

await browser.close();

// Assertions.
let fail = false;
function expect(cond, msg) {
  if (!cond) { console.error("FAIL:", msg); fail = true; }
  else console.log("PASS:", msg);
}
expect(manifestReqs.some((r) => r.when === "resp" && r.status === 200 && /quake-assets/.test(r.url)),
  "saw a 200 response on /v2/quake-assets/manifests/...");
expect(blobReqs.filter((r) => r.when === "resp" && r.status === 200).length >= 1,
  "saw at least one 200 response on /v2/quake-assets/blobs/...");
expect(distinct >= 30,
  `at least 30 distinct colour buckets in the crop (got ${distinct})`);
expect(errors.length === 0,
  "no uncaught page errors (errors=" + errors.join(" | ") + ")");

if (fail) { console.error("probe-quake-oci: OVERALL FAIL"); process.exit(1); }
console.log("probe-quake-oci: OVERALL PASS");
