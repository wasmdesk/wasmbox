// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Chromium e2e for the audio relay (docs/protocol.md, "Audio").
//
// A client worker (clients/audiotest) writes a tone into a SharedArrayBuffer
// ring; the compositor worker forwards the ring to the page; the page's
// AudioContext plays it through an AudioWorklet that reads the ring on the
// audio thread. Asserted:
//
//   POSITIVE  tone client
//     1. a session reaches the page and gets an AudioWorkletNode (attached)
//     2. AudioContext.state === "running" after ONE synthetic gesture
//        (the context is NOT running before it unless Chromium says otherwise)
//     3. the worklet played > 0 frames, and about 2 s worth, in a 2 s window
//     4. underruns over that window stay under UNDERRUN_BOUND
//   PLANTED NEGATIVE  same client, ring writer disabled (?nowrite=1)
//     the SAME assertions must FAIL (played stays 0). If they pass, the test
//     is blind and this script fails.
//   HOSTILE  a client that sends malformed audio_open (wrong SAB size, rate
//     1e9, 99 channels, a non-power-of-two ring): no session may be created,
//     in the compositor worker or the page.
//
// Env: WASMBOX_BASE_URL (default http://127.0.0.1:8137), UNDERRUN_BOUND (5),
// AUDIO_WINDOW_MS (2000). Screenshots, if any, go to the OS temp dir, never a repo.

import * as pw from "playwright";
import os from "node:os";

const BASE = (process.env.WASMBOX_BASE_URL || "http://127.0.0.1:8137").replace(/\/$/, "");
const UNDERRUN_BOUND = Number(process.env.UNDERRUN_BOUND || 5);
const WINDOW_MS = Number(process.env.AUDIO_WINDOW_MS || 2000);
const wait = (ms) => new Promise((r) => setTimeout(r, ms));
const BOOT_MS = 60000;
// AUDIO_CLIENT: tone (clients/audiotest, default), quake or doom (the real
// engines, which need their asset mirror served: see the quake-smoke and
// doom-smoke jobs). A real engine boots slowly, so wait for its first samples.
const CLIENT = process.env.AUDIO_CLIENT || "tone";
const CLIENT_URL = { tone: "clients/audiotest/worker.js", quake: "clients/quake/worker.js", doom: "clients/doom/worker.js" }[CLIENT];
if (!CLIENT_URL) throw new Error(`AUDIO_CLIENT must be tone, quake or doom, got ${CLIENT}`);
const REAL = CLIENT !== "tone";
const FIRST_SAMPLES_MS = Number(process.env.AUDIO_FIRST_SAMPLES_MS || 120000);

// ev evaluates WITHOUT a user gesture. Playwright's page.evaluate runs with
// userGesture:true, which would activate the page and let the AudioContext
// start by itself, making the "running after a gesture" assertion vacuous.
const cdps = new WeakMap();
async function ev(p, expression) {
  let s = cdps.get(p);
  if (!s) { s = await p.context().newCDPSession(p); cdps.set(p, s); }
  const r = await s.send("Runtime.evaluate", { expression, returnByValue: true, userGesture: false });
  if (r.exceptionDetails) throw new Error(r.exceptionDetails.text + " " + JSON.stringify(r.exceptionDetails.exception || {}));
  return r.result.value;
}
async function until(p, expression, ms) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    try { if (await ev(p, expression)) return true; } catch (_) {}
    await wait(100);
  }
  return false;
}

async function boot(browser, clientURL) {
  const p = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  const errs = [];
  p.on("pageerror", (e) => errs.push(String(e)));
  await p.goto(`${BASE}/index.html`, { waitUntil: "load", timeout: BOOT_MS });
  if (!(await until(p, "globalThis.wasmboxReady === true", BOOT_MS))) throw new Error("compositor did not boot");
  await wait(1000);
  await ev(p, `globalThis.wasmboxSpawnExternal(${JSON.stringify(clientURL)}); true`);
  return { p, errs };
}

// measure runs the positive assertions on a page and returns {ok, why, ...}.
async function measure(p, NEGATIVE = false) {
  const out = { ok: false, why: "" };
  if (!(await until(p, "globalThis.__wasmboxAudio && globalThis.__wasmboxAudio.stats().some((s) => s.attached)", REAL ? BOOT_MS : 15000))) {
    out.why = "no attached audio session"; return out;
  }
  out.stateBeforeGesture = await ev(p, "globalThis.__wasmboxAudio.state()");
  if (out.stateBeforeGesture !== "suspended") { out.why = `AudioContext was ${out.stateBeforeGesture} before any gesture (autoplay policy not enforced: the gesture check would be vacuous)`; return out; }
  // One synthetic gesture: a trusted click on empty desktop (user activation;
  // a lone modifier key does not count as activation).
  await p.mouse.click(1270, 400);
  try {
    await p.waitForFunction(() => globalThis.__wasmboxAudio.state() === "running", { timeout: 5000 });
  } catch (_) { out.why = `AudioContext not running after gesture (state=${await p.evaluate(() => globalThis.__wasmboxAudio.state())})`; return out; }
  out.contextRate = await p.evaluate(() => globalThis.__wasmboxAudio.contextRate());
  if (REAL) {
    // The ring opens at engine start; samples flow once the assets are loaded
    // and the first frames run. In the planted negative they never do, so this
    // wait is short there.
    const limit = NEGATIVE ? 8000 : FIRST_SAMPLES_MS;
    await until(p, "globalThis.__wasmboxAudio.stats().some((s) => s.played > 0)", limit);
  }
  await wait(300); // let the stream prime
  const a = await p.evaluate(() => globalThis.__wasmboxAudio.stats()[0]);
  await wait(WINDOW_MS);
  const b = await p.evaluate(() => globalThis.__wasmboxAudio.stats()[0]);
  out.playedFrames = (b.played - a.played) >>> 0;
  out.underruns = (b.underruns - a.underruns) >>> 0;
  out.overruns = b.overruns;
  out.capacityFrames = b.capacityFrames;
  out.buffered = b.buffered;
  out.clientRate = b.sampleRate;
  const expect = out.contextRate * (WINDOW_MS / 1000);
  if (!(out.playedFrames > 0)) { out.why = "worklet played 0 frames"; return out; }
  if (out.playedFrames < 0.5 * expect) { out.why = `played ${out.playedFrames} < half of ${expect}`; return out; }
  if (out.underruns >= UNDERRUN_BOUND) { out.why = `underruns ${out.underruns} >= bound ${UNDERRUN_BOUND}`; return out; }
  out.ok = true;
  return out;
}

async function compositorWorker(p) {
  for (const wk of p.workers()) {
    try { if (await wk.evaluate(() => typeof globalThis.__wasmboxAudioSessions === "function")) return wk; } catch (_) {}
  }
  return null;
}

const results = [];
const check = (ok, what, extra) => { results.push({ ok, what }); console.log(`${ok ? "PASS" : "FAIL"}  ${what}${extra ? "  " + extra : ""}`); };

const load1 = os.loadavg()[0];
console.log(`audio probe: 1-min load ${load1.toFixed(1)}${load1 > 8 ? "  (>8: underrun counts below are NOT a measurement)" : ""}`);

// Headless Chromium lets audio start without a gesture by default, which would
// make "running after a gesture" vacuous. Enforce the real desktop policy.
const browser = await pw.chromium.launch({
  headless: true,
  // Playwright adds --autoplay-policy=no-user-gesture-required by default.
  ignoreDefaultArgs: ["--autoplay-policy=no-user-gesture-required"],
  args: ["--autoplay-policy=document-user-activation-required"],
});
try {
  // --- positive ---
  {
    const { p, errs } = await boot(browser, CLIENT_URL);
    const r = await measure(p);
    console.log("positive:", JSON.stringify(r));
    check(r.ok, `${CLIENT} client: context running, frames played, underruns under bound`, r.ok ? "" : r.why);
    if (r.ok) {
      const ms = (r.capacityFrames / r.contextRate * 1000).toFixed(1);
      console.log(`latency: ring ${r.capacityFrames} frames @ client ${r.clientRate} Hz = ${(r.capacityFrames / r.clientRate * 1000).toFixed(1)} ms capacity; resampled to ${r.contextRate} Hz; underruns ${r.underruns}/${WINDOW_MS} ms; overruns ${r.overruns}  (${ms} ms at context rate)`);
    }
    // Other clients' wasm may not be built in a given checkout (hello/dock 404);
    // only errors that mention audio are ours.
    const mine = errs.filter((e) => /audio|worklet|ring/i.test(e));
    check(mine.length === 0, "no audio page errors", mine.join("; "));
    // closing the window's worker must free the session
    await p.close();
  }
  // --- planted negative ---
  {
    const { p } = await boot(browser, CLIENT_URL + "?nowrite=1");
    const r = await measure(p, true);
    console.log("negative:", JSON.stringify(r));
    check(!r.ok && r.playedFrames === 0 && r.why === "worklet played 0 frames", "planted negative (ring writer disabled) is DETECTED: played stays 0 and the assertions fail", r.why);
    await p.close();
  }
  // --- hostile (the synthetic client only: real engines never send malformed opens) ---
  if (CLIENT === "tone") {
    const { p } = await boot(browser, "clients/audiotest/worker.js?evil=1");
    await wait(2500);
    const cw = await compositorWorker(p);
    const inWorker = cw ? await cw.evaluate(() => globalThis.__wasmboxAudioSessions()) : -1;
    const inPage = await p.evaluate(() => globalThis.__wasmboxAudio.sessions());
    check(cw !== null && inWorker === 1 && inPage === 1,
      "5 malformed audio_open (bad size, rate, channels, non-pow2, non-shared) refused; the 1 valid open accepted; a 2nd open on the same port refused",
      `worker=${inWorker} page=${inPage} (want 1 and 1)`);
    await p.close();
  }
} finally {
  await browser.close();
}
const failed = results.filter((r) => !r.ok);
console.log(failed.length ? `${failed.length} FAILED` : "all audio checks passed");
process.exit(failed.length ? 1 : 0);
