# ---------------------------------------------------------------------------
class Frame
  # The default frame (overridden by the boot wire).
  @@current = nil
  def self.current
    @@current ||= OpenboxFrame.new
  end
  def self.current=(c) ; @@current = c ; end

  # The window-geometry hooks. Default impls reuse Theme:: constants so a
  # chrome that wants the Openbox geometry can just inherit OpenboxFrame.

  # Titlebar height in pixels — read by Window#frame_top + Compositor#draw.
  def title_h ; Theme::TITLE_H ; end

  # Frame border width (the stroke around the whole window).
  def border_w ; Theme::BORDER ; end

  # Does this chrome show a maximize button? When false, Window#maximize_rect
  # returns a zero-sized rect + the compositor skips painting it.
  def has_maximize? ; false ; end

  # Per-button geometry hooks. Each receives the Window + returns
  # [x, y, w, h]. The default impls below are the Openbox geometry; the
  # AquaFrame overrides them to left-anchor the 3 traffic-light buttons.
  def close_rect(win)
    return [win.x, win.y, 0, 0] unless win.decorated?
    pad = (title_h - Theme::CLOSE_SZ) / 2
    [win.right - Theme::CLOSE_SZ - pad, win.frame_top + pad, Theme::CLOSE_SZ, Theme::CLOSE_SZ]
  end

  def minimize_rect(win)
    return [win.x, win.y, 0, 0] unless win.decorated?
    pad = (title_h - Theme::MIN_SZ) / 2
    cx, cy, _cw, _ch = close_rect(win)
    [cx - Theme::MIN_SZ - pad, cy, Theme::MIN_SZ, Theme::MIN_SZ]
  end

  # No maximize in Openbox; AquaFrame overrides.
  def maximize_rect(win) ; [win.x, win.y, 0, 0] ; end

  # Resize grip + frame extents — shared across chromes.
  def resize_rect(win)
    return [win.x, win.y, 0, 0] unless win.decorated?
    return [win.x, win.y, 0, 0] if win.shaded
    [win.right - Theme::GRIP, win.bottom - Theme::GRIP, Theme::GRIP, Theme::GRIP]
  end

  def titlebar_rect(win)
    win.decorated? ? [win.x, win.frame_top, win.w, title_h] : [win.x, win.y, 0, 0]
  end

  def frame_rect(win)
    return [win.x, win.y, win.w, win.h] unless win.decorated?
    return [win.x, win.frame_top, win.w, title_h] if win.shaded
    [win.x, win.frame_top, win.w, win.h + title_h]
  end

  # paint(ctx, win, active, host) is the chrome's visual; it owns all the
  # ctx draw calls for the titlebar, buttons, border + resize grip. `host`
  # is the Compositor (used for fill_rect/stroke_rect helpers + the @ctx).
  # Implemented per subclass.
  def paint(_ctx, _win, _active, _host) ; raise NotImplementedError ; end
end

# OpenboxFrame — the existing wasmbox look. Geometry inherits Chrome
# defaults; paint reproduces what Compositor#draw_window used to inline.
class OpenboxFrame < Frame
  def paint(ctx, win, active, host)
    # Titlebar.
    host.fill_rect(titlebar_rect(win), active ? Theme::TITLE_ACTIVE : Theme::TITLE_INACTIVE)
    tx, ty, _tw, _th = titlebar_rect(win)
    host.text(win.title, tx + 6, ty + 15, Theme::TITLE_TEXT)
    # Close box (× glyph).
    cx, cy, cw, ch = close_rect(win)
    host.fill_rect(close_rect(win), Theme::CLOSE_BG)
    ctx.set("strokeStyle", Theme::CLOSE_GLYPH)
    ctx.set("lineWidth", 1.5)
    ctx.call("beginPath")
    ctx.call("moveTo", cx + 3, cy + 3)
    ctx.call("lineTo", cx + cw - 3, cy + ch - 3)
    ctx.call("moveTo", cx + cw - 3, cy + 3)
    ctx.call("lineTo", cx + 3, cy + ch - 3)
    ctx.call("stroke")
    # Minimize box ("_" glyph).
    mx, my, mw, mh = minimize_rect(win)
    host.fill_rect(minimize_rect(win), Theme::CLOSE_BG)
    ctx.set("strokeStyle", active ? Theme::CLOSE_GLYPH : Theme::BORDER_INACTIVE)
    ctx.set("lineWidth", 1.5)
    ctx.call("beginPath")
    ctx.call("moveTo", mx + 3,      my + mh - 4)
    ctx.call("lineTo", mx + mw - 3, my + mh - 4)
    ctx.call("stroke")
  end

  def paint_frame(ctx, win, active, host)
    # Resize grip + 1px frame border. Called by Compositor after the body
    # paints (so the grip + border land on top).
    rx, ry, rw, rh = resize_rect(win)
    ctx.set("strokeStyle", Theme::RESIZE_GRIP)
    ctx.set("lineWidth", 1)
    ctx.call("beginPath")
    ctx.call("moveTo", rx + rw, ry + rh * 0.4)
    ctx.call("lineTo", rx + rw * 0.4, ry + rh)
    ctx.call("moveTo", rx + rw, ry + rh * 0.75)
    ctx.call("lineTo", rx + rw * 0.75, ry + rh)
    ctx.call("stroke")
    host.stroke_rect(frame_rect(win), active ? Theme::BORDER_ACTIVE : Theme::BORDER_INACTIVE, Theme::BORDER)
  end
end

# AquaFrame — three traffic-light buttons on the LEFT, 28 px flat
# titlebar with centred title, 1 px bottom hairline. Verbatim port of
# the chrome from the sibling wasmaqua project (which becomes a pure
# preset of wasmbox once this lands).
class AquaFrame < Frame
  # Aqua-specific colour + size table — kept on the chrome rather than in
  # the top-level Theme module so adding more chromes doesn't pollute the
  # Openbox baseline.
  TITLE_H        = 28
  BTN_R          = 6
  BTN_DIAM       = BTN_R * 2
  BTN_GAP        = 8
  TITLE_ACTIVE   = "#ECECEC"
  TITLE_INACTIVE = "#E3E3E3"
  TITLE_BORDER   = "#BFBFBF"
  TITLE_TEXT_ON  = "#3C3C43"
  TITLE_TEXT_OFF = "#9B9B9F"
  CLOSE_RED      = "#FF5F57"
  CLOSE_RED_OUT  = "#E0443E"
  MIN_YELLOW     = "#FEBC2E"
  MIN_YELLOW_OUT = "#E0A12B"
  MAX_GREEN      = "#28C840"
  MAX_GREEN_OUT  = "#23A93A"
  BORDER_ACTIVE  = "#A9A9A9"
  BORDER_INACTIVE= "#CFCFCF"
  RESIZE_GRIP    = "#9B9B9F"
  SHADOW         = "#999999"
  BORDER_W       = 1

  def title_h ; TITLE_H ; end
  def border_w ; BORDER_W ; end
  def has_maximize? ; true ; end

  def close_rect(win)
    return [win.x, win.y, 0, 0] unless win.decorated?
    cy = win.frame_top + (TITLE_H - BTN_DIAM) / 2
    [win.x + BTN_GAP, cy, BTN_DIAM, BTN_DIAM]
  end

  def minimize_rect(win)
    return [win.x, win.y, 0, 0] unless win.decorated?
    cx, cy, _cw, _ch = close_rect(win)
    [cx + BTN_DIAM + BTN_GAP, cy, BTN_DIAM, BTN_DIAM]
  end

  def maximize_rect(win)
    return [win.x, win.y, 0, 0] unless win.decorated?
    cx, cy, _cw, _ch = minimize_rect(win)
    [cx + BTN_DIAM + BTN_GAP, cy, BTN_DIAM, BTN_DIAM]
  end

  def paint(ctx, win, active, host)
    tx, ty, tw, _th = titlebar_rect(win)
    host.fill_rect(titlebar_rect(win), active ? TITLE_ACTIVE : TITLE_INACTIVE)
    # 1 px hairline at the bottom of the titlebar.
    ctx.set("strokeStyle", TITLE_BORDER)
    ctx.set("lineWidth", 1)
    ctx.call("beginPath")
    ctx.call("moveTo", tx,        ty + TITLE_H - 0.5)
    ctx.call("lineTo", tx + tw,   ty + TITLE_H - 0.5)
    ctx.call("stroke")
    # Centred title text.
    ctx.set("font", "12px -apple-system, BlinkMacSystemFont, 'SF Pro Text', 'Helvetica Neue', Helvetica, Arial, sans-serif")
    ctx.set("textAlign", "center")
    ctx.set("textBaseline", "middle")
    ctx.set("fillStyle", active ? TITLE_TEXT_ON : TITLE_TEXT_OFF)
    ctx.call("fillText", win.title, tx + tw / 2, ty + TITLE_H / 2 + 1)
    ctx.set("textAlign", "left")
    ctx.set("textBaseline", "alphabetic")
    # Three traffic-light buttons.
    draw_traffic_light(ctx, close_rect(win),    CLOSE_RED,  CLOSE_RED_OUT,  active)
    draw_traffic_light(ctx, minimize_rect(win), MIN_YELLOW, MIN_YELLOW_OUT, active)
    draw_traffic_light(ctx, maximize_rect(win), MAX_GREEN,  MAX_GREEN_OUT,  active)
  end

  def paint_frame(ctx, win, active, host)
    rx, ry, rw, rh = resize_rect(win)
    ctx.set("strokeStyle", RESIZE_GRIP)
    ctx.set("lineWidth", 1)
    ctx.call("beginPath")
    ctx.call("moveTo", rx + rw, ry + rh * 0.4)
    ctx.call("lineTo", rx + rw * 0.4, ry + rh)
    ctx.call("moveTo", rx + rw, ry + rh * 0.75)
    ctx.call("lineTo", rx + rw * 0.75, ry + rh)
    ctx.call("stroke")
    # Faked 1 px drop shadow on the right + bottom of the frame.
    fx, fy, fw, fh = frame_rect(win)
    host.fill_rect([fx + fw,     fy + 1, 1, fh], SHADOW)
    host.fill_rect([fx + 1, fy + fh,     fw, 1], SHADOW)
    host.stroke_rect(frame_rect(win), active ? BORDER_ACTIVE : BORDER_INACTIVE, BORDER_W)
  end

  # (rbgo's mruby has no `private` keyword; helpers below are
  # convention-only — callers stay inside the class.)

  def draw_traffic_light(ctx, rect, fill_colour, outline_colour, active)
    bx, by, bw, bh = rect
    cx = bx + bw / 2.0
    cy = by + bh / 2.0
    fc = active ? fill_colour    : "#C7C7CC"
    oc = active ? outline_colour : "#B0B0B5"
    ctx.set("fillStyle", fc)
    ctx.call("beginPath")
    ctx.call("arc", cx, cy, BTN_R, 0, 6.283185307179586)
    ctx.call("fill")
    ctx.set("strokeStyle", oc)
    ctx.set("lineWidth", 1)
    ctx.call("beginPath")
    ctx.call("arc", cx, cy, BTN_R - 0.5, 0, 6.283185307179586)
    ctx.call("stroke")
  end
end

# FrameRegistry — name → chrome instance lookup. Picked at boot via the
# WASMBOX_FRAME env var (see compositor boot at the bottom of this file).
# Adding a new chrome is one map entry below; users can also assign
# Frame.current = MyChrome.new directly to plug a custom subclass without
# touching the registry.
module FrameRegistry
  TABLE = {
    "openbox" => -> { OpenboxFrame.new },
    "aqua"    => -> { AquaFrame.new },
  }
  def self.[](name)
    builder = TABLE[name.to_s]
    builder ? builder.call : OpenboxFrame.new
  end
  def self.names ; TABLE.keys ; end
end

# ---------------------------------------------------------------------------
# Window — a client surface plus its decoration geometry and hit-testing.
# Pure data + math; no JS here.
