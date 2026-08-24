// SPDX-License-Identifier: BSD-3-Clause
//
// Package scene renders the wasmdesk Settings panel in the WhiteSur (macOS Big
// Sur) style: a grey category sidebar on the left and a white content pane on
// the right whose rows carry toolkit Switch / Scale controls -- the libadwaita
// / System-Settings layout. It validates that the toolkit's Switch + Scale
// compose into a real preferences surface driven by (sidebar select -> page
// switch) and (row control -> model update).
//
// The panel is built entirely from stock toolkit widgets rather than
// hand-computed toolkit.Rect placement and hand-drawn chrome:
//
//	root  Container(BorderLayout)                — the app shell
//	├─ West   sidebar  VBox                       — title spacer + ListBox
//	│     ListBox (sectioned)                     — grouped category rows, each
//	│                                               drawn by an iconoir icon +
//	│                                               Label ItemRenderer
//	└─ Center content  Container(CardLayout)      — one page per category, only
//	                                                the selected page is shown
//	   page  (composition widget)                 — title Label + SettingsGroup
//	      SettingsGroup                           — the white settings "card"
//	         SettingRow ×N                        — label + Switch/Scale + divider
//
// A single root.SetBounds lays the whole tree out, root.Draw paints it, and
// root.OnEvent routes clicks into child-local space. The sidebar is a
// toolkit.ListBox in sectioned mode (its own Selected() Observable + OnActivate
// drive category selection); every settings card is a toolkit.SettingsGroup of
// toolkit.SettingRow rows; and control changes report back through the Switch /
// Scale MVVM Observables rather than a hand-wired notify callback.

package scene

import (
	"sync"

	"github.com/go-iconoir/iconoir"
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
func enableAAText() { aaOnce.Do(func() { _ = toolkit.UseOpenTypeText() }) }

// rowKind selects which control a settings row carries.
type rowKind int

const (
	rowSwitch rowKind = iota
	rowScale
)

// settingRow is one preference: a title plus exactly one control. The pages'
// SettingRow widgets wrap it (they share the sw/sc pointers).
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

	root      *toolkit.Container  // BorderLayout: West sidebar + Center content
	content   *toolkit.Container  // CardLayout page switcher
	cards     *toolkit.CardLayout // the content layout, so selection sets Active
	list      *toolkit.ListBox    // sectioned sidebar category list
	title     *toolkit.Label      // the fixed "Settings" window title
	itemLabel *toolkit.Label      // shared scratch label the ListBox ItemRenderer paints each row's text with
	pages     []*page             // content pages, in category order
	dirty     bool                // set when a routed click mutated the model
}

// Layout constants (pixels). Modelled on macOS Ventura System Settings.
const (
	sidebarW    = 200
	catTop      = 48 // sidebar title band above the category list
	catRowH     = 34 // sidebar list row height (item + section-header rows)
	sidePad     = 16 // sidebar text inset
	titleTop    = 22 // "Settings" title baseline band (for the AA-text proof)
	cardMarginX = 20 // card inset from the content-area edges
	cardTop     = 56 // page-title band above the settings card
	// Switch + slider share a compact 24px control height so their knobs read
	// as one family; the SettingRow right-aligns them inside the card.
	switchW = 44
	switchH = 24
	scaleW  = 180
	scaleH  = 24
	// Sidebar iconoir badge sizing: an 18px glyph inset catIconPad from the row
	// edge, with catTextGap before the category label.
	catIconSize = 18
	catIconPad  = 10
	catTextGap  = 8
)

// sidebarGroups partitions the categories (in their model order) into the
// ListBox's sections: each entry contributes one section header caption and its
// next n categories, so the flat selectable-item index the ListBox reports
// equals the category index. The counts sum to len(State.cats).
var sidebarGroups = []struct {
	title string
	n     int
}{
	{"System", 1},
	{"Network", 1},
	{"Media", 2},
	{"General", 1},
}

// catIcons are the iconoir stems for each category, in category order. Every
// stem is a real iconoir.Names() entry (guarded by a test).
var catIcons = []string{"palette", "wifi", "sound-high", "hd-display", "settings"}

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
				row.sw.SetBounds(toolkit.Rect{W: switchW, H: switchH})
				row.sw.On().SubscribeChanged(func() { s.dirty = true })
			case rowScale:
				row.sc = toolkit.NewScale(0, 100, 60)
				row.sc.SetBounds(toolkit.Rect{W: scaleW, H: scaleH})
				row.sc.Value().SubscribeChanged(func() { s.dirty = true })
			}
		}
	}

	s.buildTree()
	return s
}

// buildTree assembles the stock-widget container tree over the category model
// and lays it out once with a single root.SetBounds.
func (s *State) buildTree() {
	// Sidebar: a sectioned ListBox under the fixed "Settings" title band. The
	// sections group the categories (see sidebarGroups); the flat item index the
	// ListBox reports is the category index.
	var sections []toolkit.ListSection
	ci := 0
	for _, g := range sidebarGroups {
		names := make([]string, g.n)
		for k := 0; k < g.n; k++ {
			names[k] = s.cats[ci].name
			ci++
		}
		sections = append(sections, toolkit.ListSection{Title: g.title, Items: names})
	}
	s.list = toolkit.NewSectionedListBox(sections...)
	s.list.RowHeight = catRowH
	s.list.Selected().Set(s.selected)
	s.itemLabel = toolkit.NewLabel("")
	// ItemRenderer paints each category row as an iconoir badge + label; the
	// ListBox owns the row background + selection highlight and hands us the
	// resolved ink (OnSurface, or the accent-on ink when selected).
	s.list.ItemRenderer = func(p painter.Painter, th *toolkit.Theme, rc toolkit.Rect, index int, item string, selected bool, ink toolkit.RGBA) {
		ir := toolkit.Rect{X: rc.X + catIconPad, Y: rc.Y + (rc.H-catIconSize)/2, W: catIconSize, H: catIconSize}
		iconoir.Draw(p, ir, catIcons[index], ink)
		tx := ir.X + ir.W + catTextGap
		s.itemLabel.Text().Set(item)
		s.itemLabel.Ink = ink
		s.itemLabel.SetBounds(toolkit.Rect{X: tx, Y: rc.Y, W: rc.X + rc.W - tx - catIconPad, H: rc.H})
		s.itemLabel.Draw(p, th)
	}
	s.list.OnActivate = func(idx int) { s.selectCat(idx) }

	sidebar := toolkit.NewVBox()
	sidebar.Spacing = -1
	sidebar.AddFixed(toolkit.NewContainer(nil), catTop) // title band spacer
	sidebar.AddFlex(s.list, 1)

	s.title = toolkit.NewLabel("Settings")
	s.title.Ink = s.theme.OnBackground

	// Content: a card-layout stack of one page per category; only the selected
	// page draws + receives events. Each page is a title Label above a
	// SettingsGroup of SettingRows.
	s.cards = &toolkit.CardLayout{Active: s.selected}
	s.content = toolkit.NewContainer(s.cards)
	for ci := range s.cats {
		var rows []*toolkit.SettingRow
		for ri := range s.cats[ci].rows {
			src := &s.cats[ci].rows[ri]
			var ctrl toolkit.Widget
			if src.kind == rowSwitch {
				ctrl = src.sw
			} else {
				ctrl = src.sc
			}
			rows = append(rows, toolkit.NewSettingRow(src.title, ctrl))
		}
		pg := &page{
			title: toolkit.NewLabel(s.cats[ci].name),
			group: toolkit.NewSettingsGroup("", rows...),
		}
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
// the newly-active page (and its controls) get laid out, and moves the sidebar
// selection to match. A no-op when the target is already selected. Sets dirty so
// a routed click reports a needed redraw.
func (s *State) selectCat(i int) {
	if s.selected == i {
		return
	}
	s.selected = i
	s.cards.Active = i
	s.list.Selected().Set(i)
	s.content.SetBounds(s.content.Bounds()) // re-run CardLayout for the new active page
	s.dirty = true
}

// --- content page ---------------------------------------------------------

// page is one category's content surface: the page-title Label above a
// SettingsGroup card. It composes the two stock widgets, positions them (title
// band then card), and routes clicks into the group's local space. It draws no
// chrome of its own — the card frame, rows, dividers and title glyphs all come
// from the toolkit widgets.
type page struct {
	toolkit.Base
	title *toolkit.Label
	group *toolkit.SettingsGroup
}

// SetBounds places the title in the top band and the settings card below it,
// inset by cardMarginX and sized to the group's measured height.
func (pg *page) SetBounds(b toolkit.Rect) {
	pg.Base.SetBounds(b)
	innerX := b.X + cardMarginX
	innerW := b.W - 2*cardMarginX
	pg.title.SetBounds(toolkit.Rect{X: innerX, Y: b.Y, W: innerW, H: cardTop})
	pg.group.SetBounds(toolkit.Rect{X: innerX, Y: b.Y + cardTop, W: innerW, H: pg.group.Measure(innerW)})
}

// Draw paints the title then the settings card (which lays its rows out).
func (pg *page) Draw(p painter.Painter, th *toolkit.Theme) {
	pg.title.Draw(p, th)
	pg.group.Draw(p, th)
}

// OnEvent forwards the event to the SettingsGroup, translated from page-local
// into the group's local coordinate space. The group drops clicks that miss a
// row (e.g. the title band), so an above-card click mutates nothing.
func (pg *page) OnEvent(ev toolkit.Event) {
	b, gb := pg.Bounds(), pg.group.Bounds()
	ev.X += b.X - gb.X
	ev.Y += b.Y - gb.Y
	pg.group.OnEvent(ev)
}

// Children exposes the title + card so accessibility / tree walkers descend
// into the page's composed widgets.
func (pg *page) Children() []toolkit.Widget {
	return []toolkit.Widget{pg.title, pg.group}
}

// --- scene plumbing -------------------------------------------------------

// Render paints the whole panel: the grey window ground and the sidebar/content
// hairline (both via the toolkit Backdrop, not raw shape-ops), the container
// tree, then the fixed "Settings" title Label on top of the sidebar title band.
func Render(s *State, buf []byte) {
	p := painter.NewPixelPainter(buf, s.W, s.H)
	th := s.theme
	fillBox(p, toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H}, th.Background)
	fillBox(p, toolkit.Rect{X: sidebarW, Y: 0, W: 1, H: s.H}, th.Border)
	s.root.Draw(p, th)
	s.title.SetBounds(toolkit.Rect{X: sidePad, Y: 0, W: sidebarW - sidePad, H: catTop})
	s.title.Draw(p, th)
}

// HandleMouse routes a click at surface coordinates (x, y) through the container
// tree. A click in the sidebar selects a category; a click in the content pane
// toggles a switch row or moves a slider. Returns true if the click mutated the
// model (the scene should re-render).
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
