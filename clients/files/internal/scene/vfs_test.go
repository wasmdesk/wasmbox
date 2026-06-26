// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import (
	"errors"
	"testing"
)

// List("/") returns the four top-level entries (Documents, Pictures,
// Downloads, about.txt) with the three directories sorted before the file.
func TestListRoot(t *testing.T) {
	v := NewDemoVFS()
	got, err := v.List("/")
	if err != nil {
		t.Fatalf("List(/) err = %v, want nil", err)
	}
	if len(got) != 4 {
		t.Fatalf("List(/) len = %d, want 4 (%+v)", len(got), got)
	}
	want := []struct {
		Name  string
		IsDir bool
	}{
		{"Documents", true},
		{"Downloads", true},
		{"Pictures", true},
		{"about.txt", false},
	}
	for i, w := range want {
		if got[i].Name != w.Name || got[i].IsDir != w.IsDir {
			t.Errorf("List(/)[%d] = {%s,%v}, want {%s,%v}", i, got[i].Name, got[i].IsDir, w.Name, w.IsDir)
		}
	}
}

// List on a nested folder returns its (sorted) children.
func TestListNested(t *testing.T) {
	v := NewDemoVFS()
	got, err := v.List("/Documents")
	if err != nil {
		t.Fatalf("List(/Documents) err = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List(/Documents) len = %d, want 2", len(got))
	}
	if got[0].Name != "notes.md" || got[1].Name != "readme.txt" {
		t.Errorf("List(/Documents) names = [%s,%s], want [notes.md,readme.txt]", got[0].Name, got[1].Name)
	}
	if got[0].Size != 567 || got[1].Size != 1234 {
		t.Errorf("List(/Documents) sizes = [%d,%d], want [567,1234]", got[0].Size, got[1].Size)
	}
}

// An empty folder produces an empty (non-nil) slice.
func TestListEmptyFolder(t *testing.T) {
	v := NewDemoVFS()
	got, err := v.List("/Downloads")
	if err != nil {
		t.Fatalf("List(/Downloads) err = %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("List(/Downloads) = %+v, want empty", got)
	}
}

// Stat on a file returns IsDir=false + correct size.
func TestStatFile(t *testing.T) {
	v := NewDemoVFS()
	e, err := v.Stat("/about.txt")
	if err != nil {
		t.Fatalf("Stat err = %v", err)
	}
	if e.IsDir {
		t.Errorf("Stat(/about.txt).IsDir = true, want false")
	}
	if e.Size != 234 {
		t.Errorf("Stat(/about.txt).Size = %d, want 234", e.Size)
	}
}

// Stat on the root returns IsDir=true.
func TestStatRoot(t *testing.T) {
	v := NewDemoVFS()
	e, err := v.Stat("/")
	if err != nil {
		t.Fatalf("Stat(/) err = %v", err)
	}
	if !e.IsDir {
		t.Errorf("Stat(/).IsDir = false, want true")
	}
}

// List on a missing path returns ErrNotFound.
func TestListMissing(t *testing.T) {
	v := NewDemoVFS()
	_, err := v.List("/missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("List(/missing) err = %v, want ErrNotFound", err)
	}
}

// Stat on a missing path returns ErrNotFound.
func TestStatMissing(t *testing.T) {
	v := NewDemoVFS()
	_, err := v.Stat("/nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Stat(/nope) err = %v, want ErrNotFound", err)
	}
}

// List on a file (not a directory) returns ErrNotDir.
func TestListOnFile(t *testing.T) {
	v := NewDemoVFS()
	_, err := v.List("/about.txt")
	if !errors.Is(err, ErrNotDir) {
		t.Errorf("List(/about.txt) err = %v, want ErrNotDir", err)
	}
}

// IsDir true for directories, false for files.
func TestIsDir(t *testing.T) {
	v := NewDemoVFS()
	if !v.IsDir("/Documents") {
		t.Errorf("IsDir(/Documents) = false")
	}
	if v.IsDir("/about.txt") {
		t.Errorf("IsDir(/about.txt) = true, want false")
	}
}

// IsDir on a missing path returns false (no error surface).
func TestIsDirMissing(t *testing.T) {
	v := NewDemoVFS()
	if v.IsDir("/missing") {
		t.Errorf("IsDir(/missing) = true, want false")
	}
}

// Resolve traverses through nested directories to a leaf entry.
func TestResolveNestedFile(t *testing.T) {
	v := NewDemoVFS()
	e, err := v.Stat("/Pictures/cat.png")
	if err != nil {
		t.Fatalf("Stat(/Pictures/cat.png) err = %v", err)
	}
	if e.IsDir || e.Size != 89012 {
		t.Errorf("Stat(/Pictures/cat.png) = %+v", e)
	}
}

// resolve walking *through* a file (rather than a directory) reports
// not-found because intermediate path components must be directories.
func TestResolveThroughFile(t *testing.T) {
	v := NewDemoVFS()
	_, err := v.Stat("/about.txt/inside")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Stat(/about.txt/inside) err = %v, want ErrNotFound", err)
	}
}

// Clean normalises a variety of inputs.
func TestClean(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "/"},
		{"/", "/"},
		{"foo", "/foo"},
		{"/foo/", "/foo"},
		{"/foo//bar", "/foo/bar"},
		{"/foo/./bar", "/foo/bar"},
		{"/foo/../bar", "/bar"},
		{"/../foo", "/foo"},
		{"/foo/..", "/"},
		{".", "/"},
		{"..", "/"},
	}
	for _, c := range cases {
		if got := Clean(c.in); got != c.want {
			t.Errorf("Clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Join concatenates directory + name correctly at root and below.
func TestJoin(t *testing.T) {
	cases := []struct{ dir, name, want string }{
		{"/", "foo", "/foo"},
		{"/foo", "bar", "/foo/bar"},
		{"", "x", "/x"},
		{"/foo/", "bar", "/foo/bar"},
	}
	for _, c := range cases {
		if got := Join(c.dir, c.name); got != c.want {
			t.Errorf("Join(%q,%q) = %q, want %q", c.dir, c.name, got, c.want)
		}
	}
}

// Parent returns the directory above; the parent of "/" is "/".
func TestParent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/", "/"},
		{"/foo", "/"},
		{"/foo/bar", "/foo"},
		{"/foo/bar/baz", "/foo/bar"},
	}
	for _, c := range cases {
		if got := Parent(c.in); got != c.want {
			t.Errorf("Parent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
