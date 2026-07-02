// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Command files is the wasmbox external client spawned when the dock's
// "files" icon is clicked. It hosts a real file browser inspired by GNOME
// Nautilus, navigated by Up/Down (move cursor), Enter (descend into a
// folder), Backspace/Escape (go up), and the mouse:
//   - left click on a folder: descend.
//   - left click on a file: select.
//   - double-click on a .txt / .md file: show a preview overlay.
//   - right click on a row: context menu (Open / Rename / Delete).
//   - right click on empty area: context menu (New Folder / New File).
//
// The filesystem is persistent: edits write through to an IndexedDB store
// (database "wasmbox.vfs", object store "vfs") via clients/sharedvfs.IDBVFS,
// so reloading the page restores the tree. The same DB name is used by the
// terminal client so writes from one show up in the other within a session
// (after the IDB roundtrip).
//
// Runs inside a dedicated Web Worker (see worker.js). The Worker has
// already imported sdk.js (which exposes globalThis.WasmboxClient) and
// constructed `wasmboxClient`, then awaited `start()` -- so by the time
// main() runs we are connected.
//
//go:build js && wasm

package main

import (
	"syscall/js"
	"time"

	"github.com/wasmdesk/wasmbox/clients/files/internal/scene"
	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

func main() {
	client := js.Global().Get("wasmboxClient")
	if client.IsUndefined() {
		println("files: wasmboxClient missing; SDK not loaded?")
		return
	}

	w := client.Get("w").Int()
	h := client.Get("h").Int()
	pixels := client.Get("pixels")
	bufLen := pixels.Get("length").Int()
	if bufLen != 4*w*h {
		println("files: pixel buffer size mismatch")
		return
	}

	// Open the persistent IndexedDB-backed VFS. Falls back to a fresh
	// InMemoryVFS when `indexedDB` is unavailable (non-browser host or
	// disabled by the page). The fallback path keeps the demo functional
	// even when persistence is denied.
	vfs := openVFS()
	sharedvfs.SeedDemoTree(vfs)

	local := make([]byte, 4*w*h)
	state := scene.NewWithVFS(w, h, vfs)

	render := func() {
		scene.Render(state, local)
		client.Call("beginFrame") // open the seqlock window before the bulk copy (tear-free)
		js.CopyBytesToJS(pixels, local)
		damage := js.Global().Call("Object")
		damage.Set("x", 0)
		damage.Set("y", 0)
		damage.Set("w", w)
		damage.Set("h", h)
		client.Call("commit", damage)
	}

	render()

	// Double-click detection is local: the compositor only emits single
	// mousedown events, so we look at the elapsed time + the previous
	// pointer position to decide when to call HandleMouseButton with
	// clickCount=2.
	var lastClickAt time.Time
	var lastX, lastY int
	const doubleClickWindow = 350 * time.Millisecond

	cb := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		kind := ev.Get("kind").String()
		switch kind {
		case "keydown":
			key := ev.Get("key").String()
			if state.HandleKey(key) {
				render()
			}
		case "mousedown":
			x := ev.Get("x").Int()
			y := ev.Get("y").Int()
			btn := 0
			if b := ev.Get("button"); !b.IsUndefined() {
				btn = b.Int()
			}
			clicks := 1
			now := time.Now()
			if !lastClickAt.IsZero() && now.Sub(lastClickAt) < doubleClickWindow &&
				abs(x-lastX) < 6 && abs(y-lastY) < 6 {
				clicks = 2
				// Reset so a third click doesn't keep counting.
				lastClickAt = time.Time{}
			} else {
				lastClickAt = now
				lastX = x
				lastY = y
			}
			if state.HandleMouseButton(x, y, btn, clicks) {
				render()
			}
		}
		return nil
	})
	client.Call("onInput", cb)

	select {}
}

// openVFS opens the IndexedDB-backed VFS, falling back to an in-memory VFS
// when IDB is not available. The returned VFS is always ready to serve --
// IDB.LoadAll() is invoked here so a successful open immediately reflects
// the persisted tree.
func openVFS() sharedvfs.VFS {
	db := sharedvfs.OpenIDB("wasmbox.vfs", "vfs")
	if !db.Truthy() {
		println("files: IndexedDB unavailable; falling back to in-memory VFS")
		return sharedvfs.NewInMemoryVFS()
	}
	idb := sharedvfs.NewIDBVFS(db, "vfs")
	idb.LoadAll()
	return idb
}

// abs returns the absolute value of an int -- used by the double-click
// distance check (math.Abs is float64 only).
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
