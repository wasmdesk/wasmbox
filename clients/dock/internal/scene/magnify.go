// SPDX-License-Identifier: BSD-3-Clause

package scene

// Magnify configures the macOS-style dock magnification: as the cursor hovers
// over the iconbar the button under the pointer (and its neighbours, with a
// smooth falloff) swell, and the row re-lays so the swollen buttons never
// overlap. The effect is owned by the toolkit.AppDock the iconbar is composed
// from (see applyItems); this struct is the dock's public knob set, mapped
// onto the AppDock's Magnify / MaxScale / Radius fields.
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

// SetMagnify swaps the magnification config and republishes the iconbar so the
// persistent AppDock picks up the new swell knobs. The caller decides when to
// repaint (the dock repaints on every hover move while magnification is on).
func (s *State) SetMagnify(m Magnify) {
	s.Magnify = m
	s.publishItems()
}

// LauncherRects returns the current on-screen rectangles of the launcher
// buttons (magnified when the cursor hovers, resting otherwise), one [x,y,w,h]
// per app in Apps order — the AppDock item rects for the first len(Apps) items.
// Exposed so the wasm shell can publish the live geometry for headless probes
// and so hit-testing + paint share one source.
func (s *State) LauncherRects() [][4]int {
	rects := s.dock.ItemRects()
	out := make([][4]int, 0, len(s.Apps))
	for i := 0; i < len(s.Apps) && i < len(rects); i++ {
		r := rects[i]
		out = append(out, [4]int{r.X, r.Y, r.W, r.H})
	}
	return out
}

// WindowRects returns the current on-screen rectangles of the open-window task
// buttons (magnified when hovering, resting otherwise), one [x,y,w,h] per entry
// in Windows order — the AppDock item rects past the launcher row. The
// __wasmdockGeometry probe hook reads this so a headless test clicks the button
// where it is actually painted, magnification included.
func (s *State) WindowRects() [][4]int {
	rects := s.dock.ItemRects()
	out := make([][4]int, 0, len(s.Windows))
	for i := len(s.Apps); i < len(rects); i++ {
		r := rects[i]
		out = append(out, [4]int{r.X, r.Y, r.W, r.H})
	}
	return out
}
