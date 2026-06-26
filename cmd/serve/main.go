// Command serve is wasmbox's development HTTP server. It serves the current
// working directory and sets the headers a `SharedArrayBuffer`-using page
// needs in 2026 Chromium:
//
//   Cross-Origin-Opener-Policy: same-origin
//   Cross-Origin-Embedder-Policy: require-corp
//
// plus the application/wasm MIME type for *.wasm and a no-cache policy so the
// browser re-fetches after `task build`.
//
//go:build !js
// +build !js

package main

import (
	"flag"
	"log"
	"mime"
	"net/http"
	"strings"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address (host:port)")
	dir := flag.String("dir", ".", "directory to serve")
	// -no-coi reproduces a static host that cannot set headers (GitHub Pages):
	// it withholds COOP/COEP so the page must earn cross-origin isolation via
	// coi-serviceworker.js instead. Used by `task serve:pages` + the Pages
	// probe to validate the real deploy path locally.
	noCOI := flag.Bool("no-coi", false, "omit COOP/COEP headers (simulate a static host like GitHub Pages)")
	flag.Parse()

	// .wasm should always be application/wasm; some systems don't ship that
	// mapping by default. Add it before constructing the file server.
	_ = mime.AddExtensionType(".wasm", "application/wasm")
	_ = mime.AddExtensionType(".mjs", "text/javascript")
	_ = mime.AddExtensionType(".js", "text/javascript")

	fs := http.FileServer(http.Dir(*dir))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// Required for crossOriginIsolated → SharedArrayBuffer to be usable.
		// Withheld under -no-coi so coi-serviceworker.js must supply them, the
		// same as on GitHub Pages.
		if !*noCOI {
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Cross-Origin-Embedder-Policy", "require-corp")
		}
		// Dev-server defaults: hot reload + correct wasm MIME.
		h.Set("Cache-Control", "no-cache, no-store, must-revalidate")
		if strings.HasSuffix(r.URL.Path, ".wasm") {
			h.Set("Content-Type", "application/wasm")
		}
		fs.ServeHTTP(w, r)
	})

	log.Printf("wasmbox dev server: http://localhost%s (serving %s)", normalize(*addr), *dir)
	if err := http.ListenAndServe(*addr, handler); err != nil {
		log.Fatal(err)
	}
}

// normalize turns ":8080" into ":8080" (a no-op) and "localhost:8080" into
// ":8080" so the printed URL is always reachable via localhost. Returns addr
// unchanged if it does not parse as host:port.
func normalize(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return addr
	}
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr
	}
	return addr[i:]
}
