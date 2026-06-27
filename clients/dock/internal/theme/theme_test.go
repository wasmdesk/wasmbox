// SPDX-License-Identifier: BSD-3-Clause

package theme

import "testing"

// The default Fluxbox-light theme must report the exact Openbox-spelled
// colours the README documents — the values are part of the user-visible
// contract.
func TestDefaultFluxboxLightColours(t *testing.T) {
	th := DefaultFluxboxLight()
	if got, want := th.Window.Active.Title.Bg.Color, MustHex("#c8c8c8"); got != want {
		t.Fatalf("active.title.bg.color = %v, want %v", got, want)
	}
	if got, want := th.Window.Active.Title.Bg.ColorTo, MustHex("#909090"); got != want {
		t.Fatalf("active.title.bg.colorTo = %v, want %v", got, want)
	}
	if got, want := th.Window.Active.Title.Label.Color, MustHex("#1a1a1a"); got != want {
		t.Fatalf("active.label.text.color = %v, want %v", got, want)
	}
	if got, want := th.Window.Inactive.Title.Bg.Color, MustHex("#909090"); got != want {
		t.Fatalf("inactive.title.bg.color = %v, want %v", got, want)
	}
	if got, want := th.Osd.Bg.Color, MustHex("#d0d0d0"); got != want {
		t.Fatalf("osd.bg.color = %v, want %v", got, want)
	}
	if got, want := th.Border.Color, MustHex("#4a4a4a"); got != want {
		t.Fatalf("border.color = %v, want %v", got, want)
	}
	if th.Border.Width != 1 {
		t.Fatalf("border.width = %d, want 1", th.Border.Width)
	}
	if th.Padding.Width != 1 || th.Padding.Height != 1 {
		t.Fatalf("padding = %+v, want 1x1", th.Padding)
	}
	if th.Window.Active.Title.Bg.Gradient != GradientVertical {
		t.Fatalf("active.title.bg gradient = %v, want vertical", th.Window.Active.Title.Bg.Gradient)
	}
	if th.Window.Active.Title.Label.Font.Face != "sans" || th.Window.Active.Title.Label.Font.Size != 9 {
		t.Fatalf("label font = %+v, want {sans,9}", th.Window.Active.Title.Label.Font)
	}
	// Menu + osd reuse the same ink as the active label.
	if th.Menu.Title.Label.Color != th.Window.Active.Title.Label.Color {
		t.Fatalf("menu.title.label.color != active.label.color")
	}
	if th.Menu.Items.Text.Color != th.Window.Active.Title.Label.Color {
		t.Fatalf("menu.items.text.color != active.label.color")
	}
	if th.Osd.Label.Color != th.Window.Active.Title.Label.Color {
		t.Fatalf("osd.label.text.color != active.label.color")
	}
}

func TestMustHexValid(t *testing.T) {
	if got := MustHex("#ABcd12"); got != (Color{0xAB, 0xCD, 0x12}) {
		t.Fatalf("MustHex case-mixed = %v, want {0xAB,0xCD,0x12}", got)
	}
	if got := MustHex("#000000"); got != (Color{0, 0, 0}) {
		t.Fatalf("MustHex black = %v", got)
	}
	if got := MustHex("#FFFFFF"); got != (Color{0xFF, 0xFF, 0xFF}) {
		t.Fatalf("MustHex white = %v", got)
	}
}

func TestMustHexInvalid(t *testing.T) {
	cases := []string{"", "#fff", "fff000", "#GGGGGG", "#12345Z", "#12-456"}
	for _, c := range cases {
		c := c
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("MustHex(%q) should panic", c)
				}
			}()
			MustHex(c)
		}()
	}
}

func TestPaintGradientFlat(t *testing.T) {
	const w, h = 8, 4
	buf := make([]byte, 4*w*h)
	PaintGradient(buf, w, h, 0, 0, w, h, GradientFlat, Color{0x20, 0x40, 0x80}, Color{0xFF, 0, 0})
	// Every pixel must equal c1 with alpha 0xFF.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			off := (y*w + x) * 4
			if buf[off] != 0x20 || buf[off+1] != 0x40 || buf[off+2] != 0x80 || buf[off+3] != 0xFF {
				t.Fatalf("flat pixel (%d,%d) = %v", x, y, buf[off:off+4])
			}
		}
	}
}

func TestPaintGradientVertical(t *testing.T) {
	const w, h = 4, 8
	buf := make([]byte, 4*w*h)
	PaintGradient(buf, w, h, 0, 0, w, h, GradientVertical, Color{0, 0, 0}, Color{0xFF, 0xFF, 0xFF})
	top := buf[0]
	bottom := buf[((h-1)*w+0)*4]
	if top != 0 {
		t.Fatalf("vertical top R = %d, want 0", top)
	}
	if bottom != 0xFF {
		t.Fatalf("vertical bottom R = %d, want 0xFF", bottom)
	}
	// Monotonic: each row red >= row above.
	for y := 1; y < h; y++ {
		a := buf[((y-1)*w+0)*4]
		b := buf[(y*w+0)*4]
		if b < a {
			t.Fatalf("vertical not monotonic at y=%d: %d -> %d", y, a, b)
		}
	}
}

func TestPaintGradientHorizontal(t *testing.T) {
	const w, h = 8, 4
	buf := make([]byte, 4*w*h)
	PaintGradient(buf, w, h, 0, 0, w, h, GradientHorizontal, Color{0, 0, 0}, Color{0xFF, 0xFF, 0xFF})
	if got := buf[0]; got != 0 {
		t.Fatalf("horizontal left R = %d, want 0", got)
	}
	if got := buf[(w-1)*4]; got != 0xFF {
		t.Fatalf("horizontal right R = %d, want 0xFF", got)
	}
}

func TestPaintGradientDiagonal(t *testing.T) {
	const w, h = 4, 4
	buf := make([]byte, 4*w*h)
	PaintGradient(buf, w, h, 0, 0, w, h, GradientDiagonal, Color{0, 0, 0}, Color{0xFF, 0, 0})
	// top-left = c1, bottom-right = c2.
	if buf[0] != 0 {
		t.Fatalf("diagonal TL = %d, want 0", buf[0])
	}
	if got := buf[((h-1)*w+(w-1))*4]; got != 0xFF {
		t.Fatalf("diagonal BR = %d, want 0xFF", got)
	}
}

func TestPaintGradientCrossDiagonal(t *testing.T) {
	const w, h = 4, 4
	buf := make([]byte, 4*w*h)
	PaintGradient(buf, w, h, 0, 0, w, h, GradientCrossDiagonal, Color{0, 0, 0}, Color{0xFF, 0, 0})
	// top-right = c1, bottom-left = c2.
	if got := buf[(w-1)*4]; got != 0 {
		t.Fatalf("crossdiag TR = %d, want 0", got)
	}
	if got := buf[((h-1)*w+0)*4]; got != 0xFF {
		t.Fatalf("crossdiag BL = %d, want 0xFF", got)
	}
}

func TestPaintGradientRecordedTypesAreFlat(t *testing.T) {
	// pipecross / rectangle / pyramid / raisedbevel / sunkenbevel /
	// parentrelative are recorded for themerc compatibility and paint as a
	// flat c1 fill in v0.
	for _, g := range []GradientType{
		GradientPipeCross, GradientRectangle, GradientPyramid,
		GradientRaisedBevel, GradientSunkenBevel, GradientParentRelative,
	} {
		buf := make([]byte, 4*4*4)
		PaintGradient(buf, 4, 4, 0, 0, 4, 4, g, Color{0x10, 0x20, 0x30}, Color{0xFF, 0xFF, 0xFF})
		for i := 0; i < 16; i++ {
			off := i * 4
			if buf[off] != 0x10 || buf[off+1] != 0x20 || buf[off+2] != 0x30 {
				t.Fatalf("gradient %v not c1 at %d: %v", g, i, buf[off:off+3])
			}
		}
	}
}

func TestPaintGradientDegenerate(t *testing.T) {
	buf := make([]byte, 4*4*4)
	PaintGradient(buf, 4, 4, 0, 0, 0, 4, GradientFlat, Color{0xFF, 0, 0}, Color{}) // w=0
	PaintGradient(buf, 4, 4, 0, 0, 4, 0, GradientFlat, Color{0xFF, 0, 0}, Color{}) // h=0
	for i := range buf {
		if buf[i] != 0 {
			t.Fatalf("degenerate paint leaked byte at %d = %d", i, buf[i])
		}
	}
}

func TestPaintGradientClipsOutOfBounds(t *testing.T) {
	const w, h = 4, 4
	buf := make([]byte, 4*w*h)
	// rect extends past the surface on every side.
	PaintGradient(buf, w, h, -2, -2, w+4, h+4, GradientFlat, Color{0x11, 0x22, 0x33}, Color{})
	// All in-buffer pixels should be c1; no panic.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			off := (y*w + x) * 4
			if buf[off] != 0x11 || buf[off+1] != 0x22 || buf[off+2] != 0x33 {
				t.Fatalf("clipped paint pixel (%d,%d) = %v", x, y, buf[off:off+3])
			}
		}
	}
}

// lerp clamps out-of-range step values to [0, denom] so callers feeding
// degenerate coordinates (e.g. a tiny dest rect) never index past the colour
// stops.
func TestLerpClamps(t *testing.T) {
	c1 := Color{0, 0, 0}
	c2 := Color{0xFF, 0xFF, 0xFF}
	if got := lerp(c1, c2, -5, 10); got != c1 {
		t.Fatalf("lerp(-5,10) = %v, want c1", got)
	}
	if got := lerp(c1, c2, 25, 10); got != c2 {
		t.Fatalf("lerp(25,10) = %v, want c2", got)
	}
}

// A 1-pixel-thin vertical section must collapse to a c1 fill (denom == 0).
func TestPaintGradientOnePixelTall(t *testing.T) {
	buf := make([]byte, 4*4*1)
	PaintGradient(buf, 4, 1, 0, 0, 4, 1, GradientVertical, Color{0xAA, 0, 0}, Color{0xFF, 0, 0})
	for x := 0; x < 4; x++ {
		if buf[x*4] != 0xAA {
			t.Fatalf("1px tall vertical col %d = %d, want 0xAA", x, buf[x*4])
		}
	}
}
