class Menu
  # These row/pad metrics MIRROR the toolkit menu that actually PAINTS the
  # panel (go-widgets/toolkit menu.go: MenuRowH=22, MenuSeparatorH=6, and a
  # body drawn from `r.Y + sc(2)` with a matching 2px bottom pad). The Ruby
  # model here does the click hit-testing (routing stays in Ruby — see
  # 08_menu_widgets.rb), so its geometry MUST match the toolkit's pixel layout
  # or clicks land on the wrong row. When the menu drawing moved to the toolkit
  # these had stayed at the old hand-drawn ITEM_H=24 / no-pad, which drifted the
  # hit zones 2px per row below the painted rows (worse the further down the
  # menu) — the "click offset" bug. Keep them in lockstep with menu.go.
  ITEM_H  = 22     # row height (= toolkit MenuRowH)
  WIDTH   = 170    # default menu width
  SEP_H   = 6      # separator row height (= toolkit MenuSeparatorH)
  PAD_X   = 8      # left/right text padding
  TOP_PAD = 2      # body top inset (= toolkit `r.Y + sc(2)` row start)
  BOT_PAD = 2      # body bottom inset (toolkit total = rows + sc(4))

  attr_reader :entries

  def initialize(entries)
    @entries = entries
  end

  # The pixel height required to paint this menu: the top+bottom body pads plus
  # every entry row (separators are shorter than regular rows). Matches the
  # toolkit's natural height (rows + sc(4)); sizing the blit buffer to exactly
  # this keeps the toolkit from scrolling the body (which would re-introduce an
  # offset), and keeps hit_test's region flush with the painted panel.
  def height
    h = TOP_PAD + BOT_PAD
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
    return -1 if my < y + TOP_PAD
    cy = y + TOP_PAD
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
    cy = y + TOP_PAD
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
    cy = y + TOP_PAD
    @entries.each_with_index do |e, i|
      row_h = e[:separator] ? SEP_H : ITEM_H
      yield e, cy, row_h, i
      cy += row_h
    end
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
