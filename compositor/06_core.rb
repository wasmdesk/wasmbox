class Compositor
  def initialize(wm)
    @wm = wm
    @drag = nil      # {win:, mode: :move|:resize, dx:, dy:}
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

    # --- dirty-rectangle compositing state ---------------------------------
    # @damage accumulates the regions that changed since the last composited
    # frame; compute_damage rebuilds it each frame by diffing the live scene
    # against @prev_wins / @prev_globals (the retained snapshot of what we last
    # drew). render then skips entirely (idle) or repaints only those regions.
    @damage = DamageSet.new
    @prev_wins = {}       # id => { vk: <visual signature>, b: <screen bounds> }
    @prev_globals = nil   # [frame_name, workspace, width, height, menu_open?]
    @cur_wins = {}        # working snapshot built during compute_damage
    @rendered_frames = 0  # frames we actually composited (HUD counter)
    # @bench_no_gate / @bench_no_sprite are optional bench seams: cmd/rbbench
    # sets them via instance_variable_set to A/B the old (full redraw every
    # frame / inline chrome paint) vs new (dirty-gate / cached sprite) paths in
    # one binary. They are nil (falsy) in the shipped compositor, so production
    # never touches the legacy paths.
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
    @bus.on("wasmbox-spawn-dom-window") do |e|
      detail = e.get("detail")
      url = detail.get("url").to_s
      w = detail.get("w").to_i
      h = detail.get("h").to_i
      title = detail.get("title").to_s
      spawn_dom_window(url, w, h, title)
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
    %w[window_id w h stride title role app index workspace name parent rel_x rel_y lock_aspect ratio].each do |k|
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
  end

  def install_input
    @canvas.on("mousedown")   { |e| on_mousedown(e) }
    @canvas.on("mousemove")   { |e| on_mousemove(e) }
    @canvas.on("mouseup")     { |e| on_mouseup(e) }
    @canvas.on("contextmenu") { |e| on_contextmenu(e) }
    @canvas.on("wheel")       { |e| on_wheel(e) }
    JS.window.on("keydown")   { |e| on_keydown(e) }
    JS.window.on("keyup")     { |e| on_keyup(e) }
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
    @drag = nil
    win = @wm.focused
    return nil unless win&.external?
    mx = e.get("offsetX")
    my = e.get("offsetY")
    return nil unless win.hit?(win.body_rect, mx, my)
    forward_mouse_to_client(win, "mouseup", mx, my, e)
  end

  def on_keyup(e)
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
      return # empty desktop, left button: nothing (right button = menu)
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
      menu = RootMenu.build(@wm)
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
    case key
    when "Tab"
      # Shift+Tab cycles windows (Openbox/Alt-Tab equivalent). Plain Tab
      # forwards to the focused window so the terminal can use it for
      # autocompletion (and any text-editor client can use it for indent).
      if e[:shiftKey]
        e.call("preventDefault")
        @wm.cycle
        notify_windows_changed
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

    # Persist the layout whenever it actually changed (a move, resize, spawn,
    # close or restack), not every frame.
    sig = @wm.layout_signature
    if sig != @last_layout_sig
      @last_layout_sig = sig
      save_layout
    end
  end

  def render
    # Re-anchor every panel to the bottom-center of the current canvas BEFORE
    # we diff, so a re-anchor (e.g. after a viewport resize) shows up as a
    # geometry change in compute_damage rather than being missed.
    @wm.panels.each { |p| @wm.anchor_panel(p, @width, @height) }

    compute_damage

    # Idle fast-path: nothing changed since the last composited frame, so we
    # skip the whole scene walk. This is the dirty-rectangle gate at its
    # coarsest — zero damage, zero work (beyond the cheap diff above). It is
    # the Ruby-side complement of the JS-side commit-seq gate: that one avoids
    # re-copying an unchanged surface; this one avoids re-walking an unchanged
    # desktop.
    return if !@bench_no_gate && @damage.empty?

    if @bench_no_gate || @damage.full?
      draw_full
    else
      draw_regions(@damage)
    end

    @rendered_frames += 1
    snapshot_scene
  end

  # Whole-screen recomposite: the original render path. Used for the first
  # frame, viewport resizes, frame/palette swaps, workspace switches, whenever
  # a menu is open, and whenever damage collapses to `full`.
  def draw_full
    draw_desktop
    # Draw normal windows first, then panels on top (always-on-top stratum).
    # Minimized windows have been folded into the dock's iconbar; the
    # compositor skips them entirely so they leave no pixels on the canvas.
    # Windows on inactive workspaces are likewise skipped — only the active
    # workspace's windows render. Panels (the dock) IGNORE the workspace
    # filter and always render, because the dock is the UI that switches
    # workspaces in the first place.
    visible_windows.each { |win| draw_window(win) }
    draw_menu if @menu
    draw_hud
  end

  # Region recomposite: repaint only the damaged rectangles. Each rect is
  # clipped so the draw calls for a partially-damaged window touch only the
  # in-region pixels; everything outside every rect is retained from the prior
  # frame (the OffscreenCanvas is persistent, never cleared). Within a rect we
  # redraw the desktop background then every window whose screen bounds
  # intersect it, in bottom-to-top stacking order, so overlaps stay correct.
  def draw_regions(dmg)
    wins = visible_windows
    dmg.rects.each do |r|
      @ctx.call("save")
      @ctx.call("beginPath")
      @ctx.call("rect", r[:x], r[:y], r[:w], r[:h])
      @ctx.call("clip")
      draw_desktop_region(r)
      wins.each do |win|
        next unless DamageSet.rect_intersects?(r, window_bounds(win))
        draw_window(win)
      end
      @ctx.call("restore")
    end
    # The HUD is an always-on-top overlay whose fps/frame text changes every
    # composited frame. compute_damage appends its rect to the damage set on
    # every rendered frame, so the loop above already refreshed the background
    # (and any window) beneath it; paint the text last, unclipped.
    draw_hud
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
  # chrome sprite bounds for a decorated window, else the bare body rect.
  def window_bounds(win)
    win.decorated? ? Frame.sprite_bounds(win) : win.body_rect
  end

  # Everything that, when it changes, is cheapest to handle with a full
  # recomposite: the active frame/palette, the active workspace, the canvas
  # size and whether a menu is currently open.
  def current_globals
    [Frame.current_name, @wm.active_workspace, @width, @height, !@menu.nil?]
  end

  # A window's visual signature: every input that changes its on-screen pixels
  # OR its position. Two frames with equal signatures for a window mean that
  # window need not be repainted (and its cached chrome sprite still applies).
  def window_vkey(win)
    "#{win.x}:#{win.y}:#{win.w}:#{win.h}:#{win.focused? ? 1 : 0}:#{win.shaded? ? 1 : 0}:#{win.title}"
  end

  # Small reserved band at the bottom-left for the HUD text (fps + frame no.).
  def hud_rect
    [0, @height - 24, [540, @width].min, 24]
  end

  # Diff the live scene against the last composited frame and populate @damage.
  # Global changes (or the first frame) force a full recomposite; otherwise we
  # accumulate one rectangle per changed / appeared / vanished window plus one
  # per external surface that committed new pixels, then the HUD band.
  def compute_damage
    @damage.clear
    g = current_globals
    # First frame, a global change, or ANY frame while a menu is open (menus
    # are transient + cheap, and a full repaint keeps popup draw + erase
    # trivial). @prev_globals carries last frame's menu-open flag, so the frame
    # a menu CLOSES on also lands here and erases it.
    force_full = @prev_globals.nil? || g != @prev_globals || !@menu.nil?

    cur = {}
    visible_windows.each do |win|
      b = window_bounds(win)
      vk = window_vkey(win)
      cur[win.id] = { vk: vk, b: b }
      next if force_full
      prev = @prev_wins[win.id]
      if prev.nil?
        @damage.add_rect(b)                     # newly appeared
      elsif prev[:vk] != vk
        u = DamageSet.union(prev[:b], b)        # moved / resized / focus / shade / retitle
        @damage.add(u[:x], u[:y], u[:w], u[:h])
      end
      # New client pixels (a commit) damage the surface even when its geometry
      # is unchanged. We over-approximate to the whole window bounds rather
      # than mapping the sub-rect through the resize scale — simpler, and still
      # far less than a full-screen repaint whenever other windows exist.
      if win.external? && !win.clipped_damage.nil?
        @damage.add_rect(b)
      end
    end
    @cur_wins = cur

    if force_full
      @damage.full!
      return
    end
    # A window present last frame but gone now leaves a hole to repaint.
    @prev_wins.each do |id, prev|
      @damage.add_rect(prev[:b]) unless cur.key?(id)
    end
    # Keep the HUD live on every composited frame.
    @damage.add_rect(hud_rect) unless @damage.empty?
  end

  # Promote the working snapshot to the retained one, after a composite.
  def snapshot_scene
    @prev_wins = @cur_wins
    @prev_globals = current_globals
  end

  # Repaint the desktop background (fill + grid) inside a single damage rect.
  # Only the grid lines that cross the rect are stroked, so the per-region cost
  # scales with the rect, not the viewport.
  def draw_desktop_region(r)
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

    # Shaded ("rolled up"): only the titlebar + its buttons are drawn — no body,
    # no resize grip, no border. The body area shows whatever sits behind the
    # window. The chrome sprite for a shaded window is titlebar-only (its key +
    # bounds already encode the shade), so we just present it and return.
    if win.shaded?
      present_chrome(win, active)
      return
    end

    # Client body FIRST, then the chrome sprite on top. The sprite carries the
    # titlebar (above the body, no overlap) plus the border + resize grip which
    # must land ON the body edges — so presenting it after the body reproduces
    # the original z-order (paint, body, paint_frame). The body area of the
    # sprite is transparent, so the body pixels show through.
    #
    # In-process windows paint a solid fill; external windows blit their
    # SharedArrayBuffer through a cached ImageData view; dom windows do NOT
    # paint a body -- the body is a real <iframe> the main thread overlays on
    # the canvas at the body-rect coords, and we just republish those coords so
    # the iframe tracks the WM.
    if win.dom?
      bx, by, bw, bh = win.body_rect
      JS.global.call("wasmboxIframeMove", win.id, bx, by, bw, bh)
    elsif win.external?
      blit_external(win)
    else
      fill_rect(win.body_rect, win.fill)
    end

    present_chrome(win, active)
  end

  # Present a decorated window's chrome (titlebar + buttons + border + resize
  # grip) via the retained-mode sprite cache. The JS helper wasmboxChromeBegin
  # returns the sprite's OffscreenCanvas 2D context on a cache MISS (so we
  # re-render the ~25 chrome ops into it once), or nil on a HIT (the cached
  # bitmap is still valid). Either way wasmboxChromePresent blits the cached
  # sprite onto the live canvas with a single drawImage. Moving/dragging a
  # window keeps the cache hot (position is not part of the key), so a drag
  # costs one drawImage per frame instead of a full chrome repaint.
  def present_chrome(win, active)
    ox, oy, sw, sh = Frame.sprite_bounds(win)
    chrome = Frame.current

    # Bench baseline: paint the decoration inline on every composite (the
    # pre-cache behaviour) so cmd/rbbench can measure the sprite-cache win.
    if @bench_no_sprite
      chrome.paint(@ctx, win, active, self)
      chrome.paint_frame(@ctx, win, active, self) unless win.shaded?
      return
    end

    key = Frame.sprite_key(win, active)
    octx = JS.global.call("wasmboxChromeBegin", win.id, key, sw, sh, ox, oy)
    unless octx.nil?
      # Cache miss: render the chrome into the sprite. wasmboxChromeBegin has
      # translated the sprite ctx by (-ox, -oy), so the chrome's absolute
      # screen coordinates land at the sprite's local origin. Swap @ctx so the
      # host helpers (fill_rect / text / stroke_rect, which the chrome calls)
      # target the sprite too.
      saved = @ctx
      @ctx = octx
      chrome.paint(@ctx, win, active, self)
      chrome.paint_frame(@ctx, win, active, self) unless win.shaded?
      @ctx = saved
    end
    JS.global.call("wasmboxChromePresent", @ctx, win.id, ox, oy)
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
    n = @wm.windows.length
    # @rendered_frames counts COMPOSITED frames (not every rAF tick): with the
    # dirty-rect gate an idle desktop stops compositing, so the counter — and
    # the fps reading — hold steady, which is the honest picture of the work
    # actually done.
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
