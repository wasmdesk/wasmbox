# ---------------------------------------------------------------------------
# PILOT (2026-08-02): render the desktop root / window context menu through the
# go-widgets `require "widgets"` binding instead of hand-drawing it on the 2D
# canvas. This proves the compositor render -> RGBA -> overlay seam on a small,
# self-contained, transient overlay:
#
#   Widgets.menu(entries) -> Widgets.render(handle, w, h) -> { "pixels" => RGBA
#   bytes, ... } -> base64 -> JS wasmboxBlitRGBA -> putImageData at the menu's
#   (x, y).
#
# Only the PAINT moves to widgets. The pure Ruby menu domain model (Menu /
# RootMenu in 05_menu.rb) and all hit-testing (menu_resolve / handle_menu_click
# / handle_menu_hover in 06_core.rb) are untouched, so the menu behaves
# identically — it just gets its pixels from the toolkit painter now.
#
# Requiring the binding here (at boot, after 07_boot.rb) doubles as the
# boot-time smoke test: if `require "widgets"` did not register in the wasm
# build the program would raise on load and wasmboxError would be set.
# ---------------------------------------------------------------------------
require "widgets"
require "base64"

# The toolkit's dark palette (Surface #1f2228 body / OnSurface #e6e7ee ink /
# Border #3a3e46 frame) lands within a couple of levels of the compositor's
# Theme::MENU_* colours, so the widget-painted menu reads the same as the
# canvas one. Set once at boot.
Widgets.set_theme("dark")

class Compositor
  # Apply the toolkit-widget row geometry to a menu the moment it opens, so the
  # Ruby click routing (menu_resolve / handle_menu_click / handle_menu_hover in
  # 06_core.rb) is driven by the SAME numbers the toolkit paints with — the
  # single source of truth for the menu's hit geometry. Every site that pops a
  # menu (root / window / tray in 06_core.rb + 13_tray.rb) and every site that
  # opens a submenu calls this before storing or hit-testing it. It just hands
  # the `Widgets` module to Menu#apply_widget_layout (which keeps the pure Menu
  # domain model free of the binding — see 05_menu.rb). Returns the menu.
  def layout_menu(menu)
    menu.apply_widget_layout(Widgets)
    menu
  end

  # Paint the open menu (and its open submenu, if any) via the widgets binding.
  # Mirrors draw_menu's two-panel structure. Called from draw_menu when the
  # flag is on.
  def draw_menu_widgets
    state = @menu
    blit_menu_panel("menu", state[:menu], state[:x], state[:y])
    if state[:submenu]
      blit_menu_panel("sub", state[:submenu], state[:submenu_x], state[:submenu_y])
    end
  end

  # Build a Widgets menu tree from `menu`'s entries, render it to an RGBA buffer
  # and putImageData it at (x, y) through the JS helper.
  #
  # Rendering is memoised per panel `key` on a content signature: the compositor
  # re-composites every rAF frame (draw_desktop repaints the background), but a
  # menu panel's pixels only change when its content does. When the signature is
  # unchanged we send an empty buffer and the JS helper re-presents its cached
  # ImageData — bounding the per-frame cost to one putImageData instead of a
  # Widgets.render + base64 + ImageData allocation (the Firefox-GC hazard the
  # SAB blit path documents), and incidentally bounding widget-handle growth.
  def blit_menu_panel(key, menu, x, y)
    @mw_sig = {} if @mw_sig.nil?
    w = Menu::WIDTH
    h = menu.height
    sig = menu_paint_sig(key, menu, w, h)
    if @mw_sig[key] == sig
      JS.global.call("wasmboxBlitRGBA", @ctx, "", w, h, x, y, key)
      return
    end
    handle = build_widget_menu(menu)
    Widgets.layout(handle, w, h)
    img = Widgets.render(handle, w, h)
    b64 = Base64.strict_encode64(img["pixels"])
    @mw_sig[key] = sig
    JS.global.call("wasmboxBlitRGBA", @ctx, b64, w, h, x, y, key)
  end

  # Build the toolkit menu widget for `menu` from its domain entries. The
  # entries → Widgets.menu item-hash translation lives on the pure Menu model
  # (Menu#to_widget_items in 05_menu.rb) so the painter here and the geometry
  # query (Menu#apply_widget_layout) build the IDENTICAL widget — the queried
  # row bands then line up exactly with these painted rows. See
  # Menu#to_widget_items for the marker/chevron conventions.
  def build_widget_menu(menu)
    Widgets.menu(menu.to_widget_items)
  end

  # A cheap content signature for a menu panel: its key, pixel size, and the
  # ordered labels (+ chevron / separator markers). Changes whenever the visible
  # content changes — a different submenu slides open, or the active theme/frame
  # "* " prefix moves — so the memoised render refreshes exactly when it must,
  # and reuses the JS-side ImageData cache across identical re-opens.
  def menu_paint_sig(key, menu, w, h)
    parts = [key, w.to_s, h.to_s]
    menu.entries.each do |e|
      if e[:separator]
        parts << "-"
      else
        parts << "#{e[:label]}#{e[:submenu] ? '>' : ''}"
      end
    end
    parts.join("|")
  end
end
