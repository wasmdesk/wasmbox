// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import "testing"

// DefaultSidebar carries the canonical Nautilus-style two-section list.
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
	if sb[0].Section != "BOOKMARKS" || sb[3].Section != "BOOKMARKS" {
		t.Errorf("expected BOOKMARKS for indices 0..3")
	}
	if sb[4].Section != "OTHER LOCATIONS" || sb[5].Section != "OTHER LOCATIONS" {
		t.Errorf("expected OTHER LOCATIONS for indices 4..5")
	}
	if sb[0].Kind != "home" || sb[4].Kind != "computer" || sb[5].Kind != "trash" {
		t.Errorf("kinds wrong: %q %q %q", sb[0].Kind, sb[4].Kind, sb[5].Kind)
	}
}

// MoveCursor advances + retreats inside the row range and clamps at the ends.
func TestMoveCursorClamps(t *testing.T) {
	b := &BrowserState{CurrentPath: "/"}
	b.Refresh(NewDemoVFS())
	b.MoveCursor(1)
	if b.Cursor != 1 {
		t.Errorf("Cursor after +1 = %d, want 1", b.Cursor)
	}
	b.MoveCursor(-5)
	if b.Cursor != 0 {
		t.Errorf("Cursor after -5 = %d, want 0 (clamp low)", b.Cursor)
	}
	b.MoveCursor(100)
	if want := len(b.Entries) - 1; b.Cursor != want {
		t.Errorf("Cursor after +100 = %d, want %d (clamp high)", b.Cursor, want)
	}
}

// MoveCursor on an empty entries slice pins Cursor at 0.
func TestMoveCursorEmpty(t *testing.T) {
	b := &BrowserState{Entries: nil, Cursor: 5}
	b.MoveCursor(1)
	if b.Cursor != 0 {
		t.Errorf("Cursor after MoveCursor on empty = %d, want 0", b.Cursor)
	}
}

// ActivateCurrent: directory descends, file + out-of-range + empty are no-ops.
func TestActivateCurrent(t *testing.T) {
	vfs := NewDemoVFS()
	b := &BrowserState{CurrentPath: "/"}
	b.Refresh(vfs)
	if !b.ActivateCurrent(vfs) { // row 0 = /Documents
		t.Fatal("ActivateCurrent on /Documents returned false")
	}
	if b.CurrentPath != "/Documents" {
		t.Errorf("CurrentPath = %q, want /Documents", b.CurrentPath)
	}
	b.Cursor = 999
	if b.ActivateCurrent(vfs) {
		t.Error("ActivateCurrent out of range returned true")
	}
	b.Entries = nil
	b.Cursor = 0
	if b.ActivateCurrent(vfs) {
		t.Error("ActivateCurrent on empty returned true")
	}
	// A file is a no-op.
	b2 := &BrowserState{CurrentPath: "/"}
	b2.Refresh(vfs)
	b2.Cursor = 3 // about.txt
	if b2.ActivateCurrent(vfs) {
		t.Error("ActivateCurrent on file returned true")
	}
}

// GoUp: no-op at the root, ascends from a nested path.
func TestGoUp(t *testing.T) {
	vfs := NewDemoVFS()
	b := &BrowserState{CurrentPath: "/"}
	b.Refresh(vfs)
	if b.GoUp(vfs) {
		t.Error("GoUp at root returned true")
	}
	_ = b.ActivateCurrent(vfs) // -> /Documents
	if !b.GoUp(vfs) {
		t.Fatal("GoUp from /Documents returned false")
	}
	if b.CurrentPath != "/" {
		t.Errorf("CurrentPath after GoUp = %q, want /", b.CurrentPath)
	}
}

// Refresh on an unreadable CurrentPath falls back to "/".
func TestRefreshFallsBackOnMissing(t *testing.T) {
	vfs := NewDemoVFS()
	b := &BrowserState{CurrentPath: "/missing"}
	b.Refresh(vfs)
	if b.CurrentPath != "/" {
		t.Errorf("CurrentPath after Refresh of missing = %q, want /", b.CurrentPath)
	}
}

// PathCrumbs at the root, nested, and deep.
func TestPathCrumbs(t *testing.T) {
	if got := PathCrumbs("/"); len(got) != 1 || got[0] != "Home" {
		t.Errorf("PathCrumbs(/) = %v, want [Home]", got)
	}
	got := PathCrumbs("/a/b/c")
	want := []string{"Home", "a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("PathCrumbs(/a/b/c) = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("crumb[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// A trailing slash produces no empty segment.
	if c := PathCrumbs("/x/"); len(c) != 2 || c[1] != "x" {
		t.Errorf("PathCrumbs(/x/) = %v, want [Home x]", c)
	}
}

// hasTextExt covers the previewable filename rule.
func TestHasTextExt(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"foo.txt", true}, {"foo.md", true}, {"foo.png", false}, {"a", false}, {"", false},
	}
	for _, c := range cases {
		if got := hasTextExt(c.name); got != c.want {
			t.Errorf("hasTextExt(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// splitLines respects the maxLines cap + trailing-newline + no-newline rules.
func TestSplitLines(t *testing.T) {
	if out := splitLines("", 5); out != nil {
		t.Errorf("splitLines empty = %v", out)
	}
	if out := splitLines("a\nb\nc", 2); len(out) != 2 || out[0] != "a" || out[1] != "b" {
		t.Errorf("splitLines cap = %v", out)
	}
	if out := splitLines("a\nb\nc\n", 10); len(out) != 3 {
		t.Errorf("splitLines trailing newline = %v", out)
	}
	if out := splitLines("noLF", 10); len(out) != 1 || out[0] != "noLF" {
		t.Errorf("splitLines noLF = %v", out)
	}
}

// itoa covers 0, positive, negative.
func TestItoa(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{{0, "0"}, {1, "1"}, {10, "10"}, {123, "123"}, {-7, "-7"}}
	for _, c := range cases {
		if got := itoa(c.in); got != c.want {
			t.Errorf("itoa(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// itoa64 covers 0, positive, negative (the negative arm is unreachable via
// formatSize, so it is exercised directly here).
func TestItoa64(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{{0, "0"}, {42, "42"}, {-42, "-42"}}
	for _, c := range cases {
		if got := itoa64(c.in); got != c.want {
			t.Errorf("itoa64(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// formatSize edge cases: 0, sub-1024, exactly 1024, multi-tier, TB cap.
func TestFormatSize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 bytes"},
		{1, "1 bytes"},
		{1023, "1023 bytes"},
		{1024, "1.0 KB"},
		{1234, "1.2 KB"},
		{89012, "86.9 KB"},
		{1024 * 1024, "1.0 MB"},
		{12 * 1024 * 1024, "12.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TB"},
		{2 * 1024 * 1024 * 1024 * 1024 * 1024, "2048.0 TB"},
	}
	for _, c := range cases {
		if got := formatSize(c.in); got != c.want {
			t.Errorf("formatSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Basename edge cases (re-exported from sharedvfs via vfs.go).
func TestBasename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/", "/"}, {"/Documents", "Documents"}, {"/Documents/notes.md", "notes.md"}, {"", "/"},
	}
	for _, c := range cases {
		if got := Basename(c.in); got != c.want {
			t.Errorf("Basename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
