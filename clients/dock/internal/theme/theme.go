// SPDX-License-Identifier: BSD-3-Clause
//
// Package theme encodes the Openbox-style theme attribute tree the wasmdock
// toolbar honours when painting itself. The attribute names mirror the
// Openbox theming reference (https://openbox.org/help/Themes_fr) so a future
// theme loader can translate an `*.themerc` / `*.obt` file straight into a
// Theme value: every attribute Openbox documents that is meaningful for a
// Fluxbox-style toolbar gets a field here under the same hierarchy.
//
// The package is pure Go (no syscall/js, no cgo) so it builds for any
// architecture and is unit-tested natively. The wasm scene only consumes
// Theme values + the gradient renderer; it never reaches back into JS land.
package theme

// GradientType enumerates the gradient strategies Openbox supports for any
// "*.bg" attribute. We honour `flat`, `vertical` and `horizontal` in v0
// (the bevel variants short-circuit to flat). The set + names match the
// Openbox reference so a future themerc parser can map identifiers 1:1.
type GradientType int

const (
	// GradientFlat paints a solid colour (c1; c2 is ignored).
	GradientFlat GradientType = iota
	// GradientVertical interpolates linearly from c1 at the top row to c2 at
	// the bottom row.
	GradientVertical
	// GradientHorizontal interpolates linearly from c1 at the left column to
	// c2 at the right column.
	GradientHorizontal
	// GradientDiagonal interpolates along the top-left -> bottom-right axis.
	GradientDiagonal
	// GradientCrossDiagonal interpolates along the top-right -> bottom-left
	// axis.
	GradientCrossDiagonal
	// GradientPipeCross is recorded for Openbox themerc compatibility; v0
	// renders it as a flat c1 fill.
	GradientPipeCross
	// GradientRectangle is recorded for Openbox themerc compatibility; v0
	// renders it as a flat c1 fill.
	GradientRectangle
	// GradientPyramid is recorded for Openbox themerc compatibility; v0
	// renders it as a flat c1 fill.
	GradientPyramid
	// GradientRaisedBevel paints a flat c1 fill (the scene draws an explicit
	// 1-pixel raised bevel around the section on top of the fill).
	GradientRaisedBevel
	// GradientSunkenBevel paints a flat c1 fill (the scene draws an explicit
	// 1-pixel sunken bevel around the section on top of the fill).
	GradientSunkenBevel
	// GradientParentRelative is recorded for Openbox themerc compatibility;
	// v0 renders it as a flat c1 fill.
	GradientParentRelative
)

// Color is an RGB triple, 0..255 per channel. Stored as three bytes (matching
// the Openbox `#RRGGBB` literal); alpha is implicit 0xFF (toolbar pixels are
// always opaque).
type Color [3]uint8

// MustHex parses a `#RRGGBB` Openbox-style colour literal and panics on a
// malformed input. The toolbar embeds known-good literals so a parse failure
// is a programming error, not a runtime condition.
func MustHex(s string) Color {
	if len(s) != 7 || s[0] != '#' {
		panic("theme: malformed colour literal " + s)
	}
	var c Color
	for i := 0; i < 3; i++ {
		hi, ok1 := hexNibble(s[1+2*i])
		lo, ok2 := hexNibble(s[2+2*i])
		if !ok1 || !ok2 {
			panic("theme: malformed colour literal " + s)
		}
		c[i] = hi<<4 | lo
	}
	return c
}

// hexNibble returns the 0..15 value of a single hex digit (case-insensitive),
// and whether the byte was a valid hex digit at all.
func hexNibble(b byte) (uint8, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	}
	return 0, false
}

// Border models the Openbox `border.color` + `border.width` attributes.
type Border struct {
	// Color matches `border.color`.
	Color Color
	// Width matches `border.width` (pixels).
	Width int
}

// Padding models the Openbox `padding.width` + `padding.height` attributes
// (inner spacing between a section's border and its content, in pixels).
type Padding struct {
	// Width matches `padding.width`.
	Width int
	// Height matches `padding.height`.
	Height int
}

// Font models the Openbox `*.text.font` attribute. The wasmdock bitmap font
// is fixed-size so Size is recorded for theme-file round-tripping but
// ignored at paint time.
type Font struct {
	// Face matches the `*.text.font` family field (e.g. "sans").
	Face string
	// Size matches the `*.text.font` size field (e.g. 9).
	Size int
}

// Bg models a `*.bg` attribute group: a gradient declaration with up to two
// colour stops. Openbox spells the stops as `*.bg.color` (c1, the start)
// and `*.bg.colorTo` (c2, the end) when the gradient is non-flat.
type Bg struct {
	// Gradient matches the `*.bg` value (gradient strategy).
	Gradient GradientType
	// Color matches `*.bg.color`.
	Color Color
	// ColorTo matches `*.bg.colorTo`.
	ColorTo Color
}

// Text models a `*.text` attribute group: ink colour + font face.
type Text struct {
	// Color matches `*.text.color` (label/text ink).
	Color Color
	// Font matches `*.text.font`.
	Font Font
}

// TitleSection models the `window.*.title.*` attribute group: a background
// (gradient + colours) plus a label-text ink. Openbox factors this once per
// state (active / inactive); we mirror that.
type TitleSection struct {
	// Bg matches `window.*.title.bg.*`.
	Bg Bg
	// Label matches `window.*.label.text.*`.
	Label Text
}

// WindowState models a single window state (active or inactive). The toolbar
// reuses the title-section attributes as the look of its individual sections
// (workspace / iconbar buttons / clock).
type WindowState struct {
	// Title matches `window.<state>.title.*` + `window.<state>.label.*`.
	Title TitleSection
}

// WindowTheme groups the active / inactive window states. The toolbar uses
// the active state for the "selected" button (none in v0 — all buttons are
// inactive launchers) and the inactive state as the fallback look.
type WindowTheme struct {
	// Active matches `window.active.*`.
	Active WindowState
	// Inactive matches `window.inactive.*`.
	Inactive WindowState
}

// MenuTheme models the Openbox `menu.*` attribute group. The toolbar does
// not pop a menu in v0 but the fields exist so an iconbar button hover/click
// could pop one without re-touching the schema later.
type MenuTheme struct {
	// Title matches `menu.title.*`.
	Title TitleSection
	// Items matches `menu.items.*`.
	Items struct {
		// Bg matches `menu.items.bg.*`.
		Bg Bg
		// Text matches `menu.items.text.*`.
		Text Text
	}
}

// OsdTheme models the Openbox `osd.*` attribute group (on-screen display
// — the dock currently uses the OSD ink as its clock-text colour).
type OsdTheme struct {
	// Bg matches `osd.bg.*`.
	Bg Bg
	// Label matches `osd.label.text.*`.
	Label Text
}

// Theme is the full Openbox-compatible attribute tree the toolbar honours.
type Theme struct {
	// Border matches `border.*`.
	Border Border
	// Padding matches `padding.*`.
	Padding Padding
	// Window matches `window.*`.
	Window WindowTheme
	// Menu matches `menu.*`.
	Menu MenuTheme
	// Osd matches `osd.*`.
	Osd OsdTheme
}

// DefaultFluxboxLight returns the bevel-gray Fluxbox-classic look the dock
// ships with: light-gray vertical-gradient title backgrounds, near-black ink,
// 1-pixel mid-gray border, 1-pixel padding. All colours are spelled as
// Openbox-style `#RRGGBB` literals so the values round-trip cleanly to / from
// an Openbox theme file.
func DefaultFluxboxLight() Theme {
	c8 := MustHex("#c8c8c8") // light bevel face
	c9 := MustHex("#909090") // mid bevel face
	cd := MustHex("#d0d0d0") // OSD face
	c4 := MustHex("#4a4a4a") // dark border
	c1 := MustHex("#1a1a1a") // active label ink
	c2 := MustHex("#202020") // inactive label ink
	cf := Font{Face: "sans", Size: 9}
	t := Theme{
		Border: Border{Color: c4, Width: 1},
		Padding: Padding{Width: 1, Height: 1},
	}
	t.Window.Active.Title.Bg = Bg{Gradient: GradientVertical, Color: c8, ColorTo: c9}
	t.Window.Active.Title.Label = Text{Color: c1, Font: cf}
	t.Window.Inactive.Title.Bg = Bg{Gradient: GradientVertical, Color: c9, ColorTo: c9}
	t.Window.Inactive.Title.Label = Text{Color: c2, Font: cf}
	t.Menu.Title.Bg = Bg{Gradient: GradientFlat, Color: c8}
	t.Menu.Title.Label = Text{Color: c1, Font: cf}
	t.Menu.Items.Bg = Bg{Gradient: GradientFlat, Color: cd}
	t.Menu.Items.Text = Text{Color: c1, Font: cf}
	t.Osd.Bg = Bg{Gradient: GradientFlat, Color: cd}
	t.Osd.Label = Text{Color: c1, Font: cf}
	return t
}

// PaintGradient fills the rect (rx, ry, rw, rh) inside an RGBA32 row-major
// buffer of the given total surface size (sw, sh) with a gradient of type g
// from c1 to c2. Out-of-buffer pixels are clipped. Alpha is forced to 0xFF
// (toolbar sections are opaque).
//
// Honoured gradient types in v0: flat / vertical / horizontal / diagonal /
// crossdiagonal. Every other Openbox-named type short-circuits to a flat c1
// fill so a themerc value is never rejected.
func PaintGradient(buf []byte, sw, sh, rx, ry, rw, rh int, g GradientType, c1, c2 Color) {
	if rw <= 0 || rh <= 0 {
		return
	}
	for j := 0; j < rh; j++ {
		y := ry + j
		if y < 0 || y >= sh {
			continue
		}
		for i := 0; i < rw; i++ {
			x := rx + i
			if x < 0 || x >= sw {
				continue
			}
			c := interp(g, i, j, rw, rh, c1, c2)
			off := (y*sw + x) * 4
			buf[off+0] = c[0]
			buf[off+1] = c[1]
			buf[off+2] = c[2]
			buf[off+3] = 0xFF
		}
	}
}

// interp resolves the colour at (i, j) inside an rw x rh section under the
// given gradient. The math is per-channel linear lerp between c1 and c2; for
// the bevel variants we return c1 (the scene draws the bevel highlights as
// 1-pixel strokes outside this fill).
func interp(g GradientType, i, j, rw, rh int, c1, c2 Color) Color {
	switch g {
	case GradientVertical:
		return lerp(c1, c2, j, rh-1)
	case GradientHorizontal:
		return lerp(c1, c2, i, rw-1)
	case GradientDiagonal:
		return lerp(c1, c2, i+j, (rw-1)+(rh-1))
	case GradientCrossDiagonal:
		return lerp(c1, c2, (rw-1-i)+j, (rw-1)+(rh-1))
	default:
		// Flat + bevel + recorded-only variants: solid c1.
		return c1
	}
}

// lerp interpolates each RGB channel linearly between c1 (at step=0) and c2
// (at step=denom). A non-positive denom collapses to c1 (1-pixel section).
func lerp(c1, c2 Color, step, denom int) Color {
	if denom <= 0 {
		return c1
	}
	if step < 0 {
		step = 0
	}
	if step > denom {
		step = denom
	}
	var out Color
	for k := 0; k < 3; k++ {
		a := int(c1[k])
		b := int(c2[k])
		out[k] = uint8(a + (b-a)*step/denom)
	}
	return out
}
