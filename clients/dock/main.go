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
	"encoding/json"
	"strings"
	"syscall/js"

	"github.com/wasmdesk/wasmbox/clients/dock/internal/scene"
	"github.com/wasmdesk/wasmbox/clients/dock/internal/theme"
)

// exposeGeometry publishes the dock's current window-button rectangles (surface
// coordinates) on a worker global so headless probes can locate + click a
// specific window's iconbar entry without hardcoding the layout. Read it via
// Playwright's worker.evaluate(() => globalThis.__wasmdockGeometry): the screen
// position is (VIEW_W - w)/2 + x, (VIEW_H - h) + y (the dock is bottom-center
// anchored). Cheap; only called when the window set changes.
func exposeGeometry(state *scene.State) {
	wins := state.Windows
	buttons := make([]interface{}, 0, len(wins))
	for i := range wins {
		x, y, w, h := state.WindowButtonRect(i)
		buttons = append(buttons, map[string]interface{}{
			"id":        wins[i].Id,
			"title":     wins[i].Title,
			"minimized": wins[i].Minimized,
			"focused":   wins[i].Focused,
			"x":         x, "y": y, "w": w, "h": h,
		})
	}
	js.Global().Set("__wasmdockGeometry", js.ValueOf(map[string]interface{}{
		"w":       state.W,
		"h":       state.H,
		"buttons": buttons,
	}))
}

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
		// Open the seqlock write window before copying the frame into the SAB so
		// the compositor never blits a half-copied frame (matters for the dock's
		// per-hover magnification repaints); client.commit() closes it.
		client.Call("beginFrame")
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

	// focus asks the compositor to raise + focus a window the user
	// left-clicked on its iconbar button. Travels over the SDK's MessagePort
	// just like `launch`; the compositor's WindowManager.handle_client_message
	// routes the message to its `:focus` arm — which also restores the window
	// first if it was minimized, matching Fluxbox semantics (one click on an
	// iconbar entry brings the window to the foreground). The compositor then
	// pushes a refreshed window list back through `windows_changed`.
	focusWin := func(id int) {
		println("wasmdock: focus", id)
		client.Call("focus", id)
	}

	// closeWin asks the compositor to close a window the user right-clicked
	// on its iconbar button. Same effect as clicking the window's title-bar
	// close box. Fire-and-forget; the compositor drops the message for an
	// unknown or panel id.
	closeWin := func(id int) {
		println("wasmdock: close", id)
		client.Call("closeWindow", id)
	}

	// setWorkspace asks the compositor to switch the active workspace to
	// `index` (1..workspaceCount). Travels over the SDK's MessagePort just
	// like `launch`; the compositor's WindowManager.handle_client_message
	// routes the message to its `:set_workspace` arm and broadcasts a
	// `workspace_changed` event back here on success (which updates the
	// model + repaints).
	setWorkspace := func(index int) {
		println("wasmdock: setWorkspace", index)
		client.Call("setWorkspace", index)
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
			// Mouse button: 0 = left, 2 = right (matches the W3C DOM
			// MouseEvent.button). The compositor forwards the raw value via
			// forward_mouse_to_client; missing field falls back to 0.
			button := 0
			if b := ev.Get("button"); !b.IsUndefined() && !b.IsNull() {
				button = b.Int()
			}
			// Workspace section: left-click cycles to the next workspace.
			// A right-click is reserved for a future "workspace menu" — in
			// v0 it is a no-op (no popup yet, no per-workspace context).
			if state.HitTestWorkspace(x, y) {
				if button != 2 {
					setWorkspace(state.NextWorkspace())
				}
				break
			}
			if i := state.HitTest(x, y); i >= 0 {
				launch(state.Apps[i].Id)
				break
			}
			if i := state.HitTestWindow(x, y); i >= 0 {
				id := state.Windows[i].Id
				if button == 2 {
					// Right-click on a window button: close the window.
					closeWin(id)
				} else {
					// Left-click (or any non-right button): focus + raise
					// (restoring first if minimized).
					focusWin(id)
				}
			}
		case "wheel":
			// Scroll-wheel input: cycle workspaces when the wheel fires over
			// the workspace section. deltaY > 0 = scroll DOWN = forward
			// (next workspace); deltaY < 0 = scroll UP = backward (previous
			// workspace). A wheel elsewhere on the toolbar is ignored.
			x := ev.Get("x").Int()
			y := ev.Get("y").Int()
			if !state.HitTestWorkspace(x, y) {
				break
			}
			dy := 0.0
			if d := ev.Get("deltaY"); !d.IsUndefined() && !d.IsNull() {
				dy = d.Float()
			}
			if dy > 0 {
				setWorkspace(state.NextWorkspace())
			} else if dy < 0 {
				setWorkspace(state.PrevWorkspace())
			}
		case "workspace_changed":
			// Compositor pushes the new active workspace + total count after
			// a successful set_workspace. Update the model + repaint so the
			// workspace section shows the new "<active> of <count>" label.
			// The compositor sends a windows_changed immediately after this
			// event, so the iconbar refresh is handled by that arm and we
			// only need to re-render the workspace label here.
			if c := ev.Get("count"); !c.IsUndefined() && !c.IsNull() {
				state.SetWorkspaceCount(c.Int())
			}
			if a := ev.Get("active"); !a.IsUndefined() && !a.IsNull() {
				state.SetActiveWorkspace(a.Int())
			}
			render()
		case "windows_changed":
			// Compositor pushes the current open-window list as a
			// JSON-encoded array string under `windows_json`. We parse it
			// into a fresh []scene.Window and re-render so the iconbar
			// reflects the new state (new window, close, minimize, restore,
			// focus shift, title rename — every state-changing event posts
			// a fresh windows_changed).
			raw := ev.Get("windows_json")
			if raw.IsUndefined() || raw.IsNull() {
				state.SetWindows(nil)
			} else {
				var parsed []scene.Window
				if err := json.Unmarshal([]byte(raw.String()), &parsed); err != nil {
					println("wasmdock: windows_changed parse error:", err.Error())
					parsed = nil
				}
				state.SetWindows(parsed)
			}
			render()
			exposeGeometry(state) // test hook: publish window-button rects
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
		case "theme_changed":
			// Compositor broadcasts the new active theme to every panel after
			// a successful set_theme. The payload carries `name` (display
			// label, for future "active theme" indicators) and `themerc`
			// (the raw .themerc source — we re-parse it locally rather than
			// reading individual fields off JS so a future theme attribute
			// is one parser change here, not a wire-shape change). Unknown
			// names + empty .themerc are dropped silently; the dock keeps
			// its previous theme.
			rc := ev.Get("themerc")
			if rc.IsUndefined() || rc.IsNull() {
				break
			}
			src := rc.String()
			if strings.TrimSpace(src) == "" {
				break
			}
			th, _ := theme.ParseRC(strings.NewReader(src))
			state.SetTheme(th)
			render()
		}
		return nil
	})
	client.Call("onInput", cb)

	// Park forever so the Go runtime keeps the FuncOf callback alive.
	select {}
}
