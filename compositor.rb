# A Wayland-inspired compositor with an Openbox-style window-manager policy,
# written in pure Ruby and rendered to a <canvas> through the interpreter's
# interactive JS bridge (see internal/vm/jsbridge_wasm.go).
#
# Step A keeps everything in ONE WASM instance: the compositor and every
# in-process Window live in the same Ruby program. Step B (this file) adds an
# EXTERNAL CLIENT path on top: ExternalWindow + WindowManager#spawn_external
# attach a Web Worker carrying its own wasm instance, which writes pixels into
# a SharedArrayBuffer and posts {type:"commit",...} to flush damage rectangles.
# The compositor blits the SAB onto its canvas and forwards input events back
# to the focused worker. See docs/protocol.md for the wire format.
#
# The file is laid out so the *pure* window-management logic (geometry,
# hit-testing, stacking, placement, focus, client-message dispatch) lives in
# plain Ruby classes with no reference to JS. Only Compositor#attach_to_canvas,
# #render and the worker-spawning helpers touch the bridge, so the policy can
# be unit-tested natively (the JS module is a no-op off-wasm).

# ---------------------------------------------------------------------------
# Theme — Openbox-minimal decorations.
# ---------------------------------------------------------------------------
module Theme
  DESKTOP        = "#11131a"
  DESKTOP_GRID   = "#171a24"
  TITLE_ACTIVE   = "#9b1c2e"
  TITLE_INACTIVE = "#2a2d3a"
  TITLE_TEXT     = "#f5f6fa"
  BORDER_ACTIVE  = "#ef4444"
  BORDER_INACTIVE= "#3a3d4a"
  CLOSE_BG       = "#e6e7ee"
  CLOSE_GLYPH    = "#1a1a2e"
  RESIZE_GRIP    = "#5b6072"
  HUD_TEXT       = "#9aa0b4"
  MENU_BG        = "#1d1f29"
  MENU_BORDER    = "#3a3d4a"
  MENU_TEXT      = "#e6e7ee"
  MENU_HILITE    = "#9b1c2e"

  TITLE_H   = 22 # titlebar height
  BORDER    = 1  # decoration border width
  CLOSE_SZ  = 14 # close-box side
  GRIP      = 14 # resize-corner side
  MIN_W     = 90
  MIN_H     = 60
end

# ---------------------------------------------------------------------------
# Window — a client surface plus its decoration geometry and hit-testing.
# Pure data + math; no JS here.
# ---------------------------------------------------------------------------
class Window
  attr_accessor :id, :title, :x, :y, :w, :h, :fill, :focused, :role

  def initialize(id, title, x, y, w, h, fill, role = "window")
    @id = id
    @title = title
    @x = x
    @y = y
    @w = w
    @h = h
    @fill = fill
    @focused = false
    # role is "window" (normal, decorated, cascade-placed) or "panel" (the dock:
    # undecorated, bottom-center anchored, always-on-top). An unknown role MUST
    # behave like a normal window, so anything that is not exactly "panel" is
    # treated as a window. See docs/protocol.md + wasmdock INTEGRATION.md.
    @role = role
  end

  def focused? = @focused

  # A panel (dock-style surface) has no decoration and is anchored, never
  # cascade-placed. Anything else is a normal window.
  def panel? = @role == "panel"

  # Outer frame (decoration included): the titlebar sits above the body. For a
  # panel there is no titlebar, so the frame top is the body top.
  def frame_top = panel? ? @y : @y - Theme::TITLE_H
  def right     = @x + @w
  def bottom    = @y + @h

  # Rectangles, each as [x, y, w, h]. Panels carry no decoration rectangles, so
  # the titlebar / close / resize hit-rects collapse to empty (zero-size) and
  # the frame equals the body.
  def titlebar_rect = panel? ? [@x, @y, 0, 0] : [@x, frame_top, @w, Theme::TITLE_H]
  def body_rect     = [@x, @y, @w, @h]

  def close_rect
    return [@x, @y, 0, 0] if panel?
    pad = (Theme::TITLE_H - Theme::CLOSE_SZ) / 2
    [right - Theme::CLOSE_SZ - pad, frame_top + pad, Theme::CLOSE_SZ, Theme::CLOSE_SZ]
  end

  def resize_rect
    return [@x, @y, 0, 0] if panel?
    [right - Theme::GRIP, bottom - Theme::GRIP, Theme::GRIP, Theme::GRIP]
  end

  # The whole decorated extent, used for "did the click land on me at all?".
  # For a panel the decorated extent is just the body.
  def frame_rect = panel? ? [@x, @y, @w, @h] : [@x, frame_top, @w, @h + Theme::TITLE_H]

  def hit?(rect, px, py)
    rx, ry, rw, rh = rect
    px >= rx && px < rx + rw && py >= ry && py < ry + rh
  end

  def contains?(px, py)   = hit?(frame_rect, px, py)
  # A panel is never draggable, closable-by-box or resizable, so all three
  # decoration hit-tests report "no hit" — those gestures can never start.
  def on_titlebar?(px, py)= panel? ? false : hit?(titlebar_rect, px, py)
  def on_close?(px, py)   = panel? ? false : hit?(close_rect, px, py)
  def on_resize?(px, py)  = panel? ? false : hit?(resize_rect, px, py)

  def move_to(nx, ny)
    @x = nx
    @y = ny
  end

  def resize_to(nw, nh)
    @w = [nw, Theme::MIN_W].max
    @h = [nh, Theme::MIN_H].max
  end

  # In-process windows ("step A"): painted by the compositor itself from #fill.
  # ExternalWindow overrides this to true.
  def external? = false
end

# ---------------------------------------------------------------------------
# ExternalWindow — a window whose body pixels live in a SharedArrayBuffer
# owned by an external client (Web Worker / separate wasm instance). The
# compositor still owns the decoration; the client only paints inside the
# body rectangle (surface-local coordinates).
#
# The class itself does NOT touch JS — it just stores the worker ref, the SAB
# ref and a (lazily built) ImageData ref. The Compositor pulls those refs out
# when it blits the body, and WindowManager#handle_client_message routes
# protocol messages to the matching ExternalWindow.
# ---------------------------------------------------------------------------
class ExternalWindow < Window
  attr_accessor :worker, :sab, :ctl, :image_data, :stride, :pending_damage

  # No fill colour: an ExternalWindow's body is the SAB's RGBA bytes. We pass
  # a sentinel through to Window#initialize because the existing decoration
  # path never reads `fill` for external windows (Compositor.draw_window
  # branches on #external? before fill_rect).
  def initialize(id, title, x, y, w, h, role = "window")
    super(id, title, x, y, w, h, "#000000", role)
    @worker = nil
    @sab = nil
    @image_data = nil
    @stride = 4 * w
    # Until the first commit lands, paint the full body so we don't flash an
    # uninitialised SAB. After that, the client tells us which rect changed.
    @pending_damage = { x: 0, y: 0, w: w, h: h }
  end

  def external? = true

  # Replace the pending damage with the union of (previous, new). A client
  # can post several commits between two render frames; the compositor only
  # blits once per frame and needs the union.
  def merge_damage(d)
    if @pending_damage.nil?
      @pending_damage = d
      return
    end
    a = @pending_damage
    x0 = [a[:x], d[:x]].min
    y0 = [a[:y], d[:y]].min
    x1 = [a[:x] + a[:w], d[:x] + d[:w]].max
    y1 = [a[:y] + a[:h], d[:y] + d[:h]].max
    @pending_damage = { x: x0, y: y0, w: x1 - x0, h: y1 - y0 }
  end

  # Clip the damage rect to the surface bounds and to a maximum-area-1 sanity
  # bound, returning nil if there is nothing left.
  def clipped_damage
    return nil unless @pending_damage
    d = @pending_damage
    x = [d[:x], 0].max
    y = [d[:y], 0].max
    w = [d[:w] + [d[:x], 0].min, @w - x].min
    h = [d[:h] + [d[:y], 0].min, @h - y].min
    return nil if w <= 0 || h <= 0
    { x: x, y: y, w: w, h: h }
  end

  def clear_damage
    @pending_damage = nil
  end
end

# ---------------------------------------------------------------------------
# WindowManager — stacking order, focus policy and placement. Pure logic,
# fully exercisable without a browser. Also routes external-client messages
# (handle_client_message) — that's all pure dispatch, no JS.
#
# The stack is bottom-to-top: stack.last is the topmost / most-recently-raised
# window. Focus follows the top of the stack (click-to-focus, raise-on-focus).
# ---------------------------------------------------------------------------
class WindowManager
  attr_reader :stack

  # Geometry restored from browser storage, keyed by window title:
  # { "xterm" => { x:, y:, w:, h: }, ... }. Compositor#restore_layout loads it
  # from localStorage at boot (before the initial spawns); spawn and
  # register_external apply it. Pure data — no JS lives in this class.
  attr_accessor :saved_layout

  PALETTE = ["#1f6feb", "#2ea043", "#d29922", "#8957e5", "#db61a2", "#1f9da6"].freeze

  # LAUNCHABLE is the trust boundary for the `launch` protocol message: it maps
  # an opaque app id (sent by a client, e.g. the dock) to a COMPOSITOR-OWNED
  # spawn descriptor. A `launch` message never carries a URL/path/argv — only
  # an id — and an id that is not a key here is dropped. This means a
  # malicious client can at most ask to open one of the already-installed
  # apps, never run arbitrary code. See wasmdock INTEGRATION.md §1.
  #
  # Two descriptor shapes are supported (both equally trusted, since both live
  # inside the compositor source):
  #
  #   String           — a static path to a worker.js the compositor serves
  #                       from its own asset tree. Dispatched via
  #                       `wasmboxSpawnWorker(url)`. Backward-compatible with
  #                       every pre-OCI launch entry.
  #
  #   {oci: "<ref>"}   — an OCI image reference ("repo:tag"). Resolved at
  #                       launch time via `wasmboxSpawnFromOCI(ref)`, which
  #                       pulls the worker.js + wasm_exec.js + <app>.wasm out
  #                       of the registry, wraps each in a Blob URL, and
  #                       spawns a worker from those URLs. The default
  #                       registry list is set by the compositor worker
  #                       (DEFAULT_OCI_REGISTRIES); the caller can override
  #                       it via `globalThis.WASMBOX_OCI_REGISTRIES`.
  #
  # The dock ships ids "terminal"/"editor"/"files". "terminal" and "files" map
  # to their dedicated placeholder clients (recognizable titles, distinct
  # surfaces); "editor" stays on the bundled hello client until a dedicated
  # editor client lands. A click on a dock icon thus opens a window whose
  # title matches the icon, completing the user-visible launch chain.
  LAUNCHABLE = {
    "terminal"  => "clients/terminal/worker.js",
    "editor"    => "clients/hello/worker.js",
    "files"     => "clients/files/worker.js",
    # OCI demo ids (resolved against WASMBOX_OCI_REGISTRIES or the default
    # http://127.0.0.1:5000 registry). The id "hello-oci" mirrors the bundled
    # hello client but pulls it from the registry instead of from disk; it
    # is what the Playwright OCI probe drives via wasmboxSpawnFromOCI.
    "hello-oci" => { oci: "hello:latest" },
  }.freeze

  LAYOUT_SEP = "\t"

  def initialize
    @stack = []
    @next_id = 0
    @cascade = 0
    @saved_layout = {}
    # The most recently registered external window. The Compositor wires the
    # incoming worker to this on `welcome`. We track it explicitly because a
    # panel is never focused, so wm.focused cannot identify a freshly-registered
    # panel.
    @last_registered = nil
    # Records the most recent commit/lifecycle messages we have processed, for
    # introspection from tests. (Bounded to the last 16 — pure data.)
    @last_messages = []
  end

  # Apply any persisted geometry for win.title (size + position), overriding the
  # default cascade slot. No-op when nothing was saved for this title.
  def apply_saved(win)
    s = @saved_layout[win.title]
    return nil unless s
    win.move_to(s[:x], s[:y])
    win.resize_to(s[:w], s[:h])
    win
  end

  # A cheap string that changes whenever geometry or stacking order changes, so
  # the Compositor only writes storage on a real change.
  def layout_signature
    @stack.map { |w| "#{w.id}:#{w.x}:#{w.y}:#{w.w}:#{w.h}" }.join("|")
  end

  # Serialize the current layout to a storage string: one tab-separated
  # "title<TAB>x<TAB>y<TAB>w<TAB>h" record per line (titles sanitized).
  def serialize_layout
    lines = []
    @stack.each do |w|
      key = w.title.to_s.gsub("\t", " ").gsub("\n", " ")
      lines << [key, w.x, w.y, w.w, w.h].join(LAYOUT_SEP)
    end
    lines.join("\n")
  end

  # Parse serialize_layout output into @saved_layout. Malformed or short records
  # are skipped; the last record for a given title wins.
  def parse_layout(text)
    out = {}
    text.to_s.split("\n").each do |line|
      parts = line.split(LAYOUT_SEP)
      if parts.length >= 5
        out[parts[0]] = { x: parts[1].to_i, y: parts[2].to_i, w: parts[3].to_i, h: parts[4].to_i }
      end
    end
    @saved_layout = out
  end

  def windows = @stack

  # The keyboard-focused window: the top-most NORMAL window. Panels (the dock)
  # are excluded from the focus ring, so a panel is never "focused" even though
  # it sits on top visually. Returns nil when no normal window exists.
  def focused = normal_windows.last

  # Look up a window (in or out of process) by its compositor id.
  # Implemented as a manual scan because rbgo's block-`return` does not return
  # from the enclosing method (it returns from the block).
  def find(id)
    found = nil
    @stack.each { |w| found = w if w.id == id }
    found
  end

  # Map a launchable app id to its compositor-owned worker URL, or nil when
  # the id is unknown OR when the descriptor is an OCI-shaped Hash (in which
  # case use launchable_oci instead). Kept as the original static-spawn lookup
  # so legacy callers (and the rbtest assertions on built-in ids) still pass.
  def launchable_url(app)
    desc = LAUNCHABLE[app.to_s]
    desc.is_a?(String) ? desc : nil
  end

  # Map a launchable app id to its OCI image reference ("repo:tag"), or nil
  # when the id is unknown OR when the descriptor is a static-path String.
  # The `:launch` dispatcher in the Compositor checks both shapes and routes
  # to wasmboxSpawnWorker / wasmboxSpawnFromOCI accordingly.
  def launchable_oci(app)
    desc = LAUNCHABLE[app.to_s]
    return nil unless desc.is_a?(Hash)
    ref = desc[:oci]
    ref.nil? ? nil : ref.to_s
  end

  # Generic "is this id launchable" probe — true if either shape is present.
  # handle_client_message uses this so a new descriptor shape added in the
  # future doesn't need a new gate; only the dispatcher needs to learn it.
  def launchable?(app)
    LAUNCHABLE.key?(app.to_s)
  end

  # Normal (non-panel) windows, bottom-to-top, in stacking order.
  def normal_windows
    @stack.reject { |w| w.panel? }
  end

  # Panel windows (the dock), bottom-to-top. Drawn after every normal window so
  # panels are always-on-top: a new normal window can never raise above a panel.
  def panels
    @stack.select { |w| w.panel? }
  end

  # The compositing order: every normal window first, then every panel on top.
  # render() walks this so panels stay above the normal-window pool each frame
  # regardless of focus/raise activity.
  def ordered_windows
    normal_windows + panels
  end

  # Anchor a panel to the bottom-center of a canvas_w x canvas_h desktop: the
  # surface is horizontally centered and its bottom edge is flush to the canvas
  # bottom (the dock paints its own bottom margin inside the surface). Because a
  # panel is undecorated the surface IS the whole window, so no titlebar offset.
  # Pure geometry — no JS. No-op for a non-panel window.
  def anchor_panel(win, canvas_w, canvas_h)
    return nil unless win&.panel?
    win.move_to((canvas_w - win.w) / 2, canvas_h - win.h)
    win
  end

  attr_reader :last_messages

  # Cascade placement, Openbox-style: each new window steps down-and-right and
  # wraps once it would run off a notional screen.
  def spawn(title, w = 240, h = 150)
    @next_id += 1
    step = 28
    base_x = 60
    base_y = 60
    x = base_x + (@cascade % 6) * step
    y = base_y + (@cascade % 6) * step
    @cascade += 1
    fill = PALETTE[(@next_id - 1) % PALETTE.length]
    win = Window.new(@next_id, title, x, y, w, h, fill)
    @stack.push(win)
    focus(win)
    apply_saved(win)
    win
  end

  # Remove a window from the stack by object identity, returning a new array.
  # (rbgo's Array lacks #delete, so we filter by #equal? and rebuild.)
  def unstack(win)
    @stack.reject! { |w| w.equal?(win) }
  end

  # Raise + focus. Moving to the end of the array puts the window on top.
  #
  # Panels are excluded from the keyboard-focus / raise policy: a panel is never
  # the focused window and clicking it never steals focus from an app window. It
  # still lives in the stack (so it renders and receives hover input), it just
  # does not participate in focus. So for a panel we leave focus untouched.
  def focus(win)
    return nil unless win
    return win if win.panel?
    unstack(win)
    @stack.push(win)
    @stack.each { |o| o.focused = false unless o.panel? }
    win.focused = true
    win
  end

  def close(win)
    unstack(win)
    top = @stack.last
    top.focused = true if top
    win
  end

  # Top-most window under the pointer (search the stack top-down).
  def window_at(px, py)
    hit = nil
    @stack.each { |w| hit = w if w.contains?(px, py) } # last (topmost) wins
    hit
  end

  # Alt+Tab-ish cycle over the NORMAL windows only — panels (the dock) are
  # excluded from the focus ring so Tab never lands on the dock. Sends the
  # current focused window to the bottom of the normal pool and focuses the next
  # normal window down, so repeated presses walk the whole app stack.
  def cycle
    normals = normal_windows
    return nil if normals.length < 2
    top = normals.last
    next_win = normals[-2]
    unstack(top)            # drop the old top...
    @stack = [top] + @stack # ...and reinsert it at the bottom
    focus(next_win)         # then raise+focus the next normal window down
  end

  # -------------------------------------------------------------------------
  # External-client side: register a freshly-created ExternalWindow and
  # dispatch protocol messages. None of this touches the JS bridge; the wiring
  # to Worker.postMessage / onmessage lives in Compositor.
  # -------------------------------------------------------------------------

  # Allocate a fresh window id for an external client, build an ExternalWindow
  # and push it onto the stack. Returns the window.
  #
  # A normal window gets the next cascade slot and is raised+focused. A panel
  # (role "panel", e.g. the dock) skips cascade placement entirely (the
  # Compositor anchors it to the bottom-center each frame), is never focused,
  # and is not subject to saved-layout geometry — its size is its own.
  def register_external(title, req_w, req_h, role = "window")
    @next_id += 1
    granted_w = [req_w, Theme::MIN_W].max
    granted_h = [req_h, Theme::MIN_H].max
    panel = role == "panel"
    if panel
      x = 0
      y = 0
    else
      step = 28
      x = 60 + (@cascade % 6) * step
      y = 60 + (@cascade % 6) * step
      @cascade += 1
    end
    win = ExternalWindow.new(@next_id, title, x, y, granted_w, granted_h, role)
    @stack.push(win)
    @last_registered = win
    if panel
      win
    else
      focus(win)
      apply_saved(win)
      win
    end
  end

  # The window most recently created by register_external. The Compositor uses
  # this to wire an incoming worker to its window on `welcome` (a panel is never
  # focused, so wm.focused would not identify it).
  def last_registered = @last_registered

  # Route a decoded client message (a Hash) to the right window. Returns the
  # symbol :welcome / :commit / :title / :closed / :ignored describing what we
  # did, so callers (the Compositor, and tests) can react.
  def handle_client_message(msg)
    record(msg)
    case msg[:type]
    when "hello"
      role = msg[:role].to_s == "panel" ? "panel" : "window"
      win = register_external(msg[:title] || "client", msg[:w] || 200, msg[:h] || 150, role)
      win.sab = msg[:sab]
      win.ctl = msg[:ctl]   # optional seqlock control word; nil for older clients
      win.stride = msg[:stride] || (4 * win.w)
      :welcome
    when "launch"
      # A client asks the compositor to start another client. Validate the app
      # id against the LAUNCHABLE registry; an unknown id is dropped (never spawn
      # from an untrusted id). The actual Worker spawn is JS-touching, so it lives
      # in the Compositor — here we only report whether the id is launchable
      # (under any supported descriptor shape: static path, OCI ref, ...).
      launchable?(msg[:app]) ? :launch : :ignored
    when "commit"
      win = find(msg[:window_id])
      return :ignored unless win&.external?
      win.merge_damage(msg[:damage] || { x: 0, y: 0, w: win.w, h: win.h })
      :commit
    when "set_title"
      win = find(msg[:window_id])
      return :ignored unless win
      win.title = msg[:title].to_s
      :title
    when "request_close"
      win = find(msg[:window_id])
      return :ignored unless win
      close(win)
      :closed
    when "request_resize"
      # Reserved — the SDK does not implement size renegotiation yet.
      :ignored
    else
      :ignored
    end
  end

  # Build the input payload the compositor sends to a client. Returns a Hash
  # in surface-local coordinates, or nil if the event has no useful payload
  # for an external window (e.g. it hit a decoration).
  def translate_input(win, kind, screen_x, screen_y, extra = {})
    return nil unless win
    payload = { kind: kind }
    # `||` line-continuation is unreliable in rbgo, so collect the two acceptable
    # kind forms (symbol + string) into a single array first.
    sym_kinds = [:mousedown, :mouseup, :mousemove, :wheel]
    str_kinds = ["mousedown", "mouseup", "mousemove", "wheel"]
    if sym_kinds.include?(kind) || str_kinds.include?(kind)
      payload[:x] = screen_x - win.x
      payload[:y] = screen_y - win.y
    end
    extra.each { |k, v| payload[k] = v }
    payload
  end

  # NOTE: `record` is logically private but rbgo does not implement Ruby's
  # `private` modifier yet, so we leave the method public.
  def record(msg)
    @last_messages << msg
    @last_messages.shift while @last_messages.length > 16
  end
end

# ---------------------------------------------------------------------------
# Compositor — owns the WM, the canvas and the input/render loop. This is the
# only part that talks to the JS bridge.
# ---------------------------------------------------------------------------
class Compositor
  def initialize(wm)
    @wm = wm
    @drag = nil      # {win:, mode: :move|:resize, dx:, dy:}
    @menu = nil      # {x:, y:, items: [[label, action]]}
    @frames = 0
    @last_t = 0.0
    @fps = 0.0
    @last_layout_sig = nil
  end

  # --- persistence ---------------------------------------------------------
  # The window layout (size + position per title) survives a page reload by
  # round-tripping through localStorage. restore_layout runs at boot, before the
  # initial spawns; tick writes back whenever the layout actually changes.
  LAYOUT_KEY = "wasmbox.layout"

  # Load the saved layout into the WM so spawn/register_external can apply it.
  # Degrades to an empty layout when storage is unavailable (e.g. private mode).
  def restore_layout
    store = JS.window.get("localStorage")
    return nil unless store
    raw = store.call("getItem", LAYOUT_KEY)
    text = raw.nil? ? "" : raw.to_s
    @wm.parse_layout(text)
  end

  # Persist the current layout. Called from tick only when the signature changed.
  def save_layout
    store = JS.window.get("localStorage")
    return nil unless store
    store.call("setItem", LAYOUT_KEY, @wm.serialize_layout)
  end

  # --- bridge wiring -------------------------------------------------------
  def attach_to_canvas(canvas_id)
    @doc = JS.document
    @canvas = @doc.call("getElementById", canvas_id)
    @ctx = @canvas.call("getContext", "2d")
    fit_canvas
    JS.window.on("resize") { |_e| fit_canvas }
    install_input
    expose_external_spawner
  end

  # Publish the spawn-external hook: when the page calls
  # globalThis.wasmboxSpawnExternal(url), the loader (in index.html) dispatches
  # a CustomEvent on a bus element we listen to here, so Ruby can react.
  #
  # The bridge cannot wrap a Ruby Proc into a JS function directly — only
  # JS::Ref#on does — so we route everything cross-language through DOM event
  # targets. A `__wasmbox_bus` element receives spawn requests; per-worker
  # `__wasmbox_worker_N` elements receive incoming messages. The OCI twin
  # (`wasmboxSpawnExternalOCI(ref)`) dispatches `wasmbox-spawn-external-oci`
  # on the same bus and we route it through spawn_external_oci.
  def expose_external_spawner
    @bus = @doc.call("createElement", "div")
    @bus.set("id", "__wasmbox_bus")
    @doc.get("body").call("appendChild", @bus)
    @bus.on("wasmbox-spawn-external") do |e|
      url = e.get("detail")
      spawn_external(url.to_s)
    end
    @bus.on("wasmbox-spawn-external-oci") do |e|
      ref = e.get("detail")
      spawn_external_oci(ref.to_s)
    end
    @worker_seq = 0
    @workers_by_id = {}
  end

  # Create a Web Worker for `worker_url`, wire its `message` listener to a
  # per-worker bus element, and remember the worker ref so we can postMessage
  # input events back to it. Returns the worker ref.
  def spawn_external(worker_url)
    worker = JS.global.call("wasmboxSpawnWorker", worker_url)
    @worker_seq += 1
    seq = @worker_seq
    bus_id = "__wasmbox_worker_#{seq}"
    bus = @doc.call("createElement", "div")
    bus.set("id", bus_id)
    @doc.get("body").call("appendChild", bus)
    @workers_by_id[seq] = { worker: worker, bus: bus }
    bus.on("wasmbox-msg") { |e| route_worker_message(worker, e.get("detail")) }
    JS.global.call("wasmboxAttachWorker", worker, bus_id)
    worker
  end

  # OCI twin of spawn_external. We register the bus + the wasmbox-msg
  # listener up front (same shape as the static path), then hand the JS side
  # a `wasmboxSpawnFromOCIAndAttach(ref, bus_id)` call which asynchronously
  # pulls the manifest + blobs, spawns the worker from the resulting blob
  # URLs, and attaches its `message` listener to the bus. The wrapper ref
  # cannot be captured synchronously here (the JS spawn is async); instead,
  # `wasmboxSpawnFromOCIAndAttach` stashes it on the bus element under
  # `_wasmboxWrapper`, and route_worker_message pulls it back when the first
  # inbound message lands. This keeps the static path lean while letting the
  # async OCI path slot into the same wm/Compositor wiring as a static spawn.
  def spawn_external_oci(ref)
    @worker_seq += 1
    seq = @worker_seq
    bus_id = "__wasmbox_worker_#{seq}"
    bus = @doc.call("createElement", "div")
    bus.set("id", bus_id)
    @doc.get("body").call("appendChild", bus)
    # We do NOT have a worker ref yet — the JS spawn is async. The listener
    # closes over the bus, which after the JS spawn carries _wasmboxWrapper;
    # we pass that to route_worker_message as the worker ref for outbound
    # postMessage on welcome/closed/launch.
    @workers_by_id[seq] = { worker: nil, bus: bus, ref: ref }
    bus.on("wasmbox-msg") do |e|
      wrapper = bus.get("_wasmboxWrapper")
      route_worker_message(wrapper, e.get("detail")) unless wrapper.nil?
    end
    JS.global.call("wasmboxSpawnFromOCIAndAttach", ref, bus_id)
    nil
  end

  # Decode a JS message and route it. The pure-Ruby dispatch
  # (register/merge_damage/title/close) is delegated to WindowManager so it
  # stays unit-testable; only the JS-touching pieces (ImageData construction,
  # welcome/closed postMessage) live here.
  def route_worker_message(worker, data)
    msg = decode_message(data)
    return nil unless msg
    result = @wm.handle_client_message(msg)
    case result
    when :welcome
      # The just-registered window is the one we want to wire to this worker.
      # A panel is never focused, so we use last_registered rather than focused.
      win = @wm.last_registered
      win.worker = worker
      build_image_data(win)
      welcome = JS.global.call("wasmboxMakeObject",
        "type", "welcome",
        "window_id", win.id,
        "granted_w", win.w,
        "granted_h", win.h)
      JS.global.call("wasmboxPostMessage", worker, welcome)
    when :launch
      # Validated launch: route by descriptor shape.
      #   String      → wasmboxSpawnWorker(url) via spawn_external
      #   {oci: ref}  → wasmboxSpawnFromOCI(ref) via spawn_external_oci
      # The registry guarantees the descriptor is trusted; handle_client_message
      # already dropped unknown ids.
      url = @wm.launchable_url(msg[:app])
      if !url.nil?
        spawn_external(url)
      else
        ref = @wm.launchable_oci(msg[:app])
        spawn_external_oci(ref) unless ref.nil?
      end
    when :closed
      # handle_client_message already removed the window; tell the worker.
      win_id = msg[:window_id]
      stub_msg = JS.global.call("wasmboxMakeObject",
        "type", "closed", "window_id", win_id, "reason", "client")
      JS.global.call("wasmboxPostMessage", worker, stub_msg)
    end
  end

  # Pull the protocol fields out of the JS message object. We hand a plain
  # Ruby Hash to handle_client_message so the dispatcher logic stays portable
  # and unit-testable off-wasm.
  def decode_message(data)
    return nil if data.nil?
    type = data.get("type")
    return nil if type.nil?
    h = { type: type.to_s }
    %w[window_id w h stride title role app].each do |k|
      v = data.get(k)
      h[k.to_sym] = v unless v.nil?
    end
    damage = data.get("damage")
    unless damage.nil?
      h[:damage] = {
        x: damage.get("x"),
        y: damage.get("y"),
        w: damage.get("w"),
        h: damage.get("h"),
      }
    end
    sab = data.get("sab")
    h[:sab] = sab unless sab.nil?
    h
  end

  # Once we know the SAB and dimensions, build the ImageData view that
  # putImageData will read from. Sharing the SAB means the worker's writes
  # become visible without any per-frame copy on this side.
  def build_image_data(win)
    # Pass the optional seqlock control word (win.ctl, nil for older clients) so
    # the blit helper can skip a half-painted surface. nil -> JS sees no `ctl`
    # and the fence is a no-op.
    win.image_data = JS.global.call("wasmboxNewImageData", win.sab, win.w, win.h, win.ctl)
  end

  # Tell a client its window is going away. Safe to call with an in-process
  # window — only external windows carry a worker ref.
  def notify_closed(win, reason)
    return nil unless win.external?
    return nil unless win.worker
    msg = JS.global.call("wasmboxMakeObject",
      "type", "closed",
      "window_id", win.id,
      "reason", reason)
    JS.global.call("wasmboxPostMessage", win.worker, msg)
  end

  def fit_canvas
    w = JS.window.get("innerWidth")
    h = JS.window.get("innerHeight")
    @canvas.set("width", w)
    @canvas.set("height", h)
    @width = w
    @height = h
  end

  def install_input
    @canvas.on("mousedown")   { |e| on_mousedown(e) }
    @canvas.on("mousemove")   { |e| on_mousemove(e) }
    @canvas.on("mouseup")     { |e| on_mouseup(e) }
    @canvas.on("contextmenu") { |e| on_contextmenu(e) }
    JS.window.on("keydown")   { |e| on_keydown(e) }
    JS.window.on("keyup")     { |e| on_keyup(e) }
  end

  def on_mouseup(e)
    @drag = nil
    win = @wm.focused
    return nil unless win&.external?
    mx = e.get("offsetX")
    my = e.get("offsetY")
    return nil unless win.hit?(win.body_rect, mx, my)
    forward_mouse_to_client(win, "mouseup", mx, my, e)
  end

  def on_keyup(e)
    win = @wm.focused
    return nil unless win&.external?
    forward_key_to_client(win, "keyup", e)
  end

  def forward_mouse_to_client(win, kind, mx, my, e)
    return nil unless win.worker
    button = e.get("button")
    payload = JS.global.call("wasmboxMakeObject",
      "type", "input",
      "window_id", win.id,
      "event", JS.global.call("wasmboxMakeObject",
        "kind", kind,
        "x",    mx - win.x,
        "y",    my - win.y,
        "button", button.nil? ? 0 : button))
    JS.global.call("wasmboxPostMessage", win.worker, payload)
  end

  def forward_key_to_client(win, kind, e)
    return nil unless win.worker
    key  = e.get("key")
    code = e.get("code")
    payload = JS.global.call("wasmboxMakeObject",
      "type", "input",
      "window_id", win.id,
      "event", JS.global.call("wasmboxMakeObject",
        "kind", kind,
        "key",  key.nil? ? "" : key,
        "code", code.nil? ? "" : code))
    JS.global.call("wasmboxPostMessage", win.worker, payload)
  end

  # --- input handlers ------------------------------------------------------
  def on_mousedown(e)
    mx = e.get("offsetX")
    my = e.get("offsetY")

    # A menu is open: a click either activates an item or dismisses it.
    if @menu
      handle_menu_click(mx, my)
      return
    end

    # A panel (the dock) is always-on-top and takes the click on geometric
    # hover without stealing focus from the app windows. Forward mousedown to it
    # (so an icon click reaches the dock, which then posts a `launch`) and stop.
    panel = panel_at(mx, my)
    if panel
      forward_mouse_to_client(panel, "mousedown", mx, my, e) if panel.external?
      return
    end

    win = @wm.window_at(mx, my)
    unless win
      return # empty desktop, left button: nothing (right button = menu)
    end

    @wm.focus(win)

    if win.on_close?(mx, my)
      @wm.close(win)
      notify_closed(win, "user")
    elsif win.on_resize?(mx, my)
      @drag = { win: win, mode: :resize, dx: win.right - mx, dy: win.bottom - my }
    elsif win.on_titlebar?(mx, my)
      @drag = { win: win, mode: :move, dx: mx - win.x, dy: my - win.y }
    elsif win.external? && win.hit?(win.body_rect, mx, my)
      forward_mouse_to_client(win, "mousedown", mx, my, e)
    end
  end

  def on_mousemove(e)
    if @drag
      mx = e.get("offsetX")
      my = e.get("offsetY")
      win = @drag[:win]
      if @drag[:mode] == :move
        win.move_to(mx - @drag[:dx], my - @drag[:dy])
      else
        win.resize_to(mx + @drag[:dx] - win.x, my + @drag[:dy] - win.y)
      end
      return
    end
    mx = e.get("offsetX")
    my = e.get("offsetY")
    # No drag in progress. A panel (the dock) receives pointer events on
    # geometric HOVER, not on focus — so its magnification tracks the cursor
    # without it ever being the keyboard-focused window. A hovered panel wins
    # over the focused window (panels are always-on-top).
    panel = panel_at(mx, my)
    if panel
      forward_mouse_to_client(panel, "mousemove", mx, my, e)
      return
    end
    # Otherwise forward hovers to the focused external window.
    win = @wm.focused
    return nil unless win&.external?
    return nil unless win.hit?(win.body_rect, mx, my)
    forward_mouse_to_client(win, "mousemove", mx, my, e)
  end

  # The top-most panel whose body is under (px, py), or nil. Panels are the
  # always-on-top stratum, so a panel under the pointer takes hover input even
  # when a normal window is focused.
  def panel_at(px, py)
    hit = nil
    @wm.panels.each { |p| hit = p if p.hit?(p.body_rect, px, py) }
    hit
  end

  def on_contextmenu(e)
    e.call("preventDefault")
    mx = e.get("offsetX")
    my = e.get("offsetY")
    win = @wm.window_at(mx, my)
    @menu = if win
      { x: mx, y: my, items: [
        ["Raise",  -> { @wm.focus(win) }],
        ["Close",  -> { @wm.close(win) }],
      ] }
    else
      { x: mx, y: my, items: [
        ["New window", -> { @wm.spawn("xterm ##{@wm.windows.length + 1}") }],
        ["New window (wide)", -> { w = @wm.spawn("editor", 320, 200) }],
      ] }
    end
  end

  def on_keydown(e)
    key = e.get("key")
    case key
    when "Tab"
      e.call("preventDefault")
      @wm.cycle
      return
    when "Escape"
      @menu = nil
      return
    end
    win = @wm.focused
    forward_key_to_client(win, "keydown", e) if win&.external?
  end

  MENU_ITEM_H = 22
  MENU_W = 150

  def handle_menu_click(mx, my)
    items = @menu[:items]
    x = @menu[:x]
    y = @menu[:y]
    if mx >= x && mx < x + MENU_W && my >= y && my < y + items.length * MENU_ITEM_H
      idx = (my - y) / MENU_ITEM_H
      items[idx][1].call
    end
    @menu = nil
  end

  # --- rendering -----------------------------------------------------------
  def start
    loop_frame = nil
    loop_frame = proc do |t|
      tick(t || 0.0)
      render
      JS.raf(&loop_frame)
    end
    JS.raf(&loop_frame)
  end

  def tick(t)
    @frames += 1
    if @last_t > 0 && t > @last_t
      inst = 1000.0 / (t - @last_t)
      @fps = @fps == 0.0 ? inst : (@fps * 0.9 + inst * 0.1) # smoothed
    end
    @last_t = t

    # Persist the layout whenever it actually changed (a move, resize, spawn,
    # close or restack), not every frame.
    sig = @wm.layout_signature
    if sig != @last_layout_sig
      @last_layout_sig = sig
      save_layout
    end
  end

  def render
    draw_desktop
    # Re-anchor every panel to the bottom-center of the current canvas, so the
    # dock tracks viewport resizes and never cascades.
    @wm.panels.each { |p| @wm.anchor_panel(p, @width, @height) }
    # Draw normal windows first, then panels on top (always-on-top stratum).
    @wm.ordered_windows.each { |win| draw_window(win) }
    draw_menu if @menu
    draw_hud
  end

  def fill_rect(rect, colour)
    x, y, w, h = rect
    @ctx.set("fillStyle", colour)
    @ctx.call("fillRect", x, y, w, h)
  end

  def stroke_rect(rect, colour, lw = 1)
    x, y, w, h = rect
    @ctx.set("strokeStyle", colour)
    @ctx.set("lineWidth", lw)
    @ctx.call("strokeRect", x + 0.5, y + 0.5, w - 1, h - 1)
  end

  def text(str, x, y, colour, size = 12)
    @ctx.set("fillStyle", colour)
    @ctx.set("font", "#{size}px ui-monospace, Menlo, monospace")
    @ctx.call("fillText", str, x, y)
  end

  def draw_desktop
    fill_rect([0, 0, @width, @height], Theme::DESKTOP)
    # A faint grid so window motion reads clearly.
    @ctx.set("strokeStyle", Theme::DESKTOP_GRID)
    @ctx.set("lineWidth", 1)
    step = 40
    gx = 0
    while gx < @width
      @ctx.call("beginPath")
      @ctx.call("moveTo", gx + 0.5, 0)
      @ctx.call("lineTo", gx + 0.5, @height)
      @ctx.call("stroke")
      gx += step
    end
    gy = 0
    while gy < @height
      @ctx.call("beginPath")
      @ctx.call("moveTo", 0, gy + 0.5)
      @ctx.call("lineTo", @width, gy + 0.5)
      @ctx.call("stroke")
      gy += step
    end
  end

  def draw_window(win)
    # A panel (the dock) is undecorated: no titlebar, no close box, no resize
    # grip, no frame border. Its surface IS the window, so we blit the SAB
    # straight at the body rectangle. The blit composites (source-over, see
    # wasmboxBlitFromSAB), so the transparent corners outside the dock bar show
    # the desktop through instead of a black rectangle.
    if win.panel?
      blit_external(win) if win.external?
      return
    end

    active = win.focused?

    # Titlebar.
    fill_rect(win.titlebar_rect, active ? Theme::TITLE_ACTIVE : Theme::TITLE_INACTIVE)
    tx, ty, _tw, _th = win.titlebar_rect
    text(win.title, tx + 6, ty + 15, Theme::TITLE_TEXT)

    # Close box.
    cx, cy, cw, ch = win.close_rect
    fill_rect(win.close_rect, Theme::CLOSE_BG)
    @ctx.set("strokeStyle", Theme::CLOSE_GLYPH)
    @ctx.set("lineWidth", 1.5)
    @ctx.call("beginPath")
    @ctx.call("moveTo", cx + 3, cy + 3)
    @ctx.call("lineTo", cx + cw - 3, cy + ch - 3)
    @ctx.call("moveTo", cx + cw - 3, cy + 3)
    @ctx.call("lineTo", cx + 3, cy + ch - 3)
    @ctx.call("stroke")

    # Client body. In-process windows paint a solid fill; external windows
    # blit their SharedArrayBuffer through a cached ImageData view.
    if win.external?
      blit_external(win)
    else
      fill_rect(win.body_rect, win.fill)
    end

    # Resize grip (bottom-right), drawn as two short diagonals.
    rx, ry, rw, rh = win.resize_rect
    @ctx.set("strokeStyle", Theme::RESIZE_GRIP)
    @ctx.set("lineWidth", 1)
    @ctx.call("beginPath")
    @ctx.call("moveTo", rx + rw, ry + rh * 0.4)
    @ctx.call("lineTo", rx + rw * 0.4, ry + rh)
    @ctx.call("moveTo", rx + rw, ry + rh * 0.75)
    @ctx.call("lineTo", rx + rw * 0.75, ry + rh)
    @ctx.call("stroke")

    # 1px decoration border around the whole frame.
    stroke_rect(win.frame_rect, active ? Theme::BORDER_ACTIVE : Theme::BORDER_INACTIVE, Theme::BORDER)
  end

  # Blit an external window's SharedArrayBuffer onto the canvas. Chrome forbids
  # constructing an ImageData over a SAB-backed Uint8ClampedArray, so the JS
  # helper wasmboxBlitFromSAB() owns a non-shared ImageData and copies the
  # damage rect out of the SAB into it before putImageData.
  def blit_external(win)
    return nil unless win.image_data
    d = win.clipped_damage || { x: 0, y: 0, w: win.w, h: win.h }
    JS.global.call("wasmboxBlitFromSAB", @ctx, win.image_data,
                   win.x, win.y, d[:x], d[:y], d[:w], d[:h])
    win.clear_damage
  end

  def draw_menu
    items = @menu[:items]
    x = @menu[:x]
    y = @menu[:y]
    h = items.length * MENU_ITEM_H
    fill_rect([x, y, MENU_W, h], Theme::MENU_BG)
    stroke_rect([x, y, MENU_W, h], Theme::MENU_BORDER, 1)
    items.each_with_index do |(label, _action), i|
      iy = y + i * MENU_ITEM_H
      text(label, x + 8, iy + 15, Theme::MENU_TEXT)
    end
  end

  def draw_hud
    n = @wm.windows.length
    line = "rbgo compositor — #{n} window#{n == 1 ? '' : 's'} — #{'%.0f' % @fps} fps — frame #{@frames}"
    text(line, 10, @height - 12, Theme::HUD_TEXT, 12)
  end
end

# ---------------------------------------------------------------------------
# Boot: build the WM, spawn a few clients, attach to the canvas and run.
# ---------------------------------------------------------------------------
wm = WindowManager.new
comp = Compositor.new(wm)
comp.restore_layout # localStorage -> wm.saved_layout, before the spawns apply it

wm.spawn("xterm")
wm.spawn("editor", 300, 190)
wm.spawn("about rbgo", 220, 130)

comp.attach_to_canvas("screen")
comp.start

JS.log("rbgo compositor: started with #{wm.windows.length} windows")
