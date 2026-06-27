// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Command terminal is the wasmbox external client spawned when the dock's
// "terminal" icon is clicked. It hosts a real interactive shell: printable
// ASCII extends the edit line, Backspace shrinks it, Enter executes a small
// builtin (echo / help / clear / date / pwd / ls / cd / cat / mkdir / touch
// / rm / `echo TEXT > PATH`) and the output is painted above a fresh prompt.
//
// The filesystem is the SAME clients/sharedvfs the files browser paints; in
// the browser it is IndexedDB-backed (database "wasmbox.vfs"), so writes
// from the terminal (`mkdir`, `touch`, `echo > path`) are immediately
// visible in the files browser and survive a page reload.
//
// It runs inside a dedicated Web Worker (see worker.js). The Worker has
// already imported sdk.js (which exposes globalThis.WasmboxClient) and
// constructed `wasmboxClient`, then awaited `start()` -- so by the time
// main() runs we are connected.
//
//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
	"github.com/wasmdesk/wasmbox/clients/terminal/internal/scene"
)

// vfsDBName + vfsStoreName must match the values clients/files uses or the
// two clients will write to different stores and never see each other. Both
// route through clients/sharedvfs which keys the IDB store by these names.
const (
	vfsDBName    = "wasmbox.vfs"
	vfsStoreName = "vfs"
)

func main() {
	client := js.Global().Get("wasmboxClient")
	if client.IsUndefined() {
		println("terminal: wasmboxClient missing; SDK not loaded?")
		return
	}

	w := client.Get("w").Int()
	h := client.Get("h").Int()
	pixels := client.Get("pixels")
	bufLen := pixels.Get("length").Int()
	if bufLen != 4*w*h {
		println("terminal: pixel buffer size mismatch")
		return
	}

	// Open the persistent IndexedDB-backed VFS shared with clients/files.
	// Falls back to a fresh InMemoryVFS when `indexedDB` is unavailable
	// (non-browser host or disabled by the page).
	vfs := openVFS()
	sharedvfs.SeedDemoTree(vfs)

	// Local RGBA buffer + the pure-Go scene state. Each frame we re-paint
	// into `local`, then copy into the SAB-backed Uint8ClampedArray in one
	// js.CopyBytesToJS call -- this is the same pattern the hello client
	// uses, and it keeps the renderer free of any js dependency.
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

	// Initial paint so the compositor has something to blit immediately.
	render()

	// Input handler: routes keydown events into the shell. The compositor
	// sends one event per keystroke with kind=="keydown" + key=="<char>" for
	// printable keys, "Enter" / "Backspace" for the control keys we care
	// about. The handler re-renders only when HandleKey reports a change.
	cb := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		kind := ev.Get("kind").String()
		if kind != "keydown" {
			return nil
		}
		key := ev.Get("key").String()
		if state.HandleKey(key) {
			render()
		}
		return nil
	})
	client.Call("onInput", cb)

	// Park forever so the Go runtime keeps the FuncOf callback alive. Without
	// a live root the wasm Go runtime would free the callback on the next GC.
	select {}
}

// openVFS opens the IndexedDB-backed VFS, falling back to an in-memory VFS
// when IDB is not available. The returned VFS is always ready to serve --
// IDB.LoadAll() is invoked here so a successful open immediately reflects
// the persisted tree.
func openVFS() sharedvfs.VFS {
	db := sharedvfs.OpenIDB(vfsDBName, vfsStoreName)
	if !db.Truthy() {
		println("terminal: IndexedDB unavailable; falling back to in-memory VFS")
		return sharedvfs.NewInMemoryVFS()
	}
	idb := sharedvfs.NewIDBVFS(db, vfsStoreName)
	idb.LoadAll()
	return idb
}
