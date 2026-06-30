// Command wasmbox is the in-browser compositor: it bakes the Ruby window manager
// (compositor/*.rb) into the WebAssembly binary with //go:embed and runs it on
// the embedded go-embedded-ruby interpreter. There is no separate .rb fetched
// at runtime — the program ships inside the wasm.
//
// The Ruby source is split across compositor/*.rb so each file stays under
// ~1k lines (compositor.rb was 2810 lines as a single blob; navigating it
// became unreasonable). Files are loaded in alphabetic order, which is the
// dependency order — the 0N_ numeric prefixes encode it:
//
//	01_theme.rb            palette constants
//	02_frame.rb            Frame + OpenboxFrame + AquaFrame + FrameRegistry
//	03_window.rb           Window + ExternalWindow + DOMWindow
//	04_window_manager.rb   WindowManager
//	05_menu.rb             Menu + RootMenu
//	06_core.rb             Compositor
//	07_boot.rb             boot sequence (frame pick + spawn + start)
//
//go:build js && wasm

package main

import (
	"embed"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"syscall/js"

	ruby "github.com/go-embedded-ruby/ruby"
)

//go:embed compositor/*.rb
var compositorFS embed.FS

func main() {
	src, err := loadCompositor(compositorFS)
	if err != nil {
		js.Global().Set("wasmboxError", err.Error())
		return
	}
	// Run the embedded compositor. It installs DOM/Canvas event handlers and an
	// animation loop through the interpreter's JS bridge, which keep the VM (and,
	// via select{} below, the Go runtime) alive after Run returns.
	if err := ruby.Run(src, os.Stdout); err != nil {
		js.Global().Set("wasmboxError", err.Error())
		return
	}
	js.Global().Set("wasmboxReady", true)
	select {} // keep the runtime alive for the browser event/animation callbacks
}

// loadCompositor reads every compositor/*.rb file from fsys in alphabetic
// (= dependency) order and concatenates them into one Ruby program. The
// 0N_ numeric file prefixes guarantee the load order at glob time, so we
// do not need a hard-coded list — adding compositor/08_extra.rb just
// appends after 07_boot.rb. Exported (lowercase first letter via a wrapper
// would not be exported; keep it package-private — cmd/rbtest has its own
// off-wasm twin that does the same walk over the same disk dir).
func loadCompositor(fsys fs.FS) (string, error) {
	entries, err := fs.ReadDir(fsys, "compositor")
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".rb") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		b, err := fs.ReadFile(fsys, path.Join("compositor", n))
		if err != nil {
			return "", err
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}
