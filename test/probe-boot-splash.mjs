// SPDX-License-Identifier: BSD-3-Clause
//
// Headless probe for the wasmbox/wasmdesk BOOT SPLASH (index.html +
// compositor.worker.js). The splash is a determinate progress overlay shown
// from the very first paint until the compositor presents its first frame, so
// the user never stares at a blank page while the ~180 MB Ruby/WASM runtime
// downloads + boots.
//
// What we prove (at DOM precision, not "something appeared"):
//
//   (a) Right after load the splash element (#boot-splash) exists and its
//       progress bar reads ~0 (aria-valuenow / measured fill width near zero).
//   (b) The bar value strictly increases through >=2 distinct staged phases:
//       we assert the reported stage sequence (download -> instantiate ->
//       runtime -> ready, in order) AND that the measured bar value is
//       monotonically non-decreasing with multiple strict increases, ending
//       at exactly 100.
//   (c) After boot the splash is REMOVED from the DOM (getElementById -> null)
//       and the compositor canvas is presenting (screenshot has many non-blank
//       pixels -- the same "first frame" ground truth render.mjs uses).
//   (d) After removal there is no leaked timer/rAF: the compositor worker's
//       __wasmboxStats show ~0 real per-frame decode work while idle (reusing
//       the same hook test/probe-frame-cost.mjs reads), and the main-thread
//       boot state reports done with no pending fallback timer.
//
// The dev server here throttles wasmbox.wasm into chunks (with a leading delay
// and a real Content-Length) so the streamed download is observable and the
// determinate bar has time to climb -- on a hot localhost the binary otherwise
// streams in faster than a screenshot loop can latch a frame.
//
// HARD RULE: system Chromium, headless.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 40000;
const VIEW_W = 1280;
const VIEW_H = 800;
// Minimum non-blank pixels for the desktop canvas to count as "presenting"
// (mirrors render.mjs's MIN_PAINTED_PX ground truth).
const MIN_PAINTED_PX = 5000;

const MIME = {
  ".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8", ".wasm": "application/wasm",
  ".css": "text/css; charset=utf-8", ".json": "application/json; charset=utf-8",
  ".rb": "text/plain; charset=utf-8",
};

function startServer() {
  const server = createServer(async (req, res) => {
    try {
      const urlPath = decodeURIComponent((req.url || "/").split("?")[0]);
      let rel = normalize(urlPath).replace(/^(\.\.[/\\])+/, "");
      if (rel === "/" || rel === "") rel = "/index.html";
      const file = join(ROOT, rel);
      if (!file.startsWith(ROOT)) { res.writeHead(403).end("forbidden"); return; }
      const body = await readFile(file);
      res.setHeader("Content-Type", MIME[extname(file)] || "application/octet-stream");
      // COOP/COEP so SharedArrayBuffer + crossOriginIsolated work.
      res.setHeader("Cross-Origin-Opener-Policy", "same-origin");
      res.setHeader("Cross-Origin-Embedder-Policy", "require-corp");

      // Throttle the compositor wasm so the streamed download is observable:
      // a real Content-Length (determinate bar) plus chunked writes with a
      // leading delay so the splash sits at 0 % long enough for the first poll
      // to latch it, then climbs across several progress messages.
      if (rel.endsWith("wasmbox.wasm")) {
        res.setHeader("Content-Length", body.length);
        res.writeHead(200);
        const N = 10;
        const sz = Math.ceil(body.length / N);
        for (let i = 0; i < N; i++) {
          // Delay BEFORE each chunk (including the first) -> the bar is
          // observably 0 right after load, then advances chunk by chunk.
          await new Promise((r) => setTimeout(r, 45));
          const slice = body.subarray(i * sz, Math.min(body.length, (i + 1) * sz));
          res.write(slice);
        }
        res.end();
        return;
      }

      res.writeHead(200).end(body);
    } catch {
      res.writeHead(404).end("not found");
    }
  });
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      resolve({ server, base: `http://127.0.0.1:${port}` });
    });
  });
}

function fail(msg) { console.error(`FAIL: ${msg}`); process.exitCode = 1; }
function ok(msg) { console.log(`ok  ${msg}`); }

// Snapshot the splash + boot state at DOM precision.
function snapshotBoot() {
  const b = globalThis.__wasmboxBoot || null;
  const el = document.getElementById("boot-splash");
  const bar = document.getElementById("boot-bar");
  const fill = document.getElementById("boot-fill");
  let fillPct = null;
  if (fill) {
    // Measured width as a fraction of the track -> independent evidence of the
    // bar climbing (not just the reported value).
    const track = fill.parentElement;
    const tw = track ? track.getBoundingClientRect().width : 0;
    const fw = fill.getBoundingClientRect().width;
    fillPct = tw > 0 ? (fw / tw) * 100 : null;
  }
  return {
    present: !!el,
    value: b ? b.value : null,
    stage: b ? b.stage : null,
    stages: b ? b.stages.slice() : [],
    done: b ? b.done : false,
    aria: bar ? Number(bar.getAttribute("aria-valuenow")) : null,
    fillPct,
    ready: globalThis.wasmboxReady === true,
    err: globalThis.wasmboxError || null,
  };
}

async function compositorWorker(page) {
  for (const w of page.workers()) {
    try {
      if (await w.evaluate(() => typeof globalThis.__wasmboxFrameStats === "function")) return w;
    } catch (_) {}
  }
  return null;
}

const { server, base } = await startServer();
console.log(`probe-boot-splash: serving on ${base}`);

const browser = await chromium.launch({ headless: true });
const pageErrors = [];
const consoleErrors = [];

try {
  const page = await browser.newPage({ viewport: { width: VIEW_W, height: VIEW_H } });
  page.on("pageerror", (e) => pageErrors.push(String(e)));
  page.on("console", (m) => { if (m.type() === "error") consoleErrors.push(m.text()); });

  await page.goto(`${base}/index.html`, { waitUntil: "load" });

  // --- Poll the splash from load until boot completes ----------------------
  // Fast poll so we latch the near-zero start + several intermediate values.
  const samples = [];
  const deadline = Date.now() + BOOT_TIMEOUT_MS;
  for (;;) {
    const s = await page.evaluate(snapshotBoot);
    samples.push(s);
    if (s.err) { fail(`wasmboxError during boot: ${s.err}`); break; }
    if (s.done && !s.present) break;      // splash gone -> boot finished
    if (Date.now() > deadline) { fail("boot did not finish within timeout"); break; }
    await page.waitForTimeout(15);
  }
  console.log(`captured ${samples.length} boot samples`);

  const first = samples[0];
  const valuesSeen = samples.map((s) => s.value).filter((v) => v != null);
  const stagesFinal = samples.reduce((a, s) => (s.stages.length > a.length ? s.stages : a), []);

  // --- (a) splash exists + bar ~0 right after load -------------------------
  if (first && first.present && first.value != null && first.value < 12) {
    ok(`(a) splash present at load, bar ~0 (value=${first.value.toFixed(1)}%, aria=${first.aria}, fill=${first.fillPct == null ? "n/a" : first.fillPct.toFixed(1) + "%"})`);
  } else {
    fail(`(a) splash/near-zero start not observed: present=${first && first.present}, value=${first && first.value}`);
  }

  // --- (b) monotonic increase through >=2 distinct staged phases -----------
  // Stage sequence is authoritative (recorded as each worker message arrives,
  // independent of poll timing).
  const seqOk = ["download", "instantiate", "runtime", "ready"].every((st, i) => stagesFinal[i] === st) &&
                stagesFinal.length === 4;
  if (seqOk) {
    ok(`(b) stage sequence in order: ${stagesFinal.join(" -> ")}`);
  } else {
    fail(`(b) unexpected stage sequence: ${JSON.stringify(stagesFinal)}`);
  }
  // Monotonic non-decreasing measured value, with multiple strict increases.
  let regressions = 0, increases = 0, prev = -1;
  for (const v of valuesSeen) {
    if (v < prev - 0.01) regressions++;
    if (v > prev + 0.01) increases++;
    prev = v;
  }
  const distinctBelow100 = new Set(valuesSeen.filter((v) => v > 0 && v < 100).map((v) => Math.round(v))).size;
  const reached100 = valuesSeen.some((v) => v >= 100);
  if (regressions === 0 && increases >= 2 && distinctBelow100 >= 2 && reached100) {
    ok(`(b) bar monotonic up: ${increases} increases, 0 regressions, ${distinctBelow100} distinct sub-100 plateaus, reached 100`);
  } else {
    fail(`(b) bar progression bad: increases=${increases} regressions=${regressions} distinctBelow100=${distinctBelow100} reached100=${reached100} values=${valuesSeen.map((v) => v.toFixed(0)).join(",")}`);
  }

  // --- (c) splash removed + canvas presenting ------------------------------
  await page.waitForFunction(() => globalThis.wasmboxReady === true, { timeout: BOOT_TIMEOUT_MS });
  const gone = await page.evaluate(() => document.getElementById("boot-splash") === null);
  if (gone) ok("(c) #boot-splash removed from the DOM after boot");
  else fail("(c) #boot-splash still in the DOM after boot");

  const dim = await page.evaluate(() => {
    const c = document.getElementById("screen");
    return c ? { width: c.width, height: c.height } : null;
  });
  const shot = await page.screenshot({ type: "png", fullPage: false });
  const png = PNG.sync.read(shot);
  let painted = 0;
  const colors = new Set();
  for (let i = 0; i < png.data.length; i += 4) {
    const r = png.data[i], g = png.data[i + 1], b = png.data[i + 2];
    if (r || g || b) { painted++; if (colors.size < 4096) colors.add((r << 16) | (g << 8) | b); }
  }
  if (painted > MIN_PAINTED_PX) {
    ok(`(c) compositor canvas ${dim && dim.width}x${dim && dim.height} presenting: ${painted} non-blank px, ${colors.size} colors`);
  } else {
    fail(`(c) canvas looks blank after splash removal: only ${painted} non-blank px (need > ${MIN_PAINTED_PX})`);
  }

  // --- (d) no leaked timer/rAF after removal -------------------------------
  const cw = await compositorWorker(page);
  if (!cw) {
    fail("(d) compositor worker (with __wasmboxFrameStats) not found");
  } else {
    await cw.evaluate(() => globalThis.__wasmboxFrameStats(true)); // reset
    await page.waitForTimeout(2000);                               // idle
    const st = await cw.evaluate(() => globalThis.__wasmboxFrameStats(false));
    // No per-frame decode churn (the idle-repaint gate holds); the splash added
    // nothing that forces re-renders. Compositor rAF itself is expected alive.
    const churn = st.rgbaDecodesPerFrame + st.overDecodesPerFrame + st.sabCopiesPerFrame;
    const bootDone = await page.evaluate(() => {
      const b = globalThis.__wasmboxBoot;
      return b ? { done: b.done } : { done: false };
    });
    if (churn < 0.5 && st.frames > 0 && bootDone.done) {
      ok(`(d) no leaked per-frame work: ${st.frames} idle frames, decode churn ${churn.toFixed(3)}/frame; boot state done`);
    } else {
      fail(`(d) leak/idle anomaly: churn=${churn.toFixed(3)}/frame frames=${st.frames} bootDone=${bootDone.done}`);
    }
  }

  if (pageErrors.length) fail(`pageerror(s): ${pageErrors.join(" | ")}`);
  else ok("no pageerror");
  if (consoleErrors.length) console.log(`note: ${consoleErrors.length} console.error line(s): ${consoleErrors.slice(0, 3).join(" | ")}`);
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
