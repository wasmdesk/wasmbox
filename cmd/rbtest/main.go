// Command rbtest runs the pure-Ruby half of the compositor source (Theme,
// Frame, Window, ExternalWindow, WindowManager, Menu) on the native
// go-embedded-ruby interpreter and asserts the step-B window-manager logic —
// message dispatch, external window registration, damage merging, input
// translation — behaves correctly.
//
// The compositor source is split across compositor/*.rb so each file stays
// under ~1k lines. rbtest loads the pure-WM files (01_theme through 05_menu
// in alphabetic / dependency order) and SKIPS the JS-touching files
// (06_core.rb is the Compositor class which talks to the canvas; 07_boot.rb
// spawns clients). This way the same files that ship inside wasmbox.wasm are
// the files under test — no shadow copy to drift from.
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
	"path/filepath"
	"sort"
	"strings"

	ruby "github.com/go-embedded-ruby/ruby"
)

// pureWMPrefix is the file-name prefix range that contains the pure-WM half
// (off-wasm safe). Files whose name starts ABOVE this prefix touch the JS
// bridge and are excluded. With the 0N_ numeric prefix naming convention,
// "06_" and beyond is JS-touching; "00_".."05_" is pure-WM.
const pureWMUpperExcl = "06_"

func main() {
	dir := flag.String("dir", "compositor", "path to the compositor/ directory containing 0N_*.rb files")
	raw := flag.Bool("raw", false, "load ALL compositor/*.rb (incl. JS-touching files) WITHOUT appending the WM test script (debug aid)")
	flag.Parse()
	pure, err := loadPureWM(*dir, *raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rbtest: load %s: %v\n", *dir, err)
		os.Exit(2)
	}
	var script string
	if *raw {
		script = pure
	} else {
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

// loadPureWM walks dir, picks every .rb file whose name sorts BELOW
// pureWMUpperExcl (so 01_theme through 05_menu, skipping 06_core +
// 07_boot), and concatenates them in alphabetic order — the same order
// main.go's embed.FS loader uses, so the test's view of the program is
// identical to the wasm's view. When raw is true ALL .rb files are loaded
// (debug aid for inspecting the full program).
func loadPureWM(dir string, raw bool) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".rb") {
			continue
		}
		if !raw && n >= pureWMUpperExcl {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return "", err
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	return sb.String(), nil
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

# ---- launcher registry modeled on Desktop Entry (APP_MANIFEST) ------------
# Each launchable id exposes a desktopentry.Entry-shaped Hash. Name/Icon/
# Categories come from the embedded APP_MANIFEST; the Exec-analogue is derived
# from LAUNCHABLE (never stored twice), so it always matches the launch target.
de = wml.desktop_entry("terminal")
assert_eq(de[:type], "Application", "a desktop entry is Type=Application")
assert_eq(de[:name], "Terminal", "Name comes from the manifest")
assert_eq(de[:generic_name], "Terminal Emulator", "GenericName comes from the manifest")
assert_eq(de[:icon], "utilities-terminal", "Icon (themed name) comes from the manifest")
assert_eq(de[:categories], ["System", "TerminalEmulator"], "Categories come from the manifest")
assert_eq(de[:exec], "clients/terminal/worker.js", "Exec-analogue is the LAUNCHABLE worker URL")
# The Exec-analogue tracks each LAUNCHABLE descriptor shape.
assert_eq(wml.desktop_exec("terminal"), "clients/terminal/worker.js", "static descriptor -> worker URL exec")
assert_eq(wml.desktop_exec("hello-oci"), "oci://hello:latest", "OCI descriptor -> oci:// exec")
assert_eq(wml.desktop_entry("vscode")[:exec], "/code-server/", "dom descriptor -> dom URL exec")
assert_eq(wml.desktop_entry("browser")[:categories], ["Network", "WebBrowser"], "browser categories")
# An unknown id has no entry; a launchable id absent from the manifest still
# gets a fallback entry (capitalized Name, empty metadata, real exec).
assert_eq(wml.desktop_entry("nope"), nil, "unknown id has no desktop entry")
# desktop_entries lists one entry per LAUNCHABLE id, in registry order.
entries = wml.desktop_entries
assert_eq(entries.length, WindowManager::LAUNCHABLE.length, "one desktop entry per launchable id")
assert_eq(entries[0][:id], "terminal", "desktop_entries follows LAUNCHABLE insertion order")
entries.each { |e| assert(wml.launchable?(e[:id]), "every desktop entry id is launchable") }

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

# ---- Menu: geometry now comes from the toolkit widget (single source) ------
# The Menu domain model keeps entries / actions / submenu structure, but its ROW
# GEOMETRY (row tops, heights, total height) is queried from the go-widgets
# toolkit menu that actually PAINTS the panel — via Widgets.menu_row_top /
# menu_row_height, applied by Menu#apply_widget_layout(Widgets). That is the SAME
# query the compositor runs when a menu opens (08_menu_widgets.rb #layout_menu),
# so the routing exercised here is exactly the routing that ships. The model no
# longer hardcodes ITEM_H / SEP_H / TOP_PAD / BOT_PAD, so there is no mirror to
# drift — the old "click offset" bug is now structurally impossible.
#
# rbtest runs on native rbgo, whose require "widgets" binds the same
# go-ruby-widgets/widgets adapter (over go-widgets/toolkit) the wasm build uses,
# so a real toolkit menu can be built and measured right here.
require "widgets"

flat = Menu.new([
  { label: "Raise", action: [:focus, 42] },
  { label: "Close", action: [:close, 42] },
])
assert_eq(flat.entries.length, 2, "flat menu has 2 entries")
# Before layout the geometry is unset: nothing resolves, height is 0 (defensive
# — the compositor always lays a menu out before it is used or painted).
assert_eq(flat.height, 0, "unlaid menu reports 0 height")
assert_eq(flat.hit_test(0, 0, 10, 10), -1, "unlaid menu hit_test is -1")
flat.apply_widget_layout(Widgets)

# CONTROL RUN (feedback: validate a new probe vs a known-good control first):
# build the SAME toolkit widget the compositor paints and read its geometry
# DIRECTLY, then assert the toolkit reports the row metrics we expect — 22px
# rows from a 2px top inset. Because the Menu model no longer hardcodes these,
# THIS control is what would catch a toolkit MenuRowH / body-inset change.
ctl = Widgets.menu([
  { "label" => "Raise", "action" => "x" },
  { "label" => "Close", "action" => "x" },
])
Widgets.layout(ctl, Menu::WIDTH, 6 * 64)
assert_eq(Widgets.menu_row_height(ctl, 0), 22, "toolkit paints a 22px row (MenuRowH)")
assert_eq(Widgets.menu_row_top(ctl, 0), 2, "toolkit body starts at a 2px inset")
assert_eq(Widgets.menu_row_top(ctl, 1), 24, "toolkit row 1 top = inset + one 22px row")
# The Menu model's height equals the widget's total (body bottom edge + top
# inset for the matching bottom pad), all widget-derived.
assert_eq(flat.height, Widgets.menu_row_top(ctl, 2) + Widgets.menu_row_top(ctl, 0),
          "Menu#height = widget body bottom edge + top inset")
assert_eq(flat.height, 48, "flat 2-row menu is 2*22 + 4 = 48 px tall")

# Routing: a click at the WIDGET-painted centre of each row resolves to that row
# through the SAME hit_test the compositor uses. Driving the expected centre from
# the widget (not from a Ruby constant) is what makes this a real model<->toolkit
# AGREEMENT check rather than a vacuous self-check.
[0, 1].each do |i|
  cy = Widgets.menu_row_top(ctl, i) + Widgets.menu_row_height(ctl, i) / 2
  assert_eq(flat.hit_test(0, 0, 10, cy), i,
            "widget-painted row #{i} centre resolves to row #{i}")
end
assert_eq(flat.hit_test(0, 0, 10, Widgets.menu_row_top(ctl, 0) - 1), -1,
          "the top inset is not a row")
assert_eq(flat.hit_test(0, 0, 10, flat.height + 1), -1, "below the menu is -1")
assert_eq(flat.hit_test(0, 0, -1, 5), -1, "left of menu is -1")
assert_eq(flat.hit_test(0, 0, Menu::WIDTH, 5), -1, "right of menu is -1 (half-open)")
# hit_test honours the pop-up origin (x, y): shift the menu and the same widget
# row centre shifts with it.
oy = 200
c1 = Widgets.menu_row_top(ctl, 1) + Widgets.menu_row_height(ctl, 1) / 2
assert_eq(flat.hit_test(100, oy, 110, oy + c1), 1, "hit_test honours pop-up origin")

# entry_top tracks the widget's per-row top (used to anchor a sub-menu).
assert_eq(flat.entry_top(0, 0), Widgets.menu_row_top(ctl, 0), "entry_top(0) = widget RowTop(0)")
assert_eq(flat.entry_top(0, 1), Widgets.menu_row_top(ctl, 1), "entry_top(1) = widget RowTop(1)")
assert_eq(flat.entry_top(50, 1), 50 + Widgets.menu_row_top(ctl, 1), "entry_top honours y origin")

# ---- Menu: a DEEP click lands on the PAINTED row (offset-bug regression) ----
# The offset bug grew WORSE further down the menu (it accumulated ~2px/row, so a
# lower row's painted centre fell into the row ABOVE it — the pre-fix 24px/no-pad
# model mis-resolved rows from index ~7 down). Build a DEEP menu, lay it out from
# the widget, and assert EVERY row's widget-painted centre resolves to its OWN
# row. Driving both the layout and the expected centres from the widget is what
# keeps this regression's teeth: if the Ruby routing ever re-hardcoded a metric
# that drifted from the toolkit, a deep row here would resolve to its neighbour.
deep = Menu.new((0...12).map { |k| { label: "item #{k}", action: [:noop, k] } })
deep.apply_widget_layout(Widgets)
dctl = Widgets.menu((0...12).map { |k| { "label" => "item #{k}", "action" => "x" } })
Widgets.layout(dctl, Menu::WIDTH, 16 * 64)
di = 0
while di < 12
  painted_centre = Widgets.menu_row_top(dctl, di) + Widgets.menu_row_height(dctl, di) / 2
  assert_eq(deep.hit_test(0, 0, 10, painted_centre), di,
            "deep menu: widget-painted row #{di} centre resolves to its own row")
  di += 1
end

# ---- Menu: separator ------------------------------------------------
withsep = Menu.new([
  { label: "A", action: [:noop, "a"] },
  { separator: true },
  { label: "B", action: [:noop, "b"] },
])
withsep.apply_widget_layout(Widgets)
sctl = Widgets.menu([
  { "label" => "A", "action" => "x" },
  { "separator" => true },
  { "label" => "B", "action" => "x" },
])
Widgets.layout(sctl, Menu::WIDTH, 6 * 64)
# The separator counts at its shorter widget band (6px), not a full row.
assert_eq(Widgets.menu_row_height(sctl, 1), 6, "toolkit paints a 6px separator (MenuSeparatorH)")
assert_eq(withsep.height, Widgets.menu_row_top(sctl, 3) + Widgets.menu_row_top(sctl, 0),
          "height counts the separator at its widget band, not a full row")
# A click on the separator band is not selectable (-1); the row after it maps to
# entry index 2 (not 1) because separators consume an index slot.
sep_centre = Widgets.menu_row_top(sctl, 1) + Widgets.menu_row_height(sctl, 1) / 2
assert_eq(withsep.hit_test(0, 0, 10, sep_centre), -1, "hit on the separator band is -1")
b_centre = Widgets.menu_row_top(sctl, 2) + Widgets.menu_row_height(sctl, 2) / 2
assert_eq(withsep.hit_test(0, 0, 10, b_centre), 2, "row after separator is index 2")

# ---- RootMenu.build: top-level shape --------------------------------
wmr = WindowManager.new
root = RootMenu.build(wmr)
assert(root.is_a?(Menu), "RootMenu.build returns a Menu")
labels = root.entries.map { |e| e[:label] }
assert_eq(labels[0], "Applications", "top-level[0] = Applications")
assert_eq(labels[1], "Workspaces",   "top-level[1] = Workspaces")
assert_eq(labels[2], "Theme",        "top-level[2] = Theme")
assert_eq(labels[3], "Frame",        "top-level[3] = Frame")
assert_eq(labels[4], "Wallpaper",    "top-level[4] = Wallpaper")
# Top-level row 5 is the separator (no label).
assert_eq(root.entries[5][:separator], true, "top-level[5] is a separator")
assert_eq(labels[6], "About wasmbox", "top-level[6] = About wasmbox")
assert_eq(labels[7], "Reload",        "top-level[7] = Reload")
assert_eq(labels[8], "Exit",          "top-level[8] = Exit")
# Applications + Workspaces + Theme + Frame + Wallpaper each carry a submenu.
assert(root.entries[0][:submenu].is_a?(Menu), "Applications carries a submenu")
assert(root.entries[1][:submenu].is_a?(Menu), "Workspaces carries a submenu")
assert(root.entries[2][:submenu].is_a?(Menu), "Theme carries a submenu")
assert(root.entries[3][:submenu].is_a?(Menu), "Frame carries a submenu")
assert(root.entries[4][:submenu].is_a?(Menu), "Wallpaper carries a submenu")
# About/Reload/Exit each carry a :noop action (dismiss-only in v0).
assert_eq(root.entries[6][:action][0], :noop, "About is a :noop action")
assert_eq(root.entries[7][:action][0], :noop, "Reload is a :noop action")
assert_eq(root.entries[8][:action][0], :noop, "Exit is a :noop action")

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

# ---- aspect-ratio lock: default + resize_to honours it ------------------
# Default lock is 0.0 (free resize). Without a lock, resize_to writes the
# requested w/h after MIN clamp -- the existing behaviour.
wmal = WindowManager.new
fw = wmal.spawn("free", 400, 300)
assert_eq(fw.lock_aspect, 0.0, "default lock_aspect is 0 (free resize)")
fw.resize_to(900, 200)
assert_eq(fw.w, 900, "free resize: width = requested")
assert_eq(fw.h, 200, "free resize: height = requested (MIN ok)")
# Set a 4:3 lock; resize_to then snaps h = round(w / 4:3).
fw.lock_aspect = 4.0/3.0
fw.resize_to(800, 9999)
assert_eq(fw.w, 800, "locked resize: width is the leader")
assert_eq(fw.h, 600, "locked resize: height snaps to width / 4:3 (800/1.333 = 600)")
# Width-leader with a "natural" 4:3 input still snaps exactly.
fw.resize_to(400, 300)
assert_eq(fw.w, 400, "locked resize: 400 stays as width")
assert_eq(fw.h, 300, "locked resize: 4:3 of 400 = 300")
# MIN_W clamp applies BEFORE the ratio derivation: a request below MIN_W
# clamps width up, then h follows from the locked ratio.
fw.resize_to(10, 10)
assert_eq(fw.w, Theme::MIN_W, "locked resize: width clamped to MIN_W")
assert_eq(fw.h, (Theme::MIN_W / (4.0/3.0)).round,
          "locked resize: height = MIN_W / ratio (rounded)")
# 16:9 also snaps cleanly (1280/720) for the generalizability check.
fw.lock_aspect = 16.0/9.0
fw.resize_to(1280, 9999)
assert_eq(fw.w, 1280, "16:9 lock: width leader = 1280")
assert_eq(fw.h, 720, "16:9 lock: height = 1280 / (16:9) = 720")

# ---- aspect-ratio lock: handle_client_message :set_lock_aspect arm -----
wmla = WindowManager.new
# Register an external window through the normal hello path.
res = wmla.handle_client_message({ type: "hello", title: "qclient", w: 320, h: 240,
                                   sab: :sab2, stride: 1280 })
assert_eq(res, :welcome, "hello yields :welcome (pre set_lock_aspect)")
qw = wmla.last_registered
assert_eq(qw.lock_aspect, 0.0, "fresh external window: lock_aspect 0 (no hello.lock_aspect)")
# set_lock_aspect arm: writes the ratio + returns :lock_aspect_set.
res = wmla.handle_client_message({ type: "set_lock_aspect",
                                   window_id: qw.id, ratio: 4.0/3.0 })
assert_eq(res, :lock_aspect_set, "set_lock_aspect on a real window yields :lock_aspect_set")
assert((qw.lock_aspect - 4.0/3.0).abs < 0.0001, "lock_aspect stored as 4:3")
# Lock now takes effect: drag the grip would call resize_to; height snaps to w/(4:3).
qw.resize_to(900, 800)
assert_eq(qw.w, 900, "after set_lock_aspect: width still leader = 900")
assert_eq(qw.h, (900 / (4.0/3.0)).round, "after set_lock_aspect: height snaps to 4:3 of 900")
# A second set_lock_aspect rewrites the ratio (no-op-on-same-value is fine; we
# always accept). Set to 0 disabled? We treat 0/negative as "ignored, keep
# existing" so a client cannot silently DISABLE the lock by passing 0 -- they
# must spawn a fresh window. (Symmetry with the hello.lock_aspect ignore-on-0.)
res = wmla.handle_client_message({ type: "set_lock_aspect", window_id: qw.id, ratio: 0 })
assert_eq(res, :ignored, "set_lock_aspect ratio=0 is :ignored")
res = wmla.handle_client_message({ type: "set_lock_aspect", window_id: qw.id, ratio: -1.5 })
assert_eq(res, :ignored, "set_lock_aspect ratio<0 is :ignored")
assert((qw.lock_aspect - 4.0/3.0).abs < 0.0001, "lock_aspect unchanged by bad ratio")
# Missing ratio is ignored.
res = wmla.handle_client_message({ type: "set_lock_aspect", window_id: qw.id })
assert_eq(res, :ignored, "set_lock_aspect missing ratio is :ignored")
# Unknown window id is ignored.
res = wmla.handle_client_message({ type: "set_lock_aspect", window_id: 999, ratio: 1.5 })
assert_eq(res, :ignored, "set_lock_aspect unknown id is :ignored")
# Panel id is ignored (a dock has no aspect-locked surface).
wmla.register_external("dock", 480, 28, "panel")
pana = wmla.last_registered
res = wmla.handle_client_message({ type: "set_lock_aspect", window_id: pana.id, ratio: 1.5 })
assert_eq(res, :ignored, "set_lock_aspect on a panel id is :ignored")

# ---- aspect-ratio lock: hello.lock_aspect optional field ---------------
# A forward-compat client that knows its aspect can declare it in hello.
wmlh = WindowManager.new
res = wmlh.handle_client_message({ type: "hello", title: "fc", w: 320, h: 240,
                                   sab: :sab3, stride: 1280, lock_aspect: 4.0/3.0 })
assert_eq(res, :welcome, "hello with lock_aspect still yields :welcome")
fcw = wmlh.last_registered
assert((fcw.lock_aspect - 4.0/3.0).abs < 0.0001, "hello.lock_aspect stored")
# An older client without the field gets the existing free-resize default.
res = wmlh.handle_client_message({ type: "hello", title: "old", w: 200, h: 150,
                                   sab: :sab4, stride: 800 })
oldw = wmlh.last_registered
assert_eq(oldw.lock_aspect, 0.0, "hello without lock_aspect: lock_aspect stays 0")
# A non-positive lock_aspect on hello is also ignored (defensive).
res = wmlh.handle_client_message({ type: "hello", title: "bad", w: 200, h: 150,
                                   sab: :sab5, stride: 800, lock_aspect: 0 })
badw = wmlh.last_registered
assert_eq(badw.lock_aspect, 0.0, "hello.lock_aspect = 0 stays 0 (defensive)")

# ---- Frame strategy + FrameRegistry ----------------------------------
# Default frame is OpenboxFrame.
assert(Frame.current.is_a?(OpenboxFrame), "default Frame.current is OpenboxFrame")
# Registry roundtrip: every name in TABLE returns a Frame instance.
FrameRegistry.names.each do |n|
  f = FrameRegistry[n]
  assert(!f.nil?, "FrameRegistry[#{n.inspect}] is not nil")
  assert(f.is_a?(Frame), "FrameRegistry[#{n.inspect}] is a Frame")
end
# Unknown name falls back to OpenboxFrame.
assert(FrameRegistry["nope"].is_a?(OpenboxFrame), "unknown name -> OpenboxFrame fallback")
# Themed frames carry the palette they were built from.
assert(FrameRegistry["openbox-juno"].is_a?(ThemedOpenboxFrame), "openbox-juno -> ThemedOpenboxFrame")
assert(FrameRegistry["aqua-whitesur-light"].is_a?(ThemedAquaFrame), "aqua-whitesur-light -> ThemedAquaFrame")
# Layout differences: Aqua has 3 buttons + max, Openbox has 2 + no max.
ofc = OpenboxFrame.new
afc = AquaFrame.new
assert(!ofc.has_maximize?, "OpenboxFrame has no maximize button")
assert(afc.has_maximize?,  "AquaFrame has the maximize button")
# 16 frame combos exposed via the registry (2 plain + 14 themed).
assert_eq(FrameRegistry.names.length, 16, "FrameRegistry exposes 16 frames")

# ---- RootMenu Frame submenu + FrameRegistry.select ----------------------
# Frame submenu carries one entry per registry name, in registry order.
FrameRegistry.select("openbox")
frame_sub = root.entries[3][:submenu]
assert_eq(frame_sub.entries.length, FrameRegistry.names.length,
          "Frame submenu has one entry per FrameRegistry name")
# Every entry's action is [:frame, "<name>"] with a real registry name.
frame_sub.entries.each do |e|
  assert_eq(e[:action][0], :frame, "Frame entry action[0] = :frame")
  assert(FrameRegistry.names.include?(e[:action][1]), "Frame action id is a known registry name")
end
# Active marker: the entry matching Frame.current_name is prefixed with "* ".
active_labels = frame_sub.entries.map { |e| e[:label] }
assert(active_labels.include?("* openbox"), "active Frame entry marked with '* '")
assert(active_labels.include?("aqua"),      "inactive Frame entry has no '* '")
# Switch via FrameRegistry.select + rebuild — marker follows.
FrameRegistry.select("aqua")
assert_eq(Frame.current_name, "aqua", "select updated Frame.current_name")
assert(Frame.current.is_a?(AquaFrame), "select swapped Frame.current instance")
sub2 = RootMenu.build_frames
labels2 = sub2.entries.map { |e| e[:label] }
assert(labels2.include?("* aqua"), "active marker follows after select")
assert(labels2.include?("openbox"), "previously-active entry now unmarked")
# Assign_current path (direct, without FrameRegistry) also updates the name.
Frame.assign_current("openbox-juno", ThemedOpenboxFrame.new(PALETTES::JUNO))
assert_eq(Frame.current_name, "openbox-juno", "assign_current sets Frame.current_name")
# Reset to openbox so the marker check downstream stays deterministic.
FrameRegistry.select("openbox")

# ---- handle_client_message :set_frame arm ------------------------------
# The showcase (and any future client) can post a set_frame message to
# swap the compositor's Frame.current live. Same effect as the root-menu
# Frame submenu.
wmf2 = WindowManager.new
FrameRegistry.select("openbox")
res = wmf2.handle_client_message({ type: "set_frame", name: "aqua" })
assert_eq(res, :frame_changed, "set_frame to a different valid name yields :frame_changed")
assert_eq(Frame.current_name, "aqua", "set_frame swapped Frame.current_name")
# Already-active name is :ignored (no needless work).
res = wmf2.handle_client_message({ type: "set_frame", name: "aqua" })
assert_eq(res, :ignored, "set_frame to already-active name is :ignored")
# Unknown name is :ignored, current unchanged.
res = wmf2.handle_client_message({ type: "set_frame", name: "no-such-frame" })
assert_eq(res, :ignored, "set_frame with unknown name is :ignored")
assert_eq(Frame.current_name, "aqua", "unknown set_frame leaves Frame.current_name untouched")
# Missing name field: nil.to_s == "" which is not in FrameRegistry.names → :ignored.
res = wmf2.handle_client_message({ type: "set_frame" })
assert_eq(res, :ignored, "set_frame with missing name is :ignored")
# Reset to openbox so downstream state is stable.
FrameRegistry.select("openbox")

# ---- Wallpaper registry (05_menu.rb, Batch-3 desktop layer) -----------------
# The pure selectable-background registry: names, descriptor kinds, and the
# select() state machine (mirrors set_theme). The JS-side render in
# 10_desktop_widgets.rb reads Wallpaper.current, so pinning it here guards the
# root menu + the set_wallpaper wire path against a registry drift.
Wallpaper.reset!
assert_eq(Wallpaper.current_name, "Grid", "Wallpaper default is Grid")
assert_eq(Wallpaper.names[0], "Grid", "Wallpaper names[0] = Grid (the default/fallback)")
assert(Wallpaper.names.include?("Photo"), "Wallpaper registry lists the bundled Photo image")
# Every preset descriptor is well-formed: a :grid, a :gradient (with top+bottom
# hex) or an :image (with a mode).
Wallpaper::PRESETS.each do |p|
  assert(p.has_key?(:name), "every preset carries a name")
  k = p[:kind]
  assert(k == :grid || k == :gradient || k == :image, "preset kind is grid/gradient/image")
  if k == :gradient
    assert(p[:top].is_a?(String) && p[:top].start_with?("#"), "gradient preset has a top hex")
    assert(p[:bottom].is_a?(String) && p[:bottom].start_with?("#"), "gradient preset has a bottom hex")
  elsif k == :image
    assert(p[:mode].is_a?(String), "image preset carries a scale mode")
  end
end
# descriptor(): known name resolves, unknown is nil.
assert_eq(Wallpaper.descriptor("Midnight")[:kind], :gradient, "descriptor(Midnight) is a gradient")
assert(Wallpaper.descriptor("no-such").nil?, "descriptor(unknown) is nil")
assert(Wallpaper.known?("Aurora"), "known?(Aurora) true")
assert(!Wallpaper.known?("no-such"), "known?(unknown) false")
# select(): unknown -> nil, already-active -> nil, real switch -> the new name.
assert(Wallpaper.select("no-such").nil?, "select(unknown) -> nil")
assert(Wallpaper.select("Grid").nil?, "select(already-active) -> nil")
assert_eq(Wallpaper.select("Aurora"), "Aurora", "select(new) -> the new name")
assert_eq(Wallpaper.current_name, "Aurora", "select updated current_name")
assert_eq(Wallpaper.current[:kind], :gradient, "current descriptor follows select")

# ---- RootMenu.build_wallpapers: shape + active marker -----------------------
wp_sub = RootMenu.build_wallpapers
assert_eq(wp_sub.entries.length, Wallpaper.names.length,
          "Wallpaper submenu has one entry per preset")
wp_sub.entries.each do |e|
  assert_eq(e[:action][0], :wallpaper, "Wallpaper entry action[0] = :wallpaper")
  assert(Wallpaper.known?(e[:action][1]), "Wallpaper action id is a known preset")
end
wp_labels = wp_sub.entries.map { |e| e[:label] }
assert(wp_labels.include?("* Aurora"), "active Wallpaper entry marked with '* '")
assert(wp_labels.include?("Grid"), "inactive Wallpaper entry has no '* '")

# ---- handle_client_message :set_wallpaper arm -------------------------------
wmw = WindowManager.new
Wallpaper.reset!
res = wmw.handle_client_message({ type: "set_wallpaper", name: "Midnight" })
assert_eq(res, :wallpaper_changed, "set_wallpaper to a new valid name -> :wallpaper_changed")
assert_eq(Wallpaper.current_name, "Midnight", "set_wallpaper swapped Wallpaper.current_name")
res = wmw.handle_client_message({ type: "set_wallpaper", name: "Midnight" })
assert_eq(res, :ignored, "set_wallpaper to already-active name is :ignored")
res = wmw.handle_client_message({ type: "set_wallpaper", name: "no-such" })
assert_eq(res, :ignored, "set_wallpaper with unknown name is :ignored")
res = wmw.handle_client_message({ type: "set_wallpaper" })
assert_eq(res, :ignored, "set_wallpaper with missing name is :ignored")
# Reset so downstream state is deterministic.
Wallpaper.reset!

# ---- Frame#decoration_spec (widgets paint path, 11_frame_widgets.rb) --------
# decoration_spec re-expresses the same colours + geometry as #paint /
# #paint_frame as a go-widgets WindowDecoration spec Hash, with every rect
# FRAME-LOCAL. Hit-testing (Window#*_rect) is unchanged; these assertions pin
# the spec so the paint can never drift from the geometry the compositor
# hit-tests against.
wmd = WindowManager.new
FrameRegistry.select("openbox")
dw = wmd.spawn("Deco", 240, 150)   # decorated, w=240

ob = Frame.current.decoration_spec(dw, true)
assert_eq(ob["title"], "Deco", "openbox spec carries the title")
assert_eq(ob["title_center"], false, "openbox title is left-aligned")
assert_eq(ob["titlebar"], [0, 0, 240, 22], "openbox titlebar is frame-local at the origin")
assert_eq(ob["title_color"], Theme::TITLE_ACTIVE, "openbox active titlebar colour")
assert_eq(ob["buttons"].length, 2, "openbox has close + minimize (no maximize)")
assert_eq(ob["buttons"][0]["glyph"], "close", "openbox button 0 is close")
assert_eq(ob["buttons"][1]["glyph"], "minimize", "openbox button 1 is minimize")
assert_eq(ob["buttons"][0]["shape"], "rect", "openbox buttons are box-shaped")
assert(ob["show_grip"], "openbox unshaded window shows the grip")
assert(!ob["border"].nil?, "openbox unshaded window has a border")

# Inactive focus swaps the titlebar colour.
obi = Frame.current.decoration_spec(dw, false)
assert_eq(obi["title_color"], Theme::TITLE_INACTIVE, "openbox inactive titlebar colour")

# Shaded window: titlebar only — no border, no grip.
dw.instance_variable_set(:@shaded, true)
obs = Frame.current.decoration_spec(dw, true)
assert(obs["border"].nil?, "shaded window omits the border")
assert(obs["show_grip"].nil?, "shaded window omits the grip")
dw.instance_variable_set(:@shaded, false)

# Aqua: centred title, hairline, drop shadow, three traffic-light circles.
FrameRegistry.select("aqua")
aq = Frame.current.decoration_spec(dw, true)
assert_eq(aq["title_center"], true, "aqua title is centred")
assert_eq(aq["titlebar"], [0, 0, 240, 28], "aqua titlebar is 28px tall, frame-local")
assert_eq(aq["buttons"].length, 3, "aqua has close + minimize + maximize")
assert_eq(aq["buttons"][0]["shape"], "circle", "aqua buttons are traffic-light circles")
assert(!aq["hairline"].nil?, "aqua paints a titlebar hairline")
assert(!aq["shadow"].nil?, "aqua paints a faux drop shadow")
# Traffic-lights stay canonical red active, grey out inactive.
assert_eq(aq["buttons"][0]["face"], AquaFrame::CLOSE_RED, "active close light is canonical red")
aqi = Frame.current.decoration_spec(dw, false)
assert_eq(aqi["buttons"][0]["face"], AquaFrame::TL_DIM_FILL, "inactive close light greys out")

# Themed variants pull their palette (proves the 7-palette matrix stays distinct).
FrameRegistry.select("openbox-juno")
tj = Frame.current.decoration_spec(dw, true)
assert_eq(tj["title_color"], PALETTES::JUNO[:title_active], "themed openbox uses the Juno palette")
FrameRegistry.select("aqua-whitesur-light")
tw = Frame.current.decoration_spec(dw, true)
assert_eq(tw["title_color"], PALETTES::WHITESUR_LIGHT[:title_active], "themed aqua uses the WhiteSur palette")
assert_eq(tw["buttons"][0]["face"], AquaFrame::CLOSE_RED, "themed aqua keeps canonical traffic-lights")
FrameRegistry.select("openbox")

# ---- Desktop notifications (05_notifications.rb) ---------------------------
# Notification model: kind normalization, the composed pill line, expiry.
assert_eq(Notification.normalize_kind("warning"), "warning", "known kind kept")
assert_eq(Notification.normalize_kind("bogus"), "info", "unknown kind -> info")
assert_eq(Notification.normalize_kind(nil), "info", "nil kind -> info")

nfull = Notification.new(1, { title: "T", body: "B", kind: "error", timeout_ms: 2000 }, 100)
assert_eq(nfull.text, "T — B", "text joins title and body with an em dash")
assert_eq(nfull.kind, "error", "kind stored")
assert_eq(nfull.expire_at, 2100, "expire_at = now + timeout_ms")
assert(!nfull.sticky?, "positive timeout is not sticky")
assert(!nfull.has_action?, "no action label -> no action")
assert_eq(nfull.key, "notif#1", "key encodes id")
assert(!nfull.expired?(2099), "not expired before deadline")
assert(nfull.expired?(2100), "expired at deadline")

ntitle = Notification.new(2, { title: "only" }, 0)
assert_eq(ntitle.text, "only", "title-only text drops the separator")
assert(!ntitle.sticky?, "default timeout (5000) is not sticky")
nbody = Notification.new(3, { body: "hey" }, 0)
assert_eq(nbody.text, "hey", "body-only text drops the separator")
nstick = Notification.new(4, { title: "x", timeout_ms: 0 }, 50)
assert(nstick.sticky?, "timeout_ms 0 -> sticky")
assert(!nstick.expired?(999999), "sticky never expires")
nact = Notification.new(5, { title: "x", action_label: "Undo", action: "undo" }, 0)
assert(nact.has_action?, "action label -> has_action")

# ---- Notification v0.86 refinements: icon slot, multi-line body, multi-action -
# Icon: a stock glyph name (no dims) vs. base64 pixel data (positive dims) vs.
# none. glyph_icon? recognizes the ten stock names; image_icon? needs a size.
assert(Notification.glyph_icon?("open"), "'open' is a stock glyph name")
assert(Notification.glyph_icon?("settings"), "'settings' is a stock glyph name")
assert(!Notification.glyph_icon?("nope"), "unknown icon name is not a glyph")
assert(!Notification.glyph_icon?(""), "empty icon name is not a glyph")
nicon_g = Notification.new(10, { title: "t", icon: "open" }, 0)
assert(nicon_g.has_icon?, "a glyph-name icon counts as an icon")
assert(!nicon_g.image_icon?, "a glyph-name icon (no dims) is not an image icon")
assert_eq(nicon_g.icon, "open", "glyph icon name stored")
assert_eq(nicon_g.icon_w, 0, "glyph icon has zero width")
nicon_p = Notification.new(11, { title: "t", icon: "QUJD", icon_w: 2, icon_h: 3 }, 0)
assert(nicon_p.has_icon?, "a pixel icon counts as an icon")
assert(nicon_p.image_icon?, "a pixel icon (positive dims) is an image icon")
assert_eq(nicon_p.icon_w, 2, "pixel icon width stored")
assert_eq(nicon_p.icon_h, 3, "pixel icon height stored")
nicon_n = Notification.new(12, { title: "t" }, 0)
assert(!nicon_n.has_icon?, "no icon field -> no icon")
assert(!nicon_n.image_icon?, "no icon field -> not an image icon")

# Multi-line body: title + body -> two lines; a single half -> one line.
nlines = Notification.new(13, { title: "Build done", body: "3 warnings" }, 0)
assert_eq(nlines.lines, ["Build done", "3 warnings"], "title over body -> two lines")
assert_eq(Notification.new(14, { title: "only" }, 0).lines, ["only"], "title-only -> one line")
assert_eq(Notification.new(15, { body: "hey" }, 0).lines, ["hey"], "body-only -> one line")

# Multi-action parsing: "Label|cb;Label2|cb2" scalar -> ordered [{label, action}].
acts = Notification.parse_actions("Open|do_open;Dismiss|do_dismiss")
assert_eq(acts.length, 2, "two actions parsed from the scalar")
assert_eq(acts[0][:label], "Open", "action 0 label")
assert_eq(acts[0][:action], "do_open", "action 0 callback")
assert_eq(acts[1][:label], "Dismiss", "action 1 label")
assert_eq(acts[1][:action], "do_dismiss", "action 1 callback")
# A field with no "|callback" carries an empty action; a blank label is skipped.
acts2 = Notification.parse_actions("Solo;|orphan;Two|cb")
assert_eq(acts2.length, 2, "blank-label field skipped")
assert_eq(acts2[0][:label], "Solo", "label-only field kept")
assert_eq(acts2[0][:action], "", "label-only field has an empty callback")
assert_eq(acts2[1][:label], "Two", "trailing field parsed after the skipped one")
assert_eq(Notification.parse_actions(nil), [], "nil actions -> []")
assert_eq(Notification.parse_actions(""), [], "empty actions -> []")
# The #actions accessor: parsed list wins; else the legacy single action folds in;
# else [].
nmulti = Notification.new(16, { title: "t", actions: "A|a;B|b" }, 0)
assert_eq(nmulti.actions.length, 2, "parsed actions surface via #actions")
nlegacy = Notification.new(17, { title: "t", action_label: "Undo", action: "undo" }, 0)
assert_eq(nlegacy.actions.length, 1, "legacy single action folds into #actions")
assert_eq(nlegacy.actions[0][:label], "Undo", "folded legacy action label")
assert_eq(nlegacy.actions[0][:action], "undo", "folded legacy action callback")
assert_eq(Notification.new(18, { title: "t" }, 0).actions, [], "no actions -> []")
# A multi-action post supersedes the legacy pair when both are present.
nboth = Notification.new(19, { title: "t", action_label: "Old", action: "old", actions: "New|new" }, 0)
assert_eq(nboth.actions.length, 1, "multi-action list supersedes the legacy pair")
assert_eq(nboth.actions[0][:label], "New", "multi-action wins over the legacy label")

# ---- freedesktop Notification semantics (map_freedesktop, mirrors ToToast) ----
# urgency -> kind: Critical is an error pill; Low and Normal are info (never
# success/warning), matching toast.KindFor.
assert_eq(Notification.kind_for_urgency(Notification::URGENCY_CRITICAL), "error", "Critical urgency -> error kind")
assert_eq(Notification.kind_for_urgency(Notification::URGENCY_LOW), "info", "Low urgency -> info kind")
assert_eq(Notification.kind_for_urgency(Notification::URGENCY_NORMAL), "info", "Normal urgency -> info kind")

# Sticky mapping (toast.Sticky): expire 0, resident, or Critical is sticky.
assert(Notification.sticky_by?(0, false, Notification::URGENCY_NORMAL), "expire_timeout 0 -> sticky")
assert(Notification.sticky_by?(3000, true, Notification::URGENCY_NORMAL), "resident hint -> sticky")
assert(Notification.sticky_by?(3000, false, Notification::URGENCY_CRITICAL), "Critical urgency -> sticky")
assert(!Notification.sticky_by?(3000, false, Notification::URGENCY_NORMAL), "finite Normal is not sticky")
# timeout_ms mapping (toast.LifeFor semantics): 0/resident/Critical -> 0 sentinel,
# -1 (server default) -> DEFAULT_TIMEOUT_MS, any other value passes through (ms).
assert_eq(Notification.timeout_ms_for(0, false, Notification::URGENCY_NORMAL), 0, "expire 0 -> sticky sentinel 0")
assert_eq(Notification.timeout_ms_for(3000, false, Notification::URGENCY_CRITICAL), 0, "Critical -> sticky sentinel 0")
assert_eq(Notification.timeout_ms_for(Notification::EXPIRE_DEFAULT, false, Notification::URGENCY_NORMAL),
          NotificationStack::DEFAULT_TIMEOUT_MS, "expire -1 -> server default ms")
assert_eq(Notification.timeout_ms_for(2500, false, Notification::URGENCY_NORMAL), 2500, "finite expire passes through as ms")

# body-markup stripping (toast.stripMarkup): tags dropped, five entities decoded.
assert_eq(Notification.strip_markup("Build <b>done</b>"), "Build done", "markup tags stripped")
assert_eq(Notification.strip_markup("a &amp; b &lt;c&gt; &quot;d&quot; &apos;e&apos;"),
          "a & b <c> \"d\" 'e'", "the five named entities decoded")
assert_eq(Notification.strip_markup("plain text"), "plain text", "plain text is untouched")
assert_eq(Notification.strip_markup("open <i tag runs off"), "open ", "unterminated tag runs to end")

# summary + body -> lines (toast.linesFor): stripped summary then each body line.
assert_eq(Notification.lines_from("Hi <b>there</b>", "line1\nline2"),
          ["Hi there", "line1", "line2"], "summary over each body line, markup stripped")
assert_eq(Notification.lines_from("Solo", ""), ["Solo"], "summary-only -> one line")

# actions: default key skipped (toast Action.IsDefault), rest -> {label, action:key}.
fda = Notification.fdo_actions("Activate|default;Reply|reply;Later|later")
assert_eq(fda.length, 2, "the reserved default action is skipped")
assert_eq(fda[0][:label], "Reply", "first non-default action label")
assert_eq(fda[0][:action], "reply", "action key is echoed back as the action")
assert_eq(fda[1][:action], "later", "second non-default action key")

# freedesktop? recognizes the spec field set, not a native-only post.
assert(Notification.freedesktop?({ summary: "s" }), "a summary marks a freedesktop post")
assert(Notification.freedesktop?({ urgency: 2 }), "an urgency marks a freedesktop post")
assert(Notification.freedesktop?({ expire_timeout: 0 }), "an expire_timeout marks a freedesktop post")
assert(!Notification.freedesktop?({ title: "t", kind: "info", timeout: 5 }), "a native post is not freedesktop")

# map_freedesktop: a Critical, 2-action, sticky post round-trips through the model.
fmsg = { summary: "Deploy <b>failed</b>", body: "2 errors\ncheck logs",
         urgency: Notification::URGENCY_CRITICAL, expire_timeout: 0,
         actions: "Retry|act_retry;Dismiss|act_dismiss" }
assert(Notification.freedesktop?(fmsg), "the critical post is recognized as freedesktop")
fopts = Notification.map_freedesktop(fmsg)
fn = Notification.new(30, fopts, 100)
assert_eq(fn.kind, "error", "Critical urgency mapped to an error pill")
assert(fn.sticky?, "expire 0 + Critical -> a sticky toast")
assert(!fn.expired?(999999), "the sticky critical toast never auto-expires")
assert_eq(fn.lines, ["Deploy failed", "2 errors", "check logs"],
          "summary + multi-line body mapped to lines with markup stripped")
assert_eq(fn.actions.length, 2, "both freedesktop actions surfaced as buttons")
assert_eq(fn.actions[1][:label], "Dismiss", "second freedesktop action label")
assert_eq(fn.actions[1][:action], "act_dismiss", "second freedesktop action key echoes back")

# map_freedesktop: a Normal, finite-expire post is an info pill that auto-dismisses.
nmsg = { summary: "Saved", urgency: Notification::URGENCY_NORMAL, expire_timeout: 1500 }
nopts = Notification.map_freedesktop(nmsg)
nn = Notification.new(31, nopts, 0)
assert_eq(nn.kind, "info", "Normal urgency -> info pill")
assert(!nn.sticky?, "a finite expire_timeout is not sticky")
assert_eq(nn.expire_at, 1500, "expire_timeout is milliseconds (not re-scaled)")
assert(nn.expired?(1500), "the normal toast auto-dismisses at its deadline")

# map_freedesktop icon: inline image-data wins; else the app_icon glyph; else none.
img_opts = Notification.map_freedesktop({ summary: "s", image_data: "QUJD", image_w: 2, image_h: 3, app_icon: "open" })
img_n = Notification.new(32, img_opts, 0)
assert(img_n.image_icon?, "inline image-data becomes the image icon")
assert_eq(img_n.icon_w, 2, "image-data width mapped")
glyph_opts = Notification.map_freedesktop({ summary: "s", app_icon: "settings" })
glyph_n = Notification.new(33, glyph_opts, 0)
assert(glyph_n.has_icon?, "a stock app_icon glyph becomes the icon")
assert(!glyph_n.image_icon?, "an app_icon glyph is not an image icon")
assert_eq(glyph_n.icon, "settings", "app_icon glyph name mapped")
none_opts = Notification.map_freedesktop({ summary: "s", app_icon: "no-such-icon" })
assert(!Notification.new(34, none_opts, 0).has_icon?, "an unresolved app_icon yields no icon")

# map_freedesktop guards an empty post (no summary and no body).
assert_eq(Notification.map_freedesktop({ urgency: 1 }), nil, "empty summary+body maps to nil")

# NotificationStack: post + stack order + top-right placement.
ns = NotificationStack.new
assert(ns.empty?, "fresh stack is empty")
a = ns.post({ title: "A", timeout_ms: 1000 }, 0)
b = ns.post({ title: "B", timeout_ms: 3000 }, 0)
assert_eq(ns.length, 2, "two toasts stacked")
assert(!ns.empty?, "stack is non-empty after post")
lay = ns.layout(1000)
assert_eq(lay[0][:notif].id, a.id, "first posted is the top row")
assert_eq(lay[1][:notif].id, b.id, "second posted stacks below the first")
assert_eq(lay[0][:x], 1000 - 300 - 16, "toast x is top-right (screen_w - w - margin)")
assert_eq(lay[0][:y], 16, "row 0 y == margin")
assert_eq(lay[1][:y], 16 + NotificationStack::TOAST_H + 10, "row 1 y == margin + h + gap")

# Expiry drops A (deadline 1000); B survives and reflows up to row 0.
dropped = ns.tick(1000)
assert_eq(dropped.length, 1, "one toast expired at its deadline")
assert_eq(dropped[0].id, a.id, "A is the expired toast")
assert_eq(ns.length, 1, "B survives the tick")
assert_eq(ns.layout(1000)[0][:notif].id, b.id, "B reflowed up to row 0")
assert(ns.find(b.id).equal?(b), "find returns the live toast")

# dismiss: found returns the toast; unknown returns nil.
d = ns.dismiss(b.id)
assert_eq(d.id, b.id, "dismiss returns the removed toast")
assert(ns.empty?, "stack empty after the last dismiss")
assert_eq(ns.dismiss(999), nil, "dismiss of an unknown id -> nil")

# Cap: a chatty poster can never grow past MAX_VISIBLE — the oldest drop.
nc = NotificationStack.new
first = nc.post({ title: "0", timeout_ms: 9999 }, 0)
second = nc.post({ title: "1", timeout_ms: 9999 }, 0)
j = 2
while j < NotificationStack::MAX_VISIBLE + 2
  nc.post({ title: j.to_s, timeout_ms: 9999 }, 0)
  j += 1
end
assert_eq(nc.length, NotificationStack::MAX_VISIBLE, "stack capped at MAX_VISIBLE")
assert_eq(nc.find(first.id), nil, "oldest toast dropped by the cap")
assert_eq(nc.find(second.id), nil, "second-oldest toast dropped by the cap")

# Hit-testing: inside a toast rect hits it; outside misses; row1 hits the lower.
nh = NotificationStack.new
t1 = nh.post({ title: "hit", timeout_ms: 9999 }, 0)
assert(nh.at(1000 - 316 + 5, 16 + 5, 1000).equal?(t1), "click inside a toast hits it")
assert_eq(nh.at(10, 10, 1000), nil, "click far from any toast misses")
assert_eq(nh.at(1000 - 316 + 5, 400, 1000), nil, "click below the column misses")
t2 = nh.post({ title: "hit2", timeout_ms: 9999 }, 0)
assert(nh.at(1000 - 316 + 5, 16 + NotificationStack::TOAST_H + 10 + 5, 1000).equal?(t2), "click in row 1 hits the second toast")

# WindowManager notify wire arm: a title OR a body yields :notify; empty drops.
wmn = WindowManager.new
assert_eq(wmn.handle_client_message({ type: "notify", title: "hi" }), :notify, "notify with a title -> :notify")
assert_eq(wmn.handle_client_message({ type: "notify", body: "yo" }), :notify, "notify with a body -> :notify")
assert_eq(wmn.handle_client_message({ type: "notify" }), :ignored, "notify with neither title nor body -> :ignored")

# ---- System tray / status area (05_tray.rb) -------------------------------
# TrayItem: construction, glyph vs image, key, compositor-owned, partial update.
ti = TrayItem.new("mail", { glyph: "search", tooltip: "Inbox",
                            owner: :wk, owner_window: 7,
                            menu: [{ label: "Open", action: [:noop, "o"] }] })
assert_eq(ti.id, "mail", "tray item stores its id")
assert(ti.glyph?, "glyph item reports glyph?")
assert_eq(ti.key, "tray#mail", "tray item key encodes id")
assert(!ti.compositor_owned?, "an owned item is not compositor-owned")
assert_eq(ti.owner_window, 7, "owner_window stored")
# An image item (base64 + size, no glyph) is not a glyph item.
tii = TrayItem.new("pic", { icon: "AAAA", w: 2, h: 2, tooltip: "img" })
assert(!tii.glyph?, "image item is not a glyph item")
assert(tii.compositor_owned?, "an owner-less item is compositor-owned")
# A blank glyph string is treated as "no glyph".
tblank = TrayItem.new("b", { glyph: "" })
assert(!tblank.glyph?, "empty glyph string -> not a glyph item")
# Partial update overwrites only the supplied fields + invalidates the cache.
ti.b64 = "cached"; ti.blitted = true
ti.update({ tooltip: "Unread" })
assert_eq(ti.tooltip, "Unread", "update applied the new tooltip")
assert_eq(ti.glyph, "search", "update left the untouched glyph intact")
assert_eq(ti.b64, nil, "update dropped the render cache")
assert_eq(ti.blitted, false, "update reset the blitted flag")

# TrayArea: add / find / dedup / update / remove / owner cleanup / layout / hit.
ta = TrayArea.new
assert(ta.empty?, "fresh tray is empty")
i1 = ta.add("a", { glyph: "settings", owner_window: 1 })
i2 = ta.add("b", { glyph: "search", owner_window: 2 })
assert_eq(ta.length, 2, "two items added")
assert(!ta.empty?, "tray non-empty after add")
assert(ta.find("a").equal?(i1), "find returns the live item")
# Re-adding a known id updates in place (no duplicate).
again = ta.add("a", { tooltip: "changed" })
assert(again.equal?(i1), "re-add of a known id returns the SAME item (update)")
assert_eq(ta.length, 2, "re-add did not grow the tray")
assert_eq(i1.tooltip, "changed", "re-add applied the update")
# update by id: known -> item, unknown -> nil.
assert(ta.update("b", { tooltip: "t" }).equal?(i2), "update of a known id returns the item")
assert_eq(i2.tooltip, "t", "update mutated the item")
assert_eq(ta.update("nope", { tooltip: "x" }), nil, "update of an unknown id -> nil")

# Layout: right-to-left from just LEFT of the reserved notification column, so
# the two top strips never overlap. Item 0 (oldest) sits nearest the right.
reserve = NotificationStack::TOAST_W + 2 * NotificationStack::TOAST_MARGIN
assert_eq(TrayArea.notif_reserve, reserve, "notif_reserve mirrors the toast column width")
lay = ta.layout(1280)
right = 1280 - reserve
assert_eq(lay[0][:item].id, "a", "layout item 0 is the oldest add")
assert_eq(lay[0][:x], right - TrayArea::ICON, "item 0 hugs the right of the tray strip")
assert_eq(lay[0][:y], TrayArea::MARGIN, "tray strip sits at the top margin")
assert_eq(lay[1][:x], right - TrayArea::ICON - (TrayArea::ICON + TrayArea::GAP),
          "item 1 steps one cell + gap further LEFT")
# The tray strip's right edge stays clear of the toast column's left edge.
toast_left = 1280 - NotificationStack::TOAST_W - NotificationStack::TOAST_MARGIN
assert(lay[0][:x] + TrayArea::ICON <= toast_left,
       "tray strip never overlaps the notification toast column")

# Hit-test: inside a cell hits it; a miss returns nil.
hit = ta.at(lay[0][:x] + 2, lay[0][:y] + 2, 1280)
assert(hit.equal?(i1), "click inside cell 0 hits item a")
assert_eq(ta.at(10, 10, 1280), nil, "click far from the strip misses")
assert(ta.at(lay[1][:x] + 2, lay[1][:y] + 2, 1280).equal?(i2), "click in cell 1 hits item b")

# remove: known -> item, unknown -> nil.
r = ta.remove("a")
assert(r.equal?(i1), "remove returns the removed item")
assert_eq(ta.length, 1, "tray shrank after remove")
assert_eq(ta.remove("gone"), nil, "remove of an unknown id -> nil")

# Owner cleanup: dropping a window removes only ITS items; compositor-owned
# (owner_window nil) survive.
tc = TrayArea.new
tc.add("app1", { glyph: "search", owner_window: 5 })
tc.add("app2", { glyph: "settings", owner_window: 5 })
tc.add("app3", { glyph: "copy", owner_window: 9 })
tc.add("builtin", { glyph: "settings" }) # owner_window nil -> compositor-owned
dropped = tc.remove_for_window(5)
assert_eq(dropped.length, 2, "remove_for_window dropped both items owned by window 5")
assert_eq(tc.length, 2, "window-9 item + the compositor-owned item survive")
assert(!tc.find("builtin").nil?, "compositor-owned item is never window-dropped")
assert(!tc.find("app3").nil?, "another window's item is untouched")
assert_eq(tc.remove_for_window(123), [], "remove_for_window of a window with no items -> []")

# WindowManager tray wire arms: a non-empty id yields the tray verb; empty drops.
wmt_tray = WindowManager.new
assert_eq(wmt_tray.handle_client_message({ type: "tray_add", id: "x", glyph: "search" }),
          :tray_add, "tray_add with an id -> :tray_add")
assert_eq(wmt_tray.handle_client_message({ type: "tray_add" }), :ignored,
          "tray_add with no id -> :ignored")
assert_eq(wmt_tray.handle_client_message({ type: "tray_remove", id: "x" }),
          :tray_remove, "tray_remove with an id -> :tray_remove")
assert_eq(wmt_tray.handle_client_message({ type: "tray_remove" }), :ignored,
          "tray_remove with no id -> :ignored")
assert_eq(wmt_tray.handle_client_message({ type: "tray_update", id: "x", tooltip: "t" }),
          :tray_update, "tray_update with an id -> :tray_update")
assert_eq(wmt_tray.handle_client_message({ type: "tray_update" }), :ignored,
          "tray_update with no id -> :ignored")
# A tray message never creates a window.
assert_eq(wmt_tray.windows.length, 0, "tray messages create no window")

# ---- Desktop applets (05_applets.rb) --------------------------------------
# Applet: kind registry, per-kind geometry, cache invalidation, hit-test.
assert(Applet.kind?("clock"), "clock is a known applet kind")
assert(Applet.kind?("calendar"), "calendar is a known applet kind")
assert(Applet.kind?("monitor"), "monitor is a known applet kind")
assert(!Applet.kind?("bogus"), "unknown applet kind rejected")
assert_eq(Applet::KINDS.length, 3, "three built-in applet kinds")
assert_eq(Applet.label("clock"), "Clock", "clock friendly label")
assert_eq(Applet.label("monitor"), "System Monitor", "monitor friendly label")
ap = Applet.new("clock", 40, 50)
assert_eq(ap.kind, "clock", "applet stores its kind")
assert_eq(ap.x, 40, "applet x")
assert_eq(ap.y, 50, "applet y")
assert_eq(ap.w, Applet::SIZE["clock"][0], "applet w from SIZE")
assert_eq(ap.h, Applet::SIZE["clock"][1], "applet h from SIZE")
assert_eq(ap.key, "applet#clock", "applet cache key encodes kind")
# contains? is half-open on the far edges.
assert(ap.contains?(40, 50), "top-left corner is inside")
assert(ap.contains?(40 + ap.w - 1, 50 + ap.h - 1), "bottom-right-most pixel inside")
assert(!ap.contains?(39, 50), "one px left is outside")
assert(!ap.contains?(40 + ap.w, 50), "far x edge is half-open (outside)")
assert(!ap.contains?(40, 50 + ap.h), "far y edge is half-open (outside)")
# invalidate clears the render cache.
ap.b64 = "cached"; ap.blitted = true; ap.sig = "s"
ap.invalidate
assert_eq(ap.b64, nil, "invalidate dropped the buffer")
assert_eq(ap.blitted, false, "invalidate reset the blitted flag")
assert_eq(ap.sig, nil, "invalidate cleared the signature")

# AppletBoard: add / dedup / find / shown? / remove / toggle.
ab = AppletBoard.new
assert(ab.empty?, "fresh board is empty")
c1 = ab.add("clock")
assert(!c1.nil?, "add clock returns the applet")
assert_eq(ab.length, 1, "one applet after add")
assert(ab.shown?("clock"), "clock reported shown")
assert(!ab.shown?("monitor"), "monitor not shown yet")
# Default position is the kind's DEFAULT_POS.
assert_eq(c1.x, Applet::DEFAULT_POS["clock"][0], "add uses default x")
assert_eq(c1.y, Applet::DEFAULT_POS["clock"][1], "add uses default y")
# Re-adding a shown kind is a no-op (same object, no duplicate).
again = ab.add("clock")
assert(again.equal?(c1), "re-add of a shown kind returns the SAME applet")
assert_eq(ab.length, 1, "re-add did not grow the board")
# Unknown kind rejected.
assert_eq(ab.add("bogus"), nil, "add of an unknown kind -> nil")
# add with explicit coordinates.
mon = ab.add("monitor", 500, 300)
assert_eq(mon.x, 500, "add honours an explicit x")
assert_eq(mon.y, 300, "add honours an explicit y")
assert_eq(ab.shown_kinds.length, 2, "two kinds shown")
# find + remove.
assert(ab.find("monitor").equal?(mon), "find returns the live applet")
r = ab.remove("monitor")
assert(r.equal?(mon), "remove returns the removed applet")
assert(!ab.shown?("monitor"), "monitor gone after remove")
assert_eq(ab.remove("monitor"), nil, "remove of an unshown kind -> nil")
# toggle: adds when hidden, removes when shown.
t = ab.toggle("calendar")
assert_eq(t[0], :added, "toggle of a hidden kind adds it")
assert(ab.shown?("calendar"), "calendar shown after toggle-on")
t2 = ab.toggle("calendar")
assert_eq(t2[0], :removed, "toggle of a shown kind removes it")
assert(!ab.shown?("calendar"), "calendar hidden after toggle-off")
assert_eq(ab.toggle("bogus"), nil, "toggle of an unknown kind -> nil")

# move: clamps the whole tile onto the desktop.
ab2 = AppletBoard.new
mv = ab2.add("clock", 0, 0)
ab2.move(mv, -100, -100, 1280, 800)
assert_eq(mv.x, 0, "move clamps x up to 0")
assert_eq(mv.y, 0, "move clamps y up to 0")
ab2.move(mv, 5000, 5000, 1280, 800)
assert_eq(mv.x, 1280 - mv.w, "move clamps x to screen_w - w")
assert_eq(mv.y, 800 - mv.h, "move clamps y to screen_h - h")
ab2.move(mv, 300, 200, 1280, 800)
assert_eq(mv.x, 300, "in-bounds move keeps x")
assert_eq(mv.y, 200, "in-bounds move keeps y")
assert_eq(ab2.move(nil, 1, 1, 1280, 800), nil, "move of nil applet -> nil")

# at: topmost applet under a point, later-added wins a tie; miss -> nil.
ab3 = AppletBoard.new
a_lo = ab3.add("clock", 100, 100)
assert(ab3.at(105, 105).equal?(a_lo), "at hits the clock under the point")
assert_eq(ab3.at(10, 10), nil, "at far from any tile -> nil")
# Overlapping tiles: the later-added monitor wins the shared point.
a_hi = ab3.add("monitor", 110, 110)
assert(ab3.at(115, 115).equal?(a_hi), "at returns the later-added tile on overlap")

# serialize / parse round-trip (clamped), skipping malformed + unknown fields.
ab4 = AppletBoard.new
ab4.add("clock", 40, 40)
ab4.add("monitor", 300, 120)
s = ab4.serialize
assert_eq(s, "clock:40,40;monitor:300,120", "serialize encodes kind:x,y;...")
ab5 = AppletBoard.new
ab5.parse(s, 1280, 800)
assert_eq(ab5.length, 2, "parse restored both applets")
assert(ab5.shown?("clock") && ab5.shown?("monitor"), "parse restored the kinds")
assert_eq(ab5.find("monitor").x, 300, "parse restored the x")
assert_eq(ab5.find("monitor").y, 120, "parse restored the y")
# Malformed / unknown fields are skipped; a nil text yields an empty board.
ab6 = AppletBoard.new
ab6.parse("clock:10,10;bogus:1,1;garbage;monitor:oops", 1280, 800)
assert_eq(ab6.length, 1, "parse skips unknown kind + malformed fields")
assert(ab6.shown?("clock"), "the one valid field survived parse")
assert_eq(ab6.find("clock").x, 10, "valid field parsed its x")
ab7 = AppletBoard.new
ab7.parse(nil, 1280, 800)
assert(ab7.empty?, "parse(nil) yields an empty board")
# parse clamps out-of-bounds saved positions.
ab8 = AppletBoard.new
ab8.parse("clock:99999,99999", 1280, 800)
assert_eq(ab8.find("clock").x, 1280 - Applet::SIZE["clock"][0], "parse clamps a saved x onto the desktop")

# ---- CalendarView: the calendar applet's month-nav mirror (05_applets.rb) --
# Construction clamps the month into 1..12 and the day into the month's range.
cv = CalendarView.new(2026, 8, 15)
assert_eq(cv.year, 2026, "calendar view year")
assert_eq(cv.month, 8, "calendar view month")
assert_eq(cv.selected, 15, "calendar view selected day")
assert_eq(cv.sig, "2026-8-15", "sig encodes year-month-selected")
assert_eq(CalendarView.new(2026, 0, 1).month, 1, "month clamped up to 1")
assert_eq(CalendarView.new(2026, 13, 1).month, 12, "month clamped down to 12")
assert_eq(CalendarView.new(2026, 1, 0).selected, 1, "selected clamped up to 1")
assert_eq(CalendarView.new(2026, 1, 99).selected, 31, "selected clamped to days_in_month (Jan=31)")

# days_in_month incl. leap February.
assert_eq(CalendarView.new(2026, 2, 1).days_in_month, 28, "Feb 2026 has 28 days")
assert_eq(CalendarView.new(2024, 2, 1).days_in_month, 29, "Feb 2024 (leap) has 29 days")
assert_eq(CalendarView.new(2000, 2, 1).days_in_month, 29, "Feb 2000 (div-400 leap) has 29 days")
assert_eq(CalendarView.new(1900, 2, 1).days_in_month, 28, "Feb 1900 (div-100 non-leap) has 28 days")
assert(CalendarView.leap?(2024), "2024 is leap")
assert(!CalendarView.leap?(2026), "2026 is not leap")
assert(!CalendarView.leap?(1900), "1900 is not leap (century, not div-400)")
assert(CalendarView.leap?(2000), "2000 is leap (div-400)")

# next_month advances, wraps December -> next January, and re-clamps the day.
cvn = CalendarView.new(2026, 11, 30)
cvn.next_month
assert_eq(cvn.month, 12, "next_month Nov -> Dec")
assert_eq(cvn.year, 2026, "next_month within the year keeps the year")
cvn.next_month
assert_eq(cvn.month, 1, "next_month Dec wraps to Jan")
assert_eq(cvn.year, 2027, "next_month Dec bumps the year")
# Day re-clamp on a shrink: 31 Jan -> Feb clamps to 28 (2027 non-leap).
cvc = CalendarView.new(2027, 1, 31)
cvc.next_month
assert_eq(cvc.month, 2, "next_month Jan -> Feb")
assert_eq(cvc.selected, 28, "selected 31 re-clamped to 28 in Feb 2027")

# prev_month steps back, wraps January -> previous December.
cvp = CalendarView.new(2026, 2, 15)
cvp.prev_month
assert_eq(cvp.month, 1, "prev_month Feb -> Jan")
assert_eq(cvp.year, 2026, "prev_month within the year keeps the year")
cvp.prev_month
assert_eq(cvp.month, 12, "prev_month Jan wraps to Dec")
assert_eq(cvp.year, 2025, "prev_month Jan drops the year")

# set_selected clamps into the current month.
cvs = CalendarView.new(2026, 4, 10) # April = 30 days
assert_eq(cvs.set_selected(31).selected, 30, "set_selected clamps 31 -> 30 in April")
assert_eq(cvs.set_selected(-5).selected, 1, "set_selected clamps below 1 -> 1")
assert_eq(cvs.set_selected(12).selected, 12, "in-range set_selected keeps the day")
# A month navigation moves the sig (drives the applet dirty-gate) — and only then.
cvsig = CalendarView.new(2026, 6, 5)
before = cvsig.sig
cvsig.next_month
assert(cvsig.sig != before, "sig changes after a month navigation")

# ---- RootMenu Applets submenu (05_menu.rb) --------------------------------
# build_applets: one entry per kind, in KINDS order, active marker on shown.
board = AppletBoard.new
board.add("clock")
asub = RootMenu.build_applets(board)
assert_eq(asub.entries.length, Applet::KINDS.length, "Applets submenu has one entry per kind")
asub.entries.each do |e|
  assert_eq(e[:action][0], :applet, "every Applets entry action[0] = :applet")
  assert(Applet.kind?(e[:action][1]), "every Applets action[1] is a known kind")
end
alabels = asub.entries.map { |e| e[:label] }
assert(alabels.include?("* Clock"), "shown applet marked with '* '")
assert(alabels.include?("System Monitor"), "hidden applet has no marker")
# A nil board (feature off) still builds a submenu, all unmarked.
asub_nil = RootMenu.build_applets(nil)
asub_nil.entries.each do |e|
  assert(!e[:label].start_with?("* "), "nil board leaves every applet unmarked")
end

# RootMenu.build with a board inserts "Applets" after "Wallpaper"; without one
# the menu is byte-for-byte the pre-applets shape (the top-level assertions
# higher up pin the 1-arg shape — Applications/Workspaces/Theme/Frame/Wallpaper,
# separator, About/Reload/Exit — so here we only check the 2-arg insertion).
root_with = RootMenu.build(wmr, board)
lbls = root_with.entries.map { |e| e[:label] }
assert_eq(lbls[3], "Frame", "Frame still at index 3 with a board")
assert_eq(lbls[4], "Wallpaper", "Wallpaper still at index 4 with a board")
assert_eq(lbls[5], "Applets", "Applets inserted at index 5 (after Wallpaper)")
assert_eq(root_with.entries[6][:separator], true, "separator follows Applets")
assert(root_with.entries[5][:submenu].is_a?(Menu), "Applets carries a submenu")
# 1-arg build (no board) omits the entry entirely.
root_without = RootMenu.build(wmr)
lbls2 = root_without.entries.map { |e| e[:label] }
assert(!lbls2.include?("Applets"), "1-arg build omits the Applets entry (feature-gated)")

# ---- window snapping / half-tiling: snap_rect geometry -------------------
# A 1000x800 screen with a 40px dock strip at the bottom and a 22px titlebar
# strip at the top: the work area is (0, 22) .. (1000, 738).
wsnap = WindowManager.new
SW = 1000; SH = 800; RT = 22; RB = 40
lr = wsnap.snap_rect(:left, SW, SH, RT, RB)
assert_eq(lr, [0, 22, 500, 738], "snap_rect :left = left half of the work area")
rr = wsnap.snap_rect(:right, SW, SH, RT, RB)
assert_eq(rr, [500, 22, 500, 738], "snap_rect :right = right half, flush to the right edge")
mr = wsnap.snap_rect(:max, SW, SH, RT, RB)
assert_eq(mr, [0, 22, 1000, 738], "snap_rect :max = the whole work area (dock + titlebar reserved)")
assert(wsnap.snap_rect(:bogus, SW, SH, RT, RB).nil?, "snap_rect unknown zone is nil")
# Odd width: right half must still reach the right edge (no 1px seam).
odd = wsnap.snap_rect(:right, 1001, 800, 0, 0)
assert_eq(odd, [500, 0, 501, 800], "snap_rect :right on odd width covers the last column")
lodd = wsnap.snap_rect(:left, 1001, 800, 0, 0)
assert_eq(lodd[0] + lodd[2], odd[0], "left + right halves tile with no overlap/gap on odd width")
# Zero reserve degrades to a full-screen work area.
assert_eq(wsnap.snap_rect(:max, 800, 600, 0, 0), [0, 0, 800, 600], "snap_rect :max with no reserve = full screen")

# ---- snap: applies geometry + saves pre-snap, restore round-trips ---------
sww = wsnap.spawn("snapme", 300, 200)
sww.move_to(120, 90)
ox, oy, ownd, oht = sww.x, sww.y, sww.w, sww.h
assert(!sww.snapped?, "fresh window is not snapped")
res = wsnap.snap_left(sww, SW, SH, RT, RB)
assert(!res.nil?, "snap_left returns the window on a real snap")
assert(sww.snapped?, "window reports snapped? after snap_left")
assert_eq(sww.snap_state, :left, "snap_state records the zone")
assert_eq([sww.x, sww.y, sww.w, sww.h], [0, 22, 500, 738], "snap_left moved+resized to the left half")
# Pre-snap geometry captured exactly once (the ORIGINAL free geometry).
assert_eq(sww.pre_snap, { x: ox, y: oy, w: ownd, h: oht }, "pre_snap captured the original free geometry")
# Re-snap to another zone must NOT overwrite the saved pre-snap geometry.
wsnap.snap_right(sww, SW, SH, RT, RB)
assert_eq(sww.snap_state, :right, "snap_right updates the zone")
assert_eq([sww.x, sww.y, sww.w, sww.h], [500, 22, 500, 738], "snap_right moved to the right half")
assert_eq(sww.pre_snap, { x: ox, y: oy, w: ownd, h: oht }, "pre_snap unchanged across a re-snap")
wsnap.maximize(sww, SW, SH, RT, RB)
assert_eq(sww.snap_state, :max, "maximize sets the :max zone")
assert_eq([sww.x, sww.y, sww.w, sww.h], [0, 22, 1000, 738], "maximize fills the work area")
# Restore returns to the exact original free geometry + clears the state.
rres = wsnap.restore_snap(sww)
assert(!rres.nil?, "restore_snap returns the window on a real restore")
assert(!sww.snapped?, "window is free again after restore_snap")
assert_eq([sww.x, sww.y, sww.w, sww.h], [ox, oy, ownd, oht], "restore_snap returned the original geometry")
# Restore on a free window is a no-op.
assert(wsnap.restore_snap(sww).nil?, "restore_snap on a free window is a no-op")

# ---- snap: exclusions (only decorated 'window' role, no aspect-lock) -------
# Panels never snap.
wsnap.register_external("dock-snap", 480, 28, "panel")
pnl = wsnap.last_registered
assert(wsnap.snap_left(pnl, SW, SH, RT, RB).nil?, "snap_left on a panel is a no-op")
assert(!pnl.snapped?, "panel never gets a snap_state")
# Popups never snap.
wsnap.handle_client_message({ type: "hello", title: "menu", role: "popup",
                              parent: sww.id, rel_x: 5, rel_y: 5, w: 40, h: 24 })
pop = wsnap.last_registered
assert(wsnap.snap_left(pop, SW, SH, RT, RB).nil?, "snap_left on a popup is a no-op")
# Aspect-locked windows are skipped (ratio preserved, matching resize policy).
lockw = wsnap.spawn("locked", 320, 240)
lockw.lock_aspect = 320.0 / 240.0
assert(wsnap.snap_left(lockw, SW, SH, RT, RB).nil?, "snap on an aspect-locked window is a no-op")
assert(!lockw.snapped?, "aspect-locked window never snaps")
# nil / degenerate work-area guards.
assert(wsnap.snap_left(nil, SW, SH, RT, RB).nil?, "snap on nil window is a no-op")
tiny = wsnap.spawn("tiny-area")
assert(wsnap.snap(tiny, :max, 10, 10, 20, 20).nil?, "snap into a non-positive work area is a no-op")

# ---- Alt-Tab switcher: candidate ring ordering (WindowManager) ------------
# cycle_candidates returns the SAME pool #cycle walks — normal, non-minimized
# windows on the ACTIVE workspace — but MOST-RECENTLY-FOCUSED FIRST (the head is
# where the switcher opens its cursor).
watr = WindowManager.new
at1 = watr.spawn("at-1")
at2 = watr.spawn("at-2")
at3 = watr.spawn("at-3") # spawned last -> focused -> head of the ring
cands = watr.cycle_candidates
assert_eq(cands.length, 3, "cycle_candidates lists all 3 normal windows")
assert(cands[0].equal?(at3), "cycle_candidates[0] is the focused (most-recent) window")
assert(cands[1].equal?(at2), "cycle_candidates[1] is the next-most-recent")
assert(cands[2].equal?(at1), "cycle_candidates[2] is the least-recent")
# Minimized windows are excluded.
watr.minimize(at2)
cmin = watr.cycle_candidates
assert_eq(cmin.length, 2, "cycle_candidates skips a minimized window")
assert(cmin.none? { |w| w.equal?(at2) }, "the minimized window is not a candidate")
watr.restore_window(at2)
# Panels are excluded.
watr.register_external("dock-at", 480, 28, "panel")
assert_eq(watr.cycle_candidates.length, 3, "cycle_candidates excludes the panel")
# Off-workspace windows are excluded.
watr.set_workspace(2)
assert_eq(watr.cycle_candidates.length, 0, "cycle_candidates is empty on a fresh workspace")
watr.set_workspace(1)
assert_eq(watr.cycle_candidates.length, 3, "cycle_candidates back to 3 on the original workspace")

# ---- AltTab cursor: open / advance / wrap / reverse / select --------------
# Drive the pure state machine with a real candidate ring from a FRESH WM (the
# ring above was reordered by the minimize/restore/workspace focus traffic).
watr2 = WindowManager.new
bt1 = watr2.spawn("bt-1")
bt2 = watr2.spawn("bt-2")
bt3 = watr2.spawn("bt-3") # focused -> head of the ring [bt3, bt2, bt1]
sw_at = AltTab.new
assert(!sw_at.active?, "fresh switcher is closed")
assert_eq(sw_at.count, 0, "closed switcher has no candidates")
assert(sw_at.selected.nil?, "closed switcher selects nothing")
assert(sw_at.advance.nil?, "advance on a closed switcher is a no-op")
# Open parks the cursor on the FIRST (focused) candidate without moving.
ring = watr2.cycle_candidates # [bt3, bt2, bt1]
assert(!sw_at.open(ring).nil?, "open over a non-empty ring returns self")
assert(sw_at.active?, "switcher is up after open")
assert_eq(sw_at.count, 3, "switcher count = candidate count")
assert_eq(sw_at.index, 0, "open parks the cursor on the focused window (index 0)")
assert(sw_at.selected.equal?(bt3), "index 0 selects the focused window")
# Re-opening while up must NOT reset a walk in progress.
sw_at.advance
assert_eq(sw_at.index, 1, "advance moves the cursor forward one step")
sw_at.open(ring)
assert_eq(sw_at.index, 1, "re-open while active keeps the current cursor")
# Advance forward wraps at the end.
assert_eq(sw_at.advance, 2, "advance -> 2")
assert(sw_at.selected.equal?(bt1), "index 2 selects the last candidate")
assert_eq(sw_at.advance, 0, "advance from the last wraps back to 0")
# Reverse advance wraps at the start.
assert_eq(sw_at.advance(true), 2, "reverse advance from 0 wraps to the last")
assert_eq(sw_at.advance(true), 1, "reverse advance -> 1")

# ---- AltTab: commit + cancel ----------------------------------------------
# Commit returns the selected candidate and closes; a second commit is inert.
picked = sw_at.commit
assert(picked.equal?(bt2), "commit returns the selected candidate")
assert(!sw_at.active?, "switcher is closed after commit")
assert_eq(sw_at.count, 0, "closed switcher holds no candidates after commit")
assert(sw_at.commit.nil?, "a second commit after close returns nil")
# Cancel closes without choosing.
sw2 = AltTab.new
sw2.open(watr.cycle_candidates)
sw2.advance
assert(sw2.active?, "second switcher is up")
assert(sw2.cancel.nil?, "cancel returns nil (chose nothing)")
assert(!sw2.active?, "switcher is closed after cancel")
# A single-window ring is degenerate but safe: open + advance stay on index 0.
solo = WindowManager.new
only = solo.spawn("solo")
sw3 = AltTab.new
sw3.open(solo.cycle_candidates)
assert_eq(sw3.count, 1, "single-window ring has one candidate")
assert_eq(sw3.advance, 0, "advance on a single-window ring stays on 0 (wraps to self)")
assert(sw3.commit.equal?(only), "commit on a single-window ring returns that window")
# Open over an empty ring is refused (stays closed).
sw4 = AltTab.new
assert(sw4.open([]).nil?, "open over an empty ring returns nil")
assert(!sw4.active?, "switcher stays closed on an empty open")

# ---- Spotlight: fuzzy_match? (subsequence, case-insensitive) --------------
assert(Spotlight.fuzzy_match?("terminal", ""), "empty query matches anything")
assert(Spotlight.fuzzy_match?("terminal", "term"), "prefix substring matches")
assert(Spotlight.fuzzy_match?("terminal", "trml"), "non-contiguous subsequence matches")
assert(Spotlight.fuzzy_match?("terminal", "terminal"), "full-string matches")
assert(!Spotlight.fuzzy_match?("terminal", "xyz"), "absent chars do not match")
assert(!Spotlight.fuzzy_match?("terminal", "mret"), "out-of-order chars do not match")
assert(!Spotlight.fuzzy_match?("cat", "cats"), "query longer than text does not match")

# ---- Spotlight.app_list: built from LAUNCHABLE + labels + hidden ----------
sl_apps = Spotlight.app_list(WindowManager::LAUNCHABLE, RootMenu::APP_LABELS, RootMenu::HIDDEN)
# Every entry is a { id:, label: } Hash whose id is genuinely launchable.
wsl = WindowManager.new
sl_apps.each do |app|
  assert(app.has_key?(:id) && app.has_key?(:label), "app entry has id + label")
  assert(wsl.launchable?(app[:id]), "every Spotlight app id is in LAUNCHABLE")
end
sl_labels = sl_apps.map { |a| a[:label] }
sl_ids    = sl_apps.map { |a| a[:id] }
assert(sl_labels.include?("Terminal"), "Spotlight lists Terminal")
assert(sl_labels.include?("Calculator"), "Spotlight lists Calculator")
assert(sl_ids.include?("terminal"), "Spotlight carries the terminal id")
# Hidden ids (hello-oci) are omitted, exactly like the Applications menu.
assert(!sl_labels.include?("Hello (OCI)"), "Spotlight hides the probe-only hello-oci")
expected_sl = 0
WindowManager::LAUNCHABLE.each do |id, _d|
  expected_sl += 1 unless RootMenu::HIDDEN.include?(id)
end
assert_eq(sl_apps.length, expected_sl, "Spotlight lists one entry per non-hidden LAUNCHABLE id")
# Labelled ids sort first (APP_LABELS order): Terminal is entry 0.
assert_eq(sl_apps[0][:label], "Terminal", "Spotlight order follows APP_LABELS (Terminal first)")

# app_list with a nil hidden arg + a synthetic UNLABELLED id -> capitalized
# fallback appended after the labelled ids (covers the leftover-id branch).
synth_launch = { "terminal" => "clients/terminal/worker.js", "widget" => "clients/widget/worker.js" }
synth_labels = { "terminal" => "Terminal" }
synth = Spotlight.app_list(synth_launch, synth_labels, nil)
assert_eq(synth.length, 2, "synthetic app_list has both ids")
assert_eq(synth[0][:label], "Terminal", "labelled id first")
assert_eq(synth[1][:id], "widget", "unlabelled id appended")
assert_eq(synth[1][:label], "Widget", "unlabelled id falls back to capitalized")

# ---- Spotlight: closed-state transitions are all inert --------------------
sp0 = Spotlight.new
assert(!sp0.active?, "fresh palette is closed")
assert_eq(sp0.count, 0, "closed palette has no results")
assert(sp0.selected.nil?, "closed palette selects nothing")
assert(sp0.type("x").nil?, "type on a closed palette is a no-op")
assert(sp0.backspace.nil?, "backspace on a closed palette is a no-op")
assert(sp0.set_query("x").nil?, "set_query on a closed palette is a no-op")
assert(sp0.move(1).nil?, "move on a closed palette is a no-op")
assert(sp0.commit.nil?, "commit on a closed palette returns nil")
# Open over an empty list is refused (stays closed).
assert(sp0.open([]).nil?, "open over an empty app list returns nil")
assert(!sp0.active?, "palette stays closed on an empty open")
assert(sp0.open(nil).nil?, "open over nil is refused")

# ---- Spotlight: open + fuzzy filter + selection + launch resolution -------
sp = Spotlight.new
assert(!sp.open(sl_apps).nil?, "open over a non-empty list returns self")
assert(sp.active?, "palette is up after open")
assert_eq(sp.query, "", "open starts with an empty query")
assert_eq(sp.count, sl_apps.length, "empty query matches every app")
assert_eq(sp.index, 0, "open parks the selection on the first result")
assert(sp.selected.equal?(sl_apps[0]), "index 0 selects the first app")
# Re-opening while up keeps the walk in progress (no reset).
sp.set_query("cal")
assert(sp.open(sl_apps).equal?(sp), "re-open while active returns self without resetting")
assert_eq(sp.query, "cal", "re-open kept the live query")
# Fuzzy-filter to Terminal.
sp.set_query("term")
assert_eq(sp.count, 1, "query 'term' fuzzy-filters to exactly one app")
assert_eq(sp.selected[:id], "terminal", "the sole 'term' match is Terminal")
assert(wsl.launchable?(sp.selected[:id]), "the resolved Spotlight id is launchable")
# Backspace widens the match again.
sp.backspace # "ter"
assert(sp.count >= 1, "backspace re-widens the result set")
assert_eq(sp.query, "ter", "backspace trimmed the last query char")
# type() appends a char and re-parks the selection on the first result.
sp.set_query("")
sp.type("c"); sp.type("a"); sp.type("l")
assert_eq(sp.query, "cal", "type() appended each character")
cal_labels = sp.results.map { |a| a[:label] }
assert(cal_labels.include?("Calculator"), "query 'cal' includes Calculator")
assert_eq(sp.index, 0, "type() re-parks the selection at the top")

# ---- Spotlight: move wraps within the filtered result set -----------------
sp.set_query("") # all apps visible
n = sp.count
assert(n >= 3, "the full app list has several results to walk")
assert_eq(sp.move(1), 1, "move(1) -> index 1")
assert_eq(sp.move(1), 2, "move(1) -> index 2")
assert_eq(sp.move(-1), 1, "move(-1) -> index 1")
# Wrap at the start.
sp.set_query("")
assert_eq(sp.move(-1), n - 1, "move(-1) from 0 wraps to the last result")
# Wrap at the end: step to the last, then one more wraps to 0.
sp.set_query("")
(n - 1).times { sp.move(1) }
assert_eq(sp.index, n - 1, "walked to the last result")
assert_eq(sp.move(1), 0, "move(1) from the last wraps back to 0")

# ---- Spotlight: no-match query yields an empty, inert result set ----------
spx = Spotlight.new
spx.open(sl_apps)
spx.set_query("zzzq")
assert_eq(spx.count, 0, "an unmatched query has no results")
assert(spx.selected.nil?, "no selection when nothing matches")
assert(spx.move(1).nil?, "move over an empty result set is a no-op")

# ---- Spotlight: commit returns the selected app + closes; cancel closes ----
spc = Spotlight.new
spc.open(sl_apps)
spc.set_query("term")
picked = spc.commit
assert(!picked.nil?, "commit returns the selected app")
assert_eq(picked[:id], "terminal", "commit returns the Terminal entry")
assert(!spc.active?, "palette is closed after commit")
assert_eq(spc.count, 0, "closed palette holds no results after commit")
assert(spc.commit.nil?, "a second commit after close returns nil")
# Command routing: the moved selection is exactly what commit resolves + launches.
# (16_spotlight.rb drives the bound command_palette, but the pure model pins the
# open -> filter -> move -> commit contract the launcher routes on.)
spr = Spotlight.new
spr.open(sl_apps)
routed0 = spr.selected
spr.move(1) # step to the second result
routed1 = spr.selected
assert(!routed1.equal?(routed0), "move changes the routed selection")
picked_routed = spr.commit
assert(picked_routed.equal?(routed1), "commit routes to the moved selection")
assert(wsl.launchable?(picked_routed[:id]), "the routed commit id is launchable")
# Cancel closes without choosing.
spk = Spotlight.new
spk.open(sl_apps)
spk.type("t")
assert(spk.active?, "palette up before cancel")
assert(spk.cancel.nil?, "cancel returns nil (chose nothing)")
assert(!spk.active?, "palette closed after cancel")
assert_eq(spk.query, "", "cancel cleared the query")

puts "rbtest: ran all Spotlight model assertions"

# ---- Expose: grid dimensions (near-square, integer sqrt) ------------------
# cols = smallest c with c*c >= n; rows = ceil(n / cols).
assert_eq(Expose.grid_dims(0), [0, 0], "grid_dims(0) is empty")
assert_eq(Expose.grid_dims(1), [1, 1], "grid_dims(1) = 1x1")
assert_eq(Expose.grid_dims(2), [2, 1], "grid_dims(2) = 2x1")
assert_eq(Expose.grid_dims(3), [2, 2], "grid_dims(3) = 2x2 (short last row)")
assert_eq(Expose.grid_dims(4), [2, 2], "grid_dims(4) = 2x2")
assert_eq(Expose.grid_dims(5), [3, 2], "grid_dims(5) = 3x2")
assert_eq(Expose.grid_dims(6), [3, 2], "grid_dims(6) = 3x2")
assert_eq(Expose.grid_dims(7), [3, 3], "grid_dims(7) = 3x3")
assert_eq(Expose.grid_dims(9), [3, 3], "grid_dims(9) = 3x3")
assert_eq(Expose.grid_dims(10), [4, 3], "grid_dims(10) = 4x3")

# ---- Expose: closed-state transitions are all inert -----------------------
ex0 = Expose.new
assert(!ex0.active?, "fresh spread is closed")
assert_eq(ex0.count, 0, "closed spread has no tiles")
assert(ex0.selected.nil?, "closed spread selects nothing")
assert(ex0.move(:left).nil?, "move on a closed spread is a no-op")
assert(ex0.select_index(0).nil?, "select_index on a closed spread is a no-op")
assert(ex0.commit.nil?, "commit on a closed spread returns nil")
assert(ex0.open([]).nil?, "open over an empty list returns nil")
assert(!ex0.active?, "spread stays closed on an empty open")
assert(ex0.open(nil).nil?, "open over nil is refused")

# ---- Expose: open over a candidate ring (grid + parked selection) ---------
# Build a real candidate ring from a WM: cycle_candidates is most-recent-first.
wex = WindowManager.new
ex_wins = []
5.times { |i| ex_wins << wex.spawn("ex-#{i}") } # ex-4 focused -> head of ring
ring = wex.cycle_candidates
assert_eq(ring.length, 5, "candidate ring has all 5 windows")
ex = Expose.new
assert(!ex.open(ring).nil?, "open over a non-empty ring returns self")
assert(ex.active?, "spread is up after open")
assert_eq(ex.count, 5, "spread tile count = candidate count")
assert_eq(ex.cols, 3, "5 tiles lay out in 3 columns")
assert_eq(ex.rows, 2, "5 tiles lay out in 2 rows")
assert_eq(ex.index, 0, "open parks the selection on the focused window (index 0)")
assert(ex.selected.equal?(ring[0]), "index 0 selects the focused (head) window")
# Re-open while up must NOT reset the selection.
ex.move(:right)
assert_eq(ex.index, 1, "move(:right) advanced the selection")
ex.open(ring)
assert_eq(ex.index, 1, "re-open while active keeps the current selection")

# ---- Expose: row_tiles + rows_in_col shape (5 tiles, 3x2) -----------------
# Grid index layout for n=5, cols=3:  row0 = [0,1,2]  row1 = [3,4]
assert_eq(ex.row_tiles(0), 3, "row 0 holds a full 3 tiles")
assert_eq(ex.row_tiles(1), 2, "row 1 (short last row) holds 2 tiles")
assert_eq(ex.rows_in_col(0), 2, "column 0 spans both rows")
assert_eq(ex.rows_in_col(1), 2, "column 1 spans both rows")
assert_eq(ex.rows_in_col(2), 1, "column 2 only exists in row 0 (short last row)")

# ---- Expose: 2-D selection move + wrap ------------------------------------
# Left/right wrap within the row; up/down wrap within the column.
exm = Expose.new
exm.open(ring) # index 0 (row0,col0)
assert_eq(exm.move(:right), 1, "right: 0 -> 1")
assert_eq(exm.move(:right), 2, "right: 1 -> 2")
assert_eq(exm.move(:right), 0, "right wraps within row 0: 2 -> 0")
assert_eq(exm.move(:left), 2, "left wraps within row 0: 0 -> 2")
# Down from (row0,col0=index0) -> (row1,col0=index3); down again wraps to 0.
exm.select_index(0)
assert_eq(exm.move(:down), 3, "down: index 0 -> 3 (next row, same column)")
assert_eq(exm.move(:down), 0, "down wraps within column 0: 3 -> 0")
assert_eq(exm.move(:up), 3, "up wraps within column 0: 0 -> 3")
# Column 2 has only one row (index 2): up/down stay put (wrap to self).
exm.select_index(2)
assert_eq(exm.move(:down), 2, "down on a single-row column stays put")
assert_eq(exm.move(:up), 2, "up on a single-row column stays put")
# Down from index1 (row0,col1) -> index4 (row1,col1).
exm.select_index(1)
assert_eq(exm.move(:down), 4, "down: index 1 -> 4 (row1, col1)")

# ---- Expose: tile rectangles are non-overlapping + fit the work area ------
# Lay 5 tiles into a 1000x600 work area at origin (40,40) with an 10px gap and
# assert every rect is inside the bounds and no two rects overlap.
AX = 40; AY = 40; AW = 1000; AH = 600; GAP = 10
rects = []
5.times { |i| rects << exm.tile_rect(i, AX, AY, AW, AH, GAP) }
rects.each_with_index do |r, i|
  assert(r[0] >= AX, "tile #{i} left inside work area")
  assert(r[1] >= AY, "tile #{i} top inside work area")
  assert(r[0] + r[2] <= AX + AW, "tile #{i} right inside work area")
  assert(r[1] + r[3] <= AY + AH, "tile #{i} bottom inside work area")
  assert(r[2] > 0 && r[3] > 0, "tile #{i} has positive size")
end
# Pairwise non-overlap: two rects overlap iff they intersect on BOTH axes.
def rects_overlap?(a, b)
  ax1 = a[0]; ay1 = a[1]; ax2 = a[0] + a[2]; ay2 = a[1] + a[3]
  bx1 = b[0]; by1 = b[1]; bx2 = b[0] + b[2]; by2 = b[1] + b[3]
  ax1 < bx2 && bx1 < ax2 && ay1 < by2 && by1 < ay2
end
i = 0
while i < rects.length
  j = i + 1
  while j < rects.length
    assert(!rects_overlap?(rects[i], rects[j]), "tiles #{i} and #{j} do not overlap")
    j += 1
  end
  i += 1
end

# ---- Expose: click -> tile resolution -------------------------------------
# A point at a tile's center resolves to that tile; a point in the dim gap
# between tiles (or outside the work area) resolves to -1.
5.times do |i|
  r = exm.tile_rect(i, AX, AY, AW, AH, GAP)
  cx = r[0] + r[2] / 2
  cy = r[1] + r[3] / 2
  assert_eq(exm.tile_at(cx, cy, AX, AY, AW, AH, GAP), i,
            "tile_at at the center of tile #{i} resolves to #{i}")
end
# A click far outside the work area hits nothing.
assert_eq(exm.tile_at(-100, -100, AX, AY, AW, AH, GAP), -1,
          "tile_at outside the work area is -1")
# A click in the 1px seam between the first two tiles (the gap) hits nothing:
# just past tile 0's right edge, before tile 1's left edge.
r0 = exm.tile_rect(0, AX, AY, AW, AH, GAP)
gap_x = r0[0] + r0[2] + 1 # 1px into the dim gap after tile 0
gap_y = r0[1] + r0[3] / 2
assert_eq(exm.tile_at(gap_x, gap_y, AX, AY, AW, AH, GAP), -1,
          "tile_at in the gap between tiles is -1")

# ---- Expose: commit + cancel ----------------------------------------------
exc = Expose.new
exc.open(ring)
exc.move(:right) # select index 1
picked = exc.commit
assert(picked.equal?(ring[1]), "commit returns the selected candidate")
assert(!exc.active?, "spread is closed after commit")
assert_eq(exc.count, 0, "closed spread holds no candidates after commit")
assert(exc.commit.nil?, "a second commit after close returns nil")
# Cancel closes without choosing.
exx = Expose.new
exx.open(ring)
exx.move(:down)
assert(exx.active?, "spread is up before cancel")
assert(exx.cancel.nil?, "cancel returns nil (chose nothing)")
assert(!exx.active?, "spread is closed after cancel")
# A single-window spread is degenerate but safe: 1x1 grid, moves stay on 0.
solo_ex = WindowManager.new
only_ex = solo_ex.spawn("solo-ex")
exs = Expose.new
exs.open(solo_ex.cycle_candidates)
assert_eq(exs.count, 1, "single-window spread has one tile")
assert_eq(exs.cols, 1, "single-window spread is 1 column")
assert_eq(exs.rows, 1, "single-window spread is 1 row")
assert_eq(exs.move(:right), 0, "move on a 1x1 spread stays on 0")
assert_eq(exs.move(:down), 0, "down on a 1x1 spread stays on 0")
assert(exs.commit.equal?(only_ex), "commit on a 1x1 spread returns that window")
# select_index out of range is a no-op.
exr = Expose.new
exr.open(ring)
assert(exr.select_index(99).nil?, "select_index out of range is a no-op")
assert(exr.select_index(-1).nil?, "select_index negative is a no-op")
assert_eq(exr.index, 0, "out-of-range select_index left the selection put")

puts "rbtest: ran all Expose model assertions"

# ---- DamageSet: empty / full / add / collapse -------------------------
ds = DamageSet.new
assert(ds.empty?, "fresh DamageSet is empty")
assert(!ds.full?, "fresh DamageSet is not full")
ds.add(10, 20, 30, 40)
assert(!ds.empty?, "DamageSet not empty after add")
assert_eq(ds.rects.length, 1, "one rect added")
assert_eq(ds.rects[0], { x: 10, y: 20, w: 30, h: 40 }, "rect stored as hash")
assert_eq(ds.total_area, 30 * 40, "total_area sums rect area")
# Zero/negative rects are dropped.
ds.add(0, 0, 0, 5)
ds.add(0, 0, -3, 5)
assert_eq(ds.rects.length, 1, "empty/negative rects dropped")
# clear resets both flag + rects.
ds.clear
assert(ds.empty?, "clear resets to empty")
# full! wins over rects.
ds.add(1, 2, 3, 4)
ds.full!
assert(ds.full?, "full! marks full")
assert(!ds.empty?, "full set is not empty")
ds.add(5, 5, 5, 5) # add is a no-op while full
assert(ds.full?, "add is a no-op once full")
# Accumulating past MAX_RECTS collapses to full (bounded book-keeping).
ds2 = DamageSet.new
i = 0
while i <= DamageSet::MAX_RECTS
  ds2.add(i, 0, 2, 2)
  i += 1
end
assert(ds2.full?, "> MAX_RECTS rects collapses to full")

# ---- DamageSet: add_rect / rect_intersects? / union -------------------
ds3 = DamageSet.new
ds3.add_rect([100, 100, 50, 50])
assert_eq(ds3.rects[0], { x: 100, y: 100, w: 50, h: 50 }, "add_rect takes [x,y,w,h]")
r = { x: 100, y: 100, w: 50, h: 50 }
assert(DamageSet.rect_intersects?(r, [120, 120, 10, 10]), "overlapping bounds intersect")
assert(DamageSet.rect_intersects?(r, [90, 90, 20, 20]), "corner overlap intersects")
assert(!DamageSet.rect_intersects?(r, [200, 200, 10, 10]), "disjoint bounds do not intersect")
assert(!DamageSet.rect_intersects?(r, [150, 100, 10, 10]), "touching far edge is half-open (no intersect)")
assert(!DamageSet.rect_intersects?(r, [100, 60, 10, 40]), "touching top edge is half-open (no intersect)")
# union bounds two [x,y,w,h] rects.
u = DamageSet.union([10, 10, 20, 20], [50, 40, 10, 10])
assert_eq(u, { x: 10, y: 10, w: 50, h: 40 }, "union spans both rects")
u2 = DamageSet.union([0, 0, 100, 100], [20, 20, 10, 10])
assert_eq(u2, { x: 0, y: 0, w: 100, h: 100 }, "union of a rect containing another is the outer rect")

# ---- Frame chrome sprite key + bounds ---------------------------------
wmsp = WindowManager.new
FrameRegistry.select("openbox")
spw = wmsp.spawn("sprite", 200, 120)
k1 = Frame.sprite_key(spw, true)
# Same inputs -> same key (a cache HIT: the decoration need not repaint).
assert_eq(Frame.sprite_key(spw, true), k1, "sprite_key is stable for unchanged inputs")
# Focus flips the key (active vs inactive colours differ).
assert(Frame.sprite_key(spw, false) != k1, "sprite_key changes with focus state")
# Position does NOT affect the key: a dragged window keeps its cached sprite.
spw.move_to(spw.x + 137, spw.y + 42)
assert_eq(Frame.sprite_key(spw, true), k1, "sprite_key is position-independent (drag stays a cache hit)")
# Size, title, shade + the active frame each invalidate the key.
spw.resize_to(260, 120)
assert(Frame.sprite_key(spw, true) != k1, "sprite_key changes with size")
spw.resize_to(200, 120)
spw.title = "renamed"
assert(Frame.sprite_key(spw, true) != k1, "sprite_key changes with title")
spw.title = "sprite"
wmsp.shade(spw)
assert(Frame.sprite_key(spw, true) != k1, "sprite_key changes when shaded")
wmsp.unshade(spw)
FrameRegistry.select("aqua")
assert(Frame.sprite_key(spw, true) != k1, "sprite_key changes with the active frame/palette")
FrameRegistry.select("openbox")
assert_eq(Frame.sprite_key(spw, true), k1, "sprite_key returns to its value once inputs are restored")
# sprite_bounds is frame_rect padded by 1px on every side (captures the 1px
# drop shadow / half-pixel border stroke) and fully contains frame_rect.
frct = spw.frame_rect
sb = Frame.sprite_bounds(spw)
assert_eq(sb[0], frct[0] - 1, "sprite_bounds x = frame x - 1 (left pad)")
assert_eq(sb[1], frct[1] - 1, "sprite_bounds y = frame top - 1 (top pad)")
assert(sb[0] <= frct[0] && sb[1] <= frct[1], "sprite_bounds origin covers frame origin")
assert(sb[0] + sb[2] >= frct[0] + frct[2], "sprite_bounds right covers frame right + shadow")
assert(sb[1] + sb[3] >= frct[1] + frct[3], "sprite_bounds bottom covers frame bottom + shadow")
# Reset to openbox so downstream state is stable.
FrameRegistry.select("openbox")

# ---- A11yTree: ARIA projection of the window stack --------------------
# Stand-in windows built directly so focus / minimize / title are set precisely
# (the model takes any object answering id/title/role/focused?/minimized?/x/y/w/h).
aw1 = Window.new(1, "xterm",  40, 40, 500, 320, "#000", "window")
aw2 = Window.new(2, "editor", 80, 80, 400, 300, "#000", "window")
aw2.focused = true
awlist = [aw1, aw2]

# active_id is the focused window's id; nil when nothing is focused.
assert_eq(A11yTree.active_id(awlist), 2, "active_id is the focused window")
assert(A11yTree.active_id([aw1]).nil?, "active_id nil when nothing focused")

# build: application root + one node per window, stack order preserved.
tree = A11yTree.build(awlist)
assert_eq(tree[:role], "application", "root role is application")
assert_eq(tree[:label], "wasmbox desktop", "root default label")
assert_eq(tree[:active_id], 2, "root active_id follows focus")
assert_eq(tree[:nodes].length, 2, "one node per window")
assert_eq(tree[:nodes][0][:id], 1, "node order preserved (bottom-to-top)")
assert_eq(tree[:nodes][1][:id], 2, "second node is the focused window")
assert_eq(A11yTree.build([aw1], "desk")[:label], "desk", "custom root label honoured")

# window_node fields + geometry.
n1 = tree[:nodes][0]
assert_eq(n1[:role], "window", "window node role")
assert_eq(n1[:label], "xterm", "window node label is the title")
assert_eq(n1[:focused], false, "unfocused window node not focused")
assert_eq(n1[:minimized], false, "live window node not minimized")
assert_eq(n1[:x], 40, "node carries x")
assert_eq(n1[:y], 40, "node carries y")
assert_eq(n1[:w], 500, "node carries w")
assert_eq(n1[:h], 320, "node carries h")
assert_eq(tree[:nodes][1][:focused], true, "focused window node is focused")

# window_label: title, with a blank-title fallback so no node is nameless.
blank = Window.new(3, "", 0, 0, 10, 10, "#000", "window")
assert_eq(A11yTree.window_label(blank), "(untitled)", "blank title -> (untitled)")
assert_eq(A11yTree.window_label(aw1), "xterm", "non-blank title kept")

# window_actions: a LIVE window offers focus / minimize / close, in that order.
acts = A11yTree.window_actions(aw1, "xterm")
assert_eq(acts.length, 3, "live window has 3 actions")
assert_eq(acts[0][:name], "focus", "first action is focus")
assert_eq(acts[0][:label], "Activate xterm", "focus action label")
assert_eq(acts[1][:name], "minimize", "live window offers minimize")
assert_eq(acts[1][:label], "Minimize xterm", "minimize action label")
assert_eq(acts[2][:name], "close", "last action is close")
assert_eq(acts[2][:label], "Close xterm", "close action label")

# a MINIMIZED window offers restore in place of minimize; its node is flagged.
mini = Window.new(4, "files", 0, 0, 10, 10, "#000", "window")
mini.minimized = true
macts = A11yTree.window_actions(mini, "files")
assert_eq(macts[1][:name], "restore", "minimized window offers restore")
assert_eq(macts[1][:label], "Restore files", "restore action label")
assert_eq(A11yTree.window_node(mini)[:minimized], true, "minimized node flagged")

# signature: sensitive to focus / minimize / title, INSENSITIVE to geometry
# (a drag or resize moves pixels but changes nothing a screen reader announces).
sig0 = A11yTree.signature(awlist)
aw1.move_to(aw1.x + 100, aw1.y + 100)
assert_eq(A11yTree.signature(awlist), sig0, "signature ignores geometry")
aw1.title = "xterm-2"
assert(A11yTree.signature(awlist) != sig0, "signature changes on retitle")
aw1.title = "xterm"
assert_eq(A11yTree.signature(awlist), sig0, "signature restored when title restored")
aw2.focused = false
assert(A11yTree.signature(awlist) != sig0, "signature changes on focus move")
aw2.focused = true
aw1.minimized = true
assert(A11yTree.signature(awlist) != sig0, "signature changes on minimize")
aw1.minimized = false
assert_eq(A11yTree.signature(awlist), sig0, "signature returns to baseline")

# to_json: deterministic JSON with booleans, an active_id + null active_id.
j = A11yTree.to_json(A11yTree.build(awlist))
assert(j.include?('"role":"application"'), "json has application root")
assert(j.include?('"active_id":2'), "json carries active_id")
assert(j.include?('"label":"xterm"'), "json carries a window label")
assert(j.include?('"name":"focus"'), "json carries an action name")
assert(j.include?('"minimized":false'), "json serializes booleans")
assert(j.include?('"w":500'), "json carries geometry")
jn = A11yTree.to_json(A11yTree.build([aw1]))
assert(jn.include?('"active_id":null'), "json null active_id when nothing focused")

# esc: quotes, backslashes + the C0 controls that would break a JSON string.
assert_eq(A11yTree.esc("plain"), "plain", "esc passes plain text")
assert_eq(A11yTree.esc("a\"b"), "a\\\"b", "esc escapes double-quote")
assert_eq(A11yTree.esc("a\\b"), "a\\\\b", "esc escapes backslash")
assert_eq(A11yTree.esc("a\nb"), "a\\nb", "esc escapes newline")
assert_eq(A11yTree.esc("a\tb\rc"), "a\\tb\\rc", "esc escapes tab + CR")
# a title needing escaping serializes safely.
qw = Window.new(5, "a\"q", 0, 0, 10, 10, "#000", "window")
assert(A11yTree.to_json(A11yTree.build([qw])).include?('a\\"q'), "quoted title escaped in json")

puts "rbtest: ran all A11y model assertions"

puts "rbtest: ran all pure-WM assertions"
`
