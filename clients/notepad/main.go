// Command notepad is a multi-doc plain-text editor built on the
// wasmdesk/toolkit widget set: Toolbar (New/Open/Save + Cut/Copy/Paste
// + Help) + ListBox (documents panel) + TextView (editor with IME
// preview) + Statusbar (doc-count / line-col / encoding readout). A
// real toolkit consumer with a broader widget footprint than the
// Calculator client.
//
//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/wasmdesk/wasmbox/clients/notepad/internal/scene"
)

func main() {
	client := js.Global().Get("wasmboxClient")
	if client.IsUndefined() {
		println("notepad: wasmboxClient missing; SDK not loaded?")
		return
	}
	w := client.Get("w").Int()
	h := client.Get("h").Int()
	pixels := client.Get("pixels")
	if pixels.Get("length").Int() != 4*w*h {
		println("notepad: pixel buffer size mismatch")
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

	// Drive the notification's Life countdown from an interval so
	// toasts auto-hide even when the user isn't interacting. 16 ms ≈
	// 60 Hz — matches the notification's NotificationLife = 180
	// (3 s dismiss). Re-renders too so the fade-out is visible.
	tick := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		state.Tick()
		render()
		return nil
	})
	js.Global().Call("setInterval", tick, 16)

	select {}
}
