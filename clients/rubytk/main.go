// Command rubytk is a wasmbox external client whose UI and logic are authored
// entirely in Ruby, proving the go-ruby-widgets (`require "widgets"`) path
// end-to-end. Every other wasmbox client compiles a Go toolkit scene to wasm;
// rubytk instead compiles only a thin host shell — this file — that embeds the
// go-embedded-ruby interpreter and runs the Ruby program in
// internal/scene/app.rb.
//
// The shell is deliberately minimal, mirroring the compositor's own main.go: it
// runs the Ruby source and then parks so the browser event/render callbacks the
// Ruby program registered (through the JS bridge) keep the Go runtime alive. All
// scene construction, rendering (Widgets.render -> RGBA -> base64 -> the SAB via
// worker.js's __wbPresent) and input handling (Widgets.dispatch) happen in
// Ruby. On a parse/compile/runtime error the message is published on
// globalThis.wasmboxError, which the loader surfaces as an error window.
//
//go:build js && wasm

package main

import (
	"os"
	"syscall/js"

	ruby "github.com/go-embedded-ruby/ruby"

	"github.com/wasmdesk/wasmbox/clients/rubytk/internal/scene"
)

func main() {
	if err := ruby.Run(scene.Source(), os.Stdout); err != nil {
		js.Global().Set("wasmboxError", err.Error())
		return
	}
	// Keep the runtime alive for the JS bridge callbacks (the "wbinput" input
	// listener + render loop the Ruby program installed).
	select {}
}
