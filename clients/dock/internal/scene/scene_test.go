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
	if s.Workspace != "1" {
		t.Fatalf("default workspace = %q, want %q", s.Workspace, "1")
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

// SetTasks stores the snapshot verbatim so the next render picks it up.
func TestSetTasksStores(t *testing.T) {
	s := New(tW, tH)
	if len(s.Tasks) != 0 {
		t.Fatalf("fresh state should have 0 tasks, got %d", len(s.Tasks))
	}
	s.SetTasks([]Task{{Id: 7, Title: "xterm"}, {Id: 12, Title: "editor"}})
	if got, want := len(s.Tasks), 2; got != want {
		t.Fatalf("SetTasks length = %d, want %d", got, want)
	}
	if s.Tasks[0].Id != 7 || s.Tasks[0].Title != "xterm" {
		t.Fatalf("Tasks[0] = %+v, want {7 xterm}", s.Tasks[0])
	}
	s.SetTasks(nil)
	if len(s.Tasks) != 0 {
		t.Fatalf("SetTasks(nil) should clear, got %d", len(s.Tasks))
	}
}

// TaskButtonRect places the i-th task button at the slot immediately after
// the last launcher (continuous row).
func TestTaskButtonRectFollowsLaunchers(t *testing.T) {
	s := New(tW, tH)
	s.SetTasks([]Task{{Id: 1, Title: "a"}, {Id: 2, Title: "b"}})
	n := len(s.Apps)
	for i := range s.Tasks {
		tx, ty, tw, th := s.TaskButtonRect(i)
		lx, ly, lw, lh := s.IconbarButtonRect(n + i)
		if tx != lx || ty != ly || tw != lw || th != lh {
			t.Fatalf("task[%d] rect = (%d,%d,%d,%d), want launcher-slot[%d] (%d,%d,%d,%d)",
				i, tx, ty, tw, th, n+i, lx, ly, lw, lh)
		}
	}
}

// HitTestTask returns the task index for clicks inside a task button and -1
// for clicks outside (workspace, clock, launcher row, above/below row).
func TestHitTestTask(t *testing.T) {
	s := New(tW, tH)
	s.SetTasks([]Task{{Id: 10, Title: "win10"}, {Id: 20, Title: "win20"}})
	for i := range s.Tasks {
		bx, by, bw, bh := s.TaskButtonRect(i)
		px := bx + bw/2
		py := by + bh/2
		if got := s.HitTestTask(px, py); got != i {
			t.Fatalf("HitTestTask center of task %d = %d, want %d", i, got, i)
		}
		// HitTest (launchers) must NOT match a task click.
		if got := s.HitTest(px, py); got != -1 {
			t.Fatalf("HitTest center of task %d = %d, want -1 (launcher hit-test)", i, got)
		}
	}
	// A click on the workspace label / clock is inert for tasks too.
	if got := s.HitTestTask(WorkspaceW/2, tH/2); got != -1 {
		t.Fatalf("workspace HitTestTask = %d, want -1", got)
	}
	if got := s.HitTestTask(tW-ClockW/2, tH/2); got != -1 {
		t.Fatalf("clock HitTestTask = %d, want -1", got)
	}
	// A click on a launcher button is NOT a task hit.
	bx, by, bw, bh := s.IconbarButtonRect(0)
	if got := s.HitTestTask(bx+bw/2, by+bh/2); got != -1 {
		t.Fatalf("launcher click HitTestTask = %d, want -1", got)
	}
}

// HitTestTask returns -1 when a task's anchor falls past the iconbar's
// right edge (very narrow surface fallback).
func TestHitTestTaskOverflow(t *testing.T) {
	// 400-px surface: iconbar width = 400 - 100 - 80 = 220 -> fits 1 button +
	// part of a second. Add 4 launchers (default) + a task -> the task's
	// anchor is past the iconbar's right edge.
	s := New(400, BarHeight)
	s.SetTasks([]Task{{Id: 99, Title: "off-screen"}})
	bx, _, _, _ := s.TaskButtonRect(0)
	ix, _, iw, _ := s.IconbarRect()
	if bx < ix+iw {
		t.Fatalf("test setup wrong: expected task button anchor past iconbar end (bx=%d, ix+iw=%d)", bx, ix+iw)
	}
	if got := s.HitTestTask(bx+1, IconbarVPad+1); got != -1 {
		t.Fatalf("HitTestTask past iconbar end = %d, want -1", got)
	}
}

// A task button paints ink for its title in the slot just past the
// launchers. The "[*]" accent prefix is part of the painted label.
func TestRenderTaskInked(t *testing.T) {
	s := New(tW, tH)
	s.SetTasks([]Task{{Id: 7, Title: "alpha"}})
	buf := newBuf(s)
	Render(s, buf)
	bx, by, bw, bh := s.TaskButtonRect(0)
	found := false
	for y := by; y < by+bh && !found; y++ {
		for x := bx; x < bx+bw && !found; x++ {
			off := (y*tW + x) * 4
			// Ink for the task label is dark (active label colour); the bevel
			// face is mid-gray. A pixel under 0x40 on R+G+B is an ink hit.
			if buf[off] < 0x40 && buf[off+1] < 0x40 && buf[off+2] < 0x40 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("task button never inked any pixels")
	}
}

// Render does not panic when a task's anchor falls past the iconbar's right
// edge (matches the launcher break-on-overflow path).
func TestRenderTaskOverflow(t *testing.T) {
	s := New(400, BarHeight) // narrow iconbar; default 4 apps + tasks won't all fit
	s.SetTasks([]Task{{Id: 1, Title: "off"}, {Id: 2, Title: "off2"}})
	buf := newBuf(s)
	Render(s, buf) // must not panic, must break out of the task loop
}
