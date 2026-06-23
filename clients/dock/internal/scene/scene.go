// Package scene paints the wasmdock surface: a macOS-style dock — a
// horizontally-centered, bottom-anchored translucent rounded bar carrying a
// row of app icons, with cursor-driven hover magnification.
//
// It is pure Go (no syscall/js, no cgo) so it builds for any architecture and
// is unit-tested natively. The wasm main only hands it a byte slice to fill
// plus mouse coordinates; all layout, magnification math, hit-testing and RGBA
// painting live here.
//
// Coordinate system matches the wasmbox surface contract: origin top-left,
// RGBA32 row-major, alpha 0xFF = opaque. The dock paints onto a transparent
// surface (alpha 0 outside the bar) so the compositor can show it as a panel.
package scene

// App identifies one launchable application the dock offers. Id is the string
// sent to the compositor in a {type:"launch", app:Id} message; Glyph selects
// the built-in drawn icon (no external assets, no trademarked logos).
type App struct {
	Id    string
	Glyph Glyph
}

// Glyph enumerates the built-in icon drawings.
type Glyph int

const (
	// GlyphTerminal draws a command prompt: a dark panel with a ">" caret and
	// an underscore cursor.
	GlyphTerminal Glyph = iota
	// GlyphEditor draws a document with horizontal "text" lines.
	GlyphEditor
	// GlyphFiles draws a folder shape.
	GlyphFiles
)

// Geometry constants, in surface pixels. These describe the resting (un-
// magnified) dock; magnification scales individual icons up to MaxScale.
const (
	// IconBase is the resting side length of an icon tile.
	IconBase = 48
	// IconGap is the horizontal spacing between adjacent icon centers'
	// tiles at rest (edge to edge).
	IconGap = 16
	// BarPadX is the horizontal padding inside the bar, left of the first
	// icon and right of the last.
	BarPadX = 16
	// BarPadY is the vertical padding inside the bar, above and below the
	// resting icons.
	BarPadY = 10
	// BarMarginBottom is the gap between the bar and the bottom surface edge.
	BarMarginBottom = 8
	// CornerRadius rounds the bar's corners.
	CornerRadius = 18
	// MaxScale is the magnification applied to the icon directly under the
	// cursor; neighbours scale down toward 1.0 with distance.
	MaxScale = 1.8
	// Influence is the falloff radius (in resting icon-pitch units) over
	// which magnification decays back to 1.0.
	Influence = 2.5
)

// State is the dock's mutable model: its surface size, the app row, and the
// current cursor position used to drive magnification. CursorX/Y are surface-
// local; CursorInside reports whether the pointer is currently over the
// surface (when false, the dock rests flat).
type State struct {
	W, H        int
	Apps        []App
	CursorX     int
	CursorY     int
	CursorInside bool
}

// DefaultApps is the built-in set the dock ships with.
func DefaultApps() []App {
	return []App{
		{Id: "terminal", Glyph: GlyphTerminal},
		{Id: "editor", Glyph: GlyphEditor},
		{Id: "files", Glyph: GlyphFiles},
	}
}

// New makes a dock State for a surface of width × height pixels carrying the
// default app set, with the cursor parked outside the surface (flat rest).
func New(width, height int) *State {
	return &State{
		W:    width,
		H:    height,
		Apps: DefaultApps(),
	}
}

// SetCursor records the cursor position and whether it is over the surface.
func (s *State) SetCursor(x, y int, inside bool) {
	s.CursorX = x
	s.CursorY = y
	s.CursorInside = inside
}

// pitch is the resting center-to-center distance between adjacent icons.
func pitch() float64 { return float64(IconBase + IconGap) }

// restRowWidth is the total width of the resting icon row (icon centers span
// (n-1)*pitch, plus a half icon on each end).
func (s *State) restRowWidth() float64 {
	n := len(s.Apps)
	if n == 0 {
		return 0
	}
	return float64(n-1)*pitch() + float64(IconBase)
}

// rowLeft is the surface-x of the left edge of the resting row (the row is
// horizontally centered in the surface).
func (s *State) rowLeft() float64 {
	return (float64(s.W) - s.restRowWidth()) / 2
}

// iconCenterX returns the resting center-x of icon i.
func (s *State) iconCenterX(i int) float64 {
	return s.rowLeft() + float64(IconBase)/2 + float64(i)*pitch()
}

// barTop / barBottom give the vertical extent of the bar (it hugs the bottom
// of the surface above BarMarginBottom). The bar is sized for the resting
// icons plus padding; magnified icons grow upward out of the bar (macOS-like).
func (s *State) barBottom() float64 { return float64(s.H - BarMarginBottom) }
func (s *State) barTop() float64    { return s.barBottom() - float64(IconBase+2*BarPadY) }

// BarRect returns the bar rectangle (x, y, w, h) in surface pixels. Exposed
// for tests and for the wasm glue to size damage rectangles.
func (s *State) BarRect() (x, y, w, h int) {
	left := s.rowLeft() - float64(BarPadX)
	right := s.rowLeft() + s.restRowWidth() + float64(BarPadX)
	top := s.barTop()
	bottom := s.barBottom()
	return int(left), int(top), int(right - left), int(bottom - top)
}

// scaleFor computes the magnification of icon i given the cursor. The falloff
// is a smooth cosine bump centered on the cursor, scaled by distance in pitch
// units and clamped to [1, MaxScale]. When the cursor is outside the surface
// every icon rests at 1.0.
func (s *State) scaleFor(i int) float64 {
	if !s.CursorInside {
		return 1.0
	}
	dx := (float64(s.CursorX) - s.iconCenterX(i)) / pitch()
	if dx < 0 {
		dx = -dx
	}
	if dx >= Influence {
		return 1.0
	}
	// Cosine bump: 1 at dx=0, 0 at dx=Influence.
	bump := 0.5 * (1.0 + cosApprox(pi*dx/Influence))
	return 1.0 + (MaxScale-1.0)*bump
}

// IconRect returns the on-screen rectangle (x, y, w, h) of icon i after
// magnification. Magnified icons are anchored at the bar's bottom inner edge
// and grow upward, and stay centered on their resting center-x.
func (s *State) IconRect(i int) (x, y, w, h int) {
	scale := s.scaleFor(i)
	size := float64(IconBase) * scale
	cx := s.iconCenterX(i)
	left := cx - size/2
	// Bottom-anchor: the icon's bottom sits BarPadY above the bar bottom.
	bottom := s.barBottom() - float64(BarPadY)
	top := bottom - size
	return int(left), int(top), int(size), int(size)
}

// HitTest returns the index of the icon under (x, y) in surface coordinates,
// or -1 if none. It tests against the magnified rectangles, topmost-feeling
// (it simply checks each icon's current rect). Returns the first match.
func (s *State) HitTest(x, y int) int {
	for i := range s.Apps {
		ix, iy, iw, ih := s.IconRect(i)
		if x >= ix && x < ix+iw && y >= iy && y < iy+ih {
			return i
		}
	}
	return -1
}

// ---- painting -------------------------------------------------------------

// RGBA tints used by the dock.
var (
	colTransparent = [4]uint8{0, 0, 0, 0}
	colBar         = [4]uint8{0x1c, 0x1c, 0x22, 0xCC} // translucent dark glass
	colBarEdge     = [4]uint8{0xFF, 0xFF, 0xFF, 0x33} // faint inner highlight
)

// Render fills buf (a 4*W*H byte slice, RGBA32 row-major) with the dock at the
// current cursor/magnification. buf must be exactly the right size or Render
// panics (a size mismatch in the caller is a bug). The area outside the bar is
// left fully transparent so the compositor can treat the surface as a panel.
func Render(s *State, buf []byte) {
	need := 4 * s.W * s.H
	if len(buf) != need {
		panic("scene: buffer size mismatch")
	}
	// Clear to transparent.
	for i := 0; i+3 < len(buf); i += 4 {
		buf[i] = colTransparent[0]
		buf[i+1] = colTransparent[1]
		buf[i+2] = colTransparent[2]
		buf[i+3] = colTransparent[3]
	}
	// Draw the rounded translucent bar.
	bx, by, bw, bh := s.BarRect()
	fillRoundRect(s, buf, bx, by, bw, bh, CornerRadius, colBar)
	// Faint top highlight line just inside the bar's top edge.
	for x := bx + CornerRadius; x < bx+bw-CornerRadius; x++ {
		blend(s, buf, x, by+1, colBarEdge)
	}
	// Draw each icon (magnified).
	for i := range s.Apps {
		ix, iy, iw, ih := s.IconRect(i)
		drawIcon(s, buf, s.Apps[i].Glyph, ix, iy, iw, ih)
	}
}

// setPixel writes an opaque-or-given RGBA at (x,y) if in bounds.
func setPixel(s *State, buf []byte, x, y int, c [4]uint8) {
	if x < 0 || y < 0 || x >= s.W || y >= s.H {
		return
	}
	off := (y*s.W + x) * 4
	buf[off] = c[0]
	buf[off+1] = c[1]
	buf[off+2] = c[2]
	buf[off+3] = c[3]
}

// blend alpha-composites c over the existing pixel at (x,y) (src-over).
func blend(s *State, buf []byte, x, y int, c [4]uint8) {
	if x < 0 || y < 0 || x >= s.W || y >= s.H {
		return
	}
	off := (y*s.W + x) * 4
	sa := uint32(c[3])
	if sa == 0 {
		return
	}
	if sa == 255 {
		buf[off] = c[0]
		buf[off+1] = c[1]
		buf[off+2] = c[2]
		buf[off+3] = 0xFF
		return
	}
	ia := 255 - sa
	for k := 0; k < 3; k++ {
		buf[off+k] = uint8((uint32(c[k])*sa + uint32(buf[off+k])*ia) / 255)
	}
	da := uint32(buf[off+3])
	buf[off+3] = uint8(sa + da*ia/255)
}

// fillRoundRect blends a rounded rectangle of color c into buf.
func fillRoundRect(s *State, buf []byte, x, y, w, h, r int, c [4]uint8) {
	if w <= 0 || h <= 0 {
		return
	}
	if r*2 > w {
		r = w / 2
	}
	if r*2 > h {
		r = h / 2
	}
	for yy := 0; yy < h; yy++ {
		for xx := 0; xx < w; xx++ {
			if inRoundRect(xx, yy, w, h, r) {
				blend(s, buf, x+xx, y+yy, c)
			}
		}
	}
}

// inRoundRect reports whether (xx,yy) is inside a w×h rounded rect of radius r.
func inRoundRect(xx, yy, w, h, r int) bool {
	// Corner centers.
	if xx >= r && xx < w-r {
		return true
	}
	if yy >= r && yy < h-r {
		return true
	}
	var cx, cy int
	switch {
	case xx < r && yy < r:
		cx, cy = r, r
	case xx >= w-r && yy < r:
		cx, cy = w-r-1, r
	case xx < r && yy >= h-r:
		cx, cy = r, h-r-1
	default:
		cx, cy = w-r-1, h-r-1
	}
	dx := xx - cx
	dy := yy - cy
	return dx*dx+dy*dy <= r*r
}

// drawIcon renders a glyph filling the (x,y,w,h) tile. Each glyph is a rounded
// tinted tile with a simple drawn mark on top, so icons read distinctly
// without external assets.
func drawIcon(s *State, buf []byte, g Glyph, x, y, w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	var tile [4]uint8
	switch g {
	case GlyphTerminal:
		tile = [4]uint8{0x22, 0x26, 0x2e, 0xFF}
	case GlyphEditor:
		tile = [4]uint8{0x2b, 0x5c, 0xcc, 0xFF}
	case GlyphFiles:
		tile = [4]uint8{0xe0, 0xa8, 0x3c, 0xFF}
	default:
		tile = [4]uint8{0x88, 0x88, 0x88, 0xFF}
	}
	// Rounded tile background.
	tr := w / 5
	fillRoundRect(s, buf, x, y, w, h, tr, tile)
	// Glyph mark in a contrasting ink.
	ink := [4]uint8{0xFF, 0xFF, 0xFF, 0xFF}
	switch g {
	case GlyphTerminal:
		drawTerminalMark(s, buf, x, y, w, h, ink)
	case GlyphEditor:
		drawEditorMark(s, buf, x, y, w, h, ink)
	case GlyphFiles:
		drawFilesMark(s, buf, x, y, w, h, [4]uint8{0x5a, 0x3e, 0x10, 0xFF})
	}
}

// drawTerminalMark paints a ">" caret and a cursor underscore.
func drawTerminalMark(s *State, buf []byte, x, y, w, h int, ink [4]uint8) {
	// ">" chevron: two diagonal strokes meeting at the right.
	cx := x + w*2/5
	cy := y + h*2/5
	arm := h / 6
	for t := 0; t <= arm; t++ {
		setPixel(s, buf, cx-arm+t, cy-arm+t, ink)
		setPixel(s, buf, cx-arm+t, cy+arm-t, ink)
		// thicken
		setPixel(s, buf, cx-arm+t, cy-arm+t+1, ink)
		setPixel(s, buf, cx-arm+t, cy+arm-t+1, ink)
	}
	// Underscore cursor near the bottom-left.
	uy := y + h*2/3
	for ux := x + w/4; ux < x+w*3/5; ux++ {
		setPixel(s, buf, ux, uy, ink)
		setPixel(s, buf, ux, uy+1, ink)
	}
}

// drawEditorMark paints a document with text lines.
func drawEditorMark(s *State, buf []byte, x, y, w, h int, ink [4]uint8) {
	// Three horizontal "text" lines, indented.
	x0 := x + w/4
	x1 := x + w*3/4
	for i := 0; i < 3; i++ {
		ly := y + h/3 + i*h/6
		end := x1
		if i == 2 {
			end = x + w*3/5 // last line shorter
		}
		for lx := x0; lx < end; lx++ {
			setPixel(s, buf, lx, ly, ink)
		}
	}
}

// drawFilesMark paints a folder silhouette.
func drawFilesMark(s *State, buf []byte, x, y, w, h int, ink [4]uint8) {
	// Folder body.
	fx0 := x + w/5
	fx1 := x + w*4/5
	fy0 := y + h*2/5
	fy1 := y + h*7/10
	for fy := fy0; fy <= fy1; fy++ {
		for fx := fx0; fx <= fx1; fx++ {
			setPixel(s, buf, fx, fy, ink)
		}
	}
	// Tab on the top-left.
	for fy := fy0 - h/8; fy < fy0; fy++ {
		for fx := fx0; fx <= fx0+(fx1-fx0)/2; fx++ {
			setPixel(s, buf, fx, fy, ink)
		}
	}
}

// ---- small math helpers (no math import, to keep the package dependency-free
// and trivially portable) -------------------------------------------------

const pi = 3.14159265358979323846

// cosApprox returns cos(x) for x in [0, pi] using a stable polynomial. The
// dock only ever feeds it values in that range (pi*dx/Influence with
// 0 <= dx < Influence). Accuracy here is purely cosmetic.
func cosApprox(x float64) float64 {
	// Reduce to [0, pi] is guaranteed by the caller; use the Bhaskara I
	// sine approximation via cos(x) = sin(pi/2 - x), kept simple.
	// Use a 4-term Taylor-ish series adequate for [0, pi].
	x2 := x * x
	// cos(x) ≈ 1 - x^2/2 + x^4/24 - x^6/720 + x^8/40320
	return 1 - x2/2 + x2*x2/24 - x2*x2*x2/720 + x2*x2*x2*x2/40320
}
