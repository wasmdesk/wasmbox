// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's vfs.go is a thin re-export of the shared filesystem layer
// in clients/sharedvfs. The files browser used to ship its own in-memory
// VFS here; the upgrade to a real (IndexedDB-backed) filesystem moved the
// definitions one floor up so the terminal client can speak the same VFS
// without either client depending on the other's internals.
//
// The package-level aliases below preserve the old call sites (scene.Entry,
// scene.VFS, scene.Clean, ...) so render.go + state.go + their tests do not
// have to be rewritten -- the renderer reads Entry.Name + Entry.IsDir like
// before, the state machine calls List/Stat/IsDir like before, and Clean /
// Join / Parent / Basename still resolve via this package.

package scene

import "github.com/wasmdesk/wasmbox/clients/sharedvfs"

// Entry is a re-export of sharedvfs.Entry so the renderer + tests keep
// working with `scene.Entry`.
type Entry = sharedvfs.Entry

// VFS is a re-export of sharedvfs.VFS. The browser state holds one of these
// (any concrete impl) -- in tests we use an InMemoryVFS seeded with the
// demo tree; in the browser we use an IDBVFS so edits persist.
type VFS = sharedvfs.VFS

// DemoModTime mirrors sharedvfs.DemoModTime for callers that still reach
// for the package-level constant by its old name.
const DemoModTime = sharedvfs.DemoModTime

// Sentinel error re-exports. Kept here so existing `errors.Is(err, scene.ErrNotFound)`
// call sites still type-check without an extra import.
var (
	ErrNotFound = sharedvfs.ErrNotFound
	ErrNotDir   = sharedvfs.ErrNotDir
)

// Path-helper re-exports. We keep them as functions rather than `var` aliases
// because the test layer also calls them through `scene.Clean(...)` syntax.
func Clean(p string) string         { return sharedvfs.Clean(p) }
func Join(dir, name string) string  { return sharedvfs.Join(dir, name) }
func Parent(p string) string        { return sharedvfs.Parent(p) }
func Basename(p string) string      { return sharedvfs.Basename(p) }

// NewDemoVFS keeps the legacy constructor name working: builds an InMemoryVFS
// and seeds it with the canonical demo tree. The tests + wasm boot path both
// route through here when they want a fresh non-persistent VFS.
func NewDemoVFS() VFS {
	v := sharedvfs.NewInMemoryVFS()
	sharedvfs.SeedDemoTree(v)
	return v
}
