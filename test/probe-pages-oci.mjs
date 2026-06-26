// SPDX-License-Identifier: BSD-3-Clause
//
// Pages-delivery probe: prove the GitHub-Pages model end-to-end against a
// HEADER-LESS static server (cmd/serve -no-coi, simulating Pages):
//
//   1. coi-serviceworker.js earns cross-origin isolation with NO COOP/COEP
//      response headers -> self.crossOriginIsolated === true -> SharedArrayBuffer
//      is usable (the client surface needs it).
//   2. The compositor's OCIAppsLoader pulls a client app from the SAME-ORIGIN
//      static /v2 mirror (no CORS, no token, no proxy) and spawns it.
//
// Unlike probe-spawn-from-oci.mjs (which intercepts /v2 with canned bytes),
// this hits the REAL mirror written by ociapps-static and served beside the
// page -- the actual production path. Point it at `task serve:pages`:
//
//   WASMBOX_BASE_URL=http://localhost:8137 node test/probe-pages-oci.mjs

import { chromium } from "playwright";
import { writeFileSync } from "node:fs";

const base = (process.env.WASMBOX_BASE_URL || "http://localhost:8137").replace(/\/$/, "");
const channel = process.env.WASMBOX_CHROME_CHANNEL;

const browser = await chromium.launch(channel ? { headless: true, channel } : { headless: true });
const consoleLines = [];
const errors = [];
const v2Hits = []; // [{url, status}]
function fail(msg) { console.error(`FAIL: ${msg}`); process.exitCode = 1; }

try {
  const ctx = await browser.newContext(); // serviceWorkers default 'allow'
  const page = await ctx.newPage();
  page.on("console", (m) => consoleLines.push(`[${m.type()}] ${m.text()}`));
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("response", (r) => {
    const u = r.url();
    if (u.includes("/v2/")) v2Hits.push({ url: u.replace(base, ""), status: r.status() });
  });

  // ?ociapps=1&ociapps_only=hello -> boot-config auto-spawns hello:latest from
  // the same-origin registry once the compositor is ready.
  await page.goto(`${base}/?ociapps=1&ociapps_only=hello`, { waitUntil: "load" });

  // coi-serviceworker.js reloads the page once on first visit so the now-
  // controlling worker can inject COOP/COEP. Poll across that reload for the
  // isolation flag (evaluate can transiently throw mid-navigation).
  let isolated = false;
  for (let i = 0; i < 80; i++) {
    try {
      isolated = await page.evaluate(() => self.crossOriginIsolated === true);
    } catch (_) { /* context torn down by the SW reload; retry */ }
    if (isolated) break;
    await page.waitForTimeout(250);
  }
  if (isolated) console.log("ok  crossOriginIsolated === true on a HEADER-LESS server (coi-serviceworker worked)");
  else fail("crossOriginIsolated never became true — coi-serviceworker did not establish isolation");

  // SharedArrayBuffer must actually be constructible (what the client surface needs).
  const sabOK = await page.evaluate(() => {
    try { return typeof SharedArrayBuffer !== "undefined" && new SharedArrayBuffer(8).byteLength === 8; }
    catch (_) { return false; }
  });
  if (sabOK) console.log("ok  SharedArrayBuffer is constructible");
  else fail("SharedArrayBuffer not usable despite crossOriginIsolated");

  // Compositor ready.
  await page.waitForFunction(() => {
    if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
    return globalThis.wasmboxReady === true;
  }, { timeout: 20000 });
  console.log("ok  compositor worker booted");

  // Give boot-config's auto-spawn time to pull hello from the same-origin /v2.
  let landed = false;
  let titles = [];
  for (let i = 0; i < 40; i++) {
    await page.waitForTimeout(200);
    titles = await page.evaluate(() => {
      const raw = localStorage.getItem("wasmbox.layout") || "";
      return raw.split("\n").map((l) => l.split("\t")[0]).filter((t) => t.length);
    });
    if (titles.some((t) => /hello/i.test(t))) { landed = true; break; }
  }
  console.log(`ok  layout titles: [${titles.join(", ")}]`);

  // Hard assertion: the manifest + at least one blob were pulled from the
  // SAME-ORIGIN /v2 mirror with 200s.
  const manifestOK = v2Hits.some((h) => /\/v2\/hello\/manifests\/latest$/.test(h.url) && h.status === 200);
  const blobOK = v2Hits.some((h) => /\/v2\/hello\/blobs\/sha256:[0-9a-f]+$/.test(h.url) && h.status === 200);
  console.log(`ok  same-origin /v2 hits: ${v2Hits.map((h) => h.status + " " + h.url).join("  ")}`);
  if (manifestOK) console.log("ok  manifest pulled from same-origin /v2 (200)");
  else fail("no successful same-origin /v2 manifest fetch");
  if (blobOK) console.log("ok  blob pulled from same-origin /v2 by sha256: digest (200)");
  else fail("no successful same-origin /v2 blob fetch");

  if (landed) console.log("ok  hello client landed in the WM (spawned from the same-origin OCI mirror)");
  else fail("hello client did not appear in the layout");

  await page.screenshot({ path: "/tmp/wasmbox-pages-verified.png", type: "png" });
  console.log("ok  screenshot: /tmp/wasmbox-pages-verified.png");
  writeFileSync("/tmp/wasmbox-pages-console.log", consoleLines.join("\n"));

  const bad = consoleLines.filter((l) => /digest mismatch|ociapps:.*(fail|error)|spawn\(.*\):/i.test(l));
  if (bad.length) fail(`OCI error lines in console: ${bad.join(" | ")}`);
  if (errors.length) fail(`page errors: ${errors.join(" | ")}`);
  else console.log("ok  no pageerror");
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
