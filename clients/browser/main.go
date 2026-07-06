// Command browser is a WhiteSur/Safari-styled web browser shell: a #ebebeb
// toolbar (back / forward buttons + a rounded address bar + a new-tab button)
// above a "Favourites" start page of bookmark tiles. The wasmbox page runs
// under COEP:require-corp, so live cross-origin sites can't be embedded; the
// tiles navigate to local placeholder pages with a back/forward history model.
//
//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/wasmdesk/wasmbox/clients/browser/internal/scene"
)

func main() {
	client := js.Global().Get("wasmboxClient")
	if client.IsUndefined() {
		println("browser: wasmboxClient missing; SDK not loaded?")
		return
	}

	w := client.Get("w").Int()
	h := client.Get("h").Int()
	pixels := client.Get("pixels")
	if pixels.Get("length").Int() != 4*w*h {
		println("browser: pixel buffer size mismatch")
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
