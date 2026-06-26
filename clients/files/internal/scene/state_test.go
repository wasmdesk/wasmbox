// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import "testing"

// New returns a usable State rooted at "/" with a non-empty entry list, a
// default Favorites sidebar, and no Favorite selected (since "/" is not in
// the default sidebar by path).
func TestNewSeedsRoot(t *testing.T) {
	s := New(720, 440)
	if s.W != 720 || s.H != 440 {
		t.Fatalf("New dims = (%d,%d), want (720,440)", s.W, s.H)
	}
	if s.Browser.CurrentPath != "/" {
		t.Errorf("CurrentPath = %q, want /", s.Browser.CurrentPath)
	}
	if len(s.Browser.Entries) != 4 {
		t.Errorf("root entry count = %d, want 4", len(s.Browser.Entries))
	}
	if s.Browser.Cursor != 0 {
		t.Errorf("Cursor = %d, want 0", s.Browser.Cursor)
	}
	if len(s.Sidebar) == 0 {
		t.Errorf("sidebar empty")
	}
	if s.SidebarSelected != -1 {
		t.Errorf("SidebarSelected = %d, want -1 (root not in sidebar)", s.SidebarSelected)
	}
}

// DefaultSidebar contains at least Documents + Pictures + Downloads.
func TestDefaultSidebar(t *testing.T) {
	sb := DefaultSidebar()
	names := map[string]bool{}
	for _, e := range sb {
		names[e.Name] = true
	}
	for _, want := range []string{"Documents", "Pictures", "Downloads"} {
		if !names[want] {
			t.Errorf("sidebar missing %q", want)
		}
	}
}

// MoveCursor advances and retreats inside the row range.
func TestMoveCursor(t *testing.T) {
	s := New(720, 440)
	s.Browser.MoveCursor(1)
	if s.Browser.Cursor != 1 {
		t.Errorf("Cursor after +1 = %d, want 1", s.Browser.Cursor)
	}
	s.Browser.MoveCursor(-1)
	if s.Browser.Cursor != 0 {
		t.Errorf("Cursor after -1 = %d, want 0", s.Browser.Cursor)
	}
}

// MoveCursor clamps below zero and above len(Entries)-1.
func TestMoveCursorClamps(t *testing.T) {
	s := New(720, 440)
	s.Browser.MoveCursor(-5)
	if s.Browser.Cursor != 0 {
		t.Errorf("Cursor after -5 = %d, want 0 (clamp low)", s.Browser.Cursor)
	}
	s.Browser.MoveCursor(100)
	want := len(s.Browser.Entries) - 1
	if s.Browser.Cursor != want {
		t.Errorf("Cursor after +100 = %d, want %d (clamp high)", s.Browser.Cursor, want)
	}
}

// ActivateCurrent on a directory navigates into it and re-lists.
func TestActivateFolder(t *testing.T) {
	s := New(720, 440)
	if !s.Browser.ActivateCurrent(s.VFS) {
		t.Fatalf("ActivateCurrent on /Documents returned false")
	}
	if s.Browser.CurrentPath != "/Documents" {
		t.Errorf("CurrentPath = %q, want /Documents", s.Browser.CurrentPath)
	}
	if len(s.Browser.Entries) != 2 {
		t.Errorf("Documents entries = %d, want 2", len(s.Browser.Entries))
	}
}

// ActivateCurrent on a file is a no-op.
func TestActivateFile(t *testing.T) {
	s := New(720, 440)
	s.Browser.Cursor = 3 // about.txt
	if s.Browser.ActivateCurrent(s.VFS) {
		t.Errorf("ActivateCurrent on file returned true")
	}
}

// ActivateCurrent with the cursor out of range is a safe no-op.
func TestActivateCursorOutOfRange(t *testing.T) {
	s := New(720, 440)
	s.Browser.Cursor = 999
	if s.Browser.ActivateCurrent(s.VFS) {
		t.Errorf("ActivateCurrent on out-of-range cursor returned true")
	}
}

// ActivateCurrent on an empty list is a no-op.
func TestActivateEmptyList(t *testing.T) {
	s := New(720, 440)
	s.Browser.Entries = nil
	s.Browser.Cursor = 0
	if s.Browser.ActivateCurrent(s.VFS) {
		t.Errorf("ActivateCurrent on empty entries returned true")
	}
}

// GoUp from "/" is a no-op.
func TestGoUpAtRoot(t *testing.T) {
	s := New(720, 440)
	if s.Browser.GoUp(s.VFS) {
		t.Errorf("GoUp at root returned true")
	}
}

// GoUp from /Documents returns to "/" and re-lists.
func TestGoUpFromNested(t *testing.T) {
	s := New(720, 440)
	_ = s.Browser.ActivateCurrent(s.VFS)
	if !s.Browser.GoUp(s.VFS) {
		t.Fatalf("GoUp from /Documents returned false")
	}
	if s.Browser.CurrentPath != "/" {
		t.Errorf("CurrentPath = %q, want /", s.Browser.CurrentPath)
	}
}

// HandleKey: ArrowDown/Up/Enter/Backspace/Escape/unknown.
func TestHandleKey(t *testing.T) {
	s := New(720, 440)
	if !s.HandleKey("ArrowDown") {
		t.Errorf("ArrowDown returned false")
	}
	if s.Browser.Cursor != 1 {
		t.Errorf("Cursor after ArrowDown = %d, want 1", s.Browser.Cursor)
	}
	if !s.HandleKey("ArrowUp") {
		t.Errorf("ArrowUp returned false")
	}
	if s.Browser.Cursor != 0 {
		t.Errorf("Cursor after ArrowUp = %d, want 0", s.Browser.Cursor)
	}
	if s.HandleKey("ArrowUp") {
		t.Errorf("ArrowUp at top returned true")
	}
	if !s.HandleKey("Enter") {
		t.Errorf("Enter on folder returned false")
	}
	if s.Browser.CurrentPath != "/Documents" {
		t.Errorf("CurrentPath after Enter = %q, want /Documents", s.Browser.CurrentPath)
	}
	// SidebarSelected should now point to Documents (index 0 in DefaultSidebar).
	if s.SidebarSelected != 0 {
		t.Errorf("SidebarSelected after Enter into /Documents = %d, want 0", s.SidebarSelected)
	}
	if !s.HandleKey("Escape") {
		t.Errorf("Escape returned false")
	}
	if s.Browser.CurrentPath != "/" {
		t.Errorf("CurrentPath after Escape = %q, want /", s.Browser.CurrentPath)
	}
	// Backspace also goes up.
	_ = s.HandleKey("Enter")
	if !s.HandleKey("Backspace") {
		t.Errorf("Backspace returned false")
	}
	if s.HandleKey("F1") {
		t.Errorf("F1 returned true")
	}
}

// HandleKey returns false for Enter on a file (no navigation) without
// crashing the sidebar sync.
func TestHandleKeyEnterOnFile(t *testing.T) {
	s := New(720, 440)
	s.Browser.Cursor = 3 // about.txt
	if s.HandleKey("Enter") {
		t.Errorf("Enter on file returned true")
	}
}

// HandleKey returns false for Backspace at root.
func TestHandleKeyBackspaceAtRoot(t *testing.T) {
	s := New(720, 440)
	if s.HandleKey("Backspace") {
		t.Errorf("Backspace at root returned true")
	}
}

// Refresh on a now-unreadable CurrentPath falls back to "/".
func TestRefreshFallsBackOnMissing(t *testing.T) {
	s := New(720, 440)
	s.Browser.CurrentPath = "/missing"
	s.Browser.Refresh(s.VFS)
	if s.Browser.CurrentPath != "/" {
		t.Errorf("CurrentPath after Refresh of missing = %q, want /", s.Browser.CurrentPath)
	}
}

// MoveCursor on an empty entries slice pins Cursor at 0.
func TestMoveCursorEmpty(t *testing.T) {
	s := New(720, 440)
	s.Browser.Entries = nil
	s.Browser.Cursor = 5
	s.Browser.MoveCursor(1)
	if s.Browser.Cursor != 0 {
		t.Errorf("Cursor after MoveCursor on empty = %d, want 0", s.Browser.Cursor)
	}
}

// HandleMouse on the back-arrow button at root is a no-op.
func TestHandleMouseBackAtRoot(t *testing.T) {
	s := New(720, 440)
	x := BackBtnX + BackBtnW/2
	y := BackBtnY + BackBtnH/2
	if s.HandleMouse(x, y) {
		t.Errorf("HandleMouse(back) at root returned true")
	}
}

// HandleMouse on the back-arrow button after a descent goes up.
func TestHandleMouseBackAfterDescent(t *testing.T) {
	s := New(720, 440)
	_ = s.Browser.ActivateCurrent(s.VFS) // -> /Documents
	x := BackBtnX + BackBtnW/2
	y := BackBtnY + BackBtnH/2
	if !s.HandleMouse(x, y) {
		t.Errorf("HandleMouse(back) returned false after descent")
	}
	if s.Browser.CurrentPath != "/" {
		t.Errorf("CurrentPath after back = %q, want /", s.Browser.CurrentPath)
	}
}

// HandleMouse in the toolbar away from the back button is a no-op.
func TestHandleMouseToolbarMiss(t *testing.T) {
	s := New(720, 440)
	if s.HandleMouse(400, 10) {
		t.Errorf("HandleMouse(toolbar miss) returned true")
	}
}

// HandleMouse on a sidebar Favorite jumps to that path.
func TestHandleMouseSidebarFavorite(t *testing.T) {
	s := New(720, 440)
	// Click on the first Favorite (Documents).
	x := 20
	y := ToolbarHeight + SidebarHeaderHeight + SidebarRowHeight/2
	if !s.HandleMouse(x, y) {
		t.Errorf("HandleMouse(sidebar[0]) returned false")
	}
	if s.Browser.CurrentPath != "/Documents" {
		t.Errorf("CurrentPath after sidebar click = %q, want /Documents", s.Browser.CurrentPath)
	}
	if s.SidebarSelected != 0 {
		t.Errorf("SidebarSelected = %d, want 0", s.SidebarSelected)
	}
	// Clicking it again is a no-op (state unchanged).
	if s.HandleMouse(x, y) {
		t.Errorf("repeated sidebar click returned true")
	}
}

// HandleMouse on the sidebar with y above the first row is a no-op. We pick
// a y far enough above the first row band that integer division produces a
// negative idx (SidebarHeaderHeight=8 + a generous margin so the int-div
// truncation lands at -1, not 0).
func TestHandleMouseSidebarAboveRows(t *testing.T) {
	s := New(720, 440)
	// y = ToolbarHeight - 5 -> (y - ToolbarHeight - SidebarHeaderHeight)
	// = -13 -> idx = -1 (after Go's truncation-toward-zero for negatives,
	// -13/24 == 0; pick a more negative value to guarantee -1).
	if s.HandleMouse(20, ToolbarHeight-30) {
		t.Errorf("HandleMouse above sidebar rows returned true")
	}
}

// HandleMouse on the sidebar past the last row is a no-op.
func TestHandleMouseSidebarBelowRows(t *testing.T) {
	s := New(720, 440)
	// Way below all Favorite rows.
	if s.HandleMouse(20, 400) {
		t.Errorf("HandleMouse below sidebar rows returned true")
	}
}

// HandleMouse on a list row (a folder) activates it.
func TestHandleMouseListRowFolder(t *testing.T) {
	s := New(720, 440)
	// Click on row 0 (Documents) -- it's a directory, so we descend.
	x := SidebarWidth + 50
	y := ToolbarHeight + ColumnHeaderHeight + RowHeight/2
	if !s.HandleMouse(x, y) {
		t.Errorf("HandleMouse on list row[0] returned false")
	}
	if s.Browser.CurrentPath != "/Documents" {
		t.Errorf("CurrentPath after row click = %q, want /Documents", s.Browser.CurrentPath)
	}
}

// HandleMouse on a list row (a file) selects it without navigating.
func TestHandleMouseListRowFile(t *testing.T) {
	s := New(720, 440)
	// Row 3 is about.txt.
	x := SidebarWidth + 50
	y := ToolbarHeight + ColumnHeaderHeight + 3*RowHeight + RowHeight/2
	if !s.HandleMouse(x, y) {
		t.Errorf("HandleMouse on file row returned false")
	}
	if s.Browser.Cursor != 3 {
		t.Errorf("Cursor after file-row click = %d, want 3", s.Browser.Cursor)
	}
	if s.Browser.CurrentPath != "/" {
		t.Errorf("CurrentPath after file-row click = %q, want /", s.Browser.CurrentPath)
	}
}

// HandleMouse in the column-header band (between toolbar and rows) is a no-op.
func TestHandleMouseColumnHeader(t *testing.T) {
	s := New(720, 440)
	x := SidebarWidth + 50
	y := ToolbarHeight + ColumnHeaderHeight/2
	if s.HandleMouse(x, y) {
		t.Errorf("HandleMouse on column-header band returned true")
	}
}

// HandleMouse past the last list row is a no-op.
func TestHandleMouseListBeyond(t *testing.T) {
	s := New(720, 440)
	x := SidebarWidth + 50
	y := ToolbarHeight + ColumnHeaderHeight + 10*RowHeight
	if s.HandleMouse(x, y) {
		t.Errorf("HandleMouse past last list row returned true")
	}
}

// syncSidebar with a path that does not match any Favorite leaves
// SidebarSelected at -1.
func TestSyncSidebarUnknownPath(t *testing.T) {
	s := New(720, 440)
	s.SidebarSelected = 2
	s.Browser.CurrentPath = "/nope"
	s.syncSidebar()
	if s.SidebarSelected != -1 {
		t.Errorf("SidebarSelected for /nope = %d, want -1", s.SidebarSelected)
	}
}

// Basename: edge cases.
func TestBasename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/", "/"},
		{"/Documents", "Documents"},
		{"/Documents/notes.md", "notes.md"},
		{"", "/"},
	}
	for _, c := range cases {
		if got := Basename(c.in); got != c.want {
			t.Errorf("Basename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
