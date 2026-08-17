// SPDX-License-Identifier: BSD-3-Clause
//
// Command wasmdock is a Fluxbox-style bottom toolbar implemented as a
// wasmbox external client. It paints a full-width, 28-pixel-tall bevelled
// gray bar split into three sections — a workspace label, an iconbar of
// launcher buttons (with macOS-style hover magnification, running/active
// indicators + attention badges), and a clock — into the SAB the SDK
// allocated for it, and dispatches {type:"launch"/"focus"/"close", ...} to
// the compositor on clicks. Open windows collapse into indicators on their
// launcher; a right-click on a launcher opens an application context menu in
// a child popup surface where per-window focus / close actions live.
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

// exposeGeometry publishes the dock's current launcher rectangles (MAGNIFIED
// when the cursor hovers, so a probe clicks where the icon is actually painted)
// on a worker global for headless probes. Read it via Playwright's
// worker.evaluate(() => globalThis.__wasmdockGeometry): the screen position of a
// rect is (VIEW_W - w)/2 + x, (VIEW_H - h) + y (the dock is bottom-center
// anchored). Open windows collapse into indicators on their launcher, so the
// probe reads launcher rects only. Cheap; refreshed on window changes + hover
// repaints.
func exposeGeometry(state *scene.State) {
	lr := state.LauncherRects()
	running := state.LauncherRunning()
	launchers := make([]interface{}, 0, len(lr))
	for i, r := range lr {
		launchers = append(launchers, map[string]interface{}{
			"id": state.Apps[i].Id,
			"x":  r[0], "y": r[1], "w": r[2], "h": r[3],
			"running": running[i],
		})
	}
	js.Global().Set("__wasmdockGeometry", js.ValueOf(map[string]interface{}{
		"w":         state.W,
		"h":         state.H,
		"launchers": launchers,
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

	// Initial paint so the compositor has something to blit immediately, plus a
	// first geometry publish so a probe can read the resting layout.
	render()
	exposeGeometry(state)

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
			// Repaint so the hover magnification follows the cursor. Skipped
			// when the effect is off so a flat dock stays idle.
			if state.Magnify.On {
				render()
				exposeGeometry(state)
			}
		case "mouseleave", "mouseout":
			// Pointer left the panel — drop magnification back to flat.
			state.SetCursor(state.CursorX, state.CursorY, false)
			if state.Magnify.On {
				render()
				exposeGeometry(state)
			}
		case "mousedown":
			x := ev.Get("x").Int()
			y := ev.Get("y").Int()
			// Keep the cursor recorded so hit-testing reads the SAME magnified
			// geometry the user is clicking on.
			state.SetCursor(x, y, true)
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
				if button == 2 {
					openMenu(client, state.BuildLauncherMenu(i), x)
				} else {
					launch(state.Apps[i].Id)
				}
				break
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

// hasMethod reports whether the JS value exposes a callable method `name`, so
// optional SDK methods (beginFrame / openPopup / requestClose) are
// feature-detected before use — a missing one degrades gracefully.
func hasMethod(v js.Value, name string) bool {
	return v.Get(name).Type() == js.TypeFunction
}

// openMenu opens the dock's right-click application context menu in a child
// popup surface anchored above the clicked entry, paints it via the pure
// scene.DockMenu renderer, and routes a click inside it back to a launch /
// focus / close wire message. The 28px bar is far too short to draw a menu
// in-surface, so this uses the compositor's existing "popup" role (no
// compositor change): the compositor grab-dismisses the popup on an outside
// click, and a selection requests its close. A no-op when the menu is empty or
// the SDK has no openPopup, so the dock never throws.
func openMenu(client js.Value, menu scene.DockMenu, anchorX int) {
	if len(menu.Entries) == 0 || !hasMethod(client, "openPopup") {
		return
	}
	// Anchor the menu above the bar (rel_y negative pops it upward), centred
	// under the click and clamped to the parent's left edge.
	relX := anchorX - menu.W/2
	if relX < 0 {
		relX = 0
	}
	opts := js.Global().Call("Object")
	opts.Set("title", "dock menu")
	opts.Set("w", menu.W)
	opts.Set("h", menu.H)
	opts.Set("rel_x", relX)
	opts.Set("rel_y", -menu.H)
	popup := client.Call("openPopup", opts)

	hover := -1
	var buf []byte
	var pw, ph int
	var pixels js.Value

	paint := func() {
		if buf == nil {
			return
		}
		menu.MenuRender(buf, pw, ph, hover)
		if hasMethod(popup, "beginFrame") {
			popup.Call("beginFrame")
		}
		js.CopyBytesToJS(pixels, buf)
		popup.Call("commit", js.Undefined())
	}

	var inputCb js.Func
	popup.Call("onWelcome", js.FuncOf(func(_ js.Value, _ []js.Value) any {
		pw = popup.Get("w").Int()
		ph = popup.Get("h").Int()
		pixels = popup.Get("pixels")
		buf = make([]byte, 4*pw*ph)
		paint()
		exposeMenu(menu, relX, -menu.H) // test hook: publish clickable menu rows
		return nil
	}))
	inputCb = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		switch ev.Get("kind").String() {
		case "mousemove":
			if hv := menu.MenuHover(ev.Get("y").Int()); hv != hover {
				hover = hv
				paint()
			}
		case "mousedown":
			if idx := menu.MenuHitTest(ev.Get("y").Int()); idx >= 0 {
				dispatchMenu(client, menu.Entries[idx])
			}
			// Dismiss the menu after a click (a selection or a gap click).
			if hasMethod(popup, "requestClose") {
				popup.Call("requestClose")
			}
		}
		return nil
	})
	popup.Call("onInput", inputCb)
	popup.Call("onClosed", js.FuncOf(func(js.Value, []js.Value) any {
		inputCb.Release()
		js.Global().Set("__wasmdockMenu", js.Null()) // menu gone
		return nil
	}))
}

// exposeMenu publishes the open context menu's clickable rows on a worker
// global for headless probes: the popup is anchored inside the dock body at
// (relX, relY), so a probe maps a row to screen the same way it maps a dock
// button rect, then clicks (x + w/2, y + cy) for a given row label. Cleared to
// null when the menu closes.
func exposeMenu(menu scene.DockMenu, relX, relY int) {
	centers := menu.RowCenters()
	rows := make([]interface{}, 0, len(menu.Entries))
	for i, e := range menu.Entries {
		if e.Separator {
			continue
		}
		rows = append(rows, map[string]interface{}{
			"label": e.Label, "action": actionName(e.Action), "cy": centers[i],
		})
	}
	js.Global().Set("__wasmdockMenu", js.ValueOf(map[string]interface{}{
		"rel_x": relX, "rel_y": relY, "w": menu.W, "h": menu.H, "rows": rows,
	}))
}

// actionName is the wire-message name a menu action maps to (for the probe hook).
func actionName(a scene.MenuAction) string {
	switch a {
	case scene.ActLaunch:
		return "launch"
	case scene.ActFocus:
		return "focus"
	case scene.ActClose:
		return "close"
	default:
		return "none"
	}
}

// dispatchMenu sends the wire message a chosen menu entry maps to.
func dispatchMenu(client js.Value, e scene.MenuEntry) {
	switch e.Action {
	case scene.ActLaunch:
		client.Call("launch", e.App)
	case scene.ActFocus:
		client.Call("focus", e.Win)
	case scene.ActClose:
		client.Call("closeWindow", e.Win)
	}
}
