# ---------------------------------------------------------------------------
class Frame
  # The default frame (overridden by the boot wire).
  @@current = nil
  # The FrameRegistry name that built the current frame. Used by the
  # root-menu Frame submenu to mark the active entry with "* ". Nil
  # until FrameRegistry[] or a manual assign_current call sets it.
  @@current_name = nil
  def self.current
    @@current ||= OpenboxFrame.new
  end
  def self.current=(c) ; @@current = c ; end
  # Named-set helper: swap the current frame + remember which registry
  # name built it. Used by FrameRegistry[] and by the root-menu :frame
  # dispatch. Direct Frame.current= callers stay compatible — they just
  # leave @@current_name unset (falls back to the class-name heuristic).
  def self.assign_current(name, instance)
    @@current = instance
    @@current_name = name.to_s
    instance
  end
  def self.current_name
    return @@current_name unless @@current_name.nil?
    # Fallback: infer from the class name of whichever instance is set.
    # "OpenboxFrame" -> "openbox", "AquaFrame" -> "aqua".
    cls = self.current.class.name.to_s
    return "openbox" if cls == "OpenboxFrame"
    return "aqua"    if cls == "AquaFrame"
    cls
  end

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

# ThemedOpenboxFrame — same geometry as OpenboxFrame but reads its
# titlebar/border colours from a palette Hash (passed at construction).
# This is the "OpenboxFrame painted with a GTK theme palette" — pair
# any of the shipped PALETTES below with this class to get a Juno-
# coloured Openbox decoration, an Adwaita-coloured Openbox, etc.
#
# Palette shape (keys are Ruby Symbols; missing keys fall back to the
# parent OpenboxFrame defaults):
#   :title_active   — titlebar bg when focused
#   :title_inactive — titlebar bg when unfocused
#   :title_text     — title text ink
#   :close_bg       — close + minimize box face
#   :close_glyph    — × / _ ink
#   :border_active  — frame border when focused
#   :border_inactive
#   :resize_grip    — grip stroke
class ThemedOpenboxFrame < OpenboxFrame
  def initialize(palette = {})
    @palette = palette || {}
  end

  def paint(ctx, win, active, host)
    p = @palette
    title_bg = active ? (p[:title_active] || Theme::TITLE_ACTIVE) : (p[:title_inactive] || Theme::TITLE_INACTIVE)
    close_bg = p[:close_bg] || Theme::CLOSE_BG
    close_glyph = p[:close_glyph] || Theme::CLOSE_GLYPH
    title_text = p[:title_text] || Theme::TITLE_TEXT
    host.fill_rect(titlebar_rect(win), title_bg)
    tx, ty, _tw, _th = titlebar_rect(win)
    host.text(win.title, tx + 6, ty + 15, title_text)
    cx, cy, cw, ch = close_rect(win)
    host.fill_rect(close_rect(win), close_bg)
    ctx.set("strokeStyle", close_glyph)
    ctx.set("lineWidth", 1.5)
    ctx.call("beginPath")
    ctx.call("moveTo", cx + 3, cy + 3)
    ctx.call("lineTo", cx + cw - 3, cy + ch - 3)
    ctx.call("moveTo", cx + cw - 3, cy + 3)
    ctx.call("lineTo", cx + 3, cy + ch - 3)
    ctx.call("stroke")
    mx, my, mw, mh = minimize_rect(win)
    host.fill_rect(minimize_rect(win), close_bg)
    ctx.set("strokeStyle", active ? close_glyph : (p[:border_inactive] || Theme::BORDER_INACTIVE))
    ctx.set("lineWidth", 1.5)
    ctx.call("beginPath")
    ctx.call("moveTo", mx + 3,      my + mh - 4)
    ctx.call("lineTo", mx + mw - 3, my + mh - 4)
    ctx.call("stroke")
  end

  def paint_frame(ctx, win, active, host)
    p = @palette
    rx, ry, rw, rh = resize_rect(win)
    ctx.set("strokeStyle", p[:resize_grip] || Theme::RESIZE_GRIP)
    ctx.set("lineWidth", 1)
    ctx.call("beginPath")
    ctx.call("moveTo", rx + rw, ry + rh * 0.4)
    ctx.call("lineTo", rx + rw * 0.4, ry + rh)
    ctx.call("moveTo", rx + rw, ry + rh * 0.75)
    ctx.call("lineTo", rx + rw * 0.75, ry + rh)
    ctx.call("stroke")
    border = active ? (p[:border_active] || Theme::BORDER_ACTIVE) : (p[:border_inactive] || Theme::BORDER_INACTIVE)
    host.stroke_rect(frame_rect(win), border, Theme::BORDER)
  end
end

# ThemedAquaFrame — Aqua geometry (3 left-anchored traffic-lights,
# 28-px titlebar) painted with a palette. Useful for pairing a macOS-
# style chrome layout with a GTK theme's colours (e.g. WhiteSur
# Light, where the upstream theme IS already macOS-shaped).
#
# Palette keys recognised:
#   :title_active   — titlebar bg
#   :title_inactive
#   :title_border   — 1 px bottom hairline
#   :title_text_on  — title text when active
#   :title_text_off
#   :border_active  — frame stroke when active
#   :border_inactive
#   :resize_grip
#   :shadow         — faked drop-shadow band
# Traffic-light colours stay Aqua-canonical (red/yellow/green) — they
# are a recognisable macOS UI affordance, NOT a theme variable.
class ThemedAquaFrame < AquaFrame
  def initialize(palette = {})
    @palette = palette || {}
  end

  def paint(ctx, win, active, host)
    p = @palette
    tx, ty, tw, _th = titlebar_rect(win)
    host.fill_rect(titlebar_rect(win), active ? (p[:title_active] || TITLE_ACTIVE) : (p[:title_inactive] || TITLE_INACTIVE))
    ctx.set("strokeStyle", p[:title_border] || TITLE_BORDER)
    ctx.set("lineWidth", 1)
    ctx.call("beginPath")
    ctx.call("moveTo", tx,        ty + TITLE_H - 0.5)
    ctx.call("lineTo", tx + tw,   ty + TITLE_H - 0.5)
    ctx.call("stroke")
    ctx.set("font", "12px -apple-system, BlinkMacSystemFont, 'SF Pro Text', 'Helvetica Neue', Helvetica, Arial, sans-serif")
    ctx.set("textAlign", "center")
    ctx.set("textBaseline", "middle")
    ctx.set("fillStyle", active ? (p[:title_text_on] || TITLE_TEXT_ON) : (p[:title_text_off] || TITLE_TEXT_OFF))
    ctx.call("fillText", win.title, tx + tw / 2, ty + TITLE_H / 2 + 1)
    ctx.set("textAlign", "left")
    ctx.set("textBaseline", "alphabetic")
    draw_traffic_light(ctx, close_rect(win),    CLOSE_RED,  CLOSE_RED_OUT,  active)
    draw_traffic_light(ctx, minimize_rect(win), MIN_YELLOW, MIN_YELLOW_OUT, active)
    draw_traffic_light(ctx, maximize_rect(win), MAX_GREEN,  MAX_GREEN_OUT,  active)
  end

  def paint_frame(ctx, win, active, host)
    p = @palette
    rx, ry, rw, rh = resize_rect(win)
    ctx.set("strokeStyle", p[:resize_grip] || RESIZE_GRIP)
    ctx.set("lineWidth", 1)
    ctx.call("beginPath")
    ctx.call("moveTo", rx + rw, ry + rh * 0.4)
    ctx.call("lineTo", rx + rw * 0.4, ry + rh)
    ctx.call("moveTo", rx + rw, ry + rh * 0.75)
    ctx.call("lineTo", rx + rw * 0.75, ry + rh)
    ctx.call("stroke")
    fx, fy, fw, fh = frame_rect(win)
    shadow = p[:shadow] || SHADOW
    host.fill_rect([fx + fw,     fy + 1, 1, fh], shadow)
    host.fill_rect([fx + 1, fy + fh,     fw, 1], shadow)
    border = active ? (p[:border_active] || BORDER_ACTIVE) : (p[:border_inactive] || BORDER_INACTIVE)
    host.stroke_rect(frame_rect(win), border, BORDER_W)
  end
end

# PALETTES — known GTK-theme palettes mirrored from the showcase's
# clients/showcase/internal/scene/themes/*.css fixtures. Each entry
# is hand-resolved from the upstream SCSS (so the compositor does not
# need a CSS parser of its own; if you add a theme, also add an entry
# here). Each palette is shape-compatible with BOTH ThemedOpenboxFrame
# and ThemedAquaFrame — pick the layout via the frame registry name.
module PALETTES
  ADWAITA_LIGHT = {
    title_active: "#ebebeb", title_inactive: "#fafafa",
    title_text: "#1a1a1a", title_text_on: "#1a1a1a", title_text_off: "#80808a",
    title_border: "#dcdcdc",
    close_bg: "#f5f5f5", close_glyph: "#1a1a1a",
    border_active: "#3584e4", border_inactive: "#dcdcdc",
    resize_grip: "#80808a", shadow: "#cccccc",
  }
  ADWAITA_DARK = {
    title_active: "#303030", title_inactive: "#242424",
    title_text: "#ffffff", title_text_on: "#ffffff", title_text_off: "#888888",
    title_border: "#1a1a1a",
    close_bg: "#404040", close_glyph: "#ffffff",
    border_active: "#3584e4", border_inactive: "#1a1a1a",
    resize_grip: "#999999", shadow: "#000000",
  }
  JUNO = {
    title_active: "#1A2429", title_inactive: "#131C20",
    title_text: "#fefefe", title_text_on: "#fefefe", title_text_off: "#888888",
    title_border: "#131C20",
    close_bg: "#213036", close_glyph: "#fefefe",
    border_active: "#00A9A5", border_inactive: "#131C20",
    resize_grip: "#888888", shadow: "#000000",
  }
  WHITESUR_LIGHT = {
    title_active: "#ebebeb", title_inactive: "#f5f5f5",
    title_text: "#242424", title_text_on: "#242424", title_text_off: "#80808a",
    title_border: "#cccccc",
    close_bg: "#fbfbfb", close_glyph: "#242424",
    border_active: "#0860F2", border_inactive: "#cccccc",
    resize_grip: "#80808a", shadow: "#bbbbbb",
  }
  WHITESUR_DARK = {
    title_active: "#1f1f1f", title_inactive: "#333333",
    title_text: "#dedede", title_text_on: "#dedede", title_text_off: "#888888",
    title_border: "#0f0f0f",
    close_bg: "#2c2c2c", close_glyph: "#dedede",
    border_active: "#0860F2", border_inactive: "#0f0f0f",
    resize_grip: "#888888", shadow: "#000000",
  }
  SOLARIZED_LIGHT = {
    title_active: "#eee8d5", title_inactive: "#fdf6e3",
    title_text: "#073642", title_text_on: "#073642", title_text_off: "#586e75",
    title_border: "#93a1a1",
    close_bg: "#fdf6e3", close_glyph: "#073642",
    border_active: "#268bd2", border_inactive: "#93a1a1",
    resize_grip: "#586e75", shadow: "#93a1a1",
  }
  SOLARIZED_DARK = {
    title_active: "#073642", title_inactive: "#002b36",
    title_text: "#eee8d5", title_text_on: "#eee8d5", title_text_off: "#586e75",
    title_border: "#586e75",
    close_bg: "#073642", close_glyph: "#eee8d5",
    border_active: "#268bd2", border_inactive: "#586e75",
    resize_grip: "#93a1a1", shadow: "#000000",
  }
end

# FrameRegistry — name → frame instance lookup. Picked at boot via the
# WASMBOX_FRAME env var (see compositor boot at the bottom of this file).
# Adding a new frame is one map entry below; users can also assign
# Frame.current = MyFrame.new directly to plug a custom subclass without
# touching the registry.
#
# The "<layout>-<palette>" entries combine the 2 base layouts (openbox /
# aqua) with the 7 in-tree palettes — 14 combos total + the 2 plain
# defaults = 16 frames the user can pick from a ?frame= URL param.
module FrameRegistry
  TABLE = {
    "openbox"                 => -> { OpenboxFrame.new },
    "aqua"                    => -> { AquaFrame.new },

    "openbox-adwaita-light"   => -> { ThemedOpenboxFrame.new(PALETTES::ADWAITA_LIGHT) },
    "openbox-adwaita-dark"    => -> { ThemedOpenboxFrame.new(PALETTES::ADWAITA_DARK) },
    "openbox-juno"            => -> { ThemedOpenboxFrame.new(PALETTES::JUNO) },
    "openbox-whitesur-light"  => -> { ThemedOpenboxFrame.new(PALETTES::WHITESUR_LIGHT) },
    "openbox-whitesur-dark"   => -> { ThemedOpenboxFrame.new(PALETTES::WHITESUR_DARK) },
    "openbox-solarized-light" => -> { ThemedOpenboxFrame.new(PALETTES::SOLARIZED_LIGHT) },
    "openbox-solarized-dark"  => -> { ThemedOpenboxFrame.new(PALETTES::SOLARIZED_DARK) },

    "aqua-adwaita-light"      => -> { ThemedAquaFrame.new(PALETTES::ADWAITA_LIGHT) },
    "aqua-adwaita-dark"       => -> { ThemedAquaFrame.new(PALETTES::ADWAITA_DARK) },
    "aqua-juno"               => -> { ThemedAquaFrame.new(PALETTES::JUNO) },
    "aqua-whitesur-light"     => -> { ThemedAquaFrame.new(PALETTES::WHITESUR_LIGHT) },
    "aqua-whitesur-dark"      => -> { ThemedAquaFrame.new(PALETTES::WHITESUR_DARK) },
    "aqua-solarized-light"    => -> { ThemedAquaFrame.new(PALETTES::SOLARIZED_LIGHT) },
    "aqua-solarized-dark"     => -> { ThemedAquaFrame.new(PALETTES::SOLARIZED_DARK) },
  }
  def self.[](name)
    builder = TABLE[name.to_s]
    builder ? builder.call : OpenboxFrame.new
  end
  # Convenience wrapper: build the named frame + register it as
  # Frame.current under that name (so Frame.current_name returns the
  # registry key + the root-menu Frame submenu can mark the active
  # entry with "* "). Boot code + the :frame dispatch use this.
  def self.select(name)
    Frame.assign_current(name, self[name])
  end
  def self.names ; TABLE.keys ; end
end

# ---------------------------------------------------------------------------
# Window — a client surface plus its decoration geometry and hit-testing.
# Pure data + math; no JS here.
