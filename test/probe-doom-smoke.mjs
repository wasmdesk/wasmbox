// SPDX-License-Identifier: BSD-3-Clause
//
// Cross-browser smoke + interaction test for the Doom client (clients/doom,
// built from go-doom/engine cmd/doom-wasmbox). Same verification rules as
// probe-quake-smoke.mjs, learned the hard way there:
//
//   * LOG-BASED assertions on the client's own "DOOM: ..." console lines --
//     the engine prints a "DOOM: state=..." line whenever the game state
//     changes (game state, menu, demo, map, and the player position at most
//     twice a second), so a key press is proved to have CHANGED THE GAME, not
//     merely to have been sent.
//   * REAL-FRAME sampling inside the compositor worker (__wasmboxReadRegion):
//     a Playwright viewport screenshot does not capture worker OffscreenCanvas
//     frames, getImageData does. >=2 distinct hashes = rendering, not frozen.
//   * Chromium AND Firefox: event-loop timing differs between them, and a
//     Chromium-only pass has hidden a Firefox-only freeze before.
//
// Steps per engine:
//   1. compositor boots; spawn clients/doom/worker.js
//   2. the WAD streams from /v2/doom-assets and verifies ("sha256 verified"),
//      the engine draws its first frame, no "DOOM: FAIL", no page errors
//   3. Escape opens the menu      -> a state line with menu=1
//   4. Enter x3 starts a new game -> state=level menu=0 demo=0 map=E1M1
//   5. holding ArrowUp walks      -> a later state line with another pos=,
//      and frames animate meanwhile (>=2 distinct region hashes)
//   6. the keyboard-shortcuts inhibitor holds: Ctrl+Alt+ArrowLeft leaves the
//      window where it was, Ctrl+Space opens no Spotlight
//   7. the fps lines are collected and printed (DOOM: fps=...)
//
// Headless browsers throttle timers, so the frame rate printed here is NOT a
// performance measurement; run with HEADED=1 for that (DOOM_SMOKE_HEADED).
//
// Env: WASMBOX_BASE_URL (default http://127.0.0.1:8146), DOOM_SMOKE_ENGINE
// (chromium|firefox, default both), HEADED=1, DOOM_SMOKE_PLAY_MS (default
// 15000: how long to watch after a level starts, for the fps lines).

import * as pw from "playwright";

const BASE = (process.env.WASMBOX_BASE_URL || "http://127.0.0.1:8146").replace(/\/$/, "");
const ONLY = process.env.DOOM_SMOKE_ENGINE;
const HEADED = process.env.HEADED === "1";
const PLAY_MS = Number(process.env.DOOM_SMOKE_PLAY_MS || 15000);
const BOOT_MS = 60000;
const wait = (ms) => new Promise((r) => setTimeout(r, ms));
const ENGINES = ["chromium", "firefox"].filter((e) => !ONLY || e === ONLY);

async function runEngine(name) {
  const b = await pw[name].launch({ headless: !HEADED });
  const cons = [];
  const errs = [];
  const steps = [];
  const step = (ok, what) => steps.push({ ok, what });
  // waitLine polls the console for a DOOM line matching re that appeared at
  // or after index `from`; returns its index or -1 on timeout.
  const waitLine = async (re, from, ms) => {
    const end = Date.now() + ms;
    while (Date.now() < end) {
      for (let i = from; i < cons.length; i++) if (re.test(cons[i])) return i;
      await wait(100);
    }
    return -1;
  };
  try {
    const p = await b.newPage({ viewport: { width: 1280, height: 800 } });
    p.on("console", (m) => cons.push(m.text().replace(/^\[.*?\]\s*/, "")));
    p.on("pageerror", (e) => errs.push(String(e)));
    await p.goto(`${BASE}/index.html`, { waitUntil: "load", timeout: BOOT_MS });
    await p.waitForFunction(() => {
      if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
      return globalThis.wasmboxReady === true;
    }, { timeout: BOOT_MS });
    await wait(1500);
    await p.evaluate(() => globalThis.wasmboxSpawnExternal("clients/doom/worker.js"));

    const verified = await waitLine(/^DOOM: wad freedoom1\.wad: sha256 verified/, 0, BOOT_MS);
    step(verified >= 0, "WAD streamed from /v2/doom-assets and sha256-verified");
    const first = await waitLine(/^DOOM: first frame/, 0, BOOT_MS);
    step(first >= 0, "engine drew its first frame");

    // The compositor worker carries the test hooks; the focused rect is the
    // Doom window's body (its title bar sits just above it).
    let cw = null;
    for (const wk of p.workers()) {
      try { if (await wk.evaluate(() => typeof globalThis.__wasmboxReadRegion === "function")) { cw = wk; break; } } catch (_) {}
    }
    step(!!cw, "compositor worker exposes __wasmboxReadRegion");
    let rect = cw ? await cw.evaluate(() => (globalThis.__wasmboxFocusedRect ? globalThis.__wasmboxFocusedRect() : null)) : null;
    step(!!rect && rect.x >= 0 && rect.w >= 320, `Doom window is focused (${rect ? `${rect.w}x${rect.h} at ${rect.x},${rect.y}` : "no rect"})`);
    if (!rect || rect.x < 0) rect = { x: 200, y: 150, w: 640, h: 480 };
    const sample = async () => {
      if (!cw) return null;
      const r = await cw.evaluate((q) => globalThis.__wasmboxReadRegion(q.x + 4, q.y + 4, q.w - 8, q.h - 8), rect);
      return r ? r.hash : null;
    };

    // Give the page keyboard focus with a click on the window's TITLE BAR:
    // the compositor keeps decoration clicks, whereas a click in the body
    // reaches Doom as the fire button (which on the title screen opens the
    // menu, and the Escape below would then close it).
    await p.mouse.click(rect.x + Math.floor(rect.w / 2), rect.y - 8);
    await wait(1000);
    let from = cons.length;
    await p.keyboard.press("Escape");
    const menu = await waitLine(/^DOOM: state=\S+ menu=1/, from, 15000);
    step(menu >= 0, "Escape opened the menu (state line with menu=1)");

    from = cons.length;
    for (let i = 0; i < 3; i++) { await p.keyboard.press("Enter"); await wait(700); }
    const level = await waitLine(/^DOOM: state=level menu=0 demo=0 map=E1M1 /, from, 20000);
    step(level >= 0, "Enter x3 started a new game on E1M1");

    let walked = false;
    if (level >= 0) {
      const startPos = (cons[level].match(/pos=(-?\d+,-?\d+)/) || [])[1];
      from = cons.length;
      // Walk, sampling the composited window while the view changes.
      const hs = new Set();
      await p.keyboard.down("ArrowUp");
      for (let i = 0; i < 6; i++) { const h = await sample(); if (h !== null) hs.add(h); await wait(400); }
      await p.keyboard.up("ArrowUp");
      step(hs.size >= 2, `frames animate in the compositor (${hs.size} distinct hashes of 6 samples)`);
      // DOOM_SMOKE_GRAB=<dir>: save the composited window as <dir>/doom-<engine>.png
      // (read in the worker -- a viewport screenshot would not show it).
      if (process.env.DOOM_SMOKE_GRAB && cw) {
        const url = await cw.evaluate((q) => globalThis.__wasmboxGrabRegion(q.x, q.y, q.w, q.h), rect);
        if (url) {
          const fs = await import("node:fs");
          fs.writeFileSync(`${process.env.DOOM_SMOKE_GRAB}/doom-${name}.png`, Buffer.from(url.split(",")[1], "base64"));
        }
      }
      const moved = await waitLine(new RegExp(`^DOOM: state=level .* pos=(?!${startPos}$)`), from, 8000);
      walked = moved >= 0;
      step(walked, `ArrowUp moved the player (from ${startPos}${walked ? " to " + cons[moved].split("pos=")[1] : ""})`);
    } else {
      step(false, "ArrowUp moved the player (skipped: no level)");
    }

    // Keyboard-shortcuts inhibitor: Doom fires with Ctrl and strafes with Alt,
    // so the compositor's chords must not fire while it has the keyboard.
    if (cw) {
      await p.keyboard.down("Control"); await p.keyboard.down("Alt");
      await p.keyboard.press("ArrowLeft");
      await p.keyboard.up("Alt");
      await p.keyboard.press("Space");
      await p.keyboard.up("Control");
      await wait(800);
      const r2 = await cw.evaluate(() => globalThis.__wasmboxFocusedRect());
      const sp = await cw.evaluate(() => (globalThis.__wasmboxSpotlightState ? globalThis.__wasmboxSpotlightState() : null));
      step(!!r2 && r2.x === rect.x && r2.y === rect.y && r2.w === rect.w && !r2.state,
        `Ctrl+Alt+ArrowLeft did not snap the window (${r2 ? `${r2.w}x${r2.h} at ${r2.x},${r2.y} state=${r2.state || "-"}` : "no rect"})`);
      step(!sp || !sp.active, "Ctrl+Space did not open Spotlight");
    }
    await wait(PLAY_MS);
  } finally {
    await b.close();
  }
  const failLine = cons.find((l) => /^DOOM: FAIL/.test(l));
  step(!failLine, `no DOOM: FAIL${failLine ? " -- " + failLine.slice(0, 120) : ""}`);
  step(errs.length === 0, `no page errors${errs.length ? " -- " + errs[0].slice(0, 120) : ""}`);
  if (process.env.DOOM_SMOKE_VERBOSE === "1") for (const l of cons) if (/^DOOM:/.test(l)) console.log(`      [${name}] ${l}`);
  const fps = cons.filter((l) => /^DOOM: fps=/.test(l));
  const audio = (cons.find((l) => /^DOOM: surface /.test(l)) || "").replace(/^.*audio=/, "");
  return { name, steps, fps, audio };
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
  console.log(`== ${name}${HEADED ? " (headed)" : " (headless)"} audio=${r.audio || "?"}`);
  for (const s of r.steps) {
    console.log(`${s.ok ? "ok   " : "FAIL "} ${s.what}`);
    allOK = allOK && s.ok;
  }
  for (const l of r.fps) console.log(`      ${l}`);
}
console.log(allOK ? "\nRESULT: PASS" : "\nRESULT: FAIL");
process.exitCode = allOK ? 0 : 1;
