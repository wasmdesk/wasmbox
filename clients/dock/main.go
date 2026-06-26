// Command wasmdock is a macOS-style dock implemented as a wasmbox external
// client. It paints a bottom-anchored, horizontally-centered translucent bar
// of app icons into the SAB the SDK allocated for it, magnifies the icons
// around the cursor on mousemove, and — when an icon is clicked — asks the
// compositor to launch that app via a {type:"launch", app:"<id>"} message.
//
// It runs inside a dedicated Web Worker (see worker.js). The Worker has
// already imported sdk.js (which exposes globalThis.WasmboxClient) and
// constructed `wasmboxClient`, then awaited `start()` — so by the time main()
// runs we are connected. All the windowing-independent logic (layout,
// magnification, hit-testing, painting) lives in the pure internal/scene
// package; this file is the thin JS/SAB/postMessage glue.
//
//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/wasmdesk/wasmbox/clients/dock/internal/scene"
)

func main() {
	client := js.Global().Get("wasmboxClient")
	if client.IsUndefined() {
		println("wasmdock: wasmboxClient missing; SDK not loaded?")
		return
	}

	w := client.Get("w").Int()
	h := client.Get("h").Int()
	pixels := client.Get("pixels")
	bufLen := pixels.Get("length").Int()
	if bufLen != 4*w*h {
		println("wasmdock: pixel buffer size mismatch")
		return
	}

	// Pure-Go RGBA buffer; scene.Render fills it, then we copy once per frame
	// into the SAB through the SDK's Uint8ClampedArray view.
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

	// launch asks the compositor to start another client. This is a protocol
	// extension (see INTEGRATION.md); the dock degrades gracefully if the host
	// does not honor it yet — fire-and-forget. The launch message MUST travel
	// over the SDK's MessagePort (the per-client wire the compositor listens on
	// for `wasmbox-msg`), not over `self.postMessage` (the implicit nested-worker
	// channel to compositor.worker.js, which only handles main<->compositor boot
	// traffic and silently drops application messages like `launch`). Routing
	// through `client.launch(app)` keeps us on the port wire end-to-end.
	launch := func(app string) {
		println("wasmdock: launch", app)
		client.Call("launch", app)
	}

	// Initial paint so the compositor has something to blit immediately.
	render()

	cb := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		kind := ev.Get("kind").String()
		switch kind {
		case "mousemove":
			x := ev.Get("x").Int()
			y := ev.Get("y").Int()
			state.SetCursor(x, y, true)
			render()
		case "mousedown":
			x := ev.Get("x").Int()
			y := ev.Get("y").Int()
			if i := state.HitTest(x, y); i >= 0 {
				launch(state.Apps[i].Id)
			}
		}
		return nil
	})
	client.Call("onInput", cb)

	// When the pointer leaves the focused dock window the compositor stops
	// forwarding mousemove; we cannot observe a DOM mouseleave here, so the
	// dock simply keeps its last magnification until the next move. A future
	// protocol "blur" event could reset it via state.SetCursor(0,0,false).

	// Park forever so the Go runtime keeps the FuncOf callback alive.
	select {}
}
