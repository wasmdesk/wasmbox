// SPDX-License-Identifier: BSD-3-Clause

package scene

import "math"

// Magnify configures the macOS-style dock magnification: as the cursor hovers
// over the iconbar the button under the pointer (and its neighbours, with a
// smooth falloff) swell, and the row re-lays so the swollen buttons never
// overlap. It is a pure paint + layout effect — the resting geometry
// (IconbarButtonRect / WindowButtonRect) is untouched, so a click maps through
// the SAME magnified rectangles the user sees (HitTest / HitTestWindow /
// LauncherRects / WindowRects all read the laid-out slots).
//
//   - On       toggles the effect. When false the iconbar lays out flat (every
//     scale is 1), which is also the layout when the cursor is off-surface.
//   - MaxScale is the swell factor of the button directly under the pointer
//     (1.0 = no swell, 1.6 = 60% larger). Clamped to >= 1.
//   - Radius is the falloff reach in resting-button widths: a button whose
//     centre is Radius*IconbarButtonW pixels from the cursor is back to scale 1.
//     A non-positive Radius disables the effect (no neighbour would ever swell).
type Magnify struct {
	On       bool
	MaxScale float64
	Radius   float64
}

// DefaultMagnify is the built-in magnification: on, a 1.6x peak, a 1.5-button
// falloff radius — a lively-but-not-cartoonish bulge that reads clearly at the
// dock's 28px height.
func DefaultMagnify() Magnify {
	return Magnify{On: true, MaxScale: 1.6, Radius: 1.5}
}

// SetMagnify swaps the magnification config. Pure data; the caller decides when
// to repaint (the dock repaints on every hover move while magnification is on).
func (s *State) SetMagnify(m Magnify) { s.Magnify = m }

// slot is one laid-out iconbar entry — a launcher (isWindow=false, idx into
// Apps) or an open-window button (isWindow=true, idx into Windows) — carrying
// its on-screen rectangle and the swell factor applied to it. When
// magnification is inactive the rectangle equals the resting geometry and scale
// is 1.
type slot struct {
	isWindow bool
	idx      int
	x, y, w, h int
	scale      float64
}

// restingSlots returns every iconbar entry at its resting geometry (scale 1):
// the launcher row first, then the open-window row, mirroring the paint order.
func (s *State) restingSlots() []slot {
	out := make([]slot, 0, len(s.Apps)+len(s.Windows))
	for i := range s.Apps {
		x, y, w, h := s.IconbarButtonRect(i)
		out = append(out, slot{idx: i, x: x, y: y, w: w, h: h, scale: 1})
	}
	for i := range s.Windows {
		x, y, w, h := s.WindowButtonRect(i)
		out = append(out, slot{isWindow: true, idx: i, x: x, y: y, w: w, h: h, scale: 1})
	}
	return out
}

// magnificationActive reports whether the next layout should swell the icons:
// the effect is enabled, the cursor is over the surface, and the falloff radius
// is positive (a zero/negative radius can never reach any button).
func (s *State) magnificationActive() bool {
	return s.Magnify.On && s.CursorInside && s.Magnify.Radius > 0 && s.Magnify.MaxScale > 1
}

// laidOutSlots returns the iconbar entries as they should paint + hit-test right
// now. With magnification inactive that is exactly restingSlots (so every
// resting-geometry test and the flat-layout render path are byte-identical).
// With it active each slot's scale is a raised-cosine falloff of the cursor's
// horizontal distance to the slot centre, widths and heights swell by that
// scale, and the row reflows left-to-right — the original inter-slot gaps
// (including the launcher/window SeparatorW) preserved — so swollen buttons
// push their neighbours aside instead of overlapping. The row stays anchored at
// the first slot's resting left edge and buttons stay bottom-aligned to the
// bar, which reads as the dock bulging under the pointer.
func (s *State) laidOutSlots() []slot {
	base := s.restingSlots()
	if !s.magnificationActive() || len(base) == 0 {
		return base
	}
	maxScale := s.Magnify.MaxScale
	radiusPx := s.Magnify.Radius * float64(IconbarButtonW)

	n := len(base)
	origX := make([]int, n)
	origW := make([]int, n)
	for i := range base {
		origX[i] = base[i].x
		origW[i] = base[i].w
		center := float64(origX[i]) + float64(origW[i])/2
		u := math.Abs(float64(s.CursorX)-center) / radiusPx
		base[i].scale = 1 + (maxScale-1)*bump(u)
	}

	cur := origX[0]
	for i := range base {
		// origW is IconbarButtonW (>=1) and scale is in [1, MaxScale], so the
		// swollen width is always >= the resting width — no floor needed.
		nw := int(math.Round(float64(origW[i]) * base[i].scale))
		// Height swells by the same factor, but the button stays pinned to the
		// bar's bottom padding line and its top never rises above the toolbar's
		// 1px border row (y >= 1). At the dock's short height the clamp makes
		// the swell read mostly as width — the natural bottom-bar dock look.
		origH := base[i].h
		bottom := base[i].y + origH
		nh := int(math.Round(float64(origH) * base[i].scale))
		ny := bottom - nh
		if ny < 1 {
			ny = 1
			nh = bottom - ny
		}
		base[i].x = cur
		base[i].w = nw
		base[i].y = ny
		base[i].h = nh
		if i < n-1 {
			gap := origX[i+1] - (origX[i] + origW[i])
			cur = cur + nw + gap
		}
	}

	// Cursor-anchor the reflowed row: shift it so the point under the pointer
	// stays put, giving the macOS feel where the hovered icon swells in place
	// instead of sliding out from under the cursor. Anchor on the slot the
	// cursor rests over (in resting coordinates), mapping the same fractional
	// position into its swollen width; a cursor in an inter-slot gap leaves the
	// row left-anchored (the gaps are a couple of pixels — no visible drift).
	for i := range base {
		if s.CursorX >= origX[i] && s.CursorX < origX[i]+origW[i] {
			frac := (float64(s.CursorX) - float64(origX[i])) / float64(origW[i])
			newPointX := float64(base[i].x) + frac*float64(base[i].w)
			shift := int(math.Round(float64(s.CursorX) - newPointX))
			if shift != 0 {
				for j := range base {
					base[j].x += shift
				}
			}
			break
		}
	}
	return base
}

// bump is the raised-cosine magnification falloff: 1 at the cursor (u=0),
// smoothly easing to 0 at the falloff edge (u>=1). The cosine shoulder gives
// the swell the soft macOS genie feel rather than a linear tent.
func bump(u float64) float64 {
	if u <= 0 {
		return 1
	}
	if u >= 1 {
		return 0
	}
	return 0.5 * (1 + math.Cos(math.Pi*u))
}

// LauncherRects returns the current on-screen rectangles of the launcher
// buttons (magnified when the cursor hovers, resting otherwise), one [x,y,w,h]
// per app in Apps order. Exposed so the wasm shell can publish the live
// geometry for headless probes and so hit-testing + paint share one source.
func (s *State) LauncherRects() [][4]int {
	var out [][4]int
	for _, sl := range s.laidOutSlots() {
		if sl.isWindow {
			continue
		}
		out = append(out, [4]int{sl.x, sl.y, sl.w, sl.h})
	}
	return out
}

// WindowRects returns the current on-screen rectangles of the open-window
// buttons (magnified when hovering, resting otherwise), one [x,y,w,h] per entry
// in Windows order. The __wasmdockGeometry probe hook reads this so a headless
// test clicks the button where it is actually painted, magnification included.
func (s *State) WindowRects() [][4]int {
	var out [][4]int
	for _, sl := range s.laidOutSlots() {
		if !sl.isWindow {
			continue
		}
		out = append(out, [4]int{sl.x, sl.y, sl.w, sl.h})
	}
	return out
}
