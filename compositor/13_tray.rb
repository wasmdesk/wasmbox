# ---------------------------------------------------------------------------
# System tray / status area — the compositor render + wire glue over the pure
# model in 05_tray.rb. Second feature of the DE (desktop-environment) spine
# (2026-08-03).
#
# An app registers a status icon with `{type:"tray_add", id, icon|glyph, w, h,
# tooltip}` (client SDK: `client.trayAdd(opts)`); the compositor paints the
# icons in a right-to-left strip along the top of the screen, LEFT of the toast
# column (05_notifications.rb owns the top-right corner), rendered through the
# go-widgets DE overlay set:
#
#   Widgets.status_area + Widgets.status_icon(glyph,..)/status_icon_image(b64,..)
#   -> Widgets.add_widget -> Widgets.render -> RGBA bytes -> base64 ->
#   JS wasmboxBlitRGBAOver (alpha-composited drawImage), the SAME seam the HUD
#   (09_hud_widgets.rb) and toasts (12_notifications.rb) use.
#
# Interaction is authoritative on the Ruby side (the model hit-tests the strip;
# the compositor fires the event / opens the menu), so the widget's own
# OnClick/OnRightClick callbacks are left empty — exactly like the toast's action
# button. A LEFT click on an app icon posts a `tray_clicked` input event back to
# the owner; a RIGHT click posts `tray_menu` (or, for a compositor-owned
# indicator, opens its Ruby menu through the existing compositor menu path). On a
# client's window close the compositor drops that client's tray items.
#
# Two compositor-OWNED indicators (a settings gear + a search glyph) are
# registered at first paint so the strip is visibly populated even with no app
# running — a stable target for the probe, and a home for future desktop status
# (clock is left to the dock).
#
# Gated behind Compositor::TRAY so `main` stays shippable during the live
# co-edit — flip it to false to disable every register/draw/click hook below
# with zero other changes.
# ---------------------------------------------------------------------------
require "widgets"
require "base64"

class Compositor
  # Master switch for the whole tray feature. `false` makes every
  # register/draw/click hook below a no-op, so the compositor behaves exactly as
  # it did before this feature landed.
  TRAY = true

  # Status-bar plate behind the icon strip: a translucent dark bar (like a
  # floating menu-bar segment) with a hairline border, so the widget-drawn
  # glyphs read against a consistent surface instead of straight over the
  # desktop grid. TRAY_PAD insets the plate around the cells.
  TRAY_PLATE  = "rgba(42, 45, 58, 0.92)" # a lifted surface (Theme::TITLE_INACTIVE)
  TRAY_BORDER = "rgba(90, 96, 114, 0.95)" # Theme::RESIZE_GRIP-ish hairline
  TRAY_PAD    = 5

  # The tray model (05_tray.rb, which loads before this file). Built lazily so
  # Compositor#initialize (06_core.rb) needs no edit and pure rbtest — which
  # never loads this file — is unaffected. The compositor-owned indicators are
  # seeded on first access so the strip is never empty.
  def tray
    @tray ||= begin
      t = TrayArea.new
      register_builtin_indicators(t)
      t
    end
  end

  # Seed the two compositor-owned status indicators. Both paint a stock go-widgets
  # glyph and carry a small Ruby menu dispatched through the existing menu action
  # tuples (dispatch_menu_action), so a right-click (or left-click) opens a real
  # compositor menu with no client involved.
  def register_builtin_indicators(t)
    t.add("sys.settings", {
      glyph:   "settings",
      tooltip: "System settings",
      menu: [
        { label: "Preferences",    action: [:launch, "settings"] },
        { label: "About wasmbox",  action: [:noop, "about"] },
      ],
    })
    t.add("sys.search", {
      glyph:   "search",
      tooltip: "Search",
      menu: [
        { label: "Open Files", action: [:launch, "files"] },
      ],
    })
    t
  end

  # Register (or update) a tray item from a decoded `tray_add` wire message.
  # `worker` is the posting client's worker ref (nil for a compositor-owned
  # indicator / the test hook). An empty `glyph` string means "no stock glyph"
  # (fall back to the base64 image); a missing id is dropped. No-op when the flag
  # is off.
  def add_tray_item(msg, worker)
    return nil unless TRAY
    id = msg[:id].to_s
    return nil if id.empty?
    g = msg[:glyph].to_s
    tray.add(id, {
      owner:        worker,
      owner_window: msg[:window_id],
      glyph:        g.empty? ? nil : g,
      icon:         msg[:icon],
      w:            msg[:w],
      h:            msg[:h],
      tooltip:      msg[:tooltip].to_s,
      menu:         nil, # app menus are forwarded (tray_menu), not rendered here
    })
  end

  # Update a live tray item from a decoded `tray_update` message (partial fields).
  # No-op when the flag is off or the id is unknown.
  def update_tray_item(msg)
    return nil unless TRAY
    id = msg[:id].to_s
    return nil if id.empty?
    g = msg[:glyph]
    tray.update(id, {
      glyph:   g.nil? ? nil : (g.to_s.empty? ? nil : g.to_s),
      icon:    msg[:icon],
      w:       msg[:w],
      h:       msg[:h],
      tooltip: msg[:tooltip],
    })
  end

  # Remove a tray item from a decoded `tray_remove` message. No-op when the flag
  # is off. Returns the removed item (or nil).
  def remove_tray_item(msg)
    return nil unless TRAY
    id = msg[:id].to_s
    return nil if id.empty?
    tray.remove(id)
  end

  # Drop every tray item posted by a window that is going away (its client
  # closed). Called from the close arms of route_worker_message. No-op when the
  # flag is off or the closing window owned no tray items.
  def drop_tray_for_window(win_id)
    return nil unless TRAY
    tray.remove_for_window(win_id)
    nil
  end

  # Draw the tray strip as a top overlay, left of the toast column. Each item's
  # pixels are built ONCE (Widgets.status_area + status_icon) and cached; later
  # frames re-present the JS-side ImageData (empty base64) at the item's current
  # position, at one drawImage per item (the HUD's steady-state cost). Called in
  # Compositor#render alongside the toast draw so both overlays sit above every
  # other stratum. No-op when the flag is off or the strip is empty.
  def draw_tray
    return nil unless TRAY
    t = tray
    return nil if t.empty?
    icon = TrayArea::ICON
    rows = t.layout(@width)
    # Status-bar plate spanning all cells (rightmost cell's right edge to the
    # leftmost cell's left edge), padded. Painted first so the icons composite
    # over it. rows are right-to-left, so rows.first is the rightmost.
    right_edge = rows.first[:x] + icon
    left_edge  = rows.last[:x]
    plate = [left_edge - TRAY_PAD, TrayArea::MARGIN - TRAY_PAD,
             (right_edge - left_edge) + 2 * TRAY_PAD, icon + 2 * TRAY_PAD]
    fill_rect(plate, TRAY_PLATE)
    stroke_rect(plate, TRAY_BORDER, 1)
    rows.each do |r|
      it = r[:item]
      if it.b64.nil?
        it.b64 = render_tray_icon(it, icon)
        it.blitted = false
      end
      if it.blitted
        JS.global.call("wasmboxBlitRGBAOver", @ctx, "", icon, icon, r[:x], r[:y], it.key)
      else
        JS.global.call("wasmboxBlitRGBAOver", @ctx, it.b64, icon, icon, r[:x], r[:y], it.key)
        it.blitted = true
      end
    end
  end

  # Build one tray cell's RGBA buffer (base64) via the go-widgets DE overlay set.
  # A stock-glyph item paints Widgets.status_icon; an image item paints
  # Widgets.status_icon_image over its base64 RGBA. The icon is added to a fresh
  # status_area so the cell chrome (the tray slot's square) is drawn too; Render
  # sizes the area to the whole (side, side) buffer. The click/menu is handled
  # Ruby-side (see tray_activate / open_tray_menu), so the widget's own callbacks
  # are left empty.
  def render_tray_icon(it, side)
    area = Widgets.status_area
    handle = if it.glyph?
      Widgets.status_icon(it.glyph.to_s, it.tooltip, "", "")
    elsif !it.icon.nil? && !it.w.nil? && !it.h.nil?
      Widgets.status_icon_image(it.icon, it.w.to_i, it.h.to_i, it.tooltip, "", "")
    else
      # No glyph and no usable image: fall back to a neutral stock glyph so a
      # malformed add still paints a visible (clickable) slot rather than blank.
      Widgets.status_icon("settings", it.tooltip, "", "")
    end
    Widgets.add_widget(area, handle)
    img = Widgets.render(area, side, side)
    Base64.strict_encode64(img["pixels"])
  end

  # The tray item under (px, py), or nil. Used by on_mousedown / on_contextmenu
  # to route a click on the strip. No-op (nil) when the flag is off or empty.
  def tray_at(px, py)
    return nil unless TRAY
    return nil if tray.empty?
    tray.at(px, py, @width)
  end

  # A LEFT click landed on a tray item. A compositor-owned indicator opens its
  # own menu at the cursor; an app item posts a `tray_clicked` event to its
  # owner. (px, py) place the built-in menu.
  def tray_activate(it, px, py)
    if it.compositor_owned?
      open_tray_menu(it, px, py)
    else
      fire_tray_event(it, "tray_clicked")
    end
  end

  # A RIGHT click landed on a tray item. A compositor-owned indicator with a Ruby
  # menu opens it through the existing compositor menu path (so it shares the
  # menu draw + hit-test + dispatch); an app item posts a `tray_menu` event so
  # the client can respond (e.g. re-post its own tray state or open a popup).
  def open_tray_menu(it, px, py)
    if it.menu.is_a?(Array) && !it.menu.empty?
      menu = Menu.new(it.menu)
      @menu = { kind: :root, x: px, y: py, menu: menu, hover: -1,
                submenu_idx: -1, submenu: nil, submenu_x: 0, submenu_y: 0,
                submenu_hover: -1 }
    else
      fire_tray_event(it, "tray_menu")
    end
  end

  # Post a tray event back to the client that owns the item, as an `input` event
  # carrying the tray id. Routed by the owner's window_id so the SDK's onInput
  # (keyed by surface) delivers it. No-op for a compositor-owned item (no worker)
  # or one with no owner window.
  def fire_tray_event(it, kind)
    return nil if it.owner.nil?
    return nil if it.owner_window.nil?
    payload = JS.global.call("wasmboxMakeObject",
      "type", "input",
      "window_id", it.owner_window,
      "event", JS.global.call("wasmboxMakeObject",
        "kind", kind,
        "tray_id", it.id))
    JS.global.call("wasmboxPostMessage", it.owner, payload)
  end
end

# ---------------------------------------------------------------------------
# go-ruby-widgets/widgets notes hit while wiring this (for the toolkit backlog):
#   * StatusIcon has no BADGE overlay bound here — the toolkit StatusArea/
#     StatusIcon carries a badge slot (notification counts), but the wasmbox
#     protocol exposes only glyph|image + tooltip so far. A `tray_update` badge
#     field is the natural next step (numbers on the mail/chat indicator).
#   * The tray forwards a right-click as a `tray_menu` event and lets the client
#     drive the response; a future path could accept a serialized menu on the
#     wire and open it compositor-side via Widgets.context_menu (already bound),
#     the way the built-in indicators use a Ruby menu today.
# ---------------------------------------------------------------------------
