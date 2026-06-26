// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import "testing"

// New returns a usable State rooted at "/" with a non-empty entry list.
func TestNewSeedsRoot(t *testing.T) {
	s := New(480, 360)
	if s.W != 480 || s.H != 360 {
		t.Fatalf("New dims = (%d,%d), want (480,360)", s.W, s.H)
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
}

// MoveCursor advances and retreats inside the row range.
func TestMoveCursor(t *testing.T) {
	s := New(480, 360)
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
	s := New(480, 360)
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
	s := New(480, 360)
	// First entry is Documents (dir-first sort).
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

// ActivateCurrent on a file is a no-op (path unchanged, returns false).
func TestActivateFile(t *testing.T) {
	s := New(480, 360)
	// Move to the about.txt entry (index 3 in dir-first sort).
	s.Browser.Cursor = 3
	if s.Browser.ActivateCurrent(s.VFS) {
		t.Errorf("ActivateCurrent on file returned true")
	}
	if s.Browser.CurrentPath != "/" {
		t.Errorf("CurrentPath = %q, want / (file activate no-op)", s.Browser.CurrentPath)
	}
}

// ActivateCurrent with the cursor out of range is a safe no-op.
func TestActivateCursorOutOfRange(t *testing.T) {
	s := New(480, 360)
	s.Browser.Cursor = 999
	if s.Browser.ActivateCurrent(s.VFS) {
		t.Errorf("ActivateCurrent on out-of-range cursor returned true")
	}
}

// ActivateCurrent on an empty list (no entries) is a no-op.
func TestActivateEmptyList(t *testing.T) {
	s := New(480, 360)
	s.Browser.Entries = nil
	s.Browser.Cursor = 0
	if s.Browser.ActivateCurrent(s.VFS) {
		t.Errorf("ActivateCurrent on empty entries returned true")
	}
}

// GoUp from "/" stays at "/" (no change, returns false).
func TestGoUpAtRoot(t *testing.T) {
	s := New(480, 360)
	if s.Browser.GoUp(s.VFS) {
		t.Errorf("GoUp at root returned true")
	}
	if s.Browser.CurrentPath != "/" {
		t.Errorf("CurrentPath = %q, want /", s.Browser.CurrentPath)
	}
}

// GoUp from /Documents returns to "/" and re-lists.
func TestGoUpFromNested(t *testing.T) {
	s := New(480, 360)
	_ = s.Browser.ActivateCurrent(s.VFS) // -> /Documents
	if !s.Browser.GoUp(s.VFS) {
		t.Fatalf("GoUp from /Documents returned false")
	}
	if s.Browser.CurrentPath != "/" {
		t.Errorf("CurrentPath = %q, want /", s.Browser.CurrentPath)
	}
	if len(s.Browser.Entries) != 4 {
		t.Errorf("entries after GoUp = %d, want 4", len(s.Browser.Entries))
	}
}

// HandleKey routes the documented keys + ignores everything else.
func TestHandleKey(t *testing.T) {
	s := New(480, 360)

	// ArrowDown moves cursor.
	if !s.HandleKey("ArrowDown") {
		t.Errorf("ArrowDown returned false")
	}
	if s.Browser.Cursor != 1 {
		t.Errorf("Cursor after ArrowDown = %d, want 1", s.Browser.Cursor)
	}

	// ArrowUp moves cursor back.
	if !s.HandleKey("ArrowUp") {
		t.Errorf("ArrowUp returned false")
	}
	if s.Browser.Cursor != 0 {
		t.Errorf("Cursor after ArrowUp = %d, want 0", s.Browser.Cursor)
	}

	// ArrowUp at the top is a no-op (returns false because cursor did not move).
	if s.HandleKey("ArrowUp") {
		t.Errorf("ArrowUp at top returned true")
	}

	// Enter activates the current (Documents) folder.
	if !s.HandleKey("Enter") {
		t.Errorf("Enter on folder returned false")
	}
	if s.Browser.CurrentPath != "/Documents" {
		t.Errorf("CurrentPath after Enter = %q, want /Documents", s.Browser.CurrentPath)
	}

	// Escape goes up.
	if !s.HandleKey("Escape") {
		t.Errorf("Escape returned false")
	}
	if s.Browser.CurrentPath != "/" {
		t.Errorf("CurrentPath after Escape = %q, want /", s.Browser.CurrentPath)
	}

	// Backspace also goes up — verify after re-descending.
	_ = s.HandleKey("Enter")
	if !s.HandleKey("Backspace") {
		t.Errorf("Backspace returned false")
	}
	if s.Browser.CurrentPath != "/" {
		t.Errorf("CurrentPath after Backspace = %q, want /", s.Browser.CurrentPath)
	}

	// An unknown key returns false.
	if s.HandleKey("F1") {
		t.Errorf("F1 returned true")
	}
}

// Refresh on a now-unreadable CurrentPath falls back to "/".
func TestRefreshFallsBackOnMissing(t *testing.T) {
	s := New(480, 360)
	s.Browser.CurrentPath = "/missing"
	s.Browser.Refresh(s.VFS)
	if s.Browser.CurrentPath != "/" {
		t.Errorf("CurrentPath after Refresh of missing = %q, want /", s.Browser.CurrentPath)
	}
	if len(s.Browser.Entries) != 4 {
		t.Errorf("entries after fallback = %d, want 4", len(s.Browser.Entries))
	}
}

// MoveCursor on an empty entries slice pins Cursor at 0.
func TestMoveCursorEmpty(t *testing.T) {
	s := New(480, 360)
	s.Browser.Entries = nil
	s.Browser.Cursor = 5
	s.Browser.MoveCursor(1)
	if s.Browser.Cursor != 0 {
		t.Errorf("Cursor after MoveCursor on empty = %d, want 0", s.Browser.Cursor)
	}
}
