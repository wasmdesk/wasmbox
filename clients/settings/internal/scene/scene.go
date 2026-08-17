// SPDX-License-Identifier: BSD-3-Clause
//
// Package scene renders the wasmdesk Settings panel in the WhiteSur (macOS Big
// Sur) style: a grey category sidebar on the left and a white content pane on
// the right whose rows carry toolkit Switch / Scale controls -- the libadwaita
// / System-Settings layout. It validates that the toolkit's Switch + Scale
// compose into a real preferences surface driven by (sidebar select -> page
// switch) and (row control -> model update).
//
// The panel is built from the toolkit's box-layout container model rather
// than hand-computed toolkit.Rect placement:
//
//	root  Container(BorderLayout)                — the app shell
//	├─ West   sidebar  VBox (fixed sidebarW)     — title spacer + catRow ×N + filler
//	└─ Center content  Container(CardLayout)      — one page per category, only
//	                                                the selected page is shown
//	   page  (custom widget)                      — page title + white card
//	      rows  VBox (fixed-height rows)          — one settingRowW per preference
//	         settingRowW  (custom widget)         — title + divider + Switch/Scale
//
// A single root.SetBounds lays the whole tree out, root.Draw paints it, and
// root.OnEvent routes clicks into child-local space -- there is no flat layout()
// arithmetic and no manual insideRect hit-testing.

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
// Hyperlegible @16px, toolkit v0.77.0), so the "Settings" title, sidebar
// categories and row titles render as the shaped vector face. The bundled face
// never fails to parse (the error is documented as never-returned); on the
// impossible error path the toolkit leaves the still-working bitmap default
// active, so a swallowed error degrades to legible bitmap text, never to none.
// Every label is placed with toolkit.GlyphHeight-centred DrawText, so the taller
// face re-centres itself in its band and no widget rect moves.
func enableAAText() { aaOnce.Do(func() { _ = toolkit.UseOpenTypeText() }) }

// rowKind selects which control a settings row carries.
type rowKind int

const (
	rowSwitch rowKind = iota
	rowScale
)

// settingRow is one preference: a title plus exactly one control. It is the
// model the pages' settingRowW widgets wrap (they share the sw/sc pointers).
type settingRow struct {
	title string
	kind  rowKind
	sw    *toolkit.Switch
	sc    *toolkit.Scale
}

// category is one sidebar entry and the rows shown when it is selected.
type category struct {
	name string
	rows []settingRow
}

// State bundles the sidebar model, every row's control widget and the container
// tree that lays them out.
type State struct {
	W, H     int
	theme    *toolkit.Theme
	cats     []category
	selected int

	root    *toolkit.Container  // BorderLayout: West sidebar + Center content
	content *toolkit.Container  // CardLayout page switcher
	cards   *toolkit.CardLayout // the content layout, so selection sets Active
	catRows []*catRow           // sidebar entries, in category order
	pages   []*page             // content pages, in category order
	dirty   bool                // set when a routed click mutated the model
}

// Layout constants (pixels). Modelled on macOS Ventura System Settings: a grey
// window with a translucent-feel sidebar and content area, and the settings
// rows grouped inside a white rounded "card".
const (
	sidebarW    = 200
	catTop      = 48
	catRowH     = 34
	catMargin   = 10 // sidebar pill inset from the sidebar edges
	sidePad     = 16 // sidebar text inset
	titleTop    = 22
	cardMarginX = 20 // card inset from the content-area edges
	cardTop     = 56
	cardRadius  = 10
	rowH        = 44
	rowPadX     = 16 // row content inset from the card edges
	// Switch + slider share a compact 20px control height so their knobs read
	// as the same 16px family (switch knob = switchH-2*switchPad = 16; the
	// toolkit slider thumb is 16), instead of a chunky pill next to a thin bar.
	switchW = 36
	switchH = 20
	scaleW  = 180
	scaleH  = 20
)

// New builds the Settings panel sized W×H.
func New(w, h int) *State {
	enableAAText() // sidebar + card text render with the AA/shaped OpenType face.
	s := &State{W: w, H: h, theme: toolkit.WhiteSurLight()}
	s.cats = []category{
		{name: "Appearance", rows: []settingRow{
			{title: "Dark Mode", kind: rowSwitch},
			{title: "Reduce Transparency", kind: rowSwitch},
			{title: "Sidebar Opacity", kind: rowScale},
		}},
		{name: "Wi-Fi", rows: []settingRow{
			{title: "Wi-Fi", kind: rowSwitch},
			{title: "Ask to Join Networks", kind: rowSwitch},
		}},
		{name: "Sound", rows: []settingRow{
			{title: "Output Volume", kind: rowScale},
			{title: "Alert Volume", kind: rowScale},
			{title: "Startup Chime", kind: rowSwitch},
		}},
		{name: "Displays", rows: []settingRow{
			{title: "Brightness", kind: rowScale},
			{title: "Night Shift", kind: rowSwitch},
			{title: "True Tone", kind: rowSwitch},
		}},
		{name: "General", rows: []settingRow{
			{title: "Bluetooth", kind: rowSwitch},
			{title: "Airplane Mode", kind: rowSwitch},
		}},
	}
	// A few switches default on so the panel doesn't look inert.
	onByDefault := map[string]bool{"Wi-Fi": true, "True Tone": true, "Bluetooth": true}
	for ci := range s.cats {
		for ri := range s.cats[ci].rows {
			row := &s.cats[ci].rows[ri]
			switch row.kind {
			case rowSwitch:
				row.sw = toolkit.NewSwitch(onByDefault[row.title])
			case rowScale:
				row.sc = toolkit.NewScale(0, 100, 60)
			}
		}
	}

	s.buildTree()
	return s
}

// buildTree assembles the box-layout container tree over the category model and lays
// it out once with a single root.SetBounds.
func (s *State) buildTree() {
	// Sidebar: a fixed top spacer under the "Settings" title, then one catRow
	// per category (each at its historical catTop+i*catRowH position via fixed
	// heights and no inter-row gap), then a flex filler soaking up the rest.
	sidebar := toolkit.NewVBox()
	sidebar.Spacing = -1 // contiguous rows (negative Spacing -> 0 gap in the box model)
	sidebar.AddFixed(toolkit.NewContainer(nil), catTop)
	for i := range s.cats {
		cr := &catRow{
			name:     s.cats[i].name,
			idx:      i,
			selected: &s.selected,
			onClick:  func(idx int) func() { return func() { s.selectCat(idx) } }(i),
		}
		s.catRows = append(s.catRows, cr)
		sidebar.AddFixed(cr, catRowH)
	}
	sidebar.AddFlex(toolkit.NewContainer(nil), 1)

	// Content: a card-layout stack of one page per category; only the selected
	// page draws + receives events.
	s.cards = &toolkit.CardLayout{Active: s.selected}
	s.content = toolkit.NewContainer(s.cards)
	for ci := range s.cats {
		rows := toolkit.NewVBox()
		rows.Spacing = -1 // contiguous 44px rows, no gap (dividers are drawn instead)
		n := len(s.cats[ci].rows)
		for ri := range s.cats[ci].rows {
			src := &s.cats[ci].rows[ri]
			rows.AddFixed(&settingRowW{
				title:   src.title,
				kind:    src.kind,
				sw:      src.sw,
				sc:      src.sc,
				divider: ri < n-1,
				notify:  func() { s.dirty = true },
			}, rowH)
		}
		pg := &page{name: s.cats[ci].name, rows: rows, numRows: n}
		s.pages = append(s.pages, pg)
		s.content.AddWidget(pg)
	}

	// Shell: sidebar docked West at its fixed width, content filling the Center.
	s.root = toolkit.NewContainer(toolkit.BorderLayout{})
	s.root.Add(toolkit.Item{Widget: sidebar, Region: toolkit.RegionWest, Size: sidebarW})
	s.root.Add(toolkit.Item{Widget: s.content, Region: toolkit.RegionCenter})
	s.root.SetBounds(toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H})
}

// selectCat switches the shown category, re-arranging the content card stack so
// the newly-active page (and its controls) get laid out. A no-op when the target
// is already selected. Sets dirty so a routed click reports a needed redraw.
func (s *State) selectCat(i int) {
	if s.selected == i {
		return
	}
	s.selected = i
	s.cards.Active = i
	s.content.SetBounds(s.content.Bounds()) // re-run CardLayout for the new active page
	s.dirty = true
}

// --- sidebar category row -------------------------------------------------

// catRow is one sidebar entry: a full-width band that draws an accent pill +
// light-ink label when it is the selected category, else a plain label. A click
// anywhere on the band selects the category.
type catRow struct {
	toolkit.Base
	name     string
	idx      int
	selected *int   // shared pointer to State.selected, read at draw time
	onClick  func() // selects this category
}

// Draw paints the pill (when selected) and the category label.
func (c *catRow) Draw(p painter.Painter, th *toolkit.Theme) {
	b := c.Bounds()
	onAccent := th.Extra["accent_fg_color"]
	if onAccent == (toolkit.RGBA{}) {
		onAccent = toolkit.RGB(0xff, 0xff, 0xff)
	}
	ink := th.OnBackground
	if *c.selected == c.idx {
		p.FillRoundRect(toolkit.Rect{X: b.X + catMargin, Y: b.Y, W: b.W - 2*catMargin, H: catRowH - 4}, 7, th.Accent)
		ink = onAccent
	}
	toolkit.DrawText(p, b.X+sidePad+5, b.Y+(catRowH-4-toolkit.GlyphHeight())/2, c.name, ink)
}

// OnEvent selects this category on a click (coordinates are irrelevant: the
// whole band is one target).
func (c *catRow) OnEvent(ev toolkit.Event) {
	if ev.Kind == toolkit.EventClick {
		c.onClick()
	}
}

// --- content page ---------------------------------------------------------

// page is one category's content surface: the page title above a white rounded
// card that groups the category's rows. It owns a rows VBox positioned inside the
// card and translates events into the rows' local space.
type page struct {
	toolkit.Base
	name    string
	rows    *toolkit.VBox
	numRows int
	card    toolkit.Rect
}

// SetBounds derives the card rectangle from the content bounds b (inset by
// cardMarginX on the sides, cardTop from the top, exactly numRows*rowH tall) and
// lays the rows out inside it.
func (pg *page) SetBounds(b toolkit.Rect) {
	pg.Base.SetBounds(b)
	pg.card = toolkit.Rect{X: b.X + cardMarginX, Y: b.Y + cardTop, W: b.W - 2*cardMarginX, H: pg.numRows * rowH}
	pg.rows.SetBounds(pg.card)
}

// Draw paints the page title, the white card fill + border, then the rows.
func (pg *page) Draw(p painter.Painter, th *toolkit.Theme) {
	b := pg.Bounds()
	toolkit.DrawText(p, b.X+cardMarginX, b.Y+titleTop, pg.name, th.OnSurface)
	p.FillRoundRect(pg.card, cardRadius, th.Surface)
	p.StrokeRoundRect(pg.card, cardRadius, th.Border, 1)
	pg.rows.Draw(p, th)
}

// OnEvent forwards the click to the rows VBox, translated from page-local into
// the card's (rows') local coordinate space.
func (pg *page) OnEvent(ev toolkit.Event) {
	pb, rb := pg.Bounds(), pg.rows.Bounds()
	ev.X += pb.X - rb.X
	ev.Y += pb.Y - rb.Y
	pg.rows.OnEvent(ev)
}

// --- content setting row --------------------------------------------------

// settingRowW is one preference row inside a card: its title on the left, a 1px
// divider along the bottom (except the last row), and its Switch or Scale control
// right-aligned and vertically centred. A click anywhere on a switch row toggles
// the switch (a comfortable macOS-sized target); a scale row only responds to
// clicks that land on the slider itself.
type settingRowW struct {
	toolkit.Base
	title   string
	kind    rowKind
	sw      *toolkit.Switch
	sc      *toolkit.Scale
	divider bool
	notify  func() // marks the scene dirty when the control changed
}

// control returns the row's single control widget.
func (r *settingRowW) control() toolkit.Widget {
	if r.kind == rowSwitch {
		return r.sw
	}
	return r.sc
}

// SetBounds right-aligns the control within the row band b (rowPadX inset from
// the right edge) and vertically centres it.
func (r *settingRowW) SetBounds(b toolkit.Rect) {
	r.Base.SetBounds(b)
	switch r.kind {
	case rowSwitch:
		r.sw.SetBounds(toolkit.Rect{X: b.X + b.W - rowPadX - switchW, Y: b.Y + (rowH-switchH)/2, W: switchW, H: switchH})
	case rowScale:
		r.sc.SetBounds(toolkit.Rect{X: b.X + b.W - rowPadX - scaleW, Y: b.Y + (rowH-scaleH)/2, W: scaleW, H: scaleH})
	}
}

// Draw paints the title, the optional divider, then the control.
func (r *settingRowW) Draw(p painter.Painter, th *toolkit.Theme) {
	b := r.Bounds()
	toolkit.DrawText(p, b.X+rowPadX, b.Y+(rowH-toolkit.GlyphHeight())/2, r.title, th.OnSurface)
	if r.divider {
		p.FillRect(toolkit.Rect{X: b.X + rowPadX, Y: b.Y + b.H - 1, W: b.W - 2*rowPadX, H: 1}, th.Border)
	}
	r.control().Draw(p, th)
}

// OnEvent toggles the switch on any click, or forwards a scale click (with the
// x-position preserved) only when it lands on the slider.
func (r *settingRowW) OnEvent(ev toolkit.Event) {
	if ev.Kind != toolkit.EventClick {
		return
	}
	if r.kind == rowSwitch {
		r.sw.OnEvent(toolkit.Event{Kind: toolkit.EventClick})
		r.notify()
		return
	}
	b := r.Bounds()
	sx, sy := ev.X+b.X, ev.Y+b.Y
	cb := r.sc.Bounds()
	if cb.Contains(sx, sy) {
		r.sc.OnEvent(toolkit.Event{Kind: toolkit.EventClick, X: sx - cb.X, Y: sy - cb.Y})
		r.notify()
	}
}

// --- scene plumbing -------------------------------------------------------

// Render paints the whole panel: the grey window ground and the sidebar/content
// hairline, the fixed "Settings" title, then the container tree.
func Render(s *State, buf []byte) {
	p := painter.NewPixelPainter(buf, s.W, s.H)
	th := s.theme
	fillBox(p, toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H}, th.Background)
	fillBox(p, toolkit.Rect{X: sidebarW, Y: 0, W: 1, H: s.H}, th.Border)
	toolkit.DrawText(p, sidePad, titleTop, "Settings", th.OnBackground)
	s.root.Draw(p, th)
}

// HandleMouse routes a click at surface coordinates (x, y) through the container
// tree (translated into the root's local space). A click in the sidebar selects
// a category; a click in the content pane toggles a switch row or moves a slider.
// Returns true if the click mutated the model (the scene should re-render).
func (s *State) HandleMouse(x, y int) bool {
	s.dirty = false
	rb := s.root.Bounds()
	s.root.OnEvent(toolkit.Event{Kind: toolkit.EventClick, X: x - rb.X, Y: y - rb.Y})
	return s.dirty
}

// HandleKey lets Up/Down move between categories. Returns true if the selection
// changed.
func (s *State) HandleKey(code string) bool {
	switch code {
	case "ArrowDown":
		if s.selected < len(s.cats)-1 {
			s.selectCat(s.selected + 1)
			return true
		}
	case "ArrowUp":
		if s.selected > 0 {
			s.selectCat(s.selected - 1)
			return true
		}
	}
	return false
}
