# ---------------------------------------------------------------------------
# HUD via the go-widgets binding (2026-08-02, follows the menu pilot in
# 08_menu_widgets.rb).
#
# The compositor's status HUD — a single bottom-left line "rbgo compositor -
# N windows - X fps - frame F" — is painted through the `require "widgets"`
# binding instead of a raw ctx.fillText:
#
#   Widgets.label(line) -> Widgets.render(handle, w, h) -> RGBA bytes ->
#   base64 -> JS wasmboxBlitRGBAOver -> alpha-composited drawImage at the HUD
#   origin.
#
# Unlike the menu (an OPAQUE panel, blitted with putImageData), the HUD is
# translucent text over the live desktop, so it uses wasmboxBlitRGBAOver (a
# source-over drawImage) rather than the overwrite path — otherwise the HUD's
# transparent ground would punch a rectangle through the desktop.
#
# This exercises the toolkit's Label glyph rasteriser end-to-end in the wasm
# build. The compositor calls Widgets.use_opentype_text on the first frame, so
# the HUD line is shaped with the anti-aliased OpenType face (the em-dash
# separators are replaced with ASCII " - " for a stable monospace advance).
# ---------------------------------------------------------------------------
class Compositor
  # The HUD line embeds @fps + @frames, which tick every frame — re-rendering
  # (Widgets.render + base64) 60x/sec would defeat the JS-side cache and
  # reintroduce the Firefox-GC churn the pilot documents. So the buffer is
  # rebuilt at most once every HUD_REFRESH_FRAMES frames (~4x/sec at 60fps) and
  # the cached buffer is re-presented in between — the displayed frame counter
  # lags by at most that many frames, invisible for a debug HUD.
  HUD_REFRESH_FRAMES = 15

  # Paint the status HUD via the widgets binding. Mirrors draw_hud's content +
  # bottom-left placement; called from draw_hud when the flag is on.
  def draw_hud_widgets
    n = @wm.windows.length
    # @rendered_frames counts COMPOSITED frames (see draw_hud): with the
    # dirty-rect gate an idle desktop stops compositing, so the counter holds.
    line = "rbgo compositor - #{n} window#{n == 1 ? '' : 's'} - #{'%.0f' % @fps} fps - frame #{@rendered_frames}"
    # Bitmap font is monospace at a 6px advance; ASCII-only line so length ==
    # byte count. Clamp the buffer to the canvas width.
    w = line.length * 6 + 6
    w = @width if w > @width
    h = 14
    dx = 10
    dy = @height - h - 4

    @hud_frame = -HUD_REFRESH_FRAMES if @hud_frame.nil?
    # Re-present the cached buffer unless it is stale (age or a size change,
    # e.g. the window count / fps digit-count / a viewport resize).
    if (@frames - @hud_frame) < HUD_REFRESH_FRAMES && @hud_w == w && @hud_h == h
      JS.global.call("wasmboxBlitRGBAOver", @ctx, "", @hud_w, @hud_h, dx, dy, "hud")
      return
    end

    handle = Widgets.label(line)
    Widgets.layout(handle, w, h)
    img = Widgets.render(handle, w, h)
    b64 = Base64.strict_encode64(img["pixels"])
    @hud_frame = @frames
    @hud_w = w
    @hud_h = h
    JS.global.call("wasmboxBlitRGBAOver", @ctx, b64, w, h, dx, dy, "hud")
  end
end
