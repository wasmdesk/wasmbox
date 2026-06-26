// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import "testing"

func newRGBA(w, h int) []byte { return make([]byte, 4*w*h) }

// pixelAt reads the RGBA32 sample at (x,y) from a w-wide buffer.
func pixelAt(buf []byte, w, x, y int) [4]uint8 {
	off := (y*w + x) * 4
	return [4]uint8{buf[off], buf[off+1], buf[off+2], buf[off+3]}
}

// Render fills an exactly-sized buffer without panicking.
func TestRenderExactSize(t *testing.T) {
	s := New(480, 360)
	Render(s, newRGBA(480, 360))
}

// A buffer of the wrong length panics.
func TestRenderPanicsOnSizeMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on size mismatch")
		}
	}()
	s := New(64, 64)
	Render(s, make([]byte, 4))
}

// The path bar at the top must be drawn in the dark strip colour at boot
// (every column at y == PathBarHeight/2 that is not occupied by glyph ink
// stays at ColorPathBarBG).
func TestPathBarAtTop(t *testing.T) {
	w, h := 480, 360
	s := New(w, h)
	buf := newRGBA(w, h)
	Render(s, buf)
	// Sample the rightmost column at the path-bar's vertical centre. The path
	// for "/" is short (one glyph at x=6) so the far right is guaranteed BG.
	px := pixelAt(buf, w, w-2, PathBarHeight/2)
	if px[0] != ColorPathBarBG[0] || px[1] != ColorPathBarBG[1] || px[2] != ColorPathBarBG[2] {
		t.Errorf("path bar BG at (w-2, %d) = (%d,%d,%d), want %v",
			PathBarHeight/2, px[0], px[1], px[2], ColorPathBarBG)
	}
}

// Below the path bar the panel background must show through.
func TestPanelBackground(t *testing.T) {
	w, h := 480, 360
	s := New(w, h)
	buf := newRGBA(w, h)
	Render(s, buf)
	// Sample just above the last row, far to the right — past the icon and
	// name columns so neither ink nor highlight is there.
	px := pixelAt(buf, w, w-10, h-10)
	if px[0] != ColorBG[0] || px[1] != ColorBG[1] || px[2] != ColorBG[2] {
		t.Errorf("panel BG at (w-10, h-10) = (%d,%d,%d), want %v",
			px[0], px[1], px[2], ColorBG)
	}
}

// The selected row's left edge carries the highlight background (the icon
// is centered inside the icon column; column 0 of the selected row is the
// highlight strip).
func TestSelectedRowHighlight(t *testing.T) {
	w, h := 480, 360
	s := New(w, h)
	buf := newRGBA(w, h)
	Render(s, buf)
	// The first row (Cursor=0) starts at y = PathBarHeight; sample x=0 just
	// below the top of that row, where the highlight BG is guaranteed (no
	// icon, no glyph).
	y := PathBarHeight + 1
	px := pixelAt(buf, w, 0, y)
	if px[0] != ColorHighlightBG[0] || px[1] != ColorHighlightBG[1] || px[2] != ColorHighlightBG[2] {
		t.Errorf("selected row[0] BG at (0,%d) = (%d,%d,%d), want %v",
			y, px[0], px[1], px[2], ColorHighlightBG)
	}
	// The second row (Cursor!=1) at the same x should be the panel BG.
	y2 := PathBarHeight + RowHeight + 1
	px2 := pixelAt(buf, w, 0, y2)
	if px2[0] != ColorBG[0] || px2[1] != ColorBG[1] || px2[2] != ColorBG[2] {
		t.Errorf("unselected row[1] BG at (0,%d) = (%d,%d,%d), want %v",
			y2, px2[0], px2[1], px2[2], ColorBG)
	}
}

// Moving the cursor changes WHICH row has the highlight background.
func TestCursorMoveChangesHighlight(t *testing.T) {
	w, h := 480, 360
	s := New(w, h)
	s.HandleKey("ArrowDown") // Cursor = 1
	buf := newRGBA(w, h)
	Render(s, buf)
	// Row 1 is now selected.
	y1 := PathBarHeight + RowHeight + 1
	px1 := pixelAt(buf, w, 0, y1)
	if px1[0] != ColorHighlightBG[0] || px1[1] != ColorHighlightBG[1] || px1[2] != ColorHighlightBG[2] {
		t.Errorf("row[1] BG after ArrowDown = (%d,%d,%d), want %v",
			px1[0], px1[1], px1[2], ColorHighlightBG)
	}
	// Row 0 is no longer selected.
	y0 := PathBarHeight + 1
	px0 := pixelAt(buf, w, 0, y0)
	if px0[0] != ColorBG[0] || px0[1] != ColorBG[1] || px0[2] != ColorBG[2] {
		t.Errorf("row[0] BG after ArrowDown = (%d,%d,%d), want panel BG", px0[0], px0[1], px0[2])
	}
}

// A folder icon's body uses ColorFolderFill; a file icon's body uses
// ColorFileFill — sample the centre of each icon column on the appropriate row.
func TestIconColors(t *testing.T) {
	w, h := 480, 360
	s := New(w, h)
	buf := newRGBA(w, h)
	Render(s, buf)

	// Row 0 is Documents (a folder). The icon body sits at roughly
	// (IconColW/2, PathBarHeight + RowHeight/2).
	xIcon := IconColW / 2
	yFolder := PathBarHeight + RowHeight/2
	pxF := pixelAt(buf, w, xIcon, yFolder)
	if pxF[0] != ColorFolderFill[0] || pxF[1] != ColorFolderFill[1] || pxF[2] != ColorFolderFill[2] {
		t.Errorf("folder icon body at (%d,%d) = (%d,%d,%d), want %v",
			xIcon, yFolder, pxF[0], pxF[1], pxF[2], ColorFolderFill)
	}

	// Row 3 is about.txt (a file).
	yFile := PathBarHeight + 3*RowHeight + RowHeight/2
	pxFi := pixelAt(buf, w, xIcon, yFile)
	if pxFi[0] != ColorFileFill[0] || pxFi[1] != ColorFileFill[1] || pxFi[2] != ColorFileFill[2] {
		t.Errorf("file icon body at (%d,%d) = (%d,%d,%d), want %v",
			xIcon, yFile, pxFi[0], pxFi[1], pxFi[2], ColorFileFill)
	}
}

// Render must paint exactly N rows for N entries — sample one column at y
// just past the last entry: it should be panel BG, not a partial row.
func TestRowsClipBeyondEntries(t *testing.T) {
	w, h := 480, 360
	s := New(w, h)
	buf := newRGBA(w, h)
	Render(s, buf)
	// 4 entries -> rows at y in [PathBarHeight, PathBarHeight+4*RowHeight).
	// Just past the last row, the panel BG must show.
	y := PathBarHeight + 4*RowHeight + 2
	px := pixelAt(buf, w, 0, y)
	if px[0] != ColorBG[0] || px[1] != ColorBG[1] || px[2] != ColorBG[2] {
		t.Errorf("beyond last row at (0,%d) = (%d,%d,%d), want panel BG", y, px[0], px[1], px[2])
	}
}

// A short surface drops entries that do not fit — exercises the y >= h break
// inside Paint without panicking.
func TestShortSurfaceClips(t *testing.T) {
	// Big enough for path bar + one row, smaller than four rows.
	w, h := 480, PathBarHeight+RowHeight+4
	s := New(w, h)
	buf := newRGBA(w, h)
	Render(s, buf) // must not panic
}

// drawText skips control characters silently (no ink on the path-bar row at
// pure BG locations) — exercises the c < 0x20 branch.
func TestDrawTextSkipsControlBytes(t *testing.T) {
	w, h := 100, PathBarHeight + 8
	buf := newRGBA(w, h)
	// Pre-fill the path bar so the test is exclusively about the text overlay.
	fillRect(buf, w, h, 0, 0, w, h, ColorPathBarBG)
	drawText(buf, w, h, 2, 2, "\x01\x02hi", ColorPathBarFG)
	// Some ink for 'h' must land in the band 2..2+FontH*FontScale. Just check
	// the function did not panic + that at least one pixel changed.
	changed := false
	for i := 0; i+3 < len(buf); i += 4 {
		if buf[i] != ColorPathBarBG[0] {
			changed = true
			break
		}
	}
	if !changed {
		t.Errorf("drawText with leading control bytes produced no ink")
	}
}

// drawText clips when x runs past the surface width.
func TestDrawTextClipsOnWidth(t *testing.T) {
	w, h := 30, PathBarHeight + 8
	buf := newRGBA(w, h)
	// A long string at x=2 must not panic and must clip at w.
	drawText(buf, w, h, 2, 2, "abcdefghij", ColorFG)
}

// fillRect with an empty rectangle is a no-op (zero pixels written).
func TestFillRectEmpty(t *testing.T) {
	w, h := 16, 16
	buf := newRGBA(w, h)
	fillRect(buf, w, h, 5, 5, 0, 0, ColorFG)
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("fillRect with zero size leaked into buffer")
		}
	}
}

// fillRect clips when the rectangle extends past the surface.
func TestFillRectClipsOutOfBounds(t *testing.T) {
	w, h := 8, 8
	buf := newRGBA(w, h)
	fillRect(buf, w, h, -4, -4, 16, 16, ColorFG) // half outside on every side
	// The visible (0,0) pixel should have been written.
	px := pixelAt(buf, w, 0, 0)
	if px[0] != ColorFG[0] {
		t.Errorf("fillRect clipped failed to write (0,0); got %v", px)
	}
}

// fillRect with a negative x/y stride does nothing past clipping (no panic).
func TestFillRectNegative(t *testing.T) {
	w, h := 8, 8
	buf := newRGBA(w, h)
	fillRect(buf, w, h, 10, 10, 4, 4, ColorFG) // entirely outside
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("fillRect fully outside leaked")
		}
	}
}

// Glyph returns the fallback solid block for a byte missing from the font
// table (e.g. 0x01).
func TestGlyphFallback(t *testing.T) {
	g := Glyph(0x01)
	for _, row := range g {
		if row != 0xFF {
			t.Errorf("Glyph(0x01) row = %#x, want 0xFF (fallback)", row)
		}
	}
}

// Glyph returns the table entry for a known printable character.
func TestGlyphKnown(t *testing.T) {
	g := Glyph(' ')
	for _, row := range g {
		if row != 0 {
			t.Errorf("Glyph(' ') row = %#x, want 0", row)
		}
	}
}

// PathBarSamplePoint returns the centre of the path bar strip.
func TestPathBarSamplePoint(t *testing.T) {
	x, y := PathBarSamplePoint(480)
	if x != 240 || y != PathBarHeight/2 {
		t.Errorf("PathBarSamplePoint(480) = (%d,%d), want (240,%d)", x, y, PathBarHeight/2)
	}
}
