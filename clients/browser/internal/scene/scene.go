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
//
// Layout is built from the toolkit's box-layout container model (Dock + Box +
// Card containers) rather than hand-computed toolkit.Rect placement. The whole
// chrome is one tree:
//
//	root  Dock                                   — the browser shell
//	├─ (docked North, height toolbarH)  band  VBox — the toolbar band
//	│  └─ (centre row, height btnH)      row  HBox
//	│     [ pad · back · gap · fwd · gap · addr(flex) · gap · add · pad ]
//	└─ (body / Centre)  content  Container(CardLayout)
//	   ├─ card 0  startCard — "Favourites" heading + tile grid (VBox of HBox)
//	   └─ card 1  siteCard  — the local placeholder page
//
// The toolbar row's fixed pixel widths become AddFixed extents and its uneven
// gaps become invisible fixed spacers, so every control lands at exactly the
// rect the old hand-placed code produced (proven by the golden-rect test). A
// single root.SetBounds lays the whole tree out, root.Draw paints it, and
// root.OnEvent routes clicks into child-local space — no per-widget rect
// arithmetic and no manual hit-testing. The start/site view switch is a
// CardLayout Active toggle rather than an onSite branch in the renderer.

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
// Hyperlegible @16px, toolkit v0.77.0), so the toolbar controls, address bar,
// favourites heading, tile labels and placeholder page render as the shaped
// vector face. The bundled face never fails to parse (the error is documented as
// never-returned); on the impossible error path the toolkit leaves the still-
// working bitmap default active, so a swallowed error degrades to legible bitmap
// text, never to none. All chrome text is placed with toolkit.TextWidth /
// toolkit.GlyphHeight, so the taller face re-centres itself and no rect moves.
func enableAAText() { aaOnce.Do(func() { _ = toolkit.UseOpenTypeText() }) }

// link is one favourite: a display name + the URL shown in the address bar.
type link struct {
	name string
	url  string
}

// State holds the widget tree, the favourites, and the navigation model.
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

	// Widget tree.
	root      *toolkit.Dock
	content   *toolkit.Container
	card      *toolkit.CardLayout
	startCard *startCard
	siteCard  *siteCard
	grid      *toolkit.VBox
	backBtn   *iconButton
	fwdBtn    *iconButton
	addBtn    *iconButton
	addr      *addrBar
	tiles     []*tile

	// dirty is set by a control's OnClick when a press mutates the model, so
	// HandleMouse can report to the render loop whether a click did anything
	// (the container routing itself has no hit/miss return value).
	dirty bool
}

// Layout constants (pixels).
const (
	toolbarH = 46
	btnW     = 30
	btnH     = 28
	btnLeft  = 12
	btnGap   = 6
	addrGap  = 14 // gap between the fwd button / addr pill / add button
	addrH    = 28
	tileCols = 4
	tileW    = 150
	tileH    = 98
	tileGapX = 22
	tileGapY = 26
	gridTop  = toolbarH + 56
	gridLeft = 40

	// btnY is the toolbar control's top inset, vertically centring the btnH
	// row within the toolbarH band ((46-28)/2 = 9, symmetric top/bottom).
	btnY = (toolbarH - btnH) / 2
	// headingOffset is the "Favourites" heading's baseline offset below the
	// toolbar (matches the old toolbarH+28 placement).
	headingOffset = 28
	// gridTopInset is the tile grid's top within the content body (the body
	// starts at toolbarH, the grid at gridTop).
	gridTopInset = gridTop - toolbarH
	// gridW is the tile grid's fixed width: tileCols tiles + inter-tile gaps.
	gridW = tileCols*tileW + (tileCols-1)*tileGapX
	// zeroGap sets a box Spacing to zero (Spacing==0 means "default 4px"; a
	// negative value clamps to 0 at layout time — see toolkit boxSpacing).
	zeroGap = -1
)

// New builds the browser sized W×H and lays out its widget tree.
func New(w, h int) *State {
	enableAAText() // chrome + page text render with the AA/shaped OpenType face.
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

	// Toolbar controls. Each control reads live state through a closure so a
	// single persistent widget tree reflects the navigation model at Draw
	// time (back/forward ink enable, address text) without a rebuild.
	s.backBtn = &iconButton{label: "<", ink: func() toolkit.RGBA {
		if s.onSite {
			return s.theme.OnSurface
		}
		return dim(s.theme)
	}, onClick: func() { s.dirty = s.back() }}
	s.fwdBtn = &iconButton{label: ">", ink: func() toolkit.RGBA {
		if !s.onSite && s.visited {
			return s.theme.OnSurface
		}
		return dim(s.theme)
	}, onClick: func() { s.dirty = s.forward() }}
	// The new-tab "+" button is drawn but inert (as in the original shell).
	s.addBtn = &iconButton{label: "+", ink: func() toolkit.RGBA { return s.theme.OnSurface }, onClick: func() {}}
	s.addr = &addrBar{text: func() (string, toolkit.RGBA) {
		if s.onSite {
			return s.addressText(), s.theme.OnSurface
		}
		return s.addressText(), dim(s.theme)
	}}

	// Toolbar row: fixed-width controls with invisible fixed spacers carrying
	// the exact (uneven) gaps, so each control lands at its historical rect.
	row := toolkit.NewHBox()
	row.Spacing = zeroGap
	row.AddFixed(spacer(), btnLeft)
	row.AddFixed(s.backBtn, btnW)
	row.AddFixed(spacer(), btnGap)
	row.AddFixed(s.fwdBtn, btnW)
	row.AddFixed(spacer(), addrGap)
	row.AddFlex(s.addr, 1)
	row.AddFixed(spacer(), addrGap)
	row.AddFixed(s.addBtn, btnW)
	row.AddFixed(spacer(), btnLeft)

	// Toolbar band: vertically centre the btnH row within the toolbarH band.
	band := toolkit.NewVBox()
	band.Spacing = zeroGap
	band.AddFixed(spacer(), btnY)
	band.AddFixed(row, btnH)
	band.AddFixed(spacer(), toolbarH-btnH-btnY)

	// Tile grid: a column of fixed-height rows, each a row of fixed-width
	// tiles with tileGapX/tileGapY gutters — reproduces the 150×98 tiles + 22/26
	// gaps declaratively instead of one toolkit.Rect per tile.
	s.grid = toolkit.NewVBox()
	s.grid.Spacing = tileGapY
	s.tiles = make([]*tile, len(s.favs))
	var rowBox *toolkit.HBox
	for i := range s.favs {
		if i%tileCols == 0 { // start a new row every tileCols tiles
			rowBox = toolkit.NewHBox()
			rowBox.Spacing = tileGapX
			s.grid.AddFixed(rowBox, tileH)
		}
		idx := i
		t := &tile{fav: s.favs[i], onClick: func() {
			s.navigate(idx)
			s.dirty = true
		}}
		s.tiles[i] = t
		rowBox.AddFixed(t, tileW)
	}

	// Content: the start page and the site page as two cards; the active one
	// fills the body, the other collapses to an empty rect (not drawn, not
	// hit-tested) — the Card layout replaces the old onSite render branch.
	s.startCard = &startCard{s: s, grid: s.grid}
	s.siteCard = &siteCard{s: s}
	s.card = &toolkit.CardLayout{Active: 0}
	s.content = toolkit.NewContainer(s.card)
	s.content.AddWidget(s.startCard) // card 0
	s.content.AddWidget(s.siteCard)  // card 1

	// Shell: dock the toolbar band to the top; the content body fills the rest.
	s.root = toolkit.NewDock(s.content)
	s.root.Dock(band, toolkit.DockTop, toolbarH)
	s.root.SetBounds(toolkit.Rect{X: 0, Y: 0, W: w, H: h})
	return s
}

// spacer is an invisible, inert box cell used to carry a fixed gap.
func spacer() toolkit.Widget { return toolkit.NewContainer(nil) }

// addressText is what the address bar shows for the current view.
func (s *State) addressText() string {
	if s.onSite {
		return s.favs[s.cur].url
	}
	return "Search or enter website name"
}

// Render paints the browser: the background bands first, then the widget tree.
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

	// Controls + content, laid out and routed by the container tree.
	s.root.Draw(p, th)
}

// HandleMouse routes a click at surface coordinates (x, y) through the
// container tree (translated into the root's local space); the control whose
// bounds contain the point fires its OnClick, which sets dirty. Returns true
// when a click triggered an action (the scene should re-render).
func (s *State) HandleMouse(x, y int) bool {
	s.dirty = false
	rb := s.root.Bounds()
	s.root.OnEvent(toolkit.Event{Kind: toolkit.EventClick, X: x - rb.X, Y: y - rb.Y})
	return s.dirty
}

// navigate opens favourite i and switches to the site card.
func (s *State) navigate(i int) {
	s.onSite = true
	s.visited = true
	s.cur = i
	s.syncCard()
}

// back returns from a site page to the start page.
func (s *State) back() bool {
	if s.onSite {
		s.onSite = false
		s.syncCard()
		return true
	}
	return false
}

// forward re-opens the last visited site from the start page.
func (s *State) forward() bool {
	if !s.onSite && s.visited {
		s.onSite = true
		s.syncCard()
		return true
	}
	return false
}

// syncCard points the CardLayout at the active view and re-arranges the
// content container so the shown card fills the body and the other collapses.
func (s *State) syncCard() {
	if s.onSite {
		s.card.Active = 1
	} else {
		s.card.Active = 0
	}
	s.content.SetBounds(s.content.Bounds())
}

// HandleKey: Backspace / Escape acts as Back.
func (s *State) HandleKey(code string) bool {
	if code == "Backspace" || code == "Escape" {
		return s.back()
	}
	return false
}

// --- leaf widgets ---------------------------------------------------------

// iconButton is a rounded chrome button with a centred glyph whose ink is
// resolved from live state at Draw time.
type iconButton struct {
	toolkit.Base
	label   string
	ink     func() toolkit.RGBA
	onClick func()
}

func (b *iconButton) Draw(p painter.Painter, th *toolkit.Theme) {
	r := b.Bounds()
	p.FillRoundRect(r, 6, th.SurfaceAlt)
	p.StrokeRoundRect(r, 6, th.Border, 1)
	toolkit.DrawText(p, r.X+(r.W-toolkit.TextWidth(b.label))/2, r.Y+(r.H-toolkit.GlyphHeight())/2, b.label, b.ink())
}

func (b *iconButton) OnEvent(ev toolkit.Event) {
	if ev.Kind == toolkit.EventClick && b.onClick != nil {
		b.onClick()
	}
}

// addrBar is the rounded white address pill. It is drawn but not interactive
// (the shell has no in-place editing), so it keeps Base's no-op OnEvent.
type addrBar struct {
	toolkit.Base
	text func() (string, toolkit.RGBA)
}

func (a *addrBar) Draw(p painter.Painter, th *toolkit.Theme) {
	r := a.Bounds()
	p.FillRoundRect(r, addrH/2, th.Surface)
	p.StrokeRoundRect(r, addrH/2, th.Border, 1)
	txt, ink := a.text()
	toolkit.DrawText(p, r.X+12, r.Y+(r.H-toolkit.GlyphHeight())/2, txt, ink)
}

// tile is one Favourites bookmark: a rounded card with an accent icon square
// (the site's initial) and a label; a click navigates to the site.
type tile struct {
	toolkit.Base
	fav     link
	onClick func()
}

func (t *tile) Draw(p painter.Painter, th *toolkit.Theme) {
	r := t.Bounds()
	// Card body + border (rounded).
	p.FillRoundRect(r, 10, th.Surface)
	p.StrokeRoundRect(r, 10, th.Border, 1)
	// Icon block: rounded accent square with the site's first letter.
	onAccent := th.Extra["accent_fg_color"]
	if onAccent == (toolkit.RGBA{}) {
		onAccent = toolkit.RGB(0xff, 0xff, 0xff)
	}
	iconSz := 44
	ix := r.X + (r.W-iconSz)/2
	iy := r.Y + 14
	p.FillRoundRect(toolkit.Rect{X: ix, Y: iy, W: iconSz, H: iconSz}, 10, th.Accent)
	initial := string(upper(t.fav.name[0]))
	toolkit.DrawText(p, ix+(iconSz-toolkit.TextWidth(initial))/2, iy+(iconSz-toolkit.GlyphHeight())/2, initial, onAccent)
	// Label under the icon.
	lw := toolkit.TextWidth(t.fav.name)
	toolkit.DrawText(p, r.X+(r.W-lw)/2, r.Y+r.H-16, t.fav.name, th.OnSurface)
}

func (t *tile) OnEvent(ev toolkit.Event) {
	if ev.Kind == toolkit.EventClick && t.onClick != nil {
		t.onClick()
	}
}

// startCard is the Favourites start page: it draws the heading and hosts the
// tile grid at its historical inset within the content body.
type startCard struct {
	toolkit.Base
	s    *State
	grid *toolkit.VBox
}

func (c *startCard) SetBounds(r toolkit.Rect) {
	c.Base.SetBounds(r)
	if r.W == 0 || r.H == 0 { // collapsed by CardLayout when inactive
		c.grid.SetBounds(toolkit.Rect{})
		return
	}
	c.grid.SetBounds(toolkit.Rect{
		X: r.X + gridLeft,
		Y: r.Y + gridTopInset,
		W: gridW,
		H: r.H - gridTopInset,
	})
}

func (c *startCard) Draw(p painter.Painter, th *toolkit.Theme) {
	r := c.Bounds()
	toolkit.DrawText(p, r.X+gridLeft, r.Y+headingOffset, "Favourites", th.OnBackground)
	c.grid.Draw(p, th)
}

func (c *startCard) OnEvent(ev toolkit.Event) {
	// ev is startCard-local; forward to the grid in its own local space.
	pr := c.Bounds()
	sx, sy := ev.X+pr.X, ev.Y+pr.Y
	gb := c.grid.Bounds()
	if gb.Contains(sx, sy) {
		fwd := ev
		fwd.X, fwd.Y = sx-gb.X, sy-gb.Y
		c.grid.OnEvent(fwd)
	}
}

// siteCard is the local placeholder page for the current favourite.
type siteCard struct {
	toolkit.Base
	s *State
}

func (c *siteCard) Draw(p painter.Painter, th *toolkit.Theme) {
	r := c.Bounds()
	f := c.s.favs[c.s.cur]
	x := r.X + gridLeft
	// The body starts at y=toolbarH, so r.Y+k reproduces the old toolbarH+k.
	toolkit.DrawText(p, x, r.Y+50, f.name, th.OnSurface)
	toolkit.DrawText(p, x, r.Y+50+toolkit.GlyphHeight()+10, "https://"+f.url, dim(th))
	msg := []string{
		"This is a local placeholder page.",
		"wasmbox runs under COEP:require-corp, so a live",
		"cross-origin site cannot be embedded in the sandbox.",
		"",
		"Click < (Back) to return to Favourites.",
	}
	for i, line := range msg {
		toolkit.DrawText(p, x, r.Y+96+i*(toolkit.GlyphHeight()+8), line, th.OnSurface)
	}
}

// --- helpers --------------------------------------------------------------

// dim returns a muted ink for placeholder / disabled text.
func dim(*toolkit.Theme) toolkit.RGBA { return toolkit.RGB(0x80, 0x80, 0x88) }

func upper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 32
	}
	return b
}
