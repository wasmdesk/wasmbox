// Command rbtest runs the pure-Ruby half of compositor.rb (Theme, Window,
// ExternalWindow, WindowManager) on the native go-embedded-ruby interpreter
// and asserts the step-B window-manager logic — message dispatch, external
// window registration, damage merging, input translation — behaves correctly.
//
// The JS-touching Compositor class plus the boot block at the bottom of
// compositor.rb are deliberately skipped: the test loads only the bytes BEFORE
// `class Compositor`, then appends a Ruby assertion script. This way the same
// file that ships inside wasmbox.wasm is the file under test — no shadow copy
// to drift from.
//
// Exit code is 0 on success, 1 on any failed assertion (Ruby `raise`s and the
// Go wrapper surfaces the error). `task test` invokes it.
//
//go:build !js
// +build !js

package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"strings"

	ruby "github.com/go-embedded-ruby/ruby"
)

// splitMarker is the first line of the Compositor class definition. Everything
// before it is the pure WM half (safe off-wasm); everything from it onward
// touches the JS bridge.
const splitMarker = "class Compositor"

func main() {
	path := flag.String("rb", "compositor.rb", "path to compositor.rb (relative to cwd)")
	raw := flag.Bool("raw", false, "run -rb file as-is without splitting on `class Compositor` or appending the WM test script (debug aid)")
	flag.Parse()
	src, err := os.ReadFile(*path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rbtest: read %s: %v\n", *path, err)
		os.Exit(2)
	}
	compositorRB := string(src)
	var script string
	if *raw {
		script = compositorRB
	} else {
		idx := strings.Index(compositorRB, splitMarker)
		if idx < 0 {
			fmt.Fprintln(os.Stderr, "rbtest: cannot locate `class Compositor` in compositor.rb")
			os.Exit(2)
		}
		pure := compositorRB[:idx]
		script = pure + "\n" + testScript
	}
	var out bytes.Buffer
	if err := ruby.Run(script, &out); err != nil {
		fmt.Fprintln(os.Stderr, out.String())
		fmt.Fprintf(os.Stderr, "rbtest FAIL: %v\n", err)
		os.Exit(1)
	}
	// Pass-through Ruby's stdout so test reports show.
	os.Stdout.Write(out.Bytes())
	fmt.Println("rbtest: PASS")
}

// testScript exercises the pure WM logic. Each `assert` raises on failure so
// ruby.Run returns a non-nil error and rbtest exits non-zero. Stays free of
// JS calls — the same script could run under MRI in principle.
const testScript = `
def assert(cond, msg)
  raise "ASSERT FAILED: #{msg}" unless cond
end

def assert_eq(actual, expected, msg)
  unless actual == expected
    raise "ASSERT_EQ FAILED (#{msg}): expected #{expected.inspect}, got #{actual.inspect}"
  end
end

# ---- WindowManager#spawn + focus ------------------------------------------
wm = WindowManager.new
w1 = wm.spawn("a")
w2 = wm.spawn("b")
assert_eq(wm.windows.length, 2, "spawn count")
assert(wm.focused.equal?(w2), "focus on most recent spawn")
assert(w2.focused?, "focused window marks itself")
assert(!w1.focused?, "non-focused window is not focused")

# ---- WindowManager#cycle (Alt+Tab) ----------------------------------------
wm.cycle
assert(wm.focused.equal?(w1), "cycle moves focus to next window")

# ---- handle_client_message :welcome path ----------------------------------
wm2 = WindowManager.new
res = wm2.handle_client_message({ type: "hello", title: "ext", w: 200, h: 150,
                                  sab: :fake_sab, stride: 800 })
assert_eq(res, :welcome, "hello yields :welcome")
ext = wm2.focused
assert(ext.external?, "registered external window is external")
assert_eq(ext.w, 200, "granted_w")
assert_eq(ext.h, 150, "granted_h")
assert_eq(ext.title, "ext", "title carried through")
assert_eq(ext.sab, :fake_sab, "sab stored")
assert_eq(ext.stride, 800, "stride stored")

# ---- handle_client_message :commit + damage merge -------------------------
# Fresh ExternalWindow comes pre-populated with full-surface damage so the
# first frame is never blank — clear it before testing pure union semantics.
ext.clear_damage
res = wm2.handle_client_message({ type: "commit", window_id: ext.id,
                                  damage: { x: 10, y: 10, w: 20, h: 20 } })
assert_eq(res, :commit, "commit yields :commit")
res = wm2.handle_client_message({ type: "commit", window_id: ext.id,
                                  damage: { x: 100, y: 100, w: 30, h: 30 } })
assert_eq(res, :commit, "second commit yields :commit")
d = ext.pending_damage
assert_eq(d[:x], 10, "merged damage x")
assert_eq(d[:y], 10, "merged damage y")
assert_eq(d[:w], 120, "merged damage w spans union")
assert_eq(d[:h], 120, "merged damage h spans union")

# ---- clip damage to surface bounds ----------------------------------------
ext.clear_damage
ext.merge_damage({ x: -5, y: -5, w: 1000, h: 1000 })
cd = ext.clipped_damage
assert_eq(cd[:x], 0, "clip x to 0")
assert_eq(cd[:y], 0, "clip y to 0")
assert_eq(cd[:w], 200, "clip w to surface w")
assert_eq(cd[:h], 150, "clip h to surface h")

# ---- handle_client_message :title + :closed -------------------------------
res = wm2.handle_client_message({ type: "set_title", window_id: ext.id, title: "new" })
assert_eq(res, :title, "set_title yields :title")
assert_eq(ext.title, "new", "title updated")
res = wm2.handle_client_message({ type: "request_close", window_id: ext.id })
assert_eq(res, :closed, "request_close yields :closed")
assert_eq(wm2.windows.length, 0, "window removed on close")

# ---- handle_client_message :ignored paths ---------------------------------
res = wm2.handle_client_message({ type: "commit", window_id: 999, damage: {} })
assert_eq(res, :ignored, "commit on unknown id is ignored")
res = wm2.handle_client_message({ type: "set_title", window_id: 999, title: "x" })
assert_eq(res, :ignored, "set_title on unknown id is ignored")
res = wm2.handle_client_message({ type: "request_close", window_id: 999 })
assert_eq(res, :ignored, "close on unknown id is ignored")
res = wm2.handle_client_message({ type: "request_resize", window_id: 1, w: 1, h: 1 })
assert_eq(res, :ignored, "request_resize is reserved → :ignored")
res = wm2.handle_client_message({ type: "what", window_id: 1 })
assert_eq(res, :ignored, "unknown type is :ignored")

# ---- ExternalWindow geometry inherits from Window -------------------------
wm3 = WindowManager.new
ew = wm3.register_external("g", 90, 60)
assert_eq(ew.w, 90, "MIN_W honoured (= 90)")
assert_eq(ew.h, 60, "MIN_H honoured (= 60)")
# Below-min request gets clamped.
ew2 = wm3.register_external("tiny", 10, 10)
assert_eq(ew2.w, Theme::MIN_W, "clamp below MIN_W")
assert_eq(ew2.h, Theme::MIN_H, "clamp below MIN_H")

# ---- native_w/native_h frozen at construction; resize_to drifts @w/@h -----
# The SAB is sized once at hello-time; the user dragging the resize grip must
# NOT shrink/grow the SAB-backed surface (the protocol has no resize
# handshake yet). The compositor scale-fits the native surface into @w/@h.
wm3r = WindowManager.new
ewr = wm3r.register_external("resize-me", 320, 240)
assert_eq(ewr.native_w, 320, "native_w captured at construction")
assert_eq(ewr.native_h, 240, "native_h captured at construction")
ewr.resize_to(800, 600)
assert_eq(ewr.w, 800, "resize_to grew @w")
assert_eq(ewr.h, 600, "resize_to grew @h")
assert_eq(ewr.native_w, 320, "native_w preserved across resize_to (SAB stays)")
assert_eq(ewr.native_h, 240, "native_h preserved across resize_to (SAB stays)")
# clipped_damage must clip to NATIVE bounds (SAB extent), not to @w/@h --
# otherwise a window grown larger would tell the blit to read past the SAB
# end and decode garbage.
ewr.clear_damage
ewr.merge_damage({ x: 0, y: 0, w: 9999, h: 9999 })
cdr = ewr.clipped_damage
assert_eq(cdr[:w], 320, "clipped_damage w clipped to native_w, not @w")
assert_eq(cdr[:h], 240, "clipped_damage h clipped to native_h, not @h")
# Shrinking below native should keep the SAB intact too (the visible image
# scales DOWN; the SAB never deallocates pixels).
ewr.resize_to(160, 120)
assert_eq(ewr.native_w, 320, "native_w preserved across shrink resize_to")
assert_eq(ewr.native_h, 240, "native_h preserved across shrink resize_to")

# ---- translate_input surface-local coordinates ----------------------------
ew.move_to(50, 80)
payload = wm3.translate_input(ew, :mousedown, 70, 100, button: 0)
assert_eq(payload[:kind], :mousedown, "translate_input kind")
assert_eq(payload[:x], 20, "translate_input x = screen_x - win.x")
assert_eq(payload[:y], 20, "translate_input y = screen_y - win.y")
assert_eq(payload[:button], 0, "translate_input button forwarded")

# ---- last_messages bounded to 16 ------------------------------------------
wm4 = WindowManager.new
20.times { |i| wm4.handle_client_message({ type: "hello", title: "x#{i}", w: 100, h: 100 }) }
assert(wm4.last_messages.length <= 16, "last_messages bounded to 16")

# ---- panel role: hello with role "panel" ----------------------------------
wmp = WindowManager.new
res = wmp.handle_client_message({ type: "hello", title: "wasmdock", role: "panel",
                                  w: 480, h: 120, sab: :sab, stride: 1920 })
assert_eq(res, :welcome, "panel hello yields :welcome")
panel = wmp.last_registered
assert(panel.panel?, "panel window reports panel?")
assert(panel.external?, "panel is external")
# A panel is never focused and is excluded from the focus ring.
assert(wmp.focused.nil?, "panel does not become focused")
assert_eq(wmp.panels.length, 1, "one panel tracked")
assert_eq(wmp.normal_windows.length, 0, "panel not counted as a normal window")
# A normal window registered after the panel must NOT raise above it: panels
# are always the top stratum in ordered_windows.
nrm = wmp.register_external("app", 200, 150)
assert(!nrm.panel?, "normal window is not a panel")
ord = wmp.ordered_windows
assert(ord.last.panel?, "panel is drawn last (always-on-top)")
assert(!ord.first.panel?, "normal window drawn before the panel")
# Anchoring: bottom-center of a 1000x800 desktop.
wmp.anchor_panel(panel, 1000, 800)
assert_eq(panel.x, (1000 - panel.w) / 2, "panel x centered")
assert_eq(panel.y, 800 - panel.h, "panel y flush to bottom")
# A panel carries no decoration: all three decoration hit-tests are no-hit and
# the frame equals the body.
assert(!panel.on_titlebar?(panel.x + 5, panel.y + 2), "panel titlebar not hittable")
assert(!panel.on_close?(panel.x + panel.w - 5, panel.y + 2), "panel close not hittable")
assert(!panel.on_resize?(panel.x + panel.w - 2, panel.y + panel.h - 2), "panel resize not hittable")
fr = panel.frame_rect
br = panel.body_rect
assert_eq(fr, br, "panel frame_rect equals body_rect")
# Cycle ignores the panel: with only the panel + one normal window, the normal
# stays focused (a single normal window cannot cycle).
wmp.cycle
assert(wmp.focused.equal?(nrm), "cycle keeps the only normal window focused")

# ---- popup role: hello "popup" + parent-relative placement + grab ----------
wmpp = WindowManager.new
parent = wmpp.register_external("editor", 300, 200)   # a normal, decorated window
parent.move_to(100, 80)
res = wmpp.handle_client_message({ type: "hello", title: "menu", role: "popup",
                                   parent: parent.id, rel_x: 20, rel_y: 30,
                                   w: 40, h: 24, sab: :psab, stride: 160 })
assert_eq(res, :welcome, "popup hello yields :welcome")
pop = wmpp.last_registered
assert(pop.popup?, "popup window reports popup?")
assert(!pop.decorated?, "popup is undecorated")
assert(pop.external?, "popup is external")
# Anchored at the parent body origin + (rel_x, rel_y), and NOT MIN-clamped (a
# 40x24 menu would be clamped up for a normal window, but a popup keeps it).
assert_eq(pop.x, 120, "popup x = parent.x + rel_x (100+20)")
assert_eq(pop.y, 110, "popup y = parent.y + rel_y (80+30)")
assert_eq(pop.w, 40, "popup keeps requested w (no MIN clamp)")
assert_eq(pop.h, 24, "popup keeps requested h (no MIN clamp)")
assert_eq(pop.parent_id, parent.id, "popup remembers its parent_id")
# No decoration (same as a panel): every decoration hit-test is no-hit and the
# frame equals the body.
assert(!pop.on_titlebar?(pop.x + 5, pop.y + 2), "popup titlebar not hittable")
assert(!pop.on_close?(pop.x + pop.w - 5, pop.y + 2), "popup close not hittable")
assert_eq(pop.frame_rect, pop.body_rect, "popup frame_rect equals body_rect")
# Excluded from the focus ring: the parent stays the focused window.
assert(wmpp.focused.equal?(parent), "popup does not steal focus from its parent")
assert_eq(wmpp.popups.length, 1, "one popup tracked")
assert_eq(wmpp.child_popups(parent.id).length, 1, "child_popups finds it by parent_id")
# Stacks above its parent (newest non-panel on top).
assert(wmpp.ordered_windows.last.equal?(pop), "popup drawn last (above its parent)")
# Closing the parent orphans + unmaps the popup.
wmpp.close(parent)
assert_eq(wmpp.popups.length, 0, "closing the parent unmaps its popup")
assert_eq(wmpp.windows.length, 0, "no windows remain after parent close")

# ---- nested popups: a popup parented to another popup (submenu) ------------
wmn = WindowManager.new
base = wmn.register_external("editor", 300, 200); base.move_to(50, 40)
wmn.handle_client_message({ type: "hello", title: "menu", role: "popup",
                            parent: base.id, rel_x: 10, rel_y: 10, w: 80, h: 60, sab: :s1 })
p1 = wmn.last_registered
assert_eq(p1.x, 60, "level-1 popup x = window.x + rel (50+10)")
# A submenu anchored to the FIRST popup, not the window.
wmn.handle_client_message({ type: "hello", title: "submenu", role: "popup",
                            parent: p1.id, rel_x: 70, rel_y: 5, w: 80, h: 50, sab: :s2 })
p2 = wmn.last_registered
assert(p2.popup?, "level-2 surface is a popup")
assert_eq(p2.parent_id, p1.id, "submenu's parent is the level-1 popup")
assert_eq(p2.x, p1.x + 70, "submenu x = parent popup.x + rel_x")
assert_eq(p2.y, p1.y + 5, "submenu y = parent popup.y + rel_y")
# popups is bottom-to-top: the submenu stacks above its parent popup.
pops = wmn.popups
assert_eq(pops.length, 2, "two popups tracked")
assert(pops[0].equal?(p1) && pops[1].equal?(p2), "popups bottom-to-top (p1 below its child p2)")
assert_eq(wmn.child_popups(p1.id).length, 1, "child_popups(p1) finds the submenu")
# Keyboard grab policy: an open popup is the key_target (top-most first),
# overriding the focused window beneath.
assert(wmn.key_target.equal?(p2), "key_target is the top-most popup while popups are open")
# Closing the level-1 popup orphans + unmaps its submenu too.
wmn.close(p1)
assert_eq(wmn.popups.length, 0, "closing a popup unmaps its child submenu")
assert(!wmn.find(base.id).nil?, "the parent window itself is untouched")
# With no popups left, key_target falls back to the focused window.
assert(wmn.key_target.equal?(base), "key_target falls back to the focused window when no popups")

# ---- launch registry: known id -> :launch, unknown id -> :ignored ---------
wml = WindowManager.new
assert_eq(wml.handle_client_message({ type: "launch", app: "terminal" }), :launch,
          "known launch id yields :launch")
assert_eq(wml.handle_client_message({ type: "launch", app: "editor" }), :launch,
          "editor is launchable")
assert_eq(wml.handle_client_message({ type: "launch", app: "files" }), :launch,
          "files is launchable")
assert_eq(wml.handle_client_message({ type: "launch", app: "rm -rf /" }), :ignored,
          "unknown launch id is dropped")
assert_eq(wml.handle_client_message({ type: "launch" }), :ignored,
          "missing app is dropped")
assert(!wml.launchable_url("terminal").nil?, "terminal maps to a worker url")
assert(wml.launchable_url("nope").nil?, "unknown id has no url")
# A launch never spawns a window itself (the Compositor does, JS-side).
assert_eq(wml.windows.length, 0, "launch dispatch creates no window in the WM")
# terminal + files must map to their own dedicated workers (recognizable
# titles + distinct surfaces), not to the generic hello placeholder.
assert_eq(wml.launchable_url("terminal"), "clients/terminal/worker.js",
          "terminal maps to its dedicated worker")
assert_eq(wml.launchable_url("files"), "clients/files/worker.js",
          "files maps to its dedicated worker")

# ---- launch registry: OCI descriptor shape (hash with :oci key) -----------
# The "hello-oci" entry is a {oci: "hello:latest"} hash. handle_client_message
# treats it as launchable (regardless of descriptor shape); launchable_url
# returns nil for the hash shape and launchable_oci returns the ref. The
# Compositor's :launch arm dispatches on which of the two is non-nil.
assert(wml.launchable?("hello-oci"), "hello-oci is launchable (hash shape)")
assert_eq(wml.handle_client_message({ type: "launch", app: "hello-oci" }), :launch,
          "OCI-shape launch id yields :launch")
assert(wml.launchable_url("hello-oci").nil?,
       "launchable_url returns nil for the OCI-shape descriptor")
assert_eq(wml.launchable_oci("hello-oci"), "hello:latest",
          "launchable_oci returns the ref string")
# Conversely, a static-path descriptor must not surface as an OCI ref.
assert(wml.launchable_oci("terminal").nil?,
       "launchable_oci returns nil for a static-path descriptor")
# Unknown ids: every probe returns nil/false.
assert(!wml.launchable?("nope"), "unknown id is not launchable")
assert(wml.launchable_url("nope").nil?, "unknown id has no static url")
assert(wml.launchable_oci("nope").nil?, "unknown id has no OCI ref")

# ---- minimize: geometry -----------------------------------------------
wmin = WindowManager.new
mw = wmin.spawn("min-test", 200, 120)
# minimize_rect sits just left of close_rect at the same vertical pad.
crect = mw.close_rect
mrect = mw.minimize_rect
assert_eq(mrect[2], Theme::MIN_SZ, "minimize_rect width = MIN_SZ")
assert_eq(mrect[3], Theme::MIN_SZ, "minimize_rect height = MIN_SZ")
assert_eq(mrect[1], crect[1], "minimize_rect y = close_rect y (same row)")
pad = (Theme::TITLE_H - Theme::MIN_SZ) / 2
assert_eq(mrect[0], crect[0] - Theme::MIN_SZ - pad, "minimize_rect x sits left of close_rect")
# on_minimize? hit-test responds true inside, false outside.
mid_x = mrect[0] + mrect[2]/2
mid_y = mrect[1] + mrect[3]/2
assert(mw.on_minimize?(mid_x, mid_y), "on_minimize? hits center of box")
assert(!mw.on_minimize?(mid_x - mrect[2], mid_y), "on_minimize? misses outside box")
# A panel never reports on_minimize?.
pn = wmin.register_external("panel", 480, 28, "panel")
assert(!pn.on_minimize?(pn.x + 2, pn.y + 2), "panel never hits on_minimize?")

# ---- minimize: state transitions --------------------------------------
wmin2 = WindowManager.new
a = wmin2.spawn("a")
b = wmin2.spawn("b")
assert(!a.minimized?, "fresh window not minimized")
assert(wmin2.focused.equal?(b), "b is focused before minimize")
res = wmin2.minimize(b)
assert(!res.nil?, "minimize returns the window on a real transition")
assert(b.minimized?, "minimized flag flipped")
assert(!b.focused?, "minimized window loses focus")
assert(wmin2.focused.equal?(a), "focus moves to next normal non-minimized window")
# minimize is idempotent.
res2 = wmin2.minimize(b)
assert(res2.nil?, "second minimize is a no-op")
# Minimized windows tracked + windows_snapshot reports them with the new shape.
assert_eq(wmin2.minimized_windows.length, 1, "1 minimized window tracked")
snap = wmin2.windows_snapshot
# Snapshot returns ALL non-panel windows, not just minimized ones, so both
# a (open) and b (minimized) appear. Stack order is bottom-to-top: a, b.
assert_eq(snap.length, 2, "windows_snapshot length includes open + minimized")
assert_eq(snap[0][:id], a.id, "windows_snapshot[0] is a")
assert_eq(snap[0][:title], "a", "windows_snapshot[0].title")
assert_eq(snap[0][:minimized], false, "windows_snapshot[0].minimized = false (a is open)")
assert_eq(snap[0][:focused], true, "windows_snapshot[0].focused = true (a took focus on minimize)")
assert_eq(snap[0][:role], "window", "windows_snapshot[0].role = window")
assert_eq(snap[1][:id], b.id, "windows_snapshot[1] is b")
assert_eq(snap[1][:title], "b", "windows_snapshot[1].title")
assert_eq(snap[1][:minimized], true, "windows_snapshot[1].minimized = true (b is folded)")
assert_eq(snap[1][:focused], false, "windows_snapshot[1].focused = false")
# A minimized window is excluded from cycle.
wmin2.cycle
assert(wmin2.focused.equal?(a), "cycle skips minimized window with only one normal left")
# Restore puts it back at the top with focus.
res3 = wmin2.restore_window(b)
assert(!res3.nil?, "restore_window returns the window on a real transition")
assert(!b.minimized?, "minimized cleared on restore")
assert(wmin2.focused.equal?(b), "restored window is focused")
res4 = wmin2.restore_window(b)
assert(res4.nil?, "restore on a non-minimized window is a no-op")
# Minimizing a panel is a no-op.
wmin2.register_external("dock", 480, 28, "panel")
panel = wmin2.last_registered
assert(wmin2.minimize(panel).nil?, "minimize on panel is a no-op")
assert(!panel.minimized?, "panel never gets the minimized flag")

# ---- shade ("roll up"): state + geometry ------------------------------
wsh = WindowManager.new
sw = wsh.spawn("shademe", 200, 150)
assert(!sw.shaded?, "fresh window not shaded")
# Full frame extent = body + titlebar before shading.
fr_before = sw.frame_rect
assert_eq(fr_before[3], sw.h + Theme::TITLE_H, "unshaded frame height = body + titlebar")
res = wsh.shade(sw)
assert(!res.nil?, "shade returns the window on a real transition")
assert(sw.shaded?, "shaded flag flipped")
# Shaded: the frame collapses to JUST the titlebar (body rolled away).
fr_after = sw.frame_rect
assert_eq(fr_after[3], Theme::TITLE_H, "shaded frame height = titlebar only")
assert_eq(fr_after[1], sw.frame_top, "shaded frame top still at the titlebar top")
# A click in the old BODY area no longer hits the window (falls through).
body_mid_y = sw.y + sw.h/2
assert(!sw.contains?(sw.x + 10, body_mid_y), "shaded window: old body area no longer hit-tested")
assert(sw.contains?(sw.x + 10, sw.frame_top + 2), "shaded window: titlebar still hit-tested")
# Resize is disabled while shaded (no body / grip).
assert(!sw.on_resize?(sw.right - 2, sw.bottom - 2), "shaded window not resizable")
# Titlebar gestures still work (drag/close reachable).
assert(sw.on_titlebar?(sw.x + 10, sw.frame_top + 2), "shaded window titlebar still draggable")
# shade is idempotent; unshade restores the full frame.
assert(wsh.shade(sw).nil?, "second shade is a no-op")
assert(!wsh.unshade(sw).nil?, "unshade returns the window on a real transition")
assert(!sw.shaded?, "shaded cleared on unshade")
assert_eq(sw.frame_rect[3], sw.h + Theme::TITLE_H, "unshaded frame height restored")
assert(wsh.unshade(sw).nil?, "unshade on a non-shaded window is a no-op")
# Panels never shade.
wsh.register_external("dock", 480, 28, "panel")
assert(wsh.shade(wsh.last_registered).nil?, "shade on a panel is a no-op")

# ---- minimized window: render-loop skip + click hit-test --------------
# A minimized window must be excluded from window_at so a click at its
# former coordinates does not surface it instead of the desktop.
wmin3 = WindowManager.new
c = wmin3.spawn("c", 200, 120)
cx = c.x + c.w/2
cy = c.y + c.h/2
assert(wmin3.window_at(cx, cy).equal?(c), "window_at finds the window pre-minimize")
wmin3.minimize(c)
assert(wmin3.window_at(cx, cy).nil?, "window_at skips a minimized window")
# Restore re-exposes it.
wmin3.restore_window(c)
assert(wmin3.window_at(cx, cy).equal?(c), "window_at finds the restored window again")

# ---- restore wire message ----------------------------------------------
wmin4 = WindowManager.new
d = wmin4.spawn("d", 200, 120)
wmin4.minimize(d)
res = wmin4.handle_client_message({ type: "restore", window_id: d.id })
assert_eq(res, :restored, "restore message yields :restored")
assert(!d.minimized?, "restore message cleared the flag")
res = wmin4.handle_client_message({ type: "restore", window_id: 999 })
assert_eq(res, :ignored, "restore on unknown id is :ignored")
res = wmin4.handle_client_message({ type: "restore", window_id: d.id })
assert_eq(res, :ignored, "restore on a non-minimized window is :ignored")

# ---- focus wire message -----------------------------------------------
wmf = WindowManager.new
fa = wmf.spawn("fa")
fb = wmf.spawn("fb")
assert(wmf.focused.equal?(fb), "fb is focused before focus wire")
# Focus fa via the wire message — moves it to the top of the stack.
res = wmf.handle_client_message({ type: "focus", window_id: fa.id })
assert_eq(res, :focused, "focus message yields :focused")
assert(wmf.focused.equal?(fa), "focus wire raised + focused fa")
# Focusing a MINIMIZED window restores it (Fluxbox semantics).
wmf.minimize(fa) # fa now minimized; fb takes focus
assert(fa.minimized?, "fa minimized for focus-restore test")
assert(wmf.focused.equal?(fb), "fb focused while fa is minimized")
res = wmf.handle_client_message({ type: "focus", window_id: fa.id })
assert_eq(res, :focused, "focus on a minimized window yields :focused")
assert(!fa.minimized?, "focus wire restored the minimized window")
assert(wmf.focused.equal?(fa), "focus wire raised + focused the restored window")
# Unknown id is ignored.
res = wmf.handle_client_message({ type: "focus", window_id: 999 })
assert_eq(res, :ignored, "focus on unknown id is :ignored")
# Focus on a panel is ignored.
wmf.register_external("dock", 480, 28, "panel")
pan = wmf.last_registered
res = wmf.handle_client_message({ type: "focus", window_id: pan.id })
assert_eq(res, :ignored, "focus on panel id is :ignored")

# ---- close wire message -----------------------------------------------
wmc = WindowManager.new
ca = wmc.spawn("ca")
cb = wmc.spawn("cb")
assert_eq(wmc.windows.length, 2, "2 windows before close wire")
res = wmc.handle_client_message({ type: "close", window_id: ca.id })
assert_eq(res, :closed_by_peer, "close message yields :closed_by_peer")
assert_eq(wmc.windows.length, 1, "1 window left after close wire")
# The closed window's worker ref is stashed for the route layer to pick up.
stash = wmc.take_last_closed_by_peer
assert(!stash.nil?, "take_last_closed_by_peer returns the stash")
assert_eq(stash[:window_id], ca.id, "stash window_id matches")
# Stash is cleared on read.
assert(wmc.take_last_closed_by_peer.nil?, "stash cleared after first read")
# Unknown id ignored.
res = wmc.handle_client_message({ type: "close", window_id: 999 })
assert_eq(res, :ignored, "close on unknown id is :ignored")
# Panel close ignored.
wmc.register_external("dock", 480, 28, "panel")
panc = wmc.last_registered
res = wmc.handle_client_message({ type: "close", window_id: panc.id })
assert_eq(res, :ignored, "close on panel id is :ignored")

# ---- windows_snapshot includes focus + role on every entry -----------
wms = WindowManager.new
sa = wms.spawn("sa")
sb = wms.spawn("sb")
sc = wms.spawn("sc")
# Stack order = creation order: sa, sb, sc. Top (sc) is focused.
snap = wms.windows_snapshot
assert_eq(snap.length, 3, "windows_snapshot has 3 entries")
assert_eq(snap[0][:focused], false, "snapshot[0] not focused")
assert_eq(snap[1][:focused], false, "snapshot[1] not focused")
assert_eq(snap[2][:focused], true, "snapshot[2] (top of stack) focused")
# Raise sa via focus — focus indicator must move to sa.
wms.focus(sa)
snap = wms.windows_snapshot
# Stack order after focus(sa): sb, sc, sa (sa pushed to top).
assert_eq(snap[2][:id], sa.id, "snapshot[2] is sa after focus")
assert_eq(snap[2][:focused], true, "snapshot[2] is now focused")
# Panels are excluded.
wms.register_external("dock", 480, 28, "panel")
snap = wms.windows_snapshot
assert_eq(snap.length, 3, "panels excluded from windows_snapshot")

# ---- workspaces: defaults + WORKSPACE_COUNT ---------------------------
wmw = WindowManager.new
assert_eq(wmw.active_workspace, 1, "default active workspace = 1")
assert_eq(wmw.workspace_count, 4, "workspace_count = 4 (Fluxbox default)")
assert_eq(WindowManager::WORKSPACE_COUNT, 4, "WORKSPACE_COUNT constant = 4")

# ---- workspaces: spawned window inherits active workspace ------------
wmw2 = WindowManager.new
sw1 = wmw2.spawn("on-1")
assert_eq(sw1.workspace, 1, "spawn on workspace 1 by default")
wmw2.set_workspace(2)
sw2 = wmw2.spawn("on-2")
assert_eq(sw2.workspace, 2, "spawn inherits active workspace after switch")
assert_eq(sw1.workspace, 1, "previously-spawned window stays on workspace 1")
# A panel is workspace-agnostic (sentinel 0): it must always be visible.
wmw2.register_external("dock-ws", 480, 28, "panel")
panx = wmw2.last_registered
assert_eq(panx.workspace, 0, "panel uses workspace 0 sentinel (always visible)")
# A normal external window inherits the active workspace at register time.
exwx = wmw2.register_external("ext-on-2", 200, 150)
assert_eq(exwx.workspace, 2, "external normal window inherits active workspace")

# ---- workspaces: set_workspace state transitions ---------------------
wmw3 = WindowManager.new
assert_eq(wmw3.set_workspace(1), nil, "set_workspace to current is a no-op (nil)")
assert_eq(wmw3.set_workspace(0), nil, "set_workspace below range is rejected")
assert_eq(wmw3.set_workspace(5), nil, "set_workspace above WORKSPACE_COUNT is rejected")
assert_eq(wmw3.set_workspace("2"), nil, "set_workspace requires Integer (string rejected)")
assert_eq(wmw3.set_workspace(3), 3, "set_workspace to 3 succeeds")
assert_eq(wmw3.active_workspace, 3, "active_workspace == 3 after switch")
assert_eq(wmw3.set_workspace(1), 1, "set_workspace back to 1 succeeds")

# ---- workspaces: focused tracks active workspace ----------------------
wmw4 = WindowManager.new
fa = wmw4.spawn("a-on-1") # workspace 1, focused
wmw4.set_workspace(2)
# After switching to an empty workspace 2, focus is nil (no window there).
assert(wmw4.focused.nil?, "focused is nil on empty workspace")
assert(!fa.focused?, "previously-focused window loses focus on workspace switch")
fb = wmw4.spawn("b-on-2") # spawn on workspace 2, focused
assert(wmw4.focused.equal?(fb), "focus is b on workspace 2")
assert(fb.focused?, "b carries focused? = true")
wmw4.set_workspace(1)
assert(wmw4.focused.equal?(fa), "focus returns to a when switching back to workspace 1")
assert(fa.focused?, "a re-acquires focus on workspace 1")
assert(!fb.focused?, "b loses focus on workspace 2 (out of view)")

# ---- workspaces: windows_snapshot filters to active workspace --------
wmw5 = WindowManager.new
s1 = wmw5.spawn("ws1-a")
s2 = wmw5.spawn("ws1-b")
wmw5.set_workspace(2)
s3 = wmw5.spawn("ws2-a")
# active = 2: snapshot must show s3 only.
snap = wmw5.windows_snapshot
assert_eq(snap.length, 1, "snapshot on workspace 2 has 1 entry")
assert_eq(snap[0][:id], s3.id, "snapshot[0] is s3")
assert_eq(snap[0][:workspace], 2, "snapshot[0].workspace = 2")
# Switch back: snapshot must show s1 + s2 only.
wmw5.set_workspace(1)
snap = wmw5.windows_snapshot
assert_eq(snap.length, 2, "snapshot on workspace 1 has 2 entries")
assert_eq(snap[0][:workspace], 1, "snapshot[0].workspace = 1")
assert_eq(snap[1][:workspace], 1, "snapshot[1].workspace = 1")
# windows_on_workspace helper
assert_eq(wmw5.windows_on_workspace(1).length, 2, "windows_on_workspace(1) = 2")
assert_eq(wmw5.windows_on_workspace(2).length, 1, "windows_on_workspace(2) = 1")
assert_eq(wmw5.windows_on_workspace(3).length, 0, "windows_on_workspace(3) = 0 (empty)")

# ---- workspaces: set_workspace wire arm -------------------------------
wmw6 = WindowManager.new
res = wmw6.handle_client_message({ type: "set_workspace", index: 2 })
assert_eq(res, :workspace_changed, "set_workspace wire yields :workspace_changed")
assert_eq(wmw6.active_workspace, 2, "active_workspace updated via wire")
res = wmw6.handle_client_message({ type: "set_workspace", index: 2 })
assert_eq(res, :ignored, "re-setting current workspace is :ignored")
res = wmw6.handle_client_message({ type: "set_workspace", index: 99 })
assert_eq(res, :ignored, "out-of-range workspace is :ignored")
res = wmw6.handle_client_message({ type: "set_workspace", index: 0 })
assert_eq(res, :ignored, "workspace 0 is :ignored (1-indexed)")
res = wmw6.handle_client_message({ type: "set_workspace" })
assert_eq(res, :ignored, "missing index is :ignored")
res = wmw6.handle_client_message({ type: "move_window", window_id: 1, workspace: 2 })
assert_eq(res, :ignored, "move_window reserved -> :ignored")

# ---- workspaces: window_at filters by workspace -----------------------
wmw7 = WindowManager.new
wa = wmw7.spawn("wa", 200, 120)
cx = wa.x + wa.w/2
cy = wa.y + wa.h/2
assert(wmw7.window_at(cx, cy).equal?(wa), "window_at finds wa on workspace 1")
wmw7.set_workspace(2)
assert(wmw7.window_at(cx, cy).nil?, "window_at skips wa when on workspace 2 (wa lives on 1)")
wmw7.set_workspace(1)
assert(wmw7.window_at(cx, cy).equal?(wa), "window_at finds wa again on workspace 1")

# ---- workspaces: cycle stays within active workspace ------------------
wmw8 = WindowManager.new
ca1 = wmw8.spawn("ca1") # ws 1
cb1 = wmw8.spawn("cb1") # ws 1
wmw8.set_workspace(2)
cc2 = wmw8.spawn("cc2") # ws 2 (single window, cycle no-op)
assert_eq(wmw8.cycle, nil, "cycle with <2 windows on active workspace is a no-op")
wmw8.set_workspace(1)
wmw8.cycle
assert(wmw8.focused.equal?(ca1), "cycle moves focus among workspace-1 windows only")

# ---- workspaces: minimize re-focuses within active workspace ---------
wmw9 = WindowManager.new
ma = wmw9.spawn("ma") # ws 1
mb = wmw9.spawn("mb") # ws 1, focused
wmw9.set_workspace(2)
mc = wmw9.spawn("mc") # ws 2, focused
wmw9.minimize(mc)
# On workspace 2 there is no other normal non-minimized window — focused is nil.
assert(wmw9.focused.nil?, "minimize on a workspace with only one window leaves no focus")
# Switching back to ws 1 brings mb's focus back.
wmw9.set_workspace(1)
assert(wmw9.focused.equal?(mb), "ws 1 still has mb focused after the round trip")

# ---- Menu: shape + hit_test + entry_top ------------------------------
flat = Menu.new([
  { label: "Raise", action: [:focus, 42] },
  { label: "Close", action: [:close, 42] },
])
assert_eq(flat.entries.length, 2, "flat menu has 2 entries")
assert_eq(flat.height, 2 * Menu::ITEM_H, "flat menu height = 2 * ITEM_H")
# hit_test inside / outside.
assert_eq(flat.hit_test(0, 0, 10, 5), 0, "row 0 hit (inside first row)")
assert_eq(flat.hit_test(0, 0, 10, Menu::ITEM_H + 5), 1, "row 1 hit (inside second row)")
assert_eq(flat.hit_test(0, 0, 10, -1), -1, "above the menu is -1")
assert_eq(flat.hit_test(0, 0, 10, 2 * Menu::ITEM_H + 1), -1, "below the menu is -1")
assert_eq(flat.hit_test(0, 0, -1, 5), -1, "left of menu is -1")
assert_eq(flat.hit_test(0, 0, Menu::WIDTH, 5), -1, "right of menu is -1 (half-open)")
assert_eq(flat.hit_test(100, 200, 110, 200 + Menu::ITEM_H + 1), 1,
          "hit_test honours pop-up origin")
# entry_top tracks cumulative row offsets.
assert_eq(flat.entry_top(0, 0), 0, "entry_top(0)")
assert_eq(flat.entry_top(0, 1), Menu::ITEM_H, "entry_top(1) = ITEM_H")
assert_eq(flat.entry_top(50, 1), 50 + Menu::ITEM_H, "entry_top honours y origin")

# ---- Menu: separator ------------------------------------------------
withsep = Menu.new([
  { label: "A", action: [:noop, "a"] },
  { separator: true },
  { label: "B", action: [:noop, "b"] },
])
assert_eq(withsep.height, 2 * Menu::ITEM_H + Menu::SEP_H,
          "separator counted as SEP_H not ITEM_H")
# Hit_test on the separator returns -1 (not selectable); hit on the next row
# correctly maps to entry index 2 (not 1) because separators consume index slots.
assert_eq(withsep.hit_test(0, 0, 10, Menu::ITEM_H + 1), -1,
          "hit on separator row is -1")
assert_eq(withsep.hit_test(0, 0, 10, Menu::ITEM_H + Menu::SEP_H + 1), 2,
          "row after separator is index 2")

# ---- RootMenu.build: top-level shape --------------------------------
wmr = WindowManager.new
root = RootMenu.build(wmr)
assert(root.is_a?(Menu), "RootMenu.build returns a Menu")
labels = root.entries.map { |e| e[:label] }
assert_eq(labels[0], "Applications", "top-level[0] = Applications")
assert_eq(labels[1], "Workspaces",   "top-level[1] = Workspaces")
assert_eq(labels[2], "Theme",        "top-level[2] = Theme")
# Top-level row 3 is the separator (no label).
assert_eq(root.entries[3][:separator], true, "top-level[3] is a separator")
assert_eq(labels[4], "About wasmbox", "top-level[4] = About wasmbox")
assert_eq(labels[5], "Reload",        "top-level[5] = Reload")
assert_eq(labels[6], "Exit",          "top-level[6] = Exit")
# Applications + Workspaces + Theme carry a submenu.
assert(root.entries[0][:submenu].is_a?(Menu), "Applications carries a submenu")
assert(root.entries[1][:submenu].is_a?(Menu), "Workspaces carries a submenu")
assert(root.entries[2][:submenu].is_a?(Menu), "Theme carries a submenu")
# About/Reload/Exit each carry a :noop action (dismiss-only in v0).
assert_eq(root.entries[4][:action][0], :noop, "About is a :noop action")
assert_eq(root.entries[5][:action][0], :noop, "Reload is a :noop action")
assert_eq(root.entries[6][:action][0], :noop, "Exit is a :noop action")

# ---- RootMenu.build: Applications submenu lists LAUNCHABLE -----------
apps = root.entries[0][:submenu]
app_actions = apps.entries.map { |e| e[:action] }
# Every Applications entry is a [:launch, "<id>"] tuple — no separators here.
apps.entries.each do |e|
  assert(e.has_key?(:action), "every Applications entry has an action")
  assert_eq(e[:action][0], :launch, "every Applications action is :launch")
  assert(wmr.launchable?(e[:action][1]), "every Applications id is in LAUNCHABLE")
end
# Specific labels we curate are present.
app_labels = apps.entries.map { |e| e[:label] }
assert(app_labels.include?("Terminal"), "Applications includes Terminal")
assert(app_labels.include?("Editor"),   "Applications includes Editor")
assert(app_labels.include?("Files"),    "Applications includes Files")
assert(app_labels.include?("Hello (wasm)"), "Applications includes Hello (wasm)")
assert(app_labels.include?("Quake"),    "Applications includes Quake")
# The hidden hello-oci id is NOT exposed (probe-only).
assert(!app_labels.include?("Hello (OCI)"),
       "Applications hides hello-oci (probe-only id)")
# Cardinality matches LAUNCHABLE minus the HIDDEN set.
expected_apps = 0
WindowManager::LAUNCHABLE.each do |id, _desc|
  expected_apps += 1 unless RootMenu::HIDDEN.include?(id)
end
assert_eq(apps.entries.length, expected_apps,
          "Applications has one entry per non-hidden LAUNCHABLE id")

# ---- RootMenu.build: Workspaces submenu --------------------------------
ws = root.entries[1][:submenu]
assert_eq(ws.entries.length, wmr.workspace_count,
          "Workspaces has one entry per workspace")
assert_eq(ws.entries.length, 4, "Workspaces has 4 entries (default WORKSPACE_COUNT)")
ws.entries.each_with_index do |e, i|
  assert_eq(e[:label], "Workspace #{i + 1}", "Workspaces[#{i}].label")
  assert_eq(e[:action][0], :workspace, "Workspaces[#{i}].action[0] = :workspace")
  assert_eq(e[:action][1], i + 1, "Workspaces[#{i}].action[1] = #{i + 1}")
end

# ---- RootMenu: workspaces submenu reflects wm.workspace_count ----------
# RootMenu.build_workspaces is independent of build() so a future change to
# workspace_count is honoured without rebuilding the top-level menu.
ws_direct = RootMenu.build_workspaces(wmr)
assert_eq(ws_direct.entries.length, 4, "build_workspaces honours workspace_count")

# ---- RootMenu: known action tuples are well-formed ---------------------
# Terminal must dispatch a launch of the "terminal" id (the click handler in
# the Compositor reads action[1] and routes through launchable_url, so the
# id MUST match a LAUNCHABLE key).
term_entry = apps.entries.find { |e| e[:label] == "Terminal" }
assert(!term_entry.nil?, "Terminal entry found")
assert_eq(term_entry[:action][1], "terminal", "Terminal action id = 'terminal'")
assert(wmr.launchable?(term_entry[:action][1]), "Terminal action id is launchable")
# Workspace 3 entry: action tuple ends in 3.
ws3_entry = ws.entries.find { |e| e[:label] == "Workspace 3" }
assert(!ws3_entry.nil?, "Workspace 3 entry found")
assert_eq(ws3_entry[:action][1], 3, "Workspace 3 action ws index = 3")

# ---- LAUNCHABLE additions: hello + quake ---------------------------
assert(wmr.launchable?("hello"), "hello is launchable (was added for the root menu)")
assert(wmr.launchable?("quake"), "quake is launchable (was added for the root menu)")
assert_eq(wmr.launchable_url("hello"), "clients/hello/worker.js",
          "hello maps to the bundled hello worker")
assert_eq(wmr.launchable_url("quake"), "clients/quake/worker.js",
          "quake maps to the bundled quake worker")

# ---- Theme machinery: defaults, switch, broadcast contract -----------
wmt = WindowManager.new
assert_eq(wmt.active_theme, "Fluxbox Light", "default active theme")
assert_eq(wmt.theme_names.length, 3, "three bundled themes")
assert(wmt.theme_names.include?("Fluxbox Light"), "Fluxbox Light in registry")
assert(wmt.theme_names.include?("Fluxbox Dark"),  "Fluxbox Dark in registry")
assert(wmt.theme_names.include?("GNOME Adwaita"), "GNOME Adwaita in registry")
# theme_source returns the raw .themerc for known names, nil otherwise.
src = wmt.theme_source("Fluxbox Dark")
assert(!src.nil?, "Fluxbox Dark source present")
assert(src.include?("window.active.title.bg.color:"), "Dark source carries Openbox keys")
assert(wmt.theme_source("nope").nil?, "unknown theme has no source")
# set_theme: unknown name -> nil, already-active -> nil, valid swap -> new name.
assert(wmt.set_theme("nope").nil?, "unknown theme name rejected")
assert(wmt.set_theme("Fluxbox Light").nil?, "already-active theme yields nil")
assert_eq(wmt.set_theme("Fluxbox Dark"), "Fluxbox Dark", "valid switch returns new name")
assert_eq(wmt.active_theme, "Fluxbox Dark", "active_theme updated after switch")
# Bad-type guard: a non-String name does not crash, returns nil.
assert(wmt.set_theme(42).nil?, "non-string name rejected")

# ---- handle_client_message :set_theme arm ----------------------------
wmt2 = WindowManager.new
res = wmt2.handle_client_message({ type: "set_theme", name: "Fluxbox Dark" })
assert_eq(res, :theme_changed, "set_theme -> :theme_changed on a fresh switch")
assert_eq(wmt2.active_theme, "Fluxbox Dark", "set_theme arm updated active_theme")
# Already-active is :ignored.
res2 = wmt2.handle_client_message({ type: "set_theme", name: "Fluxbox Dark" })
assert_eq(res2, :ignored, "set_theme -> :ignored when already active")
# Unknown name is :ignored, active_theme unchanged.
res3 = wmt2.handle_client_message({ type: "set_theme", name: "nope" })
assert_eq(res3, :ignored, "set_theme -> :ignored on unknown name")
assert_eq(wmt2.active_theme, "Fluxbox Dark", "active_theme unchanged after unknown")

# ---- RootMenu.build_themes: shape + active marker --------------------
themes_sub = root.entries[2][:submenu]
assert_eq(themes_sub.entries.length, 3, "Theme submenu has 3 entries")
themes_sub.entries.each do |e|
  assert_eq(e[:action][0], :theme, "Theme entry action[0] = :theme")
  assert(wmt.theme_names.include?(e[:action][1]), "Theme action[1] is a known theme name")
end
# Fluxbox Light is active in wmr (default) so its label is "* Fluxbox Light".
labels_t = themes_sub.entries.map { |e| e[:label] }
assert(labels_t.include?("* Fluxbox Light"), "active theme marked with *")
assert(labels_t.include?("Fluxbox Dark"), "inactive theme has no *")
# After a switch the active marker follows.
wmr_dark = WindowManager.new
wmr_dark.set_theme("GNOME Adwaita")
sub2 = RootMenu.build_themes(wmr_dark)
labels2 = sub2.entries.map { |e| e[:label] }
assert(labels2.include?("* GNOME Adwaita"), "active marker follows active theme")
assert(labels2.include?("Fluxbox Light"), "previously-active theme now unmarked")

# ---- WindowManager#externals: panel + normal in stack order ----------
wme = WindowManager.new
wme.spawn("normal-a", 100, 100)
wme.register_external("dock", 480, 28, "panel")
wme.register_external("client", 200, 150)
exts = wme.externals
# The non-external "normal-a" came from spawn (no worker), so it does NOT
# appear in externals. The two register_external entries DO.
assert_eq(exts.length, 2, "externals counts only register_external windows")
exts.each { |w| assert(w.external?, "every externals entry is external?") }

puts "rbtest: ran all pure-WM assertions"
`
