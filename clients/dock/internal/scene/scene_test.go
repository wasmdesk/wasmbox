// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"testing"

	"github.com/wasmdesk/wasmbox/clients/dock/internal/theme"
)

// Surface size used by most tests: 1280-wide x 28-tall, matching the worker
// surface dimensions.
const (
	tW = 1280
	tH = BarHeight
)

func newBuf(s *State) []byte { return make([]byte, 4*s.W*s.H) }

func TestNewHasDefaults(t *testing.T) {
	s := New(tW, tH)
	if got, want := len(s.Apps), 4; got != want {
		t.Fatalf("default apps = %d, want %d", got, want)
	}
	want := []string{"terminal", "editor", "files", "hello"}
	for i, a := range s.Apps {
		if a.Id != want[i] {
			t.Fatalf("app[%d].Id = %q, want %q", i, a.Id, want[i])
		}
	}
	if s.Workspace != "1 of 4" {
		t.Fatalf("default workspace = %q, want %q", s.Workspace, "1 of 4")
	}
	if s.Clock != "" {
		t.Fatalf("default clock = %q, want empty (worker will tick)", s.Clock)
	}
	if s.Theme.Border.Width != 1 {
		t.Fatalf("default theme missing border width")
	}
}

// SectionLayout — the workspace label ends at x=WorkspaceW, the clock begins
// at x=W-ClockW, and the iconbar fills the middle.
func TestSectionLayout(t *testing.T) {
	s := New(tW, tH)
	wx, _, ww, wh := s.WorkspaceRect()
	if wx != 0 || ww != WorkspaceW || wh != tH {
		t.Fatalf("workspace rect = (%d,_,%d,%d), want (0,_,%d,%d)", wx, ww, wh, WorkspaceW, tH)
	}
	cx, _, cw, _ := s.ClockRect()
	if cx != tW-ClockW || cw != ClockW {
		t.Fatalf("clock rect = (%d,_,%d,_), want (%d,_,%d,_)", cx, cw, tW-ClockW, ClockW)
	}
	ix, _, iw, _ := s.IconbarRect()
	if ix != WorkspaceW || iw != tW-WorkspaceW-ClockW {
		t.Fatalf("iconbar rect = (%d,_,%d,_), want (%d,_,%d,_)", ix, iw, WorkspaceW, tW-WorkspaceW-ClockW)
	}
}

// On a narrow surface where workspace + clock would overlap, the iconbar
// collapses to width 0 (never negative).
func TestIconbarClampsToZeroOnNarrowSurface(t *testing.T) {
	s := New(50, tH) // 50 < WorkspaceW (100) + ClockW (80)
	_, _, iw, _ := s.IconbarRect()
	if iw != 0 {
		t.Fatalf("iconbar width on narrow surface = %d, want 0", iw)
	}
}

// IconbarButtonRect places the i-th button at WorkspaceW + i*(W+gap).
func TestIconbarButtonRectStride(t *testing.T) {
	s := New(tW, tH)
	wantH := tH - 2*IconbarVPad
	for i := range s.Apps {
		bx, by, bw, bh := s.IconbarButtonRect(i)
		wantX := WorkspaceW + i*(IconbarButtonW+IconbarButtonGap)
		if bx != wantX {
			t.Fatalf("button[%d].x = %d, want %d", i, bx, wantX)
		}
		if by != IconbarVPad {
			t.Fatalf("button[%d].y = %d, want %d", i, by, IconbarVPad)
		}
		if bw != IconbarButtonW || bh != wantH {
			t.Fatalf("button[%d] size = %dx%d, want %dx%d", i, bw, bh, IconbarButtonW, wantH)
		}
	}
}

// Button height scales to fill the granted surface height (tested for the
// h=60 case the compositor actually grants because Theme::MIN_H = 60).
func TestIconbarButtonRectScalesWithSurface(t *testing.T) {
	s := New(tW, 60)
	_, by, _, bh := s.IconbarButtonRect(0)
	if by != IconbarVPad {
		t.Fatalf("button.y at h=60 = %d, want %d", by, IconbarVPad)
	}
	if want := 60 - 2*IconbarVPad; bh != want {
		t.Fatalf("button.h at h=60 = %d, want %d", bh, want)
	}
}

// A degenerate surface (h < 2*IconbarVPad+1) clamps button height to 1
// instead of returning a non-positive size.
func TestIconbarButtonRectClampsHeight(t *testing.T) {
	s := New(tW, 1) // 1 < 2*IconbarVPad => negative would land here
	_, _, _, bh := s.IconbarButtonRect(0)
	if bh != 1 {
		t.Fatalf("button.h on 1-px surface = %d, want 1", bh)
	}
}

// A click at the center of button i must HitTest to i, and the resulting
// Apps[i].Id must be the documented launch string ("terminal"/"editor"/etc).
func TestClickAtButtonCenterDispatchesExpectedApp(t *testing.T) {
	cases := []string{"terminal", "editor", "files", "hello"}
	s := New(tW, tH)
	if got, want := len(s.Apps), len(cases); got != want {
		t.Fatalf("apps = %d, want %d", got, want)
	}
	for i, wantID := range cases {
		bx, by, bw, bh := s.IconbarButtonRect(i)
		px := bx + bw/2
		py := by + bh/2
		hit := s.HitTest(px, py)
		if hit != i {
			t.Fatalf("HitTest center of button %d = %d, want %d", i, hit, i)
		}
		if got := s.Apps[hit].Id; got != wantID {
			t.Fatalf("button %d dispatches %q, want %q", i, got, wantID)
		}
	}
}

// Clicks on the workspace label / clock are inert (HitTest returns -1).
func TestClicksOnWorkspaceAndClockAreInert(t *testing.T) {
	s := New(tW, tH)
	if got := s.HitTest(WorkspaceW/2, tH/2); got != -1 {
		t.Fatalf("workspace click HitTest = %d, want -1", got)
	}
	if got := s.HitTest(tW-ClockW/2, tH/2); got != -1 {
		t.Fatalf("clock click HitTest = %d, want -1", got)
	}
}

// A click above or below the button row inside the iconbar misses.
func TestClickOutsideButtonRow(t *testing.T) {
	s := New(tW, tH)
	if got := s.HitTest(WorkspaceW+10, 0); got != -1 {
		t.Fatalf("y=0 click HitTest = %d, want -1 (above button row)", got)
	}
	if got := s.HitTest(WorkspaceW+10, tH-1); got != -1 {
		t.Fatalf("y=H-1 click HitTest = %d, want -1 (below button row)", got)
	}
}

// A click inside the iconbar but in the inter-button gap misses.
func TestClickInGapMisses(t *testing.T) {
	s := New(tW, tH)
	// Place the click between button 0 and button 1.
	bx0, _, bw0, _ := s.IconbarButtonRect(0)
	gapX := bx0 + bw0 // first column of the gap (gap is IconbarButtonGap=2 wide)
	if got := s.HitTest(gapX, IconbarVPad+IconbarButtonH/2); got != -1 {
		t.Fatalf("gap-click HitTest = %d, want -1", got)
	}
}

// Render fills the whole surface (no transparent pixels) and paints the
// workspace + iconbar + clock in their expected sections.
func TestRenderFillsAllPixelsOpaque(t *testing.T) {
	s := New(tW, tH)
	s.SetClock("12:34")
	buf := newBuf(s)
	Render(s, buf)
	for i := 3; i < len(buf); i += 4 {
		if buf[i] != 0xFF {
			t.Fatalf("non-opaque pixel at byte %d: alpha=%d", i, buf[i])
		}
	}
}

func TestRenderPanicsOnSizeMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on size mismatch")
		}
	}()
	s := New(16, BarHeight)
	Render(s, make([]byte, 4))
}

// The workspace section should show ink different from its background at
// the painted-glyph rows.
func TestRenderWorkspaceLabelInked(t *testing.T) {
	s := New(tW, tH)
	buf := newBuf(s)
	Render(s, buf)
	// Sweep the whole workspace section looking for near-black ink against
	// the mid-gray bevel face.
	found := false
	for y := 0; y < tH && !found; y++ {
		for x := 0; x < WorkspaceW && !found; x++ {
			off := (y*tW + x) * 4
			if buf[off] < 0x40 && buf[off+1] < 0x40 && buf[off+2] < 0x40 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("workspace label glyph never inked")
	}
}

// With an explicit clock string the clock section paints near-black ink
// somewhere inside it.
func TestRenderClockInked(t *testing.T) {
	s := New(tW, tH)
	s.SetClock("09:42")
	buf := newBuf(s)
	Render(s, buf)
	cx, _, cw, _ := s.ClockRect()
	found := false
	for y := 0; y < tH && !found; y++ {
		for x := cx; x < cx+cw && !found; x++ {
			off := (y*tW + x) * 4
			if buf[off] < 0x40 && buf[off+1] < 0x40 && buf[off+2] < 0x40 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("clock glyph never inked")
	}
}

// An empty clock falls back to the placeholder "--:--" so the section is
// always visually present.
func TestRenderClockFallback(t *testing.T) {
	s := New(tW, tH)
	s.Clock = ""
	buf := newBuf(s)
	Render(s, buf)
	// "--:--" has 5 chars * 6 px = 30 px; the section is ClockW=80 wide so
	// it should appear. Sweep every row of the clock section looking for
	// inked pixels (the "-" glyph sits at the middle row).
	cx, _, cw, _ := s.ClockRect()
	inked := 0
	for y := 0; y < tH; y++ {
		for x := cx; x < cx+cw; x++ {
			off := (y*tW + x) * 4
			if buf[off] < 0x40 && buf[off+1] < 0x40 && buf[off+2] < 0x40 {
				inked++
			}
		}
	}
	if inked == 0 {
		t.Fatalf("fallback clock '--:--' never inked")
	}
}

// The top border row must be the theme.Border.Color across the full width.
func TestRenderTopBorderColor(t *testing.T) {
	s := New(tW, tH)
	buf := newBuf(s)
	Render(s, buf)
	bc := s.Theme.Border.Color
	for x := 0; x < tW; x++ {
		off := (0*tW + x) * 4
		if buf[off] != bc[0] || buf[off+1] != bc[1] || buf[off+2] != bc[2] {
			t.Fatalf("top border at x=%d = %v, want %v", x, buf[off:off+3], bc)
		}
	}
}

// Disabling the border (Width = 0) skips the top stroke.
func TestRenderTopBorderSkippedWhenWidthZero(t *testing.T) {
	s := New(tW, tH)
	s.Theme.Border.Width = 0
	buf := newBuf(s)
	Render(s, buf)
	// The top row should now be the workspace gradient at x in [0..WorkspaceW),
	// not the border colour.
	off := 0
	bc := s.Theme.Border.Color
	if buf[off] == bc[0] && buf[off+1] == bc[1] && buf[off+2] == bc[2] {
		t.Fatalf("top border still painted when Width=0")
	}
}

// SetCursor / SetWorkspace / SetClock store their arguments.
func TestSetters(t *testing.T) {
	s := New(tW, tH)
	s.SetCursor(11, 22, true)
	if s.CursorX != 11 || s.CursorY != 22 || !s.CursorInside {
		t.Fatalf("SetCursor not stored: %+v", s)
	}
	s.SetWorkspace("3")
	if s.Workspace != "3" {
		t.Fatalf("SetWorkspace not stored: %q", s.Workspace)
	}
	s.SetClock("23:59")
	if s.Clock != "23:59" {
		t.Fatalf("SetClock not stored: %q", s.Clock)
	}
}

// Each glyph + the default branch (unknown glyph) must paint at least one
// pixel of ink inside its tile.
func TestEachGlyphPaints(t *testing.T) {
	glyphs := []Glyph{GlyphTerminal, GlyphEditor, GlyphFiles, GlyphHello, Glyph(99)}
	for _, g := range glyphs {
		s := New(tW, tH)
		buf := newBuf(s)
		// Fill buf with a known opaque non-ink colour so we can detect ink.
		for i := 0; i+3 < len(buf); i += 4 {
			buf[i], buf[i+1], buf[i+2], buf[i+3] = 0xC8, 0xC8, 0xC8, 0xFF
		}
		drawGlyph(s, buf, g, 10, 10, IconGlyphPx, IconGlyphPx)
		painted := 0
		for y := 10; y < 10+IconGlyphPx; y++ {
			for x := 10; x < 10+IconGlyphPx; x++ {
				off := (y*tW + x) * 4
				if buf[off] < 0x40 {
					painted++
				}
			}
		}
		if painted == 0 {
			t.Fatalf("glyph %v left no ink pixels", g)
		}
	}
}

// drawGlyphHello with a wider-than-tall box exercises the h/2 < r clamp.
func TestDrawGlyphHelloWideBox(t *testing.T) {
	s := New(tW, tH)
	buf := newBuf(s)
	drawGlyph(s, buf, GlyphHello, 0, 0, 20, 8)
	// Just confirm something got painted.
	painted := 0
	for i := range buf {
		if buf[i] != 0 {
			painted++
		}
	}
	if painted == 0 {
		t.Fatalf("hello glyph in wide box painted nothing")
	}
}

// drawGlyph with a non-positive size is a no-op.
func TestDrawGlyphDegenerate(t *testing.T) {
	s := New(40, BarHeight)
	buf := newBuf(s)
	drawGlyph(s, buf, GlyphTerminal, 0, 0, 0, 10)
	drawGlyph(s, buf, GlyphTerminal, 0, 0, 10, 0)
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("degenerate drawGlyph painted something: %d", b)
		}
	}
}

// drawBevel with a non-positive size is a no-op.
func TestDrawBevelDegenerate(t *testing.T) {
	s := New(40, BarHeight)
	buf := newBuf(s)
	drawBevel(s, buf, 0, 0, 0, 10)
	drawBevel(s, buf, 0, 0, 10, 0)
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("degenerate drawBevel painted something: %d", b)
		}
	}
}

// drawTextClipped with a non-positive maxWidth is a no-op; an unknown char
// is silently skipped.
func TestDrawTextClippedEdgeCases(t *testing.T) {
	s := New(40, BarHeight)
	buf := newBuf(s)
	drawTextClipped(s, buf, "abc", 0, 0, theme.Color{0xFF, 0, 0}, 0)
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("clipped paint at maxWidth=0 painted something")
		}
	}
	// Unknown character "@" + known "1" — only the "1" should paint.
	drawText(s, buf, "@1", 0, 0, theme.Color{0xFF, 0, 0})
	red := 0
	for i := 0; i+3 < len(buf); i += 4 {
		if buf[i] == 0xFF && buf[i+1] == 0 && buf[i+2] == 0 {
			red++
		}
	}
	if red == 0 {
		t.Fatalf("known char never painted alongside unknown")
	}
}

// drawTextClipped stops once the next glyph would push past maxWidth.
func TestDrawTextClippedTruncates(t *testing.T) {
	s := New(40, BarHeight)
	buf := newBuf(s)
	// Three glyphs would need 3*6 = 18 px; cap to 12 -> 2 glyphs.
	drawText(s, buf, "111", 0, 0, theme.Color{0xFF, 0, 0})
	red := 0
	for i := 0; i+3 < len(buf); i += 4 {
		if buf[i] == 0xFF {
			red++
		}
	}
	full := red
	for i := range buf {
		buf[i] = 0
	}
	drawTextClipped(s, buf, "111", 0, 0, theme.Color{0xFF, 0, 0}, 12)
	red = 0
	for i := 0; i+3 < len(buf); i += 4 {
		if buf[i] == 0xFF {
			red++
		}
	}
	if red == 0 || red >= full {
		t.Fatalf("clip did not truncate: full=%d clipped=%d", full, red)
	}
}

// setPixel must ignore out-of-bounds coordinates.
func TestSetPixelOutOfBounds(t *testing.T) {
	s := New(4, BarHeight)
	buf := newBuf(s)
	setPixel(s, buf, -1, 0, [3]uint8{1, 1, 1})
	setPixel(s, buf, 0, -1, [3]uint8{1, 1, 1})
	setPixel(s, buf, 4, 0, [3]uint8{1, 1, 1})
	setPixel(s, buf, 0, BarHeight, [3]uint8{1, 1, 1})
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("OOB write leaked")
		}
	}
}

// abs covers the negative-input branch.
func TestAbs(t *testing.T) {
	if abs(-3) != 3 {
		t.Fatal("abs(-3) wrong")
	}
	if abs(7) != 7 {
		t.Fatal("abs(7) wrong")
	}
	if abs(0) != 0 {
		t.Fatal("abs(0) wrong")
	}
}

// drawIconbarButton clips its right edge when its w would exceed the
// section. Exercised by rendering on a narrow surface.
func TestRenderNarrowIconbarClipsButtons(t *testing.T) {
	// 220 px = workspace(100) + 40 of iconbar + clock(80). One button only
	// partially fits.
	s := New(220, BarHeight)
	buf := newBuf(s)
	Render(s, buf) // must not panic
	if buf[0] == 0 && buf[3] == 0 {
		t.Fatalf("narrow render did not paint top-left")
	}
}

// drawIconbarButton stops painting once the button's anchor falls past the
// iconbar's right edge. Reproduced by stuffing in extra apps so some land
// past the end of the iconbar.
func TestRenderStopsExtraIconbarButtons(t *testing.T) {
	s := New(400, BarHeight)
	// iconbar width = 400 - 100 - 80 = 220 -> at most 1 full button + part of
	// a second. Add more apps than fit.
	s.Apps = []App{
		{Id: "a", Glyph: GlyphTerminal, Label: "A"},
		{Id: "b", Glyph: GlyphEditor, Label: "B"},
		{Id: "c", Glyph: GlyphFiles, Label: "C"},
	}
	buf := newBuf(s)
	Render(s, buf) // must not panic and the loop must `break`
}

// When the iconbar shrinks to width 0 the inner button loop must not paint.
func TestRenderZeroWidthIconbar(t *testing.T) {
	s := New(WorkspaceW+ClockW, BarHeight) // iconbar collapses to 0
	buf := newBuf(s)
	Render(s, buf) // must not panic
}

// SetWindows stores the snapshot verbatim so the next render picks it up.
func TestSetWindowsStores(t *testing.T) {
	s := New(tW, tH)
	if len(s.Windows) != 0 {
		t.Fatalf("fresh state should have 0 windows, got %d", len(s.Windows))
	}
	s.SetWindows([]Window{
		{Id: 7, Title: "xterm", Focused: true},
		{Id: 12, Title: "editor", Minimized: true},
	})
	if got, want := len(s.Windows), 2; got != want {
		t.Fatalf("SetWindows length = %d, want %d", got, want)
	}
	if s.Windows[0].Id != 7 || s.Windows[0].Title != "xterm" || !s.Windows[0].Focused {
		t.Fatalf("Windows[0] = %+v, want {7 xterm focused}", s.Windows[0])
	}
	if !s.Windows[1].Minimized {
		t.Fatalf("Windows[1].Minimized = false, want true")
	}
	s.SetWindows(nil)
	if len(s.Windows) != 0 {
		t.Fatalf("SetWindows(nil) should clear, got %d", len(s.Windows))
	}
}

// WindowButtonRect places the i-th window button past the launcher row + the
// SeparatorW gap.
func TestWindowButtonRectFollowsLaunchersPastSeparator(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "a"}, {Id: 2, Title: "b"}})
	ix, _, _, _ := s.IconbarRect()
	wantBaseX := ix + len(s.Apps)*(IconbarButtonW+IconbarButtonGap) - IconbarButtonGap + SeparatorW
	for i := range s.Windows {
		wx, wy, ww, wh := s.WindowButtonRect(i)
		expX := wantBaseX + i*(IconbarButtonW+IconbarButtonGap)
		if wx != expX {
			t.Fatalf("window[%d].x = %d, want %d (past SeparatorW gap)", i, wx, expX)
		}
		if wy != IconbarVPad {
			t.Fatalf("window[%d].y = %d, want %d", i, wy, IconbarVPad)
		}
		if ww != IconbarButtonW || wh != tH-2*IconbarVPad {
			t.Fatalf("window[%d] size = %dx%d, want %dx%d", i, ww, wh, IconbarButtonW, tH-2*IconbarVPad)
		}
	}
}

// WindowButtonRect with zero launchers anchors at the iconbar's left edge
// (the empty-Apps fallback).
func TestWindowButtonRectWithNoLaunchers(t *testing.T) {
	s := New(tW, tH)
	s.Apps = nil
	s.SetWindows([]Window{{Id: 1, Title: "solo"}})
	wx, _, _, _ := s.WindowButtonRect(0)
	ix, _, _, _ := s.IconbarRect()
	if wx != ix {
		t.Fatalf("zero-launcher window[0].x = %d, want %d (iconbar left)", wx, ix)
	}
}

// WindowButtonRect with degenerate surface clamps height to 1.
func TestWindowButtonRectClampsHeight(t *testing.T) {
	s := New(tW, 1)
	s.SetWindows([]Window{{Id: 1, Title: "a"}})
	_, _, _, wh := s.WindowButtonRect(0)
	if wh != 1 {
		t.Fatalf("window button.h on 1-px surface = %d, want 1", wh)
	}
}

// HitTestWindow returns the window index for clicks inside a window button
// and -1 for clicks outside (workspace, clock, launcher row, above/below row).
func TestHitTestWindow(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 10, Title: "win10"}, {Id: 20, Title: "win20", Focused: true}})
	for i := range s.Windows {
		bx, by, bw, bh := s.WindowButtonRect(i)
		px := bx + bw/2
		py := by + bh/2
		if got := s.HitTestWindow(px, py); got != i {
			t.Fatalf("HitTestWindow center of window %d = %d, want %d", i, got, i)
		}
		// HitTest (launchers) must NOT match a window click.
		if got := s.HitTest(px, py); got != -1 {
			t.Fatalf("HitTest center of window %d = %d, want -1 (launcher hit-test)", i, got)
		}
	}
	// A click on the workspace label / clock is inert for windows too.
	if got := s.HitTestWindow(WorkspaceW/2, tH/2); got != -1 {
		t.Fatalf("workspace HitTestWindow = %d, want -1", got)
	}
	if got := s.HitTestWindow(tW-ClockW/2, tH/2); got != -1 {
		t.Fatalf("clock HitTestWindow = %d, want -1", got)
	}
	// A click on a launcher button is NOT a window hit.
	bx, by, bw, bh := s.IconbarButtonRect(0)
	if got := s.HitTestWindow(bx+bw/2, by+bh/2); got != -1 {
		t.Fatalf("launcher click HitTestWindow = %d, want -1", got)
	}
}

// HitTestWindow returns -1 when a window's anchor falls past the iconbar's
// right edge (very narrow surface fallback). Covers both the outer "click
// outside iconbar" early return and the inner per-window "this window's
// anchor is past the iconbar" early return.
func TestHitTestWindowOverflow(t *testing.T) {
	// 400-px surface: iconbar width = 400 - 100 - 80 = 220 -> fits 1 button +
	// part of a second. Add 4 launchers (default) + a window -> the window's
	// anchor is past the iconbar's right edge.
	s := New(400, BarHeight)
	s.SetWindows([]Window{{Id: 99, Title: "off-screen"}})
	bx, _, _, _ := s.WindowButtonRect(0)
	ix, _, iw, _ := s.IconbarRect()
	if bx < ix+iw {
		t.Fatalf("test setup wrong: expected window button anchor past iconbar end (bx=%d, ix+iw=%d)", bx, ix+iw)
	}
	// Outer check: click past iconbar right edge — returns -1 up front.
	if got := s.HitTestWindow(bx+1, IconbarVPad+1); got != -1 {
		t.Fatalf("HitTestWindow past iconbar end = %d, want -1", got)
	}
	// Inner check: click INSIDE iconbar but the window's anchor is still
	// past the iconbar — the loop's `bx >= ix+iw` early return fires.
	if got := s.HitTestWindow(ix+iw-1, IconbarVPad+1); got != -1 {
		t.Fatalf("HitTestWindow inside iconbar but window-anchor past = %d, want -1", got)
	}
}

// A window button paints ink for its title in the slot just past the
// launcher row + the SeparatorW gap.
func TestRenderWindowInked(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 7, Title: "alpha"}})
	buf := newBuf(s)
	Render(s, buf)
	bx, by, bw, bh := s.WindowButtonRect(0)
	found := false
	for y := by; y < by+bh && !found; y++ {
		for x := bx; x < bx+bw && !found; x++ {
			off := (y*tW + x) * 4
			if buf[off] < 0x40 && buf[off+1] < 0x40 && buf[off+2] < 0x40 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("window button never inked any pixels")
	}
}

// Render does not panic when a window's anchor falls past the iconbar's right
// edge (matches the launcher break-on-overflow path).
func TestRenderWindowOverflow(t *testing.T) {
	s := New(400, BarHeight) // narrow iconbar; default 4 apps + windows won't all fit
	s.SetWindows([]Window{{Id: 1, Title: "off"}, {Id: 2, Title: "off2"}})
	buf := newBuf(s)
	Render(s, buf) // must not panic, must break out of the window loop
}

// Render clips the right edge of a window button whose right side extends
// past the iconbar's right edge (the `bx+cw > ix+iw` branch). Reproduced by
// dropping the launcher row so a window button anchored at the iconbar's
// left can extend to the right while the iconbar is too narrow to fit it.
func TestRenderWindowClipsRightEdge(t *testing.T) {
	// Narrow iconbar (iconbar width = 230 - 100 - 80 = 50) + no launchers so a
	// single window button anchors INSIDE the iconbar (bx < ix+iw) but its
	// right edge sticks out (bx + IconbarButtonW=120 > ix+iw=50).
	s := New(230, BarHeight)
	s.Apps = nil
	s.SetWindows([]Window{{Id: 1, Title: "clipme"}})
	bx, _, _, _ := s.WindowButtonRect(0)
	ix, _, iw, _ := s.IconbarRect()
	if !(bx < ix+iw && bx+IconbarButtonW > ix+iw) {
		t.Fatalf("test setup wrong: need bx<ix+iw<bx+W (bx=%d, ix+iw=%d, W=%d)", bx, ix+iw, IconbarButtonW)
	}
	buf := newBuf(s)
	Render(s, buf) // must not panic, must clip the button
}

// A focused window button must paint with a sunken bevel: the top-left
// corner pixel of the button rect carries the SUNKEN highlight (dark) while
// an unfocused button carries the RAISED highlight (bright). Probing pixel
// (bx+1, by+1) sidesteps the corner exactly and lands on the bevel stroke.
func TestRenderFocusedSunkenBevel(t *testing.T) {
	s := New(tW, tH)
	// Two windows: idx 0 focused, idx 1 unfocused.
	s.SetWindows([]Window{
		{Id: 1, Title: "f", Focused: true},
		{Id: 2, Title: "u"},
	})
	buf := newBuf(s)
	Render(s, buf)
	// The very top row of a button carries the bevel stroke (white when raised,
	// dark when sunken). Sample x=bx+bw/2 (mid-button, where the stroke is not
	// at a corner) y=by.
	bx0, by0, bw0, _ := s.WindowButtonRect(0) // focused
	bx1, by1, bw1, _ := s.WindowButtonRect(1) // unfocused
	off0 := (by0*tW + bx0 + bw0/2) * 4
	off1 := (by1*tW + bx1 + bw1/2) * 4
	// Focused: top stroke is the sunken dark stroke.
	if !(buf[off0] < 0x80 && buf[off0+1] < 0x80 && buf[off0+2] < 0x80) {
		t.Fatalf("focused window top-stroke not dark: rgb=(%d,%d,%d)", buf[off0], buf[off0+1], buf[off0+2])
	}
	// Unfocused: top stroke is the raised bright stroke.
	if !(buf[off1] > 0xC0 && buf[off1+1] > 0xC0 && buf[off1+2] > 0xC0) {
		t.Fatalf("unfocused window top-stroke not bright: rgb=(%d,%d,%d)", buf[off1], buf[off1+1], buf[off1+2])
	}
}

// A minimized window button must paint with the "[*] " accent prefix on its
// label and use the dim inactive-label ink.
func TestRenderMinimizedStylesDim(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "alpha", Minimized: true}})
	buf := newBuf(s)
	Render(s, buf)
	// Just confirm the button paints SOME ink (the "[*] alpha" label).
	bx, by, bw, bh := s.WindowButtonRect(0)
	found := false
	for y := by; y < by+bh && !found; y++ {
		for x := bx; x < bx+bw && !found; x++ {
			off := (y*tW + x) * 4
			if buf[off] < 0x40 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("minimized window button never inked any pixels")
	}
	// And the bevel must be raised (the minimized window is NOT focused, so
	// the top stroke is bright).
	off := (by*tW + bx + bw/2) * 4
	if !(buf[off] > 0xC0 && buf[off+1] > 0xC0 && buf[off+2] > 0xC0) {
		t.Fatalf("minimized window top-stroke not bright: rgb=(%d,%d,%d)", buf[off], buf[off+1], buf[off+2])
	}
}

// drawSunkenBevel with a non-positive size is a no-op.
func TestDrawSunkenBevelDegenerate(t *testing.T) {
	s := New(40, BarHeight)
	buf := newBuf(s)
	drawSunkenBevel(s, buf, 0, 0, 0, 10)
	drawSunkenBevel(s, buf, 0, 0, 10, 0)
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("degenerate drawSunkenBevel painted something: %d", b)
		}
	}
}

// The separator line is painted in the SeparatorW gap when launchers exist
// (and skipped when they do not).
func TestRenderSeparatorPainted(t *testing.T) {
	s := New(tW, tH)
	buf := newBuf(s)
	Render(s, buf)
	ix, _, _, _ := s.IconbarRect()
	sepRight := ix + len(s.Apps)*(IconbarButtonW+IconbarButtonGap) - IconbarButtonGap + SeparatorW
	sepX := sepRight - SeparatorW/2 - 1
	// Probe at mid-height; expect dark ink.
	off := ((tH/2)*tW + sepX) * 4
	if !(buf[off] < 0x60 && buf[off+1] < 0x60 && buf[off+2] < 0x60) {
		t.Fatalf("separator not painted dark at sepX=%d: rgb=(%d,%d,%d)", sepX, buf[off], buf[off+1], buf[off+2])
	}
}

// The separator is skipped when the Apps slice is empty.
func TestRenderSeparatorSkippedWhenEmptyApps(t *testing.T) {
	s := New(tW, tH)
	s.Apps = nil
	buf := newBuf(s)
	Render(s, buf) // must not panic
}

// ---- workspaces -----------------------------------------------------------

// New defaults: ActiveWorkspace=1, WorkspaceCount=4, label="1 of 4".
func TestNewWorkspaceDefaults(t *testing.T) {
	s := New(tW, tH)
	if s.ActiveWorkspace != 1 {
		t.Fatalf("default ActiveWorkspace = %d, want 1", s.ActiveWorkspace)
	}
	if s.WorkspaceCount != 4 {
		t.Fatalf("default WorkspaceCount = %d, want 4", s.WorkspaceCount)
	}
	if s.Workspace != "1 of 4" {
		t.Fatalf("default Workspace label = %q, want %q", s.Workspace, "1 of 4")
	}
}

// SetActiveWorkspace clamps below + above the legal range, refreshes label.
func TestSetActiveWorkspaceClampsAndUpdatesLabel(t *testing.T) {
	s := New(tW, tH)
	s.SetActiveWorkspace(3)
	if s.ActiveWorkspace != 3 || s.Workspace != "3 of 4" {
		t.Fatalf("SetActiveWorkspace(3) = (%d,%q), want (3,%q)", s.ActiveWorkspace, s.Workspace, "3 of 4")
	}
	s.SetActiveWorkspace(0) // below range -> clamp to 1
	if s.ActiveWorkspace != 1 {
		t.Fatalf("SetActiveWorkspace(0) ActiveWorkspace = %d, want 1", s.ActiveWorkspace)
	}
	s.SetActiveWorkspace(99) // above range -> clamp to WorkspaceCount
	if s.ActiveWorkspace != s.WorkspaceCount {
		t.Fatalf("SetActiveWorkspace(99) ActiveWorkspace = %d, want %d", s.ActiveWorkspace, s.WorkspaceCount)
	}
}

// SetWorkspaceCount keeps ActiveWorkspace coherent and re-renders the label.
func TestSetWorkspaceCountRecomputesLabel(t *testing.T) {
	s := New(tW, tH)
	s.SetActiveWorkspace(4)
	s.SetWorkspaceCount(2) // active was 4 -> clamp down to 2
	if s.ActiveWorkspace != 2 || s.Workspace != "2 of 2" {
		t.Fatalf("after SetWorkspaceCount(2): (%d,%q), want (2,%q)", s.ActiveWorkspace, s.Workspace, "2 of 2")
	}
	// Non-positive count -> label reduces to bare digit
	s.SetWorkspaceCount(0)
	if s.Workspace != "2" {
		t.Fatalf("WorkspaceCount=0 label = %q, want %q", s.Workspace, "2")
	}
	// Negative input clamps to 0.
	s.SetWorkspaceCount(-1)
	if s.WorkspaceCount != 0 {
		t.Fatalf("WorkspaceCount after -1 = %d, want 0", s.WorkspaceCount)
	}
}

// NextWorkspace + PrevWorkspace wrap at the boundaries.
func TestCycleWorkspaceWraps(t *testing.T) {
	s := New(tW, tH)
	if got := s.NextWorkspace(); got != 2 {
		t.Fatalf("NextWorkspace from 1/4 = %d, want 2", got)
	}
	s.SetActiveWorkspace(4)
	if got := s.NextWorkspace(); got != 1 {
		t.Fatalf("NextWorkspace from 4/4 = %d, want 1 (wrap)", got)
	}
	s.SetActiveWorkspace(1)
	if got := s.PrevWorkspace(); got != 4 {
		t.Fatalf("PrevWorkspace from 1/4 = %d, want 4 (wrap)", got)
	}
	s.SetActiveWorkspace(3)
	if got := s.PrevWorkspace(); got != 2 {
		t.Fatalf("PrevWorkspace from 3/4 = %d, want 2", got)
	}
}

// Non-positive count makes Next/Prev no-op (returns the current active).
func TestCycleWorkspaceNoCountIsNoop(t *testing.T) {
	s := New(tW, tH)
	s.SetWorkspaceCount(0)
	s.ActiveWorkspace = 7 // direct set; SetActiveWorkspace would not clamp w/ count=0
	if got := s.NextWorkspace(); got != 7 {
		t.Fatalf("NextWorkspace with count=0 = %d, want 7", got)
	}
	if got := s.PrevWorkspace(); got != 7 {
		t.Fatalf("PrevWorkspace with count=0 = %d, want 7", got)
	}
}

// HitTestWorkspace identifies clicks on the left section.
func TestHitTestWorkspace(t *testing.T) {
	s := New(tW, tH)
	if !s.HitTestWorkspace(WorkspaceW/2, tH/2) {
		t.Fatalf("center of workspace section not detected")
	}
	if s.HitTestWorkspace(WorkspaceW+10, tH/2) {
		t.Fatalf("iconbar click reported as workspace hit")
	}
	if s.HitTestWorkspace(tW-1, tH/2) {
		t.Fatalf("clock click reported as workspace hit")
	}
	if s.HitTestWorkspace(-5, tH/2) {
		t.Fatalf("negative-x click reported as workspace hit")
	}
}

// Render must paint the workspace label distinctly when ActiveWorkspace
// changes — the rendered ink count for "3 of 4" differs from "1 of 4".
func TestRenderWorkspaceLabelChanges(t *testing.T) {
	s := New(tW, tH)
	buf1 := newBuf(s)
	Render(s, buf1)
	s.SetActiveWorkspace(3)
	buf2 := newBuf(s)
	Render(s, buf2)
	if bytesEqual(buf1, buf2) {
		t.Fatalf("workspace label did not change between workspace 1 and 3")
	}
}

// itoa exercises the zero, negative and multi-digit branches that the other
// tests do not naturally hit.
func TestItoa(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{{0, "0"}, {1, "1"}, {9, "9"}, {10, "10"}, {1234, "1234"}, {-1, "-1"}, {-42, "-42"}}
	for _, c := range cases {
		if got := itoa(c.in); got != c.want {
			t.Fatalf("itoa(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Window.Workspace round-trips through SetWindows (the compositor sends it
// in the windows_changed payload; the dock keeps it in the model).
func TestWindowCarriesWorkspaceField(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "x", Workspace: 2}})
	if got := s.Windows[0].Workspace; got != 2 {
		t.Fatalf("Window.Workspace = %d, want 2", got)
	}
}

// TestSetThemeReplacesTheme: SetTheme swaps the active theme + the next
// Render call uses the new colours.
func TestSetTheme(t *testing.T) {
	s := New(tW, tH)
	orig := s.Theme.Border.Color
	custom := theme.Theme{}
	custom.Border.Color = theme.Color{0xAB, 0xCD, 0xEF}
	s.SetTheme(custom)
	if s.Theme.Border.Color == orig {
		t.Fatalf("SetTheme did not swap the theme: still %v", s.Theme.Border.Color)
	}
	if s.Theme.Border.Color != (theme.Color{0xAB, 0xCD, 0xEF}) {
		t.Fatalf("SetTheme stored = %v", s.Theme.Border.Color)
	}
}

// bytesEqual is a tiny []byte compare so the workspace render-change test
// does not pull in reflect.DeepEqual.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
