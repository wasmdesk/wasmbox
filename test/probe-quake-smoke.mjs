// SPDX-License-Identifier: BSD-3-Clause
//
// Cross-browser autonomous smoke test for the Quake client. It combines two
// verification styles that each dodge a way headless browser testing lies:
//
//   A) LOG-BASED assertions on the client's OWN console output (deterministic).
//   B) REAL-FRAME sampling via the compositor's __wasmboxReadRegion worker hook
//      (getImageData on the worker-owned OffscreenCanvas). A Playwright viewport
//      *screenshot* does NOT capture worker-canvas frames -- it reports a static
//      image even while the canvas animates -- which is why screenshot/pixel
//      diffing produced repeated false "it works" claims. getImageData read
//      inside the worker sees the true frames, so >=2 distinct frame hashes over
//      the sample window proves the client is actually RENDERING (not frozen).
//
// WHY cross-browser matters: the freeze this guards against
// (`QUAKE: FAIL client: unknown TE_* kind` from the attract-loop demo killing
// the wasm host) reproduced in FIREFOX but NOT Chromium on the same build -- a
// JS-event-loop timing difference. A Chromium-only test gives a false pass.
//
// KNOWN LIMIT: headless browsers throttle rAF/timers, so real-TIME game
// behaviour (the attract demo actually playing, menu-idle animations) does not
// run at real speed under automation -- neither screenshots nor this hook
// reproduce it. This smoke therefore proves "boots + streams + not frozen +
// rendering", NOT full in-browser gameplay; that still needs a real browser
// (or Playwright's Clock API + this hook, a future step).
//
// Engines:
//   - chromium + firefox: run the full check.
//   - webkit: SKIPPED -- headless WebKit never progresses past the wasm
//     handshake for the ~13 MB client + ~44 MB pak; a headless limitation, not
//     the real Safari path.
//
// Asserts, per engine: (1) compositor boots, (2) quake streams its pak +
// loads its map (`loaded maps/<name>.bsp from pak`), (3) NO `QUAKE: FAIL`,
// (4) >=2 distinct real-frame hashes = rendering/alive (skipped gracefully
// when the hook is absent).
//
// Point BASE at a server that has clients/quake/quake.wasm + a
// /v2/quake-assets mirror. Exit 0 iff every run engine passes.

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
  let frameHashes = -1;
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

    // Frame-animation check: read the quake window region straight from the
    // compositor's OffscreenCanvas (via the __wasmboxReadRegion worker hook) --
    // a viewport screenshot does NOT capture worker-canvas frames, but
    // getImageData does. >=2 distinct frame hashes over the samples proves the
    // client is actually rendering (not frozen); a single hash = frozen.
    // Skipped gracefully (-1) if the hook isn't present (older build).
    let cw = null;
    for (const wk of p.workers()) {
      try { if (await wk.evaluate(() => typeof globalThis.__wasmboxReadRegion === "function")) { cw = wk; break; } } catch (_) {}
    }
    if (cw) {
      const hs = new Set();
      for (let i = 0; i < 6; i++) {
        const r = await cw.evaluate(() => globalThis.__wasmboxReadRegion(200, 170, 560, 420));
        if (r) hs.add(r.hash);
        await wait(500);
      }
      frameHashes = hs.size;
    }
  } finally {
    await b.close();
  }

  const has = (re) => cons.some((l) => re.test(l));
  const failLine = cons.find((l) => /QUAKE: FAIL/.test(l));
  return {
    engine: name,
    streamed: has(/streaming pak/),
    // Map-agnostic: the default map has changed before (start.bsp -> lq_e0m1)
    // and will again, so match ANY "loaded maps/<name>.bsp from pak" rather
    // than a hard-coded map name that silently rots the smoke test.
    mapLoaded: has(/loaded maps\/[\w.-]+\.bsp from pak/),
    fail: failLine ? failLine.slice(0, 70) : null,
    pageerrors: errs.length,
    frameHashes,
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
  // frameHashes: -1 = hook absent (don't gate); >=2 = animating (ok); <2 = frozen.
  const animating = r.frameHashes === -1 || r.frameHashes >= 2;
  const ok = !r.fail && r.streamed && r.mapLoaded && r.pageerrors === 0 && animating;
  allOK = allOK && ok;
  if (ok) {
    console.log(`ok    ${name}: streamed + map .bsp loaded, no QUAKE:FAIL, rendering (frameHashes=${r.frameHashes})`);
  } else {
    console.log(
      `FAIL  ${name}: streamed=${r.streamed} mapLoaded=${r.mapLoaded} ` +
        `pageerrors=${r.pageerrors} frameHashes=${r.frameHashes} QUAKE:FAIL=${r.fail || "none"}`,
    );
  }
}

console.log(allOK ? "\nRESULT: PASS" : "\nRESULT: FAIL");
process.exitCode = allOK ? 0 : 1;
