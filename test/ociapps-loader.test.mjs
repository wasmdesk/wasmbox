// SPDX-License-Identifier: BSD-3-Clause
//
// Unit test for ociapps-loader.js. The loader is a vanilla JS file with no
// build step, so we load it via vm.runInContext with a stubbed `self` that
// carries:
//
//   - a mock fetch() that the test programs per call
//   - WebCrypto's SubtleCrypto (real, from node:crypto.webcrypto) so the
//     sha256 digest verification is exercised end-to-end (not stubbed)
//
// The loader's IndexedDB cache is bypassed by passing in a fresh
// OCIAppsLoader.MemoryCache(), keeping the test deterministic + free of
// any node IDB shim.
//
// Assertions:
//
//   1. parseRef edge cases (empty, missing tag).
//   2. decodeManifest rejects schemaVersion != 2.
//   3. buildFileMap returns name -> digest pairs for ociapps.path/* annotations.
//   4. fetchManifest falls back through registries on transport / decode errors.
//   5. fetchBlob falls back through registries AND short-circuits on cache hit.
//   6. fetchBlob refuses a registry that returns the wrong bytes for a digest.
//   7. loadApp end-to-end pulls every annotated blob + populates files.
//
// Run with: node test/ociapps-loader.test.mjs

import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { webcrypto } from "node:crypto";
import vm from "node:vm";

const ROOT = fileURLToPath(new URL("..", import.meta.url));

// --- harness ---------------------------------------------------------------

let failures = 0;
function ok(label) { console.log("ok  " + label); }
function fail(label, detail) {
  failures++;
  console.error("FAIL " + label + (detail ? ": " + detail : ""));
}
function assertEq(actual, expected, label) {
  const a = JSON.stringify(actual), e = JSON.stringify(expected);
  if (a === e) ok(label); else fail(label, "expected " + e + " got " + a);
}
function assert(cond, label, detail) {
  if (cond) ok(label); else fail(label, detail || "(no detail)");
}
async function assertThrows(fn, msgPart, label) {
  try { await fn(); fail(label, "no throw"); return; }
  catch (e) {
    const s = String(e && e.message ? e.message : e);
    if (s.includes(msgPart)) ok(label);
    else fail(label, "wanted substring " + JSON.stringify(msgPart) + " in " + s);
  }
}

// --- load the loader into a vm context -------------------------------------

const src = await readFile(`${ROOT}/ociapps-loader.js`, "utf8");

function freshContext(fetchFn) {
  const self = {
    crypto: webcrypto,
    indexedDB: null, // force the loader to use the MemoryCache fallback
  };
  self.self = self;
  self.fetch = fetchFn;
  vm.createContext(self);
  vm.runInContext(src, self, { filename: "ociapps-loader.js" });
  return self;
}

// Helper: sha256(bytes) -> "sha256:<hex>" using webcrypto on the host.
async function sha256Hex(bytes) {
  const d = await webcrypto.subtle.digest("SHA-256", bytes);
  const u8 = new Uint8Array(d);
  let s = "";
  for (const b of u8) s += (b < 16 ? "0" : "") + b.toString(16);
  return "sha256:" + s;
}

function strBytes(s) { return new TextEncoder().encode(s); }

// --- 1. parseRef + decodeManifest + buildFileMap edge cases ----------------

{
  const ctx = freshContext(async () => { throw new Error("no fetch"); });
  const L = ctx.OCIAppsLoader;

  assertEq(L.parseRef("hello:latest"), { repo: "hello", tag: "latest" }, "parseRef hello:latest");
  assertEq(L.parseRef("hello"),        { repo: "hello", tag: "latest" }, "parseRef hello (default tag)");
  try { L.parseRef(""); fail("parseRef empty throws"); }
  catch (e) { ok("parseRef empty throws: " + e.message); }
  try { L.parseRef(":"); fail("parseRef colon-only throws"); }
  catch (e) { ok("parseRef colon-only throws: " + e.message); }

  try { L.decodeManifest(JSON.stringify({ schemaVersion: 1 })); fail("decodeManifest v1 throws"); }
  catch (e) { ok("decodeManifest v1 throws: " + e.message); }
  const okM = L.decodeManifest(JSON.stringify({ schemaVersion: 2 }));
  assertEq(okM.schemaVersion, 2, "decodeManifest v2 parses");

  // buildFileMap: only ociapps.path/* entries survive, blanks dropped.
  const fm = L.buildFileMap({
    annotations: {
      "ociapps.path/worker.js":    "sha256:aaa",
      "ociapps.path/wasm_exec.js": "sha256:bbb",
      "ociapps.path/app.wasm":     "sha256:ccc",
      "org.opencontainers.image.title": "ignored",
      "ociapps.path/empty":        "",
      "ociapps.path/":             "should-skip-empty-name",
    },
  });
  assertEq(fm.get("worker.js"),    "sha256:aaa", "buildFileMap worker.js");
  assertEq(fm.get("wasm_exec.js"), "sha256:bbb", "buildFileMap wasm_exec.js");
  assertEq(fm.get("app.wasm"),     "sha256:ccc", "buildFileMap app.wasm");
  assertEq(fm.has("empty"),        false,        "buildFileMap drops empty digest");
  assertEq(fm.size,                3,            "buildFileMap size = 3");

  try { L.buildFileMap({ annotations: {} }); fail("buildFileMap empty throws"); }
  catch (e) { ok("buildFileMap empty throws: " + e.message); }
}

// --- 2. multi-registry fallback on manifest --------------------------------

{
  let calls = [];
  const ctx = freshContext(async (u) => {
    calls.push(u);
    if (u.startsWith("https://r1/")) return { ok: false, status: 500, text: async () => "" };
    if (u.startsWith("https://r2/")) return { ok: true,  status: 200, text: async () => JSON.stringify({
      schemaVersion: 2, layers: [], annotations: { "ociapps.path/x": "sha256:zz" },
    }) };
    throw new Error("unexpected: " + u);
  });
  const L = ctx.OCIAppsLoader;
  const loader = new L([{ url: "https://r1" }, { url: "https://r2" }],
    { cache: new L.MemoryCache() });
  const { registry } = await loader.fetchManifest("hello", "latest");
  assertEq(registry.url, "https://r2", "fetchManifest falls back to r2 on r1 5xx");
  assert(calls[0].startsWith("https://r1/v2/hello/manifests/latest"), "tried r1 first");
  assert(calls[1].startsWith("https://r2/v2/hello/manifests/latest"), "then tried r2");
}

// --- 3. multi-registry fallback + cache on blobs ---------------------------

{
  const body = strBytes("hello world");
  const digest = await sha256Hex(body);
  let r1Calls = 0, r2Calls = 0;

  const ctx = freshContext(async (u) => {
    if (u.startsWith("https://r1/v2/hello/blobs/")) { r1Calls++; return { ok: false, status: 500 }; }
    if (u.startsWith("https://r2/v2/hello/blobs/")) {
      r2Calls++;
      return { ok: true, status: 200, arrayBuffer: async () => body.buffer.slice(body.byteOffset, body.byteOffset + body.byteLength) };
    }
    throw new Error("unexpected: " + u);
  });
  const L = ctx.OCIAppsLoader;
  const cache = new L.MemoryCache();
  const loader = new L([{ url: "https://r1" }, { url: "https://r2" }], { cache });

  const b1 = await loader.fetchBlob("hello", digest);
  assertEq(b1.length, body.length, "fetchBlob returns correct length");
  assertEq(r1Calls, 1, "fetchBlob hit r1 once (failed)");
  assertEq(r2Calls, 1, "fetchBlob hit r2 once (succeeded)");

  // Second call: cache HIT, no further network.
  const b2 = await loader.fetchBlob("hello", digest);
  assertEq(b2.length, body.length, "fetchBlob cache returns correct length");
  assertEq(r1Calls, 1, "fetchBlob cache hit: r1 not called again");
  assertEq(r2Calls, 1, "fetchBlob cache hit: r2 not called again");
}

// --- 4. fetchBlob rejects digest mismatch ----------------------------------

{
  const expectedBytes = strBytes("expected");
  const expectedDigest = await sha256Hex(expectedBytes);
  const wrongBytes = strBytes("WRONG");
  const ctx = freshContext(async (u) => {
    if (u.includes("/blobs/")) {
      return { ok: true, status: 200, arrayBuffer: async () => wrongBytes.buffer.slice(wrongBytes.byteOffset, wrongBytes.byteOffset + wrongBytes.byteLength) };
    }
    throw new Error("unexpected");
  });
  const L = ctx.OCIAppsLoader;
  const loader = new L([{ url: "https://r1" }], { cache: new L.MemoryCache() });
  await assertThrows(
    () => loader.fetchBlob("hello", expectedDigest),
    "digest mismatch",
    "fetchBlob rejects wrong bytes",
  );
}

// --- 5. loadApp end-to-end -------------------------------------------------

{
  const wjBytes = strBytes("// worker.js");
  const wxBytes = strBytes("// wasm_exec.js");
  const appBytes = strBytes("\x00asm\x01\x00\x00\x00"); // wasm magic prefix
  const wjDig = await sha256Hex(wjBytes);
  const wxDig = await sha256Hex(wxBytes);
  const appDig = await sha256Hex(appBytes);
  const manifest = {
    schemaVersion: 2,
    config: { mediaType: "application/vnd.wasmdesk.ociapps.config.v1+json", digest: "sha256:cfg", size: 0 },
    layers: [
      { mediaType: "application/javascript", digest: wjDig, size: wjBytes.length },
      { mediaType: "application/javascript", digest: wxDig, size: wxBytes.length },
      { mediaType: "application/wasm",       digest: appDig, size: appBytes.length },
    ],
    annotations: {
      "ociapps.path/worker.js":    wjDig,
      "ociapps.path/wasm_exec.js": wxDig,
      "ociapps.path/hello.wasm":   appDig,
    },
  };
  const bodyOf = (digest) => {
    const map = { [wjDig]: wjBytes, [wxDig]: wxBytes, [appDig]: appBytes };
    const b = map[digest];
    return b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength);
  };
  const ctx = freshContext(async (u) => {
    if (u.endsWith("/v2/hello/manifests/test-canned")) {
      return { ok: true, status: 200, text: async () => JSON.stringify(manifest) };
    }
    const m = u.match(/\/v2\/hello\/blobs\/(sha256:[0-9a-f]+)$/);
    if (m) return { ok: true, status: 200, arrayBuffer: async () => bodyOf(m[1]) };
    throw new Error("unexpected: " + u);
  });
  const L = ctx.OCIAppsLoader;
  const loader = new L([{ url: "https://r" }], { cache: new L.MemoryCache() });
  const app = await loader.loadApp("hello:test-canned");
  assertEq(app.files.size, 3, "loadApp returns 3 files");
  assert(app.files.has("worker.js"),    "loadApp worker.js present");
  assert(app.files.has("wasm_exec.js"), "loadApp wasm_exec.js present");
  assert(app.files.has("hello.wasm"),   "loadApp hello.wasm present");
  assertEq(app.files.get("worker.js").length, wjBytes.length, "loadApp worker.js length");
}

// --- 6. empty registries throws --------------------------------------------

{
  const ctx = freshContext(async () => { throw new Error("no"); });
  const L = ctx.OCIAppsLoader;
  try { new L([]); fail("empty registries throws"); }
  catch (e) { ok("empty registries throws: " + e.message); }
}

// --- summary ---------------------------------------------------------------

if (failures) {
  console.error(`\nRESULT: FAIL (${failures} failures)`);
  process.exit(1);
} else {
  console.log("\nRESULT: PASS");
}
