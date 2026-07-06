// Command calculator is a toolkit consumer with tighter scope than the
// multi-tab showcase: one display Entry across the top + a 5×4 button
// grid below (0..9, . + - * / = C +/- %). Validates that Grid + Button +
// Entry compose cleanly in a real, small production layout.
//
//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/wasmdesk/wasmbox/clients/calculator/internal/scene"
)

func main() {
	client := js.Global().Get("wasmboxClient")
	if client.IsUndefined() {
		println("calculator: wasmboxClient missing; SDK not loaded?")
		return
	}

	w := client.Get("w").Int()
	h := client.Get("h").Int()
	pixels := client.Get("pixels")
	if pixels.Get("length").Int() != 4*w*h {
		println("calculator: pixel buffer size mismatch")
		return
	}

	local := make([]byte, 4*w*h)
	state := scene.New(w, h)

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

	cb := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		switch ev.Get("kind").String() {
		case "mousedown":
			if state.HandleMouse(ev.Get("x").Int(), ev.Get("y").Int()) {
				render()
			}
		case "keydown":
			if state.HandleKey(ev.Get("key").String()) {
				render()
			}
		case "char":
			if state.HandleChar(ev.Get("text").String()) {
				render()
			}
		}
		return nil
	})
	client.Call("onInput", cb)
	select {}
}
