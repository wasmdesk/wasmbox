// wasmbox client SDK (worker side). A wasmbox external client lives in a Web
// Worker; this SDK is what the worker imports to talk to the compositor.
//
// It allocates the surface SharedArrayBuffer, posts the initial `hello`,
// waits for `welcome`, exposes a `commit(damage)` flusher, and dispatches
// incoming `input` events to a user-supplied callback.
//
// See ../../docs/protocol.md for the wire format.
//
// Usage (inside the worker):
//   importScripts("../sdk/sdk.js");
//   const client = new WasmboxClient({ title: "hello", w: 200, h: 150 });
//   client.onWelcome((info) => { ... paint, then client.commit(); });
//   client.onInput((event) => { ... });
//   client.start();
//
// The wasm Go program (loaded after `client.start()` resolves) reaches the
// SDK through `globalThis.WASMBOX` (set below).

"use strict";

(function (g) {
  class WasmboxClient {
    constructor(opts) {
      const w = opts.w | 0;
      const h = opts.h | 0;
      if (!w || !h) throw new Error("WasmboxClient requires positive w + h");
      this.title = opts.title || "client";
      this.w = w;
      this.h = h;
      this.stride = 4 * w;
      // 4 bytes per pixel (RGBA32), row-major, origin top-left.
      this.sab = new SharedArrayBuffer(this.stride * h);
      this.pixels = new Uint8ClampedArray(this.sab); // worker-side view
      this.windowId = null;
      this._welcomeCbs = [];
      this._inputCbs = [];
      this._closedCbs = [];
      this._onMessage = (e) => this._handle(e.data);
    }

    // Begin listening + post hello. Returns a Promise that resolves with the
    // welcome payload (so the client can `await client.start()` and then paint).
    start() {
      g.addEventListener("message", this._onMessage);
      g.postMessage({
        type: "hello",
        title: this.title,
        w: this.w,
        h: this.h,
        sab: this.sab,
        stride: this.stride,
      });
      return new Promise((resolve) => this.onWelcome(resolve));
    }

    onWelcome(fn) { this._welcomeCbs.push(fn); }
    onInput(fn)   { this._inputCbs.push(fn); }
    onClosed(fn)  { this._closedCbs.push(fn); }

    // Tell the compositor "I have new pixels". `damage` defaults to the full
    // surface, which is what naive clients want.
    commit(damage) {
      if (this.windowId === null) return;
      const d = damage || { x: 0, y: 0, w: this.w, h: this.h };
      g.postMessage({
        type: "commit",
        window_id: this.windowId,
        damage: d,
      });
    }

    setTitle(title) {
      this.title = title;
      if (this.windowId === null) return;
      g.postMessage({ type: "set_title", window_id: this.windowId, title: title });
    }

    requestClose() {
      if (this.windowId === null) return;
      g.postMessage({ type: "request_close", window_id: this.windowId });
    }

    // Write a single RGBA pixel into the SAB at (x, y). Bounds-checked; OOB is
    // a no-op so naive clients can scribble freely.
    putPixel(x, y, r, gr, b, a) {
      if (x < 0 || y < 0 || x >= this.w || y >= this.h) return;
      const off = (y * this.w + x) * 4;
      this.pixels[off] = r;
      this.pixels[off + 1] = gr;
      this.pixels[off + 2] = b;
      this.pixels[off + 3] = a;
    }

    // Fill a rectangle with a solid RGBA. Default = whole surface.
    fillRect(r, gr, b, a, rect) {
      const x0 = rect ? Math.max(0, rect.x | 0) : 0;
      const y0 = rect ? Math.max(0, rect.y | 0) : 0;
      const x1 = rect ? Math.min(this.w, (rect.x + rect.w) | 0) : this.w;
      const y1 = rect ? Math.min(this.h, (rect.y + rect.h) | 0) : this.h;
      for (let y = y0; y < y1; y++) {
        let off = (y * this.w + x0) * 4;
        for (let x = x0; x < x1; x++) {
          this.pixels[off++] = r;
          this.pixels[off++] = gr;
          this.pixels[off++] = b;
          this.pixels[off++] = a;
        }
      }
    }

    // --- internals -------------------------------------------------------
    _handle(msg) {
      if (!msg || typeof msg.type !== "string") return;
      switch (msg.type) {
        case "welcome":
          this.windowId = msg.window_id;
          this.w = msg.granted_w | 0;
          this.h = msg.granted_h | 0;
          this.stride = 4 * this.w;
          for (const fn of this._welcomeCbs) fn(msg);
          break;
        case "input":
          for (const fn of this._inputCbs) fn(msg.event || {});
          break;
        case "closed":
          for (const fn of this._closedCbs) fn(msg.reason || "user");
          g.removeEventListener("message", this._onMessage);
          break;
      }
    }
  }

  g.WasmboxClient = WasmboxClient;
})(self);
