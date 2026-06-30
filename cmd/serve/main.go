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
	"net/http/httputil"
	"net/url"
	"os"
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
	// -code-server-url reverse-proxies an upstream code-server / vscodium under
	// /code-server/ so the dom-window iframe is SAME-ORIGIN as the wasmbox page.
	// Required because wasmbox sets Cross-Origin-Embedder-Policy: require-corp
	// (for SharedArrayBuffer) and code-server sends no Cross-Origin-Resource-
	// Policy header -- a direct cross-origin iframe is blocked. The reverse
	// proxy serves under the same origin so CORP doesn't apply + injects the
	// COOP/COEP headers the page also gets.
	codeServerURL := flag.String("code-server-url", "", "reverse-proxy this upstream under /code-server/ (e.g. http://127.0.0.1:8443); also reads $WASMBOX_CODE_SERVER_URL when the flag is empty so the wasmdesk-up orchestrator can opt-in without growing its command line")
	// -default-frame=NAME redirects bare "/" + "/index.html" to
	// "/?frame=NAME" so an instance dedicated to one decoration style
	// (e.g. wasmdesk-up's port-8081 instance that used to spawn
	// wasmaqua-serve) lands users on the right look without the URL
	// query-param dance. NAME is forwarded verbatim — the Ruby
	// compositor's FrameRegistry validates it; an unknown name falls
	// back to OpenboxFrame at the Ruby boot.
	defaultFrame := flag.String("default-frame", "", "redirect / to /?frame=NAME so the default landing page uses that Frame preset (e.g. -default-frame=aqua reproduces the legacy wasmaqua-serve experience)")
	flag.Parse()
	if *codeServerURL == "" {
		*codeServerURL = os.Getenv("WASMBOX_CODE_SERVER_URL")
	}

	// .wasm should always be application/wasm; some systems don't ship that
	// mapping by default. Add it before constructing the file server.
	_ = mime.AddExtensionType(".wasm", "application/wasm")
	_ = mime.AddExtensionType(".mjs", "text/javascript")
	_ = mime.AddExtensionType(".js", "text/javascript")

	fs := http.FileServer(http.Dir(*dir))

	// Reverse proxy for /code-server/* (when -code-server-url is set). We
	// strip the /code-server prefix before forwarding so the upstream sees
	// "/" -- code-server's routes are root-anchored. The Director also
	// rewrites Host so the upstream sees a same-origin request (code-server
	// rejects unexpected Host headers in -trusted-origins mode). On the
	// response side we strip code-server's Set-Cookie SameSite=Strict so
	// the cookie isn't dropped when the iframe runs under a different origin,
	// and we leave the response body untouched -- the page-level COOP/COEP
	// headers below are set on the OUTER response, not the iframe content.
	var codeProxy *httputil.ReverseProxy
	if *codeServerURL != "" {
		u, err := url.Parse(*codeServerURL)
		if err != nil {
			log.Fatalf("invalid -code-server-url %q: %v", *codeServerURL, err)
		}
		codeProxy = httputil.NewSingleHostReverseProxy(u)
		origDirector := codeProxy.Director
		codeProxy.Director = func(r *http.Request) {
			origDirector(r)
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/code-server")
			if r.URL.Path == "" {
				r.URL.Path = "/"
			}
			r.Host = u.Host
		}
		log.Printf("wasmbox dev server: reverse-proxying /code-server/ -> %s", *codeServerURL)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// -default-frame redirect: bare "/" or "/index.html" without an
		// existing ?frame= query gets bounced to "/?frame=NAME" so the
		// landing page renders with the operator's chosen Frame. Done
		// BEFORE setting headers — the redirect is a fresh response.
		if *defaultFrame != "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			if (r.URL.Path == "/" || r.URL.Path == "/index.html") && r.URL.Query().Get("frame") == "" {
				q := r.URL.Query()
				q.Set("frame", *defaultFrame)
				http.Redirect(w, r, "/?"+q.Encode(), http.StatusTemporaryRedirect)
				return
			}
		}
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
		// /code-server/* -> upstream code-server (when configured). Done AFTER
		// the COOP/COEP headers above so the iframe response carries them too;
		// COEP:require-corp on the iframe document makes the browser refuse to
		// load nested cross-origin resources without their own CORP header --
		// but since the entire iframe pipeline is now same-origin-via-proxy,
		// every nested fetch is also same-origin and COEP is trivially
		// satisfied.
		if codeProxy != nil && strings.HasPrefix(r.URL.Path, "/code-server") {
			codeProxy.ServeHTTP(w, r)
			return
		}
		// /loom/* serves the pre-built weft-loom Svelte/CodeMirror SPA
		// from clients/loom/dist. The SPA was built with Vite
		// `--base=/loom/` so every asset reference is already
		// prefix-correct. No proxy needed; collab/compile WS to the
		// upstream weft-loom-server is opt-in via wasmdesk-up's
		// orchestrator (and not yet plumbed).
		if strings.HasPrefix(r.URL.Path, "/loom/") || r.URL.Path == "/loom" {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/loom")
			if r.URL.Path == "" {
				r.URL.Path = "/"
			}
			http.StripPrefix("", http.FileServer(http.Dir("clients/loom/dist"))).ServeHTTP(w, r)
			return
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
