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
  MIN_SZ    = 14 # minimize-box side (matches close-box)
  GRIP      = 14 # resize-corner side
  MIN_W     = 90
  MIN_H     = 60
end

# ---------------------------------------------------------------------------
# Window — a client surface plus its decoration geometry and hit-testing.
# Pure data + math; no JS here.
# ---------------------------------------------------------------------------
class Window
  attr_accessor :id, :title, :x, :y, :w, :h, :fill, :focused, :role, :minimized, :workspace, :shaded

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
    # Openbox-style minimize: a minimized window is removed from the render
    # pipeline (decoration + body) but kept in the stack so a click on its
    # entry in the dock's iconbar can restore it. Panels are never minimized.
    @minimized = false
    # Fluxbox-style workspaces: every normal window belongs to exactly one
    # workspace (1..WorkspaceCount). Only the active workspace's windows
    # render and appear in the iconbar. Panels ignore the workspace field —
    # they are always visible (workspace = 0 sentinel, set by spawn paths).
    # WindowManager.register_external / WindowManager.spawn fill this in at
    # creation time so the default here only matters for direct Window.new
    # callers in tests.
    @workspace = 1
    # Window-shade ("roll-up", a.k.a. replier/déplier): a shaded window collapses
    # to just its titlebar — the body is not rendered and not hit-testable, but
    # the window stays put + draggable + closable from its titlebar. Toggled by a
    # two-finger swipe over the titlebar (up = fold, down = unfold). Distinct from
    # minimize (which folds into the dock iconbar). Panels/popups never shade.
    @shaded = false
  end

  def focused? = @focused
  def minimized? = @minimized
  def shaded? = @shaded

  # A panel (dock-style surface) has no decoration and is anchored, never
  # cascade-placed. A popup is a child surface anchored to a parent window
  # (menus / tooltips): also undecorated, but placed parent-relative and
  # grab-dismissed. Anything else is a normal, decorated window.
  def panel? = @role == "panel"
  def popup? = @role == "popup"
  # Decorated = a normal window: titlebar, close/minimize/resize boxes,
  # draggable, focusable. Panels and popups carry no decoration.
  def decorated? = !panel? && !popup?

  # Outer frame (decoration included): the titlebar sits above the body. For a
  # panel there is no titlebar, so the frame top is the body top.
  def frame_top = decorated? ? @y - Theme::TITLE_H : @y
  def right     = @x + @w
  def bottom    = @y + @h

  # Rectangles, each as [x, y, w, h]. Panels carry no decoration rectangles, so
  # the titlebar / close / resize hit-rects collapse to empty (zero-size) and
  # the frame equals the body.
  def titlebar_rect = decorated? ? [@x, frame_top, @w, Theme::TITLE_H] : [@x, @y, 0, 0]
  def body_rect     = [@x, @y, @w, @h]

  def close_rect
    return [@x, @y, 0, 0] unless decorated?
    pad = (Theme::TITLE_H - Theme::CLOSE_SZ) / 2
    [right - Theme::CLOSE_SZ - pad, frame_top + pad, Theme::CLOSE_SZ, Theme::CLOSE_SZ]
  end

  # Minimize-box rect: a 14x14 button (Theme::MIN_SZ side) placed just LEFT of
  # the close box in the titlebar, with the same vertical padding. Openbox
  # paints a single horizontal bar near the bottom of this box (the "_" glyph)
  # to signal "fold this window into the dock". Panels carry no decoration so
  # this collapses to an empty (zero-size) rect, same as close_rect.
  def minimize_rect
    return [@x, @y, 0, 0] unless decorated?
    pad = (Theme::TITLE_H - Theme::MIN_SZ) / 2
    cx, cy, _cw, _ch = close_rect
    [cx - Theme::MIN_SZ - pad, cy, Theme::MIN_SZ, Theme::MIN_SZ]
  end

  def resize_rect
    return [@x, @y, 0, 0] unless decorated?
    return [@x, @y, 0, 0] if @shaded   # a rolled-up window has no body to resize
    [right - Theme::GRIP, bottom - Theme::GRIP, Theme::GRIP, Theme::GRIP]
  end

  # The whole decorated extent, used for "did the click land on me at all?".
  # For an undecorated surface (panel / popup) the extent is just the body; for a
  # shaded (rolled-up) window the extent is just the titlebar (the body is gone,
  # so clicks in that area fall through to whatever is behind it).
  def frame_rect
    return [@x, @y, @w, @h] unless decorated?
    return [@x, frame_top, @w, Theme::TITLE_H] if @shaded
    [@x, frame_top, @w, @h + Theme::TITLE_H]
  end

  def hit?(rect, px, py)
    rx, ry, rw, rh = rect
    px >= rx && px < rx + rw && py >= ry && py < ry + rh
  end

  def contains?(px, py)   = hit?(frame_rect, px, py)
  # Only a decorated window is draggable, closable-by-box, minimizable or
  # resizable; for panels and popups every decoration hit-test reports "no hit"
  # so those gestures can never start.
  def on_titlebar?(px, py)= decorated? ? hit?(titlebar_rect, px, py) : false
  def on_close?(px, py)   = decorated? ? hit?(close_rect, px, py) : false
  def on_minimize?(px, py)= decorated? ? hit?(minimize_rect, px, py) : false
  def on_resize?(px, py)  = decorated? ? hit?(resize_rect, px, py) : false

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
  # For popups: the window_id of the parent surface this popup is anchored to
  # (nil for windows/panels). Used to dismiss a popup when its parent goes.
  attr_accessor :parent_id

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
  #
  # The desktop root-menu (right-click an empty area of the desktop, see RootMenu
  # below) reads this same table to populate its "Applications" submenu: an id
  # listed here can be launched from the menu, anything else cannot. Keep the
  # registry the SINGLE source of truth for the launch trust boundary so both
  # surfaces (dock + root-menu) agree on what is launchable.
  LAUNCHABLE = {
    "terminal"  => "clients/terminal/worker.js",
    "editor"    => "clients/hello/worker.js",
    "files"     => "clients/files/worker.js",
    # Bundled hello client (the wasm "Hello, wasmbox!" demo). Same descriptor
    # shape as terminal/files; the root menu exposes it as "Hello (wasm)".
    "hello"     => "clients/hello/worker.js",
    # Bundled quake client (pure-Go Quake 1, from the sibling go-quake1 repo).
    # The wasm is huge (~190 MB) and may not be built locally, but the worker.js
    # is part of the wasmbox tree, so the descriptor is always safe — the worker
    # surfaces an error window when the wasm is missing, never the compositor.
    "quake"     => "clients/quake/worker.js",
    # OCI demo ids (resolved against WASMBOX_OCI_REGISTRIES or the default
    # http://127.0.0.1:5000 registry). The id "hello-oci" mirrors the bundled
    # hello client but pulls it from the registry instead of from disk; it
    # is what the Playwright OCI probe drives via wasmboxSpawnFromOCI.
    "hello-oci" => { oci: "hello:latest" },
  }.freeze

  LAYOUT_SEP = "\t"

  # Fluxbox-style virtual workspaces. WORKSPACE_COUNT is fixed at 4 (numbered
  # 1..4); the active workspace defaults to 1 at boot. Every normal window
  # belongs to exactly one workspace and only the active workspace's windows
  # render + appear in the iconbar. Panels (the dock) ignore the workspace
  # field and are always visible — the dock IS the UI that switches
  # workspaces, so hiding it with the windows would be a usability bug.
  WORKSPACE_COUNT = 4

  # THEMES is the trust boundary for the `set_theme` protocol message: a
  # client can only switch to a name in this map. The value is the raw
  # Openbox `.themerc` source the panel will re-parse Go-side. The same
  # three names are mirrored by wasmdock/internal/theme.Builtin() so the
  # dock can paint them locally (it never depends on the Ruby copy reaching
  # it — the wire ships the full .themerc so the dock and compositor agree
  # on the bytes). Keep insertion order: it defines the root-menu ordering.
  #
  # Adding a new theme here:
  #   1. Ship the `.themerc` text in BOTH wasmdock/internal/theme/themes/ AND
  #      wasmbox/clients/dock/internal/theme/themes/ (Go side builds the
  #      bundle from there).
  #   2. Add an entry here with the SAME text.
  #   3. The root menu's Theme submenu picks it up automatically (build_themes
  #      below iterates THEMES). NO compositor.rb code change beyond this map.
  THEMES = {
    "Fluxbox Light" => <<~RC,
      border.color:   #4a4a4a
      border.width:   1
      padding.width:  1
      padding.height: 1
      window.active.title.bg:           vertical flat
      window.active.title.bg.color:     #c8c8c8
      window.active.title.bg.colorTo:   #909090
      window.active.label.text.color:   #1a1a1a
      window.active.label.text.font:    sans 9
      window.inactive.title.bg:         vertical flat
      window.inactive.title.bg.color:   #909090
      window.inactive.title.bg.colorTo: #909090
      window.inactive.label.text.color: #202020
      window.inactive.label.text.font:  sans 9
      menu.title.bg:           flat
      menu.title.bg.color:     #c8c8c8
      menu.title.text.color:   #1a1a1a
      menu.title.text.font:    sans 9
      menu.items.bg:           flat
      menu.items.bg.color:     #d0d0d0
      menu.items.text.color:   #1a1a1a
      menu.items.text.font:    sans 9
      osd.bg:                  flat
      osd.bg.color:            #d0d0d0
      osd.label.text.color:    #1a1a1a
      osd.label.text.font:     sans 9
    RC
    "Fluxbox Dark" => <<~RC,
      border.color:   #505050
      border.width:   1
      padding.width:  1
      padding.height: 1
      window.active.title.bg:           vertical flat
      window.active.title.bg.color:     #2a2a2a
      window.active.title.bg.colorTo:   #1a1a1a
      window.active.label.text.color:   #f0f0f0
      window.active.label.text.font:    sans 9
      window.inactive.title.bg:         vertical flat
      window.inactive.title.bg.color:   #1a1a1a
      window.inactive.title.bg.colorTo: #1a1a1a
      window.inactive.label.text.color: #b0b0b0
      window.inactive.label.text.font:  sans 9
      menu.title.bg:           flat
      menu.title.bg.color:     #2a2a2a
      menu.title.text.color:   #f0f0f0
      menu.title.text.font:    sans 9
      menu.items.bg:           flat
      menu.items.bg.color:     #1f1f1f
      menu.items.text.color:   #e0e0e0
      menu.items.text.font:    sans 9
      osd.bg:                  flat
      osd.bg.color:            #303030
      osd.label.text.color:    #f0f0f0
      osd.label.text.font:     sans 9
    RC
    "GNOME Adwaita" => <<~RC,
      border.color:   #c0c0c0
      border.width:   1
      padding.width:  1
      padding.height: 1
      window.active.title.bg:           vertical flat
      window.active.title.bg.color:     #3584e4
      window.active.title.bg.colorTo:   #1c71d8
      window.active.label.text.color:   #ffffff
      window.active.label.text.font:    sans 9
      window.inactive.title.bg:         flat
      window.inactive.title.bg.color:   #fafafa
      window.inactive.title.bg.colorTo: #fafafa
      window.inactive.label.text.color: #2e2e2e
      window.inactive.label.text.font:  sans 9
      menu.title.bg:           flat
      menu.title.bg.color:     #3584e4
      menu.title.text.color:   #ffffff
      menu.title.text.font:    sans 9
      menu.items.bg:           flat
      menu.items.bg.color:     #fafafa
      menu.items.text.color:   #2e2e2e
      menu.items.text.font:    sans 9
      osd.bg:                  flat
      osd.bg.color:            #ffffff
      osd.label.text.color:    #2e2e2e
      osd.label.text.font:     sans 9
    RC
  }.freeze

  # Default theme picked at boot. Must be a key in THEMES.
  DEFAULT_THEME = "Fluxbox Light"

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
    # Stash for the window most recently closed by a peer-issued `close` wire
    # message (the dock right-clicking an iconbar entry). Filled in by
    # handle_client_message's "close" arm BEFORE the close so the Compositor
    # can post a `closed` event to the gone window's worker without a
    # post-close lookup. Cleared by reader on consumption.
    @last_closed_by_peer = nil
    # Records the most recent commit/lifecycle messages we have processed, for
    # introspection from tests. (Bounded to the last 16 — pure data.)
    @last_messages = []
    # Currently active workspace (1..WORKSPACE_COUNT). New windows land here;
    # render + windows_snapshot filter to this number.
    @active_workspace = 1
    @workspace_count = WORKSPACE_COUNT
    # Currently active theme (a key in THEMES). The Ruby-side Theme module
    # still owns the compositor's own decoration colours; this field tracks
    # which Openbox theme the panel + future Ruby-side restyling honour. The
    # root-menu Theme submenu mutates it via set_theme; the Compositor
    # broadcasts the new theme back to panels via notify_theme_changed.
    @active_theme = DEFAULT_THEME
  end

  # Active workspace accessor; the dock reads it via the `workspace_changed`
  # event payload, tests read it directly.
  def active_workspace = @active_workspace
  def workspace_count  = @workspace_count

  # Active-theme accessor. Returns a key in THEMES (DEFAULT_THEME until
  # set_theme has accepted a different name). Tests read it directly; the
  # root menu reads it to render the `*` active-marker.
  def active_theme = @active_theme

  # Switch the active theme. Returns the new name on success, nil if `name`
  # is not a known theme or is already active (caller skips notify on nil
  # so the dock does not spin on no-ops). Pure data — the Compositor calls
  # notify_theme_changed when this returns non-nil.
  def set_theme(name)
    return nil unless name.is_a?(String)
    return nil unless THEMES.key?(name)
    return nil if name == @active_theme
    @active_theme = name
  end

  # Raw .themerc source for a known theme name, or nil for an unknown one.
  # The Compositor's notify_theme_changed ships this verbatim so the panel
  # re-parses it locally with the SAME bytes the bundled Go-side themes
  # carry (which keeps the Go + Ruby views of "Fluxbox Light" in lockstep).
  def theme_source(name)
    THEMES[name]
  end

  # Known theme names in insertion order. The root menu reads this to build
  # its Theme submenu. Returns a fresh array.
  def theme_names
    THEMES.keys
  end

  # Switch the active workspace to n (1..workspace_count). Returns the new
  # active workspace on a real change, nil when n is out of range or already
  # active (so the Compositor can skip a redundant broadcast). Pure state —
  # the render loop reads @active_workspace on its next frame.
  def set_workspace(n)
    return nil unless n.is_a?(Integer)
    return nil if n < 1 || n > @workspace_count
    return nil if n == @active_workspace
    @active_workspace = n
    # Focus must follow the active workspace: the previously-focused window
    # may live on a different workspace now (and so must not advertise itself
    # as focused). Re-pick the top normal non-minimized window on the new
    # workspace. We clear ALL focus first to keep the "exactly one focused
    # window" invariant; if no window exists on the new workspace, focus
    # legitimately becomes nil until one is spawned.
    @stack.each { |o| o.focused = false unless o.panel? }
    next_top = nil
    @stack.each do |w|
      next_top = w if !w.panel? && !w.minimized? && w.workspace == @active_workspace
    end
    next_top.focused = true if next_top
    @active_workspace
  end

  # Windows on workspace n (bottom-to-top, panels excluded). Public so callers
  # (and tests) can introspect per-workspace contents without rebuilding the
  # filter inline.
  def windows_on_workspace(n)
    @stack.select { |w| !w.panel? && w.workspace == n }
  end

  # Consume + clear the most recent peer-close stash. The route layer reads
  # this exactly once after a :closed_by_peer dispatch and posts the `closed`
  # event to the now-removed window's worker.
  def take_last_closed_by_peer
    p = @last_closed_by_peer
    @last_closed_by_peer = nil
    p
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

  # The keyboard-focused window: the top-most NORMAL non-minimized window
  # ON THE ACTIVE WORKSPACE. Panels (the dock) are excluded from the focus
  # ring, so a panel is never "focused" even though it sits on top visually.
  # A minimized window has been folded into the dock's iconbar and is not
  # visible on the canvas; it must not receive synthetic focus traffic
  # either. A window on a different workspace is hidden + cannot receive
  # focus traffic on the active one. Returns nil when no normal non-minimized
  # window exists on the active workspace.
  def focused
    top = nil
    # Only decorated windows take keyboard focus (panels + popups are excluded;
    # a popup still gets mouse input by hit-testing), and only those on the
    # active workspace.
    @stack.each do |w|
      top = w if w.decorated? && !w.minimized? && w.workspace == @active_workspace
    end
    top
  end

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

  # Every external client (panel + normal) in the stack. Used by the
  # Compositor to broadcast desktop-wide events (e.g. theme_changed) to any
  # client that opted in to follow them. Bottom-to-top stack order.
  def externals
    @stack.select { |w| w.external? }
  end

  # Open popup surfaces (menus / tooltips), bottom-to-top. The pointer is
  # grabbed while any popup is open: a click inside one is forwarded to it, a
  # click outside dismisses them (see Compositor#on_mousedown).
  def popups
    @stack.select { |w| w.popup? }
  end

  # Popups anchored to a given parent window_id (dismissed when it closes).
  def child_popups(parent_id)
    @stack.select { |w| w.popup? && w.parent_id == parent_id }
  end

  # The surface a keyboard event should be routed to: an open popup grabs the
  # keyboard (the top-most one), otherwise the focused window. nil if neither.
  def key_target
    p = popups
    p.empty? ? focused : p.last
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
  # wraps once it would run off a notional screen. Spawned onto the active
  # workspace — the user spawned it from there, so that is where they want to
  # see it.
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
    win.workspace = @active_workspace
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
    # Panels and popups never take focus: a panel is the always-on-top dock; a
    # popup receives mouse via hit-testing but must not re-stack or steal focus.
    return win if win.panel? || win.popup?
    unstack(win)
    @stack.push(win)
    @stack.each { |o| o.focused = false unless o.panel? }
    win.focused = true
    win
  end

  def close(win)
    unstack(win)
    # Popups anchored to this window are orphaned — unmap them too so a closed
    # parent never leaves a dangling menu on screen. (Telling the popup client
    # it was closed is the Compositor's job; see dismiss_popups / notify_closed.)
    child_popups(win.id).each { |p| unstack(p) } if win
    top = @stack.last
    top.focused = true if top
    win
  end

  # Minimize a normal window: mark it minimized (so the render loop skips it),
  # drop focus (the dock will not receive synthetic focus traffic), and let the
  # caller emit a tasks_changed notification afterwards. Panels are NEVER
  # minimized — they are the always-on-top stratum hosting the iconbar that
  # restores other windows. Returns the window on a real state change, nil
  # when the call is a no-op (panel, already minimized, unknown window).
  def minimize(win)
    return nil unless win
    return nil if win.panel?
    return nil if win.minimized?
    win.minimized = true
    # Clear focus so on-screen state matches: a minimized window cannot be
    # the focused one. The top normal non-minimized window ON THE ACTIVE
    # WORKSPACE takes focus (workspace-foreign windows are not visible and
    # cannot capture focus).
    @stack.each { |o| o.focused = false unless o.panel? }
    next_top = nil
    @stack.each do |w|
      next_top = w if !w.panel? && !w.minimized? && w.workspace == @active_workspace
    end
    next_top.focused = true if next_top
    win
  end

  # Restore a minimized window: clear the minimized flag and raise+focus so it
  # comes back to the top of the stack, ready for keyboard input. Returns the
  # window on a real state change, nil for unknown / non-minimized inputs.
  def restore_window(win)
    return nil unless win
    return nil unless win.minimized?
    win.minimized = false
    focus(win)
    win
  end

  # Shade ("roll up") a window: collapse it to just its titlebar (the body stops
  # rendering + hit-testing). Only decorated windows shade; idempotent. Returns
  # the window on a real state change, nil otherwise.
  def shade(win)
    return nil unless win
    return nil unless win.decorated?
    return nil if win.shaded?
    win.shaded = true
    win
  end

  # Unshade ("roll down") a window: expand it back to its full body. Idempotent.
  # Returns the window on a real state change, nil otherwise.
  def unshade(win)
    return nil unless win
    return nil unless win.shaded?
    win.shaded = false
    win
  end

  # Every window currently in the minimized state, bottom-to-top. The
  # Compositor turns this into the dock's iconbar "tasks" sub-section.
  def minimized_windows
    @stack.select { |w| w.minimized? && !w.panel? }
  end

  # Snapshot every non-panel window ON THE ACTIVE WORKSPACE (open + minimized),
  # bottom-to-top, as plain Hashes the Compositor can serialize and post to
  # the dock as a `windows_changed` event. Each entry carries:
  #   :id         compositor window id (echoed back in focus/close/restore wires)
  #   :title      current title (verbatim)
  #   :minimized  true iff the window is currently folded into the iconbar
  #   :focused    true iff this is the keyboard-focused window
  #   :role       "window" (always non-panel here — panels are filtered out)
  #   :workspace  workspace number this window lives on (always == active
  #               workspace in the current snapshot, since the filter drops
  #               foreign ones — but the field is sent so a future "show all"
  #               panel view can read it without a schema change)
  # The order mirrors the stack so first-spawned windows sit leftmost in the
  # iconbar even when later windows are raised on top. Pure data — no JS.
  def windows_snapshot
    @stack.select { |w| !w.panel? && w.workspace == @active_workspace }.map do |w|
      { id: w.id, title: w.title.to_s, minimized: w.minimized?,
        focused: w.focused?, role: w.role.to_s, workspace: w.workspace }
    end
  end

  # Top-most window under the pointer (search the stack top-down). A minimized
  # window has been folded into the dock's iconbar — it leaves no pixels on the
  # canvas, so a click at its former coordinates must NOT hit it. The
  # compositor's render loop skips minimized windows for the same reason; this
  # keeps input + paint in lockstep. Windows on inactive workspaces are
  # likewise invisible and unhittable.
  def window_at(px, py)
    hit = nil
    @stack.each do |w|
      next if w.minimized?
      next if !w.panel? && w.workspace != @active_workspace
      hit = w if w.contains?(px, py) # last (topmost) wins
    end
    hit
  end

  # Alt+Tab-ish cycle over the NORMAL non-minimized windows ON THE ACTIVE
  # WORKSPACE only — panels (the dock), minimized windows, and windows on
  # other workspaces are excluded from the focus ring so Tab never lands on
  # any of them. Sends the current focused window to the bottom of the normal
  # pool and focuses the next normal window down, so repeated presses walk
  # the visible-on-this-workspace app stack.
  def cycle
    normals = normal_windows.reject(&:minimized?).select { |w| w.workspace == @active_workspace }
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
  def register_external(title, req_w, req_h, role = "window", parent_id: nil, rel_x: 0, rel_y: 0)
    @next_id += 1
    case role
    when "panel"
      # Panels (the dock + Fluxbox-style toolbars) own their own geometry -- a
      # 28-px bottom bar is the whole point. Skip the MIN_W / MIN_H clamps that
      # keep a normal window's title + close box reachable; a panel has neither.
      granted_w = req_w
      granted_h = req_h
      x = 0
      y = 0
    when "popup"
      # Popups own their geometry too (a menu can be tiny -- no MIN clamp), and
      # are placed at (rel_x, rel_y) inside the PARENT window's body. If the
      # parent has gone, fall back to the bare offset from the screen origin.
      granted_w = req_w
      granted_h = req_h
      parent = parent_id && find(parent_id)
      x = (parent ? parent.x : 0) + rel_x
      y = (parent ? parent.y : 0) + rel_y
    else
      granted_w = [req_w, Theme::MIN_W].max
      granted_h = [req_h, Theme::MIN_H].max
      step = 28
      x = 60 + (@cascade % 6) * step
      y = 60 + (@cascade % 6) * step
      @cascade += 1
    end
    win = ExternalWindow.new(@next_id, title, x, y, granted_w, granted_h, role)
    win.parent_id = parent_id if role == "popup"
    # A panel is always visible (never workspace-filtered), encoded by the 0
    # sentinel so workspace_filtered renders + snapshots skip it cleanly. A
    # normal/popup window lands on the active workspace at register time — the
    # user opened it from there, so that is where it appears.
    win.workspace = (role == "panel") ? 0 : @active_workspace
    # Newest surface goes on top: a popup naturally sits above its (older)
    # parent. Panels are pinned on top each frame by the Compositor regardless.
    @stack.push(win)
    @last_registered = win
    # Only normal windows join the focus ring + saved-layout. Panels are never
    # focused; popups take mouse input via hit-testing but never keyboard focus.
    if role == "window"
      focus(win)
      apply_saved(win)
    end
    win
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
      role = case msg[:role].to_s
             when "panel" then "panel"
             when "popup" then "popup"
             else "window"
             end
      # parent arrives from JS as a Number (Float); coerce so `find` matches the
      # integer window ids. nil for non-popup hellos (no parent field).
      parent_id = msg[:parent] && msg[:parent].to_i
      win = register_external(msg[:title] || "client", msg[:w] || 200, msg[:h] || 150, role,
                              parent_id: parent_id, rel_x: (msg[:rel_x] || 0).to_i, rel_y: (msg[:rel_y] || 0).to_i)
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
    when "restore"
      # The dock asks to un-minimize a window the user clicked on its iconbar.
      # An unknown id, a non-minimized window or a panel id is dropped — the
      # restore op is idempotent + safe.
      win = find(msg[:window_id])
      return :ignored unless win
      return :ignored if win.panel?
      return :ignored unless win.minimized?
      restore_window(win)
      :restored
    when "focus"
      # The dock asks to focus + raise a window the user clicked on its iconbar.
      # If the window is currently minimized we also restore it (Fluxbox semantics:
      # one click on an iconbar entry brings the window to the foreground regardless
      # of its current state). Unknown id or panel id is dropped.
      win = find(msg[:window_id])
      return :ignored unless win
      return :ignored if win.panel?
      if win.minimized?
        restore_window(win)
      else
        focus(win)
      end
      :focused
    when "set_workspace"
      # The dock asks to switch the active workspace (Fluxbox: click or scroll
      # on the workspace section of the toolbar). An out-of-range or already-
      # active index is dropped — the compositor's caller skips a redundant
      # broadcast based on the :ignored result so the dock does not spin.
      idx = msg[:index]
      idx = idx.to_i if !idx.is_a?(Integer) && !idx.nil?
      changed = set_workspace(idx)
      changed.nil? ? :ignored : :workspace_changed
    when "set_theme"
      # The dock (or the root-menu Theme submenu via dispatch_menu_action)
      # asks to switch the active Openbox theme. An unknown name or the
      # already-active name is dropped (set_theme returns nil); the caller
      # skips notify_theme_changed on :ignored so the panel does not spin.
      changed = set_theme(msg[:name].to_s)
      changed.nil? ? :ignored : :theme_changed
    when "move_window"
      # Reserved for v1: drag a window to another workspace via the dock. The
      # wire shape `{type:"move_window", window_id:, workspace:}` updates
      # win.workspace and refreshes the snapshot. The dock UI for it (click +
      # hold a window-iconbar entry, drag onto a workspace number) is not in
      # v0 — only the message arm is reserved so the wire protocol stays
      # forward-compatible.
      :ignored
    when "close"
      # The dock asks to close a window the user right-clicked on its iconbar.
      # Same effect as clicking the window's close box. Unknown id or panel id
      # is dropped. Returns :closed_by_peer (distinct from the :closed symbol
      # used by a window closing ITSELF via request_close) so the Compositor
      # tells the CLOSED window's worker about the closure — not the dock's
      # worker, which is the peer that asked. The closed window's worker ref
      # + id are stashed in @last_closed_by_peer so route_worker_message can
      # post the `closed` event without re-looking-up a window we just removed
      # from the stack. An in-process (non-external) window has no worker, so
      # the stash carries external: false and the route layer skips the post.
      win = find(msg[:window_id])
      return :ignored unless win
      return :ignored if win.panel?
      ext = win.external?
      w_ref = ext ? win.worker : nil
      @last_closed_by_peer = { worker: w_ref, window_id: win.id, external: ext }
      close(win)
      :closed_by_peer
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
# Menu — a single hierarchical popup menu (Openbox-style). Each entry is a
# plain Hash so the same structure can describe a regular leaf, a separator,
# or a sub-menu opener:
#
#   { label: "Terminal",      action: [:launch, "terminal"]      }
#   { label: "Applications",  submenu: <Menu> }
#   { separator: true }
#
# Pure data + math: hit_test answers WHICH entry a (mx, my) pair falls in;
# region answers the [x, y, w, h] rectangle the menu paints into. The
# Compositor owns the canvas paint + the action dispatcher.
# ---------------------------------------------------------------------------
class Menu
  ITEM_H = 24      # row height, per spec
  WIDTH  = 170     # default menu width
  SEP_H  = 6       # separator row height (a thin divider, not a full row)
  PAD_X  = 8       # left/right text padding

  attr_reader :entries

  def initialize(entries)
    @entries = entries
  end

  # The pixel height required to paint this menu, summed over every entry
  # (separators are shorter than regular rows).
  def height
    h = 0
    @entries.each { |e| h += e[:separator] ? SEP_H : ITEM_H }
    h
  end

  # Bounding rectangle when popped up at (x, y), as [x, y, w, h].
  def region(x, y)
    [x, y, WIDTH, height]
  end

  # Index of the entry containing the point (mx, my) when the menu is popped
  # up at (x, y), or -1 if the point falls outside the menu region (or on a
  # separator, which is not selectable). Separators occupy SEP_H rather than
  # the full ITEM_H, and they advance the cursor without being targetable.
  #
  # Implementation note: rbgo does NOT propagate a `return` from a block to
  # the enclosing method (the block-local return only exits the block — see
  # WindowManager#find for the same workaround). So we walk with a while-loop.
  def hit_test(x, y, mx, my)
    return -1 if mx < x || mx >= x + WIDTH
    return -1 if my < y
    cy = y
    i = 0
    n = @entries.length
    while i < n
      e = @entries[i]
      row_h = e[:separator] ? SEP_H : ITEM_H
      if my < cy + row_h
        return -1 if e[:separator]
        return i
      end
      cy += row_h
      i += 1
    end
    -1
  end

  # Top-y of entry i when the menu starts at y. Needed by the Compositor to
  # position a sub-menu flush with the parent entry it opens from. Walks with
  # an indexed while-loop because rbgo block-return does not unwind the
  # enclosing method.
  def entry_top(y, idx)
    cy = y
    i = 0
    n = @entries.length
    while i < n
      return cy if i == idx
      cy += @entries[i][:separator] ? SEP_H : ITEM_H
      i += 1
    end
    cy
  end

  # Convenience for the renderer: walk entries with their (y, h) row metrics.
  # Yields [entry, row_y, row_h, index] for each entry.
  def each_row(y)
    cy = y
    @entries.each_with_index do |e, i|
      row_h = e[:separator] ? SEP_H : ITEM_H
      yield e, cy, row_h, i
      cy += row_h
    end
  end
end

# ---------------------------------------------------------------------------
# RootMenu — builder for the desktop right-click menu (the Openbox "root
# menu"). Hierarchy:
#
#   Applications →   (one entry per LAUNCHABLE id, label-formatted)
#   Workspaces   →   (one entry per workspace 1..wm.workspace_count)
#   ──────────       (separator)
#   About wasmbox    (v0: dismiss-only)
#   Reload           (v0: dismiss-only)
#   Exit             (v0: dismiss-only)
#
# Pure: takes a WindowManager and returns a Menu. The Compositor pops it on
# right-click of an empty desktop area and dispatches the selected entry.
# ---------------------------------------------------------------------------
module RootMenu
  # Label overrides for the Applications submenu — every LAUNCHABLE id gets an
  # entry, but the labels in the menu are human-friendly rather than the raw
  # ids. An id missing from this map falls back to the capitalized id.
  APP_LABELS = {
    "terminal"  => "Terminal",
    "editor"    => "Editor",
    "files"     => "Files",
    "hello"     => "Hello (wasm)",
    "quake"     => "Quake",
    "hello-oci" => "Hello (OCI)",
  }.freeze

  # IDs the root menu intentionally OMITS from the Applications submenu. The
  # "editor" id currently aliases the hello worker (see LAUNCHABLE) so listing
  # both would show two identical-looking entries. "hello-oci" is a probe-only
  # demo, not a user-facing app.
  HIDDEN = ["hello-oci"].freeze

  # Compose the apps + workspaces + themes submenus and the top-level menu.
  def self.build(wm)
    Menu.new([
      { label: "Applications", submenu: build_apps(wm) },
      { label: "Workspaces",   submenu: build_workspaces(wm) },
      { label: "Theme",        submenu: build_themes(wm) },
      { separator: true },
      { label: "About wasmbox", action: [:noop, "about"] },
      { label: "Reload",        action: [:noop, "reload"] },
      { label: "Exit",          action: [:noop, "exit"] },
    ])
  end

  # Build the Applications submenu from the LAUNCHABLE registry. The order
  # follows APP_LABELS insertion order so the listing is stable + readable
  # (terminal/editor/files first, hello, then quake), with any LAUNCHABLE ids
  # we did not pre-label appended at the end.
  def self.build_apps(wm)
    rows = []
    seen = {}
    APP_LABELS.each do |id, label|
      next if HIDDEN.include?(id)
      next unless wm.launchable?(id)
      rows << { label: label, action: [:launch, id] }
      seen[id] = true
    end
    # Hash#each_key is not implemented in rbgo — iterate with #each and the
    # 2-arg destructure shape so we only need the key.
    WindowManager::LAUNCHABLE.each do |id, _desc|
      next if seen[id]
      next if HIDDEN.include?(id)
      rows << { label: id.to_s.capitalize, action: [:launch, id] }
    end
    Menu.new(rows)
  end

  # Build the Workspaces submenu. One entry per workspace, 1..wm.workspace_count.
  def self.build_workspaces(wm)
    rows = []
    n = wm.workspace_count
    i = 1
    while i <= n
      rows << { label: "Workspace #{i}", action: [:workspace, i] }
      i += 1
    end
    Menu.new(rows)
  end

  # Build the Theme submenu. One entry per WindowManager::THEMES key, in
  # insertion order. The currently active theme is prefixed with "* " so
  # the user can see which one is live. Click action is [:theme, "<name>"]
  # — dispatch_menu_action routes that into wm.set_theme + notify_theme_changed.
  def self.build_themes(wm)
    rows = []
    wm.theme_names.each do |name|
      label = (name == wm.active_theme) ? "* #{name}" : name
      rows << { label: label, action: [:theme, name] }
    end
    Menu.new(rows)
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
      # A new window joins the iconbar — but only AFTER the dock panel itself
      # has registered, otherwise notify_windows_changed is a no-op anyway. For
      # the dock's own welcome, notify_windows_changed still publishes the
      # initial list of any already-spawned in-process boot windows.
      notify_windows_changed
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
    %w[window_id w h stride title role app index workspace name parent rel_x rel_y].each do |k|
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
      e.call("preventDefault")
      @wm.cycle
      notify_windows_changed
      return
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
    draw_desktop
    # Re-anchor every panel to the bottom-center of the current canvas, so the
    # dock tracks viewport resizes and never cascades.
    @wm.panels.each { |p| @wm.anchor_panel(p, @width, @height) }
    # Draw normal windows first, then panels on top (always-on-top stratum).
    # Minimized windows have been folded into the dock's iconbar; the
    # compositor skips them entirely so they leave no pixels on the canvas.
    # Windows on inactive workspaces are likewise skipped — only the active
    # workspace's windows render. Panels (the dock) IGNORE the workspace
    # filter and always render, because the dock is the UI that switches
    # workspaces in the first place.
    active = @wm.active_workspace
    @wm.ordered_windows.each do |win|
      next if win.minimized?
      next if !win.panel? && win.workspace != active
      draw_window(win)
    end
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

    # Minimize box (left of the close box). Openbox draws this as a small box
    # with a single horizontal bar near the bottom — the "_" glyph. When the
    # window is inactive the face is the same neutral CLOSE_BG but the bar ink
    # is lighter so the button reads as muted.
    mx, my, mw, mh = win.minimize_rect
    fill_rect(win.minimize_rect, Theme::CLOSE_BG)
    @ctx.set("strokeStyle", active ? Theme::CLOSE_GLYPH : Theme::BORDER_INACTIVE)
    @ctx.set("lineWidth", 1.5)
    @ctx.call("beginPath")
    @ctx.call("moveTo", mx + 3,      my + mh - 4)
    @ctx.call("lineTo", mx + mw - 3, my + mh - 4)
    @ctx.call("stroke")

    # Shaded ("rolled up"): only the titlebar + its boxes are drawn — no body,
    # no resize grip. The body area shows whatever sits behind the window.
    return if win.shaded?

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
