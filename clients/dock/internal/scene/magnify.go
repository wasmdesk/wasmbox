// SPDX-License-Identifier: BSD-3-Clause

package scene

// Magnify configures the macOS-style dock magnification: as the cursor hovers
// over the iconbar the launcher under the pointer (and its neighbours, with a
// smooth falloff) swell, and the row re-lays so the swollen icons never
// overlap. The effect itself now lives in the composed toolkit.AppDock (see
// scene.buildDock); this struct is the dock's public knobs, forwarded onto the
// widget every frame.
//
//   - On       toggles the effect. When false the dock lays out flat (every
//     scale is 1), which is also the layout when the cursor is off-surface.
//   - MaxScale is the swell factor of the icon directly under the pointer
//     (1.0 = no swell, 1.6 = 60% larger).
//   - Radius is the falloff reach in resting-icon widths: an icon whose centre
//     is Radius icon-widths from the cursor is back to scale 1. A non-positive
//     Radius or a MaxScale <= 1 disables the swell.
type Magnify struct {
	On       bool
	MaxScale float64
	Radius   float64
}

// DefaultMagnify is the built-in magnification: on, a 1.6x peak, a 1.5-icon
// falloff radius — a lively-but-not-cartoonish bulge that reads clearly at the
// dock's 28px height.
func DefaultMagnify() Magnify {
	return Magnify{On: true, MaxScale: 1.6, Radius: 1.5}
}

// SetMagnify swaps the magnification config. Pure data; the caller decides when
// to repaint (the dock repaints on every hover move while magnification is on).
func (s *State) SetMagnify(m Magnify) { s.Magnify = m }

// LauncherRects returns the current on-screen rectangles of the launcher icons
// (magnified when the cursor hovers, resting otherwise), one [x,y,w,h] per app
// in Apps order. It reads the composed toolkit.AppDock's laid-out geometry so
// paint, hit-testing and this probe hook all share one layout. Exposed so the
// wasm shell can publish the live geometry for headless probes.
func (s *State) LauncherRects() [][4]int {
	rs := s.buildDock().ItemRects()
	out := make([][4]int, len(rs))
	for i, r := range rs {
		out[i] = [4]int{r.X, r.Y, r.W, r.H}
	}
	return out
}
