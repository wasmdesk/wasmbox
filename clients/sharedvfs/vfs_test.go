// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package sharedvfs

import (
	"errors"
	"testing"
)

// TestCleanCases covers the path-normalisation table.
func TestCleanCases(t *testing.T) {
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
		{"/a/b/c/..", "/a/b"},
	}
	for _, c := range cases {
		if got := Clean(c.in); got != c.want {
			t.Errorf("Clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestJoinCases covers Join.
func TestJoinCases(t *testing.T) {
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

// TestParentCases covers Parent.
func TestParentCases(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/", "/"},
		{"/foo", "/"},
		{"/foo/bar", "/foo"},
		{"/foo/bar/baz", "/foo/bar"},
		{"foo", "/"}, // bare name -> /foo -> /
	}
	for _, c := range cases {
		if got := Parent(c.in); got != c.want {
			t.Errorf("Parent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestBasenameCases covers Basename including the root edge case.
func TestBasenameCases(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/", "/"},
		{"/foo", "foo"},
		{"/foo/bar", "bar"},
		{"", "/"},
	}
	for _, c := range cases {
		if got := Basename(c.in); got != c.want {
			t.Errorf("Basename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestResolveCases covers shell-style path resolution against a cwd.
func TestResolveCases(t *testing.T) {
	cases := []struct {
		cwd, in, want string
	}{
		{"/", "", "/"},
		{"/Documents", "", "/Documents"},
		{"/", "foo", "/foo"},
		{"/Documents", "notes.md", "/Documents/notes.md"},
		{"/Documents", "/Pictures", "/Pictures"},
		{"/Documents", "..", "/"},
		{"/", "~", "/"},
		{"/foo", "~", "/"},
		{"/foo", "~/bar", "/bar"},
	}
	for _, c := range cases {
		if got := Resolve(c.cwd, c.in); got != c.want {
			t.Errorf("Resolve(%q,%q) = %q, want %q", c.cwd, c.in, got, c.want)
		}
	}
}

// TestInMemoryRootList verifies the empty root's List succeeds + is empty.
func TestInMemoryRootList(t *testing.T) {
	v := NewInMemoryVFS()
	es, err := v.List("/")
	if err != nil {
		t.Fatalf("List(/) err = %v", err)
	}
	if len(es) != 0 {
		t.Fatalf("empty root: got %d entries, want 0", len(es))
	}
}

// TestInMemorySeedThenList exercises the demo seed end-to-end.
func TestInMemorySeedThenList(t *testing.T) {
	v := NewInMemoryVFS()
	SeedDemoTree(v)
	es, err := v.List("/")
	if err != nil {
		t.Fatalf("List(/) err = %v", err)
	}
	// Documents, Downloads, Pictures, about.txt -- directories first.
	wantNames := []string{"Documents", "Downloads", "Pictures", "about.txt"}
	if len(es) != len(wantNames) {
		t.Fatalf("root entries = %d (%+v), want %d", len(es), es, len(wantNames))
	}
	for i, w := range wantNames {
		if es[i].Name != w {
			t.Errorf("root[%d].Name = %q, want %q", i, es[i].Name, w)
		}
	}
	// Documents has two files.
	docs, err := v.List("/Documents")
	if err != nil {
		t.Fatalf("List(/Documents) err = %v", err)
	}
	if len(docs) != 2 {
		t.Errorf("Documents entries = %d, want 2", len(docs))
	}
}

// TestSeedIdempotent guarantees a second seed pass does not clobber an edit.
func TestSeedIdempotent(t *testing.T) {
	v := NewInMemoryVFS()
	SeedDemoTree(v)
	if err := v.Write("/Documents/readme.txt", []byte("CUSTOM")); err != nil {
		t.Fatalf("Write err = %v", err)
	}
	SeedDemoTree(v) // should not clobber
	got, err := v.Read("/Documents/readme.txt")
	if err != nil {
		t.Fatalf("Read err = %v", err)
	}
	if string(got) != "CUSTOM" {
		t.Errorf("readme.txt after re-seed = %q, want CUSTOM", string(got))
	}
}

// TestInMemoryMkdirAndList exercises Mkdir + List of a fresh sub-folder.
func TestInMemoryMkdirAndList(t *testing.T) {
	v := NewInMemoryVFS()
	if err := v.Mkdir("/foo"); err != nil {
		t.Fatalf("Mkdir /foo err = %v", err)
	}
	if !v.IsDir("/foo") {
		t.Errorf("IsDir /foo = false")
	}
	if err := v.Mkdir("/foo/bar"); err != nil {
		t.Fatalf("Mkdir /foo/bar err = %v", err)
	}
	es, err := v.List("/foo")
	if err != nil {
		t.Fatalf("List /foo err = %v", err)
	}
	if len(es) != 1 || es[0].Name != "bar" {
		t.Errorf("/foo contents = %+v, want [bar]", es)
	}
}

// TestInMemoryMkdirExists rejects a duplicate.
func TestInMemoryMkdirExists(t *testing.T) {
	v := NewInMemoryVFS()
	_ = v.Mkdir("/foo")
	if err := v.Mkdir("/foo"); !errors.Is(err, ErrExists) {
		t.Errorf("re-Mkdir /foo err = %v, want ErrExists", err)
	}
	if err := v.Mkdir("/"); !errors.Is(err, ErrExists) {
		t.Errorf("Mkdir / err = %v, want ErrExists", err)
	}
}

// TestInMemoryMkdirMissingParent rejects a path whose parent does not exist.
func TestInMemoryMkdirMissingParent(t *testing.T) {
	v := NewInMemoryVFS()
	if err := v.Mkdir("/a/b"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Mkdir /a/b err = %v, want ErrNotFound", err)
	}
}

// TestInMemoryWriteAndRead round-trips a file.
func TestInMemoryWriteAndRead(t *testing.T) {
	v := NewInMemoryVFS()
	body := []byte("hello world")
	if err := v.Write("/hi.txt", body); err != nil {
		t.Fatalf("Write err = %v", err)
	}
	got, err := v.Read("/hi.txt")
	if err != nil {
		t.Fatalf("Read err = %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("Read = %q, want %q", string(got), "hello world")
	}
	// Mutating the returned slice must not affect the store.
	got[0] = 'H'
	got2, _ := v.Read("/hi.txt")
	if string(got2) != "hello world" {
		t.Errorf("store mutated by caller: %q", string(got2))
	}
}

// TestInMemoryWriteOverwrite replaces existing content.
func TestInMemoryWriteOverwrite(t *testing.T) {
	v := NewInMemoryVFS()
	_ = v.Write("/x", []byte("v1"))
	_ = v.Write("/x", []byte("v2"))
	got, _ := v.Read("/x")
	if string(got) != "v2" {
		t.Errorf("after overwrite = %q, want v2", string(got))
	}
}

// TestInMemoryWriteRoot rejects writing to "/".
func TestInMemoryWriteRoot(t *testing.T) {
	v := NewInMemoryVFS()
	if err := v.Write("/", []byte("x")); !errors.Is(err, ErrInvalid) {
		t.Errorf("Write / err = %v, want ErrInvalid", err)
	}
}

// TestInMemoryWriteMissingParent fails if the parent doesn't exist.
func TestInMemoryWriteMissingParent(t *testing.T) {
	v := NewInMemoryVFS()
	if err := v.Write("/a/b.txt", []byte("x")); !errors.Is(err, ErrNotFound) {
		t.Errorf("Write /a/b.txt err = %v, want ErrNotFound", err)
	}
}

// TestInMemoryWriteOverDir refuses to write a file over a directory.
func TestInMemoryWriteOverDir(t *testing.T) {
	v := NewInMemoryVFS()
	_ = v.Mkdir("/foo")
	if err := v.Write("/foo", []byte("x")); !errors.Is(err, ErrIsDir) {
		t.Errorf("Write over dir err = %v, want ErrIsDir", err)
	}
}

// TestInMemoryReadMissing covers ErrNotFound on Read.
func TestInMemoryReadMissing(t *testing.T) {
	v := NewInMemoryVFS()
	if _, err := v.Read("/none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Read /none err = %v, want ErrNotFound", err)
	}
}

// TestInMemoryReadDir covers ErrIsDir.
func TestInMemoryReadDir(t *testing.T) {
	v := NewInMemoryVFS()
	_ = v.Mkdir("/d")
	if _, err := v.Read("/d"); !errors.Is(err, ErrIsDir) {
		t.Errorf("Read /d err = %v, want ErrIsDir", err)
	}
}

// TestInMemoryStat covers Stat / ErrNotFound + the root.
func TestInMemoryStat(t *testing.T) {
	v := NewInMemoryVFS()
	e, err := v.Stat("/")
	if err != nil {
		t.Fatalf("Stat(/) err = %v", err)
	}
	if !e.IsDir {
		t.Errorf("Stat(/).IsDir = false")
	}
	_ = v.Write("/x", []byte("ab"))
	e, _ = v.Stat("/x")
	if e.IsDir || e.Size != 2 {
		t.Errorf("Stat(/x) = %+v", e)
	}
	if _, err := v.Stat("/none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Stat(/none) err = %v, want ErrNotFound", err)
	}
}

// TestInMemoryIsDir covers true / false / missing.
func TestInMemoryIsDir(t *testing.T) {
	v := NewInMemoryVFS()
	_ = v.Mkdir("/d")
	_ = v.Write("/f", []byte("x"))
	if !v.IsDir("/d") {
		t.Errorf("IsDir /d = false")
	}
	if v.IsDir("/f") {
		t.Errorf("IsDir /f = true")
	}
	if v.IsDir("/none") {
		t.Errorf("IsDir /none = true")
	}
}

// TestInMemoryListMissing / NotDir.
func TestInMemoryListErrors(t *testing.T) {
	v := NewInMemoryVFS()
	if _, err := v.List("/none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("List(/none) err = %v", err)
	}
	_ = v.Write("/f", []byte("x"))
	if _, err := v.List("/f"); !errors.Is(err, ErrNotDir) {
		t.Errorf("List(/f) err = %v", err)
	}
}

// TestInMemoryListSorting confirms directories sort before files + each
// group sorts alphabetically.
func TestInMemoryListSorting(t *testing.T) {
	v := NewInMemoryVFS()
	_ = v.Mkdir("/bdir")
	_ = v.Mkdir("/adir")
	_ = v.Write("/bfile", []byte("x"))
	_ = v.Write("/afile", []byte("x"))
	es, _ := v.List("/")
	want := []string{"adir", "bdir", "afile", "bfile"}
	if len(es) != 4 {
		t.Fatalf("list len = %d, want 4", len(es))
	}
	for i, w := range want {
		if es[i].Name != w {
			t.Errorf("list[%d] = %q, want %q", i, es[i].Name, w)
		}
	}
}

// TestInMemoryRemoveFile covers Remove on a regular file.
func TestInMemoryRemoveFile(t *testing.T) {
	v := NewInMemoryVFS()
	_ = v.Write("/x", []byte("x"))
	if err := v.Remove("/x"); err != nil {
		t.Fatalf("Remove err = %v", err)
	}
	if _, err := v.Stat("/x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Stat after remove err = %v, want ErrNotFound", err)
	}
}

// TestInMemoryRemoveDir covers recursive directory removal.
func TestInMemoryRemoveDir(t *testing.T) {
	v := NewInMemoryVFS()
	_ = v.Mkdir("/d")
	_ = v.Mkdir("/d/sub")
	_ = v.Write("/d/sub/f", []byte("x"))
	_ = v.Write("/d/g", []byte("x"))
	if err := v.Remove("/d"); err != nil {
		t.Fatalf("Remove err = %v", err)
	}
	if v.IsDir("/d") {
		t.Errorf("IsDir(/d) after remove = true")
	}
	if _, err := v.Stat("/d/sub/f"); !errors.Is(err, ErrNotFound) {
		t.Errorf("descendant survived: err = %v", err)
	}
}

// TestInMemoryRemoveMissing covers ErrNotFound on Remove.
func TestInMemoryRemoveMissing(t *testing.T) {
	v := NewInMemoryVFS()
	if err := v.Remove("/none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Remove /none err = %v, want ErrNotFound", err)
	}
}

// TestInMemoryRemoveRoot refuses to remove the root.
func TestInMemoryRemoveRoot(t *testing.T) {
	v := NewInMemoryVFS()
	if err := v.Remove("/"); !errors.Is(err, ErrInvalid) {
		t.Errorf("Remove / err = %v, want ErrInvalid", err)
	}
}

// TestInMemorySatisfiesVFS compile-time-asserts the interface match.
func TestInMemorySatisfiesVFS(t *testing.T) {
	var _ VFS = (*InMemoryVFS)(nil)
}
