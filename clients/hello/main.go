// Command hello is a minimal wasmbox external client written in Go: it paints
// a gradient into the SAB the SDK allocated for it, posts an initial commit so
// the compositor blits the surface, and toggles its palette on mousedown.
//
// It runs inside a dedicated Web Worker (see worker.js). The Worker has
// already imported sdk.js (which exposes globalThis.WasmboxClient) and
// constructed `wasmboxClient`, then awaited `start()` — so by the time
// main() runs we are connected.
//
//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/wasmdesk/wasmbox/clients/hello/internal/scene"
)

func main() {
	client := js.Global().Get("wasmboxClient")
	if client.IsUndefined() {
		println("hello: wasmboxClient missing; SDK not loaded?")
		return
	}

	w := client.Get("w").Int()
	h := client.Get("h").Int()
	pixels := client.Get("pixels")
	bufLen := pixels.Get("length").Int()
	if bufLen != 4*w*h {
		println("hello: pixel buffer size mismatch")
		return
	}

	// Build a local RGBA byte buffer, hand it to scene.Render to fill, then
	// stream into the SAB through a Uint8ClampedArray view. Keeping a local
	// buffer means scene.Render is pure Go (no js dependency) and we copy
	// once per commit via js.CopyBytesToJS.
	local := make([]byte, 4*w*h)
	state := scene.New(w, h)

	render := func() {
		scene.Render(state, local)
		client.Call("beginFrame") // open the seqlock window before the bulk copy (tear-free)
		js.CopyBytesToJS(pixels, local)
		// One commit covering the whole surface; the compositor unions damage.
		damage := js.Global().Call("Object")
		damage.Set("x", 0)
		damage.Set("y", 0)
		damage.Set("w", w)
		damage.Set("h", h)
		client.Call("commit", damage)
	}

	// Initial paint so the compositor has something to blit immediately.
	render()

	// Input handler: any mousedown advances the palette. Registered via a
	// JS function that calls back into Go.
	cb := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		kind := ev.Get("kind").String()
		if kind == "mousedown" {
			state.NextPalette()
			render()
		}
		return nil
	})

	// Hook into the SDK's onInput.
	client.Call("onInput", cb)

	// Park forever so the Go runtime keeps the FuncOf callbacks alive.
	select {}
}
