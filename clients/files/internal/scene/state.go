// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's state.go is the file browser's UI state: a current path, a
// cached listing of its entries, the cursor index inside that listing, and a
// fixed Favorites sidebar that mirrors macOS Finder's left pane.
// Pure Go (no syscall/js, no cgo) so it builds + tests natively on every
// architecture the repo targets.

package scene

// SidebarEntry is one row in the Favorites sidebar: a display name and the
// VFS path the row jumps to when clicked. We keep the path and the name
// separate so the sidebar can show friendlier labels than the bare path (the
// root "/" entry is presented as "Macintosh HD", for example).
type SidebarEntry struct {
	Name string
	Path string
}

// DefaultSidebar is the canonical Favorites list shown in the left pane.
// "Desktop" and "Recents" are placeholders -- they point at the root because
// the demo VFS does not model a real desktop/recents view; clicking them
// still feels reactive (the selected-favorite highlight moves), and they
// document the intended Finder shape.
func DefaultSidebar() []SidebarEntry {
	return []SidebarEntry{
		{Name: "Documents", Path: "/Documents"},
		{Name: "Pictures", Path: "/Pictures"},
		{Name: "Downloads", Path: "/Downloads"},
		{Name: "Desktop", Path: "/"},
		{Name: "Recents", Path: "/"},
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
	// SidebarSelected indexes into Sidebar; -1 means "no Favorite is the
	// active root" (the user navigated via Enter/Backspace into a path that
	// no Favorite owns). The renderer highlights the selected Favorite with
	// the accent colour.
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
// demo VFS, rooted at "/" with the first entry selected and no Favorite
// active (since "/" is not in DefaultSidebar by path).
func New(width, height int) *State {
	vfs := NewDemoVFS()
	bs := &BrowserState{CurrentPath: "/"}
	bs.Refresh(vfs)
	s := &State{
		W: width, H: height, VFS: vfs, Browser: bs,
		Sidebar:         DefaultSidebar(),
		SidebarSelected: -1,
	}
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
// can decide whether to re-render -- though in practice the row inversion
// also moves, so a real client always re-renders.
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
// it is a no-op (Parent("/") == "/"). Returns true when CurrentPath changed,
// for the same reason ActivateCurrent does.
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
// to re-render. Anything else (modifiers, printable keys) is ignored. When
// the cursor moves into a Favorite path we also update SidebarSelected so the
// left-pane highlight stays in sync.
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
// the click coordinates relative to the surface origin (the SDK gives us
// canvas-window-local x/y via translate_input). Returns true when visible
// state changed so the caller re-renders.
//
// Hit-zones, top to bottom:
//   - Toolbar back-arrow button -> GoUp
//   - Sidebar row              -> jump to that Favorite's Path
//   - List row                 -> select (single click) / activate (treated
//     as activate on click so the demo is reactive -- a real Finder uses
//     double-click, but on a 720x440 placeholder one click is friendlier).
func (s *State) HandleMouse(x, y int) bool {
	// Toolbar back-arrow button.
	if y >= 0 && y < ToolbarHeight {
		if x >= BackBtnX && x < BackBtnX+BackBtnW {
			return s.handleBack()
		}
		return false
	}
	// Sidebar (everything below the toolbar, x in [0, SidebarWidth)).
	if x >= 0 && x < SidebarWidth {
		return s.handleSidebarClick(y)
	}
	// List rows. Skip the column-header band.
	listY0 := ToolbarHeight + ColumnHeaderHeight
	if y < listY0 {
		return false
	}
	idx := (y - listY0) / RowHeight
	if idx < 0 || idx >= len(s.Browser.Entries) {
		return false
	}
	s.Browser.Cursor = idx
	// One-click activate: descend into folders so the demo is reactive.
	e := s.Browser.Entries[idx]
	if e.IsDir {
		s.Browser.ActivateCurrent(s.VFS)
		s.syncSidebar()
	}
	return true
}

// handleBack is the click handler for the toolbar back-arrow button. Wraps
// GoUp + syncSidebar so callers do not have to think about Favorites state.
func (s *State) handleBack() bool {
	if s.Browser.GoUp(s.VFS) {
		s.syncSidebar()
		return true
	}
	return false
}

// handleSidebarClick maps a sidebar y-coordinate to a Favorite row and
// navigates to that row's Path. Returns true when CurrentPath changed.
func (s *State) handleSidebarClick(y int) bool {
	rowH := SidebarRowHeight
	idx := (y - ToolbarHeight - SidebarHeaderHeight) / rowH
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

// syncSidebar updates SidebarSelected to match Browser.CurrentPath. Called
// after every keyboard-driven navigation so the left-pane highlight tracks
// the current location.
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
