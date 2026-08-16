// SPDX-License-Identifier: BSD-3-Clause
//
// Headless Playwright probe for the ACCESSIBILITY (a11y) ARIA bridge:
// compositor/05_a11y.rb (the pure ARIA projection of the window stack) +
// compositor/18_a11y.rb (the JS-side publish + action dispatch + tick chain),
// compositor.worker.js (wasmboxA11yPublish / wasmboxA11yAction) and
// a11y-bridge.js (the main-thread ARIA DOM reconciler).
//
// The compositor paints the whole desktop onto an OffscreenCanvas — a screen
// reader sees no text there. This feature mirrors the window tree into a LIVE
// ARIA element tree (role=application > role=window group > <button> controls)
// that the browser accessibility API — the SAME api VoiceOver / Orca / NVDA read
// — exposes. This probe proves BOTH directions of the AT-SPI-style bridge:
//
//   READ  : the ARIA tree is present + correct in the accessibility API
//           (root application, per-window group with a "window" roledescription
//           and label, and Activate/Minimize/Close buttons).
//   DRIVE : activating a control (as a screen reader would — a synthetic click)
//           travels main -> worker -> Ruby event bus -> WindowManager and the
//           REAL WM state changes, which republishes the tree and updates the
//           DOM. All four dispatch arms are exercised: focus, minimize, restore,
//           close.
//
// CONTROL-FIRST (feedback-control-run-new-instruments): before trusting the
// accessibility-snapshot instrument on our own tree, it is validated against a
// KNOWN-GOOD control — a plain <button aria-label=...> injected into the page —
// so a broken snapshot reader cannot masquerade as a broken subject.
//
// Run: WASMBOX from a built compositor (task build:compositor), then
//   node test/probe-a11y.mjs

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 20000;
const VIEW_W = 1280;
const VIEW_H = 800;

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js":   "text/javascript; charset=utf-8",
  ".mjs":  "text/javascript; charset=utf-8",
  ".wasm": "application/wasm",
  ".css":  "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".rb":   "text/plain; charset=utf-8",
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
      res.setHeader("Cross-Origin-Opener-Policy", "same-origin");
      res.setHeader("Cross-Origin-Embedder-Policy", "require-corp");
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
function ok(msg)   { console.log(`ok  ${msg}`); }
const settle = (page, ms) => page.waitForTimeout(ms);

// Find the compositor worker (it owns __wasmboxSpawnWindow, the in-process
// window spawn hook we use to place deterministic, known-title windows).
async function compositorWorker(page) {
  for (const w of page.workers()) {
    try {
      const has = await w.evaluate(
        () => typeof globalThis.__wasmboxSpawnWindow === "function" &&
              typeof globalThis.wasmboxA11yPublish === "function",
      );
      if (has) return w;
    } catch (_) { /* not it */ }
  }
  return null;
}

// Read our ARIA DOM into a plain object (roles, labels, focus, per-window
// actions). This is the DOM the browser derives the accessibility tree from.
const readA11y = (page) => page.evaluate(() => {
  const root = document.getElementById("wasmbox-a11y-root");
  if (!root) return null;
  const groups = Array.from(root.querySelectorAll(':scope > [role="group"]'));
  const wins = groups.map((w) => ({
    domId: w.id,
    winId: w.querySelector("button[data-a11y-id]")
      ? parseInt(w.querySelector("button[data-a11y-id]").dataset.a11yId, 10)
      : null,
    label: w.getAttribute("aria-label"),
    roledesc: w.getAttribute("aria-roledescription"),
    current: w.getAttribute("aria-current"),
    minimized: w.dataset.minimized,
    actions: Array.from(w.querySelectorAll("button[data-a11y-action]")).map((b) => ({
      action: b.dataset.a11yAction,
      label: b.getAttribute("aria-label"),
    })),
  }));
  return {
    role: root.getAttribute("role"),
    label: root.getAttribute("aria-label"),
    active: root.getAttribute("aria-activedescendant"),
    wins,
  };
});

// Activate a window control the way a screen reader does: a synthetic click on
// the real <button>. Returns whether the button was found + clicked.
const invoke = (page, winId, action) => page.evaluate(
  ({ winId, action }) => globalThis.WASMBOX_A11Y.invoke(winId, action),
  { winId, action });

const winByLabel = (tree, label) => tree && tree.wins.find((w) => w.label === label);
const actionNames = (win) => win.actions.map((a) => a.action).sort().join(",");

// The role+name ARIA tree exactly as an assistive technology consumes it,
// rendered by Playwright from the browser's accessibility tree (YAML string,
// e.g. `- application "wasmbox desktop":` / `- button "Close a11y-beta"`).
const ariaSnapshot = (page) => page.locator("body").ariaSnapshot();

const { server, base } = await startServer();
console.log(`probe-a11y: serving on ${base}`);

const browser = await chromium.launch({ headless: true });
const pageErrors = [];

try {
  const page = await browser.newPage({ viewport: { width: VIEW_W, height: VIEW_H } });
  page.on("pageerror", (e) => pageErrors.push(String(e)));

  await page.goto(`${base}/index.html`, { waitUntil: "load" });
  await page.waitForFunction(
    () => {
      if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
      return globalThis.wasmboxReady === true;
    },
    { timeout: BOOT_TIMEOUT_MS },
  );
  ok("compositor booted (a11y path did not throw at boot)");
  await settle(page, 2000); // let boot clients + the dock settle

  // -- CONTROL-FIRST: validate the accessibility-snapshot instrument ---------
  // Inject a known-good control and confirm the snapshot reader reports its
  // role + name. If THIS fails, the instrument is broken and every reading
  // below would be worthless — so we bail rather than draw conclusions.
  await page.evaluate(() => {
    const b = document.createElement("button");
    b.id = "__a11y_control";
    b.setAttribute("aria-label", "a11y-control-known");
    b.textContent = "control";
    document.body.appendChild(b);
  });
  {
    const snap = await ariaSnapshot(page);
    if (!snap.includes('button "a11y-control-known"')) {
      fail("CONTROL: ARIA snapshot did not report a known <button aria-label='a11y-control-known'> — instrument is broken, aborting");
      throw new Error("instrument control failed");
    }
    ok("CONTROL: ARIA snapshot correctly reports a known button (instrument validated)");
  }
  await page.evaluate(() => document.getElementById("__a11y_control").remove());

  // -- find the compositor worker + spawn two deterministic windows ----------
  const cw = await compositorWorker(page);
  if (!cw) { fail("could not find the compositor worker"); throw new Error("no worker"); }

  await cw.evaluate(() => globalThis.__wasmboxSpawnWindow("a11y-alpha"));
  await settle(page, 300);
  await cw.evaluate(() => globalThis.__wasmboxSpawnWindow("a11y-beta"));
  await settle(page, 500); // let the tick publish the new ARIA tree

  // -- READ: the ARIA tree is present + correct ------------------------------
  const t0 = await readA11y(page);
  if (!t0) { fail("no #wasmbox-a11y-root in the DOM — the bridge did not build a tree"); throw new Error("no tree"); }
  if (t0.role === "application" && t0.label === "wasmbox desktop") {
    ok(`ARIA root present: role=${t0.role} label="${t0.label}"`);
  } else {
    fail(`ARIA root wrong: role=${t0.role} label="${t0.label}"`);
  }

  // Optional capture of the literal SR-perceived ARIA tree, for evidence.
  if (process.env.A11Y_DUMP) {
    const { writeFile } = await import("node:fs/promises");
    await writeFile(process.env.A11Y_DUMP, await ariaSnapshot(page));
    console.log(`info wrote ARIA snapshot to ${process.env.A11Y_DUMP}`);
  }

  const alpha = winByLabel(t0, "a11y-alpha");
  const beta = winByLabel(t0, "a11y-beta");
  if (!alpha || !beta) {
    fail(`spawned windows missing from ARIA tree (labels: ${t0.wins.map((w) => w.label).join(" | ")})`);
    throw new Error("windows missing");
  }
  ok(`both spawned windows are ARIA nodes: "${alpha.label}" (win ${alpha.winId}), "${beta.label}" (win ${beta.winId})`);

  if (alpha.roledesc === "window") ok('window nodes carry aria-roledescription="window"');
  else fail(`window node missing roledescription: got "${alpha.roledesc}"`);

  if (actionNames(alpha) === "close,focus,minimize") {
    ok(`live window exposes 3 activatable controls: ${alpha.actions.map((a) => a.label).join(", ")}`);
  } else {
    fail(`live window action set wrong: ${actionNames(alpha)}`);
  }

  // The newest spawn (beta) is focused: the root points at it + it is aria-current.
  if (t0.active === beta.domId && beta.current === "true") {
    ok(`focus reflected: root aria-activedescendant -> "${beta.label}", node aria-current=true`);
  } else {
    fail(`focus not reflected: active=${t0.active} beta.dom=${beta.domId} beta.current=${beta.current}`);
  }

  // The ARIA SNAPSHOT (the SR-perceived tree) shows the app root + the window
  // controls as named buttons.
  {
    const snap = await ariaSnapshot(page);
    if (snap.includes('application "wasmbox desktop"')) ok('ARIA snapshot exposes the desktop as application "wasmbox desktop"');
    else fail("ARIA snapshot missing the application root");
    if (snap.includes('button "Close a11y-beta"')) ok('ARIA snapshot exposes "Close a11y-beta" as an operable button');
    else fail('ARIA snapshot missing the "Close a11y-beta" button');
  }

  // -- DRIVE: activate controls; assert the REAL WM changed ------------------
  // (a) FOCUS: activate alpha -> alpha becomes the focused/current window.
  if (!(await invoke(page, alpha.winId, "focus"))) fail("focus button not found");
  await settle(page, 500);
  {
    const t = await readA11y(page);
    const a = winByLabel(t, "a11y-alpha");
    if (t.active === a.domId && a.current === "true") {
      ok('DRIVE focus: activating "a11y-alpha" moved focus to it (root activedescendant + aria-current)');
    } else {
      fail(`DRIVE focus failed: active=${t.active} alpha.dom=${a.domId} alpha.current=${a.current}`);
    }
  }

  // (b) MINIMIZE: minimize alpha -> node flagged minimized, action set swaps to restore.
  if (!(await invoke(page, alpha.winId, "minimize"))) fail("minimize button not found");
  await settle(page, 500);
  {
    const t = await readA11y(page);
    const a = winByLabel(t, "a11y-alpha");
    if (a && a.minimized === "true" && actionNames(a) === "close,focus,restore") {
      ok('DRIVE minimize: "a11y-alpha" is minimized and now offers Restore instead of Minimize');
    } else {
      fail(`DRIVE minimize failed: minimized=${a && a.minimized} actions=${a && actionNames(a)}`);
    }
  }

  // (c) RESTORE: restore alpha -> flag clears, action set swaps back to minimize.
  if (!(await invoke(page, alpha.winId, "restore"))) fail("restore button not found");
  await settle(page, 500);
  {
    const t = await readA11y(page);
    const a = winByLabel(t, "a11y-alpha");
    if (a && a.minimized === "false" && actionNames(a) === "close,focus,minimize") {
      ok('DRIVE restore: "a11y-alpha" is restored and offers Minimize again');
    } else {
      fail(`DRIVE restore failed: minimized=${a && a.minimized} actions=${a && actionNames(a)}`);
    }
  }

  // (d) CLOSE: close beta -> its node disappears from the tree. My click handler
  // only POSTS the action; the node is removed only because Ruby closed the
  // window and republished the tree without it — so removal proves the full
  // main->worker->WM->republish round-trip.
  const beforeClose = await readA11y(page);
  const betaNow = winByLabel(beforeClose, "a11y-beta");
  if (!(await invoke(page, betaNow.winId, "close"))) fail("close button not found");
  await settle(page, 600);
  {
    const t = await readA11y(page);
    if (!winByLabel(t, "a11y-beta")) {
      ok('DRIVE close: activating "Close a11y-beta" closed the window (its ARIA node is gone) — full round-trip verified');
    } else {
      fail('DRIVE close failed: "a11y-beta" is still in the ARIA tree');
    }
    // The SR-perceived snapshot no longer offers the closed window's button.
    const snap = await ariaSnapshot(page);
    if (!snap.includes('button "Close a11y-beta"')) {
      ok("ARIA snapshot no longer exposes the closed window's Close button");
    } else {
      fail('ARIA snapshot still exposes "Close a11y-beta" after close');
    }
  }

  // Client-asset load races (a spawned boot client's .wasm not yet served) are
  // unrelated to the a11y bridge under test.
  const fatal = pageErrors.filter((e) => !/bootWasm: HTTP \d+ for/.test(e));
  if (fatal.length) fail(`pageerror(s): ${fatal.join(" | ")}`);
  else ok("no fatal pageerror");
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
