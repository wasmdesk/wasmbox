// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"strings"

	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
)

// Running / active indicators + attention badges.
//
// A launcher whose app has at least one open window carries a small "running"
// dot centred under its glyph (the macOS/Unity dock convention); the launcher
// of the currently-focused window additionally gets a brighter, wider active
// underline. An app can also request an attention badge — a count or alert
// pill drawn in the launcher's top-right corner via the toolkit's Badge widget
// — through SetBadge; this is the placeholder API a client uses until a first-
// class "badge" dock wire message lands (the dock would set it from that
// message's payload exactly the way SetBadge does).

// appIndexForWindow maps an open window to the launcher index it belongs to, or
// -1 if none matches. The match is by the window's explicit App id when the
// compositor supplies one (Window.App, forward-compatible — the field rides the
// windows_changed payload the moment the compositor starts sending it), else by
// a case-insensitive match of the window Title against a launcher's Label or Id
// (the dom-app launch descriptors title their windows after the launcher, e.g.
// "VS Code", "Terminal", so the fallback lights the right dot today).
func (s *State) appIndexForWindow(w Window) int {
	if app := strings.TrimSpace(w.App); app != "" {
		for i := range s.Apps {
			if strings.EqualFold(s.Apps[i].Id, app) {
				return i
			}
		}
		return -1
	}
	title := strings.TrimSpace(w.Title)
	if title == "" {
		return -1
	}
	for i := range s.Apps {
		if strings.EqualFold(s.Apps[i].Label, title) || strings.EqualFold(s.Apps[i].Id, title) {
			return i
		}
	}
	return -1
}

// launcherRunning reports, per launcher index, whether at least one open (or
// folded) window maps to it — i.e. whether it should carry a running dot.
func (s *State) launcherRunning() []bool {
	out := make([]bool, len(s.Apps))
	for _, w := range s.Windows {
		if i := s.appIndexForWindow(w); i >= 0 {
			out[i] = true
		}
	}
	return out
}

// focusedLauncher returns the launcher index of the currently-focused window,
// or -1 if no focused window maps to a launcher. Its launcher gets the brighter
// active underline on top of the running dot.
func (s *State) focusedLauncher() int {
	for _, w := range s.Windows {
		if w.Focused {
			return s.appIndexForWindow(w)
		}
	}
	return -1
}

// SetBadge sets (count > 0) or clears (count <= 0) the attention badge on the
// launcher whose Id is app. The badge shows the count, capped at "99+" so the
// pill stays compact. Unknown app ids are ignored. Placeholder attention API:
// a client requests a badge through the dock, which calls this and repaints.
func (s *State) SetBadge(app string, count int) {
	if s.badges == nil {
		s.badges = map[string]int{}
	}
	if count <= 0 {
		delete(s.badges, app)
		return
	}
	s.badges[app] = count
}

// BadgeCount returns the attention-badge count for the launcher app id (0 when
// none). Exposed so the wasm shell + tests can read back what SetBadge stored.
func (s *State) BadgeCount(app string) int {
	if s.badges == nil {
		return 0
	}
	return s.badges[app]
}

// badgeText formats a badge count as its pill label, capping large counts at
// "99+" so a runaway counter cannot widen the pill past its corner.
func badgeText(n int) string {
	if n <= 0 {
		return ""
	}
	if n > 99 {
		return "99+"
	}
	return itoa(n)
}

// RunningDotPx is the diameter of the "app is running" dot drawn under a
// launcher glyph.
const RunningDotPx = 3

// drawRunningIndicator paints the running dot (and, when focused, the wider
// active underline) along the bottom edge of a launcher slot rectangle r. The
// dot is centred under the glyph; the active underline is a brighter, wider bar
// just below it. Colours come from the active theme's title inks so the marks
// read against the bar in any theme.
func (s *State) drawRunningIndicator(p painter.Painter, r toolkit.Rect, focused bool) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	cx := r.X + r.W/2
	by := r.Y + r.H - RunningDotPx - 1
	if by < r.Y {
		by = r.Y
	}
	dot := rgba(s.Theme.Window.Active.Title.Label.Color)
	for j := 0; j < RunningDotPx; j++ {
		for i := 0; i < RunningDotPx; i++ {
			p.PutPixel(cx-RunningDotPx/2+i, by+j, dot)
		}
	}
	if focused {
		// Brighter, wider underline spanning most of the button width.
		hl := rgba(s.Theme.Window.Active.Title.Bg.Color)
		uy := r.Y + r.H - 1
		half := r.W / 3
		for i := -half; i <= half; i++ {
			p.PutPixel(cx+i, uy, hl)
		}
	}
}

// drawBadge paints an attention badge pill in the top-right corner of a
// launcher slot rectangle r via the toolkit Badge widget, or is a no-op when
// count is zero. The pill hugs the corner so it overhangs the glyph the way a
// notification count does; its width auto-sizes to the digit count.
func drawBadge(p painter.Painter, r toolkit.Rect, count int) {
	txt := badgeText(count)
	if txt == "" || r.W <= 0 || r.H <= 0 {
		return
	}
	b := toolkit.NewBadge(txt)
	// Auto-size to the text, then anchor the pill's top-right at the slot's
	// top-right corner (a 1px inset so the border stays visible).
	b.SetBounds(toolkit.Rect{X: 0, Y: 0, W: 0, H: 0})
	b.Draw(p, badgeTheme) // first Draw sizes it; we discard this placement
	bw := b.Bounds().W
	bh := b.Bounds().H
	bx := r.X + r.W - bw - 1
	by := r.Y + 1
	if bx < r.X {
		bx = r.X
	}
	b.SetBounds(toolkit.Rect{X: bx, Y: by, W: bw, H: bh})
	b.Draw(p, badgeTheme)
}

// badgeTheme supplies the Badge widget's fallback pill + ink colours (a red
// alert fill with white text) so an attention badge reads as a notification
// count regardless of the dock's active Openbox theme.
var badgeTheme = func() *toolkit.Theme {
	th := toolkit.DefaultLight()
	th.Accent = toolkit.RGB(0xE0, 0x1B, 0x24)     // alert red pill
	th.Background = toolkit.RGB(0xFF, 0xFF, 0xFF)  // white ink
	return th
}()
