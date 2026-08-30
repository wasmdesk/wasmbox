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
//	   └─ (flex)  fileTable                                    — a real toolkit.Table
//	         header row: Name / Size            (Size right-aligned)
//	         body rows : one per entry, with a per-row RowIcon glyph
//
// The file list is a real toolkit.Table (Name + Size columns) with a per-row
// leading icon supplied through Table.RowIcon (v0.74's file-list-enabling
// addition): each row's glyph — the two-tone folder or the folded-page file
// icon — is painted in the reserved gutter of the Name column, so the browser
// no longer hand-composes custom fileRow/fileList leaves just to get an icon
// column (the workaround noted in files PR #61). The sectioned sidebar stays a
// custom VBox of sidebarRows because its section-header bands are not a table
// shape.
//
// A single root.SetBounds lays the whole tree out, root.Draw paints it, and
// root.OnEvent routes left-clicks into child-local space. The two things the
// toolkit's click-only Event model cannot express — a secondary (right) button
// and a double-click count — are dispatched at the scene level against the
// table's own widget bounds (fileTable.rowAt), so no hand-computed hit
// rectangles remain.
//
// The WhiteSur palette is applied to the Table through a dedicated theme
// (tableTheme) the fileTable substitutes at Draw time, so the file view keeps
// its white pane / #0860F2 accent / dimmed-secondary header look without
// disturbing the chrome theme the buttons, breadcrumbs, menu and dialog use.

package scene

import (
	"strings"
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
// Hyperlegible @16px, toolkit v0.77.0), so the header breadcrumbs, sidebar
// labels, the Name/Size table and the text preview render as the shaped vector
// face. The bundled face never fails to parse (the error is documented as
// never-returned); on the impossible error path the toolkit leaves the still-
// working bitmap default active, so a swallowed error degrades to legible bitmap
// text, never to none. The Table auto-centres its 20px line box in the fixed
// 22px rows (TableRowHeight) and the sidebar labels are GlyphHeight-centred, so
// the taller face re-centres itself and no widget rect moves.
func enableAAText() { aaOnce.Do(func() { _ = toolkit.UseOpenTypeText() }) }

// Visual constants -- exported so tests + the Playwright probes can pin layout
// invariants. The defaults reproduce GTK4 / libadwaita proportions on a
// 720x440 surface.
const (
	// HeaderBarHeight is the height of the top header bar (hamburger + nav
	// buttons + breadcrumb path-bar).
	HeaderBarHeight = 44
	// SidebarWidth is the width of the left navigation pane.
	SidebarWidth = 160
	// SidebarSectionHeaderHeight is the height of a section label band.
	SidebarSectionHeaderHeight = 22
	// SidebarRowHeight is the height of one navigation row.
	SidebarRowHeight = 28
	// SidebarTopPadding is the vertical gap between the header bar and the
	// first section label.
	SidebarTopPadding = 8

	// SizeColWidth is the fixed pixel width of the right-aligned Size column;
	// the Name column takes the remaining (flex) budget. The Size column's text
	// is right-aligned via toolkit.AlignRight so byte counts line up on the
	// right edge, exactly as the pre-Table renderer's Size column did.
	SizeColWidth = 120

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
	// ColorFolderFill is the ink the iconoir "folder" glyph is stroked in on an
	// unselected row (WhiteSur folder blue); a selected row inverts it to
	// ColorOnAccent so the line icon reads on the accent strip.
	ColorFolderFill = toolkit.RGB(74, 134, 246)
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
	sidebarList *sidebarList
	sbLabel     *toolkit.Label
	table       *fileTable

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
	enableAAText() // chrome + table + sidebar render with the AA/shaped face.
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

	// --- sidebar (sectioned toolkit.ListBox) -----------------------------
	// The navigation pane is a real toolkit.ListBox in sectioned mode: the
	// toolkit owns the section-header bands, the row backgrounds, the accent
	// selection highlight, virtual scrolling and click hit-testing. A per-row
	// ItemRenderer fills each row's content -- an iconoir kind glyph plus a
	// toolkit.Label for the label -- so no custom sidebarRow / sectionLabel leaf
	// hand-draws the pane any more. Selection is the ListBox's own Selected
	// Observable (kept in sync with SidebarSelected at Draw time); a click fires
	// OnActivate, which routes into clickSidebar exactly as before.
	s.sbLabel = toolkit.NewLabel("")
	s.sbLabel.VAlign = toolkit.VMiddle
	lb := toolkit.NewSectionedListBox(sidebarSections(s.Sidebar)...)
	lb.RowHeight = SidebarRowHeight
	lb.OnActivate = func(i int) { s.clickSidebar(i) }
	lb.ItemRenderer = s.drawSidebarItem
	s.sidebarList = &sidebarList{ListBox: lb, theme: newSidebarTheme()}

	s.sidebar = toolkit.NewVBox()
	s.sidebar.Spacing = 0
	s.sidebar.AddFixed(spacer(), SidebarTopPadding)
	s.sidebar.AddFlex(s.sidebarList, 1)

	// --- content (Name / Size Table with a per-row icon gutter) ----------
	s.table = newFileTable(s)

	// --- body + shell -----------------------------------------------------
	body := toolkit.NewHBox()
	body.Spacing = 0
	body.AddFixed(s.sidebar, SidebarWidth)
	body.AddFlex(s.table, 1)

	s.root = toolkit.NewDock(body)
	s.root.Dock(band, toolkit.DockTop, HeaderBarHeight)
	s.root.SetBounds(toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H})

	// --- overlays ---------------------------------------------------------
	s.ctxMenu = toolkit.NewContextMenu(toolkit.NewMenu(nil))
	s.ctxMenu.SetBounds(toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H})
	s.previewBody = newPreviewText()
	s.previewDialog = toolkit.NewDialog("", s.previewBody)
}

// spacer is an invisible, inert box cell used to carry a fixed gap.
func spacer() toolkit.Widget { return toolkit.NewContainer(nil) }

// refreshView re-syncs the derived widget state after a navigation: it rebuilds
// the list rows from the current listing + points the breadcrumbs at the new
// path. The bounds are stable across navigation (same surface) so no full
// re-layout is required beyond the list's own row placement.
func (s *State) refreshView() {
	s.table.setRows()
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

	// Keep the Table's accent highlight on the row the navigation cursor points
	// at. HandleKey's ArrowUp/ArrowDown move Browser.Cursor without re-listing
	// (so refreshView isn't called); syncing here means every frame paints the
	// current selection whichever path mutated the cursor.
	s.table.Selected().Set(s.Browser.Cursor)
	// Keep the sidebar ListBox's accent highlight on the navigation location.
	// SidebarSelected is the flat item index (headers excluded); -1 leaves no
	// row highlighted (the browser is somewhere no bookmark points at).
	s.sidebarList.Selected().Set(s.SidebarSelected)

	// Grounds: window (right pane) white, sidebar grey, header grey, hairlines.
	fillBox(p, toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H}, ColorWindowBG)
	fillBox(p, toolkit.Rect{X: 0, Y: HeaderBarHeight, W: SidebarWidth, H: s.H - HeaderBarHeight}, ColorSidebarBG)
	fillBox(p, toolkit.Rect{X: SidebarWidth - 1, Y: HeaderBarHeight, W: 1, H: s.H - HeaderBarHeight}, ColorDivider)
	fillBox(p, toolkit.Rect{X: 0, Y: 0, W: s.W, H: HeaderBarHeight}, ColorHeaderBarBG)
	fillBox(p, toolkit.Rect{X: 0, Y: HeaderBarHeight - 1, W: s.W, H: 1}, ColorDivider)

	// Widget tree.
	s.root.Draw(p, th)

	// Overlays on top of the navigation chrome.
	if s.ctxMenu.Open().Get() {
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
	if s.ctxMenu.Open().Get() {
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
	row := s.table.rowAt(x, y)
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
	if !s.table.bodyContains(x, y) {
		return false
	}
	if row := s.table.rowAt(x, y); row >= 0 {
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
	s.previewBody.SetText(strings.Join(lines, "\n"))
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
	py := HeaderBarHeight + toolkit.TableHeaderHeight + 16
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

// fileTable is the file list: a real toolkit.Table (Name + Size columns) with
// a per-row leading icon supplied through Table.RowIcon. It embeds the Table so
// the toolkit owns the header, row backgrounds, accent selection, text layout
// and the icon gutter; fileTable adds only the wasmbox glue the toolkit's
// click-only Event model can't express on its own:
//
//   - Draw substitutes the Files-specific tableTheme so the view keeps its
//     white pane / #0860F2 accent / dimmed-secondary header palette without
//     touching the chrome theme the buttons + menu + dialog draw with;
//   - OnEvent turns a left click into a select-or-descend (clickRow), rather
//     than the Table's own passive/multi-select behaviour;
//   - rowAt / bodyContains map a surface point to a body row for the scene's
//     right-click + double-click dispatch (which the Event model omits).
type fileTable struct {
	*toolkit.Table
	s     *State
	theme *toolkit.Theme
}

// newFileTable builds the Name/Size Table, wires the per-row icon, and pins the
// Files WhiteSur palette into a dedicated theme the Table draws with.
func newFileTable(s *State) *fileTable {
	tbl := toolkit.NewTable([]toolkit.TableColumn{
		{Title: "Name", Align: toolkit.AlignLeft},
		{Title: "Size", Width: SizeColWidth, Align: toolkit.AlignRight},
	}, nil)
	t := &fileTable{Table: tbl, s: s, theme: newTableTheme()}
	// RowIcon paints the iconoir kind glyph (a "folder" line icon for a
	// directory, a "page" line icon for a file) in the Name column's reserved
	// gutter. The closure decides the ink from Browser.Cursor (rather than the
	// ink Draw hands it) so the folder keeps its WhiteSur blue on unselected rows
	// and both glyphs invert to white on the accent row.
	tbl.RowIcon = func(row int) (toolkit.TableIconFunc, bool) {
		if row < 0 || row >= len(s.Browser.Entries) {
			return nil, false
		}
		e := s.Browser.Entries[row]
		selected := row == s.Browser.Cursor
		return func(p painter.Painter, r toolkit.Rect, _ toolkit.RGBA) {
			stem := "page"
			ink := ColorTextSecondary
			if e.IsDir {
				stem = "folder"
				ink = ColorFolderFill
			}
			if selected {
				ink = ColorOnAccent
			}
			toolkit.DrawIconoir(p, toolkit.Rect(r), stem, ink)
		}, true
	}
	return t
}

// newTableTheme is the WhiteSur palette mapped onto the roles Table.Draw reads:
// a white pane (Surface) with no zebra stripe (Background == Surface), a white
// header band (SurfaceAlt) carrying dimmed-secondary titles (OnBackground),
// primary-ink body cells (OnSurface), the #0860F2 accent selection with white
// ink on it (Extra["OnAccent"]), and #d3d3d3 hairlines (Border) for the header
// underline + the Name/Size separator.
func newTableTheme() *toolkit.Theme {
	return &toolkit.Theme{
		Background:   ColorWindowBG,
		Surface:      ColorWindowBG,
		SurfaceAlt:   ColorWindowBG,
		OnBackground: ColorTextSecondary,
		OnSurface:    ColorTextPrimary,
		Accent:       ColorAccent,
		Border:       ColorDivider,
		Extra:        map[string]toolkit.RGBA{"OnAccent": ColorOnAccent},
	}
}

// setRows rebuilds the Table's rows from the browser's current entries: one
// [Name, Size] row per entry, with folders rendering "--" for the size exactly
// as the pre-Table renderer did.
func (t *fileTable) setRows() {
	rows := make([][]string, 0, len(t.s.Browser.Entries))
	for _, e := range t.s.Browser.Entries {
		size := "--"
		if !e.IsDir {
			size = formatSize(e.Size)
		}
		rows = append(rows, []string{e.Name, size})
	}
	t.Rows = rows
}

// Draw paints the Table with the Files-specific palette rather than the chrome
// theme the container hands down.
func (t *fileTable) Draw(p painter.Painter, _ *toolkit.Theme) {
	t.Table.Draw(p, t.theme)
}

// OnEvent turns a left click into a select-or-descend on the body row it lands
// on. Coordinates arrive table-local (the container routes into child space);
// a click on the header row or below the last entry resolves to -1 and is a
// no-op, matching the pre-Table column-header / empty-area behaviour.
func (t *fileTable) OnEvent(ev toolkit.Event) {
	if ev.Kind != toolkit.EventClick {
		return
	}
	if row := t.rowAtLocal(ev.X, ev.Y); row >= 0 {
		t.s.clickRow(row)
	}
}

// rowAtLocal maps a table-local point to a body row index, or -1 for the header
// band, a horizontal miss, or a point below the last row.
func (t *fileTable) rowAtLocal(lx, ly int) int {
	b := t.Bounds()
	if lx < 0 || lx >= b.W || ly < toolkit.TableHeaderHeight {
		return -1
	}
	idx := (ly - toolkit.TableHeaderHeight) / toolkit.TableRowHeight
	if idx < 0 || idx >= len(t.Rows) {
		return -1
	}
	return idx
}

// rowAt maps a surface point to a body row index (or -1), the surface-space
// entry point the scene's right/double-click dispatch uses.
func (t *fileTable) rowAt(sx, sy int) int {
	b := t.Bounds()
	if !b.Contains(sx, sy) {
		return -1
	}
	return t.rowAtLocal(sx-b.X, sy-b.Y)
}

// bodyContains reports whether a surface point lands in the Table's body region
// (inside the widget but below the header row) -- the area a right-click may pop
// an entry or "create new" context menu over. A point in the header band or
// outside the widget is excluded, so a header right-click stays a no-op.
func (t *fileTable) bodyContains(sx, sy int) bool {
	b := t.Bounds()
	return b.Contains(sx, sy) && sy >= b.Y+toolkit.TableHeaderHeight
}

// sidebarList wraps a sectioned toolkit.ListBox so the navigation pane draws
// with the Files WhiteSur sidebar palette (a grey pane, the #0860F2 accent
// selection, white-on-accent ink) rather than the chrome theme the container
// hands down -- the same theme-substitution seam fileTable uses for the Table.
// Every other behaviour (selection, sectioned layout, hit-testing, scrolling)
// is the embedded ListBox's own.
type sidebarList struct {
	*toolkit.ListBox
	theme *toolkit.Theme
}

// Draw paints the ListBox with the Files sidebar palette instead of the chrome
// theme the container passes down.
func (l *sidebarList) Draw(p painter.Painter, _ *toolkit.Theme) { l.ListBox.Draw(p, l.theme) }

// sidebarSections groups the flat Sidebar slice into the ListBox's sectioned
// model: one toolkit.ListSection per contiguous run of a Section, its Items the
// entry Names in order. Because the entries are already grouped by Section, the
// flat selectable-item index the ListBox addresses (headers excluded) lines up
// one-to-one with the Sidebar slice index, so OnActivate(i) maps straight back
// to Sidebar[i] / clickSidebar(i).
func sidebarSections(entries []SidebarEntry) []toolkit.ListSection {
	var out []toolkit.ListSection
	for _, e := range entries {
		if len(out) == 0 || out[len(out)-1].Title != e.Section {
			out = append(out, toolkit.ListSection{Title: e.Section})
		}
		last := &out[len(out)-1]
		last.Items = append(last.Items, e.Name)
	}
	return out
}

// drawSidebarItem is the ListBox ItemRenderer: it paints one navigation row's
// content -- the iconoir kind glyph followed by the label -- into the row rect
// rc. The ListBox has already filled the row background (the accent strip on the
// selected row) and resolved ink to the label/glyph ink for the row's state
// (ColorTextPrimary normally, ColorOnAccent white on the accent strip), so a
// single-ink iconoir line icon + a toolkit.Label in that ink is all that's left.
// index is the flat selectable-item index, which equals the Sidebar slice index.
func (s *State) drawSidebarItem(p painter.Painter, theme *toolkit.Theme, rc toolkit.Rect, index int, item string, _ bool, ink toolkit.RGBA) {
	const icon = 16
	toolkit.DrawIconoir(p, toolkit.Rect{X: rc.X + 12, Y: rc.Y + (rc.H-icon)/2, W: icon, H: icon}, sidebarStem(s.Sidebar[index].Kind), ink)
	s.sbLabel.Text().Set(item)
	s.sbLabel.Ink = ink
	s.sbLabel.SetBounds(toolkit.Rect{X: rc.X + 32, Y: rc.Y, W: rc.W - 32, H: rc.H})
	s.sbLabel.Draw(p, theme)
}

// sidebarStem maps a SidebarEntry.Kind to its iconoir stem: the Home star, the
// Computer monitor, the Trash bin, or a folder for every bookmark.
func sidebarStem(kind string) string {
	switch kind {
	case "home":
		return "star"
	case "computer":
		return "computer"
	case "trash":
		return "trash"
	default:
		return "folder"
	}
}

// newSidebarTheme maps the WhiteSur sidebar palette onto the roles the sectioned
// ListBox reads: a grey pane (Surface) with grey section-header bands (SurfaceAlt
// matches the pane so a header reads as a caption, not a raised strip),
// primary-ink item labels (OnSurface), the #0860F2 accent selection carrying
// white ink (Accent + Extra["OnAccent"]).
func newSidebarTheme() *toolkit.Theme {
	return &toolkit.Theme{
		Background:   ColorSidebarBG,
		Surface:      ColorSidebarBG,
		SurfaceAlt:   ColorSidebarBG,
		OnBackground: ColorTextSecondary,
		OnSurface:    ColorTextPrimary,
		Accent:       ColorAccent,
		Border:       ColorDivider,
		Extra:        map[string]toolkit.RGBA{"OnAccent": ColorOnAccent},
	}
}

// previewText wraps a toolkit.TextView so the text-file preview overlay's body
// is a real multi-line text widget (with the toolkit's own windowed line
// clipping) rather than a hand-drawn line stack. Draw substitutes a white-pane
// preview theme so the body reads as a document regardless of the chrome theme
// the Dialog passes down.
type previewText struct {
	*toolkit.TextView
	theme *toolkit.Theme
}

// newPreviewText builds the preview body TextView. It is never focused (the
// overlay is read-only and the next click dismisses it at the scene level), so
// no caret is painted.
func newPreviewText() *previewText {
	return &previewText{TextView: toolkit.NewTextView(""), theme: newPreviewTheme()}
}

// Draw paints the TextView with the preview palette instead of the Dialog's
// chrome theme.
func (t *previewText) Draw(p painter.Painter, _ *toolkit.Theme) { t.TextView.Draw(p, t.theme) }

// newPreviewTheme is a white document pane carrying primary-ink body text and a
// #d3d3d3 hairline border, matching the file view.
func newPreviewTheme() *toolkit.Theme {
	return &toolkit.Theme{
		Background:   ColorWindowBG,
		Surface:      ColorWindowBG,
		SurfaceAlt:   ColorWindowBG,
		OnBackground: ColorTextPrimary,
		OnSurface:    ColorTextPrimary,
		Accent:       ColorAccent,
		Border:       ColorDivider,
	}
}
