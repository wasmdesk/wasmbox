# wasmbox external-client protocol (step B)

Step A keeps every window inside one WebAssembly instance: `compositor.rb`
owns the canvas *and* every "client". Step B introduces **external clients**
that live in their own Web Worker (one wasm instance per worker) and speak to
the compositor over `postMessage` plus a `SharedArrayBuffer` (SAB) for the
pixel surface.

The compositor still owns the canvas, the stacking order, focus and input
routing. A client only owns its surface (an off-screen byte buffer) and posts
**commit** messages that tell the compositor which sub-rectangle changed; the
compositor reads the SAB and blits the damage onto the canvas at the window's
current position.

This document is the wire contract. The Ruby end is
`WindowManager#handle_client_message` plus `ExternalWindow`; the JS end is
`clients/sdk/sdk.js` plus the worker bootloader.

## Threading + memory

* The **compositor** runs on the page's main thread (it needs the DOM and
  the canvas). It is a single Go wasm instance hosting `compositor.rb`.
* Each **external client** runs in a dedicated `Worker(workerUrl)`. The
  worker loads its own `wasm_exec.js` + client wasm, then instantiates the
  Go runtime.
* The pixel surface for each window is a `SharedArrayBuffer` of
  `4 * width * height` bytes, allocated on the **client side** and posted
  back to the compositor with the `hello` reply window setup.
* SharedArrayBuffer requires the page to be served with
  `Cross-Origin-Opener-Policy: same-origin` and
  `Cross-Origin-Embedder-Policy: require-corp`. `cmd/serve` does that;
  `python3 -m http.server` does not.

## Lifecycle (compositor ↔ client)

```
client (worker)                          compositor (main)
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

* **C → S** — client (worker) to compositor (main thread).
* **S → C** — compositor (main thread) to client (worker).

### `hello`  (C → S)

The first message a client sends after `Worker` instantiation completes.

```js
{
  type: "hello",
  title: "hello world",
  w: 200, h: 150,             // requested surface size, pixels
  sab: SharedArrayBuffer,     // 4*w*h bytes, RGBA32, row-major top-left
  stride: 4 * 200,            // bytes per row; canonical = 4*w
}
```

The SAB is transferred by reference (it is shared, not copied). The compositor
keeps it for the lifetime of the window and reads `damage` rectangles out of
it on every `commit`.

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
  *and* the compositor would need to suppress its decoration — out of scope
  for step B.
* `commit.damage` and `input.event.{x,y}` are **surface-local** (i.e.
  `(0,0)` is the top-left of the client surface, not the canvas).

## Reading the SAB from Ruby

Ruby code receives `sab` as a `JS::Ref` to the underlying
`SharedArrayBuffer`. The SDK wraps it as a `Uint8ClampedArray` so the
compositor can copy slices straight into an `ImageData`. The compositor uses
`canvas.getContext('2d').putImageData(image, x, y, dx, dy, dw, dh)` for the
blit, where `image` is created once per window and shares the SAB.

## Out of scope for step B

* Subsurfaces / popups / nested windows.
* GPU offload (everything blits through 2D `putImageData`).
* Server-side decorations toggling (always on, compositor-drawn).
* IME / clipboard / drag-and-drop.
* Multi-buffering with explicit fences; clients are expected to render fully
  before posting `commit`. A future iteration will add an `Atomics.wait`-based
  fence on a SAB-resident counter.
