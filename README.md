<p align="center">
  <img src="https://raw.githubusercontent.com/wasmdesk/brand/main/png/color/256/wasmdesk.png" alt="wasmdesk" width="88" height="88">
</p>

<h1 align="center">wasmbox</h1>
<p align="center"><strong>A window manager, in your browser, written in Ruby.</strong></p>

<p align="center">
  A Wayland-inspired compositor with an Openbox-style window-manager policy —
  pure Ruby, running in WebAssembly, driving real external clients, no server.
</p>

<p align="center">
  <a href="https://github.com/wasmdesk"><img alt="part of wasmdesk" src="https://img.shields.io/badge/wasmdesk-the%20WASM%20desktop-1a7f37?style=flat-square"></a>
  <a href="https://github.com/go-embedded-ruby/ruby"><img alt="built on go-embedded-ruby" src="https://img.shields.io/badge/runs%20on-go--embedded--ruby-9B1C2E?style=flat-square"></a>
  <img alt="WebAssembly" src="https://img.shields.io/badge/WebAssembly-CGO%3D0-654FF0?style=flat-square&logo=webassembly&logoColor=white">
  <a href="LICENSE"><img alt="License: BSD-3-Clause" src="https://img.shields.io/badge/license-BSD--3--Clause-blue?style=flat-square"></a>
</p>

---

`wasmbox` is a self-contained browser desktop: a **Wayland-inspired compositor**
with an **Openbox-style window-manager policy** (Aqua-style + others selectable
at boot), written entirely in **pure Ruby** and rendered to a `<canvas>` through
the interpreter's interactive JS bridge. The Ruby program owns the desktop — it
composites window surfaces, maintains the stacking order, applies focus and
placement policy, draws decorations, runs a desktop-environment shell (dock,
tray, notifications, applets, wallpaper, Spotlight, Exposé) and drives its own
`requestAnimationFrame` render loop.

The compositor runs in one WebAssembly instance of the pure-Go (CGO=0)
[`rbgo`](https://github.com/go-embedded-ruby/ruby) interpreter, with the Ruby
source (split across `compositor/*.rb`) **baked into the wasm binary** via
`//go:embed` — there is no server-side application logic and nothing of the
compositor is fetched at runtime; the page is just a loader. It builds on
`rbgo`'s `JS` bridge (a `JS` module plus `JS::Ref` handles that let Ruby reach
the DOM and the Canvas 2D context, register event listeners, and schedule
animation frames), and renders its widgets with the pure-Go
[`go-widgets/toolkit`](https://github.com/go-widgets/toolkit) (AA-rendered,
OpenType-shaped text) bound into Ruby.

Windows are no longer all the same program: **external clients each run in their
own Web Worker / WASM instance** and paint into a `SharedArrayBuffer`, speaking a
documented wire protocol to the compositor (see
[`docs/protocol.md`](docs/protocol.md)). The single-instance demo was "step A";
the code today is **step C.1** — the compositor itself lives in a dedicated
worker rendering into an `OffscreenCanvas`, and each client talks to it over a
direct `MessageChannel`.

## Live demo

<https://wasmdesk.github.io/wasmbox/> — the desktop, served from GitHub Pages
(cross-origin isolation is earned client-side via `coi-serviceworker.js`, so the
`SharedArrayBuffer` surfaces work without special server headers).

## Boot experience

The page shows a **determinate boot splash** from the very first paint: the
`wasmbox.wasm` binary is fetched with a streaming reader that drives a real
progress bar (bytes received vs `Content-Length`), then the bar completes as the
Go runtime instantiates and the embedded Ruby compositor starts. The splash then
fades and is removed from the DOM (no lingering timer/rAF). If boot fails, an
error surface replaces it.

## Desktop environment

Beyond bare window management, the compositor ships a small DE shell, all in Ruby:

- **Dock** — the [`wasmdock`](https://github.com/wasmdesk/wasmdock) client:
  bottom-anchored, magnifying, with running/active indicators; launches apps
  through the `launch` protocol message.
- **System tray / status strip**, **desktop notification toasts** (icon +
  multi-line + multi-action, from the go-widgets v0.86 refinements), and a
  **big clock**.
- **Desktop applets** behind the windows — live clock, a real **Calendar**
  applet, and monitor tiles.
- **Selectable wallpaper** (gradients + a bundled image), persisted to
  `localStorage`.
- **Window snapping / half-tiling** (`Super`+arrow, or drag to a screen edge).
- **Visual Alt-Tab** switcher and **Exposé** (`F3` "show all windows"), both with
  live per-window framebuffer thumbnails.
- **Spotlight** launcher (`⌘`/`Ctrl`+`Space`) — a centered fuzzy-search command
  palette over the launchable apps.

## Clients

Clients live under `clients/` and are built for `js/wasm`. Most are pure-Go
toolkit consumers; one is authored entirely in Ruby:

| Client | What it is |
|--------|------------|
| `hello` | reference minimal external client (the protocol smoke test) |
| `dock` | the [`wasmdock`](https://github.com/wasmdesk/wasmdock) dock, built from the sibling repo |
| `terminal`, `files` | placeholder app windows with distinct surfaces/titles |
| `code` | a VS Code-styled editor shell |
| `showcase` | a 7-tab Notebook exercising every toolkit widget family |
| `calculator` | Entry + 5×4 Button grid — a small focused toolkit consumer |
| `notepad` | multi-doc TextView editor |
| `settings` | a WhiteSur (macOS Big Sur) preferences panel (Switch/Scale rows) |
| `browser` | a WhiteSur/Safari-styled browser shell (see note below) |
| `rubytk` | a Tip Calculator whose **whole UI + logic are Ruby** via `require "widgets"` |
| `quake` | pure-Go Quake 1 from the sibling [`go-quake1`](https://github.com/go-quake1) repo, streaming its assets from an OCI registry |

Clients are spawned two ways: from a static worker URL (the compositor's own
asset tree) or pulled at runtime as an **OCI artifact** via
[`ociapps`](https://github.com/wasmdesk/ociapps) (`?ociapps=1`). The set of
launchable ids is a compositor-owned trust boundary (`LAUNCHABLE` in
`compositor/04_window_manager.rb`): a `launch` message carries only an opaque id,
never a path or command line.

> **Streaming browser (in review).** A new `clients/browser` variant renders
> **real web pages** by proxying them through the
> [`go-webengine/browserproxy`](https://github.com/go-webengine) (a WebSocket
> feeds rendered frames the client blits into its surface, behind a go-widgets
> loading skeleton). It is proposed in **PR #93** and depends on the user hosting
> the proxy, so it is not yet on `main`; the bundled `browser` here is the
> offline WhiteSur/Safari shell (the page is `COEP: require-corp`, so it cannot
> embed live cross-origin sites directly — tiles open local placeholder pages).

## Pick a window decoration

Wasmbox ships **16 registered decoration presets** (2 layouts × 7 GTK palettes,
plus the 2 plain layouts). Pick one via a URL query param:

```
http://localhost:8080/                              # OpenboxFrame (default)
http://localhost:8080/?frame=aqua                   # AquaFrame — subsumes wasmaqua
http://localhost:8080/?frame=aqua-whitesur-light    # Aqua + WhiteSur ≈ macOS Big Sur
http://localhost:8080/?frame=aqua-whitesur-dark     # ≈ Big Sur dark
http://localhost:8080/?frame=openbox-juno           # Openbox + Juno teal palette
http://localhost:8080/?frame=aqua-adwaita-{light,dark}
http://localhost:8080/?frame=openbox-solarized-{light,dark}
```

Or run `wasmbox-serve -default-frame=NAME` to make a whole instance default to
one preset (bare `/` 307-redirects to `/?frame=NAME`). The registry lives in
[`compositor/02_frame.rb`](compositor/02_frame.rb) — adding a new frame is one
entry in `FrameRegistry::TABLE`. You can also switch at runtime from the
root-menu **Frame** submenu.

The sibling [`wasmdesk/wasmaqua`](https://github.com/wasmdesk/wasmaqua) repo is
**archived** as of 2026-06-30 — its macOS-Aqua chrome is now the `AquaFrame`
preset here.

## Architecture

- **Threading (step C / C.1).** The **compositor** runs in a *dedicated Web
  Worker*, rendering into an `OffscreenCanvas` transferred from the page. The
  **main thread** is a thin shell — it owns the `<canvas>` element, relays DOM
  input to the compositor worker, and mirrors `localStorage`; it runs no
  Ruby/wasm. Each **external client** runs in its own worker and talks to the
  compositor over a **direct `MessageChannel`** (`port2` handed to the client at
  spawn), not relayed through the main thread. See
  [`docs/protocol.md`](docs/protocol.md) for the wire contract.
- **`Compositor`** — holds the canvas, fits it to the viewport, installs input
  handlers, and drives a `JS.raf` render loop. A **frame-level dirty gate** skips
  the full-frame recomposite when nothing changed, so an idle desktop costs
  ~0 CPU (this fixed a 100%-CPU idle regression in Firefox). A small HUD shows the
  window count, a smoothed **FPS** reading and a frame counter.
- **`WindowManager`** — owns the **stacking order** (last element topmost), a
  **click-to-focus / raise-on-focus** policy, **cascade placement**, workspaces,
  minimize-into-dock, the `launch` trust boundary, and the focus cycle.
- **`Window`** — a client surface plus its decoration geometry, with roles
  (`window` / `panel` / `popup`), hit-testing helpers, and per-frame damage.

The window-management logic (geometry, hit-testing, stacking, placement, focus)
is plain Ruby with no reference to `JS`, so it is unit-tested natively; only the
rendering methods touch the bridge.

## Wasm size

The compositor wasm is built with `-trimpath -ldflags '-s -w'`, and the
`js/wasm` build drops the network backends the browser cannot use anyway. That
cut the binary from **~175 MB to ~86 MB** (about **18 MB gzipped** over the
wire), which is what the streaming boot splash downloads.

## Build & serve

Uses [Task](https://taskfile.dev):

```sh
task          # list the available tasks
task build    # build wasmbox.wasm (embeds compositor/*.rb) + every client + wasm_exec.js
task serve    # build, then serve http://localhost:8080/ with COOP/COEP headers
task test     # native Go + Ruby unit tests (no browser)
task clean    # remove the build artifacts
```

`wasmbox` is a Go module that imports the interpreter, so `task build` is a plain
`go build` for the `js/wasm` target (`GOOS=js GOARCH=wasm CGO_ENABLED=0`) — Go
fetches the interpreter as a dependency; there is no separate checkout to clone.
External clients built from sibling repos (the dock, Quake) resolve those repos
via `DOCK_DIR` / `ENGINE_DIR` (see the Taskfile).

The `SharedArrayBuffer` client surfaces require **cross-origin isolation**
(COOP/COEP), which `python3 -m http.server` does not set — that is why the demo
ships its own dev server, `cmd/serve` (`task serve`). For the GitHub-Pages model
(no server headers), `task serve:pages` assembles the site and relies on
`coi-serviceworker.js` to supply isolation, reproducing the real deploy locally.
The `?ociapps=1` flow (`task demo-wasmbox-ociapps`) streams every client from a
local OCI registry instead of the static tree.

Then open <http://localhost:8080/>. The page instantiates `wasmbox.wasm`, the Go
runtime runs the embedded compositor, and the canvas takes over once ready. Any
static server that serves `.wasm` as `application/wasm` **with** COOP/COEP works.

## Controls

- **Drag a titlebar** — move a window. **Drag the bottom-right corner** — resize.
- **Click** a window — focus and raise it. **×** in the titlebar — close.
- **Right-click the desktop** — root menu (Applications submenu, Frame submenu,
  wallpaper). **Right-click a window** — context menu.
- **Tab** — cycle focus; **Alt+Tab** — visual switcher; **Esc** — dismiss.
- **Super+arrow** / drag-to-edge — snap / half-tile. **F3** — Exposé.
- **⌘/Ctrl+Space** — Spotlight launcher.

## Validation note

The in-browser rendering is validated with a **headless browser** (Playwright
driving Chrome): after boot the splash overlay is removed, the `<canvas>` is
fully painted (desktop + cascaded windows + DE shell), external clients spawn and
commit damage, an OCI-streamed client is pulled and rendered, and the console
reports the compositor started. The `GOOS=js GOARCH=wasm` binary build is also
checked in CI, along with the native Ruby/Go unit tests.

## Part of [wasmdesk](https://github.com/wasmdesk)

`wasmdesk` is a family for a WebAssembly desktop built on pure-Go Ruby. `wasmbox`
is its compositor + window manager;
[`wasmdock`](https://github.com/wasmdesk/wasmdock) is the dock client; and
[`ociapps`](https://github.com/wasmdesk/ociapps) streams client apps from any OCI
registry.
