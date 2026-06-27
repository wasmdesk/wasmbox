// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import "testing"

// firstBookmarkY (test-only) returns the y of the first sidebar entry row,
// duplicated here so state_test does not depend on render_test's helpers.
func firstBookmarkY() int {
	return HeaderBarHeight + SidebarTopPadding + SidebarSectionHeaderHeight
}

// otherLocationsY returns the y of the first entry under the "OTHER
// LOCATIONS" section (the Computer row). DefaultSidebar has 4 BOOKMARKS
// entries between the section header and the Other-Locations header.
func otherLocationsY() int {
	return firstBookmarkY() + 4*SidebarRowHeight + SidebarSectionHeaderHeight
}

// New returns a usable State rooted at "/" with a non-empty entry list, a
// default sidebar, and SidebarSelected pointing at the Home row (which owns
// "/" in DefaultSidebar).
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
	if s.SidebarSelected != 0 {
		t.Errorf("SidebarSelected = %d, want 0 (Home owns /)", s.SidebarSelected)
	}
}

// DefaultSidebar contains the canonical Nautilus-style two-section list.
func TestDefaultSidebar(t *testing.T) {
	sb := DefaultSidebar()
	wantNames := []string{"Home", "Documents", "Pictures", "Downloads", "Computer", "Trash"}
	if len(sb) != len(wantNames) {
		t.Fatalf("sidebar len = %d, want %d", len(sb), len(wantNames))
	}
	for i, want := range wantNames {
		if sb[i].Name != want {
			t.Errorf("sidebar[%d].Name = %q, want %q", i, sb[i].Name, want)
		}
	}
	// Verify the two sections (Bookmarks first, Other Locations second).
	if sb[0].Section != "BOOKMARKS" || sb[3].Section != "BOOKMARKS" {
		t.Errorf("expected Bookmarks for indices 0..3")
	}
	if sb[4].Section != "OTHER LOCATIONS" || sb[5].Section != "OTHER LOCATIONS" {
		t.Errorf("expected Other Locations for indices 4..5")
	}
	// Verify kinds.
	if sb[0].Kind != "home" {
		t.Errorf("sb[0].Kind = %q, want home", sb[0].Kind)
	}
	if sb[4].Kind != "computer" {
		t.Errorf("sb[4].Kind = %q, want computer", sb[4].Kind)
	}
	if sb[5].Kind != "trash" {
		t.Errorf("sb[5].Kind = %q, want trash", sb[5].Kind)
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
	// SidebarSelected should now point to Documents (index 1).
	if s.SidebarSelected != 1 {
		t.Errorf("SidebarSelected after Enter into /Documents = %d, want 1", s.SidebarSelected)
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

// HandleMouse on the hamburger button returns false but logs (stub).
func TestHandleMouseHamburger(t *testing.T) {
	s := New(720, 440)
	x := HamburgerBtnX + HamburgerBtnW/2
	y := HamburgerBtnY + HamburgerBtnH/2
	if s.HandleMouse(x, y) {
		t.Errorf("HandleMouse(hamburger) returned true (expected stub no-op)")
	}
}

// HandleMouse on the forward button returns false (no forward history).
func TestHandleMouseForward(t *testing.T) {
	s := New(720, 440)
	x := ForwardBtnX + ForwardBtnW/2
	y := ForwardBtnY + ForwardBtnH/2
	if s.HandleMouse(x, y) {
		t.Errorf("HandleMouse(forward) returned true (expected no-op stub)")
	}
}

// HandleMouse in the header bar away from any button is a no-op.
func TestHandleMouseHeaderBarMiss(t *testing.T) {
	s := New(720, 440)
	// Click in the empty space inside the header bar past the path bar.
	if s.HandleMouse(700, HeaderBarHeight/2) {
		t.Errorf("HandleMouse(header bar miss) returned true")
	}
}

// HandleMouse on the first sidebar row (Home) at root is a no-op (already
// selected).
func TestHandleMouseSidebarHomeAtRoot(t *testing.T) {
	s := New(720, 440)
	x := 20
	y := firstBookmarkY() + SidebarRowHeight/2
	if s.HandleMouse(x, y) {
		t.Errorf("HandleMouse(Home at root) returned true (already selected)")
	}
}

// HandleMouse on a sidebar entry jumps to that path.
func TestHandleMouseSidebarDocuments(t *testing.T) {
	s := New(720, 440)
	// Click on Documents -- second row in the BOOKMARKS section.
	x := 20
	y := firstBookmarkY() + SidebarRowHeight + SidebarRowHeight/2
	if !s.HandleMouse(x, y) {
		t.Errorf("HandleMouse(sidebar Documents) returned false")
	}
	if s.Browser.CurrentPath != "/Documents" {
		t.Errorf("CurrentPath after sidebar click = %q, want /Documents", s.Browser.CurrentPath)
	}
	if s.SidebarSelected != 1 {
		t.Errorf("SidebarSelected = %d, want 1", s.SidebarSelected)
	}
	// Clicking it again is a no-op (state unchanged).
	if s.HandleMouse(x, y) {
		t.Errorf("repeated sidebar click returned true")
	}
}

// HandleMouse on the "BOOKMARKS" section-label band returns false (the
// label is not interactive).
func TestHandleMouseSidebarSectionLabel(t *testing.T) {
	s := New(720, 440)
	// y inside the section label band.
	y := HeaderBarHeight + SidebarTopPadding + SidebarSectionHeaderHeight/2
	if s.HandleMouse(20, y) {
		t.Errorf("HandleMouse on section label returned true")
	}
}

// HandleMouse on the second section's label ("OTHER LOCATIONS") returns
// false.
func TestHandleMouseSidebarOtherSectionLabel(t *testing.T) {
	s := New(720, 440)
	// The OTHER LOCATIONS label sits right after the 4th bookmark row.
	y := firstBookmarkY() + 4*SidebarRowHeight + SidebarSectionHeaderHeight/2
	if s.HandleMouse(20, y) {
		t.Errorf("HandleMouse on OTHER LOCATIONS label returned true")
	}
}

// HandleMouse on a row in the Other Locations section navigates (the path
// "/" matches Home so Computer/Trash navigate-but-keep-the-Home-selection).
// We exercise the click landing inside the band; CurrentPath stays "/" so
// the click reports false ("path unchanged and same selection") -- this
// drives the early-out branch in handleSidebarClick.
func TestHandleMouseSidebarComputerAtRoot(t *testing.T) {
	s := New(720, 440)
	x := 20
	y := otherLocationsY() + SidebarRowHeight/2
	// At root, Home is selected (index 0) and Computer (index 4) points at
	// "/", so the click switches the selected index even if the path is the
	// same: handleSidebarClick treats "same path + same idx" as no-op, but
	// "same path + different idx" must still update.
	changed := s.HandleMouse(x, y)
	if !changed {
		t.Errorf("HandleMouse(Computer) at root returned false; expected SidebarSelected to move")
	}
	if s.SidebarSelected != 4 {
		t.Errorf("SidebarSelected = %d, want 4 (Computer)", s.SidebarSelected)
	}
}

// HandleMouse on the sidebar past the last row is a no-op.
func TestHandleMouseSidebarBelowRows(t *testing.T) {
	s := New(720, 440)
	// Way below all sidebar rows.
	if s.HandleMouse(20, 430) {
		t.Errorf("HandleMouse below sidebar rows returned true")
	}
}

// HandleMouse on a list row (a folder) activates it.
func TestHandleMouseListRowFolder(t *testing.T) {
	s := New(720, 440)
	// Click on row 0 (Documents) -- it's a directory, so we descend.
	x := SidebarWidth + 50
	y := HeaderBarHeight + ColumnHeaderHeight + RowHeight/2
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
	y := HeaderBarHeight + ColumnHeaderHeight + 3*RowHeight + RowHeight/2
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

// HandleMouse in the column-header band (between header bar and rows) is a
// no-op.
func TestHandleMouseColumnHeader(t *testing.T) {
	s := New(720, 440)
	x := SidebarWidth + 50
	y := HeaderBarHeight + ColumnHeaderHeight/2
	if s.HandleMouse(x, y) {
		t.Errorf("HandleMouse on column-header band returned true")
	}
}

// HandleMouse past the last list row is a no-op.
func TestHandleMouseListBeyond(t *testing.T) {
	s := New(720, 440)
	x := SidebarWidth + 50
	y := HeaderBarHeight + ColumnHeaderHeight + 10*RowHeight
	if s.HandleMouse(x, y) {
		t.Errorf("HandleMouse past last list row returned true")
	}
}

// syncSidebar with a path that does not match any sidebar entry leaves
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

// sidebarHitIndex returns a stable index across every band, including the
// last row in Other Locations.
func TestSidebarHitIndex(t *testing.T) {
	s := New(720, 440)
	// Trash is the last sidebar entry (index 5). Land on its row band.
	y := otherLocationsY() + SidebarRowHeight + SidebarRowHeight/2
	if idx := s.sidebarHitIndex(y); idx != 5 {
		t.Errorf("sidebarHitIndex(Trash band) = %d, want 5", idx)
	}
	// A y above every row maps to -1.
	if idx := s.sidebarHitIndex(0); idx != -1 {
		t.Errorf("sidebarHitIndex(0) = %d, want -1", idx)
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
