// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Command terminal is the wasmbox external client spawned when the dock's
// "terminal" icon is clicked. It hosts a real interactive shell on
// go-widgets/shellterm (the terminal shared with the go-wasmcontainers
// dashboard): a line editor with cursor motion, kill/yank, history, Tab
// completion and Ctrl-R, in front of a POSIX-ish shell (pipes, redirection,
// &&/||/;, $?) over the wasmdesk/coreutils tools. Output may carry escape
// sequences (colours, cursor motion). Text is selected with the mouse and
// copied with Meta+C / Ctrl+Shift+C; Meta+V / Ctrl+Shift+V pastes (a plain
// Ctrl+V is ^V for the shell). The clipboard is the browser's, through the
// wasmbox clipboard protocol (docs/protocol.md "Clipboard").
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

	"github.com/wasmdesk/wasmbox/clients/clipboard"
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

	// The browser's clipboard, through the compositor, is the toolkit's.
	clip := clipboard.Install(client)

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
		client.Call("beginFrame") // open the seqlock window before the bulk copy (tear-free)
		js.CopyBytesToJS(pixels, local)
		damage := js.Global().Call("Object")
		damage.Set("x", 0)
		damage.Set("y", 0)
		damage.Set("w", w)
		damage.Set("h", h)
		client.Call("commit", damage)
	}

	// Initial paint so the compositor has something to blit immediately.
	state.Drain()
	render()

	// Input handler: routes the compositor's input events into the terminal.
	//   keydown  -- key + ctrl/meta/alt/shift: typed text, or a key press
	//   paste    -- the clipboard text, sent ahead of the paste shortcut's keydown
	//   mouse*   -- selection (press + drag), wheel -- scrollback
	// Keys, completion and commands run on shellterm's own goroutines; what they
	// show comes back through the state's queue (the Wake loop below), so a key
	// handler never blocks the event loop and never touches the screen.
	cb := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		changed := false
		switch ev.Get("kind").String() {
		case "keydown":
			changed = state.HandleKey(scene.Key{
				Key:   ev.Get("key").String(),
				Ctrl:  ev.Get("ctrl").Truthy(),
				Meta:  ev.Get("meta").Truthy(),
				Alt:   ev.Get("alt").Truthy(),
				Shift: ev.Get("shift").Truthy(),
			})
		case "paste":
			if t := ev.Get("text"); t.Type() == js.TypeString {
				clip.Deliver(t.String())
			}
		case "mousedown", "mousemove", "mouseup":
			changed = state.HandleMouse(ev.Get("kind").String(), ev.Get("x").Int(), ev.Get("y").Int(), 0)
		case "wheel":
			changed = state.HandleMouse("wheel", ev.Get("x").Int(), ev.Get("y").Int(), ev.Get("deltaY").Float())
		}
		if changed {
			render()
		}
		return nil
	})
	client.Call("onInput", cb)

	// The UI loop: the session queues what the screen must show (output, the
	// prompt, a clear) and wakes us; we run it and repaint once. This also
	// parks main forever, so the Go runtime keeps the FuncOf callback alive.
	for range state.Wake() {
		if state.Drain() > 0 {
			render()
		}
	}
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
