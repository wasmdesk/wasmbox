// Command rbbench measures the compositor's per-frame PAINT cost the way it
// actually bites in the browser: the number of wasm<->JS bridge crossings the
// Ruby compositor issues per composite. Every @ctx.set / @ctx.call and every
// JS.global.call is one syscall/js round-trip, and those round-trips — not the
// browser's fill rate — dominate a WASM compositor's frame time.
//
// It loads the SAME compositor/*.rb source that ships in wasmbox.wasm (00_..
// through 06_, the classes we can drive off-wasm), installs a tiny Ruby fake of
// the JS bridge that COUNTS crossings instead of touching a canvas, stubs the
// DE-overlay draw methods that live in the wasm-only files (08_.. through 17_)
// so the render path fans out cleanly, and drives Compositor#render across three
// scenarios in two modes:
//
//   baseline : the pre-optimisation behaviour — recomposite the WHOLE screen
//              every frame (draw the desktop + every window), regardless of
//              whether anything changed.
//   optimised: the shipped behaviour — the dirty-rectangle gate + region
//              recomposite: skip an idle frame entirely, and otherwise repaint
//              only the union of the damaged rectangles.
//
// The two modes run in ONE binary via the @bench_no_gate seam the Compositor
// reads (set here through instance_variable_set), so the comparison is
// apples-to-apples on identical scene state.
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

// benchUpperExcl excludes 07_boot.rb (and anything after) —07_boot spawns real
// clients + starts the rAF loop, and 08_.. onward are the wasm-only DE overlay
// files (widgets binding + JS draw paths). Everything 00_.. through 06_ is the
// class definitions we exercise directly; the overlay draw methods those files
// would add are stubbed in benchScript.
const benchUpperExcl = "07_"

func main() {
	dir := flag.String("dir", "compositor", "path to the compositor/ directory")
	flag.Parse()
	src, err := loadCompositor(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rbbench: load %s: %v\n", *dir, err)
		os.Exit(2)
	}
	var out bytes.Buffer
	if err := ruby.Run(src+"\n"+benchScript, &out); err != nil {
		fmt.Fprintln(os.Stderr, out.String())
		fmt.Fprintf(os.Stderr, "rbbench FAIL: %v\n", err)
		os.Exit(1)
	}
	os.Stdout.Write(out.Bytes())
}

// loadCompositor concatenates compositor/*.rb below benchUpperExcl in
// alphabetic (= load) order — the same view main.go's embed loader bakes into
// the wasm, minus the boot + overlay files.
func loadCompositor(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".rb") || e.Name() >= benchUpperExcl {
			continue
		}
		names = append(names, e.Name())
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

// benchScript installs the counting JS fake + the overlay stubs, then drives the
// scenarios. It prints a human table plus machine-readable "RESULT <scenario>
// <baseline> <new>" lines that cmd/rbbench's test parses.
const benchScript = `
# --- counting fake of the JS bridge ----------------------------------------
$N = 0                       # total wasm<->JS crossings this measurement

# BenchCtx stands in for a Canvas 2D context: every set/call is one crossing.
class BenchCtx
  def set(*a) ; $N += 1 ; self ; end
  def call(*a) ; $N += 1 ; self ; end
end
$CTX = BenchCtx.new

# BenchGlobal stands in for JS.global: each call() is one crossing. It returns 0
# for wasmboxWindowLiveSeq (a stable seq: the anim scenario drives commits via
# merge_damage, not the seqlock) and nil otherwise (the blit helpers are void).
class BenchGlobal
  def call(name, *args)
    $N += 1
    return 0 if name == "wasmboxWindowLiveSeq"
    nil
  end
end
$BG = BenchGlobal.new

module JS
  def self.global ; $BG ; end
  def self.log(*a) ; end
  def self.raf(&b) ; end
  def self.window ; nil ; end
  def self.document ; nil ; end
end

# The DE-overlay flags + draw methods live in the wasm-only files (08_.. 17_)
# that this harness does not load. Model the SHIPPED compositor's costs: the
# desktop background, window decoration and HUD each present a CACHED widget
# buffer with a single blit crossing (DESKTOP_WIDGETS / FRAME_WIDGETS /
# HUD_WIDGETS = true in production), so we set those flags on and stub each draw
# to that one crossing — this is the honest per-window cost the dirty-rect gate
# competes against (NOT the ~25-op hand-drawn chrome, which would flatter the
# result). The interactive overlays (applets/tray/notifications/modals) are off
# + stubbed: they are out of scope for the desktop+window recomposite measured
# here. draw_desktop_region already presents the cached buffer's sub-rect via the
# real wasmboxBlitRGBARegion (one crossing), matching draw_desktop_widgets.
class Compositor
  APPLETS         = false
  TRAY            = false
  NOTIFICATIONS   = false
  ALTTAB          = false
  SPOTLIGHT       = false
  EXPOSE          = false
  MENU_WIDGETS    = false
  HUD_WIDGETS     = true
  DESKTOP_WIDGETS = true
  FRAME_WIDGETS   = true
  # One cached-buffer present each (the shipped putImageData / drawImage cost).
  def draw_desktop_widgets ; JS.global.call("wasmboxBlitRGBA") ; end
  def draw_window_frame_widgets(win, active) ; JS.global.call("wasmboxBlitRGBAOver") ; end
  def draw_hud_widgets ; JS.global.call("wasmboxBlitRGBAOver") ; end
  def draw_applets ; end
  def draw_tray ; end
  def draw_notifications ; end
  def draw_alttab ; end
  def draw_spotlight ; end
  def draw_expose ; end
  # require "widgets" is a wasm-only binding; the opt-in is a no-op off-wasm.
  def enable_opentype_text_once ; end
  # The snapping-probe publish is a per-frame JS crossing OUTSIDE the paint gate;
  # stub it so the measurement reflects only the composite cost (an idle frame
  # then genuinely issues zero crossings).
  def publish_snap_geometry ; end
end

FrameRegistry.select("openbox")

# --- scenario harness ------------------------------------------------------
def new_comp(wm, no_gate)
  comp = Compositor.new(wm)
  comp.instance_variable_set(:@ctx, $CTX)
  comp.instance_variable_set(:@width, 1920)
  comp.instance_variable_set(:@height, 1080)
  comp.instance_variable_set(:@bench_no_gate, no_gate)
  comp
end

# Build a desktop of n decorated in-process windows at spread-out positions.
def build_windows(wm, n)
  i = 0
  while i < n
    w = wm.spawn("win #{i}", 320, 220)
    w.move_to(80 + (i % 4) * 300, 80 + (i / 4) * 260)
    i += 1
  end
end

# Attach one small external "animator" window that commits new pixels every
# frame (the blinking-cursor / live-terminal case). It sits in an empty corner
# clear of the 8-window grid, so a commit damages ONLY its own region — exactly
# the localized change the dirty-rect gate must repaint without touching the
# other windows.
def add_animator(wm)
  a = wm.register_external("animator", 240, 160)
  a.instance_variable_set(:@image_data, :fake)
  a.move_to(1500, 700)
  a
end

# Composite over 'frames' rAF ticks after one un-counted warm-up frame; mutate
# runs per frame before render. Returns the crossings counted across the
# measured frames.
def measure(label, n, frames, no_gate)
  wm = WindowManager.new
  build_windows(wm, n)
  anim = (label == :anim) ? add_animator(wm) : nil
  comp = new_comp(wm, no_gate)
  comp.render                 # warm-up (full frame), not measured
  $N = 0
  f = 0
  while f < frames
    if label == :drag
      wm.windows[0].move_to(300 + f * 6, 300 + f * 4)
    elsif label == :anim
      anim.merge_damage({ x: 0, y: 0, w: 480, h: 320 })
    end
    comp.render
    f += 1
  end
  $N
end

FRAMES = 30

def report(name, n)
  base = measure(name, 8, FRAMES, true)    # no gate = whole-screen every frame
  opt  = measure(name, 8, FRAMES, false)   # dirty-rect gate + region
  per_base = base / FRAMES
  per_opt  = opt / FRAMES
  pct = base == 0 ? 0 : (100 * (base - opt) / base)
  line = "  %-26s baseline=%6d  optimised=%6d  (per-frame %5d -> %-5d  -%d%%)" % [name.to_s, base, opt, per_base, per_opt, pct]
  puts line
  puts "RESULT #{name} #{base} #{opt}"
end

puts "compositor paint crossings over #{FRAMES} frames, 8 windows (lower = cheaper):"
report(:idle, 8)   # nothing changes after warm-up
report(:drag, 8)   # one window dragged each frame
report(:anim, 8)   # one external window commits each frame (+8 static decos)
puts "rbbench: done"
`
