// Package scene paints the hello-world client's surface. It is pure Go (no
// syscall/js, no cgo) so it builds for any architecture and is unit-tested
// natively — the wasm main only hands it a byte slice.
//
// The picture is a diagonal RGB gradient tinted by a palette index that the
// client cycles on mousedown. With the default palette the surface fades from
// dark blue (top-left) to magenta (bottom-right); subsequent palettes shift
// the hue so the visual change after a click is unambiguous.
//
// Like clients/calculator, the scene is a small toolkit widget tree: a single
// custom leaf widget (gradientWidget) that IS the tree — there's no
// container, since the whole surface is one paintable. New builds it once,
// Render walks it via a painter.PixelPainter, and HandleMouse routes clicks
// into it through Widget.OnEvent, exactly like calculator's root.OnEvent.
package scene

import (
	"sync"

	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
)

// aaOnce flips the toolkit's active font to anti-aliased, shaped OpenType text
// exactly once for this client process. It is package-scoped because SetFont is
// a process-global; a single opt-in matches the toolkit's "flip it once at
// start-up" contract.
var aaOnce sync.Once

// enableAAText installs the toolkit's bundled AA/shaped OpenType face (Atkinson
// Hyperlegible @16px, toolkit v0.77.0). The bundled face never fails to parse
// (the error is documented as never-returned); on the impossible error path the
// toolkit leaves the still-working bitmap default active. Hello draws no text —
// its surface is a pure gradient — so the switch is a fleet-consistency no-op
// here, kept so every proportional-font client opts in the same way.
func enableAAText() { aaOnce.Do(func() { _ = toolkit.UseOpenTypeText() }) }

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

// gradientWidget is the scene's single leaf: it embeds toolkit.Base (for the
// stock Bounds/SetBounds/HitTest) and implements Draw + OnEvent itself. Draw
// paints the palette-tinted diagonal gradient; OnEvent advances the palette
// on any click, matching the original hand-drawn client's "any mousedown
// cycles the palette" behaviour (no hit-testing beyond "was this a click").
type gradientWidget struct {
	toolkit.Base
	palette int
}

// newGradientWidget builds a gradientWidget sized w×h, positioned at the
// surface origin (it IS the whole surface — the scene's root).
func newGradientWidget(w, h int) *gradientWidget {
	g := &gradientWidget{}
	g.SetBounds(toolkit.Rect{X: 0, Y: 0, W: w, H: h})
	return g
}

// nextPalette advances to the next palette, wrapping at the end.
func (g *gradientWidget) nextPalette() {
	g.palette = (g.palette + 1) % len(palettes)
}

// Draw paints the diagonal RGB gradient tinted by the current palette,
// pixel by pixel, through the Painter interface (PutPixel with A=0xFF
// overwrites verbatim — byte-identical to the previous direct buffer
// writes). theme is accepted to satisfy toolkit.Widget but unused: the
// gradient owns 100% of its colour, there's nothing to theme.
func (g *gradientWidget) Draw(p painter.Painter, theme *toolkit.Theme) {
	_ = theme
	r := g.Bounds()
	tr, tg, tb := palettes[g.palette][0], palettes[g.palette][1], palettes[g.palette][2]
	for y := 0; y < r.H; y++ {
		// 0..255 across height.
		gy := uint32(y*255) / uint32(max(r.H-1, 1))
		for x := 0; x < r.W; x++ {
			gx := uint32(x*255) / uint32(max(r.W-1, 1))
			// Base gradient: R rises with x, B rises with y, G is their average.
			rr := uint8((gx * uint32(tr)) / 255)
			gg := uint8((((gx + gy) / 2) * uint32(tg)) / 255)
			bb := uint8((gy * uint32(tb)) / 255)
			p.PutPixel(r.X+x, r.Y+y, toolkit.RGBA{R: rr, G: gg, B: bb, A: 0xFF}) //bricolint:allow procedural per-pixel diagonal RGB gradient — a genuine custom render leaf; no stock widget computes this palette-tinted ramp, the toolkit tree already wraps it as a leaf Draw(painter.Painter)
		}
	}
}

// OnEvent advances the palette on any EventClick — the widget covers the
// whole surface and (like the original hand-drawn client) doesn't care
// where within it the click landed.
func (g *gradientWidget) OnEvent(ev toolkit.Event) {
	if ev.Kind != toolkit.EventClick {
		return
	}
	g.nextPalette()
}

// max returns the larger of a, b. (Go 1.21+ has builtin max — kept here for
// older toolchains; the package builds the same.)
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// State is the scene's outer handle: surface size + the widget tree (here
// just the one gradientWidget, playing the "root" role clients/calculator's
// VBox plays).
type State struct {
	W, H int

	theme *toolkit.Theme
	root  *gradientWidget
}

// New makes a State for a surface of width × height pixels with the first
// palette selected.
func New(width, height int) *State {
	enableAAText() // fleet-consistent opt-in; Hello itself paints no text.
	return &State{
		W:     width,
		H:     height,
		theme: toolkit.WhiteSurLight(),
		root:  newGradientWidget(width, height),
	}
}

// NextPalette advances to the next palette, wrapping at the end. Exposed on
// State (delegating to the root widget) so callers/tests can force a
// palette change without going through a synthetic click.
func (s *State) NextPalette() {
	s.root.nextPalette()
}

// Palette reports the root widget's current palette index.
func (s *State) Palette() int {
	return s.root.palette
}

// PaletteCount returns how many palettes the hello client cycles through.
// Exported for tests so they can assert that NextPalette wraps correctly.
func PaletteCount() int { return len(palettes) }

// Render fills buf (a 4*W*H byte slice, RGBA32 row-major) with the scene at
// the current palette: a painter.PixelPainter over buf, then root.Draw. The
// function does not allocate beyond the painter's own zero-alloc primitives;
// buf must be exactly the right size or Render panics (size mismatch in the
// caller is a bug).
func Render(s *State, buf []byte) {
	need := 4 * s.W * s.H
	if len(buf) != need {
		panic("scene: buffer size mismatch")
	}
	p := painter.NewPixelPainter(buf, s.W, s.H)
	s.root.Draw(p, s.theme)
}

// HandleMouse routes a click at surface coordinates (x, y) through the root
// widget (translated into root-local space, exactly like calculator's
// HandleMouse). Returns true when the click advanced the palette, so the
// caller knows to re-render.
func (s *State) HandleMouse(x, y int) bool {
	before := s.root.palette
	rb := s.root.Bounds()
	s.root.OnEvent(toolkit.Event{Kind: toolkit.EventClick, X: x - rb.X, Y: y - rb.Y})
	return s.root.palette != before
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
