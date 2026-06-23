// Package scene paints the hello-world client's surface. It is pure Go (no
// syscall/js, no cgo) so it builds for any architecture and is unit-tested
// natively — the wasm main only hands it a byte slice.
//
// The picture is a diagonal RGB gradient tinted by a palette index that the
// client cycles on mousedown. With the default palette the surface fades from
// dark blue (top-left) to magenta (bottom-right); subsequent palettes shift
// the hue so the visual change after a click is unambiguous.
package scene

// State is the mutable bit of the scene: just the current palette pick. A
// caller constructs one with New(w, h) and pokes it via NextPalette.
type State struct {
	W, H    int
	Palette int
}

// New makes a State for a surface of width × height pixels with the first
// palette selected.
func New(width, height int) *State {
	return &State{W: width, H: height, Palette: 0}
}

// NextPalette advances to the next palette, wrapping at the end.
func (s *State) NextPalette() {
	s.Palette = (s.Palette + 1) % len(palettes)
}

// palettes is a list of RGB tints applied to the underlying gradient. Each
// tint is a per-channel multiplier in fixed-point (0..255 = 0.0..1.0). The
// gradient itself is a pure diagonal ramp.
var palettes = [][3]uint8{
	{0xFF, 0x90, 0xFF}, // magenta-tinted (default)
	{0x90, 0xFF, 0xC0}, // mint
	{0xFF, 0xD2, 0x70}, // amber
	{0x70, 0xC0, 0xFF}, // sky
	{0xFF, 0xFF, 0xFF}, // neutral white
}

// Render fills buf (a 4*W*H byte slice, RGBA32 row-major) with the scene at
// the current palette. The function does not allocate; buf must be exactly
// the right size or Render panics (size mismatch in the caller is a bug).
func Render(s *State, buf []byte) {
	need := 4 * s.W * s.H
	if len(buf) != need {
		panic("scene: buffer size mismatch")
	}
	tr, tg, tb := palettes[s.Palette][0], palettes[s.Palette][1], palettes[s.Palette][2]
	for y := 0; y < s.H; y++ {
		// 0..255 across height.
		gy := uint32(y*255) / uint32(max(s.H-1, 1))
		for x := 0; x < s.W; x++ {
			gx := uint32(x*255) / uint32(max(s.W-1, 1))
			// Base gradient: R rises with x, B rises with y, G is their average.
			r := uint8((gx * uint32(tr)) / 255)
			g := uint8((((gx + gy) / 2) * uint32(tg)) / 255)
			b := uint8((gy * uint32(tb)) / 255)
			off := (y*s.W + x) * 4
			buf[off] = r
			buf[off+1] = g
			buf[off+2] = b
			buf[off+3] = 0xFF
		}
	}
}

// max returns the larger of a, b. (Go 1.21+ has builtin max — kept here for
// older toolchains; the package builds the same.)
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// AveragePixel computes the mean (R, G, B) of buf, used by tests to assert
// that two palettes produce visually distinct surfaces without pinning every
// pixel down. Returns (0,0,0) if buf is empty or its length isn't a multiple
// of 4 (defensive — callers should pass a real RGBA slice).
func AveragePixel(buf []byte) (r, g, b uint8) {
	if len(buf) == 0 || len(buf)%4 != 0 {
		return 0, 0, 0
	}
	var sr, sg, sb uint64
	n := uint64(len(buf) / 4)
	for i := 0; i < len(buf); i += 4 {
		sr += uint64(buf[i])
		sg += uint64(buf[i+1])
		sb += uint64(buf[i+2])
	}
	return uint8(sr / n), uint8(sg / n), uint8(sb / n)
}

// PaletteCount returns how many palettes the hello client cycles through.
// Exported for tests so they can assert that NextPalette wraps correctly.
func PaletteCount() int { return len(palettes) }
