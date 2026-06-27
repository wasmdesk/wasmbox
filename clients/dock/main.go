// SPDX-License-Identifier: BSD-3-Clause
//
// Command wasmdock is a Fluxbox-style bottom toolbar implemented as a
// wasmbox external client. It paints a full-width, 28-pixel-tall bevelled
// gray bar split into three sections — a workspace label, an iconbar of
// launcher buttons, and a clock — into the SAB the SDK allocated for it,
// and dispatches {type:"launch", app:"<id>"} to the compositor when an
// iconbar button is clicked.
//
// The pure scene + theme packages do all the layout, hit-testing and
// painting; this file is the thin JS/SAB/postMessage glue. The worker.js
// shell posts a "tick" input event every 30 seconds carrying the current
// "HH:MM" string so the clock stays fresh without a Go-side time source
// (the wasm runtime's clock is fine, but the toolbar reads the JS one for
// timezone consistency with the rest of the page).
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

	// Pure-Go RGBA buffer; scene.Render fills it, then we copy once per
	// frame into the SAB through the SDK's Uint8ClampedArray view.
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

	// launch asks the compositor to start another client. The launch
	// message MUST travel over the SDK's MessagePort (the per-client wire
	// the compositor listens on for `wasmbox-msg`), not over
	// `self.postMessage` (the implicit nested-worker channel to
	// compositor.worker.js, which only handles main<->compositor boot
	// traffic and silently drops application messages like `launch`).
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
			// No hover paint in v0; SetCursor is recorded for a future
			// highlight pass. Avoid an unconditional re-render on every
			// mousemove so the worker stays idle while the cursor wanders.
		case "mousedown":
			x := ev.Get("x").Int()
			y := ev.Get("y").Int()
			if i := state.HitTest(x, y); i >= 0 {
				launch(state.Apps[i].Id)
			}
		case "tick":
			// Clock tick posted by worker.js. The payload field "clock"
			// carries the latest "HH:MM" string; the optional "workspace"
			// field can update the workspace label without a separate
			// event type.
			clock := ev.Get("clock")
			if !clock.IsUndefined() && !clock.IsNull() {
				state.SetClock(clock.String())
			}
			ws := ev.Get("workspace")
			if !ws.IsUndefined() && !ws.IsNull() {
				state.SetWorkspace(ws.String())
			}
			render()
		}
		return nil
	})
	client.Call("onInput", cb)

	// Park forever so the Go runtime keeps the FuncOf callback alive.
	select {}
}
