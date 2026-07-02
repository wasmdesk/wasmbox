// SPDX-License-Identifier: BSD-3-Clause
//
// Headless-browser probe for wasmboxSpawnFromOCI. Strategy:
//
//   1. Boot wasmbox normally on the project's cmd/serve.
//   2. Intercept any GET /v2/<repo>/manifests/<tag> + GET /v2/<repo>/blobs/<digest>
//      requests via page.route() with canned bytes for hello:test-canned. The
//      canned worker.js is a tiny WasmboxClient that paints a solid red square
//      + calls commit(), so the spawn produces a visible artefact on the
//      desktop. NO live registry is contacted in this probe (B3 owns that).
//   3. Drive globalThis.wasmboxSpawnFromOCI("hello:test-canned") from the
//      page.
//   4. Assert a new external client landed in the WM (its title appears in
//      the persisted layout, like the step-C.1 probe does for static spawns).
//   5. Save /tmp/wasmbox-b2-verified.png + the console.
//
// What this proves:
//   * The bridge constant + the relay in index.html dispatch into the
//     compositor worker.
//   * The compositor worker's OCI loader fetches manifest + blobs, wraps them
//     in blob URLs, and spawns a Worker from the worker.js blob URL.
//   * The new worker boots, awaits the OCI assets envelope, importScripts
//     wasm_exec.js from the blob URL, and runs the client SDK handshake (=>
//     hello -> welcome -> commit), proving the per-client MessageChannel
//     still works on the OCI spawn path.

import { chromium } from "playwright";
import { writeFileSync } from "node:fs";
import { createHash } from "node:crypto";

const base = process.env.WASMBOX_BASE_URL || "http://127.0.0.1:8094";
const channel = process.env.WASMBOX_CHROME_CHANNEL;

const browser = await chromium.launch(
  channel ? { headless: true, channel } : { headless: true },
);

const consoleLines = [];
const errors = [];
function fail(msg) { console.error(`FAIL: ${msg}`); process.exitCode = 1; }

// --- canned OCI app bytes --------------------------------------------------

const enc = new TextEncoder();

// The canned worker.js is intentionally tiny: it awaits the OCI assets
// envelope, importScripts the SDK + wasm_exec.js (the SDK from a relative URL
// because both static and OCI spawns have the SDK shipped in the asset tree
// next to the compositor; only wasm_exec.js + the wasm itself are blobbed),
// fakes a WasmboxClient handshake without a real wasm program (it has no
// .wasm to instantiate), and posts a hello so the WM registers the window.
//
// Note: importScripts is allowed to use absolute URLs from inside a worker
// regardless of the worker's own script URL (blob: in our case). We pull the
// SDK from `${base}/clients/sdk/sdk.js` so the canned worker has zero coupling
// to the compositor asset tree.
const cannedSdkURL = `${base}/clients/sdk/sdk.js`;
const cannedWasmExecSrc = enc.encode(`
  // canned wasm_exec.js stub: the canned worker never instantiates a real
  // wasm program, so we only need to satisfy the importScripts() call.
  globalThis.Go = function () { this.run = function () {}; this.importObject = {}; };
`);
const cannedWasmSrc = enc.encode("\x00asm\x01\x00\x00\x00"); // wasm magic only
const cannedWorkerSrc = enc.encode(`
  "use strict";
  // 1. Load the SDK from an absolute URL (decoupled from spawn path).
  importScripts(${JSON.stringify(cannedSdkURL)});

  (async () => {
    // 2. Await the OCI assets envelope (compositor sent it as message #1).
    const assets = await WasmboxClient.bootFromOCIAssets({ fallbackMs: 1500 });
    if (!assets) throw new Error("canned worker: no OCI assets envelope");
    // 3. importScripts the (stubbed) wasm_exec from the blob URL.
    importScripts(assets.wasm_exec_url);
    // 4. Construct + start a WasmboxClient. The compositor will register a
    //    window for "hello-oci-canned" so the probe can detect it.
    const client = new WasmboxClient({ title: "hello-oci-canned", w: 64, h: 64 });
    self.wasmboxClient = client;
    await client.start();
    // 5. Paint a solid red square + commit one frame.
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
  config: { mediaType: "application/vnd.wasmdesk.ociapps.config.v1+json", digest: "sha256:0".repeat(64).replace(/./, "0"), size: 0 },
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
  const ctx = await browser.newContext();
  // The OCI loader inside the compositor worker calls fetch() against a
  // /v2/hello/manifests/test-canned + .../blobs/<digest> endpoint. It resolves
  // these against a same-origin /v2 mirror (the page origin), so we match the
  // /v2 path on ANY origin rather than hardcoding a registry port. context.route
  // catches both main-thread + worker requests in Chromium.
  await ctx.route(/\/v2\/hello\/manifests\/.*/, async (route) => {
    await route.fulfill({
      status: 200,
      headers: { "Content-Type": "application/vnd.oci.image.manifest.v1+json" },
      body: Buffer.from(cannedManifestBody),
    });
  });
  await ctx.route(/\/v2\/hello\/blobs\/sha256:[0-9a-f]+/, async (route, request) => {
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

  // Wait for the compositor to be ready.
  await page.waitForFunction(
    () => {
      if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
      return globalThis.wasmboxReady === true;
    },
    { timeout: 15000 },
  );
  console.log("ok  compositor worker booted");

  // Give the built-in clients a moment to register so their titles are
  // already in the layout (we want to detect the OCI client as ADDED).
  await page.waitForTimeout(1500);

  // Read the title set BEFORE the OCI spawn.
  const titlesBefore = await page.evaluate(() => {
    const raw = localStorage.getItem("wasmbox.layout") || "";
    return raw.split("\n").map((l) => l.split("\t")[0]).filter((t) => t.length);
  });
  console.log(`ok  titles before OCI spawn: [${titlesBefore.join(", ")}]`);

  // Drive the OCI spawn from the page. This goes:
  //   page.wasmboxSpawnFromOCI(ref)
  //     -> main posts M2C_SPAWN_FROM_OCI to the compositor worker
  //         -> compositor calls wasmboxSpawnExternalOCI(ref)
  //             -> dispatches CustomEvent on the Ruby bus
  //                 -> compositor.rb's bus listener runs spawn_external_oci(ref)
  //                     -> wasmboxSpawnFromOCIAndAttach(ref, busId)
  //                         -> loader.loadApp(ref) [hits our route intercepts]
  //                         -> spawn Worker from blob URL of worker.js
  //                         -> post assets envelope + port handoff
  //                             -> canned worker.js boots, awaits assets,
  //                                  imports SDK + (stubbed) wasm_exec,
  //                                  constructs WasmboxClient,
  //                                  posts hello -> compositor welcomes,
  //                                  paints + commits one frame.
  //                             -> "hello-oci-canned" lands in layout.
  await page.evaluate(() => globalThis.wasmboxSpawnFromOCI("hello:test-canned"));

  // Wait for the canned client to handshake. Poll for its title to appear in
  // the layout (the welcome path writes it through the compositor's tick()).
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
    console.log("ok  OCI-spawned canned client completed handshake via blob-URL worker + MessagePort");
  } else {
    fail("OCI-spawned canned client missing from layout — wasmboxSpawnFromOCI did not produce a window");
  }

  // Capture the verification screenshot.
  const shotPath = "/tmp/wasmbox-b2-verified.png";
  await page.screenshot({ path: shotPath, type: "png" });
  console.log(`ok  screenshot saved: ${shotPath}`);

  // Save the page console for diagnostics.
  writeFileSync("/tmp/wasmbox-b2-console.log", consoleLines.join("\n"));
  console.log("ok  console saved: /tmp/wasmbox-b2-console.log");

  if (errors.length) fail(`page errors: ${errors.join(" | ")}`);
  else               console.log("ok  no pageerror");
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
