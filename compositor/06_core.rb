class Compositor
  # Master switch for window snapping / half-tiling (first Batch-4 window-
  # management feature). `false` makes every snap path below a no-op: the
  # Super/⌘+arrow shortcuts fall through to the focused client, the drag-to-edge
  # preview never appears, and the geometry test hook publishes nothing — so main
  # stays byte-shippable during the live co-edit exactly as it was before.
  SNAPPING = true

  # Drag-to-edge hot-zone: a move-drag whose pointer comes within this many
  # pixels of a screen edge arms a snap (left/right edge → half, top edge →
  # maximize) and shows the landing preview; released there, the window snaps.
  SNAP_EDGE_MARGIN = 16

  def initialize(wm)
    @wm = wm
    @drag = nil      # {win:, mode: :move|:resize, dx:, dy:}
    # Active drag-to-edge snap target (:left / :right / :max) or nil. Set while a
    # move-drag hovers an edge (on_mousemove), consumed on release (on_mouseup),
    # and drawn as a translucent landing preview each frame (draw_snap_preview).
    @snap_preview = nil
    # @menu is either nil (no popup) or a Hash describing the active popup:
    #   { x:, y:, menu: <Menu>, kind: :root|:window, win:?, hover:?,
    #     submenu_idx:?, submenu:?, submenu_x:?, submenu_y:?, submenu_hover:? }
    # The :root variant is the desktop right-click root menu (hierarchical,
    # with one open submenu at a time). The :window variant is the per-window
    # context menu (flat, with Raise/Close). Both share the Menu rendering /
    # hit-test path; only the action dispatcher branches on :kind.
    @menu = nil
    @frames = 0
    @last_t = 0.0
    @fps = 0.0
    @last_layout_sig = nil
    @opentype_text = false # flips true after the one-shot AA-text opt-in
    # --- dirty-rect + idle-repaint gate state ------------------------------
    # The render loop runs every rAF, but compute_damage decides both WHETHER a
    # frame is painted at all (the #88 idle gate: no damage → skip) and, when it
    # is, WHICH regions to repaint (dirty-rect compositing: only the changed
    # rectangles, not the whole canvas).
    #
    # @force_paint is the sticky "dirty" belt every input + client/bus message
    # entry point raises (mark_dirty). It guarantees the next frame composites;
    # compute_damage localises the change to a region when it can and otherwise
    # falls back to a full recomposite. Cleared the instant a frame is painted.
    # true on construction so the FIRST frame always paints (nothing drawn yet).
    @force_paint = true
    # @damage accumulates the regions that changed since the last composited
    # frame; compute_damage rebuilds it each frame by diffing the live scene
    # against the retained snapshot of what we last drew (@prev_* below).
    @damage = DamageSet.new
    @prev_wins = {}          # id => { vk: <visual signature>, b: <screen bounds> }
    @cur_wins = {}           # working snapshot built during compute_damage
    @prev_seqs = {}          # id => committed content seq of the last drawn frame
    @cur_seqs = {}           # working snapshot built during compute_damage
    @prev_globals = nil      # [frame_name, workspace, width, height]
    @prev_overlay_active = false  # was a full-screen overlay/menu up last frame?
    @prev_applet_sig = nil   # combined applet content signature last drawn
    @prev_applet_layout = nil # applet set + positions last drawn
    @prev_tray_sig = nil     # tray strip content signature last drawn
    @prev_notify_sig = nil   # toast stack content signature last drawn
    @prev_notify_count = 0   # toast count last drawn (for the clear-band extent)
    @rendered_frames = 0     # frames we ACTUALLY composited (HUD counter)
    # @bench_no_gate / @bench_no_sprite are optional bench seams: cmd/rbbench
    # sets them via instance_variable_set to A/B the old (full redraw every
    # frame) vs new (dirty-rect gate) paths in one binary. They are nil (falsy)
    # in the shipped compositor, so production never touches the legacy path.
  end

  # Raise the idle-repaint dirty flag: the next rAF frame will paint. Called from
  # every input handler (install_input wraps each DOM event), every client / bus
  # message (route_worker_message + the spawn bus), and viewport resize
  # (fit_canvas) — i.e. every externally-triggered state change. Self-driven
  # changes (a live applet tick, a window commit) are detected in compute_damage
  # instead, so they need no explicit call here.
  def mark_dirty
    @force_paint = true
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

  # The desktop wallpaper choice (Wallpaper.current_name) survives a reload the
  # same way the layout does — a single localStorage key holding the preset
  # name. restore_wallpaper runs at boot (before start), save_wallpaper fires
  # only when the selection actually changes (the root-menu :wallpaper dispatch
  # or a set_wallpaper wire message), never per frame.
  WALLPAPER_KEY = "wasmbox.wallpaper"

  # Load the saved wallpaper into the Wallpaper registry. Degrades to the Grid
  # default when storage is unavailable (private mode) or the key is unset /
  # names an unknown preset (Wallpaper.select drops it).
  def restore_wallpaper
    store = JS.window.get("localStorage")
    return nil unless store
    raw = store.call("getItem", WALLPAPER_KEY)
    return nil if raw.nil?
    Wallpaper.select(raw.to_s)
  end

  # Persist the active wallpaper name. Called from the :wallpaper dispatch and
  # the :wallpaper_changed route only after a real change.
  def save_wallpaper
    store = JS.window.get("localStorage")
    return nil unless store
    store.call("setItem", WALLPAPER_KEY, Wallpaper.current_name)
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
      mark_dirty
      url = e.get("detail")
      spawn_external(url.to_s)
    end
    @bus.on("wasmbox-spawn-external-oci") do |e|
      mark_dirty
      ref = e.get("detail")
      spawn_external_oci(ref.to_s)
    end
    @bus.on("wasmbox-spawn-dom-window") do |e|
      mark_dirty
      detail = e.get("detail")
      url = detail.get("url").to_s
      w = detail.get("w").to_i
      h = detail.get("h").to_i
      title = detail.get("title").to_s
      spawn_dom_window(url, w, h, title)
    end
    # Notification test hook: __wasmboxPostNotification(opts) (compositor.worker.js)
    # dispatches a `notify` message here so a probe can trigger a toast without a
    # client. Routed through the FULL decode -> handle -> route path (nil worker,
    # since there is no posting client), so it exercises the real wiring. The
    # handler is inert when Compositor::NOTIFICATIONS is off (post_notification
    # no-ops), so registering it unconditionally keeps main shippable.
    @bus.on("wasmbox-notify") do |e|
      route_worker_message(nil, e.get("detail"))
    end
    # Tray test hooks: __wasmboxTrayAdd(opts) / __wasmboxTrayRemove(id)
    # (compositor.worker.js) dispatch `tray_add` / `tray_remove` messages here so
    # a probe can populate + clear the strip without a client. Routed through the
    # FULL decode -> handle -> route path (nil worker → compositor-owned items),
    # so they exercise the real wiring. Inert when Compositor::TRAY is off
    # (add/remove_tray_item no-op), so registering them unconditionally keeps main
    # shippable.
    @bus.on("wasmbox-tray-add") do |e|
      route_worker_message(nil, e.get("detail"))
    end
    @bus.on("wasmbox-tray-remove") do |e|
      route_worker_message(nil, e.get("detail"))
    end
    # Applet test hooks: __wasmboxAppletToggle(kind) / __wasmboxAppletPlace(kind,
    # x, y) / __wasmboxAppletRemove(kind) (compositor.worker.js) dispatch here so
    # a probe can add/place/remove desktop applets without driving the root menu.
    # Applets are compositor-owned (no wm involvement), so these call the applet
    # helpers (14_applets.rb) directly rather than route_worker_message. Inert
    # when Compositor::APPLETS is off (the helpers no-op), so registering them
    # unconditionally keeps main shippable.
    @bus.on("wasmbox-applet-toggle") do |e|
      mark_dirty
      toggle_applet(e.get("detail").to_s)
    end
    @bus.on("wasmbox-applet-place") do |e|
      mark_dirty
      d = e.get("detail")
      place_applet(d.get("kind").to_s, d.get("x").to_i, d.get("y").to_i)
    end
    @bus.on("wasmbox-applet-remove") do |e|
      mark_dirty
      remove_applet(e.get("detail").to_s)
    end
    # Calendar-nav test hook: __wasmboxCalendarNav("prev"/"next")
    # (compositor.worker.js) dispatches here so test/probe-applets.mjs can step
    # the calendar applet's month without pixel-hunting the widget's header
    # arrows. Drives the SAME calendar_nav (14_applets.rb) the header-arrow click
    # path does. Inert when Compositor::APPLETS is off or no calendar tile is
    # shown, so registering it unconditionally keeps main shippable.
    @bus.on("wasmbox-calendar-nav") do |e|
      mark_dirty
      calendar_nav(e.get("detail").to_s)
    end
    # Wallpaper test hook: __wasmboxSetWallpaper(name) (compositor.worker.js)
    # dispatches a `set_wallpaper` message here so a probe can switch the desktop
    # background without a client. Routed through the FULL decode -> handle ->
    # route path (nil worker, since there is no posting client), so it exercises
    # the real wiring. Inert when the name is unknown/already-active
    # (Wallpaper.select returns nil -> :ignored), so registering it
    # unconditionally keeps main shippable.
    @bus.on("wasmbox-set-wallpaper") do |e|
      route_worker_message(nil, e.get("detail"))
    end
    # Snap test hook: __wasmboxSpawnWindow(title) (compositor.worker.js) dispatches
    # here so test/probe-snapping.mjs can put a real decorated, focused window on
    # the desktop WITHOUT racing a client's wasm load. It is an in-process Window
    # (compositor-painted from #fill), role "window", so it snaps like any client
    # window. Registering it unconditionally is harmless — it only ever fires from
    # the probe-only JS hook.
    @bus.on("wasmbox-spawn-window") do |e|
      mark_dirty
      @wm.spawn(e.get("detail").to_s)
      notify_windows_changed
    end
    # Live-thumbnail test hook: __wasmboxSpawnExternalStub(title, w, h)
    # (compositor.worker.js) dispatches a `hello`-shaped detail carrying a
    # SharedArrayBuffer surface here so test/probe-alttab.mjs / probe-expose.mjs
    # can put a REAL external (SAB) window on the desktop without racing a client's
    # wasm load — the deterministic surface whose pixels the Alt-Tab / Exposé grab
    # path captures. Routed through the SAME register_external + build_image_data
    # path a real client's welcome uses (spawn_external_stub). Registering it
    # unconditionally is harmless — it only ever fires from the probe-only JS hook.
    @bus.on("wasmbox-spawn-external-stub") do |e|
      mark_dirty
      spawn_external_stub(e.get("detail"))
    end
    # Alt-Tab test hook: __wasmboxAltTab(cmd) (compositor.worker.js) dispatches an
    # `alttab` command ("open"/"advance"/"advance_reverse"/"commit"/"cancel")
    # here so test/probe-alttab.mjs can drive the switcher without a real
    # metaKey/Alt chord (unreliable headless). Routed to the SAME transitions the
    # keyboard uses (15_alttab.rb#alttab_probe). Inert when Compositor::ALTTAB is
    # off (alttab_probe no-ops), so registering it unconditionally keeps main
    # shippable.
    @bus.on("wasmbox-alttab") do |e|
      mark_dirty
      alttab_probe(e.get("detail"))
    end
    # Spotlight test hook: __wasmboxSpotlight(cmd) (compositor.worker.js)
    # dispatches a spotlight command ("open"/"query:term"/"type:x"/"backspace"/
    # "down"/"up"/"commit"/"cancel") here so test/probe-spotlight.mjs can drive
    # the launcher without a real ⌘/Ctrl+Space chord (unreliable headless).
    # Routed to the SAME transitions the keyboard uses (16_spotlight.rb#
    # spotlight_probe). Inert when Compositor::SPOTLIGHT is off (spotlight_probe
    # no-ops), so registering it unconditionally keeps main shippable.
    @bus.on("wasmbox-spotlight") do |e|
      mark_dirty
      spotlight_probe(e.get("detail"))
    end
    # Exposé test hook: __wasmboxExpose(cmd) (compositor.worker.js) dispatches an
    # expose command ("toggle"/"open"/"left"/"right"/"up"/"down"/"select:i"/
    # "click:x,y"/"commit"/"cancel") here so test/probe-expose.mjs can drive the
    # spread without a real F3 chord + pixel-perfect thumbnail reads (unreliable
    # headless). Routed to the SAME transitions the keyboard + mouse use
    # (17_expose.rb#expose_probe). Inert when Compositor::EXPOSE is off
    # (expose_probe no-ops), so registering it unconditionally keeps main shippable.
    @bus.on("wasmbox-expose") do |e|
      mark_dirty
      expose_probe(e.get("detail"))
    end
    @worker_seq = 0
    @workers_by_id = {}
  end

  # spawn_dom_window creates a DOMWindow (chrome on the canvas, iframe body
  # overlaid by the main thread) at the next default placement. Calls
  # wasmboxIframeAttach on the JS side so the main thread materialises the
  # actual <iframe> element at the window's body-rect coordinates. The WM
  # keeps the window in its stack like any other; subsequent drags/resizes
  # republish the new body-rect via wasmboxIframeMove inside draw_window.
  def spawn_dom_window(url, w, h, title)
    w = 800 if w <= 0
    h = 600 if h <= 0
    title = "dom window" if title.nil? || title.empty?
    win = @wm.spawn_dom(title, w, h, url)
    body = win.body_rect
    JS.global.call("wasmboxIframeAttach", win.id, url, body[0], body[1], body[2], body[3])
    win
  end

  # Register a synthetic EXTERNAL (SharedArrayBuffer-backed) window from the
  # live-thumbnail test hook (__wasmboxSpawnExternalStub). It mirrors the
  # `:welcome` arm of route_worker_message — register_external + stash the SAB /
  # ctl refs + build_image_data — but there is no client worker, so it posts no
  # `welcome` back (worker stays nil) and never expects commits: the SAB already
  # holds a complete frame. The window is a normal ExternalWindow (role "window"),
  # so it composites, snaps, alt-tabs and exposes like any client surface, and its
  # pixels are reachable to wasmboxGrabWindow. Focused so a probe finds it.
  def spawn_external_stub(detail)
    return nil if detail.nil?
    title = detail.get("title")
    title = title.nil? ? "ext-stub" : title.to_s
    w = detail.get("w").to_i
    h = detail.get("h").to_i
    w = 240 if w <= 0
    h = 180 if h <= 0
    win = @wm.register_external(title, w, h, "window")
    win.sab = detail.get("sab")
    win.ctl = detail.get("ctl")
    win.stride = 4 * w
    win.merge_damage({ x: 0, y: 0, w: w, h: h })
    build_image_data(win)
    @wm.focus(win)
    notify_windows_changed
    win
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
    # Any client / bus message can change what is on screen (a hello, a commit, a
    # title change, a close, a tray/notification/applet/wallpaper bus event), so
    # raise the idle-repaint dirty flag before dispatching. Window pixel commits
    # ride the "commit" arm here too, so a client that repaints wakes the gate.
    mark_dirty
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
      # A new window joins the iconbar — but only AFTER the dock panel itself
      # has registered, otherwise notify_windows_changed is a no-op anyway. For
      # the dock's own welcome, notify_windows_changed still publishes the
      # initial list of any already-spawned in-process boot windows.
      notify_windows_changed
    when :launch
      # Validated launch: route by descriptor shape.
      #   String      → wasmboxSpawnWorker(url) via spawn_external
      #   {oci: ref}  → wasmboxSpawnFromOCI(ref) via spawn_external_oci
      #   {dom: url}  → spawn_dom_window (iframe overlay)
      # The registry guarantees the descriptor is trusted; handle_client_message
      # already dropped unknown ids.
      static_url = @wm.launchable_url(msg[:app])
      oci_ref    = @wm.launchable_oci(msg[:app])
      dom_desc   = @wm.launchable_dom(msg[:app])
      if !static_url.nil?
        spawn_external(static_url)
      elsif !oci_ref.nil?
        spawn_external_oci(oci_ref)
      elsif !dom_desc.nil?
        spawn_dom_window(dom_desc[:url], dom_desc[:w], dom_desc[:h], dom_desc[:title])
      end
    when :closed
      # handle_client_message already removed the window; tell the worker.
      win_id = msg[:window_id]
      stub_msg = JS.global.call("wasmboxMakeObject",
        "type", "closed", "window_id", win_id, "reason", "client")
      JS.global.call("wasmboxPostMessage", worker, stub_msg)
      # The client is gone — drop any status icons it registered (13_tray.rb).
      drop_tray_for_window(win_id)
      notify_windows_changed
    when :closed_by_peer
      # The dock right-clicked an iconbar entry — handle_client_message already
      # removed the window from the WM and stashed its worker ref. We post the
      # `closed` event to THAT worker (not the peer that asked), then refresh
      # the iconbar.
      stash = @wm.take_last_closed_by_peer
      if stash && stash[:external] && !stash[:worker].nil?
        stub_msg = JS.global.call("wasmboxMakeObject",
          "type", "closed", "window_id", stash[:window_id], "reason", "peer")
        JS.global.call("wasmboxPostMessage", stash[:worker], stub_msg)
      end
      # Drop any status icons the peer-closed window registered (13_tray.rb).
      drop_tray_for_window(stash[:window_id]) if stash
      notify_windows_changed
    when :restored
      # The dock asked to restore a minimized window — push a refreshed window
      # list so the icon switches back to its "open" style in the iconbar.
      notify_windows_changed
    when :focused
      # The dock asked to focus a window — push a refreshed window list so the
      # iconbar's focused indicator follows.
      notify_windows_changed
    when :title
      # A client renamed itself — refresh the iconbar so the new title shows.
      notify_windows_changed
    when :workspace_changed
      # The dock switched the active workspace. Two pushes back-to-back: first
      # the workspace_changed pulse so the workspace section repaints, then
      # the windows_changed snapshot — its content is now filtered to the new
      # workspace, so the iconbar updates as a side-effect of the same wire
      # call.
      notify_workspace_changed
      notify_windows_changed
    when :theme_changed
      # The dock asked to switch the active theme. Broadcast the new theme
      # to EVERY external client (the panel repaints with the new colours;
      # other clients may opt in via the SDK's onInput hook).
      notify_theme_changed
    when :wallpaper_changed
      # A client (or the set-wallpaper test hook) switched the desktop
      # background. draw_desktop_widgets repaints from Wallpaper.current on the
      # next rAF tick (its render-once cache re-renders exactly once); persist so
      # the choice survives a reload, mirroring the root-menu :wallpaper path.
      save_wallpaper
    when :notify
      # A client posted a desktop notification — push it onto the toast stack
      # (12_notifications.rb). `worker` is the poster, so a toast action can fire
      # back to it. The draw + auto-expiry run from render/tick.
      post_notification(msg, worker)
    when :tray_add
      # A client registered a status icon — add it to the tray strip
      # (13_tray.rb). `worker` is the poster, so a tray click can fire back to it.
      add_tray_item(msg, worker)
    when :tray_remove
      # A client removed one of its status icons by id.
      remove_tray_item(msg)
    when :tray_update
      # A client mutated a live status icon (glyph/image/tooltip) by id.
      update_tray_item(msg)
    end
  end

  # Encode the wm.windows_snapshot array as a tiny JSON string so the dock can
  # decode it through encoding/json without any new JS bridge helpers. The
  # snapshot is always [{id:<int>, title:<string>, minimized:<bool>,
  # focused:<bool>, role:<string>, workspace:<int>}, ...]; titles are escaped
  # for backslash, double-quote, newline, carriage return and tab — every
  # other ASCII character is passed through verbatim. Pure string
  # concatenation; no JSON library required (rbgo has none).
  def windows_json(wins)
    parts = []
    wins.each do |w|
      mflag = w[:minimized] ? "true" : "false"
      fflag = w[:focused] ? "true" : "false"
      s = '{"id":' + w[:id].to_s
      s += ',"title":"' + json_escape(w[:title].to_s) + '"'
      s += ',"minimized":' + mflag
      s += ',"focused":' + fflag
      s += ',"role":"' + json_escape(w[:role].to_s) + '"'
      s += ',"workspace":' + w[:workspace].to_s + '}'
      parts << s
    end
    "[" + parts.join(",") + "]"
  end

  def json_escape(s)
    out = ""
    i = 0
    while i < s.length
      c = s[i]
      case c
      when "\\" then out += "\\\\"
      when '"'  then out += '\\"'
      when "\n" then out += "\\n"
      when "\r" then out += "\\r"
      when "\t" then out += "\\t"
      else out += c
      end
      i += 1
    end
    out
  end

  # Push the current windows list (open + minimized + focus state) to the dock
  # (the first registered panel). Delivered as an `input` event of kind
  # "windows_changed" carrying a JSON-encoded array — this reuses the existing
  # input-event channel the dock's SDK already dispatches, so no new SDK
  # message type is needed. Fires from EVERY site that changes the windows
  # list, the focus, the minimized flag, OR the active workspace (the
  # filtered snapshot changes whenever the active workspace switches):
  # registration, close, minimize, restore, focus, cycle, set_title,
  # set_workspace. No-op when no dock panel is registered or it lacks a
  # worker ref.
  def notify_windows_changed
    panel = @wm.panels.first
    return nil unless panel
    return nil unless panel.external?
    return nil if panel.worker.nil?
    json = windows_json(@wm.windows_snapshot)
    payload = JS.global.call("wasmboxMakeObject",
      "type", "input",
      "window_id", panel.id,
      "event", JS.global.call("wasmboxMakeObject",
        "kind", "windows_changed",
        "windows_json", json))
    JS.global.call("wasmboxPostMessage", panel.worker, payload)
  end

  # Push the current active workspace + total workspace count to the dock as
  # an `input` event of kind "workspace_changed". Fires on every successful
  # set_workspace transition so the dock's workspace section can repaint
  # immediately, without waiting for the next windows_changed (which arrives
  # immediately after, but conceptually the workspace UI is independent of
  # the iconbar UI). No-op when no dock panel is registered or it lacks a
  # worker ref.
  def notify_workspace_changed
    panel = @wm.panels.first
    return nil unless panel
    return nil unless panel.external?
    return nil if panel.worker.nil?
    payload = JS.global.call("wasmboxMakeObject",
      "type", "input",
      "window_id", panel.id,
      "event", JS.global.call("wasmboxMakeObject",
        "kind", "workspace_changed",
        "active", @wm.active_workspace,
        "count", @wm.workspace_count))
    JS.global.call("wasmboxPostMessage", panel.worker, payload)
  end

  # Push the active theme to every external client as a `theme_changed`
  # input event carrying the name + the raw .themerc source. The dock
  # parses the .themerc locally with its bundled go-embed of the same file,
  # so the bytes the compositor ships are authoritative. Fires on every
  # successful set_theme transition. No-op when no external client is
  # connected.
  #
  # We broadcast to EVERY external client — not just the panel — so future
  # clients that want to follow the desktop theme can subscribe by reading
  # the same `theme_changed` event. Today only the dock honours it; other
  # clients silently ignore an unknown event kind.
  def notify_theme_changed
    src = @wm.theme_source(@wm.active_theme)
    return nil if src.nil?
    @wm.externals.each do |w|
      next if w.worker.nil?
      payload = JS.global.call("wasmboxMakeObject",
        "type", "input",
        "window_id", w.id,
        "event", JS.global.call("wasmboxMakeObject",
          "kind", "theme_changed",
          "name", @wm.active_theme,
          "themerc", src))
      JS.global.call("wasmboxPostMessage", w.worker, payload)
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
    # Scalar fields. `index`/`workspace` are dock + workspace controls; `name`
    # is the theme name for theme switching; `parent` + `rel_x` + `rel_y` carry
    # popup placement (without them a popup hello would land at (0,0)).
    # `lock_aspect` rides on `hello` (optional intrinsic ratio); `ratio` rides
    # on `set_lock_aspect` (post-handshake declaration).
    # `body`/`kind`/`icon`/`icon_w`/`icon_h`/`timeout`/`action_label`/`action`/
    # `actions` ride on `notify` (a desktop notification: headline + detail line,
    # severity, an optional leading icon — a stock glyph NAME, or base64 RGBA
    # pixels at `icon_w`x`icon_h` — seconds-until-dismiss, a legacy single
    # click-to-act button, and `actions`: a compact "Label|cb;Label2|cb2" scalar
    # for several buttons). Fields NOT listed here arrive nil, so a new notify
    # field MUST be added to this list (the go-widgets v0.86 icon/multiline/
    # multi-action refinements added `icon_w`/`icon_h`/`actions`).
    # `id`/`glyph`/`tooltip` ride on `tray_add`/`tray_remove`/`tray_update` (a
    # status icon: its caller-chosen id, a stock glyph name OR a base64 RGBA
    # `icon` at `w`x`h`, and the hover tooltip).
    # `summary`/`urgency`/`expire_timeout`/`app_icon`/`image_data`/`image_w`/
    # `image_h`/`resident` also ride on `notify` — the freedesktop.org
    # Desktop-Notification field set (see Notification.map_freedesktop): summary
    # (title), urgency byte (0/1/2), expire_timeout in MILLISECONDS (-1 default,
    # 0 never), the app_icon glyph name, inline image-data (base64 RGBA at
    # `image_w`x`image_h`) and the resident hint. `actions` is shared with the
    # native path (the compact "Label|key;..." scalar, key = the echoed action).
    %w[window_id w h stride title role app index workspace name parent rel_x rel_y lock_aspect ratio
       body kind icon icon_w icon_h timeout action_label action actions id glyph tooltip
       summary urgency expire_timeout app_icon image_data image_w image_h resident].each do |k|
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
    # SharedArrayBuffer refs: the pixel surface (sab) and the optional seqlock
    # control word (ctl). Both are JS::Refs, kept by reference (not cloned).
    sab = data.get("sab")
    h[:sab] = sab unless sab.nil?
    ctl = data.get("ctl")
    h[:ctl] = ctl unless ctl.nil?
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
  # window — only external windows carry a worker ref. For dom-windows the
  # body iframe lives in the main-thread DOM (not a worker), so we instead
  # post C2M_IFRAME_DETACH so the overlay manager removes the element.
  def notify_closed(win, reason)
    # Drop the closed window's cached widgets-frame buffer (see
    # 11_frame_widgets.rb) so a later window reusing the id never re-presents a
    # stale decoration.
    forget_frame_cache(win.id) if respond_to?(:forget_frame_cache)
    if win.dom?
      JS.global.call("wasmboxIframeDetach", win.id)
      return nil
    end
    return nil unless win.external?
    return nil unless win.worker
    msg = JS.global.call("wasmboxMakeObject",
      "type", "closed",
      "window_id", win.id,
      "reason", reason)
    JS.global.call("wasmboxPostMessage", win.worker, msg)
  end

  # Unmap a set of popup surfaces and tell each client it was closed. Used by
  # the pointer-grab dismissal and when a popup's parent window goes away.
  def dismiss_popups(list)
    list.each do |p|
      @wm.close(p)
      notify_closed(p, "user")
    end
  end

  def fit_canvas
    w = JS.window.get("innerWidth")
    h = JS.window.get("innerHeight")
    @canvas.set("width", w)
    @canvas.set("height", h)
    @width = w
    @height = h
    # Setting canvas.width/height clears the surface, so the next frame MUST
    # repaint even if nothing else changed.
    mark_dirty
  end

  # Every DOM input event is a potential state change, so each handler is wrapped
  # to raise the idle-repaint dirty flag before it runs. This is the belt half of
  # the belt-and-suspenders gate: any user input composites the next frame; the
  # suspenders half (compute_damage) additionally catches self-driven changes
  # (applet ticks, window commits) that arrive with no DOM event.
  def install_input
    @canvas.on("mousedown")   { |e| mark_dirty; on_mousedown(e) }
    @canvas.on("mousemove")   { |e| mark_dirty; on_mousemove(e) }
    @canvas.on("mouseup")     { |e| mark_dirty; on_mouseup(e) }
    @canvas.on("contextmenu") { |e| mark_dirty; on_contextmenu(e) }
    @canvas.on("wheel")       { |e| mark_dirty; on_wheel(e) }
    JS.window.on("keydown")   { |e| mark_dirty; on_keydown(e) }
    JS.window.on("keyup")     { |e| mark_dirty; on_keyup(e) }
  end

  # Scroll-wheel input. Forwarded to the panel under the pointer (the dock —
  # which uses wheel events on its workspace section to cycle workspaces) or
  # to the focused external window. The compositor itself does not consume
  # the wheel, so the page never scrolls in response (preventDefault keeps
  # the browser from scrolling underneath us).
  def on_wheel(e)
    e.call("preventDefault")
    mx = e.get("offsetX")
    my = e.get("offsetY")
    dy = e.get("deltaY")
    dx = e.get("deltaX")
    # Two-finger swipe (or wheel) over a window's TITLEBAR rolls it up/down:
    # swipe up folds the window to just its titlebar (shade), swipe down unfolds
    # it. The titlebar carries no scrollable content, so this never competes
    # with a client scrolling its body. deltaY > 0 is a swipe up under macOS
    # natural scrolling (the user's platform); flip the comparison if reversed.
    over = @wm.window_at(mx, my)
    if over && over.decorated? && over.on_titlebar?(mx, my)
      d = dy.nil? ? 0 : dy
      changed = d > 0 ? @wm.shade(over) : (d < 0 ? @wm.unshade(over) : nil)
      notify_windows_changed if changed
      return
    end
    panel = panel_at(mx, my)
    target = panel || @wm.focused
    return nil unless target&.external?
    return nil unless target.worker
    payload = JS.global.call("wasmboxMakeObject",
      "type", "input",
      "window_id", target.id,
      "event", JS.global.call("wasmboxMakeObject",
        "kind", "wheel",
        "x", mx - target.x,
        "y", my - target.y,
        "deltaX", dx.nil? ? 0 : dx,
        "deltaY", dy.nil? ? 0 : dy))
    JS.global.call("wasmboxPostMessage", target.worker, payload)
  end

  def on_mouseup(e)
    # End a live applet drag (persist its new position) and consume the mouseup.
    # Inert when no applet drag is active (so a no-op with APPLETS off).
    if @applet_drag
      applet_drag_end
      return
    end
    # Commit a drag-to-edge snap: if the move-drag ended over an armed edge, snap
    # the dragged window there. Done BEFORE clearing @drag so we still know which
    # window was being moved; the preview is cleared unconditionally after.
    if SNAPPING && @drag && @drag[:mode] == :move && @snap_preview
      commit_snap_preview(@drag[:win])
    end
    @snap_preview = nil
    @drag = nil
    win = @wm.focused
    return nil unless win&.external?
    mx = e.get("offsetX")
    my = e.get("offsetY")
    return nil unless win.hit?(win.body_rect, mx, my)
    forward_mouse_to_client(win, "mouseup", mx, my, e)
  end

  def on_keyup(e)
    # Releasing the Alt-Tab chord modifier commits the switcher: focus + raise
    # the highlighted window (15_alttab.rb). While the switcher is up it holds the
    # keyboard grab, so any other key-up is swallowed here. Self-gated on
    # Compositor::ALTTAB + an open switcher.
    if ALTTAB && alttab_active?
      k = e.get("key")
      alttab_commit if k == "Alt" || k == "Meta" || k == "Control"
      return
    end
    # Mirror on_keydown's routing: a key-up while a popup is open goes to that
    # popup (the keyboard grab), otherwise to the focused window.
    target = @wm.key_target
    return nil unless target&.external?
    forward_key_to_client(target, "keyup", e)
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

    # Exposé is a full-screen modal: while the spread is up a click focuses the
    # window whose tile was hit (and exits), or dismisses the spread when it lands
    # on the dim gap. Consumed here before anything beneath can see it. Self-gates
    # on Compositor::EXPOSE + an open spread.
    if EXPOSE && expose_active?
      expose_click(mx, my)
      return
    end

    # Toasts are the always-on-top overlay stratum: a click on one dismisses it
    # (and fires its action back to the posting client, if any) and is consumed
    # here before anything else can see the click. Self-gates on NOTIFICATIONS.
    toast = notify_at(mx, my)
    if toast
      dismiss_notification(toast)
      return
    end

    # The tray status strip is part of the same top overlay stratum: a click on
    # an icon is consumed here before anything beneath. A LEFT click fires the
    # item (or opens a compositor-owned indicator's menu); a RIGHT click is left
    # for on_contextmenu to open the menu (both consume the event). Self-gates on
    # TRAY.
    item = tray_at(mx, my)
    if item
      btn = e.get("button")
      tray_activate(item, mx, my) if btn.nil? || btn == 0
      return
    end

    # A menu is open: a click either activates an item or dismisses it.
    if @menu
      handle_menu_click(mx, my)
      return
    end

    # A client popup (menu / tooltip / submenu) grabs the pointer while open.
    # `popups` is bottom-to-top, so a child submenu sits AFTER its parent. A
    # click inside the top-most popup under the cursor is forwarded to it AND
    # closes any popups stacked above it (open submenus the user navigated away
    # from); a click outside every popup dismisses the whole chain. Either way
    # the click is consumed (one click to dismiss, like desktop menus).
    popups = @wm.popups
    unless popups.empty?
      hit = nil
      hit_i = -1
      i = 0
      popups.each do |p|
        if p.hit?(p.body_rect, mx, my)
          hit = p
          hit_i = i
        end
        i += 1
      end
      if hit
        above = []
        j = hit_i + 1
        while j < popups.length
          above.push(popups[j])
          j += 1
        end
        dismiss_popups(above)
        forward_mouse_to_client(hit, "mousedown", mx, my, e) if hit.external?
      else
        dismiss_popups(popups)
      end
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
      # Empty desktop: a left mousedown over an applet tile starts a drag (the
      # applet stratum sits behind the windows, so we only reach here when no
      # window is under the point — a window on top keeps its clicks). Self-gates
      # on Compositor::APPLETS. Otherwise nothing (right button = menu).
      applet_mousedown(mx, my, e)
      return
    end

    @wm.focus(win)
    # The focus call may have re-ordered the stack: tell the dock so its
    # iconbar's focused indicator follows the click.
    notify_windows_changed

    if win.on_close?(mx, my)
      dismiss_popups(@wm.child_popups(win.id))  # notify any orphaned popups first
      @wm.close(win)
      notify_closed(win, "user")
      notify_windows_changed
    elsif win.on_minimize?(mx, my)
      @wm.minimize(win)
      notify_windows_changed
    elsif win.on_resize?(mx, my)
      @drag = { win: win, mode: :resize, dx: win.right - mx, dy: win.bottom - my }
    elsif win.on_titlebar?(mx, my)
      @drag = { win: win, mode: :move, dx: mx - win.x, dy: my - win.y }
    elsif win.external? && win.hit?(win.body_rect, mx, my)
      forward_mouse_to_client(win, "mousedown", mx, my, e)
    end
  end

  def on_mousemove(e)
    # Exposé spread is up: a hover highlights the tile under the pointer (moves the
    # keyboard selection to it) and consumes the event before any window drag /
    # client forwarding. Self-gates on Compositor::EXPOSE + an open spread.
    if EXPOSE && expose_active?
      expose_hover(e.get("offsetX"), e.get("offsetY"))
      return
    end
    # A live applet drag moves the tile under the pointer and consumes the event
    # (before window drag / client forwarding). Inert unless a drag is active, so
    # this is a no-op when Compositor::APPLETS is off.
    if @applet_drag
      applet_drag_move(e.get("offsetX"), e.get("offsetY"))
      return
    end
    if @drag
      mx = e.get("offsetX")
      my = e.get("offsetY")
      win = @drag[:win]
      if @drag[:mode] == :move
        win.move_to(mx - @drag[:dx], my - @drag[:dy])
        # Drag-to-edge: arm (and preview) a snap when the pointer reaches a screen
        # edge; keep free-drag otherwise. A window already snapped by dragging
        # un-snaps back to free geometry the moment the pointer leaves every edge,
        # so the user can pull it away without a second gesture.
        if SNAPPING && win.decorated?
          @snap_preview = edge_snap_zone(mx, my)
        end
      else
        win.resize_to(mx + @drag[:dx] - win.x, my + @drag[:dy] - win.y)
      end
      return
    end
    mx = e.get("offsetX")
    my = e.get("offsetY")
    # An open menu captures hover so its highlight tracks the pointer and a
    # slide-over onto a parent submenu opener auto-switches the open submenu.
    # We do not forward to clients while a menu is up.
    if @menu
      handle_menu_hover(mx, my)
      return
    end
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

  # Right-click context menu. Two shapes:
  #   - over a window: a small per-window menu (Raise/Close), built directly
  #     as a Menu instance so the same draw/hit-test path serves it.
  #   - over the empty desktop: the Openbox-style hierarchical RootMenu
  #     (Applications, Workspaces, About/Reload/Exit) built from the WM's
  #     LAUNCHABLE registry + workspace count.
  # Right-click over a panel (the dock) is swallowed — the dock's own client
  # turns a right-click on an iconbar entry into a `close` wire message and
  # we must not double-open a desktop menu underneath it.
  def on_contextmenu(e)
    e.call("preventDefault")
    mx = e.get("offsetX")
    my = e.get("offsetY")
    # A right-click on a tray icon opens its menu (a compositor menu for a
    # built-in indicator, or a forwarded tray_menu event for an app item) and is
    # swallowed here so no desktop menu opens underneath. Self-gates on TRAY.
    item = tray_at(mx, my)
    if item
      open_tray_menu(item, mx, my)
      return
    end
    return if panel_at(mx, my)
    win = @wm.window_at(mx, my)
    if win
      # A right-click inside an external client's BODY belongs to the client: it
      # already receives the forwarded button-2 mousedown and shows its OWN
      # context menu (e.g. Files' New Folder / Rename). Opening a compositor menu
      # here too would stack two menus on the same click — the reported bug. The
      # compositor's window menu is offered only on the DECORATION (titlebar /
      # frame), so the two never collide.
      return if win.external? && win.hit?(win.body_rect, mx, my)
      menu = Menu.new([
        { label: "Raise", action: [:focus, win.id] },
        { label: "Close", action: [:close, win.id] },
      ])
      @menu = { kind: :window, x: mx, y: my, menu: menu, win: win, hover: -1,
                submenu_idx: -1, submenu: nil, submenu_x: 0, submenu_y: 0,
                submenu_hover: -1 }
    else
      # Pass the applet board (14_applets.rb) so the root menu grows an "Applets"
      # submenu toggling each desktop tile. applets_for_menu returns nil when
      # Compositor::APPLETS is off, in which case RootMenu omits the entry and the
      # menu is exactly what it was before applets landed.
      menu = RootMenu.build(@wm, applets_for_menu)
      @menu = { kind: :root, x: mx, y: my, menu: menu, hover: -1,
                submenu_idx: -1, submenu: nil, submenu_x: 0, submenu_y: 0,
                submenu_hover: -1 }
    end
  end

  def on_keydown(e)
    key = e.get("key")
    # An open client popup grabs the keyboard too. Escape dismisses the top-most
    # popup one level at a time (submenu before its parent — layered, like the
    # pointer grab); every other key is routed to that popup so a menu client
    # can do its own arrow/Enter navigation. Nothing reaches the window beneath.
    popups = @wm.popups
    unless popups.empty?
      top = popups.last
      if key == "Escape"
        dismiss_popups([top])
      elsif top.external?
        forward_key_to_client(top, "keydown", e)
      end
      return
    end
    # Exposé keyboard grab (17_expose.rb): while the spread is up it owns the
    # keyboard. Arrows move the selection across the grid, Enter focuses the
    # selected window + exits, Escape (or F3 again) exits without changing focus,
    # and every other key is swallowed so nothing leaks to a client. Self-gated on
    # Compositor::EXPOSE + an open spread.
    if EXPOSE && expose_active?
      case key
      when "ArrowLeft"       then expose_move(:left)
      when "ArrowRight"      then expose_move(:right)
      when "ArrowUp"         then expose_move(:up)
      when "ArrowDown"       then expose_move(:down)
      when "Enter", "Return" then expose_commit
      when "Escape", "F3"    then expose_cancel
      end
      e.call("preventDefault")
      return
    end
    # Exposé toggle chord: F3 fans every window on the workspace into the spread
    # (17_expose.rb). Checked before the switcher + `case key` so the key never
    # falls through to a client. Self-gated on Compositor::EXPOSE.
    if EXPOSE && expose_trigger?(key)
      e.call("preventDefault")
      expose_toggle
      return
    end
    # Spotlight launcher keyboard grab (16_spotlight.rb): while the palette is up
    # it owns the keyboard. Typing a printable character filters, Up/Down move the
    # highlight, Enter launches, Escape cancels, and every other key is swallowed
    # so nothing leaks to a client mid-search. Self-gated on Compositor::SPOTLIGHT.
    if SPOTLIGHT && spotlight_active?
      spotlight_key(key, e)
      e.call("preventDefault")
      return
    end
    # Spotlight OPEN chord: ⌘/Ctrl+Space raises the palette (16_spotlight.rb).
    # Checked before the switcher + `case key` so the chord never falls through
    # to a client as a literal space. Self-gated on Compositor::SPOTLIGHT.
    if SPOTLIGHT && spotlight_open_chord?(key, e)
      e.call("preventDefault")
      spotlight_open
      return
    end
    # Alt-Tab switcher keyboard grab (15_alttab.rb): while the overlay is up it
    # owns the keyboard. Tab advances the selection (Shift reverses), Enter
    # commits, Escape cancels, and every other key is swallowed so it never leaks
    # to a client mid-switch. Self-gated on Compositor::ALTTAB. The OPENING press
    # is handled in the `case key` "Tab" arm below (the switcher is not up yet).
    if ALTTAB && alttab_active?
      case key
      when "Tab"             then alttab_advance(e[:shiftKey] ? true : false)
      when "Enter", "Return" then alttab_commit
      when "Escape"          then alttab_cancel
      end
      e.call("preventDefault")
      return
    end
    case key
    when "Tab"
      # Alt/⌘+Tab (or Ctrl+Tab, an equivalent the headless env can always drive)
      # opens the visual window switcher (15_alttab.rb) and advances it; each
      # further Tab is caught by the grab block above. Plain Shift+Tab (no Alt)
      # keeps the instant quick-cycle. Bare Tab forwards to the focused window so
      # the terminal can use it for autocompletion (and an editor for indent).
      if ALTTAB && alttab_modifier?(e)
        e.call("preventDefault")
        alttab_key(e[:shiftKey] ? true : false)
        return
      end
      if e[:shiftKey]
        e.call("preventDefault")
        @wm.cycle
        notify_windows_changed
        return
      end
      # fall through to forward_key_to_client
    when "ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"
      # Super/⌘ + arrow snaps the focused window (Left = left half, Right = right
      # half, Up = maximize, Down = restore / un-snap). Only fires with the snap
      # modifier held AND a decorated window focused; otherwise the arrow falls
      # through to the client (a terminal/editor uses arrows for navigation).
      if SNAPPING && snap_modifier?(e) && handle_snap_key(key)
        e.call("preventDefault")
        return
      end
      # fall through to forward_key_to_client
    when "Escape"
      @menu = nil
      return
    end
    win = @wm.focused
    forward_key_to_client(win, "keydown", e) if win&.external?
  end

  # The snap chord modifier. The primary binding is Super/⌘ (metaKey), matching
  # macOS/GNOME half-tiling. Because some headless/browser setups never deliver
  # metaKey to a page keydown (the OS eats ⌘+arrow), Ctrl+Alt+arrow is accepted
  # as an equivalent the probe (and users on such setups) can always drive. Reads
  # the same bracket-access idiom as the Shift+Tab handler; an absent modifier
  # property is nil (falsy), so a bare arrow never counts as a chord.
  def snap_modifier?(e)
    return true if e[:metaKey]
    e[:ctrlKey] && e[:altKey] ? true : false
  end

  # The Alt-Tab chord modifier (15_alttab.rb). The primary binding is Alt/Option
  # (altKey), matching Windows/GNOME. Meta/⌘ is accepted too (some setups deliver
  # it), and Ctrl is accepted as an equivalent the probe (and users whose OS eats
  # Alt+Tab) can always drive. Reads the same bracket-access idiom as the snap
  # chord; an absent modifier property is nil (falsy), so a bare Tab never counts.
  def alttab_modifier?(e)
    e[:altKey] || e[:metaKey] || e[:ctrlKey] ? true : false
  end

  # Reserved strips for the work area a snapped window lives in. The bottom strip
  # is the tallest registered panel (the dock) so a snapped/maximized window's
  # body never hides under it; the top strip is the current chrome's titlebar
  # height so the snapped window's titlebar (which sits ABOVE its body) stays
  # fully on-screen.
  def snap_reserved_bottom
    h = 0
    @wm.panels.each { |p| h = p.h if p.h > h }
    h
  end
  def snap_reserved_top = Frame.current.title_h

  # Apply a Super+arrow snap to the focused window. Returns true when it acted on
  # a decorated window (so on_keydown can preventDefault + swallow the arrow), or
  # false/nil when there is nothing to snap (arrow then reaches the client). Down
  # restores a snapped window and is a genuine no-op on a free one — returning
  # false there lets the client keep the key.
  def handle_snap_key(key)
    win = @wm.focused
    return false unless win && win.decorated?
    sw = @width
    sh = @height
    rt = snap_reserved_top
    rb = snap_reserved_bottom
    case key
    when "ArrowLeft"  then @wm.snap_left(win, sw, sh, rt, rb)
    when "ArrowRight" then @wm.snap_right(win, sw, sh, rt, rb)
    when "ArrowUp"    then @wm.maximize(win, sw, sh, rt, rb)
    when "ArrowDown"  then return !@wm.restore_snap(win).nil?
    else return false
    end
    true
  end

  # The snap zone a move-drag pointer is currently over, or nil. Left edge → left
  # half, right edge → right half, top edge → maximize. Bottom edge is left free
  # (the dock lives there). Only decorated windows arm a preview.
  def edge_snap_zone(mx, my)
    return nil if mx <= SNAP_EDGE_MARGIN && my <= SNAP_EDGE_MARGIN # ignore the corner ambiguity
    return :left  if mx <= SNAP_EDGE_MARGIN
    return :right if mx >= @width - SNAP_EDGE_MARGIN
    return :max   if my <= SNAP_EDGE_MARGIN
    nil
  end

  # Commit the armed drag-to-edge snap to `win` (called from on_mouseup before
  # @drag/@snap_preview are cleared). No-op when nothing is armed.
  def commit_snap_preview(win)
    return nil unless win && @snap_preview
    sw = @width
    sh = @height
    rt = snap_reserved_top
    rb = snap_reserved_bottom
    case @snap_preview
    when :left  then @wm.snap_left(win, sw, sh, rt, rb)
    when :right then @wm.snap_right(win, sw, sh, rt, rb)
    when :max   then @wm.maximize(win, sw, sh, rt, rb)
    end
    nil
  end

  # Resolve a (mx, my) click against the currently-open menu (parent + an
  # optional open submenu). Returns a Hash:
  #   { in_submenu: bool, idx: int (or -1) }
  # When the open submenu is non-nil, its rectangle takes priority over the
  # parent's rectangle (a stale click in the parent under a submenu must hit
  # the submenu, not the parent).
  def menu_resolve(mx, my)
    state = @menu
    if state[:submenu]
      sub = state[:submenu]
      sx = state[:submenu_x]
      sy = state[:submenu_y]
      sidx = sub.hit_test(sx, sy, mx, my)
      return { in_submenu: true, idx: sidx } if sidx >= 0
      # Outside the submenu? Fall through to the parent.
    end
    pidx = state[:menu].hit_test(state[:x], state[:y], mx, my)
    { in_submenu: false, idx: pidx }
  end

  def handle_menu_click(mx, my)
    state = @menu
    res = menu_resolve(mx, my)
    if res[:idx] < 0
      # Clicked outside both menus: dismiss without firing anything.
      @menu = nil
      return
    end
    if res[:in_submenu]
      entry = state[:submenu].entries[res[:idx]]
      dispatch_menu_action(entry[:action])
      @menu = nil
      return
    end
    entry = state[:menu].entries[res[:idx]]
    if entry[:submenu]
      # Parent entry with a submenu: ensure the matching submenu is open. We
      # do NOT toggle on repeat click because the hover handler (on_mousemove)
      # already opens the submenu when the pointer arrives on a parent row
      # (sliding feel); a click that follows would otherwise close it. The
      # action is idempotent: clicking a parent always leaves its submenu
      # open, ready for the user to dive in.
      if state[:submenu_idx] != res[:idx]
        state[:submenu_idx] = res[:idx]
        state[:submenu] = entry[:submenu]
        state[:submenu_x] = state[:x] + Menu::WIDTH - 1
        state[:submenu_y] = state[:menu].entry_top(state[:y], res[:idx])
        state[:submenu_hover] = -1
      end
      return
    end
    dispatch_menu_action(entry[:action])
    @menu = nil
  end

  # Hover tracking: a mousemove inside the menu region highlights the entry
  # under the pointer (Openbox-style) — both parent and any open submenu. Used
  # only for the visual feedback; the actual action fires on click. Returns
  # truthy when the event was consumed (no further forwarding to clients).
  def handle_menu_hover(mx, my)
    state = @menu
    if state[:submenu]
      sub = state[:submenu]
      sidx = sub.hit_test(state[:submenu_x], state[:submenu_y], mx, my)
      state[:submenu_hover] = sidx
      if sidx >= 0
        state[:hover] = state[:submenu_idx]
        return true
      end
    end
    pidx = state[:menu].hit_test(state[:x], state[:y], mx, my)
    state[:hover] = pidx
    # Sliding the pointer onto a different parent submenu opener swaps the
    # open submenu so the menu feels live (matches every desktop env's UX).
    if pidx >= 0
      entry = state[:menu].entries[pidx]
      if entry[:submenu] && state[:submenu_idx] != pidx
        state[:submenu_idx] = pidx
        state[:submenu] = entry[:submenu]
        state[:submenu_x] = state[:x] + Menu::WIDTH - 1
        state[:submenu_y] = state[:menu].entry_top(state[:y], pidx)
        state[:submenu_hover] = -1
      end
    end
    pidx >= 0 || state[:submenu] != nil
  end

  # Dispatch a [tag, arg] action tuple. Menu entries carry their effect as
  # plain data (no Proc — Procs are awkward to reach from rbtest, and the
  # WM is the source of truth for what an action actually does). The
  # Compositor maps the tuple back onto its JS-touching helpers.
  def dispatch_menu_action(act)
    return nil unless act.is_a?(Array)
    tag = act[0]
    arg = act[1]
    case tag
    when :launch
      do_launch(arg.to_s)
    when :workspace
      changed = @wm.set_workspace(arg.to_i)
      if changed
        notify_workspace_changed
        notify_windows_changed
      end
    when :theme
      # Root-menu Theme entry clicked. Same effect as a `set_theme` wire
      # message from the dock: switch the active theme + broadcast the
      # `theme_changed` event so every panel repaints. An unknown name or
      # the already-active name is dropped (set_theme returns nil).
      changed = @wm.set_theme(arg.to_s)
      notify_theme_changed if changed
    when :frame
      # Root-menu Frame entry clicked. Swap Frame.current + repaint —
      # every window's *_rect delegates to Frame.current so the new
      # decoration appears on the next rAF tick (no client cooperation
      # needed; Frame is purely compositor-side). Unknown names fall
      # back to OpenboxFrame via FrameRegistry.select. A URL reload is
      # NOT needed — this is a pure Ruby swap.
      name = arg.to_s
      if FrameRegistry.names.include?(name) && name != Frame.current_name
        FrameRegistry.select(name)
      end
    when :wallpaper
      # Root-menu Wallpaper entry clicked. Swap the desktop-background preset
      # (grid / a gradient / the bundled image) via Wallpaper.select. The change
      # is picked up by draw_desktop_widgets on the next rAF tick — its
      # render-once cache keys on Wallpaper.current_name so exactly one
      # re-render happens (the Firefox-GC lesson) — and persisted to
      # localStorage so it survives a reload. Unknown or already-active names
      # are dropped (Wallpaper.select returns nil), skipping the persist.
      changed = Wallpaper.select(arg.to_s)
      save_wallpaper if changed
    when :focus
      win = @wm.find(arg.to_i)
      if win
        @wm.focus(win)
        notify_windows_changed
      end
    when :close
      win = @wm.find(arg.to_i)
      if win
        @wm.close(win)
        notify_closed(win, "user")
        notify_windows_changed
      end
    when :applet
      # Root-menu Applets entry clicked: toggle that desktop tile on/off and
      # persist the new shown-set (14_applets.rb). No-op when APPLETS is off.
      toggle_applet(arg.to_s)
    when :noop
      # About / Reload / Exit are dismiss-only in v0: the click closes the
      # menu (already done by the caller setting @menu = nil) and does
      # nothing else. arg names which entry was clicked, for log
      # introspection from Playwright.
      nil
    end
  end

  # Apply a validated launch by app id: route the LAUNCHABLE descriptor to
  # the matching JS spawner, exactly mirroring the :launch arm of
  # route_worker_message. Unknown ids are dropped (the menu's Applications
  # submenu only lists wm.launchable? ids, so this is defence in depth).
  def do_launch(app)
    return nil unless @wm.launchable?(app)
    url = @wm.launchable_url(app)
    if !url.nil?
      spawn_external(url)
      return :launched
    end
    ref = @wm.launchable_oci(app)
    spawn_external_oci(ref) unless ref.nil?
    :launched
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

    # Age + expire the desktop-notification toast stack (12_notifications.rb).
    # Self-gates on Compositor::NOTIFICATIONS; captures @now for post expiry.
    tick_notifications(t)

    # Persist the layout whenever it actually changed (a move, resize, spawn,
    # close or restack), not every frame.
    sig = @wm.layout_signature
    if sig != @last_layout_sig
      @last_layout_sig = sig
      save_layout
    end
  end

  # Opt the whole widgets-painted chrome into anti-aliased, shaped OpenType
  # text — window titles, the menu, the HUD and the desktop backdrop labels all
  # repaint against the toolkit's bundled Atkinson Hyperlegible vector face
  # instead of the compiled-in 5x7 bitmap. Window titles are the most visible
  # bitmap->AA win.
  #
  # The toolkit's active font is a process-global, so this flips it exactly
  # once, on the first frame — before any widgets paint path runs (draw_desktop
  # is the first) — and only when at least one widgets surface is enabled, so
  # the opt-in is gated consistently with the per-surface *_WIDGETS flags. If
  # every widgets path is off, the bitmap default is left untouched.
  def enable_opentype_text_once
    return if @opentype_text
    @opentype_text = true
    return unless MENU_WIDGETS || HUD_WIDGETS || DESKTOP_WIDGETS || FRAME_WIDGETS
    require "widgets"
    Widgets.use_opentype_text
  end

  # The per-rAF entry point. Runs every animation frame but composites only when
  # compute_damage reports a change, and then repaints only the changed REGIONS
  # rather than the whole canvas:
  #   - empty damage → idle → skip the whole scene walk (the #88 gate: no window
  #     commit, no live-applet tick, no input → idle CPU approaches zero instead
  #     of re-compositing a static scene ~60×/sec, the Firefox 100%-CPU
  #     regression the DE overlays surfaced).
  #   - full  damage → whole-screen recomposite (first frame, viewport resize,
  #     frame/palette swap, workspace switch, an open menu / full-screen modal).
  #   - region damage → repaint only the union of the changed rectangles
  #     (draw the desktop + intersecting windows clipped to each rect; retain
  #     everything else from the prior frame), then relay the top overlays.
  # The focused-rect probe publish runs every frame regardless (one cheap JS
  # call) so the snapping probe reads a fresh value even on skipped frames.
  def render
    enable_opentype_text_once
    # Re-anchor every panel to the bottom-center of the current canvas BEFORE we
    # diff, so a re-anchor (e.g. after a viewport resize) shows up as a geometry
    # change in compute_damage rather than being missed.
    @wm.panels.each { |p| @wm.anchor_panel(p, @width, @height) }

    compute_damage
    unless @damage.empty?
      if @damage.full?
        paint_full
      else
        paint_regions(@damage)
      end
      @rendered_frames += 1
    end
    # This frame is now on the canvas (or was intentionally skipped): clear the
    # belt and promote the working snapshot so the NEXT compute_damage diffs
    # against exactly what is on screen. force_paint can only be set here when
    # damage was non-empty (compute_damage escalates a stray belt to full), so
    # clearing it unconditionally never drops a pending change.
    @force_paint = false
    snapshot_scene

    # Publish the focused window's live geometry + the work-area metrics for the
    # snapping probe (test/probe-snapping.mjs reads globalThis.__wasmboxFocusedRect).
    # One cheap JS call per frame, self-gated on SNAPPING so main is unaffected,
    # and OUTSIDE the paint gate so the probe reads a fresh value on idle frames.
    publish_snap_geometry if SNAPPING
  end

  # Diff the live scene against the last composited frame and populate @damage.
  # A global change (or the first frame / a modal / the bench baseline seam)
  # forces a full recomposite; otherwise we accumulate one rectangle per changed,
  # appeared or vanished window, one per external surface that committed new
  # pixels, one per ticking desktop applet, plus the tray / notification / HUD
  # bands when those changed — then the belt catch-all promotes anything we could
  # not localise to a full recomposite.
  def compute_damage
    @damage.clear
    g = current_globals
    overlay = overlay_active?
    # Full recomposite when the bench baseline seam is set, on the first frame,
    # on any global change (frame/palette, active workspace, canvas size), or
    # when a full-screen overlay/menu is up now OR was up last frame (so the
    # frame it closes on also erases it).
    force_full = @bench_no_gate || @prev_globals.nil? || g != @prev_globals ||
                 overlay || @prev_overlay_active
    # A desktop-applet set/position change (add / remove / drag) is rare and
    # reshuffles the board, so a full recomposite is simplest and erases any
    # vacated tile footprint.
    force_full = true if APPLETS && applet_layout_signature != @prev_applet_layout

    cur = {}
    seqs = {}
    wins = visible_windows
    wins.each do |win|
      b  = window_bounds(win)
      vk = window_vkey(win)
      sq = window_live_seq(win)
      cur[win.id]  = { vk: vk, b: b }
      seqs[win.id] = sq
      next if force_full
      prev = @prev_wins[win.id]
      if prev.nil?
        @damage.add_rect(b)                      # newly appeared
      elsif prev[:vk] != vk
        u = DamageSet.union(prev[:b], b)         # moved / resized / focus / shade / retitle
        @damage.add(u[:x], u[:y], u[:w], u[:h])
      end
      # New client pixels (a commit) damage the surface even when its geometry is
      # unchanged. Over-approximated to the whole window bounds rather than
      # mapping the sub-rect through the resize scale — simpler, and still far
      # less than a full-screen repaint whenever other windows exist.
      prev_sq = @prev_seqs[win.id].nil? ? 0 : @prev_seqs[win.id]
      if win.external? && (!win.clipped_damage.nil? || sq != prev_sq)
        @damage.add_rect(b)
      end
    end
    @cur_wins = cur
    @cur_seqs = seqs

    if force_full
      @damage.full!
      return nil
    end

    # A window present last frame but gone now leaves a hole to repaint.
    @prev_wins.each do |id, prev|
      @damage.add_rect(prev[:b]) unless cur.key?(id)
    end

    # Desktop-applet content ticks (a clock second, a monitor step, a calendar
    # nav) repaint just the affected tiles — same set + positions, exact bounds.
    damage_applets
    # Tray strip + notification column are top-stratum overlays; damage their
    # bands when their content changed so the region loop repaints the desktop +
    # windows beneath, and the post-loop redraw lands them back on top.
    damage_tray
    damage_notifications

    # Belt catch-all: an input/message raised force_paint but nothing above could
    # localise it (a wallpaper/theme swap, a hover forward, …) — repaint fully so
    # a change we cannot pin to a region is never dropped.
    if @force_paint && @damage.empty?
      @damage.full!
      return nil
    end
    # NOTE: the debug HUD (fps + composited-frame counter) is a RETAINED top
    # overlay, like the tray + toasts — its pixels persist on the canvas, so it
    # is NOT damaged here every frame (that would tax every localized change with
    # a whole extra region). paint_regions relays it only when another damage
    # rect repainted the band beneath it; its counter therefore holds steady
    # during localized activity that never touches the bottom-left band, which is
    # the honest picture of the work actually done.
    nil
  end

  # Promote the working snapshot to the retained one, after a composite (or an
  # intentionally-skipped idle frame). The next compute_damage diffs against it.
  def snapshot_scene
    @prev_wins = @cur_wins
    @prev_seqs = @cur_seqs
    @prev_globals = current_globals
    @prev_overlay_active = overlay_active?
    @prev_applet_sig = applet_signature
    @prev_applet_layout = applet_layout_signature
    @prev_tray_sig = tray_signature
    @prev_notify_sig = notification_signature
    @prev_notify_count = NOTIFICATIONS ? notifications.length : 0
    nil
  end

  # The bottom-to-top list of windows that actually render: not minimized, and
  # either a panel (always visible) or on the active workspace.
  def visible_windows
    active = @wm.active_workspace
    out = []
    @wm.ordered_windows.each do |win|
      next if win.minimized?
      next if !win.panel? && win.workspace != active
      out << win
    end
    out
  end

  # Screen-space bounding box [x, y, w, h] a window paints into: the padded
  # chrome extent for a decorated window, else the bare body rect.
  def window_bounds(win)
    win.decorated? ? Frame.sprite_bounds(win) : win.body_rect
  end

  # A window's visual signature: every input that changes its on-screen pixels
  # OR its position. Equal signatures across two frames mean that window need not
  # be repainted (and its cached chrome buffer still applies).
  def window_vkey(win)
    "#{win.x}:#{win.y}:#{win.w}:#{win.h}:#{win.focused? ? 1 : 0}:#{win.shaded? ? 1 : 0}:#{win.title}"
  end

  # The live committed-content seq of one external window (0 for in-process or
  # seqlock-less surfaces), read straight from the SAB control word so a client
  # that painted new pixels is observed BEFORE the blit.
  def window_live_seq(win)
    return 0 unless win.external?
    slot = win.image_data
    slot.nil? ? 0 : JS.global.call("wasmboxWindowLiveSeq", slot).to_i
  end

  # Everything that, when it changes, is cheapest to handle with a full
  # recomposite: the active frame/palette, the active workspace and the canvas
  # size.
  def current_globals
    [Frame.current_name, @wm.active_workspace, @width, @height]
  end

  # A full-screen overlay / menu is up: repaint fully while it is (and the frame
  # it closes on). Menus, the drag-to-edge snap preview and the Alt-Tab /
  # Spotlight / Exposé modals all dim or draw over large areas, so a region walk
  # would be more code for no win.
  def overlay_active?
    return true if @menu
    return true if SNAPPING && @snap_preview
    return true if ALTTAB && alttab_active?
    return true if SPOTLIGHT && spotlight_active?
    return true if EXPOSE && expose_active?
    false
  end

  # Small reserved band at the bottom-left for the HUD text (fps + frame no.),
  # matching draw_hud_widgets' placement + extent.
  def hud_rect
    [0, @height - 24, [560, @width].min, 24]
  end

  # Damage each desktop applet tile whose content signature ticked (clock
  # second / monitor step / calendar nav). Same set + positions as last frame
  # (a set/position change already forced a full recomposite), so the tile
  # bounds are exact and nothing was vacated.
  def damage_applets
    return nil unless APPLETS
    b = applets
    return nil if b.empty?
    return nil if applet_signature == @prev_applet_sig
    b.items.each { |a| @damage.add_rect([a.x, a.y, a.w, a.h]) }
    nil
  end

  # Damage the tray strip band when its content signature changed (an add,
  # remove or a live glyph/tooltip update). A generous full-width top band — the
  # strip's left edge depends on the item count, and covering the band handles
  # any add/remove/update extent without tracking the previous layout.
  def damage_tray
    return nil unless TRAY
    return nil if tray_signature == @prev_tray_sig
    @damage.add_rect(tray_bounds)
    nil
  end

  # Damage the notification column when the toast set changed (a post, an expiry
  # or the reflow an expiry triggers). The band covers max(prev, cur) toasts so
  # the vacated slot of an expired toast is repainted too.
  def damage_notifications
    return nil unless NOTIFICATIONS
    return nil if notification_signature == @prev_notify_sig
    @damage.add_rect(notifications_bounds)
    nil
  end

  # The combined content signature of every desktop applet ("" when none /
  # APPLETS off): a live applet forces a repaint exactly when its pixels must
  # change and no more often. Reuses applet_sig (14_applets.rb) — the same
  # signature the per-applet render cache keys on.
  def applet_signature
    return "" unless APPLETS
    b = applets
    return "" if b.empty?
    b.items.map { |a| applet_sig(a) }.join("|")
  end

  # The applet board's structural signature (kinds + positions). A change here
  # (an add / remove / drag) forces a full recomposite so a vacated tile is
  # erased; a pure content tick (same kinds + positions) does not.
  def applet_layout_signature
    return "" unless APPLETS
    b = applets
    return "" if b.empty?
    b.items.map { |a| "#{a.kind}@#{a.x},#{a.y}" }.join("|")
  end

  # The tray strip's content signature: id + glyph + tooltip + image-present per
  # item (TrayItem#sig), so an add/remove OR an in-place update moves it.
  def tray_signature
    return "" unless TRAY
    t = tray
    return "" if t.empty?
    t.items.map { |it| it.sig }.join("|")
  end

  # Full-width top band tall enough for the tray strip (its plate + icons).
  def tray_bounds
    [0, 0, @width, TrayArea::MARGIN + TrayArea::ICON + 2 * TRAY_PAD]
  end

  # The notification column's content signature: the toast ids (keys), which
  # change on every post / expiry / reflow (toasts are immutable once posted).
  def notification_signature
    return "" unless NOTIFICATIONS
    s = notifications
    return "" if s.empty?
    s.items.map { |n| n.key }.join("|")
  end

  # Screen band the notification column occupies, covering max(prev, cur) toasts
  # so an expiry's vacated slot is repainted along with the survivors' reflow.
  def notifications_bounds
    w   = NotificationStack::TOAST_W
    mg  = NotificationStack::TOAST_MARGIN
    gap = NotificationStack::TOAST_GAP
    th  = NotificationStack::TOAST_H
    n = notifications.length
    n = @prev_notify_count if @prev_notify_count > n
    n = 1 if n < 1
    [@width - w - mg, 0, w + mg, mg + n * (th + gap)]
  end

  # True when any accumulated damage rect overlaps the given [x,y,w,h] bounds.
  def damage_touches?(bounds)
    hit = false
    @damage.rects.each { |r| hit = true if DamageSet.rect_intersects?(r, bounds) }
    hit
  end

  # The whole-screen recomposite: desktop → applets → windows → snap preview →
  # menu → HUD → tray → notifications → modals. Used for the first frame,
  # viewport resizes, frame/palette swaps, workspace switches, whenever a menu /
  # full-screen modal is up, and whenever damage collapses to `full`. The
  # dirty-flag clear + scene snapshot live in #render, shared with the region
  # path, so this method only paints.
  def paint_full
    draw_desktop
    # Desktop applets (14_applets.rb): the BOTTOM interactive stratum — painted
    # right after the wallpaper and BEFORE the windows, so every window
    # composites OVER an applet. Self-gates on Compositor::APPLETS.
    draw_applets
    # Draw normal windows first, then panels on top (always-on-top stratum).
    # Minimized windows have been folded into the dock's iconbar; the
    # compositor skips them entirely so they leave no pixels on the canvas.
    # Windows on inactive workspaces are likewise skipped — only the active
    # workspace's windows render. Panels (the dock) IGNORE the workspace
    # filter and always render, because the dock is the UI that switches
    # workspaces in the first place.
    visible_windows.each { |win| draw_window(win) }
    # Drag-to-edge landing preview: a translucent rect of where the window will
    # snap, drawn OVER the windows (like a tiling hint) while a move-drag hovers
    # an edge. Self-gates on Compositor::SNAPPING + an armed @snap_preview.
    draw_snap_preview
    draw_menu if @menu
    draw_hud
    # The tray status strip + notification toasts are the always-on-top overlay
    # stratum: painted last so they sit above windows, panels, the menu and the
    # HUD. They share the top edge but never overlap (tray left of the toast
    # column — see 05_tray.rb). Self-gate on Compositor::TRAY / ::NOTIFICATIONS.
    draw_tray
    draw_notifications
    # Alt-Tab window switcher (15_alttab.rb): a centered strip of window
    # thumbnails painted OVER everything while the Alt+Tab chord is held. Drawn
    # last so it reads as a modal switcher above the windows, menu, tray + HUD.
    # Self-gates on Compositor::ALTTAB + an open switcher (else it only publishes
    # the "inactive" probe state).
    draw_alttab if ALTTAB
    # Spotlight launcher (16_spotlight.rb): a centered search palette painted OVER
    # everything while ⌘/Ctrl+Space is held. Drawn last (after the switcher) so it
    # reads as the top-most modal. Self-gates on Compositor::SPOTLIGHT + an open
    # palette (else it only publishes the "inactive" probe state).
    draw_spotlight if SPOTLIGHT
    # Exposé / "show all windows" (17_expose.rb): a dimmed desktop under a grid of
    # window thumbnails, painted OVER everything while the spread is up (F3).
    # Drawn last so it reads as the top-most modal. Self-gates on Compositor::EXPOSE
    # + an open spread (else it only publishes the "inactive" probe state).
    draw_expose if EXPOSE
    nil
  end

  # The region recomposite: repaint only the damaged rectangles. Each rect is
  # clipped so the draw calls for a partially-damaged window touch only the
  # in-region pixels; everything outside every rect is retained from the prior
  # frame (the OffscreenCanvas is persistent, never cleared). Within a rect we
  # repaint the desktop background, then the applets, then every window whose
  # screen bounds intersect it, in bottom-to-top stacking order, so overlaps
  # stay correct. The always-on-top overlays (tray, notifications, HUD) sit ABOVE
  # the windows, so the per-rect loop just painted desktop/window pixels over
  # them — we relay each one after the loop when any damage rect touched it.
  # Menus, the snap preview and the Alt-Tab / Spotlight / Exposé modals never
  # reach this path (overlay_active? forced a full recomposite).
  def paint_regions(dmg)
    wins = visible_windows
    dmg.rects.each do |r|
      @ctx.call("save")
      @ctx.call("beginPath")
      @ctx.call("rect", r[:x], r[:y], r[:w], r[:h])
      @ctx.call("clip")
      draw_desktop_region(r)
      draw_applets_region(r)
      wins.each do |win|
        next unless DamageSet.rect_intersects?(r, window_bounds(win))
        draw_window(win)
      end
      @ctx.call("restore")
    end
    # Top overlays (tray strip, toast column, debug HUD) sit ABOVE the windows
    # and their pixels persist on the retained canvas, so the per-rect loop just
    # painted desktop/window pixels over any part a damage rect crossed. Relay
    # each one — unclipped, at its fixed screen position, where the damage
    # actually touched it: a change that never reaches a band leaves that overlay
    # untouched (and unpaid-for).
    draw_tray if TRAY && damage_touches?(tray_bounds)
    draw_notifications if NOTIFICATIONS && damage_touches?(notifications_bounds)
    draw_hud if damage_touches?(hud_rect)
    nil
  end

  # Repaint the desktop background inside one damage rect. With DESKTOP_WIDGETS
  # the backdrop is a cached full-canvas RGBA buffer presented via putImageData,
  # which IGNORES the ctx clip — so we re-present only the rect via the
  # dirty-rect blit helper (the first frame is always a full composite, so the
  # "desktop" buffer is populated before any region frame). The hand-drawn
  # fallback uses fill + grid strokes, which DO respect the clip, so only the
  # grid lines crossing the rect are stroked.
  def draw_desktop_region(r)
    if DESKTOP_WIDGETS
      JS.global.call("wasmboxBlitRGBARegion", @ctx, @width, @height, 0, 0,
                     "desktop", r[:x], r[:y], r[:w], r[:h])
      return nil
    end
    fill_rect([r[:x], r[:y], r[:w], r[:h]], Theme::DESKTOP)
    @ctx.set("strokeStyle", Theme::DESKTOP_GRID)
    @ctx.set("lineWidth", 1)
    step = 40
    x0 = r[:x]; x1 = r[:x] + r[:w]
    y0 = r[:y]; y1 = r[:y] + r[:h]
    gx = (x0 / step) * step
    while gx <= x1
      @ctx.call("beginPath")
      @ctx.call("moveTo", gx + 0.5, y0)
      @ctx.call("lineTo", gx + 0.5, y1)
      @ctx.call("stroke")
      gx += step
    end
    gy = (y0 / step) * step
    while gy <= y1
      @ctx.call("beginPath")
      @ctx.call("moveTo", x0, gy + 0.5)
      @ctx.call("lineTo", x1, gy + 0.5)
      @ctx.call("stroke")
      gy += step
    end
    nil
  end

  # Repaint the desktop-applet tiles that intersect one damage rect (clipped by
  # the caller). Applets are the desktop stratum, below the windows, so they are
  # painted between the background and the window loop. No-op when the flag is
  # off or nothing is shown.
  def draw_applets_region(r)
    return nil unless APPLETS
    b = applets
    return nil if b.empty?
    b.items.each do |a|
      draw_applet_tile(a) if DamageSet.rect_intersects?(r, [a.x, a.y, a.w, a.h])
    end
    nil
  end

  # Snap landing preview. Draws the frame extent (body + titlebar headroom) of
  # the armed zone as a filled translucent panel with a hairline border, so the
  # user sees exactly where the window lands before releasing. No-op when nothing
  # is armed. The preview rect mirrors WindowManager#snap_rect but expands the
  # top by the reserved titlebar strip so the hint covers the whole future frame.
  def draw_snap_preview
    return nil unless SNAPPING && @snap_preview
    rt = snap_reserved_top
    rb = snap_reserved_bottom
    body = @wm.snap_rect(@snap_preview, @width, @height, rt, rb)
    return nil unless body
    frame = [body[0], body[1] - rt, body[2], body[3] + rt]
    fill_rect(frame, Theme::SNAP_PREVIEW_FILL)
    stroke_rect(frame, Theme::SNAP_PREVIEW_EDGE, 2)
    nil
  end

  # Push the focused window rect + work-area metrics to the JS test hook. Sends a
  # sentinel (-1,-1,0,0) when nothing is focused. The snap_state ("left"/"right"/
  # "max"/"") and the armed drag preview ride along so the probe can assert both
  # the keyboard and the drag path deterministically without reading pixels.
  def publish_snap_geometry
    win = @wm.focused
    rt = snap_reserved_top
    rb = snap_reserved_bottom
    prev = @snap_preview.nil? ? "" : @snap_preview.to_s
    if win.nil? || !win.decorated?
      JS.global.call("wasmboxPublishFocusedRect", -1, -1, 0, 0, "", @width, @height, rt, rb, prev)
      return nil
    end
    state = win.snap_state.nil? ? "" : win.snap_state.to_s
    JS.global.call("wasmboxPublishFocusedRect",
      win.x, win.y, win.w, win.h, state, @width, @height, rt, rb, prev)
    nil
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
    # When Compositor::DESKTOP_WIDGETS is on (see 10_desktop_widgets.rb), paint
    # the desktop background (fill + grid) through the go-widgets binding
    # (Widgets.backdrop -> render -> RGBA -> putImageData), rendered ONCE and
    # cached (re-rendered only on a resize / palette change), instead of the
    # raw-ctx fill + per-frame grid-stroke loop below. The flag keeps this
    # hand-drawn path as the shippable fallback during the live co-edit.
    if DESKTOP_WIDGETS
      draw_desktop_widgets
      return
    end
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
    # An undecorated surface (a panel like the dock, or a popup like a menu)
    # has no titlebar, close box, resize grip or frame border: its surface IS
    # the window, so we blit the SAB straight at the body rectangle. The blit
    # composites (source-over, see wasmboxBlitFromSAB), so transparent corners
    # show the desktop through instead of a black rectangle.
    unless win.decorated?
      blit_external(win) if win.external?
      return
    end

    active = win.focused?
    chrome = Frame.current

    # WIDGETS paint path (2026-08-02): when Compositor::FRAME_WIDGETS is on (see
    # 11_frame_widgets.rb), paint the whole decoration (titlebar + buttons +
    # border + shadow + grip) through the go-widgets binding into one per-window
    # buffer and composite it OVER the body (source-over), instead of the
    # hand-drawn chrome.paint / chrome.paint_frame below. The body is painted
    # FIRST so the decoration's transparent body hole lets it show through and
    # the border + grip land on top of its edges. Hit-testing (Window#*_rect) is
    # unchanged, so the window behaves identically. The flag keeps the
    # hand-drawn path below as the shippable fallback during the live co-edit.
    if FRAME_WIDGETS
      paint_window_body(win) unless win.shaded?
      draw_window_frame_widgets(win, active)
      return
    end

    # Chrome-owned titlebar + buttons. The current chrome handles all paint
    # ops for the bar (background, hairline, title text, close/min/max
    # glyphs). Both Openbox and Aqua chromes implement this method.
    chrome.paint(@ctx, win, active, self)

    # Shaded ("rolled up"): only the titlebar + its buttons are drawn — no body,
    # no resize grip, no border. The body area shows whatever sits behind the
    # window.
    return if win.shaded?

    paint_window_body(win)

    # Chrome-owned frame chrome: resize grip + 1px border (Openbox), plus
    # the faked 1px drop shadow + slightly heavier border on Aqua. Painted
    # AFTER the body so it lands on top.
    chrome.paint_frame(@ctx, win, active, self)
  end

  # Paint a decorated window's client body. In-process windows paint a solid
  # fill; external windows blit their SharedArrayBuffer through a cached
  # ImageData view; dom windows do NOT paint a body -- the body is a real
  # <iframe> the main thread overlays on the canvas at the body-rect coords, and
  # we just republish those coords every frame so the iframe tracks the WM.
  # Shared by the hand-drawn + widgets frame paths.
  def paint_window_body(win)
    if win.dom?
      bx, by, bw, bh = win.body_rect
      JS.global.call("wasmboxIframeMove", win.id, bx, by, bw, bh)
    elsif win.external?
      blit_external(win)
    else
      fill_rect(win.body_rect, win.fill)
    end
  end

  # Blit an external window's SharedArrayBuffer onto the canvas. Chrome forbids
  # constructing an ImageData over a SAB-backed Uint8ClampedArray, so the JS
  # helper wasmboxBlitFromSAB() owns a non-shared ImageData and copies the
  # damage rect out of the SAB into it before putImageData.
  #
  # SCALE-FIT: when the user resizes a window the SAB stays at its native size
  # (the client allocated it once and the protocol has no resize handshake).
  # If win.w/win.h differ from native_w/native_h we ask the helper for a
  # scaled present -- it draws the WHOLE native surface stretched into the
  # window rect, so e.g. Quake's 320x240 framebuffer fills a 800x600 window.
  def blit_external(win)
    return nil unless win.image_data
    d = win.clipped_damage || { x: 0, y: 0, w: win.native_w, h: win.native_h }
    if win.w == win.native_w && win.h == win.native_h
      # Native-size present: copy only the damaged rect (cheaper, and we can
      # restrict the drawImage to that rect since it lines up 1:1).
      JS.global.call("wasmboxBlitFromSAB", @ctx, win.image_data,
                     win.x, win.y, d[:x], d[:y], d[:w], d[:h])
    else
      # Resized present: any damage means the visible scaled image must
      # refresh in its entirety -- a damaged pixel in source maps to a
      # damaged-RECT in destination once scale != 1, and computing the exact
      # destination damage band is fiddlier than just redrawing. The helper
      # still does the seqlock-safe copy of the damage out of the SAB so we
      # never sample mid-paint bytes.
      JS.global.call("wasmboxBlitFromSABScaled", @ctx, win.image_data,
                     d[:x], d[:y], d[:w], d[:h],
                     win.x, win.y, win.w, win.h)
    end
    win.clear_damage
  end

  # Draw the open menu (and its open submenu, if any) onto the canvas. Both
  # use the Theme::MENU_* palette: MENU_BG fill, MENU_BORDER 1-px frame,
  # MENU_TEXT label ink, and MENU_HILITE band for the hovered row.
  def draw_menu
    # PILOT (2026-08-02): when Compositor::MENU_WIDGETS is on (see
    # 08_menu_widgets.rb), paint the menu through the go-widgets `require
    # "widgets"` binding (render -> RGBA -> putImageData) instead of
    # hand-drawing it on the canvas below. Only the PAINT changes — the Ruby
    # hit-testing (menu_resolve / handle_menu_click / handle_menu_hover) is
    # untouched, so the menu stays clickable exactly as before. The flag keeps
    # the hand-drawn draw_menu_panel path below as the shippable fallback.
    if MENU_WIDGETS
      draw_menu_widgets
      return
    end
    state = @menu
    draw_menu_panel(state[:menu], state[:x], state[:y], state[:hover],
                    state[:submenu_idx])
    if state[:submenu]
      draw_menu_panel(state[:submenu], state[:submenu_x], state[:submenu_y],
                      state[:submenu_hover], -1)
    end
  end

  # Render a single menu panel at (x, y) with `hover` highlighted (-1 = no
  # highlight) and `open_sub_idx` showing the entry whose submenu is open
  # (drawn highlighted too, so the user can read the parent path at a glance).
  def draw_menu_panel(menu, x, y, hover, open_sub_idx)
    h = menu.height
    fill_rect([x, y, Menu::WIDTH, h], Theme::MENU_BG)
    stroke_rect([x, y, Menu::WIDTH, h], Theme::MENU_BORDER, 1)
    menu.each_row(y) do |entry, row_y, row_h, i|
      if entry[:separator]
        # A thin divider centered vertically in its SEP_H band — same colour
        # as the frame so it reads as a hairline. We inset by Menu::PAD_X on
        # each side so the line never touches the menu's vertical border.
        mid = row_y + row_h / 2
        @ctx.set("strokeStyle", Theme::MENU_BORDER)
        @ctx.set("lineWidth", 1)
        @ctx.call("beginPath")
        @ctx.call("moveTo", x + Menu::PAD_X,                 mid + 0.5)
        @ctx.call("lineTo", x + Menu::WIDTH - Menu::PAD_X,   mid + 0.5)
        @ctx.call("stroke")
        next
      end
      # Highlight the hovered row OR the row whose submenu is currently open.
      if i == hover || i == open_sub_idx
        fill_rect([x + 1, row_y, Menu::WIDTH - 2, row_h], Theme::MENU_HILITE)
      end
      text(entry[:label].to_s, x + Menu::PAD_X, row_y + row_h - 8, Theme::MENU_TEXT)
      if entry[:submenu]
        # Right-aligned chevron — render with the same MENU_TEXT colour as
        # the label so it reads cleanly on both the bg and the hilite band.
        text(">", x + Menu::WIDTH - Menu::PAD_X - 6, row_y + row_h - 8, Theme::MENU_TEXT)
      end
    end
  end

  def draw_hud
    # When Compositor::HUD_WIDGETS is on (see 09_hud_widgets.rb), paint the HUD
    # through the go-widgets binding (render -> RGBA -> alpha-composited blit)
    # instead of the raw-ctx fillText below. The flag keeps this hand-drawn path
    # as the shippable fallback during the live co-edit.
    if HUD_WIDGETS
      draw_hud_widgets
      return
    end
    n = @wm.windows.length
    # @rendered_frames counts COMPOSITED frames (not every rAF tick): with the
    # dirty-rect gate an idle desktop stops compositing, so the counter — and the
    # fps reading — hold steady, the honest picture of the work actually done.
    line = "rbgo compositor — #{n} window#{n == 1 ? '' : 's'} — #{'%.0f' % @fps} fps — frame #{@rendered_frames}"
    text(line, 10, @height - 12, Theme::HUD_TEXT, 12)
  end
end

# ---------------------------------------------------------------------------
# Boot: pick the window-decoration chrome, build the WM, spawn a few clients,
# attach to the canvas and run.
# ---------------------------------------------------------------------------
# Chrome picker: compositor.worker.js stashes the requested chrome name on
# `self.WASMBOX_FRAME` BEFORE the Ruby program boots (read from a URL
