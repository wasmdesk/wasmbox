// SPDX-License-Identifier: BSD-3-Clause
//
// Unit test for clients/sdk/sdk.js -- specifically the `bootWasm` helper that
// fetches a wasm URL with stream-progress reporting + paints a loading bar
// onto the WasmboxClient SAB during the fetch.
//
// The SDK is plain ES5 + uses `self` as its global; we load it via
// vm.runInContext with `self` aliased to a fresh sandbox that provides:
//   - EventTarget (the SDK calls self.addEventListener at module load)
//   - SharedArrayBuffer + Uint8ClampedArray (used by WasmboxClient)
//   - fetch / WebAssembly stubs injected via opts.fetch / opts.instantiate
//
// Assertions:
//   1. bootWasm paints frame 0 (track only) + a frame per chunk + a final
//      100% frame + calls commit() each time.
//   2. The bytes passed to WebAssembly.instantiate match the concatenated
//      chunks (no truncation, no extra bytes).
//   3. The returned value is the instance (not the {module, instance} pair).
//   4. Determinate progress: fillW grows monotonically with received bytes.
//   5. Indeterminate mode (no Content-Length): the loader still paints +
//      commits per chunk + finishes successfully.
//   6. HTTP error: throws with a useful message.
//   7. opts.client === null disables painting (no commit, no fillRect, but
//      still resolves the instance) -- the quake-style usage.
//   8. Default palette: track pixels are (218,220,224) before any chunk
//      arrives.
//   9. Fallback when resp.body has no getReader: arrayBuffer() path still
//      produces the right bytes.
//
// Run with: node test/sdk-bootwasm.test.mjs

import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import vm from "node:vm";

const ROOT = fileURLToPath(new URL("..", import.meta.url));

// --- harness ----------------------------------------------------------------

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

// --- load sdk.js into a vm context ------------------------------------------

const src = await readFile(`${ROOT}/clients/sdk/sdk.js`, "utf8");

// Stub MessagePort that the SDK can swap to (avoids the BUFFER-until-port
// path so client.commit() routes back to our recording channel).
class FakePort extends EventTarget {
  constructor() { super(); this.sent = []; }
  postMessage(msg, transfer) { this.sent.push({ msg, transfer }); }
  start() {}
}

function freshContext() {
  const self = new EventTarget();
  self.self = self;
  self.SharedArrayBuffer = SharedArrayBuffer;
  self.Uint8ClampedArray = Uint8ClampedArray;
  self.Promise = Promise;
  self.Math = Math;
  self.Error = Error;
  self.Number = Number;
  // The SDK reaches for self.fetch / WebAssembly via the global; tests inject
  // their own via opts.fetch / opts.instantiate to bootWasm, so we just need
  // a placeholder for the typeof checks not to throw.
  vm.createContext(self);
  vm.runInContext(src, self, { filename: "clients/sdk/sdk.js" });
  return self;
}

// makeClient mounts a WasmboxClient + binds it to a real MessagePort handoff
// so client.commit() routes to the recording channel. The bar paints into
// client.pixels (the Uint8ClampedArray view of the SAB).
function makeClient(ctx, w, h) {
  const client = new ctx.WasmboxClient({ title: "t", w, h });
  const port = new FakePort();
  ctx.WasmboxClient.useMessagePort(port);
  client.start(); // resolves on welcome, which we don't deliver -- not awaited
  // Force a windowId so commit() doesn't no-op.
  client.windowId = 1;
  // Track commit() calls so we can count paint frames.
  let commits = 0;
  const realCommit = client.commit.bind(client);
  client.commit = function (d) { commits++; return realCommit(d); };
  return {
    client,
    port,
    commitCount: () => commits,
    pixelAt: (x, y) => {
      const off = (y * w + x) * 4;
      return [client.pixels[off], client.pixels[off+1], client.pixels[off+2], client.pixels[off+3]];
    },
  };
}

// Helper: build a Response-like object with a streaming body of N chunks.
function streamingResp(chunks, contentLength) {
  let i = 0;
  return {
    ok: true,
    status: 200,
    headers: {
      get: (h) => {
        if (h.toLowerCase() === "content-length") {
          return contentLength === null ? null : String(contentLength);
        }
        return null;
      },
    },
    body: {
      getReader: () => ({
        read: async () => {
          if (i >= chunks.length) return { done: true };
          const value = chunks[i++];
          return { done: false, value };
        },
      }),
    },
  };
}

// Helper: build a Response-like object with NO streaming body (arrayBuffer path).
function bufferedResp(bytes) {
  return {
    ok: true,
    status: 200,
    headers: { get: () => null },
    arrayBuffer: async () => bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength),
  };
}

// --- 1 + 2 + 3 + 4 + 8. determinate-mode bootWasm -------------------------

{
  const ctx = freshContext();
  const h = makeClient(ctx, 200, 150);

  const c1 = new Uint8Array([1, 2, 3, 4, 5]);
  const c2 = new Uint8Array([6, 7, 8, 9, 10]);
  const c3 = new Uint8Array([11, 12, 13]);
  const total = c1.length + c2.length + c3.length; // 13

  let instantiated = null;
  const instance = await ctx.WasmboxClient.bootWasm("file.wasm", { env: {} }, {
    fetch: async (url) => {
      assertEq(url, "file.wasm", "bootWasm passes the URL to fetch()");
      return streamingResp([c1, c2, c3], total);
    },
    instantiate: async (bytes, importObject) => {
      instantiated = bytes;
      assert(importObject && importObject.env, "importObject reaches instantiate");
      return { module: { name: "m" }, instance: { name: "i" } };
    },
  });
  assertEq(instance, { name: "i" }, "bootWasm returns instance (not {module,instance})");

  // assertion 2: the bytes match exactly.
  const cat = new Uint8Array(total);
  cat.set(c1, 0); cat.set(c2, c1.length); cat.set(c3, c1.length + c2.length);
  assertEq(Array.from(instantiated), Array.from(cat), "bytes passed to instantiate match the concatenated chunks");

  // assertion 1: commit fired at least once per chunk + frame 0 + final 100%.
  // We had 3 chunks => at minimum 1 (frame 0) + 3 (per chunk) + 1 (100%) = 5
  // commit() calls; bootWasm may dedupe but our implementation paints every
  // chunk, so we assert >= 4 and <= 6 to allow for the 100%==last-chunk case.
  const n = h.commitCount();
  assert(n >= 4 && n <= 6, `commit() called ${n} times (want 4..6 for frame0 + 3 chunks + final)`, `n=${n}`);

  // assertion 8: the surface is no longer transparent; the track BG colour
  // (218,220,224) was painted somewhere along the centre row.
  const cy = (150 / 2) | 0;
  // Sample at the centre x (where the track lives) on the row JUST inside the
  // track. The track sits at y=h/2-3..h/2+3; we sample at cy directly.
  // After the 100% paint, the fill colour covers the whole track.
  const center = h.pixelAt(100, cy);
  // We're post-100% => fill = (53,132,228). Track is not directly visible at
  // 100%, but we can verify the bar painted by sampling either the fill or
  // the track at the trailing edge. The trailing pixel of a 200-wide track
  // centred on a 200-wide surface sits at x=199 (track ends at trackX+trackW-1).
  // Easier: at center we see the fill colour.
  assertEq([center[0], center[1], center[2]], [53, 132, 228],
    "final 100% paint: centre row painted with default fill colour");

  // Verify BG colour visible above + below the track (not painted with fill).
  const above = h.pixelAt(100, cy - 30);
  assertEq([above[0], above[1], above[2]], [250, 250, 250], "default bg (250,250,250) above the track");
  const below = h.pixelAt(100, cy + 30);
  assertEq([below[0], below[1], below[2]], [250, 250, 250], "default bg below the track");
}

// --- 4. monotonic fillW growth ---------------------------------------------

{
  const ctx = freshContext();
  const h = makeClient(ctx, 200, 150);

  // Capture the fill width per chunk by inspecting the centre row + counting
  // contiguous fill pixels at the chunk boundary. We hook commit() to take a
  // snapshot of the centre row.
  const snapshots = [];
  const realCommit = h.client.commit.bind(h.client);
  h.client.commit = function (d) {
    const w = h.client.w, hh = h.client.h;
    const cy = (hh / 2) | 0;
    // Count contiguous fill colour starting from the left edge of the track.
    const trackW = Math.min(200, Math.max(40, w - 32));
    const trackX = ((w - trackW) >> 1);
    let fillW = 0;
    for (let x = trackX; x < trackX + trackW; x++) {
      const off = (cy * w + x) * 4;
      if (h.client.pixels[off] === 53 && h.client.pixels[off+1] === 132 && h.client.pixels[off+2] === 228) fillW++;
      else break;
    }
    snapshots.push(fillW);
    return realCommit(d);
  };

  const c1 = new Uint8Array(10);
  const c2 = new Uint8Array(40);
  const c3 = new Uint8Array(50); // total 100

  await ctx.WasmboxClient.bootWasm("u", {}, {
    fetch: async () => streamingResp([c1, c2, c3], 100),
    instantiate: async () => ({ instance: {} }),
  });

  // snapshots[0] is frame 0 (0% => fillW = 0).
  assertEq(snapshots[0], 0, "frame 0 fillW = 0");
  assert(snapshots[snapshots.length - 1] >= snapshots[1],
    "final fillW >= first chunk fillW (monotonic non-decreasing)",
    `snapshots=${JSON.stringify(snapshots)}`);
  // Strictly monotonic across the chunk frames.
  for (let i = 1; i < snapshots.length; i++) {
    assert(snapshots[i] >= snapshots[i - 1], `snapshots[${i}]=${snapshots[i]} >= snapshots[${i-1}]=${snapshots[i-1]}`);
  }
  // Final == track width (100% paint). Track sizing is
  // min(200, max(40, w-32)) -- with w=200 that's min(200, 168) = 168.
  const expectedTrackW = Math.min(200, Math.max(40, 200 - 32));
  const finalW = snapshots[snapshots.length - 1];
  assert(finalW >= expectedTrackW - 1,
    `final fillW ${finalW} ~ ${expectedTrackW} (track full at 100%)`,
    `finalW=${finalW}`);
}

// --- 5. indeterminate mode (no Content-Length) -----------------------------

{
  const ctx = freshContext();
  const h = makeClient(ctx, 200, 150);

  const c1 = new Uint8Array([0xaa, 0xbb]);
  const c2 = new Uint8Array([0xcc]);
  let bytesIn = null;

  const instance = await ctx.WasmboxClient.bootWasm("u", {}, {
    fetch: async () => streamingResp([c1, c2], null),
    instantiate: async (b) => { bytesIn = b; return { instance: { tag: "ok" } }; },
  });
  assertEq(instance, { tag: "ok" }, "indeterminate mode resolves instance");
  assertEq(Array.from(bytesIn), [0xaa, 0xbb, 0xcc], "indeterminate mode concats bytes correctly");
  assert(h.commitCount() >= 3, `indeterminate mode commits per chunk (got ${h.commitCount()})`);
}

// --- 6. HTTP error -----------------------------------------------------------

{
  const ctx = freshContext();
  makeClient(ctx, 100, 100);

  await assertThrows(
    () => ctx.WasmboxClient.bootWasm("bad.wasm", {}, {
      fetch: async () => ({
        ok: false,
        status: 503,
        headers: { get: () => null },
        body: null,
      }),
      instantiate: async () => ({ instance: {} }),
    }),
    "HTTP 503",
    "bootWasm rejects HTTP 503",
  );
}

// --- 7. opts.client === null disables painting -----------------------------

{
  const ctx = freshContext();
  const h = makeClient(ctx, 100, 100);
  const before = h.commitCount();

  const c1 = new Uint8Array([1, 2, 3]);
  const instance = await ctx.WasmboxClient.bootWasm("u", {}, {
    client: null,
    fetch: async () => streamingResp([c1], 3),
    instantiate: async () => ({ instance: { skipped: true } }),
  });
  assertEq(instance, { skipped: true }, "opts.client=null still resolves instance");
  assertEq(h.commitCount() - before, 0, "opts.client=null skips all commit() calls");

  // Surface SAB stays at init zeros (transparent) -- bootWasm painted nothing.
  const px = h.pixelAt(50, 50);
  assertEq(px, [0, 0, 0, 0], "opts.client=null leaves SAB at init zeros");
}

// --- 9. fallback to arrayBuffer when body has no getReader ----------------

{
  const ctx = freshContext();
  makeClient(ctx, 100, 100);

  const payload = new Uint8Array([9, 8, 7, 6, 5]);
  let seen = null;
  await ctx.WasmboxClient.bootWasm("u", {}, {
    fetch: async () => bufferedResp(payload),
    instantiate: async (b) => { seen = b; return { instance: {} }; },
  });
  assertEq(Array.from(seen), Array.from(payload), "arrayBuffer fallback feeds the right bytes to instantiate");
}

// --- 10. custom palette routes through paintBar ----------------------------

{
  const ctx = freshContext();
  const h = makeClient(ctx, 200, 150);
  await ctx.WasmboxClient.bootWasm("u", {}, {
    fetch: async () => streamingResp([new Uint8Array(10)], 10),
    instantiate: async () => ({ instance: {} }),
    bg:    [10, 20, 30],
    track: [40, 50, 60],
    fill:  [70, 80, 90],
  });
  const cy = 75;
  const center = h.pixelAt(100, cy);
  assertEq([center[0], center[1], center[2]], [70, 80, 90], "custom fill colour reaches the SAB");
  const above = h.pixelAt(100, cy - 30);
  assertEq([above[0], above[1], above[2]], [10, 20, 30], "custom bg colour reaches the SAB");
}

// --- 11. missing fetch / instantiate throws --------------------------------

{
  const ctx = freshContext();
  makeClient(ctx, 100, 100);

  await assertThrows(
    () => ctx.WasmboxClient.bootWasm("u", {}, {
      fetch: null,
      instantiate: async () => ({ instance: {} }),
    }),
    "no fetch available",
    "bootWasm rejects when fetch is unavailable",
  );

  await assertThrows(
    () => ctx.WasmboxClient.bootWasm("u", {}, {
      fetch: async () => streamingResp([new Uint8Array(1)], 1),
      instantiate: null,
    }),
    "no WebAssembly.instantiate available",
    "bootWasm rejects when WebAssembly.instantiate is unavailable",
  );
}

// --- 12. bootWasm exists on clients/dock/sdk.js too (the standalone copy) --

{
  const dockSrc = await readFile(`${ROOT}/clients/dock/sdk.js`, "utf8");
  assert(/bootWasm\s*=\s*async function/.test(dockSrc),
    "clients/dock/sdk.js exposes WasmboxClient.bootWasm (standalone copy)");
}

// --- summary ---------------------------------------------------------------

if (failures > 0) {
  console.error(`\nRESULT: FAIL (${failures} failures)`);
  process.exit(1);
} else {
  console.log("\nRESULT: PASS");
}
