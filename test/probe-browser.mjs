// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Headless Playwright probe for the streaming WhiteSur/Safari browser client
// (clients/browser). The client is a thin front-end for the go-webengine
// browserproxy: it opens a WebSocket, blits streamed PNG frames into its
// content-area SAB canvas, and forwards navigation/clicks/keys back. This probe
// stands up a MINIMAL mock browserproxy WebSocket server (no external deps —
// raw RFC6455 over node:http + node:crypto) that streams a solid RED frame on
// connect/resize and a solid GREEN frame after a navigate. It then:
//
//   1. boots the compositor, spawns the Browser window, and asserts the RED
//      frame blitted into the content area (the streaming blit path works, and
//      a WebSocket escaped COEP:require-corp exactly as designed);
//   2. clicks the address bar, types a URL and presses Enter, and asserts the
//      content turns GREEN — proving the address bar accepts input and drives a
//      {navigate} that streams a fresh frame back into the canvas.
//
// A screenshot is saved to /tmp/browser-streaming.png.

import { createServer } from "node:http";
import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const SHOT = "/tmp/browser-streaming.png";
const WS_PORT = Number(process.env.BROWSERPROXY_PORT || 8090); // client default
const FRAME_W = 760;
const FRAME_H = 454; // 760x500 window − 46px toolbar
const RED = [220, 40, 40];
const GREEN = [40, 190, 90];
const TOOLBAR_H = 46;

const MIME = {
  ".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8", ".wasm": "application/wasm",
  ".css": "text/css; charset=utf-8", ".json": "application/json; charset=utf-8",
  ".rb": "text/plain; charset=utf-8",
};

// --- minimal WebSocket mock proxy -----------------------------------------

const solidPNG = (w, h, rgb) => {
  const png = new PNG({ width: w, height: h });
  for (let i = 0; i < png.data.length; i += 4) {
    png.data[i] = rgb[0]; png.data[i + 1] = rgb[1]; png.data[i + 2] = rgb[2]; png.data[i + 3] = 255;
  }
  return PNG.sync.write(png).toString("base64");
};

const wsAccept = (key) =>
  createHash("sha1").update(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").digest("base64");

const encodeText = (str) => {
  const payload = Buffer.from(str, "utf8");
  const len = payload.length;
  let header;
  if (len < 126) header = Buffer.from([0x81, len]);
  else if (len < 65536) { header = Buffer.alloc(4); header[0] = 0x81; header[1] = 126; header.writeUInt16BE(len, 2); }
  else { header = Buffer.alloc(10); header[0] = 0x81; header[1] = 127; header.writeUInt32BE(0, 2); header.writeUInt32BE(len, 6); }
  return Buffer.concat([header, payload]);
};

// decodeFrames parses complete client frames from one TCP chunk (client
// messages are tiny, so each arrives whole). Returns {text} or {close}.
const decodeFrames = (buf) => {
  const out = [];
  let i = 0;
  while (i + 2 <= buf.length) {
    const opcode = buf[i] & 0x0f;
    const masked = (buf[i + 1] & 0x80) !== 0;
    let len = buf[i + 1] & 0x7f;
    i += 2;
    if (len === 126) { len = buf.readUInt16BE(i); i += 2; }
    else if (len === 127) { i += 4; len = buf.readUInt32BE(i); i += 4; }
    let mask;
    if (masked) { mask = buf.subarray(i, i + 4); i += 4; }
    const payload = Buffer.from(buf.subarray(i, i + len));
    i += len;
    if (masked) for (let j = 0; j < payload.length; j++) payload[j] ^= mask[j & 3];
    if (opcode === 0x8) out.push({ close: true });
    else if (opcode === 0x1) out.push({ text: payload.toString("utf8") });
  }
  return out;
};

function startWS(port, redB64, greenB64) {
  const server = createServer();
  server.on("upgrade", (req, socket) => {
    socket.write(
      "HTTP/1.1 101 Switching Protocols\r\n" +
      "Upgrade: websocket\r\nConnection: Upgrade\r\n" +
      "Sec-WebSocket-Accept: " + wsAccept(req.headers["sec-websocket-key"]) + "\r\n\r\n");
    const send = (obj) => { try { socket.write(encodeText(JSON.stringify(obj))); } catch {} };
    send({ kind: "state", url: "", title: "", loading: false, canBack: false, canForward: false });
    socket.on("data", (buf) => {
      for (const f of decodeFrames(buf)) {
        if (f.close) { socket.end(); continue; }
        let m; try { m = JSON.parse(f.text); } catch { continue; }
        if (m.kind === "resize") {
          send({ kind: "frame", frame: redB64, w: FRAME_W, h: FRAME_H, offsetY: 0 });
          send({ kind: "state", url: "", title: "Connected", loading: false, canBack: false, canForward: false });
        } else if (m.kind === "navigate") {
          send({ kind: "frame", frame: greenB64, w: FRAME_W, h: FRAME_H, offsetY: 0 });
          send({ kind: "state", url: m.url, title: "Navigated", loading: false, canBack: true, canForward: false });
        }
      }
    });
    socket.on("error", () => {});
  });
  return new Promise((resolve) => server.listen(port, () => resolve(server)));
}

// --- static file server (COEP:require-corp, like the real desktop) ---------

function startStatic() {
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
    } catch { res.writeHead(404).end("not found"); }
  });
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => resolve({ server, base: `http://127.0.0.1:${server.address().port}` }));
  });
}

// --- pixel helpers ---------------------------------------------------------

const countColor = (png, c, tol = 24) => {
  let n = 0;
  for (let i = 0; i < png.data.length; i += 4)
    if (Math.abs(png.data[i] - c[0]) <= tol && Math.abs(png.data[i + 1] - c[1]) <= tol && Math.abs(png.data[i + 2] - c[2]) <= tol) n++;
  return n;
};

const isColor = (png, x, y, c, tol) => {
  const i = (y * png.width + x) * 4;
  return Math.abs(png.data[i] - c[0]) <= tol && Math.abs(png.data[i + 1] - c[1]) <= tol && Math.abs(png.data[i + 2] - c[2]) <= tol;
};

// denseBox locates the solid content-frame rectangle of colour c by keeping only
// rows/columns that are mostly that colour, so stray matching pixels elsewhere
// (window-chrome accents, AA fringes) do not inflate the box.
const denseBox = (png, c, tol = 16) => {
  const rowN = new Array(png.height).fill(0);
  const colN = new Array(png.width).fill(0);
  for (let y = 0; y < png.height; y++) for (let x = 0; x < png.width; x++) {
    if (isColor(png, x, y, c, tol)) { rowN[y]++; colN[x]++; }
  }
  const rows = [], cols = [];
  for (let y = 0; y < png.height; y++) if (rowN[y] >= 300) rows.push(y);
  for (let x = 0; x < png.width; x++) if (colN[x] >= 200) cols.push(x);
  if (!rows.length || !cols.length) return null;
  return { x: cols[0], y: rows[0], w: cols[cols.length - 1] - cols[0] + 1, h: rows[rows.length - 1] - rows[0] + 1 };
};

// --- run -------------------------------------------------------------------

const redB64 = solidPNG(FRAME_W, FRAME_H, RED);
const greenB64 = solidPNG(FRAME_W, FRAME_H, GREEN);

let wsServer;
try {
  wsServer = await startWS(WS_PORT, redB64, greenB64);
} catch (e) {
  console.log(`SKIP: could not bind mock proxy on :${WS_PORT} (${e.message})`);
  process.exit(0);
}
const { server: staticServer, base } = await startStatic();
const browser = await chromium.launch({ headless: true });
const out = {};
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  const errs = [];
  page.on("pageerror", (e) => errs.push(String(e)));
  await page.goto(`${base}/index.html`, { waitUntil: "load" });
  await page.waitForFunction(() => {
    if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
    return globalThis.wasmboxReady === true;
  }, { timeout: 15000 });
  await page.evaluate(() => globalThis.wasmboxSpawnExternal("clients/browser/worker.js"));

  // Poll for a screenshot whose content area is mostly `colour` (a streamed
  // frame blitted), up to ~20s — robust on slow CI runners without fixed sleeps.
  const waitForFrame = async (colour, min = 100000) => {
    for (let i = 0; i < 40; i++) {
      const shot = PNG.sync.read(await page.screenshot({ type: "png" }));
      if (countColor(shot, colour) > min) return shot;
      await page.waitForTimeout(500);
    }
    return PNG.sync.read(await page.screenshot({ type: "png" }));
  };

  // 1) The RED streamed frame must have blitted into the content area.
  let png = await waitForFrame(RED);
  const red = denseBox(png, RED);
  out.redBox = red;
  out.redPixels = countColor(png, RED);

  if (red) {
    // The address bar sits in the toolbar just above the content frame.
    const addrX = red.x + Math.floor(red.w / 2);
    const addrY = red.y - Math.floor(TOOLBAR_H / 2);
    await page.mouse.click(addrX, addrY);      // focus window + address bar
    await page.waitForTimeout(200);
    await page.keyboard.type("example.com", { delay: 20 });
    await page.keyboard.press("Enter");
  }

  // 2) The typed navigation must stream a fresh GREEN frame into the canvas.
  png = await waitForFrame(GREEN);
  out.greenPixels = countColor(png, GREEN);
  await writeFile(SHOT, PNG.sync.write(png));
  out.pageerrors = errs;
} finally {
  await browser.close();
  staticServer.close();
  wsServer.close();
}

console.log(JSON.stringify(out, null, 2));
const blitOK = out.redBox && out.redPixels > 100000;          // RED frame filled the content area
const navOK = out.greenPixels > 100000;                       // typed URL → navigate → GREEN frame
const clean = (out.pageerrors || []).length === 0;
console.log(blitOK ? "ok  RED frame blitted into the SAB canvas (streaming + COEP-escaping WS work)"
                   : "FAIL ❌ no streamed frame in the content area");
console.log(navOK ? "ok  address bar input drove a navigate → GREEN frame streamed + blitted"
                  : "FAIL ❌ navigation did not stream a new frame");
const pass = blitOK && navOK && clean;
console.log(pass ? "\nPASS ✅ streaming browser client: frame blit + interactive address bar"
                 : "\nFAIL ❌");
process.exit(pass ? 0 : 1);
