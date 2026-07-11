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

// TestDirtyRectAndSpriteCacheReduceCrossings is the regression guard for the
// two optimisations: the optimised path must issue strictly fewer wasm<->JS
// bridge crossings than the pre-optimisation baseline in every scenario, an
// idle desktop must cost nothing, and a drag / animation must recomposite only
// a region (well under the whole-screen baseline).
func TestDirtyRectAndSpriteCacheReduceCrossings(t *testing.T) {
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
	// A drag must recomposite only a region — comfortably under half the
	// whole-screen baseline (the guard is loose; observed is ~-70%).
	if d := got["drag"]; d[1]*2 >= d[0] {
		t.Errorf("drag: optimised (%d) should be well under half the baseline (%d)", d[1], d[0])
	}
	// One animating window amid 8 static decorations: the static chrome is
	// served from the sprite cache, so the animated frame stays a region.
	if a := got["anim"]; a[1]*2 >= a[0] {
		t.Errorf("anim: optimised (%d) should be well under half the baseline (%d)", a[1], a[0])
	}
}
