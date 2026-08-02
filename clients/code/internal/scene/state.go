// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's state.go is the VS Code-inspired editor's UI state and
// widget tree. The editing model is a go-widgets/toolkit TextView (a superset
// of the previous hand-rolled TextBuffer: it ships lines + cursor + insert /
// split / backspace + undo/redo + selection); the sidebar is a ListBox, the
// tab band a small tabStrip widget, the status bar a Statusbar, and the Live
// Server popup a Dialog with an Entry + Button. HandleKey / HandleMouse route
// input into that tree; Render (render.go) paints it.
//
// Pure Go, no syscall/js, so the whole package builds + tests natively on
// every architecture this repo targets.

package scene

import (
	"strconv"
	"sync"

	"github.com/go-opentype/fonts/jetbrainsmono"
	"github.com/go-widgets/toolkit"
	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

// FlashKind classifies the kind of status-bar flash to paint. Default is
// no flash; SaveOK is the green "saved" pulse the Cmd+S handler triggers;
// Info is the neutral blue flash the Live Server stub uses.
type FlashKind int

const (
	// FlashNone is the default: no flash, status bar paints its idle blue.
	FlashNone FlashKind = iota
	// FlashSaveOK is the green pulse Cmd+S triggers on a successful Write.
	FlashSaveOK
	// FlashInfo is the neutral pulse the Live Server stub triggers.
	FlashInfo
)

// TickPerFlash is the (notional) number of HandleTick calls a flash lasts.
// The wasm side clears the flash on the next interaction; the constant is
// exported so a future timer-driven renderer can pin the duration.
const TickPerFlash = 60

// editorFontPx is the pixel size of the anti-aliased monospace face the editor
// renders. JetBrains Mono at 20px has a fixed 12px advance -- byte-identical to
// the previous ×2 bitmap font's advance -- so every glyph column lands exactly
// where it did before (the TextView caret + placeCursorAt hit-test + the
// syntax-highlight probe are all advance-driven), while the glyphs are now
// crisp anti-aliased vectors instead of the compiled-in 5×7 bitmap. A
// monospace face is mandatory: the proportional AA default (Atkinson, via
// toolkit.UseOpenTypeText) would break the editor's column/caret alignment.
const editorFontPx = 20

// editorFace lazily builds + caches the anti-aliased JetBrains Mono face once
// per process. NewTrueTypeFont's error is documented as never returned for a
// well-formed bundled blob; on the impossible parse-failure path we fall back
// to the still-legible ×2 bitmap so the editor degrades to bitmap text, never
// to no text at all.
var (
	editorFaceOnce sync.Once
	editorFaceInst toolkit.Font
)

func editorFace() toolkit.Font {
	editorFaceOnce.Do(func() { editorFaceInst = buildMonoFace(jetbrainsmono.TTF, editorFontPx) })
	return editorFaceInst
}

// buildMonoFace parses ttf into an anti-aliased face at px, falling back to the
// ×2 bitmap when the blob cannot be parsed. Split from editorFace so the
// (never-reached-in-production) parse-failure branch is unit-testable with a
// deliberately malformed blob.
func buildMonoFace(ttf []byte, px int) toolkit.Font {
	f, err := toolkit.NewTrueTypeFont(ttf, px)
	if err != nil {
		return toolkit.NewBitmapFont(2)
	}
	return f
}

// SceneState is the top-of-package handle the wasm entry point holds. The
// renderer reads every field; mutation funnels through HandleKey /
// HandleMouse so the cursor + flash + popup stay coherent.
type SceneState struct {
	W, H int
	VFS  sharedvfs.VFS

	// FileTree is the cached top-level (sorted, dirs-first) listing of the
	// VFS root, mirrored into the sidebar ListBox's Items. Refreshed when the
	// user writes a file so the sidebar reflects the latest tree.
	FileTree []sharedvfs.Entry

	// CurrentPath is the path of the file shown in the editor pane, or ""
	// when no file is open (the tab then shows "untitled" and saves no-op).
	CurrentPath string

	// Flash drives the bottom-status-bar pulse; FlashKind picks the colour.
	Flash FlashKind

	// LiveServerPopupOpen reports whether the "Connect to Live Server"
	// popup (a Dialog) is currently shown.
	LiveServerPopupOpen bool

	// LiveServerURL is the buffered "wss://..." URL shown in the popup's
	// Entry. v0 stores it but does not connect; Connect flashes + closes.
	LiveServerURL string

	// Editor is the toolkit TextView holding the editable document. Always
	// non-nil -- the constructor seeds an empty single-line buffer.
	Editor *toolkit.TextView

	// Widget tree (built by buildLayout).
	root    *toolkit.Dock
	body    *toolkit.HBox
	sidebar *toolkit.ListBox
	tabs    *tabStrip
	status  *toolkit.Statusbar

	// Live Server popup widgets.
	dialog     *toolkit.Dialog
	urlEntry   *toolkit.Entry
	connectBtn *toolkit.Button

	// Per-region themes (see render.go's package doc for why four are needed).
	sidebarTheme *toolkit.Theme
	editorTheme  *toolkit.Theme
	popupTheme   *toolkit.Theme

	// dirty is set by a routed control's callback (sidebar OnActivate, popup
	// Connect) so HandleMouse can report whether a click changed visible state.
	dirty bool
}

// New constructs a SceneState for a width x height pixel surface backed by
// the demo (in-memory) VFS, with no file open.
func New(width, height int) *SceneState {
	return NewWithVFS(width, height, NewDemoVFS())
}

// NewWithVFS is the explicit constructor: builds a SceneState around the
// caller-supplied VFS so the wasm boot path can hand in an IDB-backed
// instance for persistence.
func NewWithVFS(width, height int, vfs sharedvfs.VFS) *SceneState {
	// The whole toolkit lays out + paints the chrome (tab label, gutter, status
	// bar) against one active font, and placeCursorAt's click-to-caret math
	// reads the SAME global metrics the TextView paints with -- so the mono face
	// is installed BOTH process-globally (chrome + hit-test) and on the editor
	// widget itself, pointing at one shared instance so both stay in lockstep.
	mono := editorFace()
	toolkit.SetFont(mono)

	s := &SceneState{W: width, H: height, VFS: vfs}

	s.Editor = toolkit.NewTextView("")
	s.Editor.SetFont(mono)
	s.Editor.ShowLineNumbers = true
	s.Editor.GutterColor = rgb(ColorGutterText)
	s.Editor.Highlighter = Highlight
	s.Editor.Focused = true

	s.sidebar = toolkit.NewListBox(nil)
	s.sidebar.RowHeight = SidebarRowHeight
	s.sidebar.OnActivate = s.activateSidebar

	s.tabs = &tabStrip{label: s.tabLabel}

	s.status = toolkit.NewStatusbar([]string{"", "", "", ""})

	s.urlEntry = toolkit.NewEntry("")
	s.urlEntry.Placeholder = "wss://"
	s.connectBtn = toolkit.NewButton("Connect", func() {
		s.LiveServerURL = ""
		s.LiveServerPopupOpen = false
		s.Flash = FlashInfo
		s.dirty = true
	})
	s.dialog = toolkit.NewDialog("Connect to Live Server", s.urlEntry, s.connectBtn)

	s.sidebarTheme = &toolkit.Theme{
		Surface:    rgb(ColorSidebarBG),
		OnSurface:  rgb(ColorSidebarTextDim),
		Background: rgb(ColorStatusBarText), // selected-row ink (white)
		Accent:     rgb(ColorSelectionBG),   // selected-row background
		Border:     rgb(ColorSidebarBG),
	}
	s.editorTheme = &toolkit.Theme{
		Surface:   rgb(ColorWindowBG),
		OnSurface: rgb(ColorEditorText),
		Border:    rgb(ColorWindowBG),
		Accent:    rgb(ColorWindowBG),
	}
	s.popupTheme = &toolkit.Theme{
		Background: rgb(ColorPopupBG),
		Surface:    rgb(ColorWindowBG),
		SurfaceAlt: rgb(ColorTabStripBG),
		OnSurface:  rgb(ColorSidebarTextDim),
		Border:     rgb(ColorPopupBorder),
		Accent:     rgb(ColorPopupButton),
	}

	s.buildLayout()
	s.refreshTree()
	return s
}

// NewDemoVFS returns a fresh in-memory VFS seeded with the canonical demo
// tree. Pulled out so tests can construct the same VFS the wasm side gets.
func NewDemoVFS() sharedvfs.VFS {
	v := sharedvfs.NewInMemoryVFS()
	sharedvfs.SeedDemoTree(v)
	return v
}

// buildLayout assembles the widget tree + lays it out over the surface:
//
//	Dock(body) with the Statusbar docked South; body is an HBox of the
//	fixed-width sidebar + a flex VBox stacking the tab strip over the editor.
func (s *SceneState) buildLayout() {
	rightCol := toolkit.NewVBox()
	rightCol.AddFixed(s.tabs, TabStripHeight)
	rightCol.AddFlex(s.Editor, 1)

	s.body = toolkit.NewHBox()
	s.body.AddFixed(s.sidebar, SidebarWidth)
	s.body.AddFlex(rightCol, 1)

	s.root = toolkit.NewDock(s.body)
	s.root.Dock(s.status, toolkit.DockBottom, StatusBarHeight)
	s.root.SetBounds(toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H})
}

// layoutDialog centres the Live Server popup within the surface + lays out
// its content + buttons. Called before drawing or hit-testing the popup.
func (s *SceneState) layoutDialog() {
	s.dialog.SetBounds(toolkit.Rect{
		X: (s.W - DialogW) / 2,
		Y: (s.H - DialogH) / 2,
		W: DialogW,
		H: DialogH,
	})
}

// tabLabel is the active tab's text: the open file's basename, or "untitled".
func (s *SceneState) tabLabel() string {
	if s.CurrentPath == "" {
		return "untitled"
	}
	return basename(s.CurrentPath)
}

// refreshTree re-lists "/" + mirrors the entries into FileTree + the sidebar
// ListBox (folders get a "> " prefix, files two spaces, matching the old
// flat listing). Called at construction + after a successful Write.
func (s *SceneState) refreshTree() {
	entries, err := s.VFS.List("/")
	if err != nil {
		s.FileTree = nil
		s.sidebar.Items = nil
		return
	}
	s.FileTree = entries
	items := make([]string, len(entries))
	for i, e := range entries {
		prefix := "  "
		if e.IsDir {
			prefix = "> "
		}
		items[i] = prefix + e.Name
	}
	s.sidebar.Items = items
}

// activateSidebar is the ListBox OnActivate hook: it opens the clicked file,
// or no-ops on a directory row (setting dirty so HandleMouse can report
// whether the click changed visible state).
func (s *SceneState) activateSidebar(idx int) {
	if idx < 0 || idx >= len(s.FileTree) {
		s.dirty = false
		return
	}
	e := s.FileTree[idx]
	if e.IsDir {
		s.dirty = false
		return
	}
	s.dirty = s.OpenFile("/" + e.Name)
}

// OpenFile loads the file at path into the editor. The TextView buffer is
// replaced (cursor parks at 0,0), CurrentPath is updated, and any open popup /
// flash is dismissed. Returns true when the load succeeded.
func (s *SceneState) OpenFile(path string) bool {
	data, err := s.VFS.Read(path)
	if err != nil {
		return false
	}
	s.Editor.SetText(string(data))
	s.Editor.Focused = true
	s.CurrentPath = path
	s.Flash = FlashNone
	s.LiveServerPopupOpen = false
	return true
}

// SaveCurrent writes the current editor content back to CurrentPath. A no-op
// (returns false) when no file is open; on success it flashes green.
func (s *SceneState) SaveCurrent() bool {
	if s.CurrentPath == "" {
		return false
	}
	if err := s.VFS.Write(s.CurrentPath, []byte(s.Editor.Text())); err != nil {
		return false
	}
	s.Flash = FlashSaveOK
	s.refreshTree()
	return true
}

// editorSig captures the editor's buffer + cursor so HandleKey can report
// whether a key actually changed anything (a non-moving arrow returns false,
// preserving the old MoveCursor-driven re-render gate).
func (s *SceneState) editorSig() string {
	return s.Editor.Text() + "\x00" + strconv.Itoa(s.Editor.CursorLine) + "," + strconv.Itoa(s.Editor.CursorCol)
}

// HandleKey routes one DOM-style keydown into the editor. Returns true when
// the visible state changed so the caller decides whether to re-render. Cmd+S
// / Ctrl+S are pre-folded by the wasm entry point (see main.go); Tab inserts
// four spaces; navigation + Backspace + Enter + printable characters are
// dispatched into the TextView via toolkit Events.
func (s *SceneState) HandleKey(key string) bool {
	switch key {
	case "Cmd+S", "Ctrl+S":
		return s.SaveCurrent()
	}
	before := s.editorSig()
	switch key {
	case "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End", "Backspace", "Enter":
		s.Editor.OnEvent(toolkit.Event{Kind: toolkit.EventKeyDown, Code: key})
	case "Tab":
		s.Editor.OnEvent(toolkit.Event{Kind: toolkit.EventChar, Code: "    "})
	default:
		// Printable single character; anything longer is a modifier-named key
		// we don't handle (Alt, PageDown, F1, ...).
		if len(key) == 1 && key[0] >= 0x20 && key[0] < 0x7F {
			s.Editor.OnEvent(toolkit.Event{Kind: toolkit.EventChar, Code: key})
		} else {
			return false
		}
	}
	return s.editorSig() != before
}

// HandleMouse routes one mousedown into the widget tree. (x, y) are surface-
// local pixel coordinates. Hit-zones (checked in priority order):
//
//   - Live Server popup (when open): Connect flashes + closes; a click
//     outside the dialog dismisses; a click inside but not on Connect no-ops.
//   - Status bar: the right-most Live Server region opens the popup.
//   - Sidebar: routed into the ListBox, whose OnActivate opens the file row.
//   - Editor pane: places the caret at the clicked (line, col).
//
// Returns true when the visible state changed so the caller re-renders.
func (s *SceneState) HandleMouse(x, y int) bool {
	if s.LiveServerPopupOpen {
		return s.handlePopupMouse(x, y)
	}
	// Status bar (bottom band). The Live Server segment is the right region.
	if sb := s.status.Bounds(); y >= sb.Y && y < sb.Y+sb.H {
		if x >= s.W-LiveServerWidth {
			s.LiveServerPopupOpen = true
			return true
		}
		return false
	}
	// Sidebar: route the click into the ListBox (its OnActivate sets dirty).
	if sd := s.sidebar.Bounds(); sd.Contains(x, y) {
		s.dirty = false
		s.sidebar.OnEvent(toolkit.Event{Kind: toolkit.EventClick, X: x - sd.X, Y: y - sd.Y})
		return s.dirty
	}
	// Editor: place the caret at the clicked position.
	if ed := s.Editor.Bounds(); ed.Contains(x, y) {
		s.placeCursorAt(x, y)
		return true
	}
	return false
}

// handlePopupMouse routes a click while the Live Server popup is open. A
// click on the Connect button (via the Dialog's own hit-test) flashes +
// closes; a click outside the dialog dismisses; a click inside but not on a
// button is a no-op (the URL Entry would take focus).
func (s *SceneState) handlePopupMouse(x, y int) bool {
	s.layoutDialog()
	db := s.dialog.Bounds()
	if db.Contains(x, y) {
		s.dirty = false
		s.dialog.OnEvent(toolkit.Event{Kind: toolkit.EventClick, X: x - db.X, Y: y - db.Y})
		return s.dirty
	}
	s.LiveServerPopupOpen = false
	return true
}

// placeCursorAt maps a surface pixel (x, y) inside the editor to a (line,
// col) and moves the caret there, mirroring the TextView's own paint metrics
// (gutter width + 4 px text inset, glyphHeight+4 line pitch, one advance per
// column). Row + col are clamped into the buffer's shape.
func (s *SceneState) placeCursorAt(x, y int) {
	r := s.Editor.Bounds()
	lineH := toolkit.GlyphHeight() + 4
	adv := toolkit.TextWidth(" ")
	gutterW := 0
	if s.Editor.ShowLineNumbers {
		gutterW = toolkit.TextWidth(strconv.Itoa(len(s.Editor.Lines))) + 8
	}
	textX := r.X + 4 + gutterW

	row := 0
	if lineH > 0 {
		row = (y - r.Y - 4) / lineH
	}
	if row < 0 {
		row = 0
	}
	if row >= len(s.Editor.Lines) {
		row = len(s.Editor.Lines) - 1
	}
	col := 0
	if adv > 0 {
		col = (x - textX) / adv
	}
	if col < 0 {
		col = 0
	}
	if rl := len([]rune(s.Editor.Lines[row])); col > rl {
		col = rl
	}
	s.Editor.CursorLine = row
	s.Editor.CursorCol = col
	s.Editor.Focused = true
}
