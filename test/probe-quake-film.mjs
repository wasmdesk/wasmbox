// SPDX-License-Identifier: BSD-3-Clause
// Mini-film probe: boot compositor + quake, then sample the FULL composited
// canvas as real PNG frames (via __wasmboxGrabRegion worker hook) at a fixed
// cadence, so a human/agent can watch the frame sequence for the black-frame
// flicker and inspect the actual rendered pixels (brackets vs text, etc).
import * as pw from "playwright";
import { writeFileSync, mkdirSync } from "node:fs";

const BASE = (process.env.WASMBOX_BASE_URL || "http://127.0.0.1:8139").replace(/\/$/, "");
const ENGINE = process.env.QUAKE_FILM_ENGINE || "firefox";
const OUT = process.env.QUAKE_FILM_OUT || "/tmp/wb-film/film";
const HEADLESS = process.env.QUAKE_FILM_HEADLESS !== "0";
const FRAMES = Number(process.env.QUAKE_FILM_FRAMES || 48);
const INTERVAL = Number(process.env.QUAKE_FILM_INTERVAL || 400);
const BOOT_MS = 40000;
const SETTLE_MS = Number(process.env.QUAKE_FILM_SETTLE || 30000);
const W = 1280, H = 800;
const wait = (ms) => new Promise((r) => setTimeout(r, ms));

mkdirSync(OUT, { recursive: true });
const b = await pw[ENGINE].launch({ headless: HEADLESS });
const cons = [];
try {
  const p = await b.newPage({ viewport: { width: W, height: H } });
  p.on("console", (m) => cons.push(m.text().replace(/^\[.*?\]\s*/, "")));
  await p.goto(`${BASE}/index.html`, { waitUntil: "load", timeout: BOOT_MS });
  await p.waitForFunction(() => {
    if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
    return globalThis.wasmboxReady === true;
  }, { timeout: BOOT_MS });
  await wait(1500);
  await p.evaluate(() => globalThis.wasmboxSpawnExternal("clients/quake/worker.js"));
  console.log(`spawned quake; settling ${SETTLE_MS}ms for pak stream...`);
  await wait(SETTLE_MS);

  // locate the compositor worker that owns the OffscreenCanvas
  let cw = null;
  for (const wk of p.workers()) {
    try { if (await wk.evaluate(() => typeof globalThis.__wasmboxGrabRegion === "function")) { cw = wk; break; } } catch (_) {}
  }
  if (!cw) { console.log("NO grab hook found in any worker"); process.exit(2); }

  console.log(`sampling ${FRAMES} frames @ ${INTERVAL}ms (${(FRAMES*INTERVAL/1000).toFixed(1)}s)...`);
  const series = [];
  for (let i = 0; i < FRAMES; i++) {
    const stat = await cw.evaluate(({ w, h }) => globalThis.__wasmboxReadRegion(0, 0, w, h), { w: W, h: H });
    const url = await cw.evaluate(({ w, h }) => globalThis.__wasmboxGrabRegion(0, 0, w, h), { w: W, h: H });
    if (url) {
      const png = Buffer.from(url.split(",")[1], "base64");
      writeFileSync(`${OUT}/f${String(i).padStart(3, "0")}.png`, png);
    }
    series.push({ i, ...(stat || {}) });
    await wait(INTERVAL);
  }
  // report the brightness / non-black time series
  console.log("\nframe  bright  nonblack%  hash");
  let prevHash = null, changes = 0, dark = 0;
  for (const s of series) {
    const mark = s.hash !== prevHash ? "*" : " ";
    if (s.hash !== prevHash) changes++;
    if ((s.nonblackPct ?? 0) < 3) dark++;
    prevHash = s.hash;
    console.log(`${String(s.i).padStart(4)} ${String(s.brightness).padStart(6)} ${String(s.nonblackPct).padStart(9)}  ${mark}${(s.hash>>>0).toString(16)}`);
  }
  console.log(`\ndistinct-frame changes=${changes}/${FRAMES}  near-black frames(<3% nonblack)=${dark}/${FRAMES}`);
} finally {
  const streamed = cons.some((l) => /streaming pak/.test(l));
  const startbsp = cons.some((l) => /loaded maps\/start\.bsp/.test(l));
  const fail = cons.find((l) => /QUAKE: FAIL/.test(l));
  console.log(`\nlog: streamed=${streamed} startbsp=${startbsp} FAIL=${fail ? fail.slice(0,70) : "none"}`);
  await b.close();
}
