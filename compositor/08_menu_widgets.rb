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

  # Translate a Menu's entries into the Ruby Array-of-Hashes the Widgets.menu
  # constructor accepts (keys: "label" / "separator" / "shortcut" / "action").
  #
  # Every selectable row carries a non-empty "action" marker so the toolkit
  # paints it enabled — the toolkit greys out action-less rows, and our submenu
  # parents (Applications / Workspaces / Theme / Frame) carry a :submenu rather
  # than an :action. The marker is never dispatched through the widget event
  # seam; click routing stays in Ruby. Submenu parents get a ">" shortcut as a
  # right-edge chevron approximation (the adapter's menu constructor exposes no
  # submenu/chevron field — see the pilot gap notes).
  def build_widget_menu(menu)
    items = []
    menu.entries.each do |e|
      if e[:separator]
        items << { "separator" => true }
      else
        item = { "label" => e[:label].to_s, "action" => "x" }
        item["shortcut"] = ">" if e[:submenu]
        items << item
      end
    end
    Widgets.menu(items)
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
