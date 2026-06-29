// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Command code is the wasmbox external client spawning a VS Code-styled
// editor. Layout (900x540): a 200 px sidebar on the left, a 28 px tab
// strip across the top, a 50 px line-number gutter, a syntax-highlighted
// editor pane, and a 24 px signature-blue status bar at the bottom.
//
// Input model:
//
//   - Click sidebar entry          -> OpenFile via sharedvfs.Read.
//   - Click editor pane            -> cursor jumps to the nearest (line, col).
//   - Arrow keys                   -> cursor moves with clamping.
//   - Printable single-byte keys   -> Insert at cursor.
//   - Backspace                    -> Delete char before cursor.
//   - Enter                        -> Split line at cursor.
//   - Tab                          -> insert 4 spaces.
//   - Cmd+S / Ctrl+S               -> sharedvfs.Write + green flash.
//   - Click "Live Server: ..."     -> open the Connect popup; the popup's
//     Connect button flashes "info" + closes (v0 has no protocol yet).
//
// Runs inside a dedicated Web Worker (worker.js). The Worker imports
// sdk.js, constructs wasmboxClient, awaits start(), so by the time main()
// runs we are connected.
//
//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/wasmdesk/wasmbox/clients/code/internal/scene"
	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

func main() {
	client := js.Global().Get("wasmboxClient")
	if client.IsUndefined() {
		println("code: wasmboxClient missing; SDK not loaded?")
		return
	}

	w := client.Get("w").Int()
	h := client.Get("h").Int()
	pixels := client.Get("pixels")
	bufLen := pixels.Get("length").Int()
	if bufLen != 4*w*h {
		println("code: pixel buffer size mismatch")
		return
	}

	// Open the persistent IndexedDB-backed VFS (shared with files /
	// terminal). Falls back to an in-memory VFS when IDB is unavailable.
	vfs := openVFS()
	sharedvfs.SeedDemoTree(vfs)

	local := make([]byte, 4*w*h)
	state := scene.NewWithVFS(w, h, vfs)

	render := func() {
		scene.Render(state, local)
		js.CopyBytesToJS(pixels, local)
		damage := js.Global().Call("Object")
		damage.Set("x", 0)
		damage.Set("y", 0)
		damage.Set("w", w)
		damage.Set("h", h)
		client.Call("commit", damage)
	}

	render()

	cb := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		kind := ev.Get("kind").String()
		switch kind {
		case "keydown":
			key := ev.Get("key").String()
			// Synthesise Cmd+S / Ctrl+S so the scene-state dispatcher gets a
			// pre-folded key instead of having to read modifier fields it
			// doesn't know about.
			if key == "s" || key == "S" {
				if m := ev.Get("metaKey"); !m.IsUndefined() && m.Bool() {
					key = "Cmd+S"
				} else if c := ev.Get("ctrlKey"); !c.IsUndefined() && c.Bool() {
					key = "Ctrl+S"
				}
			}
			if state.HandleKey(key) {
				render()
			}
		case "mousedown":
			x := ev.Get("x").Int()
			y := ev.Get("y").Int()
			if state.HandleMouse(x, y) {
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
		println("code: IndexedDB unavailable; falling back to in-memory VFS")
		return sharedvfs.NewInMemoryVFS()
	}
	idb := sharedvfs.NewIDBVFS(db, "vfs")
	idb.LoadAll()
	return idb
}
