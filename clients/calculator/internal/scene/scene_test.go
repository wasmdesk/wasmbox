// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"testing"

	"github.com/wasmdesk/toolkit"
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
	if s.display.Text != "0" {
		t.Fatalf("initial display want 0, got %q", s.display.Text)
	}
	// Typing "1" replaces the "0" (freshOp/zero replacement rule).
	s.press("1")
	if s.display.Text != "1" {
		t.Fatalf("after 1: %q", s.display.Text)
	}
	// Typing more digits appends.
	s.press("2")
	s.press("3")
	if s.display.Text != "123" {
		t.Fatalf("after 123: %q", s.display.Text)
	}
	// Decimal appends when absent.
	s.press(".")
	s.press("4")
	if s.display.Text != "123.4" {
		t.Fatalf("after 123.4: %q", s.display.Text)
	}
	// Second decimal is a no-op (only one dot per number).
	s.press(".")
	if s.display.Text != "123.4" {
		t.Fatalf("second dot should be ignored: %q", s.display.Text)
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
		if s.display.Text != c.want {
			t.Fatalf("keys=%v want %q got %q", c.keys, c.want, s.display.Text)
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
	if s.display.Text != "1" {
		t.Fatalf("100%% want 1, got %q", s.display.Text)
	}
	// Negate: 1 → -1.
	s.press("±")
	if s.display.Text != "-1" {
		t.Fatalf("negate want -1, got %q", s.display.Text)
	}
	// Clear resets everything.
	s.press("C")
	if s.display.Text != "0" || s.op != 0 || s.accum != 0 {
		t.Fatalf("after C: display=%q op=%v accum=%v", s.display.Text, s.op, s.accum)
	}
}

func TestEqualsWithoutOp(t *testing.T) {
	// Pressing = with no pending op is a no-op — display stays put.
	s := New(surfaceW, surfaceH)
	s.press("4")
	s.press("2")
	s.press("=")
	if s.display.Text != "42" {
		t.Fatalf("= without op should be no-op, got %q", s.display.Text)
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
	if s.display.Text != "3" {
		t.Fatalf("after 2+3*: display %q", s.display.Text)
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
		if b.Label == "7" {
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
	if s.display.Text != "7" {
		t.Fatalf("after click 7: display %q", s.display.Text)
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

func TestInsideRect(t *testing.T) {
	r := toolkit.Rect{X: 10, Y: 10, W: 20, H: 20}
	if !insideRect(15, 15, r) {
		t.Fatal("centre must be inside")
	}
	if insideRect(0, 0, r) {
		t.Fatal("(0,0) must be outside")
	}
}

func TestPressAfterOpWithBadDisplay(t *testing.T) {
	// Defensive: if display.Text is garbage when '+' is pressed,
	// accum stays at its default (0) — no panic.
	s := New(surfaceW, surfaceH)
	s.display.Text = "not-a-number"
	s.press("+")
	if s.op != '+' {
		t.Fatal("op should still register")
	}
	// Same for =: right operand parse-fail early-returns.
	s.press("=")
	if s.display.Text != "not-a-number" {
		t.Fatal("bad-parse = should leave display alone")
	}
}

func TestPressAfterOpNegateNoOp(t *testing.T) {
	// Negate/Percent with unparseable display: strconv.ParseFloat errors
	// early — display stays put, no crash.
	s := New(surfaceW, surfaceH)
	s.display.Text = "garbage"
	s.press("±")
	if s.display.Text != "garbage" {
		t.Fatal("negate on garbage should be no-op")
	}
	s.press("%")
	if s.display.Text != "garbage" {
		t.Fatal("percent on garbage should be no-op")
	}
}

func TestPressDotAfterFreshOp(t *testing.T) {
	// After 5 + . → display becomes "0.".
	s := New(surfaceW, surfaceH)
	s.press("5")
	s.press("+")
	s.press(".")
	if s.display.Text != "0." {
		t.Fatalf("after 5+. want 0., got %q", s.display.Text)
	}
}
