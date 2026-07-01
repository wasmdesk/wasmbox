// SPDX-License-Identifier: BSD-3-Clause
//
// Package scene renders the Notepad — a multi-doc plain-text editor
// composed from the wasmdesk/toolkit widgets. Layout (top to bottom):
//
//   +--------------------------------------------------+
//   | Toolbar   [N] [O] [S]  [C] [X] [V]  [?]           | ← DrawIcon* helpers
//   +--------------------------------------------------+
//   | +----------+ +------------------------------------+
//   | | Untitled | | // TextView with the current doc   |
//   | | notes.md | | // multiline, cursor, IME preview  |
//   | | todo.txt | |                                     |
//   | +----------+ +------------------------------------+
//   +--------------------------------------------------+
//   | 3 docs   ln 5, col 12   utf-8         Notepad     | ← Statusbar
//   +--------------------------------------------------+
//
// A real toolkit consumer: exercises Toolbar (icons), ListBox (docs
// panel), TextView (editor with IME), Statusbar (readout), Paned
// (draggable split). No fake data; docs live only in memory (host
// integration with sharedvfs is deferred to a follow-up).

package scene

import (
	"strconv"

	"github.com/wasmdesk/toolkit"
)

// Doc is one open document. Content persists via TextView.Text() when
// the user switches docs; Notepad restores it on switch-back.
type Doc struct {
	Title   string
	Content string
}

type State struct {
	W, H int

	theme    *toolkit.Theme
	toolbar  *toolkit.Toolbar
	docs     *toolkit.ListBox
	editor   *toolkit.TextView
	status   *toolkit.Statusbar
	notify   *toolkit.Notification

	docSet    []Doc
	activeIdx int
}

// Layout constants.
const (
	toolbarH = 28
	statusH  = 18
	docsW    = 140
	iconW    = 24
)

// New builds a Notepad with two demo docs so the first-run experience
// is not blank.
func New(w, h int) *State {
	s := &State{W: w, H: h, theme: toolkit.DefaultLight()}
	s.docSet = []Doc{
		{Title: "Untitled", Content: "# Notepad\n\nA toolkit-consumer sample app.\n\n- Click a doc on the left to switch.\n- Type here to edit; content persists across switches.\n"},
		{Title: "todo.txt", Content: "milk\nbread\nread the toolkit README\n"},
	}
	s.activeIdx = 0

	// Toolbar: New, Open, Save + separator + Cut, Copy, Paste +
	// separator + Search. Each button's OnClick wires a scene action.
	s.toolbar = toolkit.NewToolbar([]toolkit.ToolbarItem{
		{Label: "N", OnClick: func() { s.newDoc() }},
		{Label: "O", OnClick: func() { s.notif("Open: no filesystem yet") }},
		{Label: "S", OnClick: func() { s.saveDoc() }},
		{Separator: true},
		{Label: "C", OnClick: func() { s.notif("Copy: select first (drag not wired)") }},
		{Label: "X", OnClick: func() { s.notif("Cut: select first") }},
		{Label: "V", OnClick: func() { s.notif("Paste: no clipboard bridge yet") }},
		{Separator: true},
		{Label: "?", OnClick: func() { s.notif("Notepad v0.1 — a toolkit consumer") }},
	})
	s.toolbar.SetBounds(toolkit.Rect{X: 0, Y: 0, W: w, H: toolbarH})

	// Docs list on the left.
	items := make([]string, len(s.docSet))
	for i, d := range s.docSet {
		items[i] = d.Title
	}
	s.docs = toolkit.NewListBox(items)
	s.docs.Selected = 0
	s.docs.OnActivate = func(idx int) { s.switchDoc(idx) }
	s.docs.SetBounds(toolkit.Rect{X: 0, Y: toolbarH, W: docsW, H: h - toolbarH - statusH})

	// Editor on the right.
	s.editor = toolkit.NewTextView(s.docSet[s.activeIdx].Content)
	s.editor.Focused = true
	s.editor.OnChange = func() { s.updateStatus() }
	s.editor.SetBounds(toolkit.Rect{X: docsW, Y: toolbarH, W: w - docsW, H: h - toolbarH - statusH})

	// Status bar.
	s.status = toolkit.NewStatusbar([]string{"", "", "utf-8", "Notepad"})
	s.status.SetBounds(toolkit.Rect{X: 0, Y: h - statusH, W: w, H: statusH})
	s.updateStatus()

	// Notification toast (host-positioned in the top-right of the
	// editor pane). Reused across notif() calls.
	s.notify = toolkit.NewNotification("")
	s.notify.SetBounds(toolkit.Rect{X: w - 220, Y: toolbarH + 8, W: 210, H: 24})

	return s
}

// Render paints every widget in draw-order (bg → toolbar → docs list →
// editor → status → notification on top).
func Render(s *State, buf []byte) {
	fill(buf, s.W, toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H}, s.theme.Background)
	s.toolbar.Draw(buf, s.W, s.theme)
	s.docs.Draw(buf, s.W, s.theme)
	s.editor.Draw(buf, s.W, s.theme)
	s.status.Draw(buf, s.W, s.theme)
	s.notify.Draw(buf, s.W, s.theme)
}

// HandleMouse dispatches a click at (x, y) to whichever pane it lands
// in. Returns true if the scene should re-render.
func (s *State) HandleMouse(x, y int) bool {
	ev := toolkit.Event{Kind: toolkit.EventClick, X: x, Y: y}
	switch {
	case insideRect(x, y, s.toolbar.Bounds()):
		s.toolbar.OnEvent(localize(ev, s.toolbar.Bounds()))
	case insideRect(x, y, s.docs.Bounds()):
		r := s.docs.Bounds()
		local := ev
		local.X -= r.X
		local.Y -= r.Y
		s.docs.OnEvent(local)
	case insideRect(x, y, s.editor.Bounds()):
		s.editor.OnEvent(localize(ev, s.editor.Bounds()))
	}
	return true
}

// HandleKey routes to the editor when focused. Ctrl+S = save.
func (s *State) HandleKey(code string) bool {
	if code == "Ctrl+S" {
		s.saveDoc()
		return true
	}
	s.editor.OnEvent(toolkit.Event{Kind: toolkit.EventKeyDown, Code: code})
	return true
}

// HandleChar forwards printable input to the editor.
func (s *State) HandleChar(text string) bool {
	s.editor.OnEvent(toolkit.Event{Kind: toolkit.EventChar, Code: text})
	s.updateStatus()
	return true
}

// Tick drives the notification's Life countdown. Called from the wasm
// main's animation loop.
func (s *State) Tick() { s.notify.Tick() }

// --- actions --------------------------------------------------------------

func (s *State) newDoc() {
	// Persist current before switching.
	s.docSet[s.activeIdx].Content = s.editor.Text()
	title := "Untitled " + strconv.Itoa(len(s.docSet)+1)
	s.docSet = append(s.docSet, Doc{Title: title, Content: ""})
	s.activeIdx = len(s.docSet) - 1
	s.docs.Items = append(s.docs.Items, title)
	s.docs.Selected = s.activeIdx
	s.editor.SetText("")
	s.updateStatus()
	s.notif("New doc: " + title)
}

func (s *State) saveDoc() {
	s.docSet[s.activeIdx].Content = s.editor.Text()
	s.notif("Saved (in-memory only)")
}

func (s *State) switchDoc(idx int) {
	if idx < 0 || idx >= len(s.docSet) {
		return
	}
	if idx == s.activeIdx {
		return
	}
	// Persist current.
	s.docSet[s.activeIdx].Content = s.editor.Text()
	// Switch.
	s.activeIdx = idx
	s.docs.Selected = idx
	s.editor.SetText(s.docSet[idx].Content)
	s.updateStatus()
}

func (s *State) updateStatus() {
	s.status.SetSegment(0, strconv.Itoa(len(s.docSet))+" docs")
	s.status.SetSegment(1, "ln "+strconv.Itoa(s.editor.CursorLine+1)+
		", col "+strconv.Itoa(s.editor.CursorCol+1))
}

// notif shows a transient toast.
func (s *State) notif(text string) { s.notify.Show(text) }

// --- helpers --------------------------------------------------------------

func fill(buf []byte, surfaceW int, r toolkit.Rect, c toolkit.RGBA) {
	for j := 0; j < r.H; j++ {
		for i := 0; i < r.W; i++ {
			off := ((r.Y+j)*surfaceW + (r.X + i)) * 4
			if off+3 >= len(buf) {
				return
			}
			buf[off+0] = c.R
			buf[off+1] = c.G
			buf[off+2] = c.B
			buf[off+3] = c.A
		}
	}
}

func insideRect(x, y int, r toolkit.Rect) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

func localize(ev toolkit.Event, r toolkit.Rect) toolkit.Event {
	ev.X -= r.X
	ev.Y -= r.Y
	return ev
}
