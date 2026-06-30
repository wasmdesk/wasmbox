# A Wayland-inspired compositor with an Openbox-style window-manager policy,
# written in pure Ruby and rendered to a <canvas> through the interpreter's
# interactive JS bridge (see internal/vm/jsbridge_wasm.go).
#
# Step A keeps everything in ONE WASM instance: the compositor and every
# in-process Window live in the same Ruby program. Step B (this file) adds an
# EXTERNAL CLIENT path on top: ExternalWindow + WindowManager#spawn_external
# attach a Web Worker carrying its own wasm instance, which writes pixels into
# a SharedArrayBuffer and posts {type:"commit",...} to flush damage rectangles.
# The compositor blits the SAB onto its canvas and forwards input events back
# to the focused worker. See docs/protocol.md for the wire format.
#
# The file is laid out so the *pure* window-management logic (geometry,
# hit-testing, stacking, placement, focus, client-message dispatch) lives in
# plain Ruby classes with no reference to JS. Only Compositor#attach_to_canvas,
# #render and the worker-spawning helpers touch the bridge, so the policy can
# be unit-tested natively (the JS module is a no-op off-wasm).

# ---------------------------------------------------------------------------
# Theme — Openbox-minimal decorations (the default look).
#
# Frame variants (see Frame class below) override the geometry + paint
# methods that read these constants; new colour names go into the frame
# itself, not here, so this module stays the Openbox baseline.
# ---------------------------------------------------------------------------
module Theme
  DESKTOP        = "#11131a"
  DESKTOP_GRID   = "#171a24"
  TITLE_ACTIVE   = "#9b1c2e"
  TITLE_INACTIVE = "#2a2d3a"
  TITLE_TEXT     = "#f5f6fa"
  BORDER_ACTIVE  = "#ef4444"
  BORDER_INACTIVE= "#3a3d4a"
  CLOSE_BG       = "#e6e7ee"
  CLOSE_GLYPH    = "#1a1a2e"
  RESIZE_GRIP    = "#5b6072"
  HUD_TEXT       = "#9aa0b4"
  MENU_BG        = "#1d1f29"
  MENU_BORDER    = "#3a3d4a"
  MENU_TEXT      = "#e6e7ee"
  MENU_HILITE    = "#9b1c2e"

  TITLE_H   = 22 # titlebar height
  BORDER    = 1  # decoration border width
  CLOSE_SZ  = 14 # close-box side
  MIN_SZ    = 14 # minimize-box side (matches close-box)
  GRIP      = 14 # resize-corner side
  MIN_W     = 90
  MIN_H     = 60
end

# ---------------------------------------------------------------------------
# Frame — window-decoration strategy ("chrome" in UI/UX vocabulary,
# renamed here to avoid the browser-name overlap).
#
# Encapsulates the geometry + paint of a window's titlebar,
# close/minimize/maximize buttons, frame border + resize grip.
# Window#*_rect methods delegate to the current frame's *_rect methods;
# Compositor#draw_window delegates the frame paint to the current
# frame's #paint method.
#
# Two presets ship in-tree:
#   - OpenboxFrame (the default): single close-X box on the RIGHT, a
#     minimize "_" box left of close, no maximize. 22 px flat titlebar.
#     The look the wasmbox compositor.rb has always had.
#   - AquaFrame: three "traffic-light" circle buttons on the LEFT
#     (red close / yellow minimize / green maximize), 28 px flat
#     titlebar with a centred title + a 1 px bottom hairline. The look
#     the sibling wasmaqua project shipped as a fork — now subsumed by
#     selecting WASMBOX_FRAME=aqua at boot.
#
# The active frame is picked AT BOOT TIME from the WASMBOX_FRAME
# environment variable (or the ?frame= URL query param). Hot-swap is
# supported via Frame.current = FrameRegistry[name] for live theming.
