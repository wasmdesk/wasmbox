// End-to-end probe for Batch 2 of the vscodium integration: spawn a
// dom-window pointing at a local code-server, wait for the iframe to
// load the VS Code workbench, assert the iframe document contains the
// expected code-server / VS Code markers.
//
// Preconditions: code-server is running on http://127.0.0.1:8443/ with
// --auth none. The compositor's LAUNCHABLE registry has a "vscode"
// entry with the dom: descriptor, but for this probe we drive the
// public hook directly so the test doesn't depend on the dock surface
// or root-menu listing.
//
// Runs against chromium + firefox + webkit. A regression that breaks
// the chain (M2C dispatch / Ruby spawn_dom_window / JS overlay / iframe
// load) fails the probe.

import { firefox, chromium, webkit } from "playwright";

const base = process.env.WASMBOX_BASE_URL || "http://127.0.0.1:8080";
const codeServer = process.env.CODE_SERVER_URL || "http://127.0.0.1:8443/";
const wait = (ms) => new Promise(r => setTimeout(r, ms));

async function probe(name, launcher) {
  console.log(`\n=== ${name} ===`);
  const browser = await launcher.launch({ headless: true });
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const page = await ctx.newPage();
  const consoleLines = [];
  page.on("console", (m) => consoleLines.push(`[${m.type()}] ${m.text().slice(0, 200)}`));
  page.on("pageerror", (e) => consoleLines.push(`[err] ${e}`));

  let result = { name, ok: false, reason: "" };
  try {
    await page.goto(base + "/", { waitUntil: "load", timeout: 30000 });
    await page.waitForFunction(() => !!window.wasmboxReady, { timeout: 30000 });
    await wait(800);

    await page.evaluate((url) =>
      globalThis.wasmboxSpawnDOMWindow(url, 1100, 700, "VS Code"),
      codeServer);

    // Wait for the iframe element + its document to load.
    const iframe = await page.waitForSelector(
      "#__wasmbox_iframes iframe[data-window-id]",
      { state: "attached", timeout: 15000 });
    console.log(`  ${name} iframe element attached`);

    // Probe inside the iframe for a code-server / VS Code marker.
    // code-server's index HTML carries `<title>code-server</title>` +
    // a workbench root with a known class. We just check the title
    // (it's set before the workbench finishes booting + works even
    // on slow boxes / under headless rendering).
    const frame = await iframe.contentFrame();
    if (!frame) {
      result.reason = "iframe.contentFrame() returned null";
      return result;
    }
    // code-server's first GET on / 302s to /?folder=/tmp. Give it
    // some time to land on the workbench page.
    const title = await frame.waitForFunction(
      () => document.title || "",
      null,
      { timeout: 30000, polling: 500 }
    ).then(h => h.jsonValue()).catch((e) => `<timeout: ${e.message}>`);
    console.log(`  ${name} iframe title: ${JSON.stringify(title)}`);

    if (typeof title !== "string" || title.length === 0) {
      result.reason = `iframe title empty / unavailable: ${title}`;
    } else if (!/code/i.test(title)) {
      result.reason = `iframe title doesn't look like code-server: ${title}`;
    } else {
      result.ok = true;
    }
  } catch (e) {
    result.reason = String(e).split("\n")[0];
    if (process.env.DUMP_CONSOLE) {
      for (const l of consoleLines.slice(-20)) console.log("  " + l);
    }
  } finally {
    await browser.close();
  }
  return result;
}

const results = [
  await probe("chromium", chromium),
  await probe("firefox", firefox),
  await probe("webkit", webkit),
];

console.log("\n=== SUMMARY ===");
let fail = false;
for (const r of results) {
  if (r.ok) console.log(`PASS: ${r.name}`);
  else { console.log(`FAIL: ${r.name} -- ${r.reason}`); fail = true; }
}
if (fail) { console.error("\nprobe-vscode-iframe: OVERALL FAIL"); process.exit(1); }
console.log("\nprobe-vscode-iframe: OVERALL PASS");
