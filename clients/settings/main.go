// Command settings is a WhiteSur-styled preferences panel: a grey category
// sidebar (Appearance / Wi-Fi / Sound / Displays / General) and a white content
// pane whose rows carry toolkit Switch + Scale controls. Validates that those
// controls compose into a real System-Settings-style surface.
//
//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/wasmdesk/wasmbox/clients/settings/internal/scene"
)

func main() {
	client := js.Global().Get("wasmboxClient")
	if client.IsUndefined() {
		println("settings: wasmboxClient missing; SDK not loaded?")
		return
	}

	w := client.Get("w").Int()
	h := client.Get("h").Int()
	pixels := client.Get("pixels")
	if pixels.Get("length").Int() != 4*w*h {
		println("settings: pixel buffer size mismatch")
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
		}
		return nil
	})
	client.Call("onInput", cb)
	select {}
}
