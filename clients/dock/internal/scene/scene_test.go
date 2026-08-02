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

// newPainter returns a zeroed RGBA buffer of w*h plus a PixelPainter over it,
// for tests that drive the low-level Fluxbox chrome helpers directly.
func newPainter(w, h int) ([]byte, *painter.PixelPainter) {
	buf := make([]byte, 4*w*h)
	return buf, painter.NewPixelPainter(buf, w, h)
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
	cases := []string{"terminal", "editor", "files", "hello", "vscode", "loom"}
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
	// The top row should now be the workspace bevel highlight at x=0, not the
	// border colour.
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
		// Fill buf with a known opaque non-ink colour so we can detect ink.
		for i := 0; i+3 < len(buf); i += 4 {
			buf[i], buf[i+1], buf[i+2], buf[i+3] = 0xC8, 0xC8, 0xC8, 0xFF
		}
		drawGlyph(p, g, toolkit.Rect{X: 10, Y: 10, W: IconGlyphPx, H: IconGlyphPx})
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
	drawGlyph(p, GlyphHello, toolkit.Rect{X: 0, Y: 0, W: 20, H: 8})
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
	drawGlyph(p, GlyphLoom, toolkit.Rect{X: 0, Y: 0, W: 4, H: 4})
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
	drawGlyph(p, GlyphCode, toolkit.Rect{X: 0, Y: 0, W: 8, H: 8})
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
	drawGlyph(p, GlyphTerminal, toolkit.Rect{X: 0, Y: 0, W: 0, H: 10})
	drawGlyph(p, GlyphTerminal, toolkit.Rect{X: 0, Y: 0, W: 10, H: 0})
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
	// Top-left corner is the bright highlight.
	off := (2*40 + 2) * 4
	if !(buf[off] == 0xFF && buf[off+1] == 0xFF && buf[off+2] == 0xFF) {
		t.Fatalf("bevel top-left not bright: %v", buf[off:off+3])
	}
	// Bottom-right corner is the dark lowlight.
	off = ((2+8-1)*40 + (2 + 8 - 1)) * 4
	if !(buf[off] == 0x40 && buf[off+1] == 0x40 && buf[off+2] == 0x40) {
		t.Fatalf("bevel bottom-right not dark: %v", buf[off:off+3])
	}
}

// drawSunkenBevel is the inverse of drawBevel (dark top-left, bright
// bottom-right) and a no-op on a degenerate rect.
func TestDrawSunkenBevel(t *testing.T) {
	buf, p := newPainter(40, BarHeight)
	drawSunkenBevel(p, toolkit.Rect{X: 0, Y: 0, W: 0, H: 10})
	drawSunkenBevel(p, toolkit.Rect{X: 0, Y: 0, W: 10, H: 0})
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("degenerate drawSunkenBevel painted something: %d", b)
		}
	}
	drawSunkenBevel(p, toolkit.Rect{X: 2, Y: 2, W: 8, H: 8})
	off := (2*40 + 2) * 4
	if !(buf[off] == 0x40 && buf[off+1] == 0x40 && buf[off+2] == 0x40) {
		t.Fatalf("sunken bevel top-left not dark: %v", buf[off:off+3])
	}
	off = ((2+8-1)*40 + (2 + 8 - 1)) * 4
	if !(buf[off] == 0xFF && buf[off+1] == 0xFF && buf[off+2] == 0xFF) {
		t.Fatalf("sunken bevel bottom-right not bright: %v", buf[off:off+3])
	}
}

// countRedInk counts red-ink pixels (R>0, G==B==0) in a black-ground buffer —
// the whole ink coverage of drawClippedText's red text, anti-aliased edges
// included (red over black stays a pure red hue at every coverage).
func countRedInk(buf []byte) int {
	n := 0
	for i := 0; i+3 < len(buf); i += 4 {
		if buf[i] > 0 && buf[i+1] == 0 && buf[i+2] == 0 {
			n++
		}
	}
	return n
}

// drawClippedText with a non-positive maxWidth, empty text, or a maxWidth
// narrower than the first glyph all paint nothing; a generous maxWidth paints
// the text. The clip is measured with the active (AA/shaped) face, so these
// assert the font-independent TextWidth path, not the old fixed-6px count.
func TestDrawClippedTextEdgeCases(t *testing.T) {
	enableAAText() // exercise against the active AA face (idempotent)
	red := toolkit.RGB(0xFF, 0, 0)

	// maxWidth <= 0: nothing.
	buf, p := newPainter(60, BarHeight)
	drawClippedText(p, "abc", 0, 0, red, 0)
	if countRedInk(buf) != 0 {
		t.Fatalf("clipped paint at maxWidth=0 painted something")
	}
	// Empty text: nothing.
	buf, p = newPainter(60, BarHeight)
	drawClippedText(p, "", 0, 0, red, 1<<20)
	if countRedInk(buf) != 0 {
		t.Fatalf("empty text painted something")
	}
	// maxWidth of 1px is narrower than any glyph: nothing fits.
	buf, p = newPainter(60, BarHeight)
	drawClippedText(p, "abc", 0, 0, red, 1)
	if countRedInk(buf) != 0 {
		t.Fatalf("sub-glyph maxWidth painted something")
	}
	// Generous width: the whole string paints.
	buf, p = newPainter(60, BarHeight)
	drawClippedText(p, "A1", 0, 0, red, 1<<20)
	if countRedInk(buf) == 0 {
		t.Fatalf("text never painted at a generous width")
	}
}

// drawClippedText stops once the next glyph would push past maxWidth, measured
// exactly with the active proportional face via toolkit.TextWidth.
func TestDrawClippedTextTruncates(t *testing.T) {
	enableAAText()
	red := toolkit.RGB(0xFF, 0, 0)

	buf, p := newPainter(80, BarHeight)
	drawClippedText(p, "111", 0, 0, red, 1<<20) // full
	full := countRedInk(buf)

	// A width that fits exactly the first two glyphs — the third overflows.
	w2 := toolkit.TextWidth("11")
	buf, p = newPainter(80, BarHeight)
	drawClippedText(p, "111", 0, 0, red, w2)
	clipped := countRedInk(buf)

	if clipped == 0 || clipped >= full {
		t.Fatalf("clip did not truncate: full=%d clipped=%d (w2=%d)", full, clipped, w2)
	}
}

// gradientAt covers every interpolation axis plus the flat/default fall-through.
func TestGradientAt(t *testing.T) {
	c1 := theme.Color{0, 0, 0}
	c2 := theme.Color{100, 100, 100}
	// Vertical: bottom row (j=rh-1) reaches c2.
	if got := gradientAt(theme.GradientVertical, 0, 9, 10, 10, c1, c2); got != c2 {
		t.Fatalf("vertical bottom = %v, want %v", got, c2)
	}
	// Horizontal: right column reaches c2.
	if got := gradientAt(theme.GradientHorizontal, 9, 0, 10, 10, c1, c2); got != c2 {
		t.Fatalf("horizontal right = %v, want %v", got, c2)
	}
	// Diagonal: bottom-right corner reaches c2.
	if got := gradientAt(theme.GradientDiagonal, 9, 9, 10, 10, c1, c2); got != c2 {
		t.Fatalf("diagonal corner = %v, want %v", got, c2)
	}
	// CrossDiagonal: bottom-left corner reaches c2.
	if got := gradientAt(theme.GradientCrossDiagonal, 0, 9, 10, 10, c1, c2); got != c2 {
		t.Fatalf("cross-diagonal corner = %v, want %v", got, c2)
	}
	// Default (a bevel/recorded-only variant): solid c1.
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
	// Degenerate: nothing painted.
	buf, p := newPainter(20, 20)
	paintBg(p, toolkit.Rect{X: 0, Y: 0, W: 0, H: 10}, theme.Bg{Gradient: theme.GradientFlat, Color: theme.Color{9, 9, 9}})
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("degenerate paintBg painted something")
		}
	}
	// Flat fill: every pixel is the solid colour, opaque.
	buf, p = newPainter(20, 20)
	paintBg(p, toolkit.Rect{X: 0, Y: 0, W: 20, H: 20}, theme.Bg{Gradient: theme.GradientFlat, Color: theme.Color{0x11, 0x22, 0x33}})
	for i := 0; i+3 < len(buf); i += 4 {
		if buf[i] != 0x11 || buf[i+1] != 0x22 || buf[i+2] != 0x33 || buf[i+3] != 0xFF {
			t.Fatalf("flat fill wrong at byte %d: %v", i, buf[i:i+4])
		}
	}
	// Vertical gradient: top row != bottom row.
	buf, p = newPainter(4, 10)
	paintBg(p, toolkit.Rect{X: 0, Y: 0, W: 4, H: 10}, theme.Bg{Gradient: theme.GradientVertical, Color: theme.Color{0, 0, 0}, ColorTo: theme.Color{240, 240, 240}})
	top := buf[0]
	bottom := buf[(9*4+0)*4]
	if top == bottom {
		t.Fatalf("vertical gradient did not vary: top=%d bottom=%d", top, bottom)
	}
}

// ---- narrow-surface + overflow render paths -------------------------------

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

// The iconbar stops painting once a launcher button's anchor falls past the
// iconbar's right edge. Reproduced by stuffing in extra apps.
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
	// part of a second. Default 6 launchers + a window -> the window's anchor
	// is past the iconbar's right edge.
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
	s := New(400, BarHeight) // narrow iconbar; default apps + windows won't all fit
	s.SetWindows([]Window{{Id: 1, Title: "off"}, {Id: 2, Title: "off2"}})
	buf := newBuf(s)
	Render(s, buf) // must not panic, must break out of the window loop
}

// On a narrow iconbar the window-button width shrinks so the buttons always
// fit inside the iconbar's right edge (no right-edge clip is ever needed).
func TestRenderWindowShrinksToFit(t *testing.T) {
	// Narrow iconbar (iconbar width = 230 - 100 - 80 = 50) + no launchers so a
	// single window button anchors at the iconbar's left; the shrink keeps its
	// right edge exactly at (or inside) the iconbar's right edge.
	s := New(230, BarHeight)
	s.Apps = nil
	s.SetWindows([]Window{{Id: 1, Title: "fitme"}, {Id: 2, Title: "fit2"}})
	ix, _, iw, _ := s.IconbarRect()
	for i := range s.Windows {
		bx, _, bw, _ := s.WindowButtonRect(i)
		if bx+bw > ix+iw {
			t.Fatalf("window[%d] right edge %d exceeds iconbar right %d (shrink failed)", i, bx+bw, ix+iw)
		}
	}
	buf := newBuf(s)
	Render(s, buf) // must not panic
}

// A focused window button must paint with a sunken bevel: the top stroke is
// the SUNKEN highlight (dark) while an unfocused button carries the RAISED
// highlight (bright).
func TestRenderFocusedSunkenBevel(t *testing.T) {
	s := New(tW, tH)
	// Two windows: idx 0 focused, idx 1 unfocused.
	s.SetWindows([]Window{
		{Id: 1, Title: "f", Focused: true},
		{Id: 2, Title: "u"},
	})
	buf := newBuf(s)
	Render(s, buf)
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

// A minimized window button must paint the "[*] " accent prefix + a raised
// bevel (it is not focused).
func TestRenderMinimizedStylesDim(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "alpha", Minimized: true}})
	buf := newBuf(s)
	Render(s, buf)
	// Confirm the button paints SOME ink (the "[*] alpha" label).
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

// The separator line is painted in the SeparatorW gap when launchers exist.
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
	// ActiveWorkspace below 1 is bumped to 1 by SetWorkspaceCount.
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
