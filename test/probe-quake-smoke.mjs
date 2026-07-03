// SPDX-License-Identifier: BSD-3-Clause
//
// Cross-browser, LOG-BASED smoke test for the Quake client. This is the
// autonomous test protocol for quake: it asserts on the client's OWN console
// output (deterministic) instead of pixel-diffing a real-time game (which is
// unreliable and repeatedly produced false "it works" claims).
//
// WHY cross-browser matters: the client freeze this guards against
// (`QUAKE: FAIL client: unknown TE_* kind` from the attract-loop demo, which
// used to kill the whole wasm host) reproduces in FIREFOX but NOT in Chromium
// on the same build -- a JS-event-loop timing difference. A Chromium-only test
// gives a false pass. So this runs every engine Playwright ships that can
// actually execute the client.
//
// Engines:
//   - chromium + firefox: run the full check.
//   - webkit: SKIPPED -- headless WebKit does not make progress past the wasm
//     handshake for the ~13 MB client + ~44 MB pak stream (no FAIL, just never
//     streams); it is a headless-WebKit limitation, not the real Safari path.
//
// Asserts, per engine:
//   1. the compositor boots (globalThis.wasmboxReady),
//   2. quake streams its pak + `loaded maps/start.bsp` (assets reachable),
//   3. NO `QUAKE: FAIL` line (the host did not die -> the window is not frozen).
//
// Point BASE at a server that has clients/quake/quake.wasm + a
// /v2/quake-assets mirror (built by the quake-smoke CI job or `task serve` +
// a quake build). Exit 0 iff every run engine passes.

import * as pw from "playwright";

const BASE = (process.env.WASMBOX_BASE_URL || "http://127.0.0.1:8146").replace(/\/$/, "");
const ONLY = process.env.QUAKE_SMOKE_ENGINE; // optional: run just one engine
const BOOT_MS = 40000;
const PLAY_MS = Number(process.env.QUAKE_SMOKE_WAIT_MS || 40000); // pak stream + live bring-up
const wait = (ms) => new Promise((r) => setTimeout(r, ms));

const ENGINES = ["chromium", "firefox"].filter((e) => !ONLY || e === ONLY);

async function runEngine(name) {
  const engine = pw[name];
  const b = await engine.launch({ headless: true });
  const cons = [];
  const errs = [];
  try {
    const p = await b.newPage({ viewport: { width: 1280, height: 800 } });
    p.on("console", (m) => cons.push(m.text().replace(/^\[.*?\]\s*/, "")));
    p.on("pageerror", (e) => errs.push(String(e)));
    await p.goto(`${BASE}/index.html`, { waitUntil: "load", timeout: BOOT_MS });
    await p.waitForFunction(
      () => {
        if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
        return globalThis.wasmboxReady === true;
      },
      { timeout: BOOT_MS },
    );
    await wait(1500);
    await p.evaluate(() => globalThis.wasmboxSpawnExternal("clients/quake/worker.js"));
    await wait(PLAY_MS);
  } finally {
    await b.close();
  }
  const has = (re) => cons.some((l) => re.test(l));
  const failLine = cons.find((l) => /QUAKE: FAIL/.test(l));
  return {
    engine: name,
    streamed: has(/streaming pak/),
    startbsp: has(/loaded maps\/start\.bsp/),
    fail: failLine ? failLine.slice(0, 70) : null,
    pageerrors: errs.length,
  };
}

let allOK = true;
for (const name of ENGINES) {
  let r;
  try {
    r = await runEngine(name);
  } catch (e) {
    console.log(`FAIL  ${name}: unexpected ${e && e.stack ? e.stack.split("\n")[0] : e}`);
    allOK = false;
    continue;
  }
  const ok = !r.fail && r.streamed && r.startbsp && r.pageerrors === 0;
  allOK = allOK && ok;
  if (ok) {
    console.log(`ok    ${name}: streamed + maps/start.bsp loaded, no QUAKE:FAIL`);
  } else {
    console.log(
      `FAIL  ${name}: streamed=${r.streamed} startbsp=${r.startbsp} ` +
        `pageerrors=${r.pageerrors} QUAKE:FAIL=${r.fail || "none"}`,
    );
  }
}

console.log(allOK ? "\nRESULT: PASS" : "\nRESULT: FAIL");
process.exitCode = allOK ? 0 : 1;
