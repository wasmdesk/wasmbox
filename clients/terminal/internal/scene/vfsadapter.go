// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// vfsadapter.go bridges the wasmbox clients/sharedvfs.VFS interface into
// the wasmdesk/coreutils fsx.FS contract so the terminal's shell can route
// every builtin through coreutils.Dispatch. The adapter is intentionally
// thin: each method is a one-line delegation to the underlying VFS, with
// the small bits of POSIX surface coreutils expects (MkdirAll, RemoveAll,
// distinct Remove-vs-empty semantics) emulated on top of sharedvfs's
// recursive Remove primitive.

package scene

import (
	"strings"

	"github.com/wasmdesk/coreutils/pkg/fsx"
	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

// vfsAdapter wraps a sharedvfs.VFS as an fsx.FS. Constructed on every
// Execute() call so the underlying VFS handle the terminal holds remains
// the single source of truth (cheap -- the struct holds only a pointer).
type vfsAdapter struct {
	v sharedvfs.VFS
}

// newVFSAdapter wraps v into the fsx.FS contract.
func newVFSAdapter(v sharedvfs.VFS) *vfsAdapter {
	return &vfsAdapter{v: v}
}

// Stat translates ErrNotFound into fsx.ErrNotFound, etc.
func (a *vfsAdapter) Stat(p string) (fsx.FileInfo, error) {
	e, err := a.v.Stat(p)
	if err != nil {
		return fsx.FileInfo{}, mapErr(err)
	}
	return fsx.FileInfo{Name: e.Name, Size: e.Size, IsDir: e.IsDir}, nil
}

// ReadFile delegates verbatim; mapping the small error vocabulary.
func (a *vfsAdapter) ReadFile(p string) ([]byte, error) {
	b, err := a.v.Read(p)
	return b, mapErr(err)
}

// WriteFile delegates to sharedvfs.Write (which has the same "parent must
// exist" contract as fsx).
func (a *vfsAdapter) WriteFile(p string, data []byte) error {
	return mapErr(a.v.Write(p, data))
}

// Mkdir creates a single directory.
func (a *vfsAdapter) Mkdir(p string) error {
	return mapErr(a.v.Mkdir(p))
}

// MkdirAll creates p plus any missing parents. sharedvfs has no native
// mkdir -p, so we walk the path and create each missing segment.
func (a *vfsAdapter) MkdirAll(p string) error {
	c := sharedvfs.Clean(p)
	if c == "/" {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(c, "/"), "/")
	cur := ""
	for _, seg := range parts {
		cur += "/" + seg
		if a.v.IsDir(cur) {
			continue
		}
		if _, err := a.v.Stat(cur); err == nil {
			// Path exists but is not a directory; matches fsx's ErrExists.
			return fsx.ErrExists
		}
		if err := a.v.Mkdir(cur); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// ReadDir delegates + translates the Entry slice.
func (a *vfsAdapter) ReadDir(dir string) ([]fsx.FileInfo, error) {
	es, err := a.v.List(dir)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]fsx.FileInfo, len(es))
	for i, e := range es {
		out[i] = fsx.FileInfo{Name: e.Name, Size: e.Size, IsDir: e.IsDir}
	}
	return out, nil
}

// Remove deletes a single file or an EMPTY directory. sharedvfs.Remove is
// recursive, so we guard with a List() check on dirs to enforce the
// "non-empty -> ErrNotEmpty" contract coreutils expects.
func (a *vfsAdapter) Remove(p string) error {
	e, err := a.v.Stat(p)
	if err != nil {
		return mapErr(err)
	}
	if e.IsDir {
		children, lerr := a.v.List(p)
		if lerr != nil {
			return mapErr(lerr)
		}
		if len(children) > 0 {
			return fsx.ErrNotEmpty
		}
	}
	return mapErr(a.v.Remove(p))
}

// RemoveAll delegates to sharedvfs.Remove (already recursive). Removing a
// missing path is a no-op per the fsx contract.
func (a *vfsAdapter) RemoveAll(p string) error {
	if _, err := a.v.Stat(p); err != nil {
		return nil
	}
	return mapErr(a.v.Remove(p))
}

// mapErr translates a sharedvfs sentinel into its fsx peer so coreutils
// tools' errors.Is checks fire on the expected sentinel.
func mapErr(err error) error {
	switch err {
	case nil:
		return nil
	case sharedvfs.ErrNotFound:
		return fsx.ErrNotFound
	case sharedvfs.ErrNotDir:
		return fsx.ErrNotDir
	case sharedvfs.ErrIsDir:
		return fsx.ErrIsDir
	case sharedvfs.ErrExists:
		return fsx.ErrExists
	case sharedvfs.ErrInvalid:
		return fsx.ErrInvalid
	}
	return err
}
