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

// firstBookmarkRowY returns the y-coordinate of the top of the Home row
// (the first entry under the "BOOKMARKS" section header). Mirrors the
// layout walk paintSidebar performs.
func firstBookmarkRowY() int {
	return HeaderBarHeight + SidebarTopPadding + SidebarSectionHeaderHeight
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
// every entry row).
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

// The header bar fills the top band with ColorHeaderBarBG. We sample the
// far right of the bar -- nothing else paints there.
func TestHeaderBarBG(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	buf := newRGBA(w, h)
	Render(s, buf)
	hx, hy := HeaderBarSamplePoint(w)
	px := pixelAt(buf, w, hx, hy)
	if !eqRGB(px, ColorHeaderBarBG) {
		t.Errorf("header bar BG = %v, want %v", px, ColorHeaderBarBG)
	}
}

// The selected list row paints the accent colour across the right pane.
func TestSelectedRowAccent(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	buf := newRGBA(w, h)
	Render(s, buf)
	// Row 0 is selected by default. Sample inside the row, past the icon
	// but before the name (the first IconSize+10 pixels are icon-then-gap).
	y := HeaderBarHeight + ColumnHeaderHeight + RowHeight/2
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
	y0 := HeaderBarHeight + ColumnHeaderHeight + RowHeight/2
	x := SidebarWidth + 4
	px0 := pixelAt(buf, w, x, y0)
	if eqRGB(px0, ColorAccent) {
		t.Errorf("row[0] still accent after ArrowDown: %v", px0)
	}
	// Row 1 is now selected.
	y1 := HeaderBarHeight + ColumnHeaderHeight + RowHeight + RowHeight/2
	px1 := pixelAt(buf, w, x, y1)
	if !eqRGB(px1, ColorAccent) {
		t.Errorf("row[1] accent after ArrowDown = %v, want %v", px1, ColorAccent)
	}
}

// The selected-sidebar highlight paints accent inside the sidebar. We
// sample the right-edge gutter of the row band so the pixel reflects the
// accent fill rather than the label glyph painted on top of it.
func TestSidebarSelectionAccent(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	// Click on the second sidebar row (Documents -- index 1).
	clickY := firstBookmarkRowY() + SidebarRowHeight + SidebarRowHeight/2
	if !s.HandleMouse(20, clickY) {
		t.Fatal("sidebar click returned false")
	}
	buf := newRGBA(w, h)
	Render(s, buf)
	// Right-edge of the selected band, just inside the 1px divider.
	x := SidebarWidth - 4
	y := firstBookmarkRowY() + SidebarRowHeight + SidebarRowHeight/2
	px := pixelAt(buf, w, x, y)
	if !eqRGB(px, ColorAccent) {
		t.Errorf("sidebar selection at (%d,%d) = %v, want accent %v", x, y, px, ColorAccent)
	}
}

// paintFolderIcon paints the folder face pixel at the icon's mid-body.
func TestPaintFolderIcon(t *testing.T) {
	w, h := 64, 32
	buf := newRGBA(w, h)
	paintFolderIcon(buf, w, h, 4, 4, false)
	// The body fill sits at y+4..y+15 -- sample interior (avoid stroke edges).
	px := pixelAt(buf, w, 4+12, 4+10)
	if !eqRGB(px, ColorFolderFill) {
		t.Errorf("folder face = %v, want %v", px, ColorFolderFill)
	}
	// Selected variant flips to white.
	buf2 := newRGBA(w, h)
	paintFolderIcon(buf2, w, h, 4, 4, true)
	px2 := pixelAt(buf2, w, 4+12, 4+10)
	if !eqRGB(px2, ColorOnAccent) {
		t.Errorf("selected folder face = %v, want %v", px2, ColorOnAccent)
	}
}

// paintFileIcon paints white paper with a gray stroke.
func TestPaintFileIcon(t *testing.T) {
	w, h := 64, 64
	buf := newRGBA(w, h)
	paintFileIcon(buf, w, h, 4, 4, false)
	// Mid-paper, away from the fold cut + edges.
	px := pixelAt(buf, w, 4+4, 4+15)
	if !eqRGB(px, ColorFilePaper) {
		t.Errorf("file paper = %v, want %v", px, ColorFilePaper)
	}
	// Stroke on the left edge.
	pxB := pixelAt(buf, w, 4, 4+10)
	if !eqRGB(pxB, ColorFileBorder) {
		t.Errorf("file border = %v, want %v", pxB, ColorFileBorder)
	}
	// Selected variant flips the paper to white-on-accent.
	buf2 := newRGBA(w, h)
	paintFileIcon(buf2, w, h, 4, 4, true)
	px2 := pixelAt(buf2, w, 4+4, 4+15)
	if !eqRGB(px2, ColorOnAccent) {
		t.Errorf("selected file paper = %v, want %v", px2, ColorOnAccent)
	}
}

// paintHeaderBar paints the back-arrow chevron in the primary ink.
func TestPaintHeaderBarBackButton(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	buf := newRGBA(w, h)
	paintHeaderBar(buf, w, h, s)
	// The chevron sits roughly at BackBtnX+BackBtnW/2-3, BackBtnY+BackBtnH/2.
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

// paintHamburger paints three horizontal lines in the primary ink.
func TestPaintHamburger(t *testing.T) {
	w, h := 720, 440
	buf := newRGBA(w, h)
	fillRect(buf, w, h, 0, 0, w, h, ColorHeaderBarBG)
	paintHamburger(buf, w, h)
	// Sample the first line (the top "stripe"): inside the button at lx+1.
	px := pixelAt(buf, w, HamburgerBtnX+8, HamburgerBtnY+7)
	if !eqRGB(px, ColorTextPrimary) {
		t.Errorf("hamburger top line = %v, want %v", px, ColorTextPrimary)
	}
}

// paintForwardButton with enabled=false uses the disabled ink.
func TestPaintForwardButtonDisabled(t *testing.T) {
	w, h := 720, 440
	buf := newRGBA(w, h)
	fillRect(buf, w, h, 0, 0, w, h, ColorHeaderBarBG)
	paintForwardButton(buf, w, h, false)
	// Look for ColorButtonDisabled ink anywhere inside the forward button.
	found := false
	for y := ForwardBtnY + 4; y < ForwardBtnY+ForwardBtnH-4 && !found; y++ {
		for x := ForwardBtnX + 4; x < ForwardBtnX+ForwardBtnW-4 && !found; x++ {
			if eqRGB(pixelAt(buf, w, x, y), ColorButtonDisabled) {
				found = true
			}
		}
	}
	if !found {
		t.Error("forward chevron (disabled) not painted in disabled ink")
	}
}

// paintForwardButton with enabled=true uses the primary ink.
func TestPaintForwardButtonEnabled(t *testing.T) {
	w, h := 720, 440
	buf := newRGBA(w, h)
	fillRect(buf, w, h, 0, 0, w, h, ColorHeaderBarBG)
	paintForwardButton(buf, w, h, true)
	found := false
	for y := ForwardBtnY + 4; y < ForwardBtnY+ForwardBtnH-4 && !found; y++ {
		for x := ForwardBtnX + 4; x < ForwardBtnX+ForwardBtnW-4 && !found; x++ {
			if eqRGB(pixelAt(buf, w, x, y), ColorTextPrimary) {
				found = true
			}
		}
	}
	if !found {
		t.Error("forward chevron (enabled) not painted in primary ink")
	}
}

// paintPathBar at root renders just "Home" as the active crumb.
func TestPaintPathBarRoot(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	buf := newRGBA(w, h)
	fillRect(buf, w, h, 0, 0, w, h, ColorHeaderBarBG)
	paintPathBar(buf, w, h, s)
	// At the root the only crumb is "Home"; sample the active-crumb fill.
	px := pixelAt(buf, w, PathBarX+2, PathBarY+PathBarH/2)
	if !eqRGB(px, ColorCrumbActiveBG) {
		t.Errorf("active crumb fill = %v, want %v", px, ColorCrumbActiveBG)
	}
}

// paintPathBar with a nested path renders Home > <child> with two crumbs.
func TestPaintPathBarNested(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	_ = s.HandleKey("ArrowDown") // -> Documents row
	_ = s.HandleKey("ArrowUp")
	s.Browser.Cursor = 0
	_ = s.Browser.ActivateCurrent(s.VFS) // descend into Documents
	buf := newRGBA(w, h)
	fillRect(buf, w, h, 0, 0, w, h, ColorHeaderBarBG)
	paintPathBar(buf, w, h, s)
	// At /Documents the path is ["Home", "Documents"]; the second crumb is
	// active, so the active fill sits further to the right than at root.
	crumbs := PathCrumbs(s.Browser.CurrentPath)
	if len(crumbs) != 2 {
		t.Fatalf("crumbs = %v, want [Home Documents]", crumbs)
	}
}

// PathCrumbs handles a deeper path correctly.
func TestPathCrumbsDeep(t *testing.T) {
	got := PathCrumbs("/a/b/c")
	want := []string{"Home", "a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("PathCrumbs(/a/b/c) = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("crumb[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// PathCrumbs at the root returns just "Home".
func TestPathCrumbsRoot(t *testing.T) {
	got := PathCrumbs("/")
	if len(got) != 1 || got[0] != "Home" {
		t.Errorf("PathCrumbs(/) = %v, want [Home]", got)
	}
}

// paintColumnHeaders paints the header band with the window BG colour.
func TestPaintColumnHeaders(t *testing.T) {
	w, h := 720, 440
	buf := newRGBA(w, h)
	// Paint a baseline so the test exercises the header on its own.
	fillRect(buf, w, h, 0, 0, w, h, [3]uint8{1, 2, 3})
	paintColumnHeaders(buf, w, h)
	// Sample a column in the header band, well to the right (past "Name").
	y := HeaderBarHeight + 4
	px := pixelAt(buf, w, w-50, y)
	if !eqRGB(px, ColorWindowBG) {
		t.Errorf("column header BG = %v, want %v", px, ColorWindowBG)
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
	y := HeaderBarHeight + ColumnHeaderHeight + RowHeight/2
	px := pixelAt(buf, w, SidebarWidth+4, y)
	if !eqRGB(px, ColorWindowBG) {
		t.Errorf("empty list row[0] BG = %v, want %v", px, ColorWindowBG)
	}
}

// paintListRows clips rows past the surface height (exercises the
// `y >= h` early-break).
func TestPaintListRowsClipsBeyondSurface(t *testing.T) {
	w := 720
	// Surface tall enough for the header+column-header+1 row but short of all 4.
	h := HeaderBarHeight + ColumnHeaderHeight + RowHeight + 2
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
		{0, "0 bytes"},
		{1, "1 bytes"},
		{1023, "1023 bytes"},
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
	if x < 0 || x >= SidebarWidth || y < HeaderBarHeight {
		t.Errorf("SidebarSamplePoint() = (%d,%d), expected inside sidebar", x, y)
	}
	x, y = WindowBGSamplePoint(720, 440)
	if x <= SidebarWidth || y <= HeaderBarHeight {
		t.Errorf("WindowBGSamplePoint() = (%d,%d), expected in right pane", x, y)
	}
	x, y = HeaderBarSamplePoint(720)
	if y >= HeaderBarHeight || x <= 0 {
		t.Errorf("HeaderBarSamplePoint() = (%d,%d), expected inside header bar", x, y)
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

// paintSidebar with SidebarSelected=-1 paints no accent strip on any row.
// We sample a point inside a row band, but in the right-edge gutter (past
// the label glyphs) so the sample reflects pure sidebar BG -- not label ink.
func TestPaintSidebarNoSelection(t *testing.T) {
	w, h := 720, 440
	s := New(w, h)
	s.SidebarSelected = -1
	buf := newRGBA(w, h)
	Render(s, buf)
	// Inside the second sidebar row (Documents), at x=SidebarWidth-4 (the
	// 1px divider sits at SidebarWidth-1, so -4 is well clear of the
	// label glyph + the divider).
	y := firstBookmarkRowY() + SidebarRowHeight + SidebarRowHeight/2
	x := SidebarWidth - 4
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
	if len(s.Browser.Entries) != 4 {
		t.Errorf("entries = %d, want 4", len(s.Browser.Entries))
	}
}

// paintSidebarIcon exercises every kind (the dispatch is keyed by Kind).
func TestPaintSidebarIconKinds(t *testing.T) {
	w, h := 32, 32
	for _, kind := range []string{"home", "computer", "trash", "folder", "unknown"} {
		buf := newRGBA(w, h)
		paintSidebarIcon(buf, w, h, 4, 8, kind, false)
		// Each glyph must paint at least one non-zero pixel.
		any := false
		for _, b := range buf {
			if b != 0 {
				any = true
				break
			}
		}
		if !any {
			t.Errorf("paintSidebarIcon(%q) drew nothing", kind)
		}
	}
}

// paintSidebarIcon selected variant flips colours for every kind.
func TestPaintSidebarIconSelected(t *testing.T) {
	w, h := 32, 32
	for _, kind := range []string{"home", "computer", "trash", "folder"} {
		buf := newRGBA(w, h)
		paintSidebarIcon(buf, w, h, 4, 8, kind, true)
		// Selected variants paint at least one ColorOnAccent pixel.
		any := false
		for i := 0; i+3 < len(buf); i += 4 {
			if buf[i] == ColorOnAccent[0] && buf[i+1] == ColorOnAccent[1] && buf[i+2] == ColorOnAccent[2] && buf[i+3] == 0xFF {
				any = true
				break
			}
		}
		if !any {
			t.Errorf("paintSidebarIcon(%q,selected) produced no white ink", kind)
		}
	}
}
