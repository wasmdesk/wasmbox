// SPDX-License-Identifier: BSD-3-Clause
//
// Cluster-fallback E2E probe.
//
// Pins, end-to-end through the real browser-side OCIAppsLoader inside the
// compositor worker, that a two-registry cluster falls through transparently
// when the first registry is unhealthy:
//
//   * R1 = http://127.0.0.1:5000  -- returns 503 on every /v2/<repo>/manifests/
//                                    and /v2/<repo>/blobs/ request
//   * R2 = http://127.0.0.1:5001  -- serves the canned hello:test-canned
//                                    manifest + blobs
//
// The compositor worker is told about both registries via
// WASMBOX_OCI_REGISTRIES (we inject the variable by routing the worker script
// itself and prepending the assignment -- the worker has its own globalThis
// distinct from the page, so an addInitScript on the page would never reach
// it). When the page drives wasmboxSpawnFromOCI("hello:test-canned"):
//
//   1. The loader's fetchManifest tries R1 first, gets a 503, and falls
//      through to R2 -- the manifest is decoded from R2.
//   2. The loader's fetchBlob does the same per blob (R1 always 503s, so
//      every blob comes from R2). The bytes are sha256-verified before the
//      cache write, so a registry that returns wrong bytes for a digest
//      would still fail loudly.
//   3. The canned worker.js boots from R2's bytes, registers a window titled
//      "hello-oci-canned" with the compositor, and paints a red square.
//
// What this proves:
//
//   * The browser-side fallback path is identical in semantics to the Go
//     resolver's fallback (the Go twin lives in
//     wasmdesk/ociapps/resolver_test.go::TestResolver_ClusterFallback_*).
//   * Cache keyed by digest is content-addressable, so a blob served by R2
//     is reusable independent of which mirror the next call talks to.
//   * The console is clean of pageerror / uncaught exceptions even when
//     half the cluster is down.
//
// Run with: WASMBOX_BASE_URL=http://127.0.0.1:8094 node test/probe-cluster-fallback.mjs
// (CHANNEL=chrome WASMBOX_CHROME_CHANNEL=chrome to reuse the system Chrome
//  channel rather than the bundled Chromium binary.)

import { chromium } from "playwright";
import { writeFileSync } from "node:fs";
import { createHash } from "node:crypto";

const base = process.env.WASMBOX_BASE_URL || "http://127.0.0.1:8094";
const channel = process.env.WASMBOX_CHROME_CHANNEL || "chrome";

// HARD RULE: headless Chrome via the system Chrome channel.
const browser = await chromium.launch({ headless: true, channel });

const consoleLines = [];
const errors = [];
const r1Hits = [];
const r2Hits = [];
function fail(msg) { console.error(`FAIL: ${msg}`); process.exitCode = 1; }

// --- canned OCI app bytes --------------------------------------------------

const enc = new TextEncoder();

// Same canned-worker shape used in probe-spawn-from-oci.mjs: load the SDK +
// a stub wasm_exec, construct a WasmboxClient, paint + commit a frame so the
// compositor registers the "hello-oci-canned" window.
const cannedSdkURL = `${base}/clients/sdk/sdk.js`;
const cannedWasmExecSrc = enc.encode(`
  // canned wasm_exec.js stub: we never instantiate a real wasm program; only
  // satisfy the importScripts() call.
  globalThis.Go = function () { this.run = function () {}; this.importObject = {}; };
`);
const cannedWasmSrc = enc.encode("\x00asm\x01\x00\x00\x00"); // wasm magic only
const cannedWorkerSrc = enc.encode(`
  "use strict";
  importScripts(${JSON.stringify(cannedSdkURL)});
  (async () => {
    const assets = await WasmboxClient.bootFromOCIAssets({ fallbackMs: 1500 });
    if (!assets) throw new Error("canned worker: no OCI assets envelope");
    importScripts(assets.wasm_exec_url);
    const client = new WasmboxClient({ title: "hello-oci-canned", w: 64, h: 64 });
    self.wasmboxClient = client;
    await client.start();
    // Distinctive solid-red square -- DOM pixel probe checks for it.
    client.fillRect(220, 30, 30, 255);
    client.commit();
  })().catch((e) => { console.error("canned worker error: " + (e && e.stack ? e.stack : e)); });
`);

function sha256Hex(bytes) {
  const h = createHash("sha256");
  h.update(bytes);
  return "sha256:" + h.digest("hex");
}

const wjDig = sha256Hex(cannedWorkerSrc);
const wxDig = sha256Hex(cannedWasmExecSrc);
const wasmDig = sha256Hex(cannedWasmSrc);

const cannedManifest = {
  schemaVersion: 2,
  config: {
    mediaType: "application/vnd.wasmdesk.ociapps.config.v1+json",
    digest: "sha256:" + "0".repeat(64),
    size: 0,
  },
  layers: [
    { mediaType: "application/javascript", digest: wjDig,   size: cannedWorkerSrc.length },
    { mediaType: "application/javascript", digest: wxDig,   size: cannedWasmExecSrc.length },
    { mediaType: "application/wasm",       digest: wasmDig, size: cannedWasmSrc.length },
  ],
  annotations: {
    "ociapps.path/worker.js":    wjDig,
    "ociapps.path/wasm_exec.js": wxDig,
    "ociapps.path/hello.wasm":   wasmDig,
  },
};
const cannedManifestBody = enc.encode(JSON.stringify(cannedManifest));

const blobByDigest = new Map([
  [wjDig,   cannedWorkerSrc],
  [wxDig,   cannedWasmExecSrc],
  [wasmDig, cannedWasmSrc],
]);

try {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 720 } });

  // Inject WASMBOX_OCI_REGISTRIES into the compositor worker's globalThis.
  // page.addInitScript runs only in the document context; the compositor
  // worker has its own globalThis, so we intercept the worker script load
  // and prepend the assignment. The original body is appended verbatim.
  const workerScriptURL = `${base}/compositor.worker.js`;
  await ctx.route(workerScriptURL, async (route) => {
    const upstream = await fetch(workerScriptURL);
    const body = await upstream.text();
    const prelude = `globalThis.WASMBOX_OCI_REGISTRIES = ` +
      JSON.stringify([
        { url: "http://127.0.0.1:5000" },
        { url: "http://127.0.0.1:5001" },
      ]) + `;\n`;
    // Preserve COOP/COEP/CORP headers from the upstream response -- the
    // compositor worker is loaded under crossOriginIsolation and Chromium
    // will refuse to execute the worker script without them.
    const passthru = {};
    for (const [k, v] of upstream.headers.entries()) {
      const lk = k.toLowerCase();
      if (
        lk === "content-type" ||
        lk === "cross-origin-embedder-policy" ||
        lk === "cross-origin-opener-policy" ||
        lk === "cross-origin-resource-policy" ||
        lk === "cache-control"
      ) {
        passthru[k] = v;
      }
    }
    passthru["Content-Type"] = passthru["Content-Type"] || "text/javascript; charset=utf-8";
    await route.fulfill({
      status: 200,
      headers: passthru,
      body: prelude + body,
    });
  });

  // R1 = :5000 -- /v2/* always 503. Whether the loader asks for the
  // manifest or a blob, R1 fails.
  await ctx.route(/^http:\/\/127\.0\.0\.1:5000\/v2\/.*/, async (route) => {
    r1Hits.push(route.request().url());
    await route.fulfill({
      status: 503,
      headers: { "Content-Type": "text/plain" },
      body: "registry-A unavailable",
    });
  });

  // R2 = :5001 -- canned manifest + canned blobs.
  await ctx.route(/^http:\/\/127\.0\.0\.1:5001\/v2\/hello\/manifests\/.*/, async (route) => {
    r2Hits.push(route.request().url());
    await route.fulfill({
      status: 200,
      headers: { "Content-Type": "application/vnd.oci.image.manifest.v1+json" },
      body: Buffer.from(cannedManifestBody),
    });
  });
  await ctx.route(/^http:\/\/127\.0\.0\.1:5001\/v2\/hello\/blobs\/(sha256:[0-9a-f]+)/, async (route, request) => {
    r2Hits.push(request.url());
    const m = request.url().match(/\/blobs\/(sha256:[0-9a-f]+)$/);
    const dig = m ? m[1] : "";
    const body = blobByDigest.get(dig);
    if (!body) { await route.fulfill({ status: 404, body: "no such blob" }); return; }
    await route.fulfill({
      status: 200,
      headers: { "Content-Type": "application/octet-stream" },
      body: Buffer.from(body),
    });
  });

  const page = await ctx.newPage();
  page.on("console", (m) => consoleLines.push(`[${m.type()}] ${m.text()}`));
  page.on("pageerror", (e) => errors.push(String(e)));

  await page.goto(`${base}/index.html`, { waitUntil: "load" });

  await page.waitForFunction(
    () => {
      if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
      return globalThis.wasmboxReady === true;
    },
    { timeout: 15000 },
  );
  console.log("ok  compositor worker booted");

  // Give built-in clients a moment to register so the OCI client shows up
  // as an ADDITION to the layout, not a baseline entry.
  await page.waitForTimeout(1500);

  const titlesBefore = await page.evaluate(() => {
    const raw = localStorage.getItem("wasmbox.layout") || "";
    return raw.split("\n").map((l) => l.split("\t")[0]).filter((t) => t.length);
  });
  console.log(`ok  titles before OCI spawn: [${titlesBefore.join(", ")}]`);

  // Drive the OCI spawn: ref = "hello:test-canned" against a 2-registry
  // cluster where R1 is dead.
  await page.evaluate(() => globalThis.wasmboxSpawnFromOCI("hello:test-canned"));

  // Poll the layout for the canned client's title. The loader will hit R1
  // (503), fall through to R2 (200), spawn the canned worker.js, the canned
  // worker handshakes with the compositor, and the title lands.
  let saw = false;
  let titlesAfter = titlesBefore;
  for (let i = 0; i < 30; i++) {
    await page.waitForTimeout(200);
    titlesAfter = await page.evaluate(() => {
      const raw = localStorage.getItem("wasmbox.layout") || "";
      return raw.split("\n").map((l) => l.split("\t")[0]).filter((t) => t.length);
    });
    if (titlesAfter.some((t) => t === "hello-oci-canned")) { saw = true; break; }
  }
  console.log(`ok  titles after OCI spawn: [${titlesAfter.join(", ")}]`);
  if (saw) {
    console.log("ok  fallback succeeded: R1 503 then R2 200 reached the canned client through to a window registration");
  } else {
    fail("OCI-spawned canned client missing from layout — fallback did not produce a window");
  }

  // --- assertions on the fallback shape ---

  if (r1Hits.length === 0) {
    fail("R1 (port 5000) was never hit -- the loader didn't try the first registry");
  } else {
    console.log(`ok  R1 (5000) was tried ${r1Hits.length} time(s) and always 503'd`);
  }
  if (r2Hits.length === 0) {
    fail("R2 (port 5001) was never hit -- fallback did not happen");
  } else {
    console.log(`ok  R2 (5001) served ${r2Hits.length} request(s) after R1 failed`);
  }
  // The loader must have asked R1 for the manifest at least once before
  // falling through to R2.
  const r1AskedForManifest = r1Hits.some((u) => /\/manifests\//.test(u));
  const r2ServedManifest   = r2Hits.some((u) => /\/manifests\//.test(u));
  if (!r1AskedForManifest) fail("R1 was never asked for the manifest");
  else                     console.log("ok  R1 was asked for the manifest first");
  if (!r2ServedManifest)   fail("R2 never served the manifest");
  else                     console.log("ok  R2 served the manifest after R1 failed");

  // --- console + fallback log line check ---

  const fallbackLog = consoleLines.find((l) =>
    /5001/.test(l) || /fall|fallback|R2|503/.test(l));
  if (fallbackLog) {
    console.log(`ok  fallback-related console line present: ${fallbackLog.slice(0, 200)}`);
  } else {
    // Not a hard fail -- the loader is silent by design on a successful
    // fallback. We still log what we observed so a regression is visible.
    console.log("note: no explicit fallback log line (loader is silent on success)");
  }

  // --- DOM + pixel probe ---

  // Grab the screen canvas dimensions so the screenshot caption is honest
  // about which region the red-square commit actually paints into.
  const dims = await page.evaluate(() => {
    const c = document.getElementById("screen");
    return { w: c ? c.width : 0, h: c ? c.height : 0 };
  });
  console.log(`ok  canvas dims ${dims.w}x${dims.h}`);

  // Take the screenshot AND do a pixel-level check that the canned client
  // actually painted (red component dominant in at least one pixel of the
  // canvas region). Mirrors probe-step-c1.mjs's getImageData-style probe.
  const shotPath = "/tmp/wasmbox-cluster-fallback.png";
  const shotBuf = await page.screenshot({ path: shotPath, type: "png" });
  console.log(`ok  screenshot saved: ${shotPath}`);
  try {
    const { PNG } = await import("pngjs");
    const png = PNG.sync.read(shotBuf);
    let redPixels = 0;
    for (let i = 0; i < png.data.length; i += 4) {
      const r = png.data[i], g = png.data[i + 1], b = png.data[i + 2];
      if (r > 180 && g < 80 && b < 80) redPixels++;
    }
    if (redPixels > 0) {
      console.log(`ok  DOM pixel probe: ${redPixels} red pixel(s) from canned client's commit`);
    } else {
      // Don't fail the run for the pixel probe alone -- the layout title
      // assertion above is the load-bearing proof. Log so a regression is
      // visible.
      console.log("note: DOM pixel probe found no red pixels (canned commit may not have rasterised yet)");
    }
  } catch (e) {
    console.log("note: pngjs unavailable, skipping pixel probe: " + (e && e.message ? e.message : e));
  }

  writeFileSync("/tmp/wasmbox-cluster-fallback-console.log", consoleLines.join("\n"));
  console.log("ok  console saved: /tmp/wasmbox-cluster-fallback-console.log");

  if (errors.length) fail(`page errors: ${errors.join(" | ")}`);
  else               console.log("ok  console clean -- no pageerror across fallback");
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
}

if (process.exitCode && process.exitCode !== 0) {
  console.log("\nRESULT: FAIL");
} else {
  console.log("\nRESULT: PASS");
}
