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
  let activeChannel = null;       // the worker<->compositor MessagePort (one per worker)
  // A worker may own SEVERAL surfaces (a window + its popups), all multiplexed
  // over the single port. `clients` is every started surface; `pendingWelcome`
  // is those awaiting their welcome, in hello (FIFO) order.
  const clients = [];
  const pendingWelcome = [];
  // The worker's primary surface (first window-role client). bootWasm paints
  // its progress bar here by default; popups never become the active client.
  let activeClient = null;
  // Things start()/commit/etc. wanted to send before the port arrived. Flushed
  // in FIFO order the moment the port is swapped in.
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

  // One dispatcher for the whole worker. `welcome` is matched to the oldest
  // un-welcomed surface (the compositor replies in hello order over an ordered
  // port); every other message routes to the surface with that window_id.
  function dispatch(ev) {
    const m = ev && ev.data;
    if (!m || typeof m.type !== "string") return;
    if (m.type === "welcome") {
      const c = pendingWelcome.shift();
      if (c) c._applyWelcome(m);
      return;
    }
    const c = clients.find((x) => x.windowId === m.window_id);
    if (c) c._handle(m);
  }

  function swapChannel(port) {
    if (!port || activeChannel === port) return;
    port.addEventListener("message", dispatch);
    // MessagePort needs an explicit start() when consumed via addEventListener.
    try { port.start(); } catch (_) {}
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
      // Surface role + (popups only) the parent window_id and parent-relative
      // placement. role defaults to "window"; "popup" makes the compositor
      // anchor this surface to its parent and grab-dismiss it on outside click.
      this.role = opts.role || "window";
      this.parent = (opts.parent != null) ? (opts.parent | 0) : null;
      this.relX = opts.rel_x | 0;
      this.relY = opts.rel_y | 0;
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
      // Register this surface for dispatch. The single port listener is
      // installed by swapChannel when the port lands; several surfaces in one
      // worker all multiplex over it (welcome matched FIFO, the rest by id).
      clients.push(this);
      pendingWelcome.push(this);
      if (this.role !== "popup" && activeClient === null) activeClient = this;
      const hello = {
        type: "hello",
        title: this.title,
        role: this.role,
        w: this.w,
        h: this.h,
        sab: this.sab,
        stride: this.stride,
        ctl: this.ctl,
      };
      if (this.parent !== null) {
        hello.parent = this.parent;   // popup: anchor to this parent window_id
        hello.rel_x = this.relX;      // ...at this offset inside the parent body
        hello.rel_y = this.relY;
      }
      send(hello);
      return new Promise((resolve) => this.onWelcome(resolve));
    }

    onWelcome(fn) { this._welcomeCbs.push(fn); }
    onInput(fn)   { this._inputCbs.push(fn); }
    onClosed(fn)  { this._closedCbs.push(fn); }

    // Tell the compositor "I have new pixels". `damage` defaults to the full
    // surface, which is what naive clients want.
    //
    // seq is ALWAYS advanced by commit(), even when no per-pixel paint op
    // (putPixel/fillRect/...) ran this frame, because that is the signal the
    // compositor's blit cache uses to detect "new content available". Clients
    // that fill the SAB in bulk via js.CopyBytesToJS (the typical Go-wasm
    // pattern -- see clients/hello + clients/showcase) bypass _beginPaint so
    // without this, seq would stay at 0 forever and the cache would
    // incorrectly de-duplicate genuinely new frames.
    //   - If a paint op opened the window (seq odd): one bump closes it
    //     (odd -> even). seq advances by 1.
    //   - If no paint op ran (seq even): fake the pair (even -> odd -> even).
    //     The brief odd window may race with a compositor rAF tick — that
    //     tick skips the blit and the next tick (16 ms later) catches up;
    //     no torn frame is presented, only one frame of visual latency in
    //     the worst case.
    commit(damage) {
      if (this.windowId === null) return;
      const s = Atomics.load(this.seq, 0);
      if (s & 1) {
        Atomics.add(this.seq, 0, 1);
      } else {
        Atomics.add(this.seq, 0, 1);
        Atomics.add(this.seq, 0, 1);
      }
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
    // Apply the compositor's welcome. Called by the dispatcher, which FIFO-
    // matches it to the oldest surface still awaiting one.
    _applyWelcome(msg) {
      this.windowId = msg.window_id;
      this.w = msg.granted_w | 0;
      this.h = msg.granted_h | 0;
      this.stride = 4 * this.w;
      for (const fn of this._welcomeCbs) fn(msg);
    }

    _handle(msg) {
      if (!msg || typeof msg.type !== "string") return;
      switch (msg.type) {
        case "input":
          for (const fn of this._inputCbs) fn(msg.event || {});
          break;
        case "closed": {
          for (const fn of this._closedCbs) fn(msg.reason || "user");
          // Drop this surface from dispatch; the shared port stays open for the
          // worker's other surfaces (e.g. the parent window after a popup goes).
          const i = clients.indexOf(this);
          if (i >= 0) clients.splice(i, 1);
          break;
        }
      }
    }

    // Open a child popup surface anchored at (rel_x, rel_y) inside this window's
    // body. The popup is undecorated, stacks just above its parent, and the
    // compositor dismisses it (posts `closed`) on a click outside it (grab).
    // Returns the started child WasmboxClient — await its start() to paint.
    openPopup(opts) {
      if (this.windowId === null) {
        throw new Error("openPopup: call after the parent's welcome");
      }
      const popup = new WasmboxClient({
        title: opts.title || (this.title + " popup"),
        w: opts.w, h: opts.h,
        role: "popup",
        parent: this.windowId,
        rel_x: opts.rel_x | 0,
        rel_y: opts.rel_y | 0,
      });
      popup.start();
      return popup;
    }
  }

  // Test seam: force the SDK onto a specific MessagePort. Real clients never
  // call this -- the port arrives via the `__wasmbox_port` handoff -- but the
  // wire_test.js harness uses it to inject a synthetic port.
  WasmboxClient.useMessagePort = function (port) { swapChannel(port); };

  // ----- loading progress bar -------------------------------------------
  //
  // bootWasm(url, importObject, opts?) -> Promise<WebAssembly.Instance>.
  //
  // Drop-in replacement for `WebAssembly.instantiateStreaming(fetch(url), io)`
  // that ALSO paints a loading progress bar onto the active WasmboxClient's
  // SAB while the wasm is downloading. Without it the SAB sits at a flat
  // pre-paint colour for the 2-3 s a multi-MB wasm takes to fetch + boot, and
  // the user reads it as "dead window".
  //
  // Strategy:
  //   1. Paint a window-BG frame + an empty track + commit (frame 0).
  //   2. fetch(url), read body via reader.read() in a loop; per chunk:
  //      - paint window-BG over the previous fill region (cheap full-track
  //        repaint -- 200x6 px ~ 1200 px, trivial)
  //      - paint the new fill rect (trackW * progress wide)
  //      - commit
  //   3. Concat the accumulated chunks into a single Uint8Array.
  //   4. WebAssembly.instantiate(bytes, importObject).
  //   5. Resolve the instance. The wasm Go program, once started, will write
  //      over the bar with its own scene -- bootWasm never paints again past
  //      this point.
  //
  // opts:
  //   bg     [r,g,b]  window background (default 250,250,250)
  //   track  [r,g,b]  unfilled bar (default 218,220,224)
  //   fill   [r,g,b]  filled portion (default 53,132,228) -- Adwaita accent
  //   client          the WasmboxClient to paint into (defaults to the active
  //                   one set by start()). Pass null to disable painting (for
  //                   non-SDK clients like quake that drive the SAB from Go).
  //
  // Content-Length headers are honoured for the determinate case; when the
  // server omits it (or returns 0), we paint a steady 50% indicator instead
  // of guessing, then snap to 100% on completion. This degraded mode is
  // visually still "something is happening" rather than a static colour.
  //
  // The helper is intentionally tolerant of:
  //   - missing global fetch (Node tests): tests inject a stub via opts.fetch.
  //   - missing WebAssembly.instantiate (same reason): tests inject opts.instantiate.
  //   These seams keep bootWasm 100% unit-testable without a browser.
  WasmboxClient.bootWasm = async function bootWasm(url, importObject, opts) {
    opts = opts || {};
    const bg    = opts.bg    || [250, 250, 250];
    const track = opts.track || [218, 220, 224];
    const fill  = opts.fill  || [ 53, 132, 228];
    // For fetch / instantiate we honour an explicit null override (so tests
    // can force the "no fetch / no WebAssembly" error paths). `in opts` is the
    // distinguishing test -- `opts.x || fallback` would silently fall back on
    // null too.
    const fetchFn = ("fetch" in opts)
      ? opts.fetch
      : (g.fetch || (typeof fetch !== "undefined" ? fetch : null));
    const instantiateFn = ("instantiate" in opts)
      ? opts.instantiate
      : (typeof WebAssembly !== "undefined" ? WebAssembly.instantiate.bind(WebAssembly) : null);
    // client === undefined  -> use the active client (set by start()).
    // client === null       -> disable painting (quake-style).
    // client === <obj>      -> paint into that client.
    const client = (opts.client === undefined) ? activeClient : opts.client;

    // ---- paint helpers (no-op when client is null) ----
    function paintBar(progress) {
      if (!client) return;
      const w = client.w, h = client.h;
      // Clamp track to surface; centre horizontally + vertically.
      const trackW = Math.min(200, Math.max(40, w - 32));
      const trackH = 6;
      const trackX = ((w - trackW) >> 1);
      const trackY = ((h - trackH) >> 1);
      // Full window BG (cheap; small surfaces). For the dock-style "panel"
      // role this overdraws the existing pre-paint, which is intentional --
      // bootWasm OWNS the surface until the wasm starts committing.
      client.fillRect(bg[0], bg[1], bg[2], 255);
      // Track (unfilled).
      client.fillRect(track[0], track[1], track[2], 255,
        { x: trackX, y: trackY, w: trackW, h: trackH });
      // Soften corners: erase the 1px outer pixels at each end so the bar
      // doesn't look like a perfect rectangle (very subtle, but matches the
      // rounded-rect look of GTK / Aqua).
      client.fillRect(bg[0], bg[1], bg[2], 255,
        { x: trackX, y: trackY, w: 1, h: 1 });
      client.fillRect(bg[0], bg[1], bg[2], 255,
        { x: trackX + trackW - 1, y: trackY, w: 1, h: 1 });
      client.fillRect(bg[0], bg[1], bg[2], 255,
        { x: trackX, y: trackY + trackH - 1, w: 1, h: 1 });
      client.fillRect(bg[0], bg[1], bg[2], 255,
        { x: trackX + trackW - 1, y: trackY + trackH - 1, w: 1, h: 1 });
      // Fill (left-to-right). progress in [0,1].
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

    // Frame 0: empty track (0% progress) so the user sees the loader within
    // microseconds of the worker spawning.
    paintBar(0);

    const resp = await fetchFn(url);
    if (!resp || !resp.ok && resp.ok !== undefined) {
      // resp.ok is undefined for stub responses without a status field; we
      // accept those for tests + only reject real-fetch failures.
      if (resp && resp.status && (resp.status < 200 || resp.status >= 300)) {
        throw new Error("bootWasm: HTTP " + resp.status + " for " + url);
      }
    }
    const cl = resp.headers && resp.headers.get ? +resp.headers.get("content-length") : 0;
    const total = (Number.isFinite(cl) && cl > 0) ? cl : 0;

    const chunks = [];
    let received = 0;

    // resp.body may be absent in node-fetch / mock environments; fall back to
    // arrayBuffer() in that case (no progress, just snap to 100% at the end).
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
            // Indeterminate mode: paint a steady 50% indicator so the user
            // still sees motion (the bar redraws every chunk).
            paintBar(0.5);
          }
        }
      }
    } else {
      const buf = await resp.arrayBuffer();
      chunks.push(new Uint8Array(buf));
      received = chunks[0].length;
    }

    // Concat into a single Uint8Array.
    const bytes = new Uint8Array(received || chunks.reduce((n, c) => n + c.length, 0));
    let off = 0;
    for (const c of chunks) { bytes.set(c, off); off += c.length; }

    // Final paint: 100% (covers the case where Content-Length under-reported
    // and we never hit 1.0 during streaming).
    paintBar(1);

    const result = await instantiateFn(bytes, importObject);
    // WebAssembly.instantiate(BufferSource, ...) returns {module, instance};
    // some stubs may return just the instance. Normalise.
    return (result && result.instance) ? result.instance : result;
  };

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
