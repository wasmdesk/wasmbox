// wasmbox client SDK (worker side), adapted for wasmdock.
//
// This is a self-contained copy of the wasmbox SDK pattern so the dock does
// not depend on a path inside the wasmbox checkout. A wasmbox external client
// lives in a Web Worker; this SDK is what the worker imports to talk to the
// compositor.
//
// It allocates the surface SharedArrayBuffer, posts the initial `hello`, waits
// for `welcome`, exposes a `commit(damage)` flusher, dispatches incoming
// `input` events to a user-supplied callback, and adds a small `launch(app)`
// helper for the dock's launch protocol extension (see INTEGRATION.md). If the
// compositor does not implement `launch` yet, the message is simply ignored on
// the host side — the dock keeps rendering.
//
// Channel (step C.1, MessageChannel-direct):
//   The compositor sends each freshly-spawned client a dedicated MessagePort
//   as its very first message: `{type:"__wasmbox_port", port: <MessagePort>}`.
//   The SDK swaps from the implicit `self.parent` channel to that port before
//   any application traffic, so every client gets a private wire to the
//   compositor (Wayland-style). All application sends BUFFER until the port
//   is in place, then flush in FIFO order -- so callers can `client.start()`
//   synchronously at module load without racing the port handoff.
//
// See the wasmbox docs/protocol.md for the wire format.
//
// Usage (inside the worker):
//   importScripts("./sdk.js");
//   const client = new WasmboxClient({ title: "dock", w: 480, h: 120 });
//   client.onWelcome((info) => { ... paint, then client.commit(); });
//   client.onInput((event) => { ... });
//   client.start();
//
// The wasm Go program (loaded after `client.start()` resolves) reaches the SDK
// through `globalThis.wasmboxClient` (set by the worker bootloader).

"use strict";

(function (g) {
  // Channel: set once the compositor hands us a MessagePort. Until then,
  // application sends (hello/commit/...) buffer in pendingSends.
  let activeChannel = null;
  let activeClient  = null;
  const pendingSends = [];

  function flushPending() {
    while (pendingSends.length) {
      const [msg, transfer] = pendingSends.shift();
      if (transfer && transfer.length) activeChannel.postMessage(msg, transfer);
      else                              activeChannel.postMessage(msg);
    }
  }

  function send(msg, transfer) {
    if (activeChannel) {
      if (transfer && transfer.length) activeChannel.postMessage(msg, transfer);
      else                              activeChannel.postMessage(msg);
    } else {
      pendingSends.push([msg, transfer]);
    }
  }

  function swapChannel(port) {
    if (!port || activeChannel === port) return;
    if (activeClient && activeClient._onMessage) {
      port.addEventListener("message", activeClient._onMessage);
      try { port.start(); } catch (_) {}
    }
    activeChannel = port;
    flushPending();
  }

  g.addEventListener("message", function bootPortHandler(ev) {
    const m = ev.data;
    if (!m || m.type !== "__wasmbox_port" || !m.port) return;
    g.removeEventListener("message", bootPortHandler);
    swapChannel(m.port);
  });

  class WasmboxClient {
    constructor(opts) {
      const w = opts.w | 0;
      const h = opts.h | 0;
      if (!w || !h) throw new Error("WasmboxClient requires positive w + h");
      this.title = opts.title || "client";
      this.role = opts.role || "window"; // dock requests the "panel" role
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

    get channel() { return activeChannel; }

    // Begin listening + post hello. Returns a Promise that resolves with the
    // welcome payload (so the client can `await client.start()` and then paint).
    start() {
      activeClient = this;
      if (activeChannel) {
        activeChannel.addEventListener("message", this._onMessage);
        try { activeChannel.start && activeChannel.start(); } catch (_) {}
      }
      send({
        type: "hello",
        title: this.title,
        role: this.role, // panel role (compositor may ignore → defaults to window)
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
    // surface.
    commit(damage) {
      if (this.windowId === null) return;
      const d = damage || { x: 0, y: 0, w: this.w, h: this.h };
      send({
        type: "commit",
        window_id: this.windowId,
        damage: d,
      });
    }

    setTitle(title) {
      this.title = title;
      if (this.windowId === null) return;
      send({ type: "set_title", window_id: this.windowId, title: title });
    }

    requestClose() {
      if (this.windowId === null) return;
      send({ type: "request_close", window_id: this.windowId });
    }

    // launch asks the compositor to start another client. Protocol extension
    // (see INTEGRATION.md). Fire-and-forget: if the host has no handler the
    // message is dropped and the dock keeps working.
    launch(app) {
      send({ type: "launch", app: String(app) });
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
          if (activeChannel) {
            try { activeChannel.removeEventListener("message", this._onMessage); } catch (_) {}
          }
          break;
      }
    }
  }

  WasmboxClient.useMessagePort = function (port) { swapChannel(port); };

  g.WasmboxClient = WasmboxClient;
})(self);
