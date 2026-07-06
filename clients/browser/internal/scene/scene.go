// SPDX-License-Identifier: BSD-3-Clause
//
// Package scene renders the wasmdesk web browser in the WhiteSur / Safari
// style: a #ebebeb toolbar (back / forward buttons + a rounded address bar +
// a new-tab button) above a content area. The page runs under
// COEP:require-corp, so live cross-origin sites cannot load inside the wasmbox
// sandbox; the browser therefore shows a Safari-style "Favourites" start page
// of bookmark tiles that navigate to local placeholder pages. It validates a
// toolbar + address-bar + tile-grid composition and a back/forward history
// model.

package scene

import (
	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
)

// link is one favourite: a display name + the URL shown in the address bar.
type link struct {
	name string
	url  string
}

// State holds the toolbar geometry, the favourites, and the navigation model.
type State struct {
	W, H  int
	theme *toolkit.Theme
	favs  []link

	// Navigation is one level deep: onSite=false shows the Favourites start
	// page; onSite=true shows the placeholder page for favs[cur]. visited is
	// set once any site has been opened, so Forward can re-open the last one
	// after Back returns to the start page (Safari-style).
	onSite  bool
	cur     int
	visited bool

	backRect, fwdRect, addrRect, addRect toolkit.Rect
	tileRects                            []toolkit.Rect
}

// Layout constants (pixels).
const (
	toolbarH = 46
	btnW     = 30
	btnH     = 28
	btnLeft  = 12
	btnGap   = 6
	addrH    = 28
	tileCols = 4
	tileW    = 150
	tileH    = 98
	tileGapX = 22
	tileGapY = 26
	gridTop  = toolbarH + 56
	gridLeft = 40
)

// New builds the browser sized W×H.
func New(w, h int) *State {
	s := &State{W: w, H: h, theme: toolkit.WhiteSurLight()}
	s.favs = []link{
		{"weft", "weft.dev"},
		{"claimward", "claimward.io"},
		{"go-quake1", "go-quake1.dev"},
		{"go-widgets", "go-widgets.dev"},
		{"wasmdesk", "wasmdesk.org"},
		{"GitHub", "github.com"},
		{"Docs", "docs.wasmdesk.org"},
		{"Wiki", "wiki.wasmdesk.org"},
	}
	// Toolbar hit rects.
	by := (toolbarH - btnH) / 2
	s.backRect = toolkit.Rect{X: btnLeft, Y: by, W: btnW, H: btnH}
	s.fwdRect = toolkit.Rect{X: btnLeft + btnW + btnGap, Y: by, W: btnW, H: btnH}
	s.addRect = toolkit.Rect{X: w - btnLeft - btnW, Y: by, W: btnW, H: btnH}
	addrX := s.fwdRect.X + btnW + 14
	s.addrRect = toolkit.Rect{X: addrX, Y: (toolbarH - addrH) / 2, W: s.addRect.X - 14 - addrX, H: addrH}
	// Favourite tile rects.
	s.tileRects = make([]toolkit.Rect, len(s.favs))
	for i := range s.favs {
		col := i % tileCols
		rowi := i / tileCols
		s.tileRects[i] = toolkit.Rect{
			X: gridLeft + col*(tileW+tileGapX),
			Y: gridTop + rowi*(tileH+tileGapY),
			W: tileW, H: tileH,
		}
	}
	return s
}

// addressText is what the address bar shows for the current view.
func (s *State) addressText() string {
	if s.onSite {
		return s.favs[s.cur].url
	}
	return "Search or enter website name"
}

// Render paints the browser.
func Render(s *State, buf []byte) {
	p := painter.NewPixelPainter(buf, s.W, s.H)
	th := s.theme
	headerBG := th.Extra["headerbar_bg_color"]
	if headerBG == (toolkit.RGBA{}) {
		headerBG = th.Background
	}

	// Content ground first (start page = window grey, a site = white).
	contentBG := th.Background
	if s.onSite {
		contentBG = th.Surface
	}
	p.FillRect(toolkit.Rect{X: 0, Y: toolbarH, W: s.W, H: s.H - toolbarH}, contentBG)

	// Toolbar band + bottom hairline.
	p.FillRect(toolkit.Rect{X: 0, Y: 0, W: s.W, H: toolbarH}, headerBG)
	p.FillRect(toolkit.Rect{X: 0, Y: toolbarH - 1, W: s.W, H: 1}, th.Border)

	// Back / forward / new-tab buttons. Back is live on a site page; Forward
	// is live on the start page once a site has been visited.
	backInk := dim(th)
	if s.onSite {
		backInk = th.OnSurface
	}
	fwdInk := dim(th)
	if !s.onSite && s.visited {
		fwdInk = th.OnSurface
	}
	drawButton(p, th, s.backRect, "<", backInk)
	drawButton(p, th, s.fwdRect, ">", fwdInk)
	drawButton(p, th, s.addRect, "+", th.OnSurface)

	// Address bar: a rounded-ish white pill with the URL / placeholder.
	p.FillRect(s.addrRect, th.Surface)
	strokePill(p, s.addrRect, th.Border)
	urlInk := th.OnSurface
	if !s.onSite {
		urlInk = dim(th)
	}
	toolkit.DrawText(p, s.addrRect.X+12, s.addrRect.Y+(addrH-toolkit.GlyphHeight)/2, s.addressText(), urlInk)

	if s.onSite {
		renderSite(s, p, th)
		return
	}
	renderStart(s, p, th)
}

// renderStart paints the Favourites tile grid.
func renderStart(s *State, p *painter.PixelPainter, th *toolkit.Theme) {
	toolkit.DrawText(p, gridLeft, toolbarH+28, "Favourites", th.OnBackground)
	onAccent := th.Extra["accent_fg_color"]
	if onAccent == (toolkit.RGBA{}) {
		onAccent = toolkit.RGB(0xff, 0xff, 0xff)
	}
	for i, f := range s.favs {
		r := s.tileRects[i]
		// Card body + border.
		p.FillRect(r, th.Surface)
		strokePill(p, r, th.Border)
		// Icon block: accent square with the site's first letter, centred.
		iconSz := 44
		ix := r.X + (r.W-iconSz)/2
		iy := r.Y + 14
		p.FillRect(toolkit.Rect{X: ix, Y: iy, W: iconSz, H: iconSz}, th.Accent)
		initial := string(upper(f.name[0]))
		toolkit.DrawText(p, ix+(iconSz-toolkit.TextWidth(initial))/2, iy+(iconSz-toolkit.GlyphHeight)/2, initial, onAccent)
		// Label under the icon.
		lw := toolkit.TextWidth(f.name)
		toolkit.DrawText(p, r.X+(r.W-lw)/2, r.Y+r.H-16, f.name, th.OnSurface)
	}
}

// renderSite paints a placeholder page for the current favourite.
func renderSite(s *State, p *painter.PixelPainter, th *toolkit.Theme) {
	f := s.favs[s.cur]
	x := gridLeft
	toolkit.DrawText(p, x, toolbarH+50, f.name, th.OnSurface)
	toolkit.DrawText(p, x, toolbarH+50+toolkit.GlyphHeight+10, "https://"+f.url, dim(th))
	msg := []string{
		"This is a local placeholder page.",
		"wasmbox runs under COEP:require-corp, so a live",
		"cross-origin site cannot be embedded in the sandbox.",
		"",
		"Click < (Back) to return to Favourites.",
	}
	for i, line := range msg {
		toolkit.DrawText(p, x, toolbarH+96+i*(toolkit.GlyphHeight+8), line, th.OnSurface)
	}
}

// HandleMouse routes a click: Back, Forward, or a favourite tile (navigate).
// Returns true when a redraw is needed.
func (s *State) HandleMouse(x, y int) bool {
	if inRect(x, y, s.backRect) {
		return s.back()
	}
	if inRect(x, y, s.fwdRect) {
		return s.forward()
	}
	if !s.onSite {
		for i := range s.favs {
			if inRect(x, y, s.tileRects[i]) {
				s.navigate(i)
				return true
			}
		}
	}
	return false
}

// navigate opens favourite i.
func (s *State) navigate(i int) {
	s.onSite = true
	s.visited = true
	s.cur = i
}

// back returns from a site page to the start page.
func (s *State) back() bool {
	if s.onSite {
		s.onSite = false
		return true
	}
	return false
}

// forward re-opens the last visited site from the start page.
func (s *State) forward() bool {
	if !s.onSite && s.visited {
		s.onSite = true
		return true
	}
	return false
}

// HandleKey: Backspace / Escape acts as Back.
func (s *State) HandleKey(code string) bool {
	if code == "Backspace" || code == "Escape" {
		return s.back()
	}
	return false
}

// --- helpers --------------------------------------------------------------

func drawButton(p *painter.PixelPainter, th *toolkit.Theme, r toolkit.Rect, label string, ink toolkit.RGBA) {
	p.FillRect(r, th.SurfaceAlt)
	strokePill(p, r, th.Border)
	toolkit.DrawText(p, r.X+(r.W-toolkit.TextWidth(label))/2, r.Y+(r.H-toolkit.GlyphHeight)/2, label, ink)
}

// strokePill outlines r (a 1px border); named for the pill/card shapes it edges.
func strokePill(p *painter.PixelPainter, r toolkit.Rect, c toolkit.RGBA) {
	p.FillRect(toolkit.Rect{X: r.X, Y: r.Y, W: r.W, H: 1}, c)
	p.FillRect(toolkit.Rect{X: r.X, Y: r.Y + r.H - 1, W: r.W, H: 1}, c)
	p.FillRect(toolkit.Rect{X: r.X, Y: r.Y, W: 1, H: r.H}, c)
	p.FillRect(toolkit.Rect{X: r.X + r.W - 1, Y: r.Y, W: 1, H: r.H}, c)
}

// dim returns a muted ink for placeholder / disabled text.
func dim(*toolkit.Theme) toolkit.RGBA { return toolkit.RGB(0x80, 0x80, 0x88) }

func upper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 32
	}
	return b
}

func inRect(x, y int, r toolkit.Rect) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}
