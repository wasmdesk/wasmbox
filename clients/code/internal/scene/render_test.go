// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import (
	"testing"

	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

// probeGutterWidth is the horizontal offset the playwright probe adds to the
// sidebar to derive its editor-scan origin (edLeft = left + SidebarWidth +
// this). The TextView owns a narrower auto gutter now, but the probe scans a
// wide band from this offset rightward, so the tests mirror the same origin
// to prove the first keyword paints where the probe looks.
const probeGutterWidth = 50

// pixelAt reads the RGB triple at (x, y) from a w*h RGBA32 buffer.
func pixelAt(buf []byte, w, x, y int) [3]uint8 {
	off := (y*w + x) * 4
	return [3]uint8{buf[off], buf[off+1], buf[off+2]}
}

// countColor counts pixels in (rx,ry,rw,rh) of a w*h RGBA32 buffer matching c.
func countColor(buf []byte, w, rx, ry, rw, rh int, c [3]uint8) int {
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

func newSeededState(t *testing.T, w, h int) *SceneState {
	t.Helper()
	v := sharedvfs.NewInMemoryVFS()
	sharedvfs.SeedDemoTree(v)
	return NewWithVFS(w, h, v)
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

// TestRender_RegionBackgrounds asserts the four VS Code Dark+ region colours
// land where the playwright probe samples them: sidebar #252526, editor
// #1E1E1E, status bar #007ACC, and tab strip #2D2D30.
func TestRender_RegionBackgrounds(t *testing.T) {
	w, h := 900, 460
	s := newSeededState(t, w, h)
	buf := make([]byte, 4*w*h)
	Render(s, buf)

	// Sidebar band, sampled low (below the file rows) as the probe does.
	if got := pixelAt(buf, w, 40, h-StatusBarHeight-6); got != ColorSidebarBG {
		t.Errorf("sidebar @ (40,%d) = %v, want %v", h-StatusBarHeight-6, got, ColorSidebarBG)
	}
	// Editor interior, right of sidebar+probe-gutter.
	if got := pixelAt(buf, w, SidebarWidth+probeGutterWidth+60, h-StatusBarHeight-6); got != ColorWindowBG {
		t.Errorf("editor BG = %v, want %v", got, ColorWindowBG)
	}
	// Status bar (left edge, over no glyph).
	if got := pixelAt(buf, w, 2, h-StatusBarHeight/2); got != ColorStatusBarBG {
		t.Errorf("status bar = %v, want %v", got, ColorStatusBarBG)
	}
	// Tab strip, far past the active tab.
	if got := pixelAt(buf, w, w-50, 4); got != ColorTabStripBG {
		t.Errorf("tab strip = %v, want %v", got, ColorTabStripBG)
	}
}

// TestRender_KeywordInkPastProbeOrigin renders "func main() {" into the
// editor and asserts keyword-blue (#569CD6) pixels appear in the SAME band
// the probe scans (x >= SidebarWidth+probeGutterWidth). This is the native
// mirror of the browser probe's keyword-highlight assertion.
func TestRender_KeywordInkPastProbeOrigin(t *testing.T) {
	w, h := 900, 460
	s := newSeededState(t, w, h)
	s.Editor.SetText("func main() {")
	buf := make([]byte, 4*w*h)
	Render(s, buf)

	edLeft := SidebarWidth + probeGutterWidth
	kw := countColor(buf, w, edLeft, TabStripHeight, w-edLeft, h-TabStripHeight-StatusBarHeight, ColorKeyword)
	if kw < 12 {
		t.Fatalf("keyword-blue pixels past probe origin = %d, want >= 12", kw)
	}
}

// TestRender_GutterReservesWidth asserts the TextView line-number gutter
// paints numbers (ColorGutterText) AND reserves width -- the editor text is
// pushed right of the sidebar by more than the 4 px bare inset, so gutter ink
// exists between the sidebar edge and the first glyph column.
func TestRender_GutterReservesWidth(t *testing.T) {
	w, h := 600, 400
	s := New(w, h)
	s.Editor.SetText("a\nb\nc")
	buf := make([]byte, 4*w*h)
	Render(s, buf)
	// Gutter ink lives just right of the sidebar, within the reserved column.
	gut := countColor(buf, w, SidebarWidth, TabStripHeight, 40, h-TabStripHeight-StatusBarHeight, ColorGutterText)
	if gut < 5 {
		t.Fatalf("gutter ink pixels = %d, want >= 5 (gutter not reserved?)", gut)
	}
}

func TestRender_FlashSaveOK(t *testing.T) {
	w, h := 600, 400
	s := New(w, h)
	s.Flash = FlashSaveOK
	buf := make([]byte, 4*w*h)
	Render(s, buf)
	if got := pixelAt(buf, w, 2, h-StatusBarHeight/2); got != ColorFlashSaveOK {
		t.Errorf("flash save: %v, want %v", got, ColorFlashSaveOK)
	}
}

func TestRender_FlashInfo(t *testing.T) {
	w, h := 600, 400
	s := New(w, h)
	s.Flash = FlashInfo
	buf := make([]byte, 4*w*h)
	Render(s, buf)
	if got := pixelAt(buf, w, 2, h-StatusBarHeight/2); got != ColorFlashInfo {
		t.Errorf("flash info: %v, want %v", got, ColorFlashInfo)
	}
}

func TestRender_FlashNoneDefaultBlue(t *testing.T) {
	w, h := 600, 400
	s := New(w, h)
	buf := make([]byte, 4*w*h)
	Render(s, buf)
	if got := pixelAt(buf, w, 2, h-StatusBarHeight/2); got != ColorStatusBarBG {
		t.Errorf("default status: %v, want %v", got, ColorStatusBarBG)
	}
}

// TestRender_TabWithOpenFile draws the active tab (FolderTabs) with an open
// file and asserts its body fills the active-tab colour (== editor BG). The
// sample is taken well inside the tab body -- past the rounded top corners and
// the top accent bar -- since FolderTabs gives the active tab rounded top
// corners (the very corner pixels show the strip behind, by design). It also
// exercises tabLabel's file-open branch.
func TestRender_TabWithOpenFile(t *testing.T) {
	w, h := 900, 460
	s := newSeededState(t, w, h)
	if !s.OpenFile("/about.txt") {
		t.Fatal("open")
	}
	buf := make([]byte, 4*w*h)
	Render(s, buf)
	// Interior of the (single) active tab: within the tab's left padding gap
	// (the label is centred, so this is empty fill), below the rounded-corner
	// band and the accent bar, above the strip bottom.
	if got := pixelAt(buf, w, SidebarWidth+8, 20); got != ColorActiveTabBG {
		t.Errorf("active tab body: %v, want %v", got, ColorActiveTabBG)
	}
}

// TestRender_SidebarInk asserts the file-tree rows paint their dim ink.
func TestRender_SidebarInk(t *testing.T) {
	w, h := 900, 460
	s := newSeededState(t, w, h)
	buf := make([]byte, 4*w*h)
	Render(s, buf)
	ink := countColor(buf, w, 0, 0, SidebarWidth, h-StatusBarHeight, ColorSidebarTextDim)
	if ink < 20 {
		t.Fatalf("sidebar ink = %d, want >= 20", ink)
	}
}

// TestRender_Popup draws the Live Server dialog when open (exercises the
// popup branch of Paint + layoutDialog + the Entry showing the URL).
func TestRender_Popup(t *testing.T) {
	w, h := 900, 460
	s := New(w, h)
	s.LiveServerPopupOpen = true
	s.LiveServerURL = "wss://test"
	buf := make([]byte, 4*w*h)
	Render(s, buf)
	// The dialog's title bar (SurfaceAlt == #2D2D30) overpaints the editor
	// near the surface centre, proving the popup rendered over the editor.
	dialogY := (h - DialogH) / 2
	if got := pixelAt(buf, w, w/2, dialogY+5); got != ColorTabStripBG {
		t.Errorf("popup title bar = %v, want %v", got, ColorTabStripBG)
	}
}

// TestBasename covers the tab-label path helper, both with and without a
// slash and the empty case.
func TestBasename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"file.txt", "file.txt"},
		{"/file.txt", "file.txt"},
		{"/a/b.txt", "b.txt"},
	}
	for _, c := range cases {
		if got := basename(c.in); got != c.want {
			t.Errorf("basename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRender_ShortTabLabel renders with a one-character basename open: the
// FolderTabs tab sizes to its (very short) label plus padding, and the strip
// must paint without panicking.
func TestRender_ShortTabLabel(t *testing.T) {
	w, h := 900, 460
	v := sharedvfs.NewInMemoryVFS()
	_ = v.Write("/x", []byte("hi"))
	s := NewWithVFS(w, h, v)
	if !s.OpenFile("/x") {
		t.Fatal("open /x")
	}
	buf := make([]byte, 4*w*h)
	Render(s, buf) // must not panic; tab floor applies
}

// isBlend reports whether px is a partial-coverage blend of endpoints a and b
// (an anti-aliasing signature): on every channel where a and b differ, px lies
// STRICTLY between them, and px equals neither pure endpoint. A bitmap font
// paints only the two pure colours, so a positive result proves AA glyphs.
func isBlend(px, a, b [3]uint8) bool {
	if px == a || px == b {
		return false
	}
	for i := 0; i < 3; i++ {
		lo, hi := a[i], b[i]
		if lo > hi {
			lo, hi = hi, lo
		}
		if lo != hi && (px[i] <= lo || px[i] >= hi) {
			return false
		}
	}
	return true
}

// TestRender_AntiAliasedMonospace is the programmatic AA + monospace proof the
// task requires: (1) the active editor face reports a FIXED advance -- equal-
// length runs measure equal regardless of glyph, and exactly len*advance -- so
// columns/caret stay aligned; and (2) rendering the keyword "func" produces
// edge pixels that are a partial blend of keyword-blue ink and the window BG,
// which a 5x7 bitmap font (two pure colours only) could never emit.
func TestRender_AntiAliasedMonospace(t *testing.T) {
	f := editorFace()
	adv := f.Advance()
	if adv <= 0 {
		t.Fatalf("advance = %d, want > 0", adv)
	}
	if got, want := f.Measure("iiii"), f.Measure("MMMM"); got != want {
		t.Fatalf("non-monospace face: Measure(iiii)=%d != Measure(MMMM)=%d", got, want)
	}
	if got := f.Measure("MMMM"); got != 4*adv {
		t.Fatalf("Measure(MMMM)=%d, want 4*advance=%d (fixed advance)", got, 4*adv)
	}
	w, h := 900, 460
	s := newSeededState(t, w, h)
	s.Editor.SetText("func")
	buf := make([]byte, 4*w*h)
	Render(s, buf)
	blended := 0
	for y := TabStripHeight; y < h-StatusBarHeight; y++ {
		for x := SidebarWidth; x < w; x++ {
			if isBlend(pixelAt(buf, w, x, y), ColorWindowBG, ColorKeyword) {
				blended++
			}
		}
	}
	if blended < 1 {
		t.Fatal("no anti-aliased blend pixels between keyword-blue and BG; glyphs not AA?")
	}
}
