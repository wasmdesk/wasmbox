// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's state.go is the file browser's UI state: a current path, a
// cached listing of its entries, and the cursor index inside that listing.
// Pure Go (no syscall/js, no cgo) so it builds + tests natively on every
// architecture the repo targets.

package scene

// State is the top-of-package handle the wasm entry point holds. Surface
// dimensions live alongside the browser state because the renderer uses both
// (rows-per-screen, path-bar width, etc.).
type State struct {
	W, H    int
	VFS     VFS
	Browser *BrowserState
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
// demo VFS, rooted at "/" with the first entry selected.
func New(width, height int) *State {
	vfs := NewDemoVFS()
	bs := &BrowserState{CurrentPath: "/"}
	bs.Refresh(vfs)
	return &State{W: width, H: height, VFS: vfs, Browser: bs}
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
// can decide whether to re-render — though in practice the row inversion
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
		return s.Browser.ActivateCurrent(s.VFS)
	case "Backspace", "Escape":
		return s.Browser.GoUp(s.VFS)
	default:
		return false
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
