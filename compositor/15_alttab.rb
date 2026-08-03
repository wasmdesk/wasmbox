# ---------------------------------------------------------------------------
# Alt-Tab window switcher — the compositor render + wire glue over the pure
# cursor model in 05_alttab.rb. A Batch-4 window-management feature (2026-08-03),
# sibling of window snapping (04_window_manager.rb / 06_core.rb).
#
# Hold Alt+Tab and a centered overlay pops up: a horizontal strip of window
# THUMBNAIL tiles (one per cycleable window on the active workspace, most-
# recently-focused first), the current choice highlighted. Each Tab advances the
# selection (Shift+Tab reverses); releasing Alt — or pressing Enter — focuses +
# raises the chosen window; Escape cancels. The strip is a go-widgets tree:
#
#   Widgets.thumbnail(pixels, w, h, label) x N -> Widgets.h_box -> Widgets.render
#   -> RGBA bytes -> base64 -> JS wasmboxBlitRGBAOver (alpha-composited
#   drawImage) centered OVER the windows, the SAME seam the HUD
#   (09_hud_widgets.rb), toasts (12) and applets (14) use.
#
# Chosen stratum: a compositor-drawn OVERLAY (like the tray / applets), NOT a new
# window role, so the heavily co-edited WindowManager stays untouched — the only
# WM addition is the pure #cycle_candidates ring (04_window_manager.rb). The
# overlay is painted near the END of Compositor#render (draw_alttab), above the
# windows + menu, so it reads as a modal switcher.
#
# Thumbnail pixels: an in-process window contributes its solid body fill under a
# titlebar-tinted strip (a faithful shrunk snapshot of a compositor-painted
# window); an external (SharedArrayBuffer) or dom (iframe) window has no pixels
# reachable from Ruby, so it gets a titled PLACEHOLDER tile in a neutral tint —
# the tile still carries the window title + the selection border, which is what
# the switcher is for. (Per the design note: don't block on perfect thumbnails.)
#
# Because headless metaKey/Alt chords are unreliable, a probe-drivable hook
# __wasmboxAltTab(cmd) (open / advance / advance_reverse / commit / cancel) drives
# the exact same transitions the real keydown/keyup path does, and draw_alttab
# publishes the live overlay geometry + selection through wasmboxPublishAltTab so
# test/probe-alttab.mjs can assert the centered strip, the moving highlight and
# the committed focus deterministically without reading pixels.
#
# Gated behind Compositor::ALTTAB so `main` stays shippable during the live
# co-edit — flip it to false to disable the overlay + every keydown/keyup/probe
# hook below with zero other changes (Shift+Tab quick-cycle still works: it is
# the pre-existing WindowManager#cycle path in 06_core.rb, independent of this).
# ---------------------------------------------------------------------------
require "widgets"
require "base64"

class Compositor
  # Master switch for the whole Alt-Tab switcher feature. `false` makes every
  # keydown/keyup/probe hook + draw_alttab a no-op; the plain Shift+Tab
  # quick-cycle in 06_core.rb is unaffected (it does not go through here).
  ALTTAB = true

  # Overlay geometry. Each tile is a fixed cell; tiles sit in a horizontal strip
  # with ALTTAB_GAP between them, the strip inset by ALTTAB_PAD inside a lifted
  # card plate (translucent surface + hairline border, like the applet plate).
  ALTTAB_TILE_W = 168
  ALTTAB_TILE_H = 120
  ALTTAB_GAP    = 12
  ALTTAB_PAD    = 16
  # Keep the whole strip on-screen: when many windows would push the row past
  # the viewport, tiles shrink (aspect-preserved by the Thumbnail) down to
  # ALTTAB_TILE_MIN_W before the panel is allowed to exceed the desktop, leaving
  # ALTTAB_MARGIN of breathing room on each side.
  ALTTAB_TILE_MIN_W = 72
  ALTTAB_MARGIN     = 40
  ALTTAB_PLATE  = "rgba(28, 30, 40, 0.94)"
  ALTTAB_BORDER = "rgba(90, 96, 114, 0.95)"

  # Thumbnail source buffer: a small solid image the toolkit Thumbnail scales
  # (aspect-preserved) into the tile's image area. Kept tiny — the source is a
  # flat fill, so a few pixels carry the window's colour + a titlebar strip
  # exactly; the tile does the upscale. ALTTAB_TITLE_ROWS of the source are the
  # titlebar tint, the rest the body fill.
  ALTTAB_SRC_W      = 48
  ALTTAB_SRC_H      = 30
  ALTTAB_TITLE_ROWS = 6
  # Titlebar tint + placeholder body tint for windows with no Ruby-reachable
  # pixels (external SAB / dom iframe).
  ALTTAB_TITLE_FILL = "#3a3e46"
  ALTTAB_PLACEHOLD  = "#2b2f3a"

  # Lazily-built switcher cursor (model in 05_alttab.rb, loaded before this
  # file). Built on first access so Compositor#initialize (06_core.rb) needs no
  # edit and pure rbtest — which never loads this file — is unaffected.
  def alttab
    @alttab ||= AltTab.new
  end

  # Is the switcher currently up (a keyboard grab is in effect)?
  def alttab_active? = ALTTAB && alttab.active?

  # --- transitions (shared by the real keydown/keyup path + the probe hook) ---

  # A real Alt+Tab press: open the switcher over the current candidate ring if it
  # is not up yet, then advance the selection one step (Shift reverses). This is
  # the familiar "first press jumps to the next window" behaviour — open parks the
  # cursor on the focused window, the advance moves it off. No-op when the flag is
  # off or there is nothing to cycle.
  def alttab_key(reverse)
    return nil unless ALTTAB
    unless alttab.active?
      cands = @wm.cycle_candidates
      return nil if cands.empty?
      alttab.open(cands)
    end
    alttab.advance(reverse)
    nil
  end

  # Open the switcher parked on the focused window WITHOUT advancing (the probe
  # uses this to inspect the initial selection). No-op when already up / empty.
  def alttab_open
    return nil unless ALTTAB
    return nil if alttab.active?
    cands = @wm.cycle_candidates
    return nil if cands.empty?
    alttab.open(cands)
  end

  # Advance the selection while the switcher is up (a subsequent Tab, or the
  # probe). No-op when it is not up.
  def alttab_advance(reverse)
    return nil unless alttab_active?
    alttab.advance(reverse)
  end

  # Commit the current choice: close the switcher and focus + raise the selected
  # window, then refresh the iconbar (focus changed). No-op when it is not up.
  def alttab_commit
    return nil unless alttab_active?
    win = alttab.commit
    return nil if win.nil?
    @wm.focus(win)
    notify_windows_changed
    win
  end

  # Cancel (Escape): close the switcher, choosing nothing.
  def alttab_cancel
    return nil unless ALTTAB
    alttab.cancel
    nil
  end

  # Probe entry point (__wasmboxAltTab in compositor.worker.js dispatches here via
  # the bus). Drives the SAME transitions the keyboard does, so the probe path and
  # the real path share one implementation. Unknown commands are ignored.
  def alttab_probe(cmd)
    case cmd.to_s
    when "open"            then alttab_open
    when "advance"         then alttab_key(false)
    when "advance_reverse" then alttab_key(true)
    when "commit"          then alttab_commit
    when "cancel"          then alttab_cancel
    end
  end

  # --- render -------------------------------------------------------------
  # Draw the switcher overlay: a centered card plate + a horizontal strip of
  # thumbnail tiles, the selected one highlighted, blitted OVER the windows.
  # Publishes the live overlay geometry + selection every frame (active or not)
  # so the probe can read it. No-op paint when the flag is off or the switcher is
  # closed — but the publish still runs so the probe sees "inactive".
  def draw_alttab
    return nil unless ALTTAB
    unless alttab.active?
      publish_alttab_inactive
      return nil
    end
    cands = alttab.items
    n = cands.length
    tile_w  = alttab_tile_width(n)
    inner_w = n * tile_w + (n - 1) * ALTTAB_GAP
    inner_h = ALTTAB_TILE_H
    panel_w = inner_w + 2 * ALTTAB_PAD
    panel_h = inner_h + 2 * ALTTAB_PAD
    panel_x = (@width - panel_w) / 2
    panel_y = (@height - panel_h) / 2

    # Card plate (cheap ctx fill each frame) drawn first so the transparent-ground
    # widget strip composites over a solid panel.
    fill_rect([panel_x, panel_y, panel_w, panel_h], ALTTAB_PLATE)
    stroke_rect([panel_x, panel_y, panel_w, panel_h], ALTTAB_BORDER, 1)

    sig = alttab_paint_sig(cands, alttab.index, inner_w, inner_h)
    if @alttab_b64.nil? || @alttab_sig != sig
      @alttab_b64 = render_alttab_strip(cands, alttab.index, tile_w, inner_w, inner_h)
      @alttab_sig = sig
      @alttab_blitted = false
    end
    dx = panel_x + ALTTAB_PAD
    dy = panel_y + ALTTAB_PAD
    if @alttab_blitted
      JS.global.call("wasmboxBlitRGBAOver", @ctx, "", inner_w, inner_h, dx, dy, "alttab")
    else
      JS.global.call("wasmboxBlitRGBAOver", @ctx, @alttab_b64, inner_w, inner_h, dx, dy, "alttab")
      @alttab_blitted = true
    end

    publish_alttab_active(n, alttab.index, panel_x, panel_y, panel_w, panel_h, alttab.selected)
    nil
  end

  # The per-tile width for a strip of `n` tiles: the fixed ALTTAB_TILE_W, shrunk
  # (down to ALTTAB_TILE_MIN_W) only when `n` tiles at full width would push the
  # row past the desktop, so the whole switcher stays on-screen.
  def alttab_tile_width(n)
    return ALTTAB_TILE_W if n < 1
    max_inner = @width - 2 * ALTTAB_MARGIN - 2 * ALTTAB_PAD
    needed = n * ALTTAB_TILE_W + (n - 1) * ALTTAB_GAP
    return ALTTAB_TILE_W if needed <= max_inner
    fit = (max_inner - (n - 1) * ALTTAB_GAP) / n
    fit < ALTTAB_TILE_MIN_W ? ALTTAB_TILE_MIN_W : fit
  end

  # Build the strip's RGBA buffer (base64): an h_box of fixed-width Thumbnail
  # tiles with the selected one flagged. Each tile downscales the window's source
  # pixels + captions its title.
  def render_alttab_strip(cands, sel, tile_w, w, h)
    strip = Widgets.h_box
    Widgets.set_spacing(strip, ALTTAB_GAP)
    i = 0
    cands.each do |win|
      px, sw, sh = alttab_thumb(win)
      tile = Widgets.thumbnail(px, sw, sh, alttab_label(win), "")
      Widgets.set_selected(tile, i == sel)
      Widgets.add_fixed(strip, tile, tile_w)
      i += 1
    end
    Widgets.layout(strip, w, h)
    img = Widgets.render(strip, w, h)
    Base64.strict_encode64(img["pixels"])
  end

  # A content signature that changes exactly when the strip's pixels must: the
  # ordered window ids + titles, the selected index, and the buffer size. A pure
  # advance (selection move) changes `sel`; a spawn/close between opens changes
  # the id list; nothing else re-renders.
  def alttab_paint_sig(cands, sel, w, h)
    parts = ["#{w}x#{h}", "s#{sel}"]
    cands.each { |win| parts << "#{win.id}:#{win.title}" }
    parts.join("|")
  end

  # A tile caption: the window title, or a stand-in when it is blank.
  def alttab_label(win)
    t = win.title.to_s
    t.empty? ? "window ##{win.id}" : t
  end

  # The thumbnail SOURCE for a window: [base64 RGBA, src_w, src_h]. An in-process
  # window paints its real body fill under a titlebar strip (a faithful shrunk
  # snapshot); an external (SAB) / dom (iframe) window has no Ruby-reachable
  # pixels, so it gets a neutral placeholder body — still titlebar-striped, so the
  # tile reads as a window. The toolkit Thumbnail upscales this small flat source.
  def alttab_thumb(win)
    body = alttab_body_color(win)
    buf = alttab_window_rgba(ALTTAB_SRC_W, ALTTAB_SRC_H, ALTTAB_TITLE_FILL, body)
    [Base64.strict_encode64(buf), ALTTAB_SRC_W, ALTTAB_SRC_H]
  end

  # The body tint for a window's tile: its own fill for an in-process window
  # (compositor-painted, so this IS its body), else the neutral placeholder.
  def alttab_body_color(win)
    if !win.external? && !win.dom? && !win.fill.nil?
      win.fill
    else
      ALTTAB_PLACEHOLD
    end
  end

  # Build a w x h RGBA buffer (raw bytes): the top ALTTAB_TITLE_ROWS rows in the
  # titlebar tint, the rest in the body tint. Pure integer math, no image decoder.
  def alttab_window_rgba(w, h, title_hex, body_hex)
    tr, tg, tb = hex_rgb(title_hex)
    br, bg, bb = hex_rgb(body_hex)
    buf = String.new
    y = 0
    while y < h
      title = y < ALTTAB_TITLE_ROWS
      r = title ? tr : br
      g = title ? tg : bg
      b = title ? tb : bb
      x = 0
      while x < w
        buf << r
        buf << g
        buf << b
        buf << 255
        x += 1
      end
      y += 1
    end
    buf
  end

  # Parse a "#rrggbb" (or "#rgb") hex colour into [r, g, b] byte integers. An
  # unparseable value falls back to the placeholder grey so a tile never crashes
  # the render.
  def hex_rgb(hex)
    s = hex.to_s
    s = s[1..-1] if s.start_with?("#")
    if s.length == 3
      s = "#{s[0]}#{s[0]}#{s[1]}#{s[1]}#{s[2]}#{s[2]}"
    end
    return [43, 47, 58] unless s.length == 6
    [s[0, 2].to_i(16), s[2, 2].to_i(16), s[4, 2].to_i(16)]
  end

  # --- probe publish ------------------------------------------------------
  # Publish the live overlay state to the JS test hook every frame. The probe
  # reads it via __wasmboxAltTabState(): active flag, candidate count, selected
  # index, the centered panel rect (to assert centering) and the SELECTED
  # window's body rect (to assert that a commit focuses exactly that window, by
  # comparing against __wasmboxFocusedRect afterwards).
  def publish_alttab_active(count, index, px, py, pw, ph, sel)
    sx = sel.nil? ? -1 : sel.x
    sy = sel.nil? ? -1 : sel.y
    sw = sel.nil? ? 0 : sel.w
    sh = sel.nil? ? 0 : sel.h
    JS.global.call("wasmboxPublishAltTab", 1, count, index, px, py, pw, ph, sx, sy, sw, sh)
    nil
  end

  def publish_alttab_inactive
    JS.global.call("wasmboxPublishAltTab", 0, 0, 0, 0, 0, 0, 0, -1, -1, 0, 0)
    nil
  end
end
