// SPDX-License-Identifier: BSD-3-Clause
//
// Package scene renders the Notepad — a multi-doc plain-text editor
// composed from the go-widgets/toolkit widgets. Layout (top to bottom):
//
//   +--------------------------------------------------+
//   | Toolbar   [N] [O] [S]  [C] [X] [V]  [?]           | ← North docked band
//   +--------------------------------------------------+
//   | +----------+ +------------------------------------+
//   | | Untitled | | // TextView with the current doc   |
//   | | notes.md | | // multiline, cursor, IME preview  |
//   | | todo.txt | |                                     |
//   | +----------+ +------------------------------------+
//   +--------------------------------------------------+
//   | 3 docs   ln 5, col 12   utf-8         Notepad     | ← South statusbar
//   +--------------------------------------------------+
//
// The chrome is arranged with the toolkit's box-layout container model — a
// Container driven by a BorderLayout — rather than hand-computed
// toolkit.Rect placement: the toolbar docks North (fixed height), the
// statusbar docks South (fixed height), the documents list docks West
// (fixed width), and the editor's TextView fills the Center. A single
// root.SetBounds lays the whole tree out, root.Draw paints it, and
// root.OnEvent routes clicks into child-local space — no per-widget rect
// arithmetic and no manual hit-testing. The transient notification toast
// is a free-floating overlay (host-positioned in the editor pane's top
// right), so it stays outside the border tree and is painted last on top.
//
// A real toolkit consumer: exercises Toolbar (icons), ListBox (docs
// panel), TextView (editor with IME), Statusbar (readout). No fake data;
// docs live only in memory (host integration with sharedvfs is deferred
// to a follow-up).

package scene

import (
	"strconv"
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
// Hyperlegible @16px, toolkit v0.77.0), so the toolbar, docs list, editor
// TextView and status bar render as the shaped vector face. The bundled face
// never fails to parse (the error is documented as never-returned); on the
// impossible error path the toolkit leaves the still-working bitmap default
// active, so a swallowed error degrades to legible bitmap text, never to none.
// Every widget centres its 20px line box within its band, so the taller face
// re-centres itself and no widget rect moves.
func enableAAText() { aaOnce.Do(func() { _ = toolkit.UseOpenTypeText() }) }

// Doc is one open document. Content persists via TextView.Text() when
// the user switches docs; Notepad restores it on switch-back.
type Doc struct {
	Title   string
	Content string
}

type State struct {
	W, H int

	theme   *toolkit.Theme
	root    *toolkit.Container
	toolbar *toolkit.Toolbar
	docs    *toolkit.ListBox
	editor  *toolkit.TextView
	status  *toolkit.Statusbar
	notify  *toolkit.Notification

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
//
// The widget tree mirrors the visual structure declaratively:
//
//	root  Container (BorderLayout)      — the whole scene, laid out N,S,W,Center
//	├─ toolbar  Toolbar   (North, fixed height toolbarH)
//	├─ status   Statusbar (South, fixed height statusH)
//	├─ docs     ListBox   (West,  fixed width  docsW)
//	└─ editor   TextView  (Center, fills the remainder)
//
// The fixed sizes become each Item's Size (the edge band's thickness); the
// editor is the Center region and absorbs whatever the edges leave. The
// notification toast is deliberately NOT a border region — it is a floating
// overlay drawn on top of the tree.
func New(w, h int) *State {
	enableAAText() // toolbar/list/editor/status render with the AA/shaped face.
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

	// Docs list on the left.
	items := make([]string, len(s.docSet))
	for i, d := range s.docSet {
		items[i] = d.Title
	}
	s.docs = toolkit.NewListBox(items)
	s.docs.Selected().Set(0)
	s.docs.OnActivate = func(idx int) { s.switchDoc(idx) }

	// Editor on the right.
	s.editor = toolkit.NewTextView(s.docSet[s.activeIdx].Content)
	s.editor.Focused = true
	s.editor.OnChange = func() { s.updateStatus() }

	// Status bar.
	s.status = toolkit.NewStatusbar([]string{"", "", "utf-8", "Notepad"})

	// Border-layout container: docked toolbar (North) + statusbar (South) +
	// docs list (West), the editor filling the Center. One SetBounds lays
	// out the whole tree.
	s.root = toolkit.NewContainer(toolkit.BorderLayout{}).
		Add(toolkit.Item{Widget: s.toolbar, Region: toolkit.RegionNorth, Size: toolbarH}).
		Add(toolkit.Item{Widget: s.status, Region: toolkit.RegionSouth, Size: statusH}).
		Add(toolkit.Item{Widget: s.docs, Region: toolkit.RegionWest, Size: docsW}).
		Add(toolkit.Item{Widget: s.editor, Region: toolkit.RegionCenter})
	s.root.SetBounds(toolkit.Rect{X: 0, Y: 0, W: w, H: h})

	s.updateStatus()

	// Notification toast (host-positioned in the top-right of the
	// editor pane). A floating overlay outside the border tree; reused
	// across notif() calls.
	s.notify = toolkit.NewNotification("")
	s.notify.SetBounds(toolkit.Rect{X: w - 220, Y: toolbarH + 8, W: 210, H: 24})

	return s
}

// Render paints the whole scene: a background fill, the container tree, then
// the notification toast on top.
func Render(s *State, buf []byte) {
	fill(buf, s.W, toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H}, s.theme.Background)
	p := painter.NewPixelPainter(buf, s.W, s.H)
	s.root.Draw(p, s.theme)
	s.notify.Draw(p, s.theme)
}

// HandleMouse routes a click at surface coordinates (x, y) through the
// container tree (translated into the root's local space). Whichever docked
// bar or the centre editor contains the point receives the event in its own
// local coordinates. Returns true so the scene re-renders.
func (s *State) HandleMouse(x, y int) bool {
	rb := s.root.Bounds()
	s.root.OnEvent(toolkit.Event{Kind: toolkit.EventClick, X: x - rb.X, Y: y - rb.Y})
	return true
}

// HandleKey routes to the editor when focused. Recognises the app's
// menu-bar shortcuts before falling through to the editor.
//
//	Ctrl+N   → new doc
//	Ctrl+S   → save current doc
//	Ctrl+PageDown / Ctrl+Tab      → next doc (wraps)
//	Ctrl+PageUp   / Ctrl+Shift+Tab → previous doc (wraps)
//
// Anything else forwards to the editor's own key dispatch.
func (s *State) HandleKey(code string) bool {
	switch code {
	case "Ctrl+N":
		s.newDoc()
		return true
	case "Ctrl+S":
		s.saveDoc()
		return true
	case "Ctrl+PageDown", "Ctrl+Tab":
		s.switchDoc(nextDocIdx(s.activeIdx, len(s.docSet)))
		return true
	case "Ctrl+PageUp", "Ctrl+Shift+Tab":
		s.switchDoc(prevDocIdx(s.activeIdx, len(s.docSet)))
		return true
	}
	s.editor.OnEvent(toolkit.Event{Kind: toolkit.EventKeyDown, Code: code})
	return true
}

func nextDocIdx(cur, n int) int {
	if n <= 0 {
		return 0
	}
	i := cur + 1
	if i >= n {
		i = 0
	}
	return i
}

func prevDocIdx(cur, n int) int {
	if n <= 0 {
		return 0
	}
	i := cur - 1
	if i < 0 {
		i = n - 1
	}
	return i
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
	s.docs.Selected().Set(s.activeIdx)
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
	s.docs.Selected().Set(idx)
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
