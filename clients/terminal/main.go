// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Command terminal is the wasmbox external client spawned when the dock's
// "terminal" icon is clicked. It paints a recognizable "Terminal" placeholder
// surface into the SAB the SDK allocated for it and parks waiting for input.
//
// It runs inside a dedicated Web Worker (see worker.js). The Worker has
// already imported sdk.js (which exposes globalThis.WasmboxClient) and
// constructed `wasmboxClient`, then awaited `start()` — so by the time main()
// runs we are connected. A real TTY is out of scope; this client exists so
// the launch chain (dock -> compositor -> terminal client) produces a window
// titled "Terminal" rather than a generic placeholder.
//
//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/wasmdesk/wasmbox/clients/terminal/internal/scene"
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

	render()

	// Keep an input callback alive so the compositor's mousedown/keydown reaches
	// the worker (silently ignored for now). Without a live FuncOf the Go
	// runtime could collect the Worker's bridge state on quiet frames.
	cb := js.FuncOf(func(_ js.Value, _ []js.Value) any { return nil })
	client.Call("onInput", cb)

	select {}
}
