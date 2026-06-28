// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package sharedvfs is the virtual filesystem shared by the wasmbox terminal
// and files clients. It exposes a small POSIX-shaped interface (List / Stat /
// IsDir / Read / Write / Mkdir / Remove) plus path helpers (Clean / Join /
// Parent / Basename) plus a canonical "demo" seed (Documents/Pictures/
// Downloads/about.txt) so a freshly-booted wasmbox always shows a usable tree.
//
// Two concrete implementations ship in this package:
//
//   - InMemoryVFS (this file): pure Go, no I/O, used by tests + non-browser
//     callers. Thread-safety is intentionally NOT provided -- the wasmbox
//     clients run on a single goroutine each, and adding a mutex would
//     complicate the test surface without buying us anything.
//
//   - IDBVFS (idb.go, build tag js && wasm): identical interface, persists to
//     the browser's IndexedDB so the tree survives page reloads. The IDB
//     impl wraps an InMemoryVFS as its "hot" state and writes through to
//     the database in the background; reads are served from memory so
//     callers stay synchronous (the wasmbox SDK render loop is sync).
//
// Path semantics are POSIX-ish: paths are forward-slash separated, "/" is
// the root, "." and ".." are honoured inside Clean(). Listing a directory
// returns its immediate children sorted with directories first.
package sharedvfs

import (
	"errors"
	"sort"
	"strings"
)

// DemoModTime is the fixed "modified" timestamp the demo seed stamps on
// every entry. The wasm sandbox has no useful clock, so we ship a stable
// string the renderer can show in the Date Modified column.
const DemoModTime = "Jun 25 2026"

// Entry describes one node in the VFS tree: a name (basename), whether it
// is a directory, an optional Size (for files), and a pre-formatted ModTime
// string. Children are NOT carried here -- VFS implementations resolve
// children via List() so the interface stays the same whether the backing
// store is in-memory, IndexedDB, or anything else.
type Entry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime string
}

// VFS is the read+write interface every wasmbox client speaks. It is
// intentionally small: a real shell only needs a handful of primitives to
// give the user a workable experience.
type VFS interface {
	// List returns the immediate children of dir sorted directories-first.
	// Returns ErrNotFound for a missing path, ErrNotDir for a file path.
	List(dir string) ([]Entry, error)

	// Stat returns the Entry for p. Returns ErrNotFound if the path does
	// not exist. Stat("/") always succeeds with IsDir=true.
	Stat(p string) (Entry, error)

	// IsDir reports whether p resolves to a directory. Non-existent paths
	// return false (no error chain -- callers want a boolean).
	IsDir(p string) bool

	// Read returns the byte contents of a regular file. Returns ErrNotFound
	// for a missing path, ErrIsDir for a directory.
	Read(p string) ([]byte, error)

	// Write replaces the contents of p with data, creating the file if it
	// does not exist. Parent directories must already exist (Write does
	// NOT create them -- mkdir -p is the caller's job). Writing to an
	// existing directory returns ErrIsDir.
	Write(p string, data []byte) error

	// Mkdir creates a directory at p. Parent must exist. Returns ErrExists
	// if a node (file or dir) already lives at p.
	Mkdir(p string) error

	// Remove deletes p. For directories the removal is recursive (rm -r),
	// matching what the files browser's "Delete" menu item promises. The
	// root "/" cannot be removed (ErrInvalid).
	Remove(p string) error
}

// Sentinel errors returned by the VFS contract. Kept as package-level vars
// (not fmt.Errorf wraps) so callers can compare with errors.Is without
// parsing message strings.
var (
	ErrNotFound = errors.New("sharedvfs: not found")
	ErrNotDir   = errors.New("sharedvfs: not a directory")
	ErrIsDir    = errors.New("sharedvfs: is a directory")
	ErrExists   = errors.New("sharedvfs: already exists")
	ErrInvalid  = errors.New("sharedvfs: invalid argument")
)

// Clean normalises a POSIX path: collapses repeated slashes, resolves "."
// and ".." components, and always returns either "/" or a path with a
// leading "/" and no trailing slash. Self-contained (no path package dep)
// so this builds for js/wasm without pulling extra symbols.
func Clean(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			// skip
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return "/"
	}
	return "/" + strings.Join(out, "/")
}

// Join concatenates a directory path with a child name, producing a
// Clean()ed absolute path. Used by the browser when activating a row and
// by the shell when resolving a bare filename against cwd.
func Join(dir, name string) string {
	if dir == "" {
		dir = "/"
	}
	if dir == "/" {
		return Clean("/" + name)
	}
	return Clean(dir + "/" + name)
}

// Parent returns the parent directory of p. The parent of "/" is "/" (the
// root is its own parent; this keeps GoUp idempotent without extra logic).
func Parent(p string) string {
	c := Clean(p)
	if c == "/" {
		return "/"
	}
	i := strings.LastIndex(c, "/")
	if i <= 0 {
		return "/"
	}
	return c[:i]
}

// Basename returns the trailing path component of p ("/" -> "/").
func Basename(p string) string {
	c := Clean(p)
	if c == "/" {
		return "/"
	}
	i := strings.LastIndex(c, "/")
	return c[i+1:]
}

// Resolve makes a path absolute against cwd. A leading "/" is honoured
// as-is; a leading "~" expands to "/" (the shell's home); everything else
// is joined onto cwd. The result is Cleaned. Used by the shell so commands
// like `cat hello.txt` and `cd ..` work without the caller pre-joining.
func Resolve(cwd, p string) string {
	if p == "" {
		return Clean(cwd)
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		// ~ is the root in the demo VFS (there is no /home/user tree).
		return Clean("/" + strings.TrimPrefix(p, "~"))
	}
	if strings.HasPrefix(p, "/") {
		return Clean(p)
	}
	return Join(cwd, p)
}

// sortEntries sorts a slice of Entry directories-first, then alphabetically
// by name. Pulled out so both implementations produce identical orderings.
func sortEntries(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].IsDir != es[j].IsDir {
			return es[i].IsDir
		}
		return es[i].Name < es[j].Name
	})
}
