//go:build !js
// +build !js

package main

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	ruby "github.com/go-embedded-ruby/ruby"
)

// runBench loads the same compositor source rbbench does and returns the parsed
// "RESULT <scenario> <baseline> <optimised>" crossing counts.
func runBench(t *testing.T) map[string][2]int {
	t.Helper()
	src, err := loadCompositor("../../compositor")
	if err != nil {
		t.Fatalf("loadCompositor: %v", err)
	}
	var out bytes.Buffer
	if err := ruby.Run(src+"\n"+benchScript, &out); err != nil {
		t.Fatalf("ruby.Run: %v\n%s", err, out.String())
	}
	got := map[string][2]int{}
	for _, ln := range strings.Split(out.String(), "\n") {
		f := strings.Fields(ln)
		if len(f) != 4 || f[0] != "RESULT" {
			continue
		}
		base, _ := strconv.Atoi(f[2])
		opt, _ := strconv.Atoi(f[3])
		got[f[1]] = [2]int{base, opt}
	}
	return got
}

// TestDirtyRectGateReducesCrossings is the regression guard for the dirty-rect
// gate + region recomposite: the optimised path must issue strictly fewer
// wasm<->JS bridge crossings than the whole-screen baseline in every scenario,
// an idle desktop must cost NOTHING, and a drag / animation must recomposite
// only a region (well under the whole-screen baseline).
func TestDirtyRectGateReducesCrossings(t *testing.T) {
	got := runBench(t)
	for _, sc := range []string{"idle", "drag", "anim"} {
		v, ok := got[sc]
		if !ok {
			t.Fatalf("missing RESULT for scenario %q", sc)
		}
		base, opt := v[0], v[1]
		if base <= 0 {
			t.Fatalf("%s: baseline crossings should be positive, got %d", sc, base)
		}
		if opt >= base {
			t.Errorf("%s: optimised (%d) should be < baseline (%d)", sc, opt, base)
		}
	}
	// An idle desktop composites nothing after warm-up: zero crossings.
	if got["idle"][1] != 0 {
		t.Errorf("idle: optimised crossings should be 0, got %d", got["idle"][1])
	}
	// A drag across the dense 8-window grid re-touches whatever the moved
	// window's old+new extent overlaps, so its win is more modest than a
	// localized commit — but the region walk still repaints materially less than
	// the whole screen. Require at least a 20% reduction (observed ~-36%).
	if d := got["drag"]; d[1]*5 >= d[0]*4 {
		t.Errorf("drag: optimised (%d) should be at least 20%% under the baseline (%d)", d[1], d[0])
	}
	// One SMALL animating window in an empty corner — the blinking-cursor case:
	// its commit damages only its own region, so the frame repaints just that
	// window (not the other 8) and stays well under half the whole-screen
	// baseline (observed ~-68%).
	if a := got["anim"]; a[1]*2 >= a[0] {
		t.Errorf("anim: optimised (%d) should be well under half the baseline (%d)", a[1], a[0])
	}
}
