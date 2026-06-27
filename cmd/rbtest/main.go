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

puts "rbtest: ran all pure-WM assertions"
`
