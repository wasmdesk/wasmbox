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
# translucent HUD needs. Grid lines are 1px FillRects — visually identical to a
# 1px hairline stroke, and backend-portable.
# ---------------------------------------------------------------------------
require "widgets"
require "base64"

class Compositor
  # Master switch for selectable WALLPAPERS (gradient presets + a bundled
  # procedural image) layered on the widgets desktop background (2026-08-03,
  # the first Batch-3 / desktop-layer feature). When false, draw_desktop_widgets
  # always paints the grid Backdrop regardless of Wallpaper.current — the
  # shippable fallback during the live co-edit (flip to false to revert to the
  # grid-only desktop with zero other changes). The Wallpaper submenu + the
  # set_wallpaper protocol stay wired either way; they just have no visible
  # effect while this is off.
  WALLPAPER = true

  # The desktop grid pitch, matching the raw-ctx path's 40px step.
  DESKTOP_GRID_STEP = 40

  # Source dimensions of the bundled procedural wallpaper image. Deliberately
  # small: "fill" mode upscales it to the whole viewport, and the pixels are
  # generated + base64-encoded ONCE (memoised in @wallpaper_image_b64), so the
  # desktop never pays a per-frame image cost (the Firefox-GC lesson).
  WALLPAPER_IMG_W = 96
  WALLPAPER_IMG_H = 60

  # Paint the desktop background via the widgets binding. Mirrors draw_desktop's
  # full-canvas fill + grid by default; when Compositor::WALLPAPER is on it can
  # instead paint the active Wallpaper preset (a vertical gradient or the
  # bundled image), still through the same render-once cache.
  #
  # Render-once cache: the buffer only changes when the canvas size, the desktop
  # palette, OR the active wallpaper does, so we memoise on that signature and
  # otherwise re-present the JS-side cached ImageData (empty base64) — one
  # putImageData per frame, no per-tick render/base64/ImageData allocation. The
  # gradient / image widgets fill the whole w×h buffer opaquely, so the desktop
  # stays the bottom, overwrite-blitted layer (the dock, windows, menu + HUD all
  # composite ON TOP unchanged).
  def draw_desktop_widgets
    w = @width
    h = @height
    wp   = WALLPAPER ? Wallpaper.current : nil
    kind = wp ? wp[:kind] : :grid
    wpid = WALLPAPER ? Wallpaper.current_name : "Grid"
    sig = "#{w}x#{h}|#{Theme::DESKTOP}|#{Theme::DESKTOP_GRID}|#{DESKTOP_GRID_STEP}|#{wpid}"
    if @desktop_sig == sig
      JS.global.call("wasmboxBlitRGBA", @ctx, "", w, h, 0, 0, "desktop")
      return
    end

    handle = wallpaper_handle(kind, wp)
    Widgets.layout(handle, w, h)
    img = Widgets.render(handle, w, h)
    b64 = Base64.strict_encode64(img["pixels"])
    @desktop_sig = sig
    JS.global.call("wasmboxBlitRGBA", @ctx, b64, w, h, 0, 0, "desktop")
  end

  # Build the widgets handle for the active desktop background:
  #   :gradient -> Widgets.wallpaper_gradient(top, bottom) — a vertical gradient
  #   :image    -> Widgets.wallpaper(bundled base64, w, h, mode) — the bundled
  #                procedural image, scaled by the preset's mode ("fill")
  #   else      -> Widgets.backdrop — the faint-grid ground (default + fallback)
  def wallpaper_handle(kind, wp)
    if kind == :gradient
      Widgets.wallpaper_gradient(wp[:top], wp[:bottom])
    elsif kind == :image
      mode = wp[:mode].nil? ? "fill" : wp[:mode]
      Widgets.wallpaper(wallpaper_image_b64, WALLPAPER_IMG_W, WALLPAPER_IMG_H, mode)
    else
      Widgets.backdrop(Theme::DESKTOP, Theme::DESKTOP_GRID, DESKTOP_GRID_STEP)
    end
  end

  # The bundled wallpaper image as a base64 RGBA string, generated ONCE and
  # memoised so switching to the "Photo" preset costs a single render (never a
  # per-frame regeneration). A procedural diagonal indigo→teal gradient with a
  # soft diagonal ripple — no external asset, no image decoder needed.
  def wallpaper_image_b64
    return @wallpaper_image_b64 unless @wallpaper_image_b64.nil?
    @wallpaper_image_b64 = Base64.strict_encode64(build_wallpaper_image)
  end

  # Generate the bundled image as raw RGBA bytes (WALLPAPER_IMG_W×H×4). Pure
  # integer math (no Math dependency): a diagonal blend between two dark desktop
  # tones, modulated by a triangle-wave ripple so the surface reads as a
  # textured image rather than a flat gradient.
  def build_wallpaper_image
    w  = WALLPAPER_IMG_W
    h  = WALLPAPER_IMG_H
    dm = w + h # diagonal span (max of x+y)
    # indigo (top-left) -> teal (bottom-right)
    r0 = 16;  g0 = 22;  b0 = 58
    r1 = 15;  g1 = 92;  b1 = 102
    buf = String.new
    y = 0
    while y < h
      x = 0
      while x < w
        num = x + y
        r = r0 + (r1 - r0) * num / dm
        g = g0 + (g1 - g0) * num / dm
        b = b0 + (b1 - b0) * num / dm
        # Triangle-wave ripple across the diagonal, period 24, amplitude ±12.
        ph = (x * 3 + y * 2) % 24
        tri = ph < 12 ? ph : 24 - ph
        d = (tri - 6) * 2
        r = clamp_byte(r + d)
        g = clamp_byte(g + d)
        b = clamp_byte(b + d)
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

  # Clamp an integer channel value into the 0..255 byte range.
  def clamp_byte(v)
    return 0 if v < 0
    return 255 if v > 255
    v
  end
end
