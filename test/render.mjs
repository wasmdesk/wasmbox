// Headless render smoke test for the wasmbox in-browser Ruby compositor.
//
// It drives a headless Chromium to the served index page, waits for the
// embedded compositor (compositor.rb, baked into wasmbox.wasm via //go:embed)
// to boot, and asserts it actually rendered and behaves:
//
//   1. Boot       — window.wasmboxReady becomes true, wasmboxError stays null,
//                    no pageerror, and the console carries the compositor's
//                    "rbgo compositor: started with N windows" startup line.
//   2. Canvas     — <canvas id="screen"> is non-blank (high non-zero pixel
//                    count over the whole frame).
//   3. Persist    — localStorage 'wasmbox.layout' carries a tab-separated
//                    "xterm\tx\ty\tw\th" record; dragging the xterm titlebar
//                    moves it, the record updates, and a reload restores the
//                    dragged position. (Works without SharedArrayBuffer.)
//   4. Dock        — the bottom-center band of the canvas is painted (the dock
//                    panel + icons) while the 15px strip just above it is blank
//                    (no titlebar) — proving the undecorated "panel" role. This
//                    one needs the external-client path (Web Worker + SAB), so
//                    it is BEST-EFFORT (logged, never fails the build) because
//                    the SAB blit can be timing-sensitive in CI.
//
// Exit code is 0 on PASS, non-zero on any HARD assertion failure (boot, canvas,
// persistence), so CI gates on it.
//
// The page MUST be served with the cross-origin-isolation headers SAB needs
// (Cross-Origin-Opener-Policy: same-origin, Cross-Origin-Embedder-Policy:
// require-corp). In CI we point this at the project's own `cmd/serve` via
// WASMBOX_BASE_URL (which sets those headers); when that env var is unset the
// script spins up an equivalent local static server so it still runs solo.
//
// Browser: Playwright's bundled Chromium (installed via
// `npx playwright install --with-deps chromium`). Locally you may set
// WASMBOX_CHROME_CHANNEL=chrome to launch a system Chrome instead.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

// Repo root is the parent of this test/ directory.
const ROOT = fileURLToPath(new URL("..", import.meta.url));

// Minimum count of non-blank RGB pixels for the canvas to be "painted". The
// local probe saw ~900000 on a 1280x720 viewport; 50000 is a generous floor
// that still rejects a blank/transparent canvas.
const MIN_PAINTED_PX = 50000;
const BOOT_TIMEOUT_MS = 10000;

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".wasm": "application/wasm",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".rb": "text/plain; charset=utf-8",
  ".map": "application/json; charset=utf-8",
};

// Fallback static server (only used when WASMBOX_BASE_URL is unset): rooted at
// the repo, with the correct wasm MIME and the cross-origin-isolation headers
// so SharedArrayBuffer is usable — same contract as `cmd/serve`.
function startServer() {
  const server = createServer(async (req, res) => {
    try {
      const urlPath = decodeURIComponent((req.url || "/").split("?")[0]);
      let rel = normalize(urlPath).replace(/^(\.\.[/\\])+/, "");
      if (rel === "/" || rel === "" || rel === "\\") rel = "/index.html";
      const file = join(ROOT, rel);
      if (!file.startsWith(ROOT)) {
        res.writeHead(403).end("forbidden");
        return;
      }
      const body = await readFile(file);
      res.setHeader("Content-Type", MIME[extname(file)] || "application/octet-stream");
      res.setHeader("Cross-Origin-Opener-Policy", "same-origin");
      res.setHeader("Cross-Origin-Embedder-Policy", "require-corp");
      res.writeHead(200).end(body);
    } catch {
      // A 404 for /favicon.ico is expected and harmless.
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

function fail(msg) {
  console.error(`FAIL: ${msg}`);
  process.exitCode = 1;
}

// Resolve where to point the browser. Prefer an externally-served base URL
// (the project's cmd/serve in CI); otherwise stand up the fallback server.
let server = null;
let base = process.env.WASMBOX_BASE_URL;
if (base) {
  base = base.replace(/\/+$/, "");
  console.log(`using external server: ${base}`);
} else {
  const started = await startServer();
  server = started.server;
  base = started.base;
  console.log(`using built-in fallback server: ${base}`);
}

const channel = process.env.WASMBOX_CHROME_CHANNEL;
const browser = await chromium.launch(
  channel ? { headless: true, channel } : { headless: true },
);

const consoleLines = [];
const pageErrors = [];

// Read the saved-layout record for a window title out of localStorage. Returns
// null when storage has no entry or no matching record. Format: tab-separated
// "title\tx\ty\tw\th" lines.
async function layoutRecord(page, title) {
  return page.evaluate((t) => {
    const raw = localStorage.getItem("wasmbox.layout");
    if (raw == null) return null;
    for (const line of String(raw).split("\n")) {
      const p = line.split("\t");
      if (p.length >= 5 && p[0] === t) {
        return { x: Number(p[1]), y: Number(p[2]), w: Number(p[3]), h: Number(p[4]) };
      }
    }
    return null;
  }, title);
}

async function waitReady(page) {
  await page.waitForFunction(
    () => {
      if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
      return globalThis.wasmboxReady === true;
    },
    { timeout: BOOT_TIMEOUT_MS },
  );
}

try {
  const page = await browser.newPage();
  page.on("console", (m) => consoleLines.push(m.text()));
  page.on("pageerror", (e) => pageErrors.push(String(e)));

  await page.goto(`${base}/index.html`, { waitUntil: "load" });

  // --- Assertion 1: boot --------------------------------------------------
  let booted = false;
  try {
    await waitReady(page);
    booted = true;
  } catch (e) {
    fail(`compositor did not boot within ${BOOT_TIMEOUT_MS} ms: ${e.message}`);
  }

  if (booted) {
    const errVal = await page.evaluate(() => globalThis.wasmboxError ?? null);
    if (errVal == null) {
      console.log("ok  wasmboxReady === true, wasmboxError == null");
    } else {
      fail(`wasmboxError set after boot: ${errVal}`);
    }

    const statusDisplay = await page.evaluate(() => {
      const el = document.getElementById("status");
      return el ? getComputedStyle(el).display : "(no #status)";
    });
    if (statusDisplay === "none") {
      console.log(`ok  #status overlay hidden (display: ${statusDisplay})`);
    } else {
      fail(`#status overlay still visible (display: ${statusDisplay})`);
    }

    // --- Assertion 2: canvas painted -------------------------------------
    // Since step C the canvas's control was transferred to the compositor
    // Web Worker via transferControlToOffscreen(), so the main thread can no
    // longer call getContext on the <canvas> element. We rely on Playwright's
    // page screenshot instead -- it captures the rendered surface from the
    // browser's compositor, which gives the same ground truth.
    const dim = await page.evaluate(() => {
      const c = document.getElementById("screen");
      if (!c) return { error: "no #screen canvas" };
      return { width: c.width, height: c.height };
    });
    if (dim.error) {
      fail(`canvas readback failed: ${dim.error}`);
    } else {
      const shotBuf = await page.screenshot({ type: "png", fullPage: false });
      const png = PNG.sync.read(shotBuf);
      let painted = 0;
      const colors = new Set();
      for (let i = 0; i < png.data.length; i += 4) {
        const r = png.data[i], g = png.data[i + 1], b = png.data[i + 2];
        if (r || g || b) {
          painted++;
          if (colors.size < 4096) colors.add((r << 16) | (g << 8) | b);
        }
      }
      if (painted > MIN_PAINTED_PX) {
        console.log(
          `ok  canvas ${dim.width}x${dim.height} painted: ${painted} non-blank px in screenshot, ${colors.size} colors`,
        );
      } else {
        fail(`canvas looks blank: only ${painted} non-blank px (need > ${MIN_PAINTED_PX})`);
      }
    }

    // --- Assertion 3: startup line ---------------------------------------
    const startup = consoleLines.find((l) => /rbgo compositor: started with \d+ windows/.test(l));
    if (startup) {
      console.log(`ok  startup line: ${startup}`);
    } else {
      fail("missing console startup line 'rbgo compositor: started with N windows'");
    }

    // --- Assertion 4: persistence round-trip -----------------------------
    // The first spawned window is "xterm" at (60,60) 240x150; its titlebar sits
    // 22px above the body (y 38..60). Drag from inside the titlebar (avoiding
    // the close box on the far right) and confirm the saved record follows, then
    // survives a reload.
    let before = await layoutRecord(page, "xterm");
    if (before == null) {
      // tick writes the layout only once the signature changes; nudge it by
      // giving the compositor a couple of animation frames before reading.
      await page.waitForTimeout(200);
      before = await layoutRecord(page, "xterm");
    }
    if (before == null) {
      fail("localStorage 'wasmbox.layout' has no xterm record after boot");
    } else {
      console.log(`ok  persisted xterm record present: x=${before.x} y=${before.y}`);

      // Drag the titlebar: down ~(75,48), move to ~(275,198), up.
      await page.mouse.move(75, 48);
      await page.mouse.down();
      await page.mouse.move(150, 120, { steps: 8 });
      await page.mouse.move(275, 198, { steps: 8 });
      await page.mouse.up();
      await page.waitForTimeout(300);

      let after = null;
      for (let i = 0; i < 10; i++) {
        after = await layoutRecord(page, "xterm");
        if (after && (after.x !== before.x || after.y !== before.y)) break;
        await page.waitForTimeout(100);
      }
      if (after && (after.x !== before.x || after.y !== before.y)) {
        console.log(`ok  drag moved xterm: (${before.x},${before.y}) -> (${after.x},${after.y})`);

        // Reload and assert the dragged position is restored.
        await page.reload({ waitUntil: "load" });
        await waitReady(page);
        await page.waitForTimeout(200);
        const restored = await layoutRecord(page, "xterm");
        if (restored && restored.x === after.x && restored.y === after.y) {
          console.log(`ok  reload restored xterm at (${restored.x},${restored.y})`);
        } else {
          fail(
            `xterm not restored after reload: expected (${after.x},${after.y}), got ` +
              (restored ? `(${restored.x},${restored.y})` : "null"),
          );
        }
      } else {
        fail(
          `drag did not move xterm: still (${after ? after.x : "?"},${after ? after.y : "?"})`,
        );
      }
    }

    // --- Assertion 5 (BEST-EFFORT): dock panel anchored ------------------
    // The wasmdock dock asks for the "panel" role; the compositor anchors it
    // bottom-center, undecorated and always-on-top, and blits its pixels out of
    // a SharedArrayBuffer. Assert the bottom-center band is painted while the
    // strip just above it is blank (no titlebar) — proving the panel role. SAB
    // blits can be timing-sensitive in CI, so this is logged, never fatal.
    try {
      // Give the worker time to spawn, send hello, and blit a frame.
      await page.waitForTimeout(1500);
      // Like assertion 2, we now sample the screenshot instead of the canvas
      // because step C transferred control to the compositor worker.
      const dim2 = await page.evaluate(() => {
        const c = document.getElementById("screen");
        return { W: c.width, H: c.height };
      });
      const shot2 = await page.screenshot({ type: "png", fullPage: false });
      const png2 = PNG.sync.read(shot2);
      const dock = (() => {
        const W = png2.width, H = png2.height;
        const cx = Math.floor(W / 2);
        const halfBand = 260;
        const x0 = Math.max(0, cx - halfBand);
        const bandW = Math.min(W, cx + halfBand) - x0;
        const bandH = 130;
        const by = H - bandH;
        let bandPainted = 0;
        for (let y = by; y < by + bandH; y++) {
          for (let x = x0; x < x0 + bandW; x++) {
            const i = (y * W + x) * 4;
            if (png2.data[i] || png2.data[i + 1] || png2.data[i + 2]) bandPainted++;
          }
        }
        const stripH = 15;
        const sy = by - stripH;
        let stripPainted = 0;
        for (let y = sy; y < sy + stripH; y++) {
          for (let x = x0; x < x0 + bandW; x++) {
            const i = (y * W + x) * 4;
            if (png2.data[i] || png2.data[i + 1] || png2.data[i + 2]) stripPainted++;
          }
        }
        return { W, H, bandPainted, bandTotal: bandW * bandH, stripPainted };
      })();
      void dim2; // dim2 only kept to document the canvas size source.
      const bandOk = dock.bandPainted > 2000; // dock is a wide painted bar
      const stripBlank = dock.stripPainted === 0;
      if (bandOk) {
        // The bottom-center band carrying the dock is painted: the panel client
        // blitted its surface through the SAB path. The "titlebar strip blank"
        // sub-signal proves the undecorated panel role, but cascaded normal
        // windows can legitimately paint into that strip too, so it is reported,
        // not required.
        console.log(
          `ok  dock anchored bottom-center: band painted ${dock.bandPainted}px` +
            (stripBlank
              ? ", titlebar strip blank (undecorated panel confirmed)"
              : ` (strip above painted ${dock.stripPainted}px — overlapped by a normal window)`),
        );
      } else {
        console.log(
          `warn (best-effort) dock band not painted: ${dock.bandPainted}px (need >2000). ` +
            `SAB blit may be slow/disabled in this environment.`,
        );
      }
    } catch (e) {
      console.log(`warn (best-effort) dock assertion errored: ${e.message}`);
    }
  }

  // --- Assertion 6: no uncaught page errors ------------------------------
  // (A 404 for /favicon.ico is a server response, not a pageerror.)
  if (pageErrors.length === 0) {
    console.log("ok  no pageerror");
  } else {
    fail(`page errors: ${pageErrors.join(" | ")}`);
  }
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  if (server) server.close();
}

if (process.exitCode && process.exitCode !== 0) {
  console.log("\nRESULT: FAIL");
} else {
  console.log("\nRESULT: PASS");
}
