// SPDX-License-Identifier: BSD-3-Clause
//
// Autonomous physics/render protocol for the Quake client. Where
// probe-quake-smoke proves "boots + streams + not frozen", this proves the
// PLAYER PHYSICS and the VIEW are correct -- the class of bug you cannot see
// from a frame hash: the eye floating at the ceiling, the player falling
// through the world, or monsters drawing through solid geometry.
//
// It is LOG-BASED and deterministic. The client emits a permanent "PHYS" line
// from its render loop (runner/render.go, every few frames) carrying, per
// sampled frame:
//
//   PHYS tic=N view=[x y z] vleaf=L floorDist=D p=[x y z] vel=[x y z] flags=F aliasR=A
//
//   view      the eye/camera origin actually used to render this frame
//   vleaf     the BSP leaf the eye is in (<=0 means solid/void => bad PVS)
//   floorDist eye height above the floor via a straight-down trace
//   p/vel     the SERVER player edict origin + velocity (authoritative)
//   flags     edict flags; bit 512 (FL_ONGROUND) = standing on ground
//   aliasR    alias (monster/item) models rendered this frame
//
// From these we assert, with no human in the loop, that:
//   1. the player LANDS (FL_ONGROUND, velocity -> 0) -- no infinite fall;
//   2. the eye stands at a sane height above the floor -- NOT floating;
//   3. the eye is always inside a real leaf -- never in solid/void;
//   4. the VIEW tracks the server player (viewZ ~= playerZ + viewheight);
//   5. horizontal input displaces the player (moves & collides);
//   6. the frame loop reports FPS / a render cadence;
//   7. rendered monsters are PVS-bounded -- you don't see through matter.
//
// Core invariants (1-4, 7) need only a couple of grounded samples and gate
// hard. Movement (5) and FPS (6) are best-effort: headless browsers throttle
// rAF, so a short automated window may not accumulate enough motion frames --
// they warn rather than fail when starved. Run headed (QUAKE_PHYS_HEADED=1)
// for the full set.
//
// Point BASE at a server with clients/quake/quake.wasm + a quake-assets mirror
// whose default map spawns monsters (e.g. lq_e0m1). Exit 0 iff every hard
// invariant holds.

import * as pw from "playwright";

const BASE = (process.env.WASMBOX_BASE_URL || "http://127.0.0.1:8146").replace(/\/$/, "");
const HEADED = process.env.QUAKE_PHYS_HEADED === "1";
const BOOT_MS = 45000;
const SPAWN_MS = Number(process.env.QUAKE_PHYS_SPAWN_MS || 30000);
const SETTLE_MS = 6000;
const wait = (ms) => new Promise((r) => setTimeout(r, ms));

const FL_ONGROUND = 512;
const VIEWHEIGHT = 22;

const RE =
  /PHYS tic=(\d+) view=\[(-?\d+) (-?\d+) (-?[\d.]+)\] vleaf=(-?\d+) floorDist=(-?\d+) p=\[(-?\d+) (-?\d+) (-?[\d.]+)\] vel=\[(-?[\d.]+) (-?[\d.]+) (-?[\d.]+)\] flags=(\d+) aliasR=(\d+)/;

function parsePhys(line) {
  const m = RE.exec(line);
  if (!m) return null;
  const n = m.map(Number);
  return {
    tic: n[1], view: [n[2], n[3], n[4]], vleaf: n[5], floorDist: n[6],
    p: [n[7], n[8], n[9]], vel: [n[10], n[11], n[12]], flags: n[13], aliasR: n[14],
  };
}

const results = [];
const check = (name, pass, hard, detail) => {
  results.push({ name, pass, hard });
  const tag = pass ? "PASS" : hard ? "FAIL" : "WARN";
  console.log(`${tag}  ${name}${detail ? " -- " + detail : ""}`);
};

const b = await pw.firefox.launch({ headless: !HEADED });
const raw = [];
try {
  const p = await b.newPage({ viewport: { width: 1280, height: 800 } });
  p.on("console", (m) => raw.push(m.text().replace(/^\[.*?\]\s*/, "")));
  await p.goto(`${BASE}/index.html`, { waitUntil: "load", timeout: BOOT_MS });
  await p.waitForFunction(() => {
    if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
    return globalThis.wasmboxReady === true;
  }, { timeout: BOOT_MS });
  await wait(1500);
  await p.evaluate(() => globalThis.wasmboxSpawnExternal("clients/quake/worker.js"));
  await wait(SPAWN_MS);

  // Dismiss the menu into the game, then let the player fall to the floor.
  await p.mouse.click(570, 450);
  await wait(400);
  for (const k of ["Enter", "Enter", "Enter"]) { await p.keyboard.press(k); await wait(1200); }
  await wait(SETTLE_MS);

  // Drive input: turn, walk into the room (collide), jump, walk more.
  for (const [key, hold] of [["ArrowRight", 900], ["w", 2500], [" ", 250], ["w", 1500]]) {
    await p.keyboard.down(key); await wait(hold); await p.keyboard.up(key); await wait(400);
  }
  await wait(3000);
} finally {
  await b.close();
}

const samples = raw.map(parsePhys).filter(Boolean);
if (samples.length < 2) {
  console.log(`FAIL  telemetry -- only ${samples.length} PHYS samples (client did not render/spawn)`);
  console.log(raw.filter((l) => /PHYS|QUAKE|error/i.test(l)).slice(-15).join("\n"));
  process.exitCode = 1;
} else {
  const settled = samples.filter((s) => (s.flags & FL_ONGROUND) !== 0);

  check("landed (FL_ONGROUND observed)", settled.length > 0, true,
    `${settled.length}/${samples.length} grounded`);
  check("no infinite fall (|velZ|<50 grounded)",
    settled.length > 0 && settled.every((s) => Math.abs(s.vel[2]) < 50), true,
    `max |velZ| = ${Math.max(0, ...settled.map((s) => Math.abs(s.vel[2]))).toFixed(0)}`);
  check("eye at standing height (20<floorDist<120)",
    settled.length > 0 && settled.every((s) => s.floorDist > 20 && s.floorDist < 120), true,
    `floorDist = [${settled.map((s) => s.floorDist).join(",")}]`);
  check("eye always in a real leaf (vleaf>0)",
    samples.every((s) => s.vleaf > 0), true,
    `min vleaf = ${Math.min(...samples.map((s) => s.vleaf))}`);
  check("view tracks server player (viewZ~=pZ+viewheight)",
    settled.length > 0 && settled.every((s) => Math.abs(s.view[2] - (s.p[2] + VIEWHEIGHT)) < 8), true,
    settled.length ? `${settled[0].view[2]} vs ${(settled[0].p[2] + VIEWHEIGHT).toFixed(0)}` : "");
  check("rendered monsters PVS-bounded (aliasR<=12)",
    settled.every((s) => s.aliasR <= 12), true,
    `max aliasR = ${Math.max(0, ...settled.map((s) => s.aliasR))}`);

  // Best-effort (headless rAF throttling can starve these).
  const g = settled.length >= 2 ? settled : samples;
  const moved = Math.hypot(g[g.length - 1].p[0] - g[0].p[0], g[g.length - 1].p[1] - g[0].p[1]);
  check("horizontal input displaced the player (>16u)", moved > 16, false, `moved ${moved.toFixed(0)}u`);
  check("frame loop reports FPS / render cadence",
    raw.some((l) => /FPS|rendered \d+ alias|tic \d+ rendered/i.test(l)), false);

  const failed = results.filter((r) => r.hard && !r.pass);
  console.log(`\n${results.filter((r) => r.pass).length}/${results.length} invariants hold` +
    (failed.length ? `; ${failed.length} HARD failure(s)` : ""));
  console.log(failed.length ? "\nRESULT: FAIL" : "\nRESULT: PASS");
  process.exitCode = failed.length ? 1 : 0;
}
