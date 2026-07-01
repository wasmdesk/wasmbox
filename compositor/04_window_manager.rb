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
    "code"      => "clients/code/worker.js",
    "files"     => "clients/files/worker.js",
    # Bundled hello client (the wasm "Hello, wasmbox!" demo). Same descriptor
    # shape as terminal/files; the root menu exposes it as "Hello (wasm)".
    "hello"     => "clients/hello/worker.js",
    # Toolkit showcase: a single wasmbox window holding the wasmdesk/toolkit
    # widget set (MenuBar + Toolbar + Notebook with one tab per family +
    # Statusbar). Acts as both a smoke test for the toolkit and a live API
    # reference users can poke from inside the compositor.
    "showcase"  => "clients/showcase/worker.js",
    # Toolkit calculator: a small focused consumer of wasmdesk/toolkit
    # (display Entry + 5×4 Grid of Buttons). Same wire protocol as every
    # other client; validates the toolkit at tighter scope than the
    # multi-tab showcase.
    "calculator" => "clients/calculator/worker.js",
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
    # Dom-window descriptor: opens a real VS Code / vscodium running on a
    # local code-server inside a wasmbox window via an iframe overlay.
    # The compositor paints the chrome (titlebar/close/resize) on the
    # canvas; the body is the iframe at body-rect.
    #
    # The URL is SAME-ORIGIN (relative path "/code-server/"): wasmbox sets
    # Cross-Origin-Embedder-Policy: require-corp for SAB, and a direct
    # cross-origin iframe to 127.0.0.1:8443 would be blocked because
    # code-server sends no CORP header. The cmd/serve reverse-proxy mounts
    # the upstream under /code-server/ when -code-server-url (or
    # $WASMBOX_CODE_SERVER_URL) is set, making the iframe same-origin.
    # Operator must have code-server listening AND start the wasmbox
    # serve with the proxy config:
    #   code-server --auth none --bind-addr 127.0.0.1:8443
    #   WASMBOX_CODE_SERVER_URL=http://127.0.0.1:8443 wasmdesk-up
    "vscode"    => { dom: "/code-server/", w: 1100, h: 700, title: "VS Code" },
    # Loom: openweft's collaborative editor (Svelte 5 + CodeMirror 6 +
    # Yjs). Built once with `vite build --base=/loom/` from
    # weft-loom-server/web, the dist is copied into clients/loom/dist
    # and mounted same-origin under /loom/* by cmd/serve. Opens as a
    # dom-window iframe. Compile/collab WS to the upstream
    # weft-loom-server is opt-in and not yet wired.
    "loom"      => { dom: "/loom/", w: 1200, h: 800, title: "Loom" },
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

  # Map a launchable app id to its dom-window descriptor (url + initial
  # geometry + title), or nil when the id is unknown OR when the descriptor
  # is one of the other shapes. Returns a Hash with :url / :w / :h / :title
  # all guaranteed non-nil; the :launch dispatcher hands it to
  # spawn_dom_window which validates further.
  def launchable_dom(app)
    desc = LAUNCHABLE[app.to_s]
    return nil unless desc.is_a?(Hash)
    url = desc[:dom]
    return nil if url.nil?
    {
      url:   url.to_s,
      w:     (desc[:w] || 1100).to_i,
      h:     (desc[:h] || 700).to_i,
      title: (desc[:title] || app.to_s),
    }
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

  # spawn_dom is the dom-window twin of #spawn: same cascade placement +
  # focus + saved-layout restore, but produces a DOMWindow whose body
  # is a real <iframe> the main thread overlays on the canvas. Used by
  # Compositor#spawn_dom_window (which then calls wasmboxIframeAttach
  # to materialise the iframe element).
  def spawn_dom(title, w, h, url)
    @next_id += 1
    step = 28
    base_x = 60
    base_y = 60
    x = base_x + (@cascade % 6) * step
    y = base_y + (@cascade % 6) * step
    @cascade += 1
    win = DOMWindow.new(@next_id, title, x, y, w, h, url)
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
      # Optional aspect-ratio lock declared at hello time. A client that knows
      # its native aspect (Quake = 4:3, retro consoles ditto) can pin it here
      # and the compositor's resize_to will preserve w/h == ratio while the
      # user drags the grip. Absent / zero / negative -> free resize.
      la = msg[:lock_aspect]
      win.lock_aspect = la.to_f if !la.nil? && la.to_f > 0
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
    when "set_lock_aspect"
      # A client (e.g. Quake) post-handshake declares an aspect-ratio lock for
      # the interactive resize grip. The compositor's resize_to then snaps
      # height to width/ratio on every drag tick, so the SAB's intrinsic shape
      # (4:3 for Quake) survives the user pulling the grip around. Purely
      # additive: a client that never sends this keeps the free-resize behaviour
      # the SDK has always exposed. Unknown id, panel id, or non-positive ratio
      # is dropped -- the lock is opt-in.
      win = find(msg[:window_id])
      return :ignored unless win
      return :ignored if win.panel?
      ratio = msg[:ratio]
      return :ignored if ratio.nil?
      r = ratio.to_f
      return :ignored if r <= 0
      win.lock_aspect = r
      :lock_aspect_set
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
