// Command wasmbox is the in-browser compositor: it bakes the Ruby window manager
// (compositor.rb) into the WebAssembly binary with //go:embed and runs it on the
// embedded go-embedded-ruby interpreter. There is no separate .rb fetched at
// runtime — the program ships inside the wasm.
//
//go:build js && wasm

package main

import (
	_ "embed"
	"os"
	"syscall/js"

	ruby "github.com/go-embedded-ruby/ruby"
)

//go:embed compositor.rb
var compositor string

func main() {
	// Run the embedded compositor. It installs DOM/Canvas event handlers and an
	// animation loop through the interpreter's JS bridge, which keep the VM (and,
	// via select{} below, the Go runtime) alive after Run returns.
	if err := ruby.Run(compositor, os.Stdout); err != nil {
		js.Global().Set("wasmboxError", err.Error())
		return
	}
	js.Global().Set("wasmboxReady", true)
	select {} // keep the runtime alive for the browser event/animation callbacks
}
