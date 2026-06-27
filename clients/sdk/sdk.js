// wasmbox client SDK (worker side). A wasmbox external client lives in a Web
// Worker; this SDK is what the worker imports to talk to the compositor.
//
// It allocates the surface SharedArrayBuffer, posts the initial `hello`,
// waits for `welcome`, exposes a `commit(damage)` flusher, and dispatches
// incoming `input` events to a user-supplied callback.
//
// Channel (step C.1, MessageChannel-direct):
//   The compositor sends each freshly-spawned client a dedicated MessagePort
//   as its very first message: `{type:"__wasmbox_port", port: <MessagePort>}`.
//   The SDK swaps from the implicit `self.parent` channel to that port before
//   any application traffic, so every client gets a private wire to the
//   compositor (Wayland-style). Until the port arrives, the SDK falls back to
//   posting on `self` (which routes to `self.parent`), so a test harness that
//   never sends a port still works -- backward-compatible with the step-C
//   nested-worker direct path.
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
  // ---- channel ----------------------------------------------------------
  // The channel is the EventTarget the SDK posts to + listens on. Default is
  // `self` (postMessage to it goes to the spawner -- the compositor worker --
  // and onmessage receives from the spawner). When the compositor hands us a
  // MessagePort via the one-shot `__wasmbox_port` message, we swap to it.
  //
  // Why an explicit channel instead of just `self`: the compositor's own
  // `self.onmessage` only handles main-thread bridge traffic (M2C_BOOT/...).
  // A client `hello` posted on `self` before the port handoff would land
  // there and be silently ignored. So the SDK BUFFERS application sends
  // until activeChannel is a real MessagePort.
  let activeChannel = null;       // set once the port handoff arrives
  let activeClient  = null;       // set by WasmboxClient.start()
  // Things start() (or commit/etc.) wanted to send before the port arrived.
  // Flushed in FIFO order the moment the port is swapped in.
  const pendingSends = [];

  function flushPending() {
    while (pendingSends.length) {
      const [msg, transfer] = pendingSends.shift();
      if (transfer && transfer.length) activeChannel.postMessage(msg, transfer);
      else                              activeChannel.postMessage(msg);
    }
  }

  // Post on the channel if it's ready, otherwise queue until it is.
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
      // MessagePort needs an explicit start() when consumed via
      // addEventListener; the compositor side does the same.
      try { port.start(); } catch (_) {}
    }
    activeChannel = port;
    flushPending();
  }

  // Listen on `self` for the one-shot port handoff. Registered at module load
  // so it catches the very first message the compositor sends after spawn,
  // even before the worker constructs its WasmboxClient.
  g.addEventListener("message", function bootPortHandler(ev) {
    const m = ev.data;
    if (!m || m.type !== "__wasmbox_port" || !m.port) return;
    g.removeEventListener("message", bootPortHandler);
    swapChannel(m.port);
  });

  // OCI assets handoff: when the compositor spawned us via
  // `wasmboxSpawnFromOCI`, it sends a one-shot `__wasmbox_assets` envelope
  // with blob URLs for wasm_exec.js + the app's <name>.wasm + the rest of
  // the app's files. The worker.js can read these by awaiting
  // `WasmboxClient.bootFromOCIAssets()` and use them in place of the
  // hard-coded `./wasm_exec.js` / `./<app>.wasm` paths that work for the
  // static spawn path. Static-path workers never receive this message; the
  // returned promise resolves to `null` only on a fallback timeout (set via
  // `WasmboxClient.bootFromOCIAssets({fallbackMs})`), so a worker that opts
  // in to OCI loading can still race-detect the static path.
  let ociAssets = null;
  let ociAssetsResolve = null;
  const ociAssetsPromise = new Promise((resolve) => { ociAssetsResolve = resolve; });
  g.addEventListener("message", function bootAssetsHandler(ev) {
    const m = ev.data;
    if (!m || m.type !== "__wasmbox_assets") return;
    g.removeEventListener("message", bootAssetsHandler);
    ociAssets = {
      wasm_exec_url: m.wasm_exec_url || null,
      wasm_url:      m.wasm_url      || null,
      wasm_name:     m.wasm_name     || null,
      files:         m.files         || {},
      ref:           m.ref           || null,
    };
    if (ociAssetsResolve) { ociAssetsResolve(ociAssets); ociAssetsResolve = null; }
  });

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
      // Seqlock control word (one Int32 in a tiny shared buffer). The client
      // bumps it ODD on the first pixel write of a frame and EVEN at commit;
      // the compositor refuses to blit a surface whose seq is odd or that
      // changes mid-read, so a half-painted (torn) frame is never shown. There
      // is a single writer (this worker), so load + conditional add is
      // race-free here.
      this.ctl = new SharedArrayBuffer(4);
      this.seq = new Int32Array(this.ctl);
      this.windowId = null;
      this._welcomeCbs = [];
      this._inputCbs = [];
      this._closedCbs = [];
      this._onMessage = (e) => this._handle(e.data);
    }

    // The channel currently in use (MessagePort once the handoff lands, null
    // before). Tests reach in here to assert the swap; real clients never
    // need it.
    get channel() { return activeChannel; }

    // Begin listening + post hello. Returns a Promise that resolves with the
    // welcome payload (so the client can `await client.start()` and then
    // paint). If the compositor has not yet handed us our MessagePort, the
    // hello is queued and flushed the moment the port arrives -- so callers
    // can construct + start() synchronously at module load without racing the
    // port handoff.
    start() {
      activeClient = this;
      // If the port already arrived (rare: only if the SDK is loaded long
      // after spawn), wire the listener now. Otherwise swapChannel will do
      // it the moment the port lands.
      if (activeChannel) {
        activeChannel.addEventListener("message", this._onMessage);
        try { activeChannel.start && activeChannel.start(); } catch (_) {}
      }
      send({
        type: "hello",
        title: this.title,
        w: this.w,
        h: this.h,
        sab: this.sab,
        stride: this.stride,
        ctl: this.ctl,
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
      // Close the seqlock write window (odd -> even) so the compositor may read
      // a complete frame. Only flips if a paint op opened it this frame.
      if ((Atomics.load(this.seq, 0) & 1) === 1) Atomics.add(this.seq, 0, 1);
      const d = damage || { x: 0, y: 0, w: this.w, h: this.h };
      send({
        type: "commit",
        window_id: this.windowId,
        damage: d,
      });
    }

    // Open the seqlock write window (even -> odd) on the first pixel write of a
    // frame. Idempotent within a frame (no-op once already odd).
    _beginPaint() {
      if ((Atomics.load(this.seq, 0) & 1) === 0) Atomics.add(this.seq, 0, 1);
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

    // Write a single RGBA pixel into the SAB at (x, y). Bounds-checked; OOB is
    // a no-op so naive clients can scribble freely.
    putPixel(x, y, r, gr, b, a) {
      if (x < 0 || y < 0 || x >= this.w || y >= this.h) return;
      this._beginPaint();
      const off = (y * this.w + x) * 4;
      this.pixels[off] = r;
      this.pixels[off + 1] = gr;
      this.pixels[off + 2] = b;
      this.pixels[off + 3] = a;
    }

    // Fill a rectangle with a solid RGBA. Default = whole surface.
    fillRect(r, gr, b, a, rect) {
      this._beginPaint();
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
          if (activeChannel) {
            try { activeChannel.removeEventListener("message", this._onMessage); } catch (_) {}
          }
          break;
      }
    }
  }

  // Test seam: force the SDK onto a specific MessagePort. Real clients never
  // call this -- the port arrives via the `__wasmbox_port` handoff -- but the
  // wire_test.js harness uses it to inject a synthetic port.
  WasmboxClient.useMessagePort = function (port) { swapChannel(port); };

  // OCI assets accessor. Returns a Promise that resolves to the envelope
  // the compositor sent via wasmboxSpawnFromOCI ({wasm_exec_url, wasm_url,
  // wasm_name, files, ref}), or to `null` when the spawn was static. The
  // promise model is necessary because postMessage delivery is async even
  // for queued messages -- the worker's top-level script runs before its
  // message handlers fire on the assets envelope.
  //
  // Worker.js implementations typically:
  //
  //   const assets = await WasmboxClient.bootFromOCIAssets({fallbackMs: 50});
  //   const wasmExecURL = assets ? assets.wasm_exec_url : "../../wasm_exec.js";
  //   const wasmURL     = assets ? assets.wasm_url      : "./hello.wasm";
  //   importScripts(wasmExecURL);
  //   ...
  //
  // The fallbackMs option (default 50 ms) is how long to wait before
  // declaring "no assets envelope arrived, treat this as a static spawn".
  // Set to 0 to disable the fallback (the promise then only resolves on a
  // real envelope) -- useful for hard-OCI workers that have no static path.
  WasmboxClient.bootFromOCIAssets = function (opts) {
    if (ociAssets) return Promise.resolve(ociAssets);
    const fallbackMs = (opts && typeof opts.fallbackMs === "number") ? opts.fallbackMs : 50;
    if (fallbackMs <= 0) return ociAssetsPromise;
    return Promise.race([
      ociAssetsPromise,
      new Promise((resolve) => setTimeout(() => resolve(ociAssets), fallbackMs)),
    ]);
  };

  // Test seam: inject a canned OCI assets envelope, for harnesses that load
  // the SDK directly (without going through the compositor's spawn path).
  WasmboxClient._setOCIAssets = function (a) {
    ociAssets = a;
    if (ociAssetsResolve) { ociAssetsResolve(ociAssets); ociAssetsResolve = null; }
  };

  g.WasmboxClient = WasmboxClient;
})(self);
