# wasmbox external-client protocol

A Wayland-inspired, multi-instance compositor. The wire contract below was
introduced in **step B** and is unchanged; **steps C / C.1 moved where the
endpoints run** (see *Threading + memory*), so the older "compositor on the
main thread" wording is gone.

* **Step A** — every window lives inside one WebAssembly instance:
  `compositor.rb` owns the canvas *and* every "client". Not multi-instance.
* **Step B** — **external clients**, each in its own Web Worker (one wasm
  instance per worker), speaking to the compositor over `postMessage` + a
  `SharedArrayBuffer` (SAB) pixel surface (the `wl_shm` analog).
* **Step C** — the **compositor itself** moves off the main thread into a
  **dedicated Web Worker**: the page transfers the canvas via
  `transferControlToOffscreen()` and becomes a thin shell that owns the
  `<canvas>` element + relays DOM input. Ruby/wasm activity is all in the
  compositor worker.
* **Step C.1** — each client talks to the compositor over a **direct
  worker↔worker `MessageChannel`** (a `MessagePort` handed to the client at
  spawn), not relayed through the main thread.

Clients are spawned two ways: from a static worker URL
(`wasmboxSpawnExternal`) or pulled at runtime as an **OCI artifact**
(`wasmboxSpawnFromOCI` → `OCIAppsLoader`, see `ociapps-loader.js`); both end
on the same C.1 port handoff.

The compositor owns the canvas, the stacking order, focus and input routing. A
client owns only its surface (an off-screen byte buffer) and posts **commit**
messages naming the changed sub-rectangle; the compositor reads the SAB and
blits the damage onto the canvas at the window's current position.

This document is the wire contract. The Ruby end is
`WindowManager#handle_client_message` plus `ExternalWindow`; the JS end is
`clients/sdk/sdk.js` plus the worker bootloader (`compositor.worker.js`).

## Threading + memory

* The **compositor** runs in a **dedicated Web Worker** (step C): a single Go
  wasm instance hosting `compositor.rb`, rendering into an `OffscreenCanvas`
  transferred from the page. The **main thread** is a thin shell — it owns the
  `<canvas>` element, relays DOM input events to the compositor worker, and
  relays write-through `localStorage`. It runs no Ruby/wasm.
* Each **external client** runs in its own `Worker(workerUrl)` (or a Worker
  built from blob URLs, for OCI-streamed clients). The worker loads its own
  `wasm_exec.js` + client wasm, then instantiates the Go runtime.
* Client ↔ compositor messaging uses a **direct `MessageChannel`** (step C.1):
  the compositor worker creates the channel, keeps `port1`, and transfers
  `port2` to the client worker at spawn. The lifecycle below flows over that
  port — the main thread is not in the path.
* The pixel surface for each window is a `SharedArrayBuffer` of
  `4 * width * height` bytes, allocated on the **client side** and posted
  back to the compositor with the `hello` reply window setup. (Nested workers
  inherit cross-origin isolation, so SAB works in the client workers.)
* SharedArrayBuffer requires the page to be served with
  `Cross-Origin-Opener-Policy: same-origin` and
  `Cross-Origin-Embedder-Policy: require-corp`. `cmd/serve` does that;
  `python3 -m http.server` does not.

## Lifecycle (compositor ↔ client)

```
client (worker)                          compositor (worker)
       │                                        │
       │ ───── hello { title, w, h, sab } ────► │
       │                                        │  WindowManager.spawn_external
       │ ◄──── welcome { window_id, w, h } ──── │
       │                                        │
       │ paint into SAB …                       │
       │ ───── commit { window_id, damage } ──► │  ExternalWindow.paint copies
       │                                        │     SAB[damage] onto canvas
       │ ◄──── input  { window_id, event } ──── │  forwarded by focus
       │ ───── set_title { window_id, title } ► │
       │ ◄──── closed { window_id, reason } ─── │
```

## Messages

All messages are plain JSON-cloneable objects with a `type` field. Sides:

* **C → S** — client (worker) to compositor (compositor worker), over the port.
* **S → C** — compositor (compositor worker) to client (worker), over the port.

### `hello`  (C → S)

The first message a client sends after `Worker` instantiation completes.

```js
{
  type: "hello",
  title: "hello world",
  w: 200, h: 150,             // requested surface size, pixels
  sab: SharedArrayBuffer,     // 4*w*h bytes, RGBA32, row-major top-left
  stride: 4 * 200,            // bytes per row; canonical = 4*w
  role: "window",             // optional surface role (see below); default "window"
  ctl: SharedArrayBuffer,     // optional 1×Int32 seqlock control word (tear-free; see below)
  parent: 7,                  // popups only: parent window_id to anchor to
  rel_x: 20, rel_y: 30,       // popups only: offset inside the parent's body
}
```

The SAB is transferred by reference (it is shared, not copied). The compositor
keeps it for the lifetime of the window and reads `damage` rectangles out of
it on every `commit`. `ctl` is optional — when present it enables the
tear-free seqlock (see *Tear-free presentation*); a client that omits it gets
the older "read whenever" behaviour. `parent` + `rel_x` + `rel_y` apply only to
`role: "popup"` (see *Popups* below).

**Surface roles** (`hello.role`, validated by the compositor — an unknown role
is treated as `"window"`):

* `"window"` — a normal, decorated, cascade-placed, focusable window.
* `"panel"` — a dock-style surface (the wlr-layer-shell analog): no
  decoration, bottom-center anchored, always-on-top, never draggable /
  closable / resizable, and excluded from the Alt-Tab focus cycle. Used by
  `clients/dock`.
* `"popup"` — a child surface anchored to a `parent` window (the xdg-popup /
  menu / tooltip analog): undecorated, placed at `(rel_x, rel_y)` inside the
  parent body, stacked directly above the parent, excluded from keyboard focus,
  and **grab-dismissed** — a click outside the popup closes it (a `closed`
  message). See *Popups* below.

### `welcome`  (S → C)

```js
{
  type: "welcome",
  window_id: 7,               // integer, unique to this compositor session
  granted_w: 200,
  granted_h: 150,
}
```

The granted size may be smaller than the requested size (the compositor may
clamp). The client should re-allocate its SAB if `granted_w/h` differ from
the requested values — but the bundled SDK does not implement clamping yet.

### `commit`  (C → S)

The client tells the compositor "I have written new pixels into the SAB —
copy this rectangle onto the screen". One commit per frame is the expected
cadence.

```js
{
  type: "commit",
  window_id: 7,
  damage: { x: 0, y: 0, w: 200, h: 150 },  // sub-rect inside the surface
}
```

`damage` is in surface-local coordinates. The compositor translates by the
window's current screen position. A `damage` covering the full surface is
always valid (the SDK uses that as its default).

### `set_title`  (C → S)

```js
{ type: "set_title", window_id: 7, title: "hello, click me" }
```

### `request_close`  (C → S)

```js
{ type: "request_close", window_id: 7 }
```

The compositor responds by unmapping the window and posting `closed` back.

### `request_resize`  (C → S)

```js
{ type: "request_resize", window_id: 7, w: 320, h: 240 }
```

Reserved; the SDK does not implement it yet (would need a fresh SAB).

### `launch`  (C → S)

A client (typically the dock `panel`) asks the compositor to start another
client. The message carries **only an app id** — never a URL, path, or argv:

```js
{ type: "launch", app: "terminal" }
```

The compositor resolves `app` through its `LAUNCHABLE` table (the **trust
boundary**: a fixed id → spawn-descriptor map). A descriptor is either a static
worker URL (→ `spawn_external`) or `{ oci: "<ref>" }` (→ `spawn_external_oci`,
which pulls the client as an OCI artifact). An id not in the table is ignored.
This keeps a client from ever causing the compositor to load an arbitrary URL.

### `input`  (S → C)

The compositor forwards a DOM-style event to the **focused** external window.
Coordinates are translated from screen-space to surface-local space.

```js
{
  type: "input",
  window_id: 7,
  event: {
    kind: "mousedown",    // mousedown | mouseup | mousemove | wheel | keydown | keyup
    x: 42, y: 17,         // surface-local pixels (mouse only)
    button: 0,            // 0=left 1=middle 2=right (mouse only)
    key: "a",             // KeyboardEvent.key (keyboard only)
    code: "KeyA",         // KeyboardEvent.code (keyboard only)
    dx: 0, dy: -1,        // wheel delta (wheel only)
  }
}
```

Only events that land on the focused window are dispatched. Decoration hits
(titlebar drag, close box, resize grip) are intercepted by the compositor
and never reach the client.

### `closed`  (S → C)

```js
{ type: "closed", window_id: 7, reason: "user" }    // "user" | "client"
```

Sent once. After this the worker may shut itself down (`self.close()`).

## Coordinate system

* The compositor's canvas has its origin at the top-left of the viewport.
* A window's "body" starts at `(window.x, window.y)`. Its titlebar sits
  *above* the body (`Theme::TITLE_H` pixels tall).
* The surface SAB covers exactly the body rectangle (no decorations). A
  client that wants to paint its own titlebar would need a larger surface
  *and* the compositor would need to suppress its decoration — see
  Roadmap (client-side decorations).
* `commit.damage` and `input.event.{x,y}` are **surface-local** (i.e.
  `(0,0)` is the top-left of the client surface, not the canvas).

## Reading the SAB from Ruby

Ruby code receives `sab` as a `JS::Ref` to the underlying
`SharedArrayBuffer`. The compositor wraps it once as a `Uint8ClampedArray`
view and keeps a sibling non-shared `ImageData` of the same size
(`wasmboxNewImageData`). On every commit, the JS helper
`wasmboxBlitFromSAB` copies the damage sub-rectangle out of the SAB view
into the ImageData's own buffer and then calls
`putImageData(image, x, y, dx, dy, dw, dh)`. The copy is needed because
modern Chromium refuses to construct an `ImageData` over a
SAB-backed array ("The provided Uint8ClampedArray value must not be
shared"); the SAB still avoids an extra structured-clone of the surface
across the worker/main boundary, which was the protocol's main goal.

## Tear-free presentation (seqlock)

A single-buffered surface can tear: the compositor's per-frame copy might run
while the client is mid-paint. The optional `ctl` control word fixes that with
a **seqlock** — no second buffer, so the incremental-damage model is preserved:

* **Client (writer, single thread):** bump `seq` to **odd** on the first pixel
  write of a frame (`Atomics.add`), and to **even** in `commit()`. Odd = "a
  frame is being painted".
* **Compositor (reader):** before copying, load `seq`; if it is **odd**, skip
  this frame's blit (keep the last complete frame). After copying into its
  private `ImageData`, load `seq` again; if it **changed**, the client wrote
  during the copy (a torn read) → discard it (don't `putImageData`). Only a
  copy bracketed by a stable, even `seq` is presented.

Because the compositor re-composites every window every `requestAnimationFrame`,
a skipped frame simply retries on the next raf once the client has committed —
so no update is lost, and a half-painted surface is never shown. A client that
sends no `ctl` keeps the older unsynchronised behaviour.

## Popups (child surfaces)

A popup is the xdg-popup / menu / tooltip primitive: a **child surface owned by
the same client as its parent window**. One worker can therefore drive several
surfaces — a window plus its popups — all multiplexed over the single
client↔compositor port. The SDK routes that traffic by surface: a `welcome` is
matched to the oldest surface still awaiting one (the compositor replies in
`hello` order over the ordered port), and everything else routes by `window_id`.

A client opens one with `parentClient.openPopup({ w, h, rel_x, rel_y })`, which
mints a second `WasmboxClient` with `role: "popup"`, `parent` set to the parent
window_id, and the relative offset. The compositor then:

* **places** the popup at `(parent.x + rel_x, parent.y + rel_y)` and stacks it
  directly above the parent (it owns its size — no MIN clamp);
* draws **no decoration** and keeps it **out of the keyboard-focus ring** (it
  still receives mouse input by hit-testing);
* **grabs the pointer**: a click inside the popup is forwarded to it; a click
  anywhere else dismisses every open popup (each gets a `closed`) and is
  consumed;
* **orphans** are cleaned up — closing a window also dismisses its popups.

`clients/hello` demonstrates it: clicking the window opens a small menu popup.

## Implemented since step B

* **Compositor in its own Worker** (step C) + **direct client↔compositor
  `MessageChannel`** (step C.1) — see *Threading + memory*.
* **Surface roles** — `panel` (dock / layer-shell analog) and `popup`
  (xdg-popup analog) alongside `window`.
* **Popups / subsurfaces** — multi-surface clients with grab-dismissed,
  parent-anchored child surfaces (see *Popups* above).
* **`launch`** — id-gated client spawning through the `LAUNCHABLE` trust table.
* **OCI client delivery** — clients pulled at runtime as OCI artifacts
  (`wasmboxSpawnFromOCI` / `OCIAppsLoader`).
* **Tear-free seqlock** — the optional `ctl` control word (see above).

## Roadmap / not yet implemented

* Nested popups (a popup opening its own sub-popup) + keyboard-driven menu
  navigation.
* `request_resize` (would need a fresh SAB) + surface-size clamping negotiation.
* GPU offload (everything blits through 2D `putImageData`; no dmabuf/WebGPU).
* Client-side decorations (decorations are always compositor-drawn).
* IME / clipboard / drag-and-drop.
