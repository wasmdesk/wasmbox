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
	"strings"
	"syscall/js"

	"github.com/wasmdesk/wasmbox/clients/showcase/internal/scene"
)

// parseQueryParam extracts a value from a "?a=1&b=2"-style query
// string. Returns "" when the key is absent or the string is
// malformed. Not a URL-decoder — just a splitter (values with
// %-escapes come through raw, which is fine for the frame= use
// case where the value is always a bare a-z-hyphen registry key).
func parseQueryParam(query, key string) string {
	q := strings.TrimPrefix(query, "?")
	for _, pair := range strings.Split(q, "&") {
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			continue
		}
		if pair[:eq] == key {
			return pair[eq+1:]
		}
	}
	return ""
}

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

	// Wire the "swap compositor Frame" callback so the showcase's
	// Frame menu can ask the compositor to re-decorate every window
	// live. The SDK's setFrame method posts a set_frame wire message
	// the WindowManager arm accepts; no client-side re-render needed.
	state.SetFrameSetter(func(name string) {
		client.Call("setFrame", name)
	})

	// Seed the Frame menu's active marker from the page URL's
	// ?frame= param — same source the compositor read at boot.
	// Falls back to "openbox" when unset. Best-effort: if the URL
	// or query API is unavailable in this worker context, skip
	// (the menu just has no marker until the user clicks).
	if loc := js.Global().Get("location"); !loc.IsUndefined() {
		if search := loc.Get("search"); !search.IsUndefined() {
			s := search.String()
			frameName := parseQueryParam(s, "frame")
			if frameName == "" {
				frameName = "openbox"
			}
			state.SetActiveFrame(frameName)
		}
	}

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
