// Command showcase is the wasmdesk/toolkit reference client: a single
// wasmbox window holding a MenuBar + Toolbar + Notebook of tabs, each
// tab exercising a different widget family from
// github.com/wasmdesk/toolkit. Acts as both a smoke test for the
// toolkit and a live API reference users can poke from inside the
// wasmdesk compositor.
//
//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/wasmdesk/wasmbox/clients/showcase/internal/scene"
)

func main() {
	client := js.Global().Get("wasmboxClient")
	if client.IsUndefined() {
		println("showcase: wasmboxClient missing; SDK not loaded?")
		return
	}

	w := client.Get("w").Int()
	h := client.Get("h").Int()
	pixels := client.Get("pixels")
	bufLen := pixels.Get("length").Int()
	if bufLen != 4*w*h {
		println("showcase: pixel buffer size mismatch")
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

	cb := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		kind := ev.Get("kind").String()
		switch kind {
		case "mousedown":
			x := ev.Get("x").Int()
			y := ev.Get("y").Int()
			if state.HandleMouse(x, y) {
				render()
			}
		case "keydown":
			key := ev.Get("key").String()
			if state.HandleKey(key) {
				render()
			}
		case "char":
			text := ev.Get("text").String()
			if state.HandleChar(text) {
				render()
			}
		}
		return nil
	})
	client.Call("onInput", cb)
	select {}
}
