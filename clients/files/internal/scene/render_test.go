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

func eqRGB(p [4]uint8, c [3]uint8) bool {
	return p[0] == c[0] && p[1] == c[1] && p[2] == c[2]
}

// Render fills an exactly-sized buffer without panicking.
func TestRenderExactSize(t *testing.T) {
	s := New(720, 440)
	Render(s, newRGBA(720, 440))
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

// The sidebar background colour appears at the SidebarSamplePoint (a column
// inside the left pane that the renderer guarantees stays empty -- below
// every Favorite row, in the centre column).
func TestSidebarBG(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	buf := newRGBA(w, h)
	Render(s, buf)
	x, y := SidebarSamplePoint()
	px := pixelAt(buf, w, x, y)
	if !eqRGB(px, ColorSidebarBG) {
		t.Errorf("sidebar BG at (%d,%d) = %v, want %v", x, y, px, ColorSidebarBG)
	}
}

// The right-pane window background appears at the bottom-right corner.
func TestWindowBG(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	buf := newRGBA(w, h)
	Render(s, buf)
	x, y := WindowBGSamplePoint(w, h)
	px := pixelAt(buf, w, x, y)
	if !eqRGB(px, ColorWindowBG) {
		t.Errorf("window BG at (%d,%d) = %v, want %v", x, y, px, ColorWindowBG)
	}
}

// The toolbar fills the top band with ColorToolbarBG. We sample the far
// right of the toolbar -- nothing else paints there.
func TestToolbarBG(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	buf := newRGBA(w, h)
	Render(s, buf)
	px := pixelAt(buf, w, w-4, ToolbarHeight/2)
	if !eqRGB(px, ColorToolbarBG) {
		t.Errorf("toolbar BG = %v, want %v", px, ColorToolbarBG)
	}
}

// The selected list row paints the accent colour across the right pane.
func TestSelectedRowAccent(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	buf := newRGBA(w, h)
	Render(s, buf)
	// Row 0 is selected by default. Sample inside the row, past the icon.
	y := ToolbarHeight + ColumnHeaderHeight + RowHeight/2
	x := SidebarWidth + 4 // just inside the right pane, before any icon
	px := pixelAt(buf, w, x, y)
	if !eqRGB(px, ColorAccent) {
		t.Errorf("row[0] accent at (%d,%d) = %v, want %v", x, y, px, ColorAccent)
	}
}

// Moving the cursor moves which row carries the accent fill.
func TestCursorMoveChangesAccent(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	if !s.HandleKey("ArrowDown") {
		t.Fatal("ArrowDown returned false")
	}
	buf := newRGBA(w, h)
	Render(s, buf)
	// Row 0 is no longer selected.
	y0 := ToolbarHeight + ColumnHeaderHeight + RowHeight/2
	x := SidebarWidth + 4
	px0 := pixelAt(buf, w, x, y0)
	if eqRGB(px0, ColorAccent) {
		t.Errorf("row[0] still accent after ArrowDown: %v", px0)
	}
	// Row 1 is now selected.
	y1 := ToolbarHeight + ColumnHeaderHeight + RowHeight + RowHeight/2
	px1 := pixelAt(buf, w, x, y1)
	if !eqRGB(px1, ColorAccent) {
		t.Errorf("row[1] accent after ArrowDown = %v, want %v", px1, ColorAccent)
	}
}

// The selected-favorite highlight paints accent inside the sidebar.
func TestSidebarSelectionAccent(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	// Click on the first Favorite (Documents). Coordinates: sidebar x, y in
	// the first row band.
	clickY := ToolbarHeight + SidebarHeaderHeight + 4
	if !s.HandleMouse(20, clickY) {
		t.Fatal("sidebar click returned false")
	}
	buf := newRGBA(w, h)
	Render(s, buf)
	// Sample a pixel inside the highlighted sidebar row, past the icon.
	x := SidebarWidth/2 + 20
	y := ToolbarHeight + SidebarHeaderHeight + SidebarRowHeight/2
	px := pixelAt(buf, w, x, y)
	if !eqRGB(px, ColorAccent) {
		t.Errorf("sidebar selection at (%d,%d) = %v, want accent %v", x, y, px, ColorAccent)
	}
}

// paintFolderIcon paints the folder face pixel at the icon's mid-height.
func TestPaintFolderIcon(t *testing.T) {
	w, h := 64, 32
	buf := newRGBA(w, h)
	paintFolderIcon(buf, w, h, 4, 4, false)
	// The body (lighter face) sits at y+5..y+10 -- sample mid-body.
	px := pixelAt(buf, w, 4+8, 4+7)
	if !eqRGB(px, ColorFolderFill) {
		t.Errorf("folder face = %v, want %v", px, ColorFolderFill)
	}
	// Selected variant flips to white.
	buf2 := newRGBA(w, h)
	paintFolderIcon(buf2, w, h, 4, 4, true)
	px2 := pixelAt(buf2, w, 4+8, 4+7)
	if !eqRGB(px2, ColorOnAccent) {
		t.Errorf("selected folder face = %v, want %v", px2, ColorOnAccent)
	}
}

// paintFileIcon paints white paper with a gray border.
func TestPaintFileIcon(t *testing.T) {
	w, h := 64, 32
	buf := newRGBA(w, h)
	paintFileIcon(buf, w, h, 4, 4, false)
	// Mid-paper (offset +2 in x because file body is inset).
	px := pixelAt(buf, w, 4+2+4, 4+8)
	if !eqRGB(px, ColorFilePaper) {
		t.Errorf("file paper = %v, want %v", px, ColorFilePaper)
	}
	// Border on the left edge (x = 4+2).
	pxB := pixelAt(buf, w, 4+2, 4+8)
	if !eqRGB(pxB, ColorFileBorder) {
		t.Errorf("file border = %v, want %v", pxB, ColorFileBorder)
	}
	// Selected variant flips the paper to white-on-accent.
	buf2 := newRGBA(w, h)
	paintFileIcon(buf2, w, h, 4, 4, true)
	px2 := pixelAt(buf2, w, 4+2+4, 4+8)
	if !eqRGB(px2, ColorOnAccent) {
		t.Errorf("selected file paper = %v, want %v", px2, ColorOnAccent)
	}
}

// paintToolbar paints the back-arrow chevron in the primary ink.
func TestPaintToolbarBackButton(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	buf := newRGBA(w, h)
	paintToolbar(buf, w, h, s)
	// The chevron sits roughly at BackBtnX+BackBtnW/2, BackBtnY+BackBtnH/2.
	// Sample around the tip to find at least one ink pixel.
	found := false
	for dy := -4; dy <= 4 && !found; dy++ {
		for dx := -4; dx <= 4 && !found; dx++ {
			x := BackBtnX + BackBtnW/2 - 3 + dx
			y := BackBtnY + BackBtnH/2 + dy
			if x < 0 || y < 0 || x >= w || y >= h {
				continue
			}
			if eqRGB(pixelAt(buf, w, x, y), ColorTextPrimary) {
				found = true
			}
		}
	}
	if !found {
		t.Error("back-arrow chevron not painted in primary ink")
	}
}

// paintColumnHeaders paints the header band with the toolbar BG colour.
func TestPaintColumnHeaders(t *testing.T) {
	w, h := 720, 440
	buf := newRGBA(w, h)
	// Paint a baseline window BG so the test exercises the header on its
	// own (no prior paint stage to confuse the sample).
	fillRect(buf, w, h, 0, 0, w, h, ColorWindowBG)
	paintColumnHeaders(buf, w, h)
	// Sample a column in the header band, well to the right (past "Name").
	y := ToolbarHeight + 4
	px := pixelAt(buf, w, w-50, y)
	if !eqRGB(px, ColorToolbarBG) {
		t.Errorf("column header BG = %v, want %v", px, ColorToolbarBG)
	}
}

// paintListRows on an empty Entries slice paints no row backgrounds.
func TestPaintListRowsEmpty(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	s.Browser.Entries = nil
	buf := newRGBA(w, h)
	fillRect(buf, w, h, 0, 0, w, h, ColorWindowBG)
	paintListRows(buf, w, h, s)
	// The first row band should still be window BG (no accent strip).
	y := ToolbarHeight + ColumnHeaderHeight + RowHeight/2
	px := pixelAt(buf, w, SidebarWidth+4, y)
	if !eqRGB(px, ColorWindowBG) {
		t.Errorf("empty list row[0] BG = %v, want %v", px, ColorWindowBG)
	}
}

// paintListRows clips rows past the surface height (exercises the
// `y >= h` early-break).
func TestPaintListRowsClipsBeyondSurface(t *testing.T) {
	w := 720
	// Surface tall enough for the toolbar+header+1 row but short of all 4.
	h := ToolbarHeight + ColumnHeaderHeight + RowHeight + 2
	s := New(w, h)
	buf := newRGBA(w, h)
	Render(s, buf) // must not panic
}

// formatSize edge cases: 0, just-under-1024, exactly 1024, multi-tier.
func TestFormatSize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1234, "1.2 KB"},
		{89012, "86.9 KB"},
		{1024 * 1024, "1.0 MB"},
		{12 * 1024 * 1024, "12.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TB"},
		// Past TB we cap at TB (the largest unit), so a 2 PB count would
		// display as "2048.0 TB" -- exercise the cap branch too.
		{2 * 1024 * 1024 * 1024 * 1024 * 1024, "2048.0 TB"},
	}
	for _, c := range cases {
		if got := formatSize(c.in); got != c.want {
			t.Errorf("formatSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// drawText skips control characters silently.
func TestDrawTextSkipsControlBytes(t *testing.T) {
	w, h := 100, 16
	buf := newRGBA(w, h)
	fillRect(buf, w, h, 0, 0, w, h, ColorWindowBG)
	drawText(buf, w, h, 2, 2, "\x01\x02hi", 1, ColorTextPrimary)
	// At least one pixel must have changed (the 'h' or 'i' inked).
	changed := false
	for i := 0; i+3 < len(buf); i += 4 {
		if buf[i] != ColorWindowBG[0] {
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
	w, h := 30, 16
	buf := newRGBA(w, h)
	drawText(buf, w, h, 2, 2, "abcdefghij", 1, ColorTextPrimary)
}

// drawGlyph clips out-of-range pixels (negative + past width/height).
func TestDrawGlyphClips(t *testing.T) {
	w, h := 8, 8
	buf := newRGBA(w, h)
	// Drawing at (-4,-4) with scale 1 should clip the upper-left quadrant.
	drawGlyph(buf, w, h, -4, -4, 'A', 1, ColorTextPrimary)
	// Drawing at (12,12) is fully outside.
	drawGlyph(buf, w, h, 12, 12, 'A', 1, ColorTextPrimary)
}

// drawTextRight right-aligns to rx; not-bold path.
func TestDrawTextRight(t *testing.T) {
	w, h := 200, 16
	buf := newRGBA(w, h)
	drawTextRight(buf, w, h, 100, 2, "ABC", 1, ColorTextPrimary, false)
	// At x = 100 - 3*8 = 76, we expect "A" ink in the band 2..10.
	// Just assert SOMETHING was painted near the expected column.
	found := false
	for x := 76; x < 100 && !found; x++ {
		for y := 2; y < 10 && !found; y++ {
			if eqRGB(pixelAt(buf, w, x, y), ColorTextPrimary) {
				found = true
			}
		}
	}
	if !found {
		t.Error("drawTextRight produced no ink in expected band")
	}
}

// drawTextRight bold path -- second pass at +1 thickens the glyph.
func TestDrawTextRightBold(t *testing.T) {
	w, h := 200, 16
	buf := newRGBA(w, h)
	drawTextRight(buf, w, h, 100, 2, "X", 1, ColorTextPrimary, true)
	// Just confirm no panic + at least one ink pixel landed.
	found := false
	for i := 0; i+3 < len(buf); i += 4 {
		if buf[i] == ColorTextPrimary[0] {
			found = true
			break
		}
	}
	if !found {
		t.Error("bold drawTextRight produced no ink")
	}
}

// fillRect with an empty rectangle is a no-op.
func TestFillRectEmpty(t *testing.T) {
	w, h := 16, 16
	buf := newRGBA(w, h)
	fillRect(buf, w, h, 5, 5, 0, 0, ColorTextPrimary)
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
	fillRect(buf, w, h, -4, -4, 16, 16, ColorTextPrimary)
	px := pixelAt(buf, w, 0, 0)
	if !eqRGB(px, ColorTextPrimary) {
		t.Errorf("fillRect clipped failed to write (0,0); got %v", px)
	}
}

// fillRect fully outside the surface is a no-op (no panic, no writes).
func TestFillRectOutside(t *testing.T) {
	w, h := 8, 8
	buf := newRGBA(w, h)
	fillRect(buf, w, h, 10, 10, 4, 4, ColorTextPrimary)
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("fillRect fully outside leaked")
		}
	}
}

// Glyph returns the fallback solid block for a byte missing from the font
// table.
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

// SidebarSamplePoint + WindowBGSamplePoint return coordinates inside the
// expected regions.
func TestSamplePoints(t *testing.T) {
	x, y := SidebarSamplePoint()
	if x < 0 || x >= SidebarWidth || y < ToolbarHeight {
		t.Errorf("SidebarSamplePoint() = (%d,%d), expected inside sidebar", x, y)
	}
	x, y = WindowBGSamplePoint(720, 440)
	if x <= SidebarWidth || y <= ToolbarHeight {
		t.Errorf("WindowBGSamplePoint() = (%d,%d), expected in right pane", x, y)
	}
}

// DrawText (exported alias) writes to the buffer.
func TestDrawTextExported(t *testing.T) {
	w, h := 100, 16
	buf := newRGBA(w, h)
	DrawText(buf, w, h, 2, 2, 1, ColorTextPrimary, "Hi")
	found := false
	for i := 0; i+3 < len(buf); i += 4 {
		if buf[i] == ColorTextPrimary[0] {
			found = true
			break
		}
	}
	if !found {
		t.Error("DrawText produced no ink")
	}
}

// paintSidebar with no SidebarSelected (-1) paints no accent strip.
func TestPaintSidebarNoSelection(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	s.SidebarSelected = -1
	buf := newRGBA(w, h)
	Render(s, buf)
	// Inside the first Favorite row, past the icon: should be sidebar BG.
	y := ToolbarHeight + SidebarHeaderHeight + SidebarRowHeight/2
	x := SidebarWidth/2 + 20
	px := pixelAt(buf, w, x, y)
	if !eqRGB(px, ColorSidebarBG) {
		t.Errorf("unselected sidebar row at (%d,%d) = %v, want %v", x, y, px, ColorSidebarBG)
	}
}

// Render with the surface dimensions wired into worker.js (720x440) does
// not panic and lays out all four root entries.
func TestRenderAtProductionSize(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	Render(s, newRGBA(w, h))
	// All four root entries fit -- the 4th row's accent strip is reachable
	// at row index 3.
	if len(s.Browser.Entries) != 4 {
		t.Errorf("entries = %d, want 4", len(s.Browser.Entries))
	}
}
