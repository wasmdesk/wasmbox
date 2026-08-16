# ---------------------------------------------------------------------------
# Window frame decorations via the go-widgets binding (2026-08-02, follows the
# menu pilot in 08_menu_widgets.rb, the HUD in 09_hud_widgets.rb and the desktop
# background in 10_desktop_widgets.rb).
#
# A decorated window's chrome — the titlebar (band + title + close/min/max
# buttons), the frame border, the faux drop shadow and the resize grip — is
# painted through the `require "widgets"` binding instead of the hand-drawn
# ctx calls in the Frame classes (02_frame.rb):
#
#   chrome.decoration_spec(win, active) -> Widgets.decoration(spec) ->
#   Widgets.render(handle, w, h) -> RGBA bytes -> base64 -> JS
#   wasmboxBlitRGBAOver -> alpha-composited drawImage AROUND the window body.
#
# The chrome spec (Frame#decoration_spec) expresses the chrome's colours +
# geometry as data, but the rects are FRAME-LOCAL (see Frame#frame_local) and
# the whole thing is rendered into one per-window buffer that spans the frame
# extent with a TRANSPARENT body hole. The body itself is untouched — it stays
# SAB-blitted (external), fill-painted (in-process) or an <iframe> (dom) — and
# the decoration composites OVER it (source-over, like the translucent HUD), so
# the body shows through the hole and the border + grip land on top of its edges.
#
# Hit-testing is UNCHANGED: Window#titlebar_rect / close_rect / minimize_rect /
# maximize_rect / resize_rect (all delegating to the current Frame) stay the
# single source of truth, so close / minimize / maximize / drag / resize / shade
# behave identically — only the pixels come from the toolkit painter now. The
# spec's frame-local rects are DERIVED from those same *_rect methods, so paint
# and hit-testing can never drift apart.
#
# Per-window render-once cache: the compositor re-composites every rAF frame
# (draw_desktop repaints the background), but a window's chrome pixels only
# change when its width/height, focus, title, frame style or shade state does —
# NOT when it merely moves (a drag re-presents the cached buffer at the new
# origin) or when its body repaints. So the buffer is memoised per window on a
# content signature and re-rendered ONLY on change; in between, an empty base64
# re-presents the JS-side cached OffscreenCanvas (one drawImage), bounding the
# per-frame cost to a single composite and avoiding the render + base64 +
# ImageData churn every tick (the Firefox-GC hazard the SAB blit path documents).
#
# The compositor calls Widgets.use_opentype_text on the first frame, so window
# titles shape with the anti-aliased OpenType face; the traffic-light / close-×
# / grip strokes are drawn by the toolkit painter (backend-portable, no canvas
# arc / stroke calls).
# ---------------------------------------------------------------------------
require "widgets"
require "base64"

class Compositor
  # Paint `win`'s decoration through the widgets binding, composited over the
  # already-painted body. Called from draw_window when the flag is on and the
  # window is decorated. `active` is win.focused?.
  def draw_window_frame_widgets(win, active)
    chrome = Frame.current
    title_h = chrome.title_h
    fw = win.w
    fh = win.shaded? ? title_h : title_h + win.h
    # Aqua paints a 1px faux drop shadow one unit past the frame's right + bottom
    # edges; widen the buffer to hold it. Openbox has none (margin 0), so its
    # extra column/row stays transparent and invisible.
    margin = win.shaded? ? 0 : 1
    bw = fw + margin
    bh = fh + margin
    dx = win.x
    dy = win.frame_top
    key = "frame#{win.id}"

    @frame_sig = {} if @frame_sig.nil?
    sig = "#{fw}x#{fh}|#{active}|#{win.shaded?}|#{Frame.current_name}|#{win.title}"
    if @frame_sig[key] == sig
      JS.global.call("wasmboxBlitRGBAOver", @ctx, "", bw, bh, dx, dy, key)
      return
    end

    handle = Widgets.decoration(chrome.decoration_spec(win, active))
    Widgets.layout(handle, bw, bh)
    img = Widgets.render(handle, bw, bh)
    b64 = Base64.strict_encode64(img["pixels"])
    @frame_sig[key] = sig
    JS.global.call("wasmboxBlitRGBAOver", @ctx, b64, bw, bh, dx, dy, key)
  end

  # Drop a closed window's cached frame signature so a later window reusing the
  # id re-renders instead of re-presenting a stale buffer. Called from the WM on
  # window removal.
  def forget_frame_cache(win_id)
    @frame_sig.delete("frame#{win_id}") if @frame_sig
  end
end
