package scene

import (
	"testing"

	"github.com/go-widgets/toolkit"
)

// TestRenderSinglePixelSurface exercises the degenerate 1×1 surface, where
// max(H-1, 1) and max(W-1, 1) both take their "b" branch (H-1 == W-1 == 0) —
// the only way to hit that side of max's comparison, since every other test
// surface is larger than 1px per axis.
func TestRenderSinglePixelSurface(t *testing.T) {
	s := New(1, 1)
	buf := make([]byte, 4)
	Render(s, buf)
	if buf[3] != 0xFF {
		t.Fatalf("alpha = %d, want 0xFF", buf[3])
	}
	// gx=gy=0 at the single pixel, same as the top-left corner of any
	// larger surface: near-black regardless of palette.
	if buf[0] != 0 || buf[1] != 0 || buf[2] != 0 {
		t.Fatalf("1x1 RGB = (%d,%d,%d), want (0,0,0)", buf[0], buf[1], buf[2])
	}
}

func TestRenderFillsExpectedSize(t *testing.T) {
	s := New(8, 4)
	buf := make([]byte, 4*8*4)
	Render(s, buf)
	// Every alpha byte must be 0xFF (the renderer paints opaque).
	for i := 3; i < len(buf); i += 4 {
		if buf[i] != 0xFF {
			t.Fatalf("alpha byte at %d = %d, want 0xFF", i, buf[i])
		}
	}
}

func TestRenderTopLeftIsDark(t *testing.T) {
	s := New(64, 64)
	buf := make([]byte, 4*64*64)
	Render(s, buf)
	// Pixel (0,0) — the top-left corner of the gradient is near-black on
	// every palette (gx=gy=0).
	if buf[0] != 0 || buf[1] != 0 || buf[2] != 0 {
		t.Fatalf("top-left RGB = (%d,%d,%d), want (0,0,0)", buf[0], buf[1], buf[2])
	}
}

func TestNextPaletteWraps(t *testing.T) {
	s := New(2, 2)
	for i := 0; i < PaletteCount(); i++ {
		if s.Palette() != i {
			t.Fatalf("palette = %d, want %d", s.Palette(), i)
		}
		s.NextPalette()
	}
	if s.Palette() != 0 {
		t.Fatalf("palette did not wrap to 0, got %d", s.Palette())
	}
}

func TestPalettesProduceDistinctAverages(t *testing.T) {
	s := New(32, 32)
	buf := make([]byte, 4*32*32)
	seen := map[[3]uint8]bool{}
	for i := 0; i < PaletteCount(); i++ {
		Render(s, buf)
		r, g, b := AveragePixel(buf)
		key := [3]uint8{r, g, b}
		if seen[key] {
			t.Fatalf("palette %d produced a duplicate average (%d,%d,%d)", i, r, g, b)
		}
		seen[key] = true
		s.NextPalette()
	}
}

func TestRenderPanicsOnSizeMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on size mismatch")
		}
	}()
	s := New(8, 8)
	Render(s, make([]byte, 4))
}

func TestAveragePixelDefensive(t *testing.T) {
	r, g, b := AveragePixel(nil)
	if r != 0 || g != 0 || b != 0 {
		t.Fatalf("empty buf: got (%d,%d,%d)", r, g, b)
	}
	r, g, b = AveragePixel([]byte{1, 2, 3}) // bad length, not a multiple of 4
	if r != 0 || g != 0 || b != 0 {
		t.Fatalf("non-RGBA buf: got (%d,%d,%d)", r, g, b)
	}
}

// TestGradientWidgetPaintsNonEmptyPixels asserts the toolkit widget's Draw
// actually wrote colour into the buffer (not just left it zeroed) — a
// regression guard for the toolkit-widget conversion: it's easy to build a
// widget whose Bounds don't line up with the painter's canvas and silently
// paint nothing.
func TestGradientWidgetPaintsNonEmptyPixels(t *testing.T) {
	s := New(16, 16)
	buf := make([]byte, 4*16*16)
	Render(s, buf)
	nonZero := false
	for i := 0; i < len(buf); i += 4 {
		if buf[i] != 0 || buf[i+1] != 0 || buf[i+2] != 0 {
			nonZero = true
			break
		}
	}
	if !nonZero {
		t.Fatal("rendered buffer is entirely black; widget did not paint")
	}
	// The bottom-right corner (gx=gy=255) should be fully tinted by the
	// default palette's magenta ({0xFF, 0x90, 0xFF}), the brightest corner
	// of the gradient.
	last := len(buf) - 4
	if buf[last] == 0 && buf[last+1] == 0 && buf[last+2] == 0 {
		t.Fatal("bottom-right corner is black; expected the brightest gradient corner")
	}
}

// TestHandleMouseAdvancesPaletteThroughWidgetEvent drives a click via
// State.HandleMouse — the same path main.go's onInput handler uses — and
// confirms it reaches the widget's OnEvent and advances the palette exactly
// like a direct NextPalette() call would.
func TestHandleMouseAdvancesPaletteThroughWidgetEvent(t *testing.T) {
	s := New(100, 80)
	if s.Palette() != 0 {
		t.Fatalf("initial palette = %d, want 0", s.Palette())
	}
	if changed := s.HandleMouse(10, 10); !changed {
		t.Fatal("HandleMouse reported no change on a click")
	}
	if s.Palette() != 1 {
		t.Fatalf("palette after one click = %d, want 1", s.Palette())
	}
	// One full lap (PaletteCount() more clicks) must land back on 1, same
	// wraparound TestNextPaletteWraps checks, driven through the widget
	// event path instead of a direct NextPalette() call.
	for i := 0; i < PaletteCount(); i++ {
		s.HandleMouse(0, 0)
	}
	if s.Palette() != 1 {
		t.Fatalf("palette after a full lap = %d, want back to 1", s.Palette())
	}
}

// TestHandleMouseIgnoresNonClickIsNotApplicable documents that
// gradientWidget.OnEvent only reacts to EventClick; HandleMouse always
// sends EventClick, so this is exercised indirectly by
// TestHandleMouseAdvancesPaletteThroughWidgetEvent above. Kept as a direct
// unit test of OnEvent's guard for a non-click event kind, since HandleMouse
// alone can't reach that branch.
func TestGradientWidgetOnEventIgnoresNonClick(t *testing.T) {
	s := New(10, 10)
	before := s.Palette()
	s.root.OnEvent(toolkit.Event{Kind: toolkit.EventKeyDown, Code: "Enter"})
	if s.Palette() != before {
		t.Fatalf("palette changed on a non-click event: %d -> %d", before, s.Palette())
	}
}
