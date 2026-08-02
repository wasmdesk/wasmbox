// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"strings"
	"testing"

	"github.com/go-ruby-parser/parser"
)

// TestSourceNotEmpty guards the embed: an empty Source means app.rb went
// missing or the //go:embed directive drifted.
func TestSourceNotEmpty(t *testing.T) {
	if strings.TrimSpace(Source()) == "" {
		t.Fatal("scene.Source() is empty; app.rb embed is broken")
	}
}

// TestSourceParses runs the embedded Ruby through the very parser rbgo uses, so
// a syntax slip in app.rb (e.g. an rbgo-unsupported multi-line ternary) fails
// `go test` here instead of only surfacing in the browser at runtime.
func TestSourceParses(t *testing.T) {
	if _, err := parser.Parse(Source()); err != nil {
		t.Fatalf("app.rb does not parse as rbgo Ruby: %v", err)
	}
}

// TestSourceUsesWidgetsBinding asserts the scene is driven through the
// go-ruby-widgets path this client exists to prove — require "widgets", a
// Widgets.render call and Widgets.dispatch routing — not hand-rolled drawing.
func TestSourceUsesWidgetsBinding(t *testing.T) {
	src := Source()
	for _, want := range []string{
		`require "widgets"`,
		`require "base64"`,
		"Widgets.render",
		"Widgets.dispatch",
		"__wbPresent",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("app.rb missing expected marker %q", want)
		}
	}
}
