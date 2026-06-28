// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package sharedvfs

import "strings"

// InMemoryVFS is the trivial pure-Go VFS implementation: a flat map keyed by
// the Cleaned absolute path. Each entry carries a node value with kind +
// optional file bytes. Children of "/dir" are discovered by scanning the map
// for paths whose Parent equals "/dir" -- O(N) but N stays tiny for the
// wasmbox demo tree (a couple of dozen entries at most).
//
// The in-memory variant is the source of truth for tests and for the
// IndexedDB-backed impl's hot state. It is NOT thread-safe -- the wasmbox
// clients each run on a single goroutine so a mutex would be dead weight.
type InMemoryVFS struct {
	// nodes is keyed by the Cleaned absolute path. The root ("/") is always
	// present after NewInMemoryVFS; everything else is added by Mkdir/Write
	// (or the demo seed).
	nodes map[string]*inmemNode
}

// inmemNode is the per-path payload. Kept small (kind + size + data + mod
// time) -- name is encoded in the key, parent is derivable from the key, so
// neither needs to be duplicated here.
type inmemNode struct {
	isDir   bool
	data    []byte // nil for directories
	modTime string
}

// NewInMemoryVFS returns an empty VFS whose only node is the root directory.
// Callers that want the canonical demo tree should pass the returned VFS to
// SeedDemoTree (see seed.go).
func NewInMemoryVFS() *InMemoryVFS {
	v := &InMemoryVFS{nodes: make(map[string]*inmemNode)}
	v.nodes["/"] = &inmemNode{isDir: true, modTime: DemoModTime}
	return v
}

// List returns the immediate children of dir sorted directories-first.
func (v *InMemoryVFS) List(dir string) ([]Entry, error) {
	d := Clean(dir)
	n, ok := v.nodes[d]
	if !ok {
		return nil, ErrNotFound
	}
	if !n.isDir {
		return nil, ErrNotDir
	}
	var out []Entry
	prefix := d
	if prefix != "/" {
		prefix += "/"
	}
	for path, node := range v.nodes {
		if path == d {
			continue
		}
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		rest := path[len(prefix):]
		if strings.Contains(rest, "/") {
			continue // grandchild, not immediate
		}
		out = append(out, Entry{
			Name:    rest,
			IsDir:   node.isDir,
			Size:    int64(len(node.data)),
			ModTime: node.modTime,
		})
	}
	// Guarantee a non-nil slice for an empty directory so callers can iterate
	// without nil-checking.
	if out == nil {
		out = []Entry{}
	}
	sortEntries(out)
	return out, nil
}

// Stat returns the Entry for p.
func (v *InMemoryVFS) Stat(p string) (Entry, error) {
	c := Clean(p)
	n, ok := v.nodes[c]
	if !ok {
		return Entry{}, ErrNotFound
	}
	return Entry{
		Name:    Basename(c),
		IsDir:   n.isDir,
		Size:    int64(len(n.data)),
		ModTime: n.modTime,
	}, nil
}

// IsDir reports whether p resolves to a directory.
func (v *InMemoryVFS) IsDir(p string) bool {
	n, ok := v.nodes[Clean(p)]
	if !ok {
		return false
	}
	return n.isDir
}

// Read returns the byte contents of a regular file.
func (v *InMemoryVFS) Read(p string) ([]byte, error) {
	c := Clean(p)
	n, ok := v.nodes[c]
	if !ok {
		return nil, ErrNotFound
	}
	if n.isDir {
		return nil, ErrIsDir
	}
	// Return a copy so a mutating caller cannot scribble over our store.
	out := make([]byte, len(n.data))
	copy(out, n.data)
	return out, nil
}

// Write replaces the contents of p with data. Parent must exist.
func (v *InMemoryVFS) Write(p string, data []byte) error {
	c := Clean(p)
	if c == "/" {
		return ErrInvalid
	}
	parent := Parent(c)
	pn, ok := v.nodes[parent]
	if !ok || !pn.isDir {
		return ErrNotFound
	}
	if existing, ok := v.nodes[c]; ok && existing.isDir {
		return ErrIsDir
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	v.nodes[c] = &inmemNode{isDir: false, data: cp, modTime: DemoModTime}
	return nil
}

// Mkdir creates a directory at p.
func (v *InMemoryVFS) Mkdir(p string) error {
	c := Clean(p)
	if c == "/" {
		return ErrExists
	}
	if _, exists := v.nodes[c]; exists {
		return ErrExists
	}
	parent := Parent(c)
	pn, ok := v.nodes[parent]
	if !ok || !pn.isDir {
		return ErrNotFound
	}
	v.nodes[c] = &inmemNode{isDir: true, modTime: DemoModTime}
	return nil
}

// Remove deletes p (recursively for directories).
func (v *InMemoryVFS) Remove(p string) error {
	c := Clean(p)
	if c == "/" {
		return ErrInvalid
	}
	if _, ok := v.nodes[c]; !ok {
		return ErrNotFound
	}
	// Delete the node + any descendants. The descendant sweep handles the
	// recursive-delete case for directories; for a file the sweep simply
	// finds no descendants.
	prefix := c + "/"
	delete(v.nodes, c)
	for k := range v.nodes {
		if strings.HasPrefix(k, prefix) {
			delete(v.nodes, k)
		}
	}
	return nil
}
