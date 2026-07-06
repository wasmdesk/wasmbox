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

// Layout constants (pixels).
const (
	sidebarW   = 180
	catTop     = 48
	catRowH    = 36
	catMargin  = 8
	contentPad = 22
	titleTop   = 20
	contentTop = 58
	rowH       = 52
	switchW    = 46
	switchH    = 26
	scaleW     = 150
	scaleH     = 22
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

// layout assigns each row control its absolute bounds. Row index alone drives
// the vertical position, so every category's controls are laid out once (only
// the selected category is drawn / hit-tested).
func (s *State) layout() {
	contentX := sidebarW
	contentW := s.W - sidebarW
	for ci := range s.cats {
		for ri := range s.cats[ci].rows {
			row := &s.cats[ci].rows[ri]
			ry := contentTop + ri*rowH
			switch row.kind {
			case rowSwitch:
				row.sw.SetBounds(toolkit.Rect{
					X: contentX + contentW - contentPad - switchW,
					Y: ry + (rowH-switchH)/2, W: switchW, H: switchH,
				})
			case rowScale:
				row.sc.SetBounds(toolkit.Rect{
					X: contentX + contentW - contentPad - scaleW,
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
	contentX := sidebarW
	onAccent := th.Extra["accent_fg_color"]
	if onAccent == (toolkit.RGBA{}) {
		onAccent = toolkit.RGB(0xff, 0xff, 0xff)
	}

	// Sidebar ground + white content pane + hairline divider between them.
	p.FillRect(toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H}, th.Background)
	p.FillRect(toolkit.Rect{X: contentX, Y: 0, W: s.W - contentX, H: s.H}, th.Surface)
	p.FillRect(toolkit.Rect{X: contentX, Y: 0, W: 1, H: s.H}, th.Border)

	// Sidebar: title + category rows (selected row = accent pill).
	toolkit.DrawText(p, contentPad-4, titleTop, "Settings", th.OnBackground)
	for i, c := range s.cats {
		ry := catTop + i*catRowH
		ink := th.OnBackground
		if i == s.selected {
			p.FillRoundRect(toolkit.Rect{X: catMargin, Y: ry, W: sidebarW - 2*catMargin, H: catRowH - 4}, 8, th.Accent)
			ink = onAccent
		}
		toolkit.DrawText(p, contentPad, ry+(catRowH-4-toolkit.GlyphHeight)/2, c.name, ink)
	}

	// Content: page title + divider, then the selected category's rows.
	cat := s.cats[s.selected]
	toolkit.DrawText(p, contentX+contentPad, titleTop, cat.name, th.OnSurface)
	p.FillRect(toolkit.Rect{X: contentX + contentPad, Y: titleTop + toolkit.GlyphHeight + 8, W: s.W - contentX - 2*contentPad, H: 1}, th.Border)
	for ri := range cat.rows {
		row := cat.rows[ri]
		ry := contentTop + ri*rowH
		toolkit.DrawText(p, contentX+contentPad, ry+(rowH-toolkit.GlyphHeight)/2, row.title, th.OnSurface)
		p.FillRect(toolkit.Rect{X: contentX + contentPad, Y: ry + rowH - 1, W: s.W - contentX - 2*contentPad, H: 1}, th.Border)
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
		ry := contentTop + ri*rowH
		if row.kind == rowSwitch && y >= ry && y < ry+rowH {
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
