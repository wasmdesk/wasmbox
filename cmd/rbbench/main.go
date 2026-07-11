// Command rbbench measures the compositor's per-frame cost the way it actually
// bites in the browser: the number of wasm<->JS bridge crossings issued per
// composite. Every @ctx.set / @ctx.call and every JS.global.call the Ruby
// compositor makes is one syscall/js round-trip, and those round-trips — not
// the browser's fill rate — dominate a WASM compositor's frame time.
//
// It loads the SAME compositor/*.rb source that ships in wasmbox.wasm (00_..
// through 06_, skipping 07_boot which spawns real clients), installs a tiny
// Ruby fake of the JS bridge that COUNTS crossings instead of touching a
// canvas, and drives Compositor#render across three scenarios in two modes:
//
//   baseline : the pre-optimisation behaviour — recomposite the whole screen
//              every frame, repaint each window's decoration inline.
//   optimised: the shipped behaviour — dirty-rectangle gate + region
//              recomposite + retained-mode chrome sprite cache.
//
// The two modes run in ONE binary via the @bench_no_gate / @bench_no_sprite
// seams the Compositor reads (set here through instance_variable_set), so the
// comparison is apples-to-apples on identical scene state.
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

// benchUpperExcl excludes 07_boot.rb (and anything after) — that file spawns
// real clients + starts the rAF loop, which need a live browser. Everything
// 00_.. through 06_ is the class definitions we exercise directly.
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
// the wasm, minus the boot file.
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

// benchScript installs the counting JS fake + drives the scenarios. It prints
// a human table plus machine-readable "RESULT <scenario> <baseline> <new>"
// lines that cmd/rbbench's test parses.
const benchScript = `
# --- counting fake of the JS bridge ----------------------------------------
$N = 0                       # total wasm<->JS crossings this measurement

# BenchCtx stands in for a Canvas 2D context: every set/call is one crossing.
class BenchCtx
  def set(*a) ; $N += 1 ; self ; end
  def call(*a) ; $N += 1 ; self ; end
end
$CTX = BenchCtx.new

# BenchGlobal stands in for JS.global. Each call() is one crossing; it also
# models the chrome sprite cache: a matching key is a HIT (returns nil, caller
# just presents), a new/changed key is a MISS (returns the sprite ctx, caller
# repaints into it — those paints are counted through $CTX).
class BenchGlobal
  def initialize ; @cache = {} ; end
  def call(name, *args)
    $N += 1
    if name == "wasmboxChromeBegin"
      win_id = args[0] ; key = args[1]
      return nil if @cache[win_id] == key    # cache hit
      @cache[win_id] = key                   # cache miss
      return $CTX
    end
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

FrameRegistry.select("openbox")

# --- scenario harness ------------------------------------------------------
def new_comp(wm, no_gate, no_sprite)
  comp = Compositor.new(wm)
  comp.instance_variable_set(:@ctx, $CTX)
  comp.instance_variable_set(:@width, 1920)
  comp.instance_variable_set(:@height, 1080)
  comp.instance_variable_set(:@bench_no_gate, no_gate)
  comp.instance_variable_set(:@bench_no_sprite, no_sprite)
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

# Attach one external "animator" window that commits new pixels every frame,
# overlapping a couple of the static windows.
def add_animator(wm)
  a = wm.register_external("animator", 480, 320)
  a.instance_variable_set(:@image_data, :fake)
  a.move_to(200, 160)
  a
end

# Run frames composites after one un-counted warm-up frame; mutate runs per
# frame before render. Returns the crossings counted across the measured frames.
def measure(label, n, frames, no_gate, no_sprite)
  wm = WindowManager.new
  build_windows(wm, n)
  anim = (label == :anim) ? add_animator(wm) : nil
  comp = new_comp(wm, no_gate, no_sprite)
  $BG = BenchGlobal.new
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
  base = measure(name, 8, FRAMES, true, true)    # no gate + no sprite = old
  opt  = measure(name, 8, FRAMES, false, false)  # gate + sprite     = new
  per_base = base / FRAMES
  per_opt  = opt / FRAMES
  pct = base == 0 ? 0 : (100 * (base - opt) / base)
  line = "  %-26s baseline=%6d  optimised=%6d  (per-frame %5d -> %-5d  -%d%%)" % [name.to_s, base, opt, per_base, per_opt, pct]
  puts line
  puts "RESULT #{name} #{base} #{opt}"
end

puts "compositor crossings over #{FRAMES} frames, 8 windows (lower = cheaper):"
report(:idle, 8)   # nothing changes after warm-up
report(:drag, 8)   # one window dragged each frame
report(:anim, 8)   # one external window commits each frame (+8 static decos)
puts "rbbench: done"
`
