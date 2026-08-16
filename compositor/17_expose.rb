# ---------------------------------------------------------------------------
# Exposé / "show all windows" — the compositor render + wire glue over the pure
# spread model in 05_expose.rb. The LAST Batch-4 window-management feature
# (2026-08-03), sibling of the Alt-Tab switcher (15_alttab.rb), the Spotlight
# launcher (16_spotlight.rb) and window snapping (06_core.rb).
#
# Press F3 (Mission-Control muscle memory) and every window on the current
# workspace fans out as a NON-OVERLAPPING grid of THUMBNAIL tiles over a dimmed
# desktop: hover a tile to highlight it, click it to focus that window and exit;
# or use the arrows to walk a keyboard selection, Enter to commit, Escape to
# exit unchanged. F3 again toggles the spread back off. Each tile is a go-widgets
# thumbnail:
#
#   Widgets.thumbnail(pixels, w, h, title) -> Widgets.v_box -> Widgets.render
#   -> RGBA bytes -> base64 -> JS wasmboxBlitRGBAOver (alpha-composited
#   drawImage) at the tile's grid rect, over a full-screen dim fill — the SAME
#   seam the HUD (09), toasts (12), applets (14), the Alt-Tab strip (15) and the
#   Spotlight palette (16) use.
#
# Chosen stratum: a compositor-drawn OVERLAY (like Alt-Tab / Spotlight), NOT a
# new window role, so the heavily co-edited WindowManager stays untouched — the
# candidate ring is the pre-existing #cycle_candidates (04_window_manager.rb),
# the SAME one Alt-Tab uses, so panels, popups, minimized windows and windows on
# other workspaces are already excluded. The dim + spread is painted at the END
# of Compositor#render (draw_expose), above the windows, so it reads as a modal.
#
# Thumbnail pixels reuse the Alt-Tab machinery verbatim (alttab_thumb /
# alttab_label / alttab_window_rgba, 15_alttab.rb): an in-process window
# contributes its solid body fill under a titlebar-tinted strip; an external
# (SharedArrayBuffer) or dom (iframe) window has no Ruby-reachable pixels and so
# gets a titled placeholder tile — still captioned + selection-bordered, which is
# what the spread is for.
#
# Because a headless F3 chord + pixel-perfect thumbnail reads are unreliable, a
# probe-drivable hook __wasmboxExpose(cmd) drives the exact same transitions the
# real keydown/mouse path does ("toggle"/"open"/"left"/"right"/"up"/"down"/
# "select:<i>"/"click:<x>,<y>"/"commit"/"cancel"), and draw_expose publishes the
# live spread geometry — the grid dimensions, every tile rectangle, the selected
# index and the SELECTED window's rect — through wasmboxPublishExpose so
# test/probe-expose.mjs can assert the non-overlapping tiles, the moving
# selection, the click→window resolution and the committed focus deterministically
# without reading pixels.
#
# Gated behind Compositor::EXPOSE so `main` stays shippable during the live
# co-edit — flip it to false to disable the overlay + every keydown/mouse/probe
# hook below with zero other changes.
# ---------------------------------------------------------------------------
require "widgets"
require "base64"

class Compositor
  # Master switch for the whole Exposé feature. `false` makes every keydown/
  # mouse/probe hook + draw_expose a no-op.
  EXPOSE = true

  # Spread geometry. The work area is the desktop inset by EXPOSE_MARGIN on every
  # side, minus the dock strip at the bottom (snap_reserved_bottom) so no tile
  # hides under the panel. Each tile sits inside its grid cell with EXPOSE_GAP of
  # dim gap around it (so tiles never touch — the non-overlap is visible).
  EXPOSE_MARGIN = 48
  EXPOSE_GAP    = 14
  # Full-screen scrim painted UNDER the tiles so the live windows read as dimmed
  # and the thumbnails pop. Translucent so a hint of the desktop shows through.
  EXPOSE_DIM = "rgba(12, 14, 22, 0.72)"

  # Lazily-built spread cursor (model in 05_expose.rb, loaded before this file).
  # Built on first access so Compositor#initialize (06_core.rb) needs no edit and
  # pure rbtest — which never loads this file — is unaffected.
  def expose
    @expose ||= Expose.new
  end

  # Is the spread currently up (a keyboard + mouse grab is in effect)?
  def expose_active? = EXPOSE && expose.active?

  # The work-area rectangle [ax, ay, aw, ah] the grid lays out inside. Shared by
  # the render (draw_expose) and the hit-test (expose_tile_at) so a click resolves
  # against the exact rectangles that were painted.
  def expose_bounds
    ax = EXPOSE_MARGIN
    ay = EXPOSE_MARGIN
    aw = @width - 2 * EXPOSE_MARGIN
    ah = @height - 2 * EXPOSE_MARGIN - snap_reserved_bottom
    [ax, ay, aw, ah]
  end

  # --- transitions (shared by the real keydown/mouse path + the probe hook) ---

  # Toggle the spread: open it over the current candidate ring if it is down,
  # else cancel it (F3 both raises and dismisses). No-op when the flag is off or,
  # on open, there is nothing to spread. Returns self / nil.
  def expose_toggle
    return nil unless EXPOSE
    if expose.active?
      expose.cancel
      return nil
    end
    cands = @wm.cycle_candidates
    return nil if cands.empty?
    expose.open(cands)
  end

  # Open the spread over the current candidate ring (the probe uses this to
  # inspect the initial layout). No-op when already up / empty.
  def expose_open
    return nil unless EXPOSE
    return nil if expose.active?
    cands = @wm.cycle_candidates
    return nil if cands.empty?
    expose.open(cands)
  end

  # Move the keyboard selection one cell (:left/:right/:up/:down) while the spread
  # is up. No-op when it is not up.
  def expose_move(dir)
    return nil unless expose_active?
    expose.move(dir)
  end

  # Jump the selection straight to tile `i` (mouse hover). No-op when not up.
  def expose_select(i)
    return nil unless expose_active?
    expose.select_index(i)
  end

  # Commit the current choice: close the spread and focus + raise the selected
  # window, then refresh the iconbar (focus changed). No-op when it is not up.
  def expose_commit
    return nil unless expose_active?
    win = expose.commit
    return nil if win.nil?
    @wm.focus(win)
    notify_windows_changed
    win
  end

  # Cancel (Escape / click on empty space): close the spread, choosing nothing.
  def expose_cancel
    return nil unless EXPOSE
    expose.cancel
    nil
  end

  # The tile index under (mx, my) in screen coordinates, or -1. Resolves against
  # the SAME work-area bounds + gap the render uses, so a click lands on exactly
  # the tile the user sees.
  def expose_tile_at(mx, my)
    return -1 unless expose_active?
    b = expose_bounds
    expose.tile_at(mx, my, b[0], b[1], b[2], b[3], EXPOSE_GAP)
  end

  # Mouse hover: highlight the tile under the pointer by moving the selection to
  # it. A hover over the dim gap leaves the selection put. No-op when not up.
  def expose_hover(mx, my)
    return nil unless expose_active?
    i = expose_tile_at(mx, my)
    expose.select_index(i) if i >= 0
    nil
  end

  # Mouse click: focus the window whose tile was clicked and exit; a click over
  # the dim gap (no tile) dismisses the spread without changing focus. No-op when
  # not up.
  def expose_click(mx, my)
    return nil unless expose_active?
    i = expose_tile_at(mx, my)
    if i < 0
      expose_cancel
      return nil
    end
    expose.select_index(i)
    expose_commit
  end

  # The Exposé toggle key: F3, the Mission-Control / GNOME "overview" idiom. A
  # bare F3 keydown (no chord) is checked in on_keydown before the arrow/Tab
  # handling so it never leaks to a client, and it is reliable headless.
  def expose_trigger?(key)
    key == "F3"
  end

  # Probe entry point (__wasmboxExpose in compositor.worker.js dispatches here via
  # the bus). Drives the SAME transitions the keyboard + mouse do. The detail is a
  # command string, optionally "<cmd>:<arg>":
  #   toggle | open | commit | cancel | left | right | up | down
  #   select:<i>     jump the selection to tile i
  #   click:<x>,<y>  resolve + commit the tile under (x, y)
  # Unknown commands are ignored.
  def expose_probe(cmd)
    s = cmd.to_s
    name = s
    arg = ""
    idx = s.index(":")
    unless idx.nil?
      name = s[0, idx]
      arg  = s[(idx + 1), s.length - idx - 1]
    end
    case name
    when "toggle" then expose_toggle
    when "open"   then expose_open
    when "commit" then expose_commit
    when "cancel" then expose_cancel
    when "left"   then expose_move(:left)
    when "right"  then expose_move(:right)
    when "up"     then expose_move(:up)
    when "down"   then expose_move(:down)
    when "select" then expose_select(arg.to_i)
    when "click"  then expose_probe_click(arg)
    end
  end

  # Parse a "<x>,<y>" click argument and drive expose_click through it (the probe
  # exercises the exact click→window resolution the real mouse path uses).
  def expose_probe_click(arg)
    s = arg.to_s
    ci = s.index(",")
    return nil if ci.nil?
    cx = s[0, ci].to_i
    cy = s[(ci + 1), s.length - ci - 1].to_i
    expose_click(cx, cy)
  end

  # --- render -------------------------------------------------------------
  # Draw the spread: a full-screen dim scrim, then a grid of thumbnail tiles (the
  # selected one highlighted) blitted OVER it. Publishes the live spread geometry
  # every frame (active or not) so the probe can read it. No-op paint when the
  # flag is off or the spread is closed — but the publish still runs so the probe
  # sees "inactive".
  def draw_expose
    return nil unless EXPOSE
    unless expose.active?
      publish_expose_inactive
      return nil
    end
    n = expose.count
    b = expose_bounds
    ax = b[0]
    ay = b[1]
    aw = b[2]
    ah = b[3]

    # Dim the desktop + windows behind the spread (cheap ctx fill each frame).
    fill_rect([0, 0, @width, @height], EXPOSE_DIM)

    sig = expose_paint_sig(expose.items, expose.index, ax, ay, aw, ah)
    if @expose_tiles.nil? || @expose_sig != sig
      @expose_tiles = render_expose_tiles(ax, ay, aw, ah, EXPOSE_GAP)
      @expose_sig = sig
      @expose_blitted = false
    end
    i = 0
    @expose_tiles.each do |t|
      key = "expose#{i}"
      if @expose_blitted
        JS.global.call("wasmboxBlitRGBAOver", @ctx, "", t[:w], t[:h], t[:x], t[:y], key)
      else
        JS.global.call("wasmboxBlitRGBAOver", @ctx, t[:b64], t[:w], t[:h], t[:x], t[:y], key)
      end
      i += 1
    end
    @expose_blitted = true

    publish_expose_active(n, expose.index, ax, ay, aw, ah)
    nil
  end

  # Build the per-tile buffers: for each candidate, its grid rectangle + a
  # base64 RGBA thumbnail (the selected one flagged). Each tile is a v_box holding
  # one Thumbnail so the small window source upscales into the whole tile, exactly
  # as the Alt-Tab strip composes its tiles.
  def render_expose_tiles(ax, ay, aw, ah, gap)
    tiles = []
    cands = expose.items
    n = cands.length
    i = 0
    while i < n
      win = cands[i]
      rr = expose.tile_rect(i, ax, ay, aw, ah, gap)
      px, sw, sh = alttab_thumb(win)
      box = Widgets.v_box
      tile = Widgets.thumbnail(px, sw, sh, alttab_label(win), "")
      Widgets.set_selected(tile, i == expose.index)
      Widgets.add_flex(box, tile, 1)
      Widgets.layout(box, rr[2], rr[3])
      img = Widgets.render(box, rr[2], rr[3])
      tiles << { x: rr[0], y: rr[1], w: rr[2], h: rr[3],
                 b64: Base64.strict_encode64(img["pixels"]) }
      i += 1
    end
    tiles
  end

  # A content signature that changes exactly when the spread's pixels must: the
  # work-area rect, the selected index, the tile count and the ordered window ids
  # + titles. A pure selection move changes `sel`; a spawn/close between opens
  # changes the id list; a resize changes the bounds; nothing else re-renders.
  def expose_paint_sig(cands, sel, ax, ay, aw, ah)
    parts = ["#{ax},#{ay},#{aw},#{ah}", "s#{sel}", "n#{cands.length}"]
    # Fold each window's live content-seq (alttab_content_seq, 15_alttab.rb) into
    # the signature so a live (external SAB) tile re-renders when its window
    # repaints; idle tiles hold their seq and the cached spread is re-presented.
    cands.each { |win| parts << "#{win.id}:#{win.title}:#{alttab_content_seq(win)}" }
    parts.join("|")
  end

  # A compact JSON array of every tile rectangle "[[x,y,w,h],...]" so the probe
  # can assert the tiles are non-overlapping + inside the work area. Pure string
  # concatenation (rbgo has no JSON library).
  def expose_tiles_json(ax, ay, aw, ah, gap, n)
    parts = []
    i = 0
    while i < n
      rr = expose.tile_rect(i, ax, ay, aw, ah, gap)
      parts << "[#{rr[0]},#{rr[1]},#{rr[2]},#{rr[3]}]"
      i += 1
    end
    "[" + parts.join(",") + "]"
  end

  # --- probe publish ------------------------------------------------------
  # Publish the live spread state to the JS test hook every frame. The probe reads
  # it via __wasmboxExposeState(): the active flag, the tile count, the grid
  # cols/rows, the selected index, the SELECTED window's body rect (to confirm a
  # commit focuses exactly that window against __wasmboxFocusedRect) and the JSON
  # of every tile rect (to assert the non-overlapping spread).
  # sel_live is 1 when the selected tile is a LIVE grabbed framebuffer (external
  # SAB window) vs a flat placeholder (in-process / dom); the selected tile's
  # on-screen rect is tiles[index], which the probe samples to assert real
  # (textured) content.
  def publish_expose_active(n, index, ax, ay, aw, ah)
    sel = expose.selected
    sx = sel.nil? ? -1 : sel.x
    sy = sel.nil? ? -1 : sel.y
    sw = sel.nil? ? 0 : sel.w
    sh = sel.nil? ? 0 : sel.h
    sel_live = sel && alttab_live?(sel) ? 1 : 0
    tiles = expose_tiles_json(ax, ay, aw, ah, EXPOSE_GAP, n)
    JS.global.call("wasmboxPublishExpose",
      1, n, expose.cols, expose.rows, index, sx, sy, sw, sh, tiles, sel_live)
    nil
  end

  def publish_expose_inactive
    JS.global.call("wasmboxPublishExpose",
      0, 0, 0, 0, 0, -1, -1, 0, 0, "[]", 0)
    nil
  end
end
