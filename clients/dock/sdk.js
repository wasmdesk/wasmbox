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
      // Buffer for input events that arrived BEFORE any onInput handler was
      // attached. The Go side of an external client only calls onInput after
      // its wasm boots, which can race a `windows_changed` snapshot the
      // compositor posts in the welcome handler — without this buffer the
      // initial snapshot would be silently dropped. Flushed (in FIFO order)
      // by the first onInput() call.
      this._pendingInputs = [];
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
    onInput(fn)   {
      this._inputCbs.push(fn);
      // Drain any input events that arrived before the first onInput handler
      // was attached — see _pendingInputs in the constructor.
      if (this._pendingInputs.length) {
        const queued = this._pendingInputs;
        this._pendingInputs = [];
        for (const ev of queued) fn(ev);
      }
    }
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

    // restore asks the compositor to un-minimize a window the user clicked on
    // a task button. The compositor's WindowManager.handle_client_message
    // routes the message to its `:restore` arm; an unknown id (or a window
    // that is not currently minimized) is dropped — restore is idempotent.
    // Kept as an alias of focus for backward compatibility with the existing
    // protocol; the compositor's `:focus` arm now restores minimized windows
    // on its own. Fire-and-forget like `launch`.
    restore(id) {
      send({ type: "restore", window_id: id | 0 });
    }

    // focus asks the compositor to raise + focus a window the user clicked
    // on its iconbar button. If the window is currently minimized it is
    // restored first, matching Fluxbox semantics. Fire-and-forget.
    focus(id) {
      send({ type: "focus", window_id: id | 0 });
    }

    // closeWindow asks the compositor to close a window the user
    // right-clicked on its iconbar button. Same effect as clicking the
    // window's title-bar close box. Fire-and-forget. (Named closeWindow,
    // not close, because `close()` is taken on the global Worker scope.)
    closeWindow(id) {
      send({ type: "close", window_id: id | 0 });
    }

    // setWorkspace asks the compositor to switch the active workspace to
    // `index` (1..workspaceCount, currently 4). The compositor drops
    // out-of-range or already-active indices and broadcasts a
    // `workspace_changed` input event back to every panel on success.
    // Fire-and-forget.
    setWorkspace(index) {
      send({ type: "set_workspace", index: index | 0 });
    }

    // setTheme asks the compositor to switch the active Openbox theme to
    // `name` (one of the bundled theme names — currently "Fluxbox Light",
    // "Fluxbox Dark", "GNOME Adwaita"). The compositor drops unknown names
    // and broadcasts a `theme_changed` input event back to every panel on
    // success (carrying both the name and the full .themerc source). Fire-
    // and-forget. The dock root menu in compositor.rb is the primary caller;
    // a client could in principle drive this too.
    setTheme(name) {
      send({ type: "set_theme", name: String(name) });
    }

    // putPixel + fillRect: minimal SAB scribblers used by bootWasm's loading
    // progress bar (kept in lockstep with clients/sdk/sdk.js).
    putPixel(x, y, r, gr, b, a) {
      if (x < 0 || y < 0 || x >= this.w || y >= this.h) return;
      const off = (y * this.w + x) * 4;
      this.pixels[off] = r;
      this.pixels[off + 1] = gr;
      this.pixels[off + 2] = b;
      this.pixels[off + 3] = a;
    }

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
          if (this._inputCbs.length) {
            for (const fn of this._inputCbs) fn(msg.event || {});
          } else {
            // No onInput handler yet (wasm not booted) — buffer the event
            // so the first onInput() call drains it. See _pendingInputs.
            this._pendingInputs.push(msg.event || {});
          }
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

  // ----- loading progress bar -------------------------------------------
  //
  // bootWasm(url, importObject, opts?) -> Promise<WebAssembly.Instance>.
  // See clients/sdk/sdk.js for the full doc-comment; this is a verbatim copy
  // because clients/dock/sdk.js is the dock's STANDALONE SDK so the dock
  // builds outside the wasmbox checkout. Any change here must be mirrored in
  // clients/sdk/sdk.js (the canonical copy).
  WasmboxClient.bootWasm = async function bootWasm(url, importObject, opts) {
    opts = opts || {};
    const bg    = opts.bg    || [250, 250, 250];
    const track = opts.track || [218, 220, 224];
    const fill  = opts.fill  || [ 53, 132, 228];
    const fetchFn = ("fetch" in opts)
      ? opts.fetch
      : (g.fetch || (typeof fetch !== "undefined" ? fetch : null));
    const instantiateFn = ("instantiate" in opts)
      ? opts.instantiate
      : (typeof WebAssembly !== "undefined" ? WebAssembly.instantiate.bind(WebAssembly) : null);
    const client = (opts.client === undefined) ? activeClient : opts.client;

    function paintBar(progress) {
      if (!client) return;
      const w = client.w, h = client.h;
      const trackW = Math.min(200, Math.max(40, w - 32));
      const trackH = 6;
      const trackX = ((w - trackW) >> 1);
      const trackY = ((h - trackH) >> 1);
      client.fillRect(bg[0], bg[1], bg[2], 255);
      client.fillRect(track[0], track[1], track[2], 255,
        { x: trackX, y: trackY, w: trackW, h: trackH });
      client.fillRect(bg[0], bg[1], bg[2], 255, { x: trackX, y: trackY, w: 1, h: 1 });
      client.fillRect(bg[0], bg[1], bg[2], 255, { x: trackX + trackW - 1, y: trackY, w: 1, h: 1 });
      client.fillRect(bg[0], bg[1], bg[2], 255, { x: trackX, y: trackY + trackH - 1, w: 1, h: 1 });
      client.fillRect(bg[0], bg[1], bg[2], 255, { x: trackX + trackW - 1, y: trackY + trackH - 1, w: 1, h: 1 });
      const p = Math.max(0, Math.min(1, progress));
      const fillW = Math.round(trackW * p);
      if (fillW > 0) {
        client.fillRect(fill[0], fill[1], fill[2], 255,
          { x: trackX, y: trackY, w: fillW, h: trackH });
      }
      client.commit();
    }

    if (!fetchFn) throw new Error("bootWasm: no fetch available");
    if (!instantiateFn) throw new Error("bootWasm: no WebAssembly.instantiate available");

    paintBar(0);

    const resp = await fetchFn(url);
    if (!resp || !resp.ok && resp.ok !== undefined) {
      if (resp && resp.status && (resp.status < 200 || resp.status >= 300)) {
        throw new Error("bootWasm: HTTP " + resp.status + " for " + url);
      }
    }
    const cl = resp.headers && resp.headers.get ? +resp.headers.get("content-length") : 0;
    const total = (Number.isFinite(cl) && cl > 0) ? cl : 0;

    const chunks = [];
    let received = 0;

    if (resp.body && resp.body.getReader) {
      const reader = resp.body.getReader();
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        if (value && value.length) {
          chunks.push(value);
          received += value.length;
          if (total > 0) {
            paintBar(received / total);
          } else {
            paintBar(0.5);
          }
        }
      }
    } else {
      const buf = await resp.arrayBuffer();
      chunks.push(new Uint8Array(buf));
      received = chunks[0].length;
    }

    const bytes = new Uint8Array(received || chunks.reduce((n, c) => n + c.length, 0));
    let off = 0;
    for (const c of chunks) { bytes.set(c, off); off += c.length; }

    paintBar(1);

    const result = await instantiateFn(bytes, importObject);
    return (result && result.instance) ? result.instance : result;
  };

  g.WasmboxClient = WasmboxClient;
})(self);
