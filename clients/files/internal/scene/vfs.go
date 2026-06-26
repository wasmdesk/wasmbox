// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's vfs.go is the in-memory virtual filesystem the files browser
// navigates. The wasm sandbox has no real filesystem; we ship a small demo
// tree so the user can exercise the navigate-into-folder / go-up flow without
// any host dependency.
//
// Path semantics are POSIX-ish: paths are forward-slash separated, "/" is the
// root, "." and ".." are honoured inside Clean(). Listing a directory returns
// its immediate Children sorted so directories sort before files (and each
// group alphabetically), matching what most file managers default to.

package scene

import (
	"errors"
	"sort"
	"strings"
)

// DemoModTime is the fixed "modified" timestamp the demo VFS stamps on every
// entry. The wasm sandbox has no useful clock, so we ship a stable string the
// renderer can show in the Date Modified column. Mirroring real Finder: short
// month, day, year (no time-of-day so the columns stay narrow).
const DemoModTime = "Jun 25 2026"

// Entry describes one node in the VFS tree: a name (basename), whether it is
// a directory, an optional Size (for files), and Children (for directories).
// ModTime is a pre-formatted human string ("Jun 25 2026") because the wasm
// clock is opaque -- we stamp every demo entry with DemoModTime.
// Pure data -- no I/O -- so the tree can be constructed in tests and walked
// safely from the renderer.
type Entry struct {
	Name     string
	IsDir    bool
	Size     int64
	ModTime  string
	Children []Entry
}

// VFS is the read-only interface the browser uses against any virtual
// filesystem. We keep List/Stat/IsDir as the minimal surface; v0 has no
// editing.
type VFS interface {
	List(path string) ([]Entry, error)
	Stat(path string) (Entry, error)
	IsDir(path string) bool
}

// ErrNotFound is returned by List/Stat when the given path does not exist.
// Kept as a sentinel (not a fmt.Errorf wrapping the path) so callers can
// compare with errors.Is without parsing strings.
var ErrNotFound = errors.New("vfs: not found")

// ErrNotDir is returned by List when the path resolves to a file (not a
// directory). Stat on a file is fine; List requires a directory.
var ErrNotDir = errors.New("vfs: not a directory")

// InMemoryVFS is the demo VFS: one root Entry whose Children describe the
// whole tree. We store the root by value so the demo data is immutable from
// the consumer's perspective.
type InMemoryVFS struct {
	root Entry
}

// NewDemoVFS constructs the canonical demo tree the prompt requires:
//
//	/
//	|-- Documents/
//	|   |-- readme.txt    (1234)
//	|   `-- notes.md      (567)
//	|-- Pictures/
//	|   |-- cat.png       (89012)
//	|   `-- dog.jpg       (45678)
//	|-- Downloads/
//	`-- about.txt          (234)
//
// Every entry carries DemoModTime so the Date Modified column has stable
// content for the Finder-style list.
func NewDemoVFS() *InMemoryVFS {
	return &InMemoryVFS{
		root: Entry{
			Name:    "",
			IsDir:   true,
			ModTime: DemoModTime,
			Children: []Entry{
				{Name: "Documents", IsDir: true, ModTime: DemoModTime, Children: []Entry{
					{Name: "readme.txt", Size: 1234, ModTime: DemoModTime},
					{Name: "notes.md", Size: 567, ModTime: DemoModTime},
				}},
				{Name: "Pictures", IsDir: true, ModTime: DemoModTime, Children: []Entry{
					{Name: "cat.png", Size: 89012, ModTime: DemoModTime},
					{Name: "dog.jpg", Size: 45678, ModTime: DemoModTime},
				}},
				{Name: "Downloads", IsDir: true, ModTime: DemoModTime, Children: []Entry{}},
				{Name: "about.txt", Size: 234, ModTime: DemoModTime},
			},
		},
	}
}

// Clean normalises a POSIX path: collapses repeated slashes, resolves "."
// and ".." components, and always returns either "/" or a path with a
// leading "/" and no trailing slash (except for the root itself). Mirrors
// path.Clean's semantics but keeps the dependency surface zero so the
// package is self-contained.
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

// Join concatenates a directory path with a child name, producing a Clean()ed
// absolute path. Used by the browser when activating a row.
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
// root is its own parent; this keeps the "go up" handler idempotent without
// extra logic in the state machine).
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

// Basename returns the trailing path component of p ("/" -> "/"). Used by
// the toolbar's breadcrumb so a path like "/Documents" displays as the more
// Finder-like "Documents".
func Basename(p string) string {
	c := Clean(p)
	if c == "/" {
		return "/"
	}
	i := strings.LastIndex(c, "/")
	return c[i+1:]
}

// resolve walks the in-memory tree to the node identified by p. Returns the
// entry by value (so callers cannot mutate the tree) and a found flag.
func (v *InMemoryVFS) resolve(p string) (Entry, bool) {
	c := Clean(p)
	if c == "/" {
		return v.root, true
	}
	parts := strings.Split(strings.TrimPrefix(c, "/"), "/")
	cur := v.root
	for _, part := range parts {
		if !cur.IsDir {
			return Entry{}, false
		}
		var next *Entry
		for i := range cur.Children {
			if cur.Children[i].Name == part {
				next = &cur.Children[i]
				break
			}
		}
		if next == nil {
			return Entry{}, false
		}
		cur = *next
	}
	return cur, true
}

// List returns the (sorted) immediate children of dir. Directories sort
// before files; within each group entries sort alphabetically (case-sensitive).
func (v *InMemoryVFS) List(dir string) ([]Entry, error) {
	e, ok := v.resolve(dir)
	if !ok {
		return nil, ErrNotFound
	}
	if !e.IsDir {
		return nil, ErrNotDir
	}
	out := make([]Entry, len(e.Children))
	copy(out, e.Children)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Stat returns the Entry for p. Unlike a real filesystem, the root has a
// blank Name; we leave it as-is so the caller can distinguish "/" by path,
// not by basename.
func (v *InMemoryVFS) Stat(p string) (Entry, error) {
	e, ok := v.resolve(p)
	if !ok {
		return Entry{}, ErrNotFound
	}
	return e, nil
}

// IsDir reports whether p resolves to a directory. Non-existent paths return
// false (we deliberately do not surface an error here -- the renderer wants a
// boolean, not an error chain).
func (v *InMemoryVFS) IsDir(p string) bool {
	e, ok := v.resolve(p)
	if !ok {
		return false
	}
	return e.IsDir
}
