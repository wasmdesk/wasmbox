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
	// Menu is the currently-open context menu (right-click) or nil when no
	// menu is up. Mutating the tree via Menu actions routes through
	// applyMenuAction.
	Menu *ContextMenu
	// Preview is a transient overlay showing the contents of a recently
	// double-clicked text file. Cleared by the next mouse event.
	Preview *PreviewOverlay
}

// ContextMenu is the popup spawned by right-click. Pos is the surface-local
// top-left corner; Target is the path the menu acts on (empty when the menu
// was opened on empty list space -- in that case actions create siblings of
// the current directory). Items lists the labels in display order.
type ContextMenu struct {
	X, Y   int
	Target string
	Items  []ContextMenuItem
}

// ContextMenuItem is one entry in the context menu. ID is a stable string the
// click dispatcher switches on ("open" / "delete" / "newfolder" / "newfile" /
// "rename"). Label is the user-facing string.
type ContextMenuItem struct {
	ID    string
	Label string
}

// PreviewOverlay is the simple "double-click .txt" overlay: a centred panel
// showing up to PreviewMaxLines of file content. Cleared on the next click.
type PreviewOverlay struct {
	Path  string
	Lines []string
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
// demo (in-memory) VFS, rooted at "/" with the first entry selected. The
// Home row in DefaultSidebar owns "/" so SidebarSelected starts pointing
// at it. Used by tests + by the wasm boot path when IndexedDB is
// unavailable.
func New(width, height int) *State {
	return NewWithVFS(width, height, NewDemoVFS())
}

// NewWithVFS is the explicit constructor the wasm boot path uses: it builds
// a State around the caller-supplied VFS so the browser-side code can hand
// in an IDB-backed instance for persistence. Tests reach for New() (which
// routes through here with the in-memory demo VFS).
func NewWithVFS(width, height int, vfs VFS) *State {
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

// HandleMouse is the legacy single-button mouse handler. It treats every
// click as a primary (left) mousedown so existing tests + callers that do
// not care about right-click + double-click keep working. New code should
// reach for HandleMouseButton.
func (s *State) HandleMouse(x, y int) bool {
	return s.HandleMouseButton(x, y, 0, 1)
}

// HandleMouseButton routes a surface-local mousedown into the browser.
// (x, y) are surface-local; button is the DOM button index (0=primary/left,
// 2=secondary/right); clickCount is 1 for single, 2 for double. Returns
// true when the visible state changed so the caller re-renders.
//
// Hit-zones, top to bottom:
//   - Active overlay (preview/menu) -> dismiss + consume
//   - Header-bar buttons            -> hamburger / back / forward
//   - Sidebar row                   -> jump to that row's Path
//   - List row                      -> select / activate (one-click descend)
//     . right-click on row          -> open context menu on the row
//     . double-click on .txt file   -> show preview overlay
//   - Empty list area               -> right-click opens "new folder/file" menu
func (s *State) HandleMouseButton(x, y, button, clickCount int) bool {
	// Any open preview is consumed by the very next click.
	if s.Preview != nil {
		s.Preview = nil
		// fall through so the click still drives whatever it lands on
	}
	// An open menu intercepts the next click: either activates an item,
	// or dismisses if the click landed outside the menu region.
	if s.Menu != nil {
		if s.handleMenuClick(x, y) {
			return true
		}
		s.Menu = nil
		return true
	}
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
	// Right-click on the empty area below the last row: spawn the
	// "create new..." menu rooted at the current directory.
	if idx < 0 || idx >= len(s.Browser.Entries) {
		if button == 2 {
			s.openEmptyAreaMenu(x, y)
			return true
		}
		return false
	}
	// Right-click on a row: open the row's context menu.
	if button == 2 {
		s.Browser.Cursor = idx
		s.openEntryMenu(x, y, s.Browser.Entries[idx])
		return true
	}
	// Double-click on a .txt file: show the preview overlay.
	e := s.Browser.Entries[idx]
	if clickCount >= 2 && !e.IsDir && hasTextExt(e.Name) {
		s.openPreview(Join(s.Browser.CurrentPath, e.Name))
		s.Browser.Cursor = idx
		return true
	}
	// Plain left-click on a row: select; folders descend on a single click.
	s.Browser.Cursor = idx
	if e.IsDir {
		s.Browser.ActivateCurrent(s.VFS)
		s.syncSidebar()
	}
	return true
}

// openEntryMenu spawns a context menu on the supplied row entry. The Open
// item activates a directory or previews a text file; Rename is a stub
// pending v1; Delete removes the path.
func (s *State) openEntryMenu(x, y int, e Entry) {
	target := Join(s.Browser.CurrentPath, e.Name)
	items := []ContextMenuItem{
		{ID: "open", Label: "Open"},
		{ID: "rename", Label: "Rename"},
		{ID: "delete", Label: "Delete"},
	}
	s.Menu = &ContextMenu{X: x, Y: y, Target: target, Items: items}
}

// openEmptyAreaMenu spawns the "create new..." menu when the user
// right-clicks on the empty list area below the last row.
func (s *State) openEmptyAreaMenu(x, y int) {
	items := []ContextMenuItem{
		{ID: "newfolder", Label: "New Folder"},
		{ID: "newfile", Label: "New File"},
	}
	s.Menu = &ContextMenu{X: x, Y: y, Target: "", Items: items}
}

// handleMenuClick dispatches a click while a menu is open. Returns true when
// the click landed inside the menu (and was consumed); the caller dismisses
// the menu on a false return.
func (s *State) handleMenuClick(x, y int) bool {
	idx := s.menuHitIndex(x, y)
	if idx < 0 {
		return false
	}
	item := s.Menu.Items[idx]
	target := s.Menu.Target
	s.Menu = nil
	s.applyMenuAction(item.ID, target)
	return true
}

// menuHitIndex maps (x, y) to the menu item index it lands on, or -1 if it
// falls outside the menu region. Items are ContextMenuRowHeight tall and
// stack from Menu.X, Menu.Y downward.
func (s *State) menuHitIndex(x, y int) int {
	if s.Menu == nil {
		return -1
	}
	mx0 := s.Menu.X
	my0 := s.Menu.Y
	if x < mx0 || x >= mx0+ContextMenuWidth {
		return -1
	}
	if y < my0 {
		return -1
	}
	rel := (y - my0) / ContextMenuRowHeight
	if rel < 0 || rel >= len(s.Menu.Items) {
		return -1
	}
	return rel
}

// applyMenuAction runs the menu action keyed by id on the supplied target
// path. For "newfolder" / "newfile" the target is unused (the action creates
// a child of the current directory with an auto-generated name).
func (s *State) applyMenuAction(id, target string) {
	switch id {
	case "open":
		if s.VFS.IsDir(target) {
			s.Browser.CurrentPath = target
			s.Browser.Cursor = 0
			s.Browser.Refresh(s.VFS)
			s.syncSidebar()
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
	case "rename":
		// v0 stub -- rename isn't wired yet (no inline editor).
	case "newfolder":
		s.createSibling(true)
	case "newfile":
		s.createSibling(false)
	}
}

// createSibling generates an unused name in the current directory and
// creates either an empty folder or an empty file. Used by the empty-area
// context menu. Returns silently on failure (the in-memory + IDB-backed
// VFS impls don't surface persistence errors).
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
		return
	}
}

// openPreview reads the target file and stores it in s.Preview for the
// renderer to overlay. Splits into lines on '\n'; a missing file or read
// error leaves Preview cleared so the overlay disappears cleanly.
func (s *State) openPreview(path string) {
	data, err := s.VFS.Read(path)
	if err != nil {
		s.Preview = nil
		return
	}
	body := string(data)
	lines := splitLines(body, PreviewMaxLines)
	if len(lines) == 0 {
		lines = []string{"(empty file)"}
	}
	s.Preview = &PreviewOverlay{Path: path, Lines: lines}
}

// hasTextExt reports whether name ends with one of the extensions the
// preview overlay knows how to render (.txt, .md). Pulled out so the
// double-click + the menu's Open both agree on "is this previewable?".
func hasTextExt(name string) bool {
	if len(name) >= 4 && name[len(name)-4:] == ".txt" {
		return true
	}
	if len(name) >= 3 && name[len(name)-3:] == ".md" {
		return true
	}
	return false
}

// splitLines splits body on '\n' returning at most maxLines lines. Empty
// trailing line from a final '\n' is dropped.
func splitLines(body string, maxLines int) []string {
	if body == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(body) && len(out) < maxLines; i++ {
		if body[i] == '\n' {
			out = append(out, body[start:i])
			start = i + 1
		}
	}
	if len(out) < maxLines && start < len(body) {
		out = append(out, body[start:])
	}
	return out
}

// itoa is a tiny base-10 formatter used by createSibling to disambiguate
// "untitled-1.txt" / "untitled-2.txt". Pulled local so the package keeps
// strconv out of its import list (which the renderer already pays for).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
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
