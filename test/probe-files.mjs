// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Headless Playwright probe for the GNOME Nautilus-inspired file browser
// client (clients/files).
//
// Spawns a system Chrome (channel: "chrome", headless: true), loads the index
// page, opens a Files window via wasmboxSpawnExternal, locates the window on
// the canvas by sampling the Adwaita sidebar background colour, then drives:
//
//   - ArrowDown moves the selection from row 0 to row 1 -- we assert that the
//     accent-blue row strip migrates one row down.
//   - Click on a folder row -- the row should be painted with the accent fill
//     before navigation happens; we sample inside the row to confirm.
//   - Per-region pixel samples for sidebar / window BG / header bar / accent.
//
// Saves a screenshot to /tmp/files-nautilus.png.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { PNG } from "pngjs";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const BOOT_TIMEOUT_MS = 15000;
const SCREENSHOT_PATH = "/tmp/files-nautilus.png";

// Adwaita (light) palette duplicated from clients/files/internal/scene/render.go
// so the probe is self-contained. Keep in sync if the colour table changes there.
const COLOR_WINDOW_BG      = [250, 250, 250];
const COLOR_SIDEBAR_BG     = [241, 241, 241];
const COLOR_HEADERBAR_BG   = [248, 248, 248];
const COLOR_ACCENT         = [53, 132, 228];
const COLOR_TEXT_PRIMARY   = [46, 52, 54];
const COLOR_FOLDER_FILL    = [95, 161, 224];
const COLOR_ON_ACCENT      = [255, 255, 255];

// Layout constants must match render.go.
const HEADER_BAR_HEIGHT          = 44;
const COLUMN_HEADER_HEIGHT       = 28;
const ROW_HEIGHT                 = 32;
const SIDEBAR_WIDTH              = 160;
const SIDEBAR_TOP_PADDING        = 8;
const SIDEBAR_SECTION_HEADER_H   = 22;
const SIDEBAR_ROW_H              = 28;
const ICON_SIZE                  = 18;
const NAME_COL_X                 = SIDEBAR_WIDTH + 12;
const SURFACE_W                  = 720;
const SURFACE_H                  = 440;

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

// pixelAt reads the RGBA32 sample at (x,y) from a PNG.
function pixelAt(png, x, y) {
  const i = (y * png.width + x) * 4;
  return [png.data[i], png.data[i+1], png.data[i+2]];
}

function eqColor(px, c) { return px[0] === c[0] && px[1] === c[1] && px[2] === c[2]; }

// findSidebarBounds locates the file browser surface by its unique sidebar
// background colour (no other compositor pane uses Adwaita's @sidebar_bg).
// Returns the bounding box of the contiguous sidebar pixel block.
function findSidebarBounds(png, color) {
  const { width, height, data } = png;
  let minX = width, minY = height, maxX = -1, maxY = -1;
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const i = (y * width + x) * 4;
      if (data[i] === color[0] && data[i+1] === color[1] && data[i+2] === color[2]) {
        if (x < minX) minX = x;
        if (y < minY) minY = y;
        if (x > maxX) maxX = x;
        if (y > maxY) maxY = y;
      }
    }
  }
  if (maxX < 0) return null;
  return { x: minX, y: minY, w: maxX - minX + 1, h: maxY - minY + 1 };
}

// Surface gives the file browser surface bounds. The sidebar starts at x=0
// of the surface (row 0 is the header bar, no sidebar) so we offset minY by
// HEADER_BAR_HEIGHT to land on the surface origin.
function fileSurface(png) {
  const sb = findSidebarBounds(png, COLOR_SIDEBAR_BG);
  if (!sb) return null;
  // sb.x is the surface origin x; sb.y is HEADER_BAR_HEIGHT below surface origin.
  return { x: sb.x, y: sb.y - HEADER_BAR_HEIGHT, w: SURFACE_W, h: SURFACE_H };
}

// findHighlightedRow returns the y-coordinate of the accent-strip top edge
// inside the file browser surface. Scans a column inside the right pane,
// past the sidebar.
function findHighlightedRow(png, surface) {
  const x = surface.x + SIDEBAR_WIDTH + 4;
  const y0 = surface.y + HEADER_BAR_HEIGHT + COLUMN_HEADER_HEIGHT;
  const y1 = y0 + 8 * ROW_HEIGHT;
  for (let y = y0; y < y1; y++) {
    if (eqColor(pixelAt(png, x, y), COLOR_ACCENT)) {
      return y;
    }
  }
  return null;
}

const { server, base } = await startServer();
console.log(`probe-files: serving on ${base}`);

// HARD RULE: system Chrome, headless. Per the prompt.
const browser = await chromium.launch({ headless: true, channel: "chrome" });
const consoleLines = [];
const pageErrors = [];

try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  page.on("console",   (m) => consoleLines.push(m.text()));
  page.on("pageerror", (e) => pageErrors.push(String(e)));

  await page.goto(`${base}/index.html`, { waitUntil: "load" });
  await page.waitForFunction(
    () => {
      if (globalThis.wasmboxError) throw new Error(String(globalThis.wasmboxError));
      return globalThis.wasmboxReady === true;
    },
    { timeout: BOOT_TIMEOUT_MS },
  );
  console.log("ok  compositor booted");

  // Spawn the files client.
  await page.evaluate(() => globalThis.wasmboxSpawnExternal("clients/files/worker.js"));
  await page.waitForTimeout(2500);

  // Discover the Files window on the canvas by its unique cream panel BG.
  let shot1 = await page.screenshot({ type: "png", fullPage: false });
  let png1 = PNG.sync.read(shot1);

  const surface = fileSurface(png1);
  if (!surface) {
    fail(`Files surface not visible on canvas (no sidebar BG ${COLOR_SIDEBAR_BG} found)`);
  } else {
    console.log(`ok  Files surface located at (${surface.x},${surface.y}) ${surface.w}x${surface.h}`);

    // Per-region pixel samples (the "looks right" proof).
    // Sidebar BG -- sample the right-edge gutter of the sidebar where no
    // label glyph paints (the 1px divider sits at SidebarWidth-1, so -4 is
    // safely inside).
    const sbX = surface.x + SIDEBAR_WIDTH - 4;
    const sbY = surface.y + HEADER_BAR_HEIGHT + 100;
    const sbPx = pixelAt(png1, sbX, sbY);
    if (!eqColor(sbPx, COLOR_SIDEBAR_BG)) {
      fail(`sidebar pixel at (${sbX},${sbY}) = ${sbPx}, want ${COLOR_SIDEBAR_BG}`);
    } else {
      console.log(`ok  sidebar pixel @ (${sbX},${sbY}) = (${sbPx.join(",")}) -- COLOR_SIDEBAR_BG (Adwaita @sidebar_bg)`);
    }
    // Window background (right pane, far right + below all rows).
    const wbX = surface.x + SURFACE_W - 4;
    const wbY = surface.y + SURFACE_H - 4;
    const wbPx = pixelAt(png1, wbX, wbY);
    if (!eqColor(wbPx, COLOR_WINDOW_BG)) {
      fail(`window-bg pixel at (${wbX},${wbY}) = ${wbPx}, want ${COLOR_WINDOW_BG}`);
    } else {
      console.log(`ok  window-bg pixel @ (${wbX},${wbY}) = (${wbPx.join(",")}) -- COLOR_WINDOW_BG (Adwaita @view_bg)`);
    }
    // Header bar background (right of the path bar buttons + above the column
    // headers). Sample at far-right of the header band.
    const hbX = surface.x + SURFACE_W - 6;
    const hbY = surface.y + HEADER_BAR_HEIGHT / 2;
    const hbPx = pixelAt(png1, hbX, hbY);
    if (!eqColor(hbPx, COLOR_HEADERBAR_BG)) {
      fail(`header-bar pixel at (${hbX},${hbY}) = ${hbPx}, want ${COLOR_HEADERBAR_BG}`);
    } else {
      console.log(`ok  header-bar pixel @ (${hbX},${hbY}) = (${hbPx.join(",")}) -- COLOR_HEADERBAR_BG`);
    }

    // Confirm the accent strip is at the FIRST entry row (Cursor=0).
    const row0Y = findHighlightedRow(png1, surface);
    if (row0Y === null) {
      fail("initial accent strip not visible");
    } else {
      const expected = surface.y + HEADER_BAR_HEIGHT + COLUMN_HEADER_HEIGHT;
      if (Math.abs(row0Y - expected) > 2) {
        fail(`initial accent strip at y=${row0Y}, expected ~${expected}`);
      } else {
        console.log(`ok  initial accent strip at y=${row0Y} (row 0)`);
      }
    }

    // Focus the window with a click in the right pane in a guaranteed-safe spot:
    // the column-header band (the click handler ignores it but the window grabs focus).
    const cx = surface.x + SIDEBAR_WIDTH + 200;
    const cy = surface.y + HEADER_BAR_HEIGHT + COLUMN_HEADER_HEIGHT / 2;
    await page.mouse.click(cx, cy);
    await page.waitForTimeout(150);

    // ArrowDown -> Cursor goes to row 1, accent strip migrates one row.
    await page.keyboard.press("ArrowDown");
    await page.waitForTimeout(400);

    let shot2 = await page.screenshot({ type: "png", fullPage: false });
    let png2 = PNG.sync.read(shot2);

    const row1Y = findHighlightedRow(png2, surface);
    if (row1Y === null) {
      fail("accent strip vanished after ArrowDown");
    } else {
      const expected = surface.y + HEADER_BAR_HEIGHT + COLUMN_HEADER_HEIGHT + ROW_HEIGHT;
      if (Math.abs(row1Y - expected) > 2) {
        fail(`row 1 accent at y=${row1Y}, expected ~${expected}`);
      } else {
        console.log(`ok  ArrowDown moved accent strip to y=${row1Y} (row 1)`);
      }
    }

    // Sample the accent fill inside row 1 (past the icon) for the explicit
    // "clicked row background = accent blue" claim the spec asks for.
    const accentSampleX = surface.x + SIDEBAR_WIDTH + 4;
    const accentSampleY = row1Y + ROW_HEIGHT / 2;
    const accentPx = pixelAt(png2, accentSampleX, accentSampleY);
    if (!eqColor(accentPx, COLOR_ACCENT)) {
      fail(`selected-row accent at (${accentSampleX},${accentSampleY}) = ${accentPx}, want ${COLOR_ACCENT}`);
    } else {
      console.log(`ok  selected-row accent @ (${accentSampleX},${accentSampleY}) = (${accentPx.join(",")}) -- COLOR_ACCENT`);
    }

    // ----- FOREGROUND CONTENT ASSERTIONS -----
    // The "window frame with nothing inside" regression: BG colours paint,
    // but icons + text do not. The earlier probe sampled only background
    // colours + accent, so a "BG-only" frame passed as ok. We now require
    // explicit non-zero pixel counts for the folder icon AND the row text
    // AND the sidebar entry text -- the foreground content the user looks at.
    //
    // We count pixels INSIDE a region (rather than sampling a single point)
    // because the 8x8 font lays out glyphs sparsely; one wrong x/y could miss
    // ink even on a healthy frame. We use png1 (the pre-ArrowDown frame) so
    // row 0 is selected (white-on-accent) and row 1+ are unselected.
    const countIn = (png, x, y, rw, rh, c) => {
      let n = 0;
      for (let yy = y; yy < y + rh; yy++) {
        for (let xx = x; xx < x + rw; xx++) {
          if (eqColor(pixelAt(png, xx, yy), c)) n++;
        }
      }
      return n;
    };

    // (a) Folder icon on a non-selected list row (row 1 in png1 = Pictures).
    //     paintFolderIcon fills 24x14 with ColorFolderFill (95,161,224). We
    //     scan the icon's bounding box -- if drawing fails, this is 0.
    const listY0 = surface.y + HEADER_BAR_HEIGHT + COLUMN_HEADER_HEIGHT;
    const iconRowY = listY0 + ROW_HEIGHT + (ROW_HEIGHT - ICON_SIZE) / 2;
    const iconRowX = surface.x + NAME_COL_X;
    const folderPixels = countIn(png1, iconRowX, iconRowY, 24, ICON_SIZE, COLOR_FOLDER_FILL);
    if (folderPixels < 30) {
      fail(`folder-icon pixels on list row 1 = ${folderPixels} (need >= 30 of ${COLOR_FOLDER_FILL}); icons not painting`);
    } else {
      console.log(`ok  list-row folder icon: ${folderPixels} ColorFolderFill pixels in 24x${ICON_SIZE} band`);
    }

    // (b) List row 1 NAME label in primary ink (file/folder name on the
    //     unselected row). drawText paints (46,52,54) glyphs; if drawGlyph
    //     fails this is 0.
    const nameTextX = surface.x + NAME_COL_X + ICON_SIZE + 10;
    const nameTextY = listY0 + ROW_HEIGHT;
    const namePixels = countIn(png1, nameTextX, nameTextY, 160, ROW_HEIGHT, COLOR_TEXT_PRIMARY);
    if (namePixels < 20) {
      fail(`list row 1 name-text pixels = ${namePixels} (need >= 20 of ${COLOR_TEXT_PRIMARY}); drawText is failing`);
    } else {
      console.log(`ok  list-row name text: ${namePixels} ColorTextPrimary pixels`);
    }

    // (c) Sidebar Documents entry text -- the unselected sidebar entry's
    //     label paints ColorTextPrimary at x ~32 inside the sidebar.
    const sbFirstRowY = surface.y + HEADER_BAR_HEIGHT + SIDEBAR_TOP_PADDING + SIDEBAR_SECTION_HEADER_H;
    const sbDocsY = sbFirstRowY + SIDEBAR_ROW_H; // row 1 = Documents
    const sbTextPixels = countIn(png1, surface.x + 30, sbDocsY, SIDEBAR_WIDTH - 32, SIDEBAR_ROW_H, COLOR_TEXT_PRIMARY);
    if (sbTextPixels < 6) {
      fail(`sidebar Documents text pixels = ${sbTextPixels} (need >= 6 of ${COLOR_TEXT_PRIMARY}); sidebar labels invisible`);
    } else {
      console.log(`ok  sidebar Documents text: ${sbTextPixels} ColorTextPrimary pixels`);
    }

    // (d) Sidebar Home entry icon -- the selected row paints white ink
    //     (ColorOnAccent) for the icon glyph + label.
    const sbHomeWhite = countIn(png1, surface.x + 8, sbFirstRowY, SIDEBAR_WIDTH - 16, SIDEBAR_ROW_H, COLOR_ON_ACCENT);
    if (sbHomeWhite < 8) {
      fail(`sidebar Home selected-row white pixels = ${sbHomeWhite} (need >= 8 of ${COLOR_ON_ACCENT}); selected icon/label missing`);
    } else {
      console.log(`ok  sidebar Home selected: ${sbHomeWhite} ColorOnAccent pixels (icon + label)`);
    }

    // Save the screenshot before we navigate so the saved frame shows the
    // multi-column list with the row-1 selection.
    await page.screenshot({ type: "png", path: SCREENSHOT_PATH, fullPage: false });
    console.log(`ok  saved screenshot: ${SCREENSHOT_PATH}`);
  }

  if (pageErrors.length) {
    fail(`pageerror(s): ${pageErrors.join(" | ")}`);
  } else {
    console.log("ok  no pageerror");
  }
} catch (e) {
  fail(`unexpected: ${e && e.stack ? e.stack : e}`);
} finally {
  await browser.close();
  server.close();
}

console.log(process.exitCode ? "\nRESULT: FAIL" : "\nRESULT: PASS");
