// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene renders a GNOME Nautilus-inspired file browser using the
// go-widgets/toolkit widget + container model rather than hand-computed
// painter.Rect placement. The whole chrome is one persistent widget tree:
//
//	root  Dock                                    — the browser shell
//	├─ (docked North, height HeaderBarHeight) band VBox
//	│  └─ (centre row) HBox
//	│     [ pad · ≡ · gap · < · gap · > · gap · Breadcrumbs(flex) ]
//	└─ (body / Centre)  body HBox
//	   ├─ (fixed SidebarWidth)  sidebar VBox
//	   │     [ pad · section · rows … · section · rows … ]     (sectioned)
//	   └─ (flex)  content VBox
//	         ├─ (fixed ColumnHeaderHeight) columnHeader     — Name / Size
//	         └─ (flex) fileList                              — one fileRow / entry
//
// A single root.SetBounds lays the whole tree out, root.Draw paints it, and
// root.OnEvent routes left-clicks into child-local space. The two things the
// toolkit's click-only Event model cannot express — a secondary (right) button
// and a double-click count — are dispatched at the scene level against the
// list's own widget bounds (rowAt), so no hand-computed hit rectangles remain.
//
// Layout constants + the WhiteSur palette are preserved byte-for-byte from the
// pre-migration hand-drawn renderer so the headless Playwright probes
// (test/probe-files*.mjs), which pin exact pixel colours + coordinates, keep
// passing. The custom leaf widgets (fileRow / sidebarRow / the file-type icon
// glyphs) draw those pixels; text is the toolkit's bitmap font via DrawText,
// replacing the old bespoke 8x8 font.

package scene

import (
	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
)

// Visual constants -- exported so tests + the Playwright probes can pin layout
// invariants. The defaults reproduce GTK4 / libadwaita proportions on a
// 720x440 surface.
const (
	// RowHeight is the vertical pitch of one entry row (Nautilus default ~32).
	RowHeight = 32
	// HeaderBarHeight is the height of the top header bar (hamburger + nav
	// buttons + breadcrumb path-bar).
	HeaderBarHeight = 44
	// ColumnHeaderHeight is the band that holds the Name / Size labels.
	ColumnHeaderHeight = 28
	// SidebarWidth is the width of the left navigation pane.
	SidebarWidth = 160
	// SidebarSectionHeaderHeight is the height of a section label band.
	SidebarSectionHeaderHeight = 22
	// SidebarRowHeight is the height of one navigation row.
	SidebarRowHeight = 28
	// SidebarTopPadding is the vertical gap between the header bar and the
	// first section label.
	SidebarTopPadding = 8
	// IconSize is the side length used to position list-row icons.
	IconSize = 18

	// NameColX is the left edge of the icon+name cell (surface coordinates).
	NameColX = SidebarWidth + 12
	// SizeColRight is the right edge of the right-aligned Size column.
	SizeColRight = 700

	// Header-bar control geometry (top inset + button height/width + gaps).
	headerBtnY = 10
	headerBtnH = 24
	headerBtnW = 28
	padLeft    = 8
	gapHamBack = 10
	gapBackFwd = 4
	gapFwdPath = 12

	// Preview overlay geometry: a panel centred in the right pane holding the
	// first PreviewMaxLines lines of a text file.
	PreviewWidth    = 360
	PreviewHeight   = 220
	PreviewPadding  = 12
	PreviewMaxLines = 18
)

// Palette. The values are the WhiteSur (macOS Big Sur) light-theme roles -- a
// white file "view" framed by a #f0f0f0 sidebar + #ebebeb header, WhiteSur's
// #0860F2 accent -- so Files matches wasmbox's --frame=aqua-whitesur chrome.
// The custom leaf widgets paint the pixel-exact regions the probes sample, so
// the palette lives here rather than leaning on the (differently-tuned) theme.
var (
	// ColorWindowBG is the right-pane background (WhiteSur view_bg #ffffff).
	ColorWindowBG = toolkit.RGB(255, 255, 255)
	// ColorSidebarBG is the left navigation pane background (#f0f0f0).
	ColorSidebarBG = toolkit.RGB(240, 240, 240)
	// ColorHeaderBarBG is the top header-bar background (#ebebeb).
	ColorHeaderBarBG = toolkit.RGB(235, 235, 235)
	// ColorDivider is the 1px line between chrome bands (#d3d3d3).
	ColorDivider = toolkit.RGB(211, 211, 211)
	// ColorTextPrimary is the default text ink (#242424).
	ColorTextPrimary = toolkit.RGB(36, 36, 36)
	// ColorTextSecondary is the dimmed ink for column metadata + section labels.
	ColorTextSecondary = toolkit.RGB(130, 130, 138)
	// ColorAccent is the focused-selection blue (#0860F2).
	ColorAccent = toolkit.RGB(8, 96, 242)
	// ColorOnAccent is the text/icon ink on top of the accent fill (white).
	ColorOnAccent = toolkit.RGB(255, 255, 255)
	// ColorFolderFill / ColorFolderTab / ColorFolderStroke paint the
	// Nautilus-style two-tone folder icon.
	ColorFolderFill   = toolkit.RGB(74, 134, 246)
	ColorFolderTab    = toolkit.RGB(120, 174, 250)
	ColorFolderStroke = toolkit.RGB(8, 80, 210)
	// ColorFilePaper / ColorFileBorder paint the page-with-folded-corner icon.
	ColorFilePaper  = toolkit.RGB(255, 255, 255)
	ColorFileBorder = toolkit.RGB(200, 203, 208)
	// colorStar is the Home bookmark's star glyph.
	colorStar = toolkit.RGB(240, 180, 70)
	// colorSelectedStroke is the icon stroke on the accent fill.
	colorSelectedStroke = toolkit.RGB(220, 230, 250)
)

// State bundles the navigation model, the persistent widget tree, and the
// transient overlays (context menu + text preview).
type State struct {
	W, H int

	theme *toolkit.Theme

	// Navigation model.
	VFS     VFS
	Browser *BrowserState
	Sidebar []SidebarEntry
	// SidebarSelected indexes into Sidebar; -1 means "no row is the active
	// location". The renderer highlights the selected row with the accent.
	SidebarSelected int

	// Widget tree.
	root        *toolkit.Dock
	crumbs      *toolkit.Breadcrumbs
	hamburger   *toolkit.IconButton
	backBtn     *toolkit.IconButton
	fwdBtn      *toolkit.IconButton
	sidebar     *toolkit.VBox
	sidebarRows []*sidebarRow
	content     *toolkit.VBox
	colHeader   *columnHeader
	list        *fileList

	// Overlays.
	ctxMenu       *toolkit.ContextMenu
	ctxTarget     string
	Preview       *PreviewOverlay
	previewBody   *previewText
	previewDialog *toolkit.Dialog

	// dirty is set by a control's click handler when a press mutates the
	// model, so HandleMouse can report to the render loop whether a click did
	// anything (the container routing itself has no hit/miss return value).
	dirty bool
}

// New constructs a State for a width x height pixel surface backed by the demo
// (in-memory) VFS, rooted at "/" with the first entry selected.
func New(width, height int) *State {
	return NewWithVFS(width, height, NewDemoVFS())
}

// NewWithVFS is the explicit constructor the wasm boot path uses: it builds a
// State around the caller-supplied VFS so the browser-side code can hand in an
// IndexedDB-backed instance for persistence.
func NewWithVFS(width, height int, vfs VFS) *State {
	bs := &BrowserState{CurrentPath: "/"}
	bs.Refresh(vfs)
	s := &State{
		W: width, H: height,
		theme:           toolkit.WhiteSurLight(),
		VFS:             vfs,
		Browser:         bs,
		Sidebar:         DefaultSidebar(),
		SidebarSelected: -1,
	}
	s.buildTree()
	s.syncSidebar()
	s.refreshView()
	return s
}

// buildTree assembles the persistent widget tree + overlays and lays it out.
func (s *State) buildTree() {
	// --- header band ------------------------------------------------------
	s.hamburger = toolkit.NewIconButton("=", func() { s.onHamburger() })
	s.backBtn = toolkit.NewIconButton("<", func() { s.onBack() })
	s.fwdBtn = toolkit.NewIconButton(">", func() {}) // v0 has no forward history
	s.crumbs = toolkit.NewBreadcrumbs(PathCrumbs(s.Browser.CurrentPath))

	row := toolkit.NewHBox()
	row.Spacing = 0
	row.AddFixed(spacer(), padLeft)
	row.AddFixed(s.hamburger, headerBtnW)
	row.AddFixed(spacer(), gapHamBack)
	row.AddFixed(s.backBtn, headerBtnW)
	row.AddFixed(spacer(), gapBackFwd)
	row.AddFixed(s.fwdBtn, headerBtnW)
	row.AddFixed(spacer(), gapFwdPath)
	row.AddFlex(s.crumbs, 1)

	band := toolkit.NewVBox()
	band.Spacing = 0
	band.AddFixed(spacer(), headerBtnY)
	band.AddFixed(row, headerBtnH)
	band.AddFixed(spacer(), HeaderBarHeight-headerBtnH-headerBtnY)

	// --- sidebar (sectioned) ---------------------------------------------
	s.sidebar = toolkit.NewVBox()
	s.sidebar.Spacing = 0
	s.sidebar.AddFixed(spacer(), SidebarTopPadding)
	prevSection := ""
	for i, e := range s.Sidebar {
		if e.Section != prevSection {
			s.sidebar.AddFixed(&sectionLabel{text: e.Section}, SidebarSectionHeaderHeight)
			prevSection = e.Section
		}
		r := &sidebarRow{s: s, i: i, e: e}
		s.sidebarRows = append(s.sidebarRows, r)
		s.sidebar.AddFixed(r, SidebarRowHeight)
	}

	// --- content (column header + list) ----------------------------------
	s.colHeader = &columnHeader{}
	s.list = &fileList{s: s}
	s.content = toolkit.NewVBox()
	s.content.Spacing = 0
	s.content.AddFixed(s.colHeader, ColumnHeaderHeight)
	s.content.AddFlex(s.list, 1)

	// --- body + shell -----------------------------------------------------
	body := toolkit.NewHBox()
	body.Spacing = 0
	body.AddFixed(s.sidebar, SidebarWidth)
	body.AddFlex(s.content, 1)

	s.root = toolkit.NewDock(body)
	s.root.Dock(band, toolkit.DockTop, HeaderBarHeight)
	s.root.SetBounds(toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H})

	// --- overlays ---------------------------------------------------------
	s.ctxMenu = toolkit.NewContextMenu(toolkit.NewMenu(nil))
	s.ctxMenu.SetBounds(toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H})
	s.previewBody = &previewText{}
	s.previewDialog = toolkit.NewDialog("", s.previewBody)
}

// spacer is an invisible, inert box cell used to carry a fixed gap.
func spacer() toolkit.Widget { return toolkit.NewContainer(nil) }

// refreshView re-syncs the derived widget state after a navigation: it rebuilds
// the list rows from the current listing + points the breadcrumbs at the new
// path. The bounds are stable across navigation (same surface) so no full
// re-layout is required beyond the list's own row placement.
func (s *State) refreshView() {
	s.list.setRows()
	s.crumbs.Segments = PathCrumbs(s.Browser.CurrentPath)
}

// Render paints the chrome grounds first (so the panes read as distinct even
// under the widgets' transparent text), then the widget tree, then any open
// overlay.
func Render(s *State, buf []byte) {
	need := 4 * s.W * s.H
	if len(buf) != need {
		panic("scene: Render buffer size mismatch")
	}
	p := painter.NewPixelPainter(buf, s.W, s.H)
	th := s.theme

	// Grounds: window (right pane) white, sidebar grey, header grey, hairlines.
	p.FillRect(toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H}, ColorWindowBG)
	p.FillRect(toolkit.Rect{X: 0, Y: HeaderBarHeight, W: SidebarWidth, H: s.H - HeaderBarHeight}, ColorSidebarBG)
	p.FillRect(toolkit.Rect{X: SidebarWidth - 1, Y: HeaderBarHeight, W: 1, H: s.H - HeaderBarHeight}, ColorDivider)
	p.FillRect(toolkit.Rect{X: 0, Y: 0, W: s.W, H: HeaderBarHeight}, ColorHeaderBarBG)
	p.FillRect(toolkit.Rect{X: 0, Y: HeaderBarHeight - 1, W: s.W, H: 1}, ColorDivider)

	// Widget tree.
	s.root.Draw(p, th)

	// Overlays on top of the navigation chrome.
	if s.ctxMenu.Open {
		s.ctxMenu.Draw(p, th)
	}
	if s.Preview != nil {
		s.previewDialog.Draw(p, th)
	}
}

// HandleKey routes one DOM-style keydown into the browser. Recognised keys:
//
//	"ArrowDown"  -> move cursor down
//	"ArrowUp"    -> move cursor up
//	"Enter"      -> descend into the selected folder
//	"Backspace"  -> go up
//	"Escape"     -> go up
//
// Returns true when the visible state changed.
func (s *State) HandleKey(key string) bool {
	switch key {
	case "ArrowDown":
		old := s.Browser.Cursor
		s.Browser.MoveCursor(1)
		return s.Browser.Cursor != old
	case "ArrowUp":
		old := s.Browser.Cursor
		s.Browser.MoveCursor(-1)
		return s.Browser.Cursor != old
	case "Enter":
		if s.Browser.ActivateCurrent(s.VFS) {
			s.syncSidebar()
			s.refreshView()
			return true
		}
		return false
	case "Backspace", "Escape":
		if s.Browser.GoUp(s.VFS) {
			s.syncSidebar()
			s.refreshView()
			return true
		}
		return false
	default:
		return false
	}
}

// HandleMouse is the legacy single-button handler: every click is a primary
// (left) single mousedown. New callers reach for HandleMouseButton.
func (s *State) HandleMouse(x, y int) bool {
	return s.HandleMouseButton(x, y, 0, 1)
}

// HandleMouseButton routes a surface-local mousedown into the browser. (x, y)
// are surface-local; button is the DOM button index (0=primary/left,
// 2=secondary/right); clickCount is 1 for single, 2 for double. Returns true
// when the visible state changed so the caller re-renders.
//
// Dispatch order:
//   - an open preview is consumed by the next click (dismiss + fall through);
//   - an open context menu owns the click (activate item / dismiss);
//   - a right button opens a context menu over the file list;
//   - a double-click on a previewable file opens the preview overlay;
//   - otherwise the click routes through the widget tree in root-local space.
func (s *State) HandleMouseButton(x, y, button, clickCount int) bool {
	// Any open preview is consumed by the very next click; the click still
	// drives whatever it lands on.
	had := s.Preview != nil
	s.Preview = nil

	// An open menu intercepts the next click: its OnEvent activates the hit
	// row's Action (which mutates the model) or dismisses on an outside click.
	if s.ctxMenu.Open {
		s.ctxMenu.OnEvent(toolkit.Event{Kind: toolkit.EventClick, X: x, Y: y})
		return true
	}

	// Right-click: open a row / empty-area context menu (needs the button).
	if button == 2 {
		if s.openContextMenuAt(x, y) {
			return true
		}
		return had
	}

	// Double-click on a previewable text file (needs the click count).
	if clickCount >= 2 && s.previewAt(x, y) {
		return true
	}

	// Normal left click: route through the widget tree.
	s.dirty = false
	rb := s.root.Bounds()
	s.root.OnEvent(toolkit.Event{Kind: toolkit.EventClick, X: x - rb.X, Y: y - rb.Y})
	return s.dirty || had
}

// previewAt opens the preview overlay when (x, y) lands on a previewable text
// file row. Returns true when a preview opened.
func (s *State) previewAt(x, y int) bool {
	row := s.list.rowAt(x, y)
	if row < 0 {
		return false
	}
	e := s.Browser.Entries[row]
	if e.IsDir || !hasTextExt(e.Name) {
		return false
	}
	s.Browser.Cursor = row
	s.openPreview(Join(s.Browser.CurrentPath, e.Name))
	return true
}

// openContextMenuAt pops a context menu when the right-click lands inside the
// file list: an Open/Rename/Delete menu on a row, or a New Folder/New File menu
// on the empty area below the rows. Returns false (no menu) elsewhere.
func (s *State) openContextMenuAt(x, y int) bool {
	if !s.list.Bounds().Contains(x, y) {
		return false
	}
	if row := s.list.rowAt(x, y); row >= 0 {
		s.Browser.Cursor = row
		e := s.Browser.Entries[row]
		s.ctxTarget = Join(s.Browser.CurrentPath, e.Name)
		s.buildEntryMenu()
	} else {
		s.ctxTarget = ""
		s.buildCreateMenu()
	}
	s.ctxMenu.Popup(x, y)
	return true
}

// buildEntryMenu fills the context menu with the row actions. Open activates a
// directory or previews a text file; Rename is a v0 stub; Delete removes the
// path. Each item's Action closes over the current ctxTarget.
func (s *State) buildEntryMenu() {
	target := s.ctxTarget
	s.ctxMenu.Menu.Items = []toolkit.MenuItem{
		{Label: "Open", Action: func() { s.applyMenuAction("open", target) }},
		{Label: "Rename", Action: func() { s.applyMenuAction("rename", target) }},
		{Label: "Delete", Action: func() { s.applyMenuAction("delete", target) }},
	}
}

// buildCreateMenu fills the context menu with the empty-area "create new"
// actions rooted at the current directory.
func (s *State) buildCreateMenu() {
	s.ctxMenu.Menu.Items = []toolkit.MenuItem{
		{Label: "New Folder", Action: func() { s.applyMenuAction("newfolder", "") }},
		{Label: "New File", Action: func() { s.applyMenuAction("newfile", "") }},
	}
}

// applyMenuAction runs the menu action keyed by id on target. For the create
// actions target is unused (the action creates a child of the current dir).
func (s *State) applyMenuAction(id, target string) {
	switch id {
	case "open":
		if s.VFS.IsDir(target) {
			s.Browser.CurrentPath = target
			s.Browser.Cursor = 0
			s.Browser.Refresh(s.VFS)
			s.syncSidebar()
			s.refreshView()
			return
		}
		if hasTextExt(Basename(target)) {
			s.openPreview(target)
		}
	case "delete":
		if target == "" {
			return
		}
		_ = s.VFS.Remove(target)
		s.Browser.Refresh(s.VFS)
		s.refreshView()
	case "rename":
		// v0 stub -- rename isn't wired yet (no inline editor).
	case "newfolder":
		s.createSibling(true)
	case "newfile":
		s.createSibling(false)
	}
}

// createSibling generates an unused name in the current directory and creates
// either an empty folder or an empty file. Returns silently on failure.
func (s *State) createSibling(isDir bool) {
	stem := "untitled"
	ext := ".txt"
	for i := 0; i < 1000; i++ {
		name := stem
		if isDir {
			ext = ""
		}
		if i > 0 {
			name = stem + "-" + itoa(i)
		}
		path := Join(s.Browser.CurrentPath, name+ext)
		if _, err := s.VFS.Stat(path); err == nil {
			continue
		}
		if isDir {
			_ = s.VFS.Mkdir(path)
		} else {
			_ = s.VFS.Write(path, nil)
		}
		s.Browser.Refresh(s.VFS)
		s.refreshView()
		return
	}
}

// openPreview reads target and stores it in s.Preview for the overlay. A
// missing file or read error leaves Preview cleared so the overlay disappears
// cleanly.
func (s *State) openPreview(path string) {
	data, err := s.VFS.Read(path)
	if err != nil {
		s.Preview = nil
		return
	}
	lines := splitLines(string(data), PreviewMaxLines)
	if len(lines) == 0 {
		lines = []string{"(empty file)"}
	}
	s.Preview = &PreviewOverlay{Path: path, Lines: lines}
	s.previewBody.lines = lines
	s.previewDialog.Title = Basename(path)
	s.layoutPreview()
}

// layoutPreview centres the preview dialog in the right pane, clamped so it
// never spills over the sidebar.
func (s *State) layoutPreview() {
	px := SidebarWidth + (s.W-SidebarWidth-PreviewWidth)/2
	if px < SidebarWidth+8 {
		px = SidebarWidth + 8
	}
	py := HeaderBarHeight + ColumnHeaderHeight + 16
	s.previewDialog.SetBounds(toolkit.Rect{X: px, Y: py, W: PreviewWidth, H: PreviewHeight})
}

// syncSidebar updates SidebarSelected to match Browser.CurrentPath. The first
// matching sidebar row wins (Home owns "/" before Computer / Trash do).
func (s *State) syncSidebar() {
	s.SidebarSelected = -1
	for i, e := range s.Sidebar {
		if e.Path == s.Browser.CurrentPath {
			s.SidebarSelected = i
			return
		}
	}
}

// --- control click handlers ----------------------------------------------

// onBack goes up one level (the back arrow). No-op at the root.
func (s *State) onBack() {
	if s.Browser.GoUp(s.VFS) {
		s.syncSidebar()
		s.refreshView()
		s.dirty = true
	}
}

// onHamburger is the menu button: v0 has no drop-down, so it logs to the
// browser console and reports no change (the hit-zone stays exercised for a
// future revision).
func (s *State) onHamburger() {
	println("files: hamburger menu clicked (no-op stub)")
}

// clickRow selects list row i; a directory descends on a single click.
func (s *State) clickRow(i int) {
	s.Browser.Cursor = i
	if s.Browser.Entries[i].IsDir {
		s.Browser.ActivateCurrent(s.VFS)
		s.syncSidebar()
		s.refreshView()
	}
	s.dirty = true
}

// clickSidebar navigates to sidebar row i's Path. A click that changes neither
// the path nor the selected index is a no-op.
func (s *State) clickSidebar(i int) {
	target := s.Sidebar[i].Path
	if target == s.Browser.CurrentPath && s.SidebarSelected == i {
		return
	}
	s.Browser.CurrentPath = target
	s.Browser.Cursor = 0
	s.Browser.Refresh(s.VFS)
	s.SidebarSelected = i
	s.refreshView()
	s.dirty = true
}

// --- custom leaf widgets -------------------------------------------------

// fileRow is one entry row: a file-type icon + name (left) + right-aligned
// size. The selected row (Browser.Cursor) fills the accent across the whole
// right pane with white ink.
type fileRow struct {
	toolkit.Base
	s *State
	i int
	e Entry
}

func (r *fileRow) Draw(p painter.Painter, _ *toolkit.Theme) {
	b := r.Bounds()
	selected := r.i == r.s.Browser.Cursor
	fg := ColorTextPrimary
	fgSecondary := ColorTextSecondary
	if selected {
		p.FillRect(b, ColorAccent)
		fg = ColorOnAccent
		fgSecondary = ColorOnAccent
	}
	iconX := NameColX
	iconY := b.Y + (RowHeight-IconSize)/2
	if r.e.IsDir {
		drawFolderIcon(p, iconX, iconY, selected)
	} else {
		drawFileIcon(p, iconX, iconY, selected)
	}
	nameX := iconX + IconSize + 10
	nameY := b.Y + (RowHeight-toolkit.GlyphHeight())/2
	toolkit.DrawText(p, nameX, nameY, r.e.Name, fg)
	size := "--"
	if !r.e.IsDir {
		size = formatSize(r.e.Size)
	}
	sizeX := SizeColRight - 16 - toolkit.TextWidth(size)
	toolkit.DrawText(p, sizeX, nameY, size, fgSecondary)
}

func (r *fileRow) OnEvent(ev toolkit.Event) {
	if ev.Kind == toolkit.EventClick {
		r.s.clickRow(r.i)
	}
}

// fileList is a custom container of fileRows. It rebuilds its rows from the
// current listing (setRows), lays them out at RowHeight pitch, routes clicks to
// the row they land on, and exposes rowAt for the scene's right/double-click
// dispatch. (The toolkit Table has no per-row icon column, so the list is
// composed of custom rows instead -- see the follow-up note in the PR.)
type fileList struct {
	toolkit.Base
	s    *State
	rows []*fileRow
}

// setRows rebuilds the row widgets from the browser's current entries + lays
// them out within the list's bounds.
func (l *fileList) setRows() {
	l.rows = l.rows[:0]
	for i := range l.s.Browser.Entries {
		l.rows = append(l.rows, &fileRow{s: l.s, i: i, e: l.s.Browser.Entries[i]})
	}
	l.layoutRows()
}

// layoutRows positions each row top-to-bottom at RowHeight pitch.
func (l *fileList) layoutRows() {
	b := l.Bounds()
	for i, r := range l.rows {
		r.SetBounds(toolkit.Rect{X: b.X, Y: b.Y + i*RowHeight, W: b.W, H: RowHeight})
	}
}

func (l *fileList) SetBounds(r toolkit.Rect) {
	l.Base.SetBounds(r)
	l.layoutRows()
}

func (l *fileList) Draw(p painter.Painter, th *toolkit.Theme) {
	b := l.Bounds()
	for _, r := range l.rows {
		if r.Bounds().Y >= b.Y+b.H {
			break // clip rows past the surface, matching the old renderer
		}
		r.Draw(p, th)
	}
}

func (l *fileList) OnEvent(ev toolkit.Event) {
	if ev.Kind != toolkit.EventClick {
		return
	}
	b := l.Bounds()
	sx, sy := ev.X+b.X, ev.Y+b.Y
	for _, r := range l.rows {
		rb := r.Bounds()
		if rb.Contains(sx, sy) {
			r.OnEvent(toolkit.Event{Kind: ev.Kind, X: sx - rb.X, Y: sy - rb.Y})
			return
		}
	}
}

// rowAt maps a surface point to a row index, or -1 if it lands outside the list
// or below the last row.
func (l *fileList) rowAt(sx, sy int) int {
	b := l.Bounds()
	if !b.Contains(sx, sy) {
		return -1
	}
	idx := (sy - b.Y) / RowHeight
	if idx < 0 || idx >= len(l.rows) {
		return -1
	}
	return idx
}

// sidebarRow is one navigation row: a kind glyph + a label. The selected row
// (SidebarSelected) fills the accent across the pane (minus the 1px divider)
// with white ink.
type sidebarRow struct {
	toolkit.Base
	s *State
	i int
	e SidebarEntry
}

func (r *sidebarRow) Draw(p painter.Painter, _ *toolkit.Theme) {
	b := r.Bounds()
	selected := r.i == r.s.SidebarSelected
	fg := ColorTextPrimary
	if selected {
		p.FillRect(toolkit.Rect{X: b.X, Y: b.Y, W: SidebarWidth - 1, H: b.H}, ColorAccent)
		fg = ColorOnAccent
	}
	iconY := b.Y + (SidebarRowHeight-12)/2
	drawSidebarIcon(p, b.X+12, iconY, r.e.Kind, selected)
	labelY := b.Y + (SidebarRowHeight-toolkit.GlyphHeight())/2
	toolkit.DrawText(p, b.X+32, labelY, r.e.Name, fg)
}

func (r *sidebarRow) OnEvent(ev toolkit.Event) {
	if ev.Kind == toolkit.EventClick {
		r.s.clickSidebar(r.i)
	}
}

// sectionLabel is a non-interactive sidebar section header ("BOOKMARKS").
type sectionLabel struct {
	toolkit.Base
	text string
}

func (l *sectionLabel) Draw(p painter.Painter, _ *toolkit.Theme) {
	b := l.Bounds()
	y := b.Y + (SidebarSectionHeaderHeight-toolkit.GlyphHeight())/2
	toolkit.DrawText(p, b.X+12, y, l.text, ColorTextSecondary)
}

// HitTest is false so a click on the section band passes through (the label is
// not interactive).
func (l *sectionLabel) HitTest(_, _ int) bool { return false }

// columnHeader is the non-interactive Name / Size band above the file list.
type columnHeader struct {
	toolkit.Base
}

func (c *columnHeader) Draw(p painter.Painter, _ *toolkit.Theme) {
	b := c.Bounds()
	y := b.Y + (ColumnHeaderHeight-toolkit.GlyphHeight())/2
	toolkit.DrawText(p, b.X+12, y, "Name", ColorTextSecondary)
	size := "Size"
	toolkit.DrawText(p, SizeColRight-toolkit.TextWidth(size), y, size, ColorTextSecondary)
	// Bottom divider.
	p.FillRect(toolkit.Rect{X: b.X, Y: b.Y + b.H - 1, W: b.W, H: 1}, ColorDivider)
}

func (c *columnHeader) HitTest(_, _ int) bool { return false }

// previewText draws the preview overlay's body lines (the Dialog's content).
type previewText struct {
	toolkit.Base
	lines []string
}

func (t *previewText) Draw(p painter.Painter, _ *toolkit.Theme) {
	b := t.Bounds()
	y := b.Y + PreviewPadding
	lh := toolkit.GlyphHeight() + 4
	for _, ln := range t.lines {
		if y+toolkit.GlyphHeight() > b.Y+b.H-PreviewPadding {
			break
		}
		toolkit.DrawText(p, b.X+PreviewPadding, y, ln, ColorTextPrimary)
		y += lh
	}
}

// --- file-type icon glyphs (painter primitives) --------------------------

// drawFolderIcon paints a Nautilus-style 24x18 folder at (x, y). Selected rows
// flip the fill to white so the icon reads on the accent strip.
func drawFolderIcon(p painter.Painter, x, y int, selected bool) {
	face, tab, stroke := ColorFolderFill, ColorFolderTab, ColorFolderStroke
	if selected {
		face, tab, stroke = ColorOnAccent, ColorOnAccent, colorSelectedStroke
	}
	fill(p, x, y, 8, 3, tab)          // tab
	fill(p, x, y+3, 24, 14, face)     // body
	fill(p, x, y+3, 24, 1, stroke)    // strokes
	fill(p, x, y+16, 24, 1, stroke)
	fill(p, x, y+3, 1, 14, stroke)
	fill(p, x+23, y+3, 1, 14, stroke)
	fill(p, x, y, 8, 1, stroke)
	fill(p, x, y, 1, 3, stroke)
	fill(p, x+7, y, 1, 3, stroke)
}

// drawFileIcon paints an 18x22 page with a folded top-right corner at (x, y).
func drawFileIcon(p painter.Painter, x, y int, selected bool) {
	paper, stroke := ColorFilePaper, ColorFileBorder
	if selected {
		paper, stroke = ColorOnAccent, colorSelectedStroke
	}
	fill(p, x, y, 18, 22, paper)
	fill(p, x, y, 18, 1, stroke)
	fill(p, x, y+21, 18, 1, stroke)
	fill(p, x, y, 1, 22, stroke)
	fill(p, x+17, y, 1, 22, stroke)
	for i := 0; i < 6; i++ { // folded corner
		fill(p, x+12+i, y, 6-i, 1, stroke)
	}
	for i := 0; i < 6; i++ { // fold diagonal
		fill(p, x+12+i, y+5-i, 1, 1, stroke)
	}
}

// drawSidebarIcon dispatches to the mini glyph for the entry kind.
func drawSidebarIcon(p painter.Painter, x, y int, kind string, selected bool) {
	switch kind {
	case "home":
		drawStarIcon(p, x, y, selected)
	case "computer":
		drawComputerIcon(p, x, y, selected)
	case "trash":
		drawTrashIcon(p, x, y, selected)
	default:
		drawMiniFolder(p, x, y, selected)
	}
}

// drawMiniFolder is a small (14x10) folder glyph for the Bookmarks section.
func drawMiniFolder(p painter.Painter, x, y int, selected bool) {
	face, tab, stroke := ColorFolderFill, ColorFolderTab, ColorFolderStroke
	if selected {
		face, tab, stroke = ColorOnAccent, ColorOnAccent, colorSelectedStroke
	}
	fill(p, x, y, 6, 2, tab)
	fill(p, x, y+2, 14, 8, face)
	fill(p, x, y+9, 14, 1, stroke)
	fill(p, x, y+2, 1, 8, stroke)
	fill(p, x+13, y+2, 1, 8, stroke)
}

// drawStarIcon draws a small star (a filled diamond + legs) for the Home entry.
func drawStarIcon(p painter.Painter, x, y int, selected bool) {
	ink := colorStar
	if selected {
		ink = ColorOnAccent
	}
	fill(p, x+5, y, 4, 2, ink)
	fill(p, x+3, y+2, 8, 2, ink)
	fill(p, x+1, y+4, 12, 2, ink)
	fill(p, x+3, y+6, 8, 2, ink)
	fill(p, x+2, y+8, 3, 2, ink)
	fill(p, x+9, y+8, 3, 2, ink)
}

// drawComputerIcon draws a small monitor for the Computer entry.
func drawComputerIcon(p painter.Painter, x, y int, selected bool) {
	face := ColorTextPrimary
	inside := ColorSidebarBG
	if selected {
		face, inside = ColorOnAccent, ColorOnAccent
	}
	fill(p, x, y, 14, 8, face)
	fill(p, x+1, y+1, 12, 6, inside)
	fill(p, x+5, y+8, 4, 2, face)
	fill(p, x+3, y+10, 8, 1, face)
}

// drawTrashIcon draws a small bin for the Trash entry.
func drawTrashIcon(p painter.Painter, x, y int, selected bool) {
	face := ColorTextPrimary
	if selected {
		face = ColorOnAccent
	}
	fill(p, x+1, y, 12, 1, face)
	fill(p, x+5, y-1, 4, 1, face)
	fill(p, x+2, y+1, 1, 9, face)
	fill(p, x+11, y+1, 1, 9, face)
	fill(p, x+2, y+9, 10, 1, face)
	fill(p, x+5, y+2, 1, 6, face)
	fill(p, x+8, y+2, 1, 6, face)
}

// fill is a shorthand for an opaque rectangle in surface coordinates.
func fill(p painter.Painter, x, y, w, h int, c toolkit.RGBA) {
	p.FillRect(toolkit.Rect{X: x, Y: y, W: w, H: h}, c)
}
