// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package sharedvfs

// SeedDemoTree populates vfs with the canonical wasmbox demo layout:
//
//	/
//	|-- Documents/
//	|   |-- readme.txt   (small intro)
//	|   `-- notes.md     (small intro)
//	|-- Pictures/         (empty -- the demo carries no real image bytes)
//	|-- Downloads/        (empty)
//	`-- about.txt         (small intro)
//
// Idempotent: if a node already exists (because a previous boot wrote a
// persisted tree into the VFS) SeedDemoTree leaves it alone. This is what
// makes the seed safe to call on every page load against an IndexedDB-backed
// VFS without clobbering user edits.
func SeedDemoTree(vfs VFS) {
	mkdir := func(p string) {
		if vfs.IsDir(p) {
			return
		}
		_ = vfs.Mkdir(p)
	}
	write := func(p string, body string) {
		if _, err := vfs.Stat(p); err == nil {
			return
		}
		_ = vfs.Write(p, []byte(body))
	}
	mkdir("/Documents")
	mkdir("/Pictures")
	mkdir("/Downloads")
	write("/Documents/readme.txt", "Welcome to the wasmbox files demo.\nEdit me from the terminal.\n")
	write("/Documents/notes.md", "# wasmbox notes\n\n- shared VFS\n- IndexedDB persistence\n")
	write("/about.txt", "wasmbox: a WebAssembly desktop in pure Go Ruby.\n")
}
