// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"testing"

	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
	"github.com/wasmdesk/wasmbox/clients/dock/internal/theme"
)

// Surface size used by most tests: 1280-wide x 28-tall, matching the worker
// surface dimensions.
const (
	tW = 1280
	tH = BarHeight
)

func newBuf(s *State) []byte { return make([]byte, 4*s.W*s.H) }

// newPainter returns a zeroed RGBA buffer of w*h plus a PixelPainter over it,
// for tests that drive the low-level Fluxbox chrome helpers directly.
func newPainter(w, h int) ([]byte, *painter.PixelPainter) {
	buf := make([]byte, 4*w*h)
	return buf, painter.NewPixelPainter(buf, w, h)
}

// glyphInk is the near-black ink the dock passes its icon painters; the glyph
// tests use it directly now that drawGlyph takes the ink from its host.
var glyphInk = toolkit.RGB(0x1a, 0x1a, 0x1a)

// distinctLuma counts how many distinct R+G+B sums appear inside region r of an
// RGBA buffer. The 5x7 bitmap font paints each text pixel either full ink or
// untouched ground, so a bitmap render of a text region holds only the ground's
// handful of levels plus one ink level; the AA/shaped face scan-converts glyph
// outlines to partial-coverage masks, adding a whole ramp of intermediate levels
// the bitmap can never produce. A strictly higher distinct-luma count over the
// SAME region is therefore a ground-independent proof the AA face is active.
func distinctLuma(buf []byte, W int, r toolkit.Rect) int {
	seen := map[int]struct{}{}
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			o := (y*W + x) * 4
			seen[int(buf[o])+int(buf[o+1])+int(buf[o+2])] = struct{}{}
		}
	}
	return len(seen)
}

// TestAATextIsAntiAliased proves the dock now renders labels with the toolkit's
// AA/shaped OpenType face rather than the 5x7 bitmap. It renders the workspace
// section ("1 of 4") with the AA face (New enabled it) and again with the bitmap
// default, and asserts the AA render carries strictly more distinct luma levels
// in that region — the partial-coverage ramp only anti-aliasing produces. The
// region spans a bevelled gradient ground, so the assertion is deliberately
// ground-independent (see distinctLuma).
func TestAATextIsAntiAliased(t *testing.T) {
	const W, H = 400, BarHeight
	region := toolkit.Rect{X: 0, Y: 0, W: WorkspaceW, H: H}

	aa := make([]byte, 4*W*H)
	Render(New(W, H), aa) // AA face (enableAAText ran in New)

	toolkit.SetFont(nil) // bitmap default
	defer func() { _ = toolkit.UseOpenTypeText() }()
	bm := make([]byte, 4*W*H)
	Render(New(W, H), bm)

	aaN := distinctLuma(aa, W, region)
	bmN := distinctLuma(bm, W, region)
	if aaN <= bmN {
		t.Fatalf("workspace label: distinct luma aa=%d not > bitmap=%d — AA face not active", aaN, bmN)
	}
	t.Logf("workspace distinct luma: aa=%d bm=%d", aaN, bmN)
}

// TestAAFaceFitsBar asserts the taller AA line box still fits the dock's fixed
// bands: the 20px face sits inside the iconbar button height and the fixed-width
// workspace/clock sections hold their shaped labels. If a future face/size
// overflowed a band this fails loudly instead of silently clipping.
func TestAAFaceFitsBar(t *testing.T) {
	s := New(tW, tH) // switches the global font to the AA face
	f, err := toolkit.DefaultOpenTypeFont(toolkit.DefaultOpenTypeSizePx)
	if err != nil {
		t.Fatalf("DefaultOpenTypeFont: %v", err)
	}
	h := f.Height()
	if h != 20 {
		t.Fatalf("AA face height = %d, want 20 (retune bands if this changes)", h)
	}
	// The iconbar button inner height (surface minus vertical padding) holds the
	// 20px line box.
	if btnH := s.H - 2*IconbarVPad; h > btnH {
		t.Fatalf("AA height %d overflows iconbar button height %d", h, btnH)
	}
	// The fixed-width workspace + clock sections hold their shaped labels.
	if w := toolkit.TextWidth(s.Workspace); w > WorkspaceW {
		t.Fatalf("workspace label %q width %d overflows WorkspaceW %d", s.Workspace, w, WorkspaceW)
	}
	if w := toolkit.TextWidth("00:00"); w > ClockW {
		t.Fatalf("clock label width %d overflows ClockW %d", w, ClockW)
	}
}

func TestNewHasDefaults(t *testing.T) {
	s := New(tW, tH)
	if got, want := len(s.Apps), 6; got != want {
		t.Fatalf("default apps = %d, want %d", got, want)
	}
	want := []string{"terminal", "editor", "files", "hello", "vscode", "loom"}
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

// The iconbar is composed from a toolkit.AppDock: LauncherRects reports one
// laid-out rect per launcher, and a click at a launcher's centre HitTests to
// that launcher index, whose Apps[i].Id is the documented launch string. This
// is the dock's paint == hit-test contract, exercised through a full Render.
func TestDockLauncherHitTest(t *testing.T) {
	wantIDs := []string{"terminal", "editor", "files", "hello", "vscode", "loom"}
	s := New(tW, tH)
	buf := newBuf(s)
	Render(s, buf) // renders through the AppDock
	rects := s.LauncherRects()
	if len(rects) != len(s.Apps) {
		t.Fatalf("LauncherRects len %d, want %d", len(rects), len(s.Apps))
	}
	for i, r := range rects {
		if r[2] <= 0 || r[3] <= 0 {
			t.Fatalf("launcher %d has empty rect %v", i, r)
		}
		cx := r[0] + r[2]/2
		cy := r[1] + r[3]/2
		if got := s.HitTest(cx, cy); got != i {
			t.Fatalf("HitTest centre of launcher %d = %d, want %d", i, got, i)
		}
		if got := s.Apps[i].Id; got != wantIDs[i] {
			t.Fatalf("launcher %d dispatches %q, want %q", i, got, wantIDs[i])
		}
	}
}

// Clicks on the workspace label / clock fall outside the dock bounds so
// HitTest returns -1.
func TestClicksOnWorkspaceAndClockAreInert(t *testing.T) {
	s := New(tW, tH)
	if got := s.HitTest(WorkspaceW/2, tH/2); got != -1 {
		t.Fatalf("workspace click HitTest = %d, want -1", got)
	}
	if got := s.HitTest(tW-ClockW/2, tH/2); got != -1 {
		t.Fatalf("clock click HitTest = %d, want -1", got)
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

// ---- Fluxbox chrome helpers (painter-level) ------------------------------

// Each glyph + the default branch (unknown glyph) must paint at least one
// pixel of ink inside its tile.
func TestEachGlyphPaints(t *testing.T) {
	glyphs := []Glyph{GlyphTerminal, GlyphEditor, GlyphFiles, GlyphHello, GlyphCode, GlyphLoom, Glyph(99)}
	for _, g := range glyphs {
		buf, p := newPainter(tW, tH)
		for i := 0; i+3 < len(buf); i += 4 {
			buf[i], buf[i+1], buf[i+2], buf[i+3] = 0xC8, 0xC8, 0xC8, 0xFF
		}
		drawGlyph(p, g, toolkit.Rect{X: 10, Y: 10, W: IconGlyphPx, H: IconGlyphPx}, glyphInk)
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
	buf, p := newPainter(tW, tH)
	drawGlyph(p, GlyphHello, toolkit.Rect{X: 0, Y: 0, W: 20, H: 8}, glyphInk)
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

// drawGlyphLoom in a tiny box exercises the w<6/h<6 fallback grid branch.
func TestDrawGlyphLoomTinyBox(t *testing.T) {
	buf, p := newPainter(tW, tH)
	drawGlyph(p, GlyphLoom, toolkit.Rect{X: 0, Y: 0, W: 4, H: 4}, glyphInk)
	painted := 0
	for i := range buf {
		if buf[i] != 0 {
			painted++
		}
	}
	if painted == 0 {
		t.Fatalf("loom glyph fallback painted nothing")
	}
}

// drawGlyphCode in a short box exercises the armH<2 clamp.
func TestDrawGlyphCodeTinyBox(t *testing.T) {
	buf, p := newPainter(tW, tH)
	drawGlyph(p, GlyphCode, toolkit.Rect{X: 0, Y: 0, W: 8, H: 8}, glyphInk)
	painted := 0
	for i := range buf {
		if buf[i] != 0 {
			painted++
		}
	}
	if painted == 0 {
		t.Fatalf("code glyph in short box painted nothing")
	}
}

// drawGlyph with a non-positive size is a no-op.
func TestDrawGlyphDegenerate(t *testing.T) {
	buf, p := newPainter(40, BarHeight)
	drawGlyph(p, GlyphTerminal, toolkit.Rect{X: 0, Y: 0, W: 0, H: 10}, glyphInk)
	drawGlyph(p, GlyphTerminal, toolkit.Rect{X: 0, Y: 0, W: 10, H: 0}, glyphInk)
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("degenerate drawGlyph painted something: %d", b)
		}
	}
}

// drawBevel with a non-positive size is a no-op; a normal call paints the
// bright top-left highlight and dark bottom-right.
func TestDrawBevel(t *testing.T) {
	buf, p := newPainter(40, BarHeight)
	drawBevel(p, toolkit.Rect{X: 0, Y: 0, W: 0, H: 10})
	drawBevel(p, toolkit.Rect{X: 0, Y: 0, W: 10, H: 0})
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("degenerate drawBevel painted something: %d", b)
		}
	}
	drawBevel(p, toolkit.Rect{X: 2, Y: 2, W: 8, H: 8})
	off := (2*40 + 2) * 4
	if !(buf[off] == 0xFF && buf[off+1] == 0xFF && buf[off+2] == 0xFF) {
		t.Fatalf("bevel top-left not bright: %v", buf[off:off+3])
	}
	off = ((2+8-1)*40 + (2 + 8 - 1)) * 4
	if !(buf[off] == 0x40 && buf[off+1] == 0x40 && buf[off+2] == 0x40) {
		t.Fatalf("bevel bottom-right not dark: %v", buf[off:off+3])
	}
}

// gradientAt covers every interpolation axis plus the flat/default fall-through.
func TestGradientAt(t *testing.T) {
	c1 := theme.Color{0, 0, 0}
	c2 := theme.Color{100, 100, 100}
	if got := gradientAt(theme.GradientVertical, 0, 9, 10, 10, c1, c2); got != c2 {
		t.Fatalf("vertical bottom = %v, want %v", got, c2)
	}
	if got := gradientAt(theme.GradientHorizontal, 9, 0, 10, 10, c1, c2); got != c2 {
		t.Fatalf("horizontal right = %v, want %v", got, c2)
	}
	if got := gradientAt(theme.GradientDiagonal, 9, 9, 10, 10, c1, c2); got != c2 {
		t.Fatalf("diagonal corner = %v, want %v", got, c2)
	}
	if got := gradientAt(theme.GradientCrossDiagonal, 0, 9, 10, 10, c1, c2); got != c2 {
		t.Fatalf("cross-diagonal corner = %v, want %v", got, c2)
	}
	if got := gradientAt(theme.GradientRaisedBevel, 5, 5, 10, 10, c1, c2); got != c1 {
		t.Fatalf("default gradient = %v, want %v (c1)", got, c1)
	}
}

// lerpColor covers the denom<=0 collapse, the step clamps, and the midpoint.
func TestLerpColor(t *testing.T) {
	c1 := theme.Color{0, 0, 0}
	c2 := theme.Color{200, 200, 200}
	if got := lerpColor(c1, c2, 3, 0); got != c1 {
		t.Fatalf("denom<=0 = %v, want c1", got)
	}
	if got := lerpColor(c1, c2, -5, 10); got != c1 {
		t.Fatalf("step<0 clamp = %v, want c1", got)
	}
	if got := lerpColor(c1, c2, 99, 10); got != c2 {
		t.Fatalf("step>denom clamp = %v, want c2", got)
	}
	if got := lerpColor(c1, c2, 5, 10); got != (theme.Color{100, 100, 100}) {
		t.Fatalf("midpoint = %v, want {100,100,100}", got)
	}
}

// paintBg draws a flat fill via FillRect and a per-pixel gradient; both are
// opaque and a degenerate rect paints nothing.
func TestPaintBg(t *testing.T) {
	buf, p := newPainter(20, 20)
	paintBg(p, toolkit.Rect{X: 0, Y: 0, W: 0, H: 10}, theme.Bg{Gradient: theme.GradientFlat, Color: theme.Color{9, 9, 9}})
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("degenerate paintBg painted something")
		}
	}
	buf, p = newPainter(20, 20)
	paintBg(p, toolkit.Rect{X: 0, Y: 0, W: 20, H: 20}, theme.Bg{Gradient: theme.GradientFlat, Color: theme.Color{0x11, 0x22, 0x33}})
	for i := 0; i+3 < len(buf); i += 4 {
		if buf[i] != 0x11 || buf[i+1] != 0x22 || buf[i+2] != 0x33 || buf[i+3] != 0xFF {
			t.Fatalf("flat fill wrong at byte %d: %v", i, buf[i:i+4])
		}
	}
	buf, p = newPainter(4, 10)
	paintBg(p, toolkit.Rect{X: 0, Y: 0, W: 4, H: 10}, theme.Bg{Gradient: theme.GradientVertical, Color: theme.Color{0, 0, 0}, ColorTo: theme.Color{240, 240, 240}})
	top := buf[0]
	bottom := buf[(9*4+0)*4]
	if top == bottom {
		t.Fatalf("vertical gradient did not vary: top=%d bottom=%d", top, bottom)
	}
}

// ---- narrow-surface + overflow render paths -------------------------------

// The dock clips its items to the iconbar rect on a narrow surface. Exercised
// by rendering on a narrow surface (must not panic and must paint the top-left
// border).
func TestRenderNarrowIconbarClipsButtons(t *testing.T) {
	s := New(220, BarHeight)
	buf := newBuf(s)
	Render(s, buf) // must not panic
	if buf[0] == 0 && buf[3] == 0 {
		t.Fatalf("narrow render did not paint top-left")
	}
}

// The dock stops painting once a launcher's anchor falls past the iconbar's
// right edge. Reproduced by stuffing in extra apps on a narrow surface.
func TestRenderStopsExtraIconbarButtons(t *testing.T) {
	s := New(400, BarHeight)
	s.Apps = []App{
		{Id: "a", Glyph: GlyphTerminal, Label: "A"},
		{Id: "b", Glyph: GlyphEditor, Label: "B"},
		{Id: "c", Glyph: GlyphFiles, Label: "C"},
	}
	buf := newBuf(s)
	Render(s, buf) // must not panic
}

// When the iconbar shrinks to width 0 the dock must not paint any item.
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

// An open window whose title maps to a launcher lights that launcher's running
// indicator (drawn by the dock) — the grouped-window model: no per-window
// button, the window collapses onto its launcher. Rendering with a focused,
// badged running window must not panic and the launcher must stay hit-testable.
func TestRenderRunningLauncherIndicator(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 7, Title: "Terminal", Focused: true}})
	s.SetBadge("terminal", 3)
	buf := newBuf(s)
	Render(s, buf) // covers running + active + badge on the launcher
	// The terminal launcher (index 0) is running + focused-active.
	if run := s.launcherRunning(); !run[0] {
		t.Fatalf("terminal launcher should be running")
	}
	if s.focusedLauncher() != 0 {
		t.Fatalf("focused launcher = %d, want 0", s.focusedLauncher())
	}
	r := s.LauncherRects()[0]
	if got := s.HitTest(r[0]+r[2]/2, r[1]+r[3]/2); got != 0 {
		t.Fatalf("HitTest of running launcher = %d, want 0", got)
	}
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
	s.SetWorkspaceCount(0)
	if s.Workspace != "2" {
		t.Fatalf("WorkspaceCount=0 label = %q, want %q", s.Workspace, "2")
	}
	s.SetWorkspaceCount(-1)
	if s.WorkspaceCount != 0 {
		t.Fatalf("WorkspaceCount after -1 = %d, want 0", s.WorkspaceCount)
	}
	s2 := New(tW, tH)
	s2.ActiveWorkspace = 0
	s2.SetWorkspaceCount(4)
	if s2.ActiveWorkspace != 1 {
		t.Fatalf("SetWorkspaceCount bump: ActiveWorkspace = %d, want 1", s2.ActiveWorkspace)
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
// changes — the rendered ink for "3 of 4" differs from "1 of 4".
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

// Window.Workspace round-trips through SetWindows (the compositor sends it
// in the windows_changed payload; the dock keeps it in the model).
func TestWindowCarriesWorkspaceField(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "x", Workspace: 2}})
	if got := s.Windows[0].Workspace; got != 2 {
		t.Fatalf("Window.Workspace = %d, want 2", got)
	}
}

// TestSetTheme: SetTheme swaps the active theme + the next Render call uses
// the new colours.
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

// rgba maps a theme.Color to an opaque toolkit.RGBA.
func TestRGBA(t *testing.T) {
	got := rgba(theme.Color{0x12, 0x34, 0x56})
	if got.R != 0x12 || got.G != 0x34 || got.B != 0x56 || got.A != 0xFF {
		t.Fatalf("rgba = %+v, want {0x12,0x34,0x56,0xFF}", got)
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
