// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's render.go rasterises a Grid into an RGBA32 byte buffer.
// The whole pipeline is pure Go (no syscall/js, no cgo) so it builds for
// every architecture the repository targets and is unit-tested natively.
//
// Glyph data lives in font.go (Glyph(b) -> [8]byte, FontW = FontH = 8). We
// scale by an integer factor (typically 2) so the small 8x8 raster reads
// well at modern DPIs without needing a heavier font.

package scene

// PaletteFG indexes the per-cell foreground (ink) colours. Index 0 is the
// default soft-green ink; higher indices are reserved for the prompt /
// builtin help. Out-of-range indices fall back to entry 0 -- a missing
// palette pick should not crash the renderer.
var PaletteFG = [][4]uint8{
	{0xa0, 0xe0, 0xa0, 0xff}, // 0: default ink (soft green)
	{0x8a, 0xe1, 0xff, 0xff}, // 1: cyan (prompt)
	{0xff, 0x9b, 0x9b, 0xff}, // 2: red (errors)
	{0xff, 0xff, 0xff, 0xff}, // 3: bright white (help)
}

// PaletteBG indexes the per-cell background colours. Entry 0 is the canonical
// dark-terminal panel; entries beyond it support highlighted regions (none
// used yet, but the slot stays parametric).
var PaletteBG = [][4]uint8{
	{0x10, 0x10, 0x10, 0xff}, // 0: terminal panel
	{0x20, 0x20, 0x30, 0xff}, // 1: subtle highlight
}

// fgOf / bgOf return the RGBA colour for a palette index, falling back to 0
// when the index is out of bounds.
func fgOf(i uint8) [4]uint8 {
	if int(i) >= len(PaletteFG) {
		return PaletteFG[0]
	}
	return PaletteFG[i]
}
func bgOf(i uint8) [4]uint8 {
	if int(i) >= len(PaletteBG) {
		return PaletteBG[0]
	}
	return PaletteBG[i]
}

// PaintGrid rasterises g into rgba. The surface is w x h pixels (RGBA32,
// row-major, opaque alpha). Glyphs are upscaled by an integer scale that
// fits the grid in the surface; cells past the surface bounds are clipped.
// Callers paint the whole frame in one call -- there is no incremental
// dirty-rect path because the grid is small (a few hundred cells) and the
// scaled glyph blit is cheap.
func PaintGrid(rgba []byte, w, h int, g *Grid) {
	if len(rgba) != 4*w*h {
		panic("scene: PaintGrid buffer size mismatch")
	}
	// Choose the largest integer scale that fits the grid in the surface.
	// One scale on each axis would distort the glyphs; we pick the min so
	// the rendered grid is centered without anamorphic stretching.
	sx := w / (FontW * g.Cols)
	sy := h / (FontH * g.Rows)
	scale := sx
	if sy < scale {
		scale = sy
	}
	if scale < 1 {
		scale = 1
	}
	cellW := FontW * scale
	cellH := FontH * scale
	gridW := cellW * g.Cols
	gridH := cellH * g.Rows
	// Top-left offset that centers the grid in the surface.
	offX := (w - gridW) / 2
	offY := (h - gridH) / 2

	// Clear the surface to PaletteBG[0]. Drawing each cell's BG into its
	// rectangle would skip the margin around the centered grid, so a flat
	// pre-fill is simpler and produces the right colour outside the grid.
	bg0 := bgOf(0)
	fillSurface(rgba, w, h, bg0)

	for row := 0; row < g.Rows; row++ {
		for col := 0; col < g.Cols; col++ {
			cell := g.Cells[row*g.Cols+col]
			px := offX + col*cellW
			py := offY + row*cellH
			paintCell(rgba, w, h, px, py, scale, cell)
		}
	}

	// Cursor as a soft block at (CursorCol, CursorRow). We re-use glyph 0x7F
	// (the solid block) and the default ink. The cursor sits ABOVE any glyph
	// that would otherwise occupy that cell, so it stays visible while typing.
	cx := offX + g.CursorCol*cellW
	cy := offY + g.CursorRow*cellH
	paintCell(rgba, w, h, cx, cy, scale, TerminalCell{Glyph: 0x7F, FG: 0, BG: 0})
}

// fillSurface paints every pixel of buf with the given RGBA. Hot path, kept
// as a tight loop -- a memcpy from a one-pixel template would be faster on
// big surfaces but the terminal frame is small enough that this is fine.
func fillSurface(buf []byte, w, h int, c [4]uint8) {
	for i := 0; i+3 < len(buf) && i < 4*w*h; i += 4 {
		buf[i] = c[0]
		buf[i+1] = c[1]
		buf[i+2] = c[2]
		buf[i+3] = c[3]
	}
}

// paintCell rasterises one TerminalCell at (px, py) with the given integer
// scale. Background is painted first (so transparent glyph pixels show the
// BG colour, not whatever was there before), then the glyph ink on top.
// A zero-glyph cell is treated as truly empty -- font.go falls back to the
// solid block for any unknown byte (so it never silently vanishes), but a
// freshly-allocated cell IS empty by design, not "unknown".
func paintCell(buf []byte, w, h, px, py, scale int, cell TerminalCell) {
	fg := fgOf(cell.FG)
	bg := bgOf(cell.BG)
	var glyph [8]byte // all-zero -> all BG, no ink
	if cell.Glyph != 0 {
		glyph = Glyph(cell.Glyph)
	}
	// First: fill the cell rectangle with BG. Skip if the cell is entirely
	// off-surface; otherwise clip per row.
	for gy := 0; gy < FontH; gy++ {
		row := glyph[gy]
		for gx := 0; gx < FontW; gx++ {
			pix := bg
			if (row>>(7-gx))&1 != 0 {
				pix = fg
			}
			// Upscale: write a scale x scale block.
			for dy := 0; dy < scale; dy++ {
				yy := py + gy*scale + dy
				if yy < 0 || yy >= h {
					continue
				}
				for dx := 0; dx < scale; dx++ {
					xx := px + gx*scale + dx
					if xx < 0 || xx >= w {
						continue
					}
					off := (yy*w + xx) * 4
					buf[off] = pix[0]
					buf[off+1] = pix[1]
					buf[off+2] = pix[2]
					buf[off+3] = pix[3]
				}
			}
		}
	}
}
