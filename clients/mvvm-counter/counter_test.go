// Copyright (c) 2026 the wasmdesk/wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package mvvmcounter

import (
	"strings"
	"testing"
)

// TestCounterDemo runs the embedded counter.rb through rbgo and asserts the
// MVVM -> widgets binding loop drove the render to its final state. The Ruby
// program raises if any binding step fails (the initial text, the bound text
// after three clicks, or the pixels not changing with state) and otherwise
// prints a single "OK count=3 label=count: 3" line, so a matching line here is
// end-to-end proof that a `require "mvvm"` Observable bound a `require "widgets"`
// UI and the render reflected the observed state.
func TestCounterDemo(t *testing.T) {
	var buf strings.Builder
	if err := Run(&buf); err != nil {
		t.Fatalf("Run: %v\noutput:\n%s", err, buf.String())
	}
	got := strings.TrimSpace(buf.String())
	if want := "OK count=3 label=count: 3"; got != want {
		t.Fatalf("demo output = %q, want %q", got, want)
	}
}

// TestScriptEmbedded guards that the demo source is embedded and requires both
// adapters -- the two libraries this consumer exists to exercise together.
func TestScriptEmbedded(t *testing.T) {
	for _, req := range []string{`require "mvvm"`, `require "widgets"`} {
		if !strings.Contains(Script, req) {
			t.Errorf("embedded Script missing %s", req)
		}
	}
}
