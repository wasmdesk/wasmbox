package scene

import "testing"

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
		if s.Palette != i {
			t.Fatalf("palette = %d, want %d", s.Palette, i)
		}
		s.NextPalette()
	}
	if s.Palette != 0 {
		t.Fatalf("palette did not wrap to 0, got %d", s.Palette)
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
