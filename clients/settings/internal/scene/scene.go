// SPDX-License-Identifier: BSD-3-Clause
//
// Package scene renders the wasmdesk Settings panel in the WhiteSur (macOS Big
// Sur) style: a grey category sidebar on the left and a white content pane on
// the right whose rows carry toolkit Switch / Scale controls -- the libadwaita
// / System-Settings layout. It validates that the toolkit's Switch + Scale
// compose into a real preferences surface driven by (sidebar select -> page
// switch) and (row control -> model update).

package scene

import (
	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
)

// rowKind selects which control a settings row carries.
type rowKind int

const (
	rowSwitch rowKind = iota
	rowScale
)

// settingRow is one preference: a title plus exactly one control.
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

// State bundles the sidebar model + every row's control widget.
type State struct {
	W, H     int
	theme    *toolkit.Theme
	cats     []category
	selected int
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
	s.layout()
	return s
}

// cardRect is the white grouped card that holds a category's rows.
func (s *State) cardRect(numRows int) toolkit.Rect {
	x := sidebarW + cardMarginX
	return toolkit.Rect{X: x, Y: cardTop, W: s.W - x - cardMarginX, H: numRows * rowH}
}

// layout assigns each row control its absolute bounds inside its category's
// card. Every category is laid out once (only the selected one is drawn /
// hit-tested).
func (s *State) layout() {
	for ci := range s.cats {
		card := s.cardRect(len(s.cats[ci].rows))
		for ri := range s.cats[ci].rows {
			row := &s.cats[ci].rows[ri]
			ry := card.Y + ri*rowH
			switch row.kind {
			case rowSwitch:
				row.sw.SetBounds(toolkit.Rect{
					X: card.X + card.W - rowPadX - switchW,
					Y: ry + (rowH-switchH)/2, W: switchW, H: switchH,
				})
			case rowScale:
				row.sc.SetBounds(toolkit.Rect{
					X: card.X + card.W - rowPadX - scaleW,
					Y: ry + (rowH-scaleH)/2, W: scaleW, H: scaleH,
				})
			}
		}
	}
}

// Render paints the whole panel.
func Render(s *State, buf []byte) {
	p := painter.NewPixelPainter(buf, s.W, s.H)
	th := s.theme
	onAccent := th.Extra["accent_fg_color"]
	if onAccent == (toolkit.RGBA{}) {
		onAccent = toolkit.RGB(0xff, 0xff, 0xff)
	}

	// Grey window ground; a hairline splits the sidebar from the content area.
	p.FillRect(toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H}, th.Background)
	p.FillRect(toolkit.Rect{X: sidebarW, Y: 0, W: 1, H: s.H}, th.Border)

	// Sidebar: title + category rows (selected row = rounded accent pill).
	toolkit.DrawText(p, sidePad, titleTop, "Settings", th.OnBackground)
	for i, c := range s.cats {
		ry := catTop + i*catRowH
		ink := th.OnBackground
		if i == s.selected {
			p.FillRoundRect(toolkit.Rect{X: catMargin, Y: ry, W: sidebarW - 2*catMargin, H: catRowH - 4}, 7, th.Accent)
			ink = onAccent
		}
		toolkit.DrawText(p, sidePad+5, ry+(catRowH-4-toolkit.GlyphHeight)/2, c.name, ink)
	}

	// Content: page title, then the selected category's rows grouped inside a
	// white rounded card with inset dividers between rows (macOS Settings).
	cat := s.cats[s.selected]
	toolkit.DrawText(p, sidebarW+cardMarginX, titleTop, cat.name, th.OnSurface)
	card := s.cardRect(len(cat.rows))
	p.FillRoundRect(card, cardRadius, th.Surface)
	p.StrokeRoundRect(card, cardRadius, th.Border, 1)
	for ri := range cat.rows {
		row := cat.rows[ri]
		ry := card.Y + ri*rowH
		toolkit.DrawText(p, card.X+rowPadX, ry+(rowH-toolkit.GlyphHeight)/2, row.title, th.OnSurface)
		if ri < len(cat.rows)-1 {
			p.FillRect(toolkit.Rect{X: card.X + rowPadX, Y: ry + rowH - 1, W: card.W - 2*rowPadX, H: 1}, th.Border)
		}
		switch row.kind {
		case rowSwitch:
			row.sw.Draw(p, th)
		case rowScale:
			row.sc.Draw(p, th)
		}
	}
}

// HandleMouse routes a click. A click in the sidebar selects a category; a
// click in the content pane is forwarded (in widget-local coordinates) to
// whichever row control it lands on. Clicking anywhere on a switch row toggles
// it, for a comfortable macOS-sized hit target. Returns true if a redraw is
// needed.
func (s *State) HandleMouse(x, y int) bool {
	if x < sidebarW {
		for i := range s.cats {
			ry := catTop + i*catRowH
			if y >= ry && y < ry+catRowH {
				if s.selected != i {
					s.selected = i
					return true
				}
				return false
			}
		}
		return false
	}
	cat := &s.cats[s.selected]
	card := s.cardRect(len(cat.rows))
	for ri := range cat.rows {
		row := &cat.rows[ri]
		var b toolkit.Rect
		if row.kind == rowSwitch {
			b = row.sw.Bounds()
		} else {
			b = row.sc.Bounds()
		}
		if x >= b.X && x < b.X+b.W && y >= b.Y && y < b.Y+b.H {
			ev := toolkit.Event{Kind: toolkit.EventClick, X: x - b.X, Y: y - b.Y}
			if row.kind == rowSwitch {
				row.sw.OnEvent(ev)
			} else {
				row.sc.OnEvent(ev)
			}
			return true
		}
		ry := card.Y + ri*rowH
		if row.kind == rowSwitch && y >= ry && y < ry+rowH && x >= card.X && x < card.X+card.W {
			row.sw.OnEvent(toolkit.Event{Kind: toolkit.EventClick})
			return true
		}
	}
	return false
}

// HandleKey lets Up/Down move between categories. Returns true if the selection
// changed.
func (s *State) HandleKey(code string) bool {
	switch code {
	case "ArrowDown":
		if s.selected < len(s.cats)-1 {
			s.selected++
			return true
		}
	case "ArrowUp":
		if s.selected > 0 {
			s.selected--
			return true
		}
	}
	return false
}
