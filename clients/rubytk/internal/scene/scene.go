// SPDX-License-Identifier: BSD-3-Clause
//
// Package scene carries the Ruby program that IS the rubytk client. Unlike the
// other wasmbox clients — whose scene is Go toolkit code — rubytk's scene is
// authored entirely in Ruby (app.rb) and runs on the embedded go-embedded-ruby
// interpreter through `require "widgets"` (the go-ruby-widgets binding over the
// go-widgets pixel toolkit).
//
// The Ruby source is baked into the wasm with //go:embed, exactly as the
// compositor bakes in compositor/*.rb. Keeping it behind a package-level
// accessor (rather than embedding directly in the js/wasm main.go) lets the
// program be exercised off-wasm: scene_test.go parses Source() with the same
// go-ruby-parser rbgo uses, so a syntax slip is caught by `go test`, not only
// in the browser.
package scene

import _ "embed"

//go:embed app.rb
var source string

// Source returns the embedded Ruby program: the complete Tip Calculator client
// (widget tree, arithmetic model, render loop and input routing). main.go hands
// it to ruby.Run; the returned string is never empty (the embed fails the build
// otherwise).
func Source() string { return source }
