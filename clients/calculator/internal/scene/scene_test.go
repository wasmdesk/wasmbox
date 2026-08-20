// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"testing"

	"github.com/go-widgets/toolkit"
)

const surfaceW = 260
const surfaceH = 320

func newSurface() []byte { return make([]byte, 4*surfaceW*surfaceH) }

func TestNewAndRender(t *testing.T) {
	s := New(surfaceW, surfaceH)
	if s == nil {
		t.Fatal("New returned nil")
	}
	Render(s, newSurface())
}

func TestDigitEntry(t *testing.T) {
	s := New(surfaceW, surfaceH)
	// Initial display is "0".
	if s.display.Text().Get() != "0" {
		t.Fatalf("initial display want 0, got %q", s.display.Text().Get())
	}
	// Typing "1" replaces the "0" (freshOp/zero replacement rule).
	s.press("1")
	if s.display.Text().Get() != "1" {
		t.Fatalf("after 1: %q", s.display.Text().Get())
	}
	// Typing more digits appends.
	s.press("2")
	s.press("3")
	if s.display.Text().Get() != "123" {
		t.Fatalf("after 123: %q", s.display.Text().Get())
	}
	// Decimal appends when absent.
	s.press(".")
	s.press("4")
	if s.display.Text().Get() != "123.4" {
		t.Fatalf("after 123.4: %q", s.display.Text().Get())
	}
	// Second decimal is a no-op (only one dot per number).
	s.press(".")
	if s.display.Text().Get() != "123.4" {
		t.Fatalf("second dot should be ignored: %q", s.display.Text().Get())
	}
}

func TestArithmetic(t *testing.T) {
	cases := []struct {
		keys []string
		want string
	}{
		{[]string{"2", "+", "3", "="}, "5"},
		{[]string{"1", "0", "-", "4", "="}, "6"},
		{[]string{"6", "*", "7", "="}, "42"},
		{[]string{"1", "0", "/", "4", "="}, "2.5"},
		{[]string{"5", "/", "0", "="}, "0"}, // divide-by-zero → 0 (pocket calc reset)
	}
	for _, c := range cases {
		s := New(surfaceW, surfaceH)
		for _, k := range c.keys {
			s.press(k)
		}
		if s.display.Text().Get() != c.want {
			t.Fatalf("keys=%v want %q got %q", c.keys, c.want, s.display.Text().Get())
		}
	}
}

func TestClearNegatePercent(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.press("1")
	s.press("0")
	s.press("0")
	// Percent: 100 → 1.
	s.press("%")
	if s.display.Text().Get() != "1" {
		t.Fatalf("100%% want 1, got %q", s.display.Text().Get())
	}
	// Negate: 1 → -1.
	s.press("+/-")
	if s.display.Text().Get() != "-1" {
		t.Fatalf("negate want -1, got %q", s.display.Text().Get())
	}
	// Clear resets everything.
	s.press("C")
	if s.display.Text().Get() != "0" || s.op != 0 || s.accum != 0 {
		t.Fatalf("after C: display=%q op=%v accum=%v", s.display.Text().Get(), s.op, s.accum)
	}
}

func TestEqualsWithoutOp(t *testing.T) {
	// Pressing = with no pending op is a no-op — display stays put.
	s := New(surfaceW, surfaceH)
	s.press("4")
	s.press("2")
	s.press("=")
	if s.display.Text().Get() != "42" {
		t.Fatalf("= without op should be no-op, got %q", s.display.Text().Get())
	}
}

func TestChainedOps(t *testing.T) {
	// 2 + 3 * 4 → in this calculator's simple left-to-right model,
	// pressing * folds the pending + first: (2+3) → 5, then 5*4=20.
	s := New(surfaceW, surfaceH)
	s.press("2")
	s.press("+")
	s.press("3")
	s.press("*")
	// After clicking *, the display shows the display-buffer content
	// which is still "3" — accum has 2, but a chained op doesn't
	// auto-compute in this simple model. Just verify the display shows
	// "3" and the pending op is now '*'.
	if s.display.Text().Get() != "3" {
		t.Fatalf("after 2+3*: display %q", s.display.Text().Get())
	}
	if s.op != '*' {
		t.Fatalf("after 2+3*: op %v", s.op)
	}
}

func TestHandleMouseHitsButton(t *testing.T) {
	s := New(surfaceW, surfaceH)
	// Locate the "7" button + click its centre.
	var seven *toolkit.Button
	for _, b := range s.buttons {
		if b.Label().Get() == "7" {
			seven = b
			break
		}
	}
	if seven == nil {
		t.Fatal("no 7 button in the grid")
	}
	r := seven.Bounds()
	if !s.HandleMouse(r.X+r.W/2, r.Y+r.H/2) {
		t.Fatal("HandleMouse on 7 must return true")
	}
	if s.display.Text().Get() != "7" {
		t.Fatalf("after click 7: display %q", s.display.Text().Get())
	}
}

func TestHandleMouseMissAllButtons(t *testing.T) {
	s := New(surfaceW, surfaceH)
	if s.HandleMouse(-10, -10) {
		t.Fatal("HandleMouse off-canvas must return false")
	}
}

func TestFormatNumber(t *testing.T) {
	cases := []struct {
		v    float64
		want string
	}{
		{5, "5"},
		{-3, "-3"},
		{0, "0"},
		{2.5, "2.5"},
		{-0.5, "-0.5"},
	}
	for _, c := range cases {
		if got := formatNumber(c.v); got != c.want {
			t.Errorf("formatNumber(%v) = %q want %q", c.v, got, c.want)
		}
	}
}

func TestApplyUnknownOp(t *testing.T) {
	// Defensive: unknown op returns rhs unchanged.
	if apply(1, 2, '?') != 2 {
		t.Fatal("apply unknown op should return rhs")
	}
}

func TestFillOutOfBuffer(t *testing.T) {
	buf := make([]byte, 16)
	fill(buf, 4, toolkit.Rect{X: 0, Y: 0, W: 100, H: 100}, toolkit.RGB(0xFF, 0, 0))
}

// TestGoldenLayoutRects is the behaviour-preserving proof: the box-layout
// container tree lays every widget out to the EXACT same toolkit.Rect the old
// hand-placed code produced. The expected rects are recomputed here with the
// original arithmetic (display inset by sideMargin/buttonPadTop; each button at
// sideMargin+c*(colW+buttonGap), baseY+r*(rowH+buttonGap), colW×rowH).
//
// These rects are font-independent: the Grid/VBox tracks are fixed pixel sizes,
// so switching Calculator to the AA/shaped OpenType face (see enableAAText in
// New) leaves every widget rect byte-identical — only the glyph pixels inside a
// cell change. TestAATextIsAntiAliased pins that pixel-level AA behaviour.
func TestGoldenLayoutRects(t *testing.T) {
	s := New(surfaceW, surfaceH)

	wantDisplay := toolkit.Rect{X: sideMargin, Y: buttonPadTop, W: surfaceW - 2*sideMargin, H: displayH}
	if got := s.display.Bounds(); got != wantDisplay {
		t.Fatalf("display bounds = %+v, want %+v", got, wantDisplay)
	}

	baseY := buttonPadTop + displayH + buttonGap
	var want []toolkit.Rect
	for r := 0; r < 5; r++ {
		for c := 0; c < 4; c++ {
			if keys[r][c] == "" {
				continue // matches New's skip of empty cells
			}
			want = append(want, toolkit.Rect{
				X: sideMargin + c*(colW+buttonGap),
				Y: baseY + r*(rowH+buttonGap),
				W: colW,
				H: rowH,
			})
		}
	}
	if len(want) != len(s.buttons) {
		t.Fatalf("button count = %d, want %d", len(s.buttons), len(want))
	}
	for i, b := range s.buttons {
		if got := b.Bounds(); got != want[i] {
			t.Fatalf("button %d (%q) bounds = %+v, want %+v", i, b.Label().Get(), got, want[i])
		}
	}
}

// TestClickFarRightColumnRoutes guards the one geometry subtlety of the
// container migration: the button grid is a FULL-width flex child (reaching the
// surface's right edge), wider than the inset display, so a click near the
// right-most operator column's outer edge — outside the display's width — still
// routes to the button. A width-clipped column would drop these clicks.
func TestClickFarRightColumnRoutes(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.press("6")
	// "*" is row 1, col 3: X=200,W=60 → right edge 260. Click at x=258.
	if !s.HandleMouse(258, 104) {
		t.Fatal("click near the right edge of * must route to the button")
	}
	if s.op != '*' {
		t.Fatalf("after clicking *: op = %q, want '*'", s.op)
	}
}

// TestClickInterButtonGapMisses proves a click in a 4px inter-column gutter
// routes to no button (HandleMouse returns false), preserving the old
// insideRect miss behaviour.
func TestClickInterButtonGapMisses(t *testing.T) {
	s := New(surfaceW, surfaceH)
	// col0 right edge = 8+60 = 68; col1 left = 72. x=70 sits in the gutter,
	// y=104 in row 1.
	if s.HandleMouse(70, 104) {
		t.Fatal("click in the inter-column gutter must not route")
	}
}

func TestPressAfterOpWithBadDisplay(t *testing.T) {
	// Defensive: if display.Text().Get() is garbage when '+' is pressed,
	// accum stays at its default (0) — no panic.
	s := New(surfaceW, surfaceH)
	s.display.SetText("not-a-number")
	s.press("+")
	if s.op != '+' {
		t.Fatal("op should still register")
	}
	// Same for =: right operand parse-fail early-returns.
	s.press("=")
	if s.display.Text().Get() != "not-a-number" {
		t.Fatal("bad-parse = should leave display alone")
	}
}

func TestPressAfterOpNegateNoOp(t *testing.T) {
	// Negate/Percent with unparseable display: strconv.ParseFloat errors
	// early — display stays put, no crash.
	s := New(surfaceW, surfaceH)
	s.display.SetText("garbage")
	s.press("+/-")
	if s.display.Text().Get() != "garbage" {
		t.Fatal("negate on garbage should be no-op")
	}
	s.press("%")
	if s.display.Text().Get() != "garbage" {
		t.Fatal("percent on garbage should be no-op")
	}
}

func TestHandleKeyDigits(t *testing.T) {
	s := New(surfaceW, surfaceH)
	for _, k := range []string{"1", "2", "3"} {
		if !s.HandleKey(k) {
			t.Fatalf("HandleKey(%q) should return true", k)
		}
	}
	if s.display.Text().Get() != "123" {
		t.Fatalf("after 1,2,3 digits: %q", s.display.Text().Get())
	}
}

func TestHandleKeyOps(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.HandleKey("2")
	s.HandleKey("+")
	s.HandleKey("3")
	s.HandleKey("Enter") // Enter = "="
	if s.display.Text().Get() != "5" {
		t.Fatalf("2+3<Enter>: %q", s.display.Text().Get())
	}
	// "=" also works.
	s.HandleKey("*")
	s.HandleKey("4")
	s.HandleKey("=")
	if s.display.Text().Get() != "20" {
		t.Fatalf("5*4=: %q", s.display.Text().Get())
	}
}

func TestHandleKeyClearAliases(t *testing.T) {
	// Escape, Delete, Backspace, c, C all clear.
	for _, k := range []string{"Escape", "Delete", "Backspace", "c", "C"} {
		s := New(surfaceW, surfaceH)
		s.HandleKey("4")
		s.HandleKey("2")
		s.HandleKey(k)
		if s.display.Text().Get() != "0" {
			t.Fatalf("clear via %q: display=%q, want 0", k, s.display.Text().Get())
		}
	}
}

func TestHandleKeyPercentAndDecimal(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.HandleKey("5")
	s.HandleKey("0")
	s.HandleKey("%")
	if s.display.Text().Get() != "0.5" {
		t.Fatalf("50%%: %q", s.display.Text().Get())
	}
	// Decimal directly.
	s2 := New(surfaceW, surfaceH)
	s2.HandleKey("3")
	s2.HandleKey(".")
	s2.HandleKey("1")
	if s2.display.Text().Get() != "3.1" {
		t.Fatalf("3.1: %q", s2.display.Text().Get())
	}
}

func TestHandleKeyUnknownReturnsFalse(t *testing.T) {
	s := New(surfaceW, surfaceH)
	if s.HandleKey("F1") {
		t.Fatal("unknown key should return false")
	}
	if s.HandleKey("") {
		t.Fatal("empty key should return false")
	}
}

func TestHandleCharForwards(t *testing.T) {
	s := New(surfaceW, surfaceH)
	if !s.HandleChar("7") {
		t.Fatal("HandleChar(7) should return true")
	}
	if s.display.Text().Get() != "7" {
		t.Fatalf("after char 7: %q", s.display.Text().Get())
	}
	if s.HandleChar("") {
		t.Fatal("HandleChar empty should return false")
	}
}

// TestAATextIsAntiAliased is the pilot's pixel-level proof that Calculator now
// renders with the toolkit's AA/shaped OpenType face rather than the 5x7 bitmap.
// It renders the scene and scans the display Entry's rect for the anti-aliasing
// signature: neutral-grey pixels (R==G==B) whose value lies STRICTLY between the
// glyph ink (OnSurface, 54) and the Entry ground (Surface, 255). The bitmap font
// produces only fully-lit ink or untouched ground — never a partial-coverage
// blend — so the presence of intermediate greys is exactly what distinguishes
// the AA face from the bitmap default. It also asserts a solid ink core is
// present (the "0" glyph was actually painted, not just a stray edge).
func TestAATextIsAntiAliased(t *testing.T) {
	s := New(surfaceW, surfaceH)
	buf := newSurface()
	Render(s, buf)

	d := s.display.Bounds()
	on := s.theme.OnSurface // ink (54,54,54)
	var aaBlend, inkCore int
	for y := d.Y; y < d.Y+d.H; y++ {
		for x := d.X; x < d.X+d.W; x++ {
			off := (y*surfaceW + x) * 4
			r, g, b := buf[off], buf[off+1], buf[off+2]
			if r != g || g != b {
				continue // AA of neutral ink over neutral ground stays neutral
			}
			switch {
			case r > on.R && r < 255:
				aaBlend++ // partial-coverage edge pixel — the AA signature
			case r <= on.R+2:
				inkCore++ // fully-covered glyph body
			}
		}
	}
	if aaBlend == 0 {
		t.Fatalf("no anti-aliased blend pixels in display rect %+v — AA face not active", d)
	}
	if inkCore == 0 {
		t.Fatalf("no solid ink pixels in display rect %+v — no glyph painted", d)
	}
	t.Logf("display %+v: aaBlend=%d inkCore=%d", d, aaBlend, inkCore)
}

// TestAAButtonLabelsFitCells guards the pilot's central question — does the
// taller (20px) AA face overflow the fixed 60×40 key cells? — as a real
// assertion: every button label's shaped width must fit inside colW and the face
// height inside rowH, with the toolkit auto-centring it via (r.H-glyphHeight())/2.
// If a future face or size change overflowed a cell, this fails loudly instead of
// silently clipping glyphs.
func TestAAButtonLabelsFitCells(t *testing.T) {
	s := New(surfaceW, surfaceH) // switches the global font to the AA face
	h := 20                      // Atkinson Hyperlegible @16px line height (asserted stable here)
	if got := heightProbe(); got != h {
		t.Fatalf("AA face height = %d, want %d (retune rowH/displayH if this changes)", got, h)
	}
	for _, b := range s.buttons {
		w := toolkit.TextWidth(b.Label().Get())
		if w > colW {
			t.Fatalf("label %q width %d overflows colW %d", b.Label().Get(), w, colW)
		}
		if h > rowH {
			t.Fatalf("AA height %d overflows rowH %d", h, rowH)
		}
	}
	if h > displayH {
		t.Fatalf("AA height %d overflows displayH %d", h, displayH)
	}
}

// heightProbe returns the active font's line height by measuring the bundled AA
// face directly (New has already switched the global font to it).
func heightProbe() int {
	f, err := toolkit.DefaultOpenTypeFont(toolkit.DefaultOpenTypeSizePx)
	if err != nil {
		return -1
	}
	return f.Height()
}

func TestPressDotAfterFreshOp(t *testing.T) {
	// After 5 + . → display becomes "0.".
	s := New(surfaceW, surfaceH)
	s.press("5")
	s.press("+")
	s.press(".")
	if s.display.Text().Get() != "0." {
		t.Fatalf("after 5+. want 0., got %q", s.display.Text().Get())
	}
}
