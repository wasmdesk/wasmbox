// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's state.go is the file browser's UI state: a current path, a
// cached listing of its entries, the cursor index inside that listing, and a
// sectioned sidebar that mirrors GNOME Nautilus's left pane (Bookmarks +
// Other Locations). Pure Go (no syscall/js, no cgo) so it builds + tests
// natively on every architecture the repo targets.

package scene

// SidebarEntry is one row in the navigation sidebar. Section groups entries
// in the rendered list ("Bookmarks", "Other Locations"); Kind picks the
// glyph the renderer paints next to the label ("home" star, "folder",
// "computer" monitor, "trash" bin); Path is the VFS path the row navigates
// to when clicked.
type SidebarEntry struct {
	Section string
	Kind    string
	Name    string
	Path    string
}

// DefaultSidebar is the canonical Nautilus-style navigation list shown in
// the left pane. Two sections:
//
//   - Bookmarks: Home, Documents, Pictures, Downloads
//   - Other Locations: Computer, Trash
//
// Home points at the root "/" so the breadcrumb's "Home" segment lights up
// the right sidebar row. Computer and Trash are placeholders -- they point
// at the root because the demo VFS has no real disk inventory or trashcan;
// clicking them still feels reactive (the selected-row highlight moves).
func DefaultSidebar() []SidebarEntry {
	return []SidebarEntry{
		{Section: "BOOKMARKS", Kind: "home", Name: "Home", Path: "/"},
		{Section: "BOOKMARKS", Kind: "folder", Name: "Documents", Path: "/Documents"},
		{Section: "BOOKMARKS", Kind: "folder", Name: "Pictures", Path: "/Pictures"},
		{Section: "BOOKMARKS", Kind: "folder", Name: "Downloads", Path: "/Downloads"},
		{Section: "OTHER LOCATIONS", Kind: "computer", Name: "Computer", Path: "/"},
		{Section: "OTHER LOCATIONS", Kind: "trash", Name: "Trash", Path: "/"},
	}
}

// State is the top-of-package handle the wasm entry point holds. Surface
// dimensions live alongside the browser state because the renderer uses both
// (rows-per-screen, sidebar width, etc.).
type State struct {
	W, H    int
	VFS     VFS
	Browser *BrowserState
	Sidebar []SidebarEntry
	// SidebarSelected indexes into Sidebar; -1 means "no row is the active
	// location" (the user navigated via Enter/Backspace into a path that no
	// sidebar row owns). The renderer highlights the selected row with the
	// accent colour.
	SidebarSelected int
}

// BrowserState owns the navigation cursor. CurrentPath is always normalised
// (see vfs.Clean); Entries is the cached List(CurrentPath) result. Cursor
// indexes into Entries; we keep it in [0, len(Entries)) by clamping on every
// mutation.
type BrowserState struct {
	CurrentPath string
	Entries     []Entry
	Cursor      int
}

// New constructs a State for a width x height pixel surface backed by the
// demo VFS, rooted at "/" with the first entry selected. The Home row in
// DefaultSidebar owns "/" so SidebarSelected starts pointing at it.
func New(width, height int) *State {
	vfs := NewDemoVFS()
	bs := &BrowserState{CurrentPath: "/"}
	bs.Refresh(vfs)
	s := &State{
		W: width, H: height, VFS: vfs, Browser: bs,
		Sidebar:         DefaultSidebar(),
		SidebarSelected: -1,
	}
	s.syncSidebar()
	return s
}

// Refresh re-lists CurrentPath, swaps the cached Entries, and clamps Cursor
// into the new range. Called whenever CurrentPath changes (e.g. ActivateCurrent
// on a folder, GoUp) and at construction time so the renderer never sees a
// stale or nil Entries slice.
func (b *BrowserState) Refresh(vfs VFS) {
	entries, err := vfs.List(b.CurrentPath)
	if err != nil {
		// A missing or non-dir path falls back to the root rather than
		// leaving the browser stuck on an unreadable location.
		b.CurrentPath = "/"
		entries, _ = vfs.List("/")
	}
	b.Entries = entries
	b.clampCursor()
}

// MoveCursor shifts the cursor by dy and clamps it into [0, len(Entries)).
// Callers pass +1 for "down arrow" and -1 for "up arrow"; larger steps work
// the same way (no PageDown today but the math holds).
func (b *BrowserState) MoveCursor(dy int) {
	b.Cursor += dy
	b.clampCursor()
}

// ActivateCurrent enters the currently-selected entry: a directory becomes
// the new CurrentPath (and Refresh re-lists it); a file is a no-op (v0 has
// no preview/open). Returns true when CurrentPath changed, so the wasm side
// can decide whether to re-render.
func (b *BrowserState) ActivateCurrent(vfs VFS) bool {
	if b.Cursor < 0 || b.Cursor >= len(b.Entries) {
		return false
	}
	e := b.Entries[b.Cursor]
	if !e.IsDir {
		return false
	}
	b.CurrentPath = Join(b.CurrentPath, e.Name)
	b.Cursor = 0
	b.Refresh(vfs)
	return true
}

// GoUp navigates to the parent of CurrentPath. If we are already at the root
// it is a no-op (Parent("/") == "/"). Returns true when CurrentPath changed.
func (b *BrowserState) GoUp(vfs VFS) bool {
	parent := Parent(b.CurrentPath)
	if parent == b.CurrentPath {
		return false
	}
	b.CurrentPath = parent
	b.Cursor = 0
	b.Refresh(vfs)
	return true
}

// HandleKey routes one DOM-style keydown into the browser. Recognised keys:
//
//	"ArrowDown"  -> MoveCursor(+1)
//	"ArrowUp"    -> MoveCursor(-1)
//	"Enter"      -> ActivateCurrent
//	"Backspace"  -> GoUp
//	"Escape"     -> GoUp
//
// Returns true when the visible state changed, so the caller decides whether
// to re-render. Anything else (modifiers, printable keys) is ignored.
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
			return true
		}
		return false
	case "Backspace", "Escape":
		if s.Browser.GoUp(s.VFS) {
			s.syncSidebar()
			return true
		}
		return false
	default:
		return false
	}
}

// HandleMouse routes a surface-local mousedown into the browser. (x, y) are
// the click coordinates relative to the surface origin. Returns true when
// visible state changed so the caller re-renders.
//
// Hit-zones, top to bottom:
//   - Header-bar hamburger button -> stub (logged, no-op)
//   - Header-bar back-arrow       -> GoUp
//   - Header-bar forward arrow    -> stub (no forward history in v0)
//   - Sidebar row                 -> jump to that row's Path
//   - List row                    -> select / activate (one-click descend)
func (s *State) HandleMouse(x, y int) bool {
	// Header bar.
	if y >= 0 && y < HeaderBarHeight {
		if inRect(x, y, HamburgerBtnX, HamburgerBtnY, HamburgerBtnW, HamburgerBtnH) {
			return s.handleHamburger()
		}
		if inRect(x, y, BackBtnX, BackBtnY, BackBtnW, BackBtnH) {
			return s.handleBack()
		}
		if inRect(x, y, ForwardBtnX, ForwardBtnY, ForwardBtnW, ForwardBtnH) {
			return s.handleForward()
		}
		return false
	}
	// Sidebar (everything below the header bar, x in [0, SidebarWidth)).
	if x >= 0 && x < SidebarWidth {
		return s.handleSidebarClick(y)
	}
	// List rows. Skip the column-header band.
	listY0 := HeaderBarHeight + ColumnHeaderHeight
	if y < listY0 {
		return false
	}
	idx := (y - listY0) / RowHeight
	if idx < 0 || idx >= len(s.Browser.Entries) {
		return false
	}
	s.Browser.Cursor = idx
	// One-click activate on folders so the demo is reactive.
	e := s.Browser.Entries[idx]
	if e.IsDir {
		s.Browser.ActivateCurrent(s.VFS)
		s.syncSidebar()
	}
	return true
}

// inRect reports whether (x, y) falls inside the rectangle at (rx, ry) with
// size (rw, rh). Pulled out so the header-bar hit-tests stay readable.
func inRect(x, y, rx, ry, rw, rh int) bool {
	return x >= rx && x < rx+rw && y >= ry && y < ry+rh
}

// handleBack is the click handler for the back-arrow button. Wraps GoUp +
// syncSidebar so callers do not have to think about sidebar state.
func (s *State) handleBack() bool {
	if s.Browser.GoUp(s.VFS) {
		s.syncSidebar()
		return true
	}
	return false
}

// handleForward is the click handler for the forward-arrow button. v0 has
// no forward history (a single-stack navigation model), so this is a no-op
// stub that returns false. We still pay a hit-test so a future revision can
// wire up real forward navigation without changing the dispatch shape.
func (s *State) handleForward() bool {
	return false
}

// handleHamburger is the click handler for the menu button. v0 has no menu
// to drop down, so this just records a log line via println (visible in the
// browser console) and reports false. Keeps the hit-zone exercised so
// future revisions can attach a real menu without re-plumbing dispatch.
func (s *State) handleHamburger() bool {
	println("files: hamburger menu clicked (no-op stub)")
	return false
}

// handleSidebarClick maps a sidebar y-coordinate to a sidebar entry and
// navigates to that entry's Path. The mapping walks the sidebar in render
// order, accounting for the variable-height section headers, so a click
// always lands on the entry the user visually targeted.
func (s *State) handleSidebarClick(y int) bool {
	idx := s.sidebarHitIndex(y)
	if idx < 0 || idx >= len(s.Sidebar) {
		return false
	}
	target := s.Sidebar[idx].Path
	if target == s.Browser.CurrentPath && s.SidebarSelected == idx {
		return false
	}
	s.Browser.CurrentPath = target
	s.Browser.Cursor = 0
	s.Browser.Refresh(s.VFS)
	s.SidebarSelected = idx
	return true
}

// sidebarHitIndex maps a y-coordinate inside the sidebar to the index of
// the entry whose row band contains y, or -1 if y lands on a section
// header band or outside any entry. Mirrors paintSidebar's layout walk.
func (s *State) sidebarHitIndex(y int) int {
	cur := HeaderBarHeight + SidebarTopPadding
	prevSection := ""
	for i, e := range s.Sidebar {
		if e.Section != prevSection {
			if y >= cur && y < cur+SidebarSectionHeaderHeight {
				return -1 // section label band
			}
			cur += SidebarSectionHeaderHeight
			prevSection = e.Section
		}
		if y >= cur && y < cur+SidebarRowHeight {
			return i
		}
		cur += SidebarRowHeight
	}
	return -1
}

// syncSidebar updates SidebarSelected to match Browser.CurrentPath. Called
// after every keyboard-driven navigation + at construction time so the
// left-pane highlight tracks the current location. The first matching
// sidebar row wins (Home owns "/" before Computer / Trash do).
func (s *State) syncSidebar() {
	s.SidebarSelected = -1
	for i, e := range s.Sidebar {
		if e.Path == s.Browser.CurrentPath {
			s.SidebarSelected = i
			return
		}
	}
}

// clampCursor pins Cursor into [0, len(Entries)). For an empty directory the
// cursor becomes 0; the renderer paints no selection bar in that case (no
// row exists to highlight).
func (b *BrowserState) clampCursor() {
	if len(b.Entries) == 0 {
		b.Cursor = 0
		return
	}
	if b.Cursor < 0 {
		b.Cursor = 0
	}
	if b.Cursor >= len(b.Entries) {
		b.Cursor = len(b.Entries) - 1
	}
}
