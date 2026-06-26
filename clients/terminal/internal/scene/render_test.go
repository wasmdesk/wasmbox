// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import "testing"

// PaintGrid: a buffer of the wrong length is a caller bug -> panic.
func TestPaintGridPanicsOnSizeMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("PaintGrid did not panic on size mismatch")
		}
	}()
	g := NewGrid(2, 2)
	PaintGrid(make([]byte, 4), 16, 16, g)
}

// PaintGrid lays down the BG colour over the surface even when the grid is
// empty (no glyphs). The cursor block sits at (0,0) by default, so sample a
// pixel inside a non-cursor cell -- e.g. cell (1,1).
func TestPaintGridFillsBackground(t *testing.T) {
	g := NewGrid(2, 2)
	w, h := 32, 32
	buf := make([]byte, 4*w*h)
	PaintGrid(buf, w, h, g)
	// Cell (1,1) at scale=2 starts at pixel (16,16). Any pixel inside it
	// should carry PaletteBG[0] (empty glyph -> all BG).
	want := PaletteBG[0]
	off := (20*w + 20) * 4
	if buf[off] != want[0] || buf[off+1] != want[1] || buf[off+2] != want[2] || buf[off+3] != want[3] {
		t.Fatalf("interior pixel = (%d,%d,%d,%d), want %v",
			buf[off], buf[off+1], buf[off+2], buf[off+3], want)
	}
}

// PaintGrid renders a single 'A' at (0,0) with FG ink -- assert at least one
// pixel inside the cell's rectangle matches PaletteFG[0].
func TestPaintGridDrawsGlyphInk(t *testing.T) {
	g := NewGrid(2, 2)
	g.Cells[0] = TerminalCell{Glyph: 'A', FG: 0, BG: 0}
	// Pin the cursor outside cell (0,0) so the cursor block does not over-
	// paint the 'A' we want to inspect.
	g.CursorCol = 1
	g.CursorRow = 1
	w, h := 32, 32
	buf := make([]byte, 4*w*h)
	PaintGrid(buf, w, h, g)
	// Cell (0,0) of a 2x2 grid on a 32x32 surface at scale=2 spans pixel
	// (offX, offY) .. (offX+15, offY+15) where offX = (32 - 32) / 2 = 0.
	// Scan the cell rectangle for any pixel that exactly matches PaletteFG[0].
	want := PaletteFG[0]
	found := false
	for y := 0; y < 16 && !found; y++ {
		for x := 0; x < 16 && !found; x++ {
			off := (y*w + x) * 4
			if buf[off] == want[0] && buf[off+1] == want[1] &&
				buf[off+2] == want[2] && buf[off+3] == want[3] {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("no FG-coloured pixel found inside the 'A' cell")
	}
}

// PaintGrid centers a grid whose pixel size is less than the surface. With a
// 3-col x 1-row grid on a 96 x 16 surface, the largest integer scale that
// fits both axes is scale = min(96/24, 16/8) = min(4, 2) = 2, giving a
// 48 x 16 grid centered with offX = 24 of horizontal margin.
func TestPaintGridCenters(t *testing.T) {
	g := NewGrid(3, 1)
	g.Cells[0] = TerminalCell{Glyph: 'A'}
	w, h := 96, 16
	buf := make([]byte, 4*w*h)
	PaintGrid(buf, w, h, g)
	// Inside the left margin (x < 24) only BG should appear -- the grid
	// (cells + cursor) starts at x = 24.
	bg := PaletteBG[0]
	for x := 0; x < 24; x++ {
		off := (8*w + x) * 4 // middle row, scanning across the left margin
		if buf[off] != bg[0] || buf[off+1] != bg[1] || buf[off+2] != bg[2] {
			t.Fatalf("left margin pixel x=%d not BG: (%d,%d,%d)", x,
				buf[off], buf[off+1], buf[off+2])
		}
	}
}

// fgOf / bgOf fall back to entry 0 on an out-of-range index.
func TestPaletteFallback(t *testing.T) {
	if fgOf(255) != PaletteFG[0] {
		t.Fatal("fgOf(255) did not fall back to entry 0")
	}
	if bgOf(255) != PaletteBG[0] {
		t.Fatal("bgOf(255) did not fall back to entry 0")
	}
}

// paintCell clips writes that fall off the surface, in any direction.
func TestPaintCellClipsOffSurface(t *testing.T) {
	w, h := 4, 4
	buf := make([]byte, 4*w*h)
	// Paint a cell whose top-left is well outside the surface.
	paintCell(buf, w, h, -100, -100, 1, TerminalCell{Glyph: 0x7F})
	// And one well past the right/bottom edges.
	paintCell(buf, w, h, w+10, h+10, 1, TerminalCell{Glyph: 0x7F})
	for _, b := range buf {
		if b != 0 {
			t.Fatal("clipped paintCell leaked into the surface")
		}
	}
}

// A scale factor of less than 1 (sub-pixel surface) still produces a valid
// render at scale=1.
func TestPaintGridMinimumScale(t *testing.T) {
	g := NewGrid(40, 25) // 80x80 at scale=1, 160x160 at scale=2
	w, h := 40, 25       // less than 8*40, less than 8*25 -> scale should clamp to 1
	buf := make([]byte, 4*w*h)
	PaintGrid(buf, w, h, g) // must not panic
}

// Glyph: an unknown byte falls back to the solid block at 0x7F so the
// glyph never silently vanishes.
func TestGlyphFallback(t *testing.T) {
	want := Glyph(0x7F)
	got := Glyph(0x01) // not in the table
	if got != want {
		t.Fatalf("Glyph(0x01) = %v, want fallback %v", got, want)
	}
}

// fillSurface lays down the requested colour everywhere.
func TestFillSurface(t *testing.T) {
	w, h := 3, 2
	buf := make([]byte, 4*w*h)
	fillSurface(buf, w, h, [4]uint8{1, 2, 3, 4})
	for i := 0; i+3 < len(buf); i += 4 {
		if buf[i] != 1 || buf[i+1] != 2 || buf[i+2] != 3 || buf[i+3] != 4 {
			t.Fatalf("pixel %d = (%d,%d,%d,%d)", i/4, buf[i], buf[i+1], buf[i+2], buf[i+3])
		}
	}
}
