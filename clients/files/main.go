// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Command files is the wasmbox external client spawned when the dock's
// "files" icon is clicked. It hosts a real (if tiny) file browser inspired
// by macOS Finder: an in-memory virtual filesystem with a few demo files +
// nested folders, rendered as a multi-column list with a Favorites sidebar
// and a back-arrow toolbar. Navigated by Up/Down (move cursor), Enter
// (descend into a folder), Backspace/Escape (go up), and the mouse (click
// a Favorite to jump, click a row to select/activate, click the back-arrow
// button to go up).
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

	"github.com/wasmdesk/wasmbox/clients/files/internal/scene"
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

	// Local RGBA buffer + the pure-Go scene state. Each frame we re-paint
	// into `local`, then copy into the SAB-backed Uint8ClampedArray in one
	// js.CopyBytesToJS call -- same pattern as the terminal/hello clients.
	local := make([]byte, 4*w*h)
	state := scene.New(w, h)

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

	// Input handler: routes keydown + mousedown events into the browser. The
	// compositor sends one event per input with kind=="keydown"/"mousedown"
	// + key/x/y fields. We re-render only when the handler reports a state
	// change.
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
			if state.HandleMouse(x, y) {
				render()
			}
		}
		return nil
	})
	client.Call("onInput", cb)

	// Park forever so the Go runtime keeps the FuncOf callback alive.
	select {}
}
