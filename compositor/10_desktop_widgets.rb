# ---------------------------------------------------------------------------
# Desktop background via the go-widgets binding (2026-08-02, follows the menu
# pilot in 08_menu_widgets.rb and the HUD in 09_hud_widgets.rb).
#
# The compositor's desktop background — a solid fill (#11131a) plus a faint
# 40px grid — is painted through the `require "widgets"` binding instead of a
# raw ctx.fillRect + a beginPath/moveTo/lineTo/stroke grid loop run every rAF
# frame:
#
#   Widgets.backdrop(fill, grid, step) -> Widgets.render(handle, w, h) -> RGBA
#   bytes -> base64 -> JS wasmboxBlitRGBA -> putImageData at (0, 0).
#
# The desktop fills the WHOLE canvas and is drawn FIRST every frame (the dock,
# windows, menu and HUD all composite ON TOP), so re-rendering it to RGBA each
# tick would allocate a full-screen buffer + base64 string 60x/sec — exactly
# the Firefox-GC churn the SAB blit path's seqlock cache guards against. So the
# buffer is rendered ONCE and cached: the Backdrop is re-rendered only when the
# canvas size or the desktop palette changes (a viewport resize or a theme
# swap), never per tick. In the steady state each frame costs one putImageData
# of the cached ImageData (the JS wasmboxBlitRGBA "desktop" key re-presents its
# cached buffer when handed an empty base64) — the same one-blit-per-frame cost
# the old fillRect path had, minus the grid stroke loop.
#
# The Backdrop is OPAQUE (it is the bottom layer), so it uses wasmboxBlitRGBA
# (putImageData-overwrite), like the menu — not the source-over path the
# translucent HUD needs.
#
# Gated behind Compositor::DESKTOP_WIDGETS so the raw-ctx draw_desktop path in
# 06_core.rb stays the shippable fallback during the live co-edit (flip to
# false to revert the paint with zero other changes).
#
# Known parity diffs (toolkit-painter, same family as the menu/HUD pilots):
#   * Grid lines are 1px FillRects at gx / gy rather than the raw path's
#     gx+0.5 hairline strokes — visually identical at 1px, backend-portable.
# ---------------------------------------------------------------------------
require "widgets"
require "base64"

class Compositor
  # Master switch for the widgets-painted desktop background. `false` falls
  # straight back to the raw-ctx draw_desktop path in 06_core.rb.
  DESKTOP_WIDGETS = true

  # The desktop grid pitch, matching the raw-ctx path's 40px step.
  DESKTOP_GRID_STEP = 40

  # Paint the desktop background via the widgets binding. Mirrors draw_desktop's
  # full-canvas fill + grid; called from draw_desktop when the flag is on.
  #
  # Render-once cache: the buffer only changes when the canvas size or the
  # desktop palette does, so we memoise on that signature and otherwise re-present
  # the JS-side cached ImageData (empty base64) — one putImageData per frame, no
  # per-tick render/base64/ImageData allocation.
  def draw_desktop_widgets
    w = @width
    h = @height
    sig = "#{w}x#{h}|#{Theme::DESKTOP}|#{Theme::DESKTOP_GRID}|#{DESKTOP_GRID_STEP}"
    if @desktop_sig == sig
      JS.global.call("wasmboxBlitRGBA", @ctx, "", w, h, 0, 0, "desktop")
      return
    end

    handle = Widgets.backdrop(Theme::DESKTOP, Theme::DESKTOP_GRID, DESKTOP_GRID_STEP)
    Widgets.layout(handle, w, h)
    img = Widgets.render(handle, w, h)
    b64 = Base64.strict_encode64(img["pixels"])
    @desktop_sig = sig
    JS.global.call("wasmboxBlitRGBA", @ctx, b64, w, h, 0, 0, "desktop")
  end
end
