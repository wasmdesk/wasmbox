<h1 align="center">wasmbox</h1>
<p align="center"><strong>A window manager, in your browser, written in Ruby.</strong></p>

<p align="center">
  A Wayland-inspired single-instance compositor with an Openbox-style
  window-manager policy — pure Ruby, running in WebAssembly, no server.
</p>

<p align="center">
  <a href="https://github.com/wasmdesk"><img alt="part of wasmdesk" src="https://img.shields.io/badge/wasmdesk-the%20WASM%20desktop-1a7f37?style=flat-square"></a>
  <a href="https://github.com/go-embedded-ruby/ruby"><img alt="built on go-embedded-ruby" src="https://img.shields.io/badge/runs%20on-go--embedded--ruby-9B1C2E?style=flat-square"></a>
  <img alt="WebAssembly" src="https://img.shields.io/badge/WebAssembly-CGO%3D0-654FF0?style=flat-square&logo=webassembly&logoColor=white">
  <a href="LICENSE"><img alt="License: BSD-3-Clause" src="https://img.shields.io/badge/license-BSD--3--Clause-blue?style=flat-square"></a>
</p>

---

`wasmbox` is a self-contained browser demo: a **Wayland-inspired, single-instance
compositor** with an **Openbox-style window-manager policy**, written entirely in
**pure Ruby** and rendered to a `<canvas>` through the interpreter's interactive
JS bridge. The Ruby program owns the desktop — it composites window surfaces,
maintains the stacking order, applies focus and placement policy, draws minimal
decorations, and runs its own `requestAnimationFrame` render loop.

It runs in one WebAssembly instance of the pure-Go (CGO=0)
[`rbgo`](https://github.com/go-embedded-ruby/ruby) interpreter — there is no
server-side code and no JavaScript application logic; the page is just a loader.
It builds on `rbgo`'s `JS` bridge (a `JS` module plus `JS::Ref` handles that let
Ruby reach the DOM and the Canvas 2D context, register event listeners, and
schedule animation frames).

## What it is

- **`Compositor`** — holds the canvas, fits it to the viewport, installs input
  handlers, and drives a `JS.raf` render loop that clears the desktop and paints
  every window bottom-to-top each frame. A small HUD shows the window count, a
  smoothed **FPS** reading and a frame counter.
- **`WindowManager`** — owns the **stacking order** (an array whose last element
  is topmost), a **click-to-focus / raise-on-focus** policy, **cascade placement**
  for new windows, and an Alt+Tab-style focus cycle.
- **`Window`** — a client surface plus its decoration geometry, with hit-testing
  helpers for the titlebar, body, close box and resize corner.

The window-management logic (geometry, hit-testing, stacking, placement, focus)
is plain Ruby with no reference to `JS`, so it is unit-tested natively; only the
rendering methods touch the bridge.

## Build & serve

Uses [Task](https://taskfile.dev):

```sh
task          # list the available tasks
task build    # clones + builds rbgo.wasm and copies wasm_exec.js
task serve    # builds, then serves http://localhost:8080/
```

(Point `RBGO_SRC` at a local go-embedded-ruby checkout to skip the clone:
`RBGO_SRC=../ruby task build`.)

Then open <http://localhost:8080/>. The page instantiates `rbgo.wasm`, waits for
the interpreter's `rbgoReady` flag, fetches `compositor.rb`, and runs it through
the exposed `rbgoEval(src)` function. Any static server that serves `.wasm` with
the `application/wasm` MIME type works.

## Controls

- **Drag a titlebar** — move a window.
- **Drag the bottom-right corner** — resize (clamped to a minimum size).
- **Click** a window — focus and raise it.
- **×** in the titlebar — close.
- **Right-click the desktop** — root menu (spawn a window).
- **Right-click a window** — context menu (raise / close).
- **Tab** — cycle focus; **Esc** — dismiss a menu.

## Honest scope — this is "step A"

This is the **single-instance** step: one WASM instance is *both* the compositor
and every client, so the windows are surfaces the same Ruby program owns, not
independent programs. It keeps the whole desktop in one readable Ruby file and is
a faithful showcase of event-driven Ruby — but it is **not** a real multi-process
compositor.

A true split — closer to how Wayland actually works — would put the compositor
and each client in **separate Web Workers / WASM instances** speaking a
**`postMessage` + `SharedArrayBuffer`** protocol: clients render into shared
buffers and submit damage/commit messages; the compositor owns the final canvas,
the stacking order and input routing, and dispatches events to the focused
client. That is the natural **step B**, and the clean split here between pure WM
policy and the JS-touching compositor is meant to make that move straightforward.

## Validation note

There is no headless browser in CI, so the in-browser rendering and interaction
are validated **manually**. What is checked automatically: `compositor.rb` parses
and compiles, the pure WM logic is exercised with native assertions, and the
`GOOS=js GOARCH=wasm` interpreter build compiles.

## Part of [wasmdesk](https://github.com/wasmdesk)

`wasmdesk` is a family for a WebAssembly desktop built on pure-Go Ruby. `wasmbox`
is its compositor + window manager; future repositories will cover the
inter-client protocol and standalone clients.
</content>
