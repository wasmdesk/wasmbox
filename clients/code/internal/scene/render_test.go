// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import (
	"testing"

	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

// pixelAt reads the RGB triple at (x, y) from a w*h RGBA32 buffer.
func pixelAt(buf []byte, w, x, y int) [3]uint8 {
	off := (y*w + x) * 4
	return [3]uint8{buf[off], buf[off+1], buf[off+2]}
}

// countColor counts the number of (x, y) pixels in the rectangle (rx,ry,rw,rh)
// of a w*h RGBA32 buffer that match c.
func countColor(buf []byte, w int, rx, ry, rw, rh int, c [3]uint8) int {
	n := 0
	for y := ry; y < ry+rh; y++ {
		for x := rx; x < rx+rw; x++ {
			if pixelAt(buf, w, x, y) == c {
				n++
			}
		}
	}
	return n
}

func TestRender_PanicsOnSizeMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	s := New(100, 100)
	buf := make([]byte, 4*99*100) // wrong
	Render(s, buf)
}

func TestRender_BasicLayout(t *testing.T) {
	w, h := 900, 540
	v := sharedvfs.NewInMemoryVFS()
	sharedvfs.SeedDemoTree(v)
	s := NewWithVFS(w, h, v)
	s.Buffer = NewTextBuffer("hello")
	buf := make([]byte, 4*w*h)
	Render(s, buf)

	// Sidebar pixel sample.
	if got := pixelAt(buf, w, 4, 100); got != ColorSidebarBG {
		t.Errorf("sidebar @ (4,100) = %v, want %v", got, ColorSidebarBG)
	}
	// Editor BG sample (right of sidebar+gutter, mid height).
	if got := pixelAt(buf, w, SidebarWidth+GutterWidth+200, h/2); got != ColorWindowBG {
		t.Errorf("editor BG = %v, want %v", got, ColorWindowBG)
	}
	// Status bar sample (any x, bottom band).
	if got := pixelAt(buf, w, 400, h-5); got != ColorStatusBarBG {
		t.Errorf("status bar = %v, want %v", got, ColorStatusBarBG)
	}
	// Tab strip background -- sample far past the active tab in the empty band.
	if got := pixelAt(buf, w, w-50, 4); got != ColorTabStripBG {
		t.Errorf("tab strip = %v, want %v", got, ColorTabStripBG)
	}
}

func TestPaint_DrawsLineNumbers(t *testing.T) {
	w, h := 600, 400
	s := New(w, h)
	s.Buffer = NewTextBuffer("a\nb\nc")
	buf := make([]byte, 4*w*h)
	Paint(buf, w, h, s)
	// Look for at least a few gutter-ink pixels inside the gutter region.
	gut := countColor(buf, w, SidebarWidth, TabStripHeight, GutterWidth, h-StatusBarHeight-TabStripHeight, ColorGutterText)
	if gut < 5 {
		t.Errorf("gutter ink pixels = %d, want >= 5", gut)
	}
}

func TestPaint_KeywordHighlight(t *testing.T) {
	w, h := 900, 540
	s := New(w, h)
	s.Buffer = NewTextBuffer("func main() {")
	buf := make([]byte, 4*w*h)
	Paint(buf, w, h, s)
	// Count keyword-colour pixels in the editor region.
	kw := countColor(buf, w, SidebarWidth+GutterWidth, TabStripHeight, w-SidebarWidth-GutterWidth, h-TabStripHeight-StatusBarHeight, ColorKeyword)
	if kw < 20 {
		t.Errorf("keyword pixels = %d, want >= 20", kw)
	}
}

func TestPaint_FlashSaveOK_OverpaintsStatusBar(t *testing.T) {
	w, h := 600, 400
	s := New(w, h)
	s.Flash = FlashSaveOK
	buf := make([]byte, 4*w*h)
	Paint(buf, w, h, s)
	if got := pixelAt(buf, w, 100, h-5); got != ColorFlashSaveOK {
		t.Errorf("flash save: %v, want %v", got, ColorFlashSaveOK)
	}
}

func TestPaint_FlashInfo_OverpaintsStatusBar(t *testing.T) {
	w, h := 600, 400
	s := New(w, h)
	s.Flash = FlashInfo
	buf := make([]byte, 4*w*h)
	Paint(buf, w, h, s)
	if got := pixelAt(buf, w, 100, h-5); got != ColorFlashInfo {
		t.Errorf("flash info: %v, want %v", got, ColorFlashInfo)
	}
}

func TestPaint_PopupRendered(t *testing.T) {
	w, h := 900, 540
	s := New(w, h)
	s.LiveServerPopupOpen = true
	s.LiveServerURL = "wss://test"
	buf := make([]byte, 4*w*h)
	Paint(buf, w, h, s)
	// Connect button background.
	if got := pixelAt(buf, w, PopupConnectX+10, PopupConnectY+10); got != ColorPopupButton {
		t.Errorf("popup button: %v, want %v", got, ColorPopupButton)
	}
	// Panel BG at popup centre.
	if got := pixelAt(buf, w, PopupX+30, PopupY+5); got != ColorPopupBG {
		t.Errorf("popup BG: %v, want %v", got, ColorPopupBG)
	}
}

func TestPaint_TabStripWithOpenFile(t *testing.T) {
	w, h := 900, 540
	v := sharedvfs.NewInMemoryVFS()
	sharedvfs.SeedDemoTree(v)
	s := NewWithVFS(w, h, v)
	if !s.OpenFile("/about.txt") {
		t.Fatal("open")
	}
	buf := make([]byte, 4*w*h)
	Paint(buf, w, h, s)
	// Active tab fill (== editor BG) is present at tab strip area near sidebar.
	if got := pixelAt(buf, w, SidebarWidth+5, 4); got != ColorActiveTabBG {
		t.Errorf("active tab: %v, want %v", got, ColorActiveTabBG)
	}
}

func TestPaint_SidebarShowsDirPrefix(t *testing.T) {
	w, h := 900, 540
	v := sharedvfs.NewInMemoryVFS()
	sharedvfs.SeedDemoTree(v)
	s := NewWithVFS(w, h, v)
	buf := make([]byte, 4*w*h)
	Paint(buf, w, h, s)
	// Just ensure some sidebar ink is painted (at least one row name pixel).
	ink := countColor(buf, w, 0, TabStripHeight, SidebarWidth, h-TabStripHeight-StatusBarHeight, ColorSidebarTextDim)
	if ink < 10 {
		t.Errorf("sidebar ink = %d, want >= 10", ink)
	}
}

func TestPaint_FlashNone_DefaultStatusBar(t *testing.T) {
	w, h := 600, 400
	s := New(w, h)
	// Flash is FlashNone by default.
	buf := make([]byte, 4*w*h)
	Paint(buf, w, h, s)
	if got := pixelAt(buf, w, 100, h-5); got != ColorStatusBarBG {
		t.Errorf("default status: %v, want %v", got, ColorStatusBarBG)
	}
}

func TestPaint_SidebarRowsClampedOnTallTree(t *testing.T) {
	// Make a VFS with so many entries the sidebar overflows below the status
	// bar; the renderer should break out of the loop rather than draw past.
	v := sharedvfs.NewInMemoryVFS()
	for i := 0; i < 200; i++ {
		_ = v.Write("/f"+itoa(i)+".txt", []byte("x"))
	}
	w, h := 600, 400
	s := NewWithVFS(w, h, v)
	buf := make([]byte, 4*w*h)
	Paint(buf, w, h, s)
	// Status bar still painted.
	if got := pixelAt(buf, w, 100, h-5); got != ColorStatusBarBG {
		t.Errorf("status bar overwritten by sidebar overflow: %v", got)
	}
}

// itoa is a tiny formatter for the overflow test (we keep strconv out of the
// hot test path).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestPaint_CursorPainted(t *testing.T) {
	w, h := 600, 400
	s := New(w, h)
	s.Buffer = NewTextBuffer("abc")
	s.Buffer.SetCursor(0, 1)
	buf := make([]byte, 4*w*h)
	Paint(buf, w, h, s)
	// Cursor at row 0, col 1 -- look inside the cursor rect.
	cx := SidebarWidth + GutterWidth + 1*FontW*EditorFontScale
	cy := TabStripHeight + (LineHeight-FontH*EditorFontScale)/2 + 2
	if got := pixelAt(buf, w, cx, cy); got != ColorCursor {
		t.Errorf("cursor pixel: %v, want %v", got, ColorCursor)
	}
}

func TestPaint_CursorClippedWhenOffScreen(t *testing.T) {
	w, h := 600, 60
	s := New(w, h)
	// Build a buffer with many lines so the cursor's row exceeds visible.
	body := ""
	for i := 0; i < 50; i++ {
		body += "x\n"
	}
	s.Buffer = NewTextBuffer(body)
	s.Buffer.SetCursor(40, 0)
	buf := make([]byte, 4*w*h)
	// Should NOT panic and should not paint the cursor.
	Paint(buf, w, h, s)
}

func TestDrawText_SkipsNonPrintable(t *testing.T) {
	w, h := 200, 60
	buf := make([]byte, 4*w*h)
	for i := range buf {
		buf[i] = 0
	}
	drawText(buf, w, h, 0, 0, "\x01"+"A", 1, [3]uint8{255, 0, 0})
	// "A" should render at x=0 since the 0x01 byte is skipped (cx isn't advanced).
	// Count red pixels in the first 8x8 region.
	red := 0
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if pixelAt(buf, w, x, y) == [3]uint8{255, 0, 0} {
				red++
			}
		}
	}
	if red == 0 {
		t.Fatal("no ink after skipping non-printable")
	}
}

func TestDrawText_ClipsAtRightEdge(t *testing.T) {
	w, h := 16, 8
	buf := make([]byte, 4*w*h)
	// Three glyphs at scale 1 = 24 px > w=16; loop should break.
	drawText(buf, w, h, 0, 0, "ABC", 1, [3]uint8{1, 2, 3})
	// No panic = pass.
}

func TestFillRect_Clips(t *testing.T) {
	w, h := 10, 10
	buf := make([]byte, 4*w*h)
	// Out-of-bounds top-left + over-extending right/bottom: should clip.
	fillRect(buf, w, h, -5, -5, 20, 20, [3]uint8{9, 9, 9})
	// Every pixel inside should now be (9,9,9).
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if pixelAt(buf, w, x, y) != [3]uint8{9, 9, 9} {
				t.Fatalf("(%d,%d)=%v", x, y, pixelAt(buf, w, x, y))
			}
		}
	}
}

func TestDrawGlyph_ClipsOutOfBounds(t *testing.T) {
	w, h := 4, 4
	buf := make([]byte, 4*w*h)
	// Glyph at (-2, -2) scale 2 spills off all four edges -- should not panic.
	drawGlyph(buf, w, h, -2, -2, 'A', 2, [3]uint8{1, 2, 3})
	// And again way past the right edge.
	drawGlyph(buf, w, h, 100, 100, 'A', 2, [3]uint8{1, 2, 3})
}

func TestSharedvfsBasename(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"file.txt", "file.txt"},
		{"/file.txt", "file.txt"},
		{"/a/b.txt", "b.txt"},
	}
	for _, c := range cases {
		if got := sharedvfsBasename(c.in); got != c.want {
			t.Errorf("basename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
