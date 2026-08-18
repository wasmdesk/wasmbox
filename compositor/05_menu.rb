class Menu
  # WIDTH is the panel width the compositor imposes on every menu: the width it
  # hands the toolkit at layout time and blits the rendered panel into (see
  # 08_menu_widgets.rb). It is a compositor CHOICE, not a per-row metric that can
  # drift, so it stays a constant here.
  #
  # Every ROW metric (row height, separator height, body top/bottom pad) now
  # comes from the go-widgets/toolkit menu widget that actually PAINTS the panel,
  # queried once when the menu opens via #apply_widget_layout (RowTop/RowHeight).
  # The Ruby routing (hit_test / entry_top / height) then reads those cached
  # widget-derived numbers, so it can never diverge from the pixels the toolkit
  # draws. Previously this file HARDCODED ITEM_H=22 / SEP_H=6 / TOP_PAD=2 /
  # BOT_PAD=2 as a hand-kept mirror of menu.go — which had earlier sat at the
  # stale ITEM_H=24 / no-pad and drifted the hit zones 2px/row below the painted
  # rows (worse the further down the menu): the "click offset" bug. Sourcing the
  # geometry from the widget removes the mirror, and with it the drift.
  WIDTH = 170

  attr_reader :entries

  def initialize(entries)
    @entries = entries
    # Row geometry is INJECTED by #apply_widget_layout when the menu opens. Until
    # then the routing methods report "no row" / zero height rather than guess a
    # metric — the compositor always lays a menu out before it uses or paints it.
    @row_tops = nil
    @row_heights = nil
    @total_h = 0
  end

  # Translate the domain entries into the Array-of-Hashes the toolkit menu
  # constructor (Widgets.menu) accepts. PURE data — no widgets dependency — so
  # the painter (08_menu_widgets.rb #build_widget_menu) and the geometry query
  # (#apply_widget_layout) build the SAME widget, and the queried row bands line
  # up exactly with the painted rows. Every selectable row carries a non-empty
  # "action" so the toolkit paints it enabled (and RowAt treats it as a hit
  # target — it returns -1 for action-less rows); submenu parents get a ">"
  # chevron via the "shortcut" field. The marker is never dispatched through the
  # widget; click routing stays in Ruby.
  def to_widget_items
    items = []
    @entries.each do |e|
      if e[:separator]
        items << { "separator" => true }
      else
        item = { "label" => e[:label].to_s, "action" => "x" }
        item["shortcut"] = ">" if e[:submenu]
        items << item
      end
    end
    items
  end

  # Query the toolkit menu widget for this menu's row geometry and cache it, so
  # the Ruby click routing is driven by the SAME numbers the toolkit paints with
  # — the toolkit is the single source of truth for the hit geometry.
  #
  # `widgets` is the `Widgets` module (require "widgets"). It is PASSED IN rather
  # than referenced directly so this pure-WM file (loaded by rbtest as 01..05,
  # before any `require "widgets"`) stays free of the binding: the compositor
  # hands it in (08_menu_widgets.rb #layout_menu) and rbtest hands in the same
  # module. Contract: RowTop/RowHeight need the handle laid out first (as render
  # already requires), so we lay it out — tall enough that the menu never needs
  # to scroll, so every reported RowTop is a true, scroll-free content position
  # (the compositor never scrolls a menu).
  def apply_widget_layout(widgets)
    handle = widgets.menu(to_widget_items)
    n = @entries.length
    widgets.layout(handle, WIDTH, (n + 4) * 64)
    tops = []
    heights = []
    i = 0
    while i < n
      tops << widgets.menu_row_top(handle, i)
      heights << widgets.menu_row_height(handle, i)
      i += 1
    end
    @row_tops = tops
    @row_heights = heights
    # Total painted height = the widget's body bottom edge (RowTop past the last
    # row) plus the top inset (RowTop(0)) for the matching bottom pad — every
    # term widget-derived, no hardcoded pad. Sizing the blit buffer to exactly
    # this keeps the toolkit from scrolling the body (which would re-introduce an
    # offset) and keeps the hit bands flush with the painted panel.
    @total_h = widgets.menu_row_top(handle, n) + widgets.menu_row_top(handle, 0)
    self
  end

  # The pixel height required to paint this menu, from the toolkit geometry
  # cached by #apply_widget_layout (0 before the menu has been laid out).
  def height
    @total_h
  end

  # Index of the entry whose widget row band contains (mx, my) when the menu is
  # popped up at (x, y), or -1 outside the menu / on a separator (whose band the
  # toolkit reports but which is not selectable). Mirrors toolkit RowAt: the
  # point is mapped into widget-local space (mx - x, my - y) and tested against
  # the cached [top, top + height) band of each row. Returns -1 until the
  # geometry has been applied (defensive).
  #
  # Implementation note: rbgo does NOT propagate a `return` from a block to the
  # enclosing method (see WindowManager#find), so we walk with a while-loop.
  def hit_test(x, y, mx, my)
    return -1 if @row_tops.nil?
    return -1 if mx < x || mx >= x + WIDTH
    ly = my - y
    i = 0
    n = @entries.length
    while i < n
      top = @row_tops[i]
      if ly >= top && ly < top + @row_heights[i]
        return -1 if @entries[i][:separator]
        return i
      end
      i += 1
    end
    -1
  end

  # Top-y of entry i when the menu starts at y, from the toolkit's per-row top
  # (RowTop) cached by #apply_widget_layout. Needed by the Compositor to position
  # a sub-menu flush with the parent entry it opens from. Falls back to y for an
  # unlaid menu or an out-of-range index (defensive — callers always pass a valid
  # parent index on a laid-out menu).
  def entry_top(y, idx)
    return y if @row_tops.nil? || idx < 0 || idx >= @row_tops.length
    y + @row_tops[idx]
  end
end

# ---------------------------------------------------------------------------
# Wallpaper — the selectable desktop-background registry (2026-08-03, the first
# Batch-3 / desktop-layer feature). PURE data + selection state, mirroring the
# Frame / FrameRegistry pattern so it is unit-testable off-wasm (rbtest loads
# 01..05 only). The JS-touching render lives in 10_desktop_widgets.rb, which
# reads Wallpaper.current every cache-miss; this module only says WHICH
# background is active, never paints.
#
# Each preset is one of three kinds:
#   {name:, kind: :grid}                     -> the faint-grid Backdrop (default
#                                               + the WALLPAPER-flag-off fallback)
#   {name:, kind: :gradient, top:, bottom:}  -> a vertical gradient painted via
#                                               Widgets.wallpaper_gradient
#   {name:, kind: :image, mode:}             -> the bundled procedural image
#                                               painted via Widgets.wallpaper
# ---------------------------------------------------------------------------
module Wallpaper
  # The selectable backgrounds, in root-menu order. "Grid" is first so it reads
  # as the default; the gradient presets are hand-picked dark desktop tones; the
  # trailing "Photo" is the bundled procedural image (10_desktop_widgets.rb
  # generates its pixels — no external asset to decode).
  PRESETS = [
    { name: "Grid",     kind: :grid },
    { name: "Midnight", kind: :gradient, top: "#0b1224", bottom: "#1b2a4a" },
    { name: "Slate",    kind: :gradient, top: "#2b303b", bottom: "#0e1013" },
    { name: "Aurora",   kind: :gradient, top: "#12343b", bottom: "#2c5364" },
    { name: "Ember",    kind: :gradient, top: "#3a1c1c", bottom: "#0e0a08" },
    { name: "Violet",   kind: :gradient, top: "#1a1033", bottom: "#3b1f5e" },
    { name: "Photo",    kind: :image, mode: "fill" },
  ].freeze

  # The boot / fallback selection: the grid backdrop wasmbox has always shown.
  DEFAULT = "Grid"
  @@current = DEFAULT

  # The names, in registry order, for the root-menu Wallpaper submenu.
  def self.names
    PRESETS.map { |p| p[:name] }
  end

  # The active preset NAME (marked with "* " in the submenu).
  def self.current_name
    @@current
  end

  # Look up a preset descriptor by name, or nil when unknown. Walks with an
  # indexed while-loop because rbgo block-return does not unwind the enclosing
  # method (same workaround as Menu#hit_test / WindowManager#find).
  def self.descriptor(name)
    i = 0
    n = PRESETS.length
    while i < n
      return PRESETS[i] if PRESETS[i][:name] == name
      i += 1
    end
    nil
  end

  # The active descriptor, falling back to the Grid default for an unknown
  # @@current (defensive — select() only ever stores a known name).
  def self.current
    descriptor(@@current) || PRESETS[0]
  end

  # True when name is a registered preset.
  def self.known?(name)
    !descriptor(name.to_s).nil?
  end

  # Select a preset by name IF it exists AND differs from the active one.
  # Returns the name on a real change, nil when unknown or already-active — so
  # callers (the root-menu :wallpaper dispatch, the set_wallpaper wire message,
  # restore_wallpaper) can skip a redundant repaint/persist, mirroring
  # WindowManager#set_theme.
  def self.select(name)
    name = name.to_s
    return nil unless known?(name)
    return nil if name == @@current
    @@current = name
    name
  end

  # Reset to the default — test-only helper so a spec run is order-independent.
  def self.reset!
    @@current = DEFAULT
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
    # "VS Code" is the pure-Go editor client (clients/code) — it loads
    # same-origin on any static host incl. GitHub Pages. The code-server
    # dom-window ("vscode") needs a live code-server backend + reverse proxy,
    # so it is labelled distinctly and only works on backend deployments.
    "code"      => "VS Code",
    "vscode"    => "VS Code (code-server)",
    "loom"      => "Loom",
    "showcase"  => "Toolkit Showcase",
    "calculator" => "Calculator",
    "notepad"    => "Notepad",
    "settings"   => "Settings",
    "browser"    => "Browser",
    "rubytk"     => "Tip Calculator (Ruby)",
  }.freeze

  # IDs the root menu intentionally OMITS from the Applications submenu. The
  # "editor" id currently aliases the hello worker (see LAUNCHABLE) so listing
  # both would show two identical-looking entries. "hello-oci" is a probe-only
  # demo, not a user-facing app.
  HIDDEN = ["hello-oci"].freeze

  # Compose the apps + workspaces + themes + frames submenus and the
  # top-level menu.
  #
  # `applets` is the Compositor's AppletBoard (14_applets.rb) or nil. When
  # non-nil an "Applets" submenu is inserted (after "Wallpaper") so the user can
  # toggle each desktop applet on/off; when nil (the default — and the shape
  # cmd/rbtest asserts) the entry is omitted entirely, so the menu is byte-for-
  # byte what it was before applets landed and the feature stays flag-gated.
  def self.build(wm, applets = nil)
    rows = [
      { label: "Applications", submenu: build_apps(wm) },
      { label: "Workspaces",   submenu: build_workspaces(wm) },
      { label: "Theme",        submenu: build_themes(wm) },
      { label: "Frame",        submenu: build_frames },
      { label: "Wallpaper",    submenu: build_wallpapers },
    ]
    rows << { label: "Applets", submenu: build_applets(applets) } unless applets.nil?
    rows.concat([
      { separator: true },
      { label: "About wasmbox", action: [:noop, "about"] },
      { label: "Reload",        action: [:noop, "reload"] },
      { label: "Exit",          action: [:noop, "exit"] },
    ])
    Menu.new(rows)
  end

  # Build the Applets submenu from the AppletBoard: one entry per Applet::KINDS,
  # in menu order, labelled with its friendly name and prefixed with "* " when
  # that applet is currently shown (mirrors the Theme / Frame active marker).
  # Click action is [:applet, "<kind>"] — dispatch_menu_action toggles the tile
  # on the desktop + persists the new shown-set.
  def self.build_applets(board)
    rows = []
    Applet::KINDS.each do |kind|
      shown = !board.nil? && board.shown?(kind)
      label = shown ? "* #{Applet.label(kind)}" : Applet.label(kind)
      rows << { label: label, action: [:applet, kind] }
    end
    Menu.new(rows)
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

  # Build the Frame submenu — one entry per FrameRegistry name (16 as of
  # 2026-06-30: 2 plain layouts + 14 layout×palette combos). The active
  # frame is prefixed with "* " so the user can see which one is live.
  # Click action is [:frame, "<name>"] — dispatch_menu_action swaps
  # Frame.current + repaints on the next rAF tick.
  def self.build_frames
    rows = []
    active = Frame.current_name
    FrameRegistry.names.each do |name|
      label = (name == active) ? "* #{name}" : name
      rows << { label: label, action: [:frame, name] }
    end
    Menu.new(rows)
  end

  # Build the Wallpaper submenu — one entry per Wallpaper::PRESETS name ("Grid"
  # + the gradient presets + the bundled "Photo" image). The active wallpaper is
  # prefixed with "* ". Click action is [:wallpaper, "<name>"] —
  # dispatch_menu_action routes that into Wallpaper.select + a persist, and
  # draw_desktop_widgets repaints from Wallpaper.current on the next tick.
  def self.build_wallpapers
    rows = []
    active = Wallpaper.current_name
    Wallpaper.names.each do |name|
      label = (name == active) ? "* #{name}" : name
      rows << { label: label, action: [:wallpaper, name] }
    end
    Menu.new(rows)
  end
end

# ---------------------------------------------------------------------------
# Compositor — owns the WM, the canvas and the input/render loop. This is the
# only part that talks to the JS bridge.
# ---------------------------------------------------------------------------
