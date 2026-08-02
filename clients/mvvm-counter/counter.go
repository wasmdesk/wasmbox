// Copyright (c) 2026 the wasmdesk/wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package mvvmcounter is a self-contained wasmbox demo of the MVVM data-binding
// stack: a `require "mvvm"` Observable drives a `require "widgets"` UI, run
// through the embedded go-embedded-ruby interpreter (rbgo). It is the first
// consumer wiring the mvvm and widgets adapters together end to end.
//
// Unlike the pixel-painting clients (hello, calculator, ...), which are Go
// js/wasm programs, this demo's UI and state live entirely in Ruby: [Script]
// (counter.rb) is the program, and [Run] executes it on a fresh rbgo VM. The
// Ruby side owns the Observable, the widget tree, and the binding loop; the Go
// side only hosts the interpreter. Run it directly with
// `rbgo clients/mvvm-counter/counter.rb`, or drive it from Go via [Run].
package mvvmcounter

import (
	_ "embed"
	"io"

	ruby "github.com/go-embedded-ruby/ruby"
)

// Script is the embedded counter.rb demo source.
//
//go:embed counter.rb
var Script string

// Run executes the embedded counter.rb demo on a fresh rbgo VM, writing its
// output to out. It returns any parse, compile or runtime error (the Ruby side
// raises on a failed binding assertion); on success out receives the demo's
// single "OK ..." line.
func Run(out io.Writer) error {
	return ruby.Run(Script, out)
}
