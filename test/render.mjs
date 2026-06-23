// Headless render smoke test for the wasmbox in-browser Ruby compositor.
//
// It serves the repo root over http (wasm needs the application/wasm MIME type;
// file:// will not instantiate), drives a headless Chromium to the index page,
// waits for the embedded compositor to boot, and asserts it actually rendered:
//
//   * the #status loader overlay computes to display:none once booted,
//   * the <canvas id="screen"> is non-blank (enough non-zero RGB pixels),
//   * no uncaught pageerror fired,
//   * the console carries the compositor's startup line
//     ("rbgo compositor: started with N windows").
//
// Exit code is 0 on PASS, non-zero on any failed assertion, so CI gates on it.
//
// In CI this uses Playwright's bundled Chromium (installed via
// `npx playwright install --with-deps chromium`). Locally you may pass
// WASMBOX_CHROME_CHANNEL=chrome to launch a system Chrome instead.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

// Repo root is the parent of this test/ directory.
const ROOT = fileURLToPath(new URL("..", import.meta.url));

// Minimum count of non-blank RGB pixels for the canvas to be "painted". The
// local probe saw ~792000 on an 1100x720 desktop; 50000 is a generous floor
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

// Tiny static file server rooted at the repo, with the correct wasm MIME and
// the cross-origin-isolation headers the page documents (harmless here, but it
// keeps the served environment faithful to `task serve`).
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

const { server, base } = await startServer();
const channel = process.env.WASMBOX_CHROME_CHANNEL;
const browser = await chromium.launch(
  channel ? { headless: true, channel } : { headless: true },
);

const consoleLines = [];
const pageErrors = [];

try {
  const page = await browser.newPage();
  page.on("console", (m) => consoleLines.push(m.text()));
  page.on("pageerror", (e) => pageErrors.push(String(e)));

  await page.goto(`${base}/index.html`, { waitUntil: "load" });

  // Poll up to BOOT_TIMEOUT_MS for main() to publish the ready flag. Surface a
  // Ruby boot error eagerly rather than waiting out the full timeout.
  let booted = false;
  try {
    await page.waitForFunction(
      () => {
        if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
        return globalThis.wasmboxReady === true;
      },
      { timeout: BOOT_TIMEOUT_MS },
    );
    booted = true;
  } catch (e) {
    fail(`compositor did not boot within ${BOOT_TIMEOUT_MS} ms: ${e.message}`);
  }

  if (booted) {
    // Assertion 1: the loader overlay is hidden (display:none) once booted.
    const statusDisplay = await page.evaluate(() => {
      const el = document.getElementById("status");
      return el ? getComputedStyle(el).display : "(no #status)";
    });
    if (statusDisplay === "none") {
      console.log(`ok  #status overlay computed display: ${statusDisplay}`);
    } else {
      fail(`#status overlay still visible (display: ${statusDisplay})`);
    }

    // Assertion 2: the canvas is non-blank. Count pixels with any non-zero RGB
    // channel and tally distinct colours as a sanity signal.
    const px = await page.evaluate(() => {
      const c = document.getElementById("screen");
      if (!c) return { error: "no #screen canvas" };
      const ctx = c.getContext("2d");
      if (!ctx) return { error: "no 2d context" };
      const { width, height } = c;
      if (!width || !height) return { error: `zero-sized canvas ${width}x${height}` };
      const data = ctx.getImageData(0, 0, width, height).data;
      let painted = 0;
      const colors = new Set();
      for (let i = 0; i < data.length; i += 4) {
        const r = data[i], g = data[i + 1], b = data[i + 2];
        if (r || g || b) {
          painted++;
          if (colors.size < 4096) colors.add((r << 16) | (g << 8) | b);
        }
      }
      return { width, height, painted, colors: colors.size };
    });
    if (px.error) {
      fail(`canvas readback failed: ${px.error}`);
    } else if (px.painted > MIN_PAINTED_PX) {
      console.log(
        `ok  canvas ${px.width}x${px.height} painted: ${px.painted} non-blank px, ${px.colors} colors`,
      );
    } else {
      fail(`canvas looks blank: only ${px.painted} non-blank px (need > ${MIN_PAINTED_PX})`);
    }

    // Assertion 3: the compositor logged its startup line.
    const startup = consoleLines.find((l) => /rbgo compositor: started with \d+ windows/.test(l));
    if (startup) {
      console.log(`ok  startup line: ${startup}`);
    } else {
      fail("missing console startup line 'rbgo compositor: started with N windows'");
    }
  }

  // Assertion 4: no uncaught page errors. (A 404 for /favicon.ico is a server
  // response, not a pageerror, so it does not show up here.)
  if (pageErrors.length === 0) {
    console.log("ok  no pageerror");
  } else {
    fail(`page errors: ${pageErrors.join(" | ")}`);
  }
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

if (process.exitCode && process.exitCode !== 0) {
  console.log("\nRESULT: FAIL");
} else {
  console.log("\nRESULT: PASS");
}
