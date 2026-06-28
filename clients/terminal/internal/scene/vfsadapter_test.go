// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import (
	"errors"
	"testing"

	corefsx "github.com/wasmdesk/coreutils/pkg/fsx"
	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

// newAdapter returns an empty MemFS-backed adapter for tests.
func newAdapter() *vfsAdapter {
	return newVFSAdapter(sharedvfs.NewInMemoryVFS())
}

// Round-trip on the read/write surface.
func TestVFSAdapterRoundtrip(t *testing.T) {
	a := newAdapter()
	if info, err := a.Stat("/"); err != nil || !info.IsDir {
		t.Fatalf("Stat / = %+v %v", info, err)
	}
	if _, err := a.Stat("/nope"); !errors.Is(err, corefsx.ErrNotFound) {
		t.Errorf("Stat /nope = %v", err)
	}
	if _, err := a.ReadFile("/nope"); !errors.Is(err, corefsx.ErrNotFound) {
		t.Errorf("ReadFile /nope = %v", err)
	}
	if err := a.Mkdir("/d"); err != nil {
		t.Fatalf("Mkdir = %v", err)
	}
	if err := a.WriteFile("/d/f", []byte("hi")); err != nil {
		t.Fatalf("WriteFile = %v", err)
	}
	if b, err := a.ReadFile("/d/f"); err != nil || string(b) != "hi" {
		t.Errorf("ReadFile = %q %v", b, err)
	}
	if es, err := a.ReadDir("/d"); err != nil || len(es) != 1 || es[0].Name != "f" {
		t.Errorf("ReadDir = %v %v", es, err)
	}
	if _, err := a.ReadDir("/nope"); !errors.Is(err, corefsx.ErrNotFound) {
		t.Errorf("ReadDir /nope = %v", err)
	}
}

func TestVFSAdapterMkdirAll(t *testing.T) {
	a := newAdapter()
	// New chain.
	if err := a.MkdirAll("/a/b/c"); err != nil {
		t.Fatalf("MkdirAll = %v", err)
	}
	if info, _ := a.Stat("/a/b/c"); !info.IsDir {
		t.Errorf("/a/b/c not dir")
	}
	// Root is a no-op.
	if err := a.MkdirAll("/"); err != nil {
		t.Errorf("MkdirAll / = %v", err)
	}
	// Idempotent on existing chain.
	if err := a.MkdirAll("/a/b/c"); err != nil {
		t.Errorf("MkdirAll existing = %v", err)
	}
	// Path that already exists as a file -> ErrExists.
	_ = a.WriteFile("/file", nil)
	if err := a.MkdirAll("/file/sub"); !errors.Is(err, corefsx.ErrExists) {
		t.Errorf("MkdirAll through file err = %v", err)
	}
}

func TestVFSAdapterMkdirAllInnerFails(t *testing.T) {
	// Drive the inner-Mkdir-error path with a sharedvfs that fails Mkdir.
	v := &failingVFS{VFS: sharedvfs.NewInMemoryVFS(), failMkdir: true}
	a := newVFSAdapter(v)
	if err := a.MkdirAll("/x"); err == nil {
		t.Errorf("expected error")
	}
}

func TestVFSAdapterRemoveEmpty(t *testing.T) {
	a := newAdapter()
	_ = a.Mkdir("/d")
	if err := a.Remove("/d"); err != nil {
		t.Errorf("Remove empty dir = %v", err)
	}
}

func TestVFSAdapterRemoveNonEmpty(t *testing.T) {
	a := newAdapter()
	_ = a.Mkdir("/d")
	_ = a.WriteFile("/d/x", nil)
	if err := a.Remove("/d"); !errors.Is(err, corefsx.ErrNotEmpty) {
		t.Errorf("Remove non-empty = %v, want ErrNotEmpty", err)
	}
}

func TestVFSAdapterRemoveMissing(t *testing.T) {
	a := newAdapter()
	if err := a.Remove("/nope"); !errors.Is(err, corefsx.ErrNotFound) {
		t.Errorf("Remove missing = %v", err)
	}
}

func TestVFSAdapterRemoveFile(t *testing.T) {
	a := newAdapter()
	_ = a.WriteFile("/f", nil)
	if err := a.Remove("/f"); err != nil {
		t.Errorf("Remove file = %v", err)
	}
}

func TestVFSAdapterRemoveAll(t *testing.T) {
	a := newAdapter()
	if err := a.RemoveAll("/never"); err != nil {
		t.Errorf("RemoveAll missing = %v", err)
	}
	_ = a.Mkdir("/d")
	_ = a.WriteFile("/d/x", nil)
	if err := a.RemoveAll("/d"); err != nil {
		t.Errorf("RemoveAll = %v", err)
	}
	if _, err := a.Stat("/d"); !errors.Is(err, corefsx.ErrNotFound) {
		t.Errorf("/d still present: %v", err)
	}
}

func TestVFSAdapterMapErr(t *testing.T) {
	cases := []struct {
		in   error
		want error
	}{
		{nil, nil},
		{sharedvfs.ErrNotFound, corefsx.ErrNotFound},
		{sharedvfs.ErrNotDir, corefsx.ErrNotDir},
		{sharedvfs.ErrIsDir, corefsx.ErrIsDir},
		{sharedvfs.ErrExists, corefsx.ErrExists},
		{sharedvfs.ErrInvalid, corefsx.ErrInvalid},
	}
	for _, c := range cases {
		if got := mapErr(c.in); got != c.want {
			t.Errorf("mapErr(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	// Unknown -> pass-through.
	custom := errors.New("custom")
	if got := mapErr(custom); got != custom {
		t.Errorf("mapErr(custom) = %v, want passthrough", got)
	}
}

// Drive the List-failure branch inside Remove (Stat says dir, List fails).
func TestVFSAdapterRemoveListFails(t *testing.T) {
	v := &failingVFS{VFS: sharedvfs.NewInMemoryVFS(), failList: true}
	_ = v.VFS.Mkdir("/d")
	a := newVFSAdapter(v)
	if err := a.Remove("/d"); err == nil {
		t.Errorf("expected error")
	}
}

// failingVFS wraps an inner VFS and lets tests force specific operations
// to return ErrInvalid. Used to drive the adapter's failure branches.
type failingVFS struct {
	sharedvfs.VFS
	failMkdir, failList bool
}

func (f *failingVFS) Mkdir(p string) error {
	if f.failMkdir {
		return sharedvfs.ErrInvalid
	}
	return f.VFS.Mkdir(p)
}
func (f *failingVFS) List(p string) ([]sharedvfs.Entry, error) {
	if f.failList {
		return nil, sharedvfs.ErrInvalid
	}
	return f.VFS.List(p)
}
