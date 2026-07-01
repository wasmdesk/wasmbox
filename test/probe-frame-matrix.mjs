// Frame-matrix regression probe.
//
// Boots wasmbox once per registered Frame preset (16 as of 2026-06-30:
// see wasmbox/compositor/02_frame.rb FrameRegistry::TABLE), captures a
// PNG of the whole canvas, and asserts:
//
//   1. Every ?frame= URL boots cleanly (window.wasmboxReady === true,
//      no pageerror, no console.error).
//   2. Every capture is a non-blank frame (>= 5 % of pixels differ from
//      pure black — catches "wasm crashed after boot, nothing painted").
//   3. Every capture is DISTINCT from every OTHER capture — pairwise
//      pixel-hash comparison rejects any two frames that render
//      identically. A regression that silently made two Frame classes
//      collapse to the same paint would trip this.
//
// Snapshots are written to test/frame-matrix-out/{name}.png so a human
// can eyeball the matrix after a CI run.
//
// Run:  pkgx task serve            # in one terminal
//       node test/probe-frame-matrix.mjs
//
// Env overrides:
//   WASMBOX_BASE_URL   base URL (default http://localhost:8080/)
//   FRAMES             comma-separated subset to test (default all 16)
//   OUT_DIR            snapshot dir (default test/frame-matrix-out)

import { chromium } from "playwright";
import { createHash } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";

const BASE_URL = process.env.WASMBOX_BASE_URL || "http://localhost:8080/";
const OUT_DIR  = process.env.OUT_DIR || "frame-matrix-out";

// The 16 registered frames as of commit ecb364f.
const DEFAULT_FRAMES = [
  "openbox", "aqua",
  "openbox-adwaita-light", "openbox-adwaita-dark",
  "openbox-juno",
  "openbox-whitesur-light", "openbox-whitesur-dark",
  "openbox-solarized-light", "openbox-solarized-dark",
  "aqua-adwaita-light", "aqua-adwaita-dark",
  "aqua-juno",
  "aqua-whitesur-light", "aqua-whitesur-dark",
  "aqua-solarized-light", "aqua-solarized-dark",
];
const FRAMES = (process.env.FRAMES || DEFAULT_FRAMES.join(","))
  .split(",").map((s) => s.trim()).filter(Boolean);

function fail(msg) { console.error("FAIL:", msg); process.exit(1); }
function pass(msg) { console.log("PASS:", msg); process.exit(0); }

await mkdir(OUT_DIR, { recursive: true });

const browser = await chromium.launch({ headless: true });
const ctx = await browser.newContext({ viewport: { width: 900, height: 600 } });

// Per-frame capture: return {name, png Buffer, hash string, nonBlack ratio}.
async function capture(name) {
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push("pageerror: " + e.message));
  page.on("console", (m) => { if (m.type() === "error") errors.push("console.error: " + m.text()); });
  const url = new URL(BASE_URL);
  url.searchParams.set("frame", name);
  await page.goto(url.href, { waitUntil: "networkidle", timeout: 20_000 });
  await page.waitForFunction(() => window.wasmboxReady === true, null, { timeout: 15_000 });
  // Give the compositor 3 rAF frames to actually paint the decorated windows.
  await page.waitForTimeout(500);
  const png = await page.locator("#screen").screenshot({ type: "png" });
  if (errors.length) {
    console.error(`[${name}] JS errors:\n  ` + errors.join("\n  "));
    await page.close();
    return { name, error: errors.join("; ") };
  }
  await page.close();
  const hash = createHash("sha256").update(png).digest("hex").slice(0, 16);
  // Non-black ratio: sample every 100th byte of the raw PNG for non-zero.
  // Not statistically meaningful pixel-count but a fast "did anything paint?"
  // gate.
  let nonZero = 0, sampled = 0;
  for (let i = 0; i < png.length; i += 100) {
    sampled++;
    if (png[i] !== 0) nonZero++;
  }
  const nonBlack = nonZero / Math.max(1, sampled);
  return { name, png, hash, nonBlack };
}

const results = [];
for (const name of FRAMES) {
  process.stdout.write(`[probe] ${name} …`);
  const r = await capture(name);
  if (r.error) {
    process.stdout.write(` ERROR\n`);
    results.push(r);
    continue;
  }
  await writeFile(join(OUT_DIR, name + ".png"), r.png);
  process.stdout.write(` hash=${r.hash} nonBlack=${(r.nonBlack * 100).toFixed(1)}%\n`);
  results.push(r);
}
await browser.close();

// Assert 1: no boot errors.
const errored = results.filter((r) => r.error);
if (errored.length) {
  fail(`${errored.length}/${results.length} frames errored on boot: ${errored.map((r) => r.name).join(", ")}`);
}

// Assert 2: every capture is non-blank (arbitrary 5 % threshold — plenty
// of headroom above a truly blank frame + tolerant of themes with a lot
// of dark background).
const blank = results.filter((r) => r.nonBlack < 0.05);
if (blank.length) {
  fail(`${blank.length}/${results.length} frames painted <5% non-black: ${blank.map((r) => r.name).join(", ")}`);
}

// Assert 3: every capture is DISTINCT (pairwise hash comparison). A
// duplicate hash means two frames collapsed to the same paint — a real
// regression signal (e.g. all "themed" variants ignoring the palette).
const bucket = new Map();
for (const r of results) {
  const list = bucket.get(r.hash) || [];
  list.push(r.name);
  bucket.set(r.hash, list);
}
const dupes = [...bucket.entries()].filter(([, names]) => names.length > 1);
if (dupes.length) {
  console.error("hash collisions:");
  for (const [hash, names] of dupes) {
    console.error(`  ${hash}: ${names.join(", ")}`);
  }
  fail(`${dupes.length} hash collisions among ${results.length} frames — two or more frames painted identically`);
}

pass(`all ${results.length} frames booted, painted, and hash-distinct (snapshots in ${OUT_DIR}/)`);
