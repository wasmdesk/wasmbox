// SPDX-License-Identifier: BSD-3-Clause
//
// Deterministic unit test for the surface seqlock fence (docs/protocol.md →
// "Tear-free presentation"). It replicates the exact Atomics protocol used by
// the SDK writer (clients/sdk/sdk.js) and the compositor reader
// (compositor.worker.js → wasmboxBlitFromSAB) and proves the invariant:
//
//   the reader NEVER presents a torn (mixed-frame) surface — it either skips
//   the frame or presents a fully-consistent one.
//
// Single-threaded + deterministic: we drive the interleavings explicitly
// (reader called while the writer is mid-paint, and writer sneaking a write
// during the reader's copy) rather than racing real threads.
//
// Run:  node test/seqlock.test.mjs

let failures = 0;
function check(cond, msg) {
  if (cond) { console.log("ok  " + msg); }
  else      { console.error("FAIL: " + msg); failures++; }
}

// A surface of N pixels (1 byte each here, for simplicity) + a seqlock control
// word, both in SharedArrayBuffers — exactly the buffers `hello` carries.
class Surface {
  constructor(n) {
    this.px = new Uint8Array(new SharedArrayBuffer(n));
    this.seq = new Int32Array(new SharedArrayBuffer(4));
    this.n = n;
  }
  // --- writer (mirrors the SDK) ---
  _beginPaint() { if ((Atomics.load(this.seq, 0) & 1) === 0) Atomics.add(this.seq, 0, 1); }
  paintPixel(i, v) { this._beginPaint(); this.px[i] = v; }
  commit() { if ((Atomics.load(this.seq, 0) & 1) === 1) Atomics.add(this.seq, 0, 1); }

  // --- reader (mirrors wasmboxBlitFromSAB) ---
  // Returns the copied frame if it can be presented, or null if skipped.
  // midCopyHook (optional) runs between the two seq loads, simulating the
  // client writing during the reader's copy.
  tryPresent(midCopyHook) {
    const s1 = Atomics.load(this.seq, 0);
    if (s1 & 1) return null;                 // client mid-paint -> skip
    const copy = this.px.slice();            // the "copy into private ImageData"
    if (midCopyHook) midCopyHook();
    if (Atomics.load(this.seq, 0) !== s1) return null; // torn during copy -> drop
    return copy;
  }
}

const consistent = (frame) => frame.every((v) => v === frame[0]);

// 1. A complete frame is presented and consistent.
{
  const s = new Surface(8);
  for (let i = 0; i < 8; i++) s.paintPixel(i, 42);
  s.commit();
  const f = s.tryPresent();
  check(f !== null && consistent(f) && f[0] === 42, "complete frame presented + consistent");
}

// 2. Reader during an in-progress paint (seq odd) MUST skip.
{
  const s = new Surface(8);
  for (let i = 0; i < 8; i++) s.paintPixel(i, 7); s.commit();   // frame 7 done
  s.paintPixel(0, 9);            // start frame 9 (seq now odd), only pixel 0 written
  const f = s.tryPresent();      // reader sees odd
  check(f === null, "reader skips while client mid-paint (seq odd)");
  // finish + commit -> now presentable + consistent
  for (let i = 1; i < 8; i++) s.paintPixel(i, 9);
  s.commit();
  const f2 = s.tryPresent();
  check(f2 !== null && consistent(f2) && f2[0] === 9, "next read after commit is the complete new frame");
}

// 3. Client writes DURING the reader's copy (seq changes) -> reader drops it.
{
  const s = new Surface(8);
  for (let i = 0; i < 8; i++) s.paintPixel(i, 5); s.commit();   // frame 5 done (seq even)
  const f = s.tryPresent(() => {
    // a new frame starts mid-copy: this is the torn case
    s.paintPixel(0, 6);
  });
  check(f === null, "reader drops a copy torn by a mid-copy write (seq changed)");
}

// 4. Fuzz: across many random interleavings, every PRESENTED frame is
//    internally consistent (never torn).
{
  const s = new Surface(16);
  for (let i = 0; i < 16; i++) s.paintPixel(i, 1); s.commit();
  let presented = 0, torn = 0;
  // pseudo-random but seeded (no Math.random dependence)
  let r = 12345; const rnd = () => (r = (r * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff;
  for (let frame = 2; frame < 400; frame++) {
    const k = 1 + Math.floor(rnd() * 16);     // paint a random prefix of pixels...
    for (let i = 0; i < k; i++) s.paintPixel(i, frame);
    // ...possibly try to read mid-paint (before finishing/committing)
    if (rnd() < 0.5) { if (s.tryPresent() !== null) presented++; }  // (will skip: seq odd)
    for (let i = k; i < 16; i++) s.paintPixel(i, frame);
    s.commit();
    const f = s.tryPresent(rnd() < 0.3 ? () => { s.paintPixel(0, frame + 1000); } : null);
    if (f !== null) { presented++; if (!consistent(f)) torn++; }
  }
  check(torn === 0, `fuzz: 0 torn frames among ${presented} presented (over 398 frames)`);
}

console.log(failures ? `\nRESULT: FAIL (${failures})` : "\nRESULT: PASS");
process.exit(failures ? 1 : 0);
