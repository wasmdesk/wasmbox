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
    "vscode"    => "VS Code",
    "loom"      => "Loom",
    "showcase"  => "Toolkit Showcase",
    "calculator" => "Calculator",
  }.freeze

  # IDs the root menu intentionally OMITS from the Applications submenu. The
  # "editor" id currently aliases the hello worker (see LAUNCHABLE) so listing
  # both would show two identical-looking entries. "hello-oci" is a probe-only
  # demo, not a user-facing app.
  HIDDEN = ["hello-oci"].freeze

  # Compose the apps + workspaces + themes + frames submenus and the
  # top-level menu.
  def self.build(wm)
    Menu.new([
      { label: "Applications", submenu: build_apps(wm) },
      { label: "Workspaces",   submenu: build_workspaces(wm) },
      { label: "Theme",        submenu: build_themes(wm) },
      { label: "Frame",        submenu: build_frames },
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
end

# ---------------------------------------------------------------------------
# Compositor — owns the WM, the canvas and the input/render loop. This is the
# only part that talks to the JS bridge.
# ---------------------------------------------------------------------------
