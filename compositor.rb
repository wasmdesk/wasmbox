# A Wayland-inspired, single-instance compositor with an Openbox-style
# window-manager policy — written in pure Ruby and rendered to a <canvas>
# through the interpreter's interactive JS bridge (see internal/vm/jsbridge_wasm.go).
#
# This is the "step A" of a Ruby-written desktop: ONE WASM instance owns both
# the compositor (surface compositing, stacking, damage) and the WM policy
# (focus, placement, decorations, interaction). A true multi-process split
# would move clients into separate Web Workers / WASM instances and speak a
# postMessage + SharedArrayBuffer protocol — see COMPOSITOR.md.
#
# The file is laid out so the *pure* window-management logic (geometry,
# hit-testing, stacking, placement, focus) lives in plain Ruby classes with no
# reference to JS. Only Compositor#attach_to_canvas / #render touch the bridge,
# so the policy can be unit-tested natively (the JS module is a no-op off-wasm).

# ---------------------------------------------------------------------------
# Theme — Openbox-minimal decorations.
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
  GRIP      = 14 # resize-corner side
  MIN_W     = 90
  MIN_H     = 60
end

# ---------------------------------------------------------------------------
# Window — a client surface plus its decoration geometry and hit-testing.
# Pure data + math; no JS here.
# ---------------------------------------------------------------------------
class Window
  attr_accessor :id, :title, :x, :y, :w, :h, :fill, :focused

  def initialize(id, title, x, y, w, h, fill)
    @id = id
    @title = title
    @x = x
    @y = y
    @w = w
    @h = h
    @fill = fill
    @focused = false
  end

  def focused? = @focused

  # Outer frame (decoration included): the titlebar sits above the body.
  def frame_top = @y - Theme::TITLE_H
  def right     = @x + @w
  def bottom    = @y + @h

  # Rectangles, each as [x, y, w, h].
  def titlebar_rect = [@x, frame_top, @w, Theme::TITLE_H]
  def body_rect     = [@x, @y, @w, @h]

  def close_rect
    pad = (Theme::TITLE_H - Theme::CLOSE_SZ) / 2
    [right - Theme::CLOSE_SZ - pad, frame_top + pad, Theme::CLOSE_SZ, Theme::CLOSE_SZ]
  end

  def resize_rect
    [right - Theme::GRIP, bottom - Theme::GRIP, Theme::GRIP, Theme::GRIP]
  end

  # The whole decorated extent, used for "did the click land on me at all?".
  def frame_rect = [@x, frame_top, @w, @h + Theme::TITLE_H]

  def hit?(rect, px, py)
    rx, ry, rw, rh = rect
    px >= rx && px < rx + rw && py >= ry && py < ry + rh
  end

  def contains?(px, py)   = hit?(frame_rect, px, py)
  def on_titlebar?(px, py)= hit?(titlebar_rect, px, py)
  def on_close?(px, py)   = hit?(close_rect, px, py)
  def on_resize?(px, py)  = hit?(resize_rect, px, py)

  def move_to(nx, ny)
    @x = nx
    @y = ny
  end

  def resize_to(nw, nh)
    @w = [nw, Theme::MIN_W].max
    @h = [nh, Theme::MIN_H].max
  end
end

# ---------------------------------------------------------------------------
# WindowManager — stacking order, focus policy and placement. Pure logic,
# fully exercisable without a browser.
#
# The stack is bottom-to-top: stack.last is the topmost / most-recently-raised
# window. Focus follows the top of the stack (click-to-focus, raise-on-focus).
# ---------------------------------------------------------------------------
class WindowManager
  attr_reader :stack

  PALETTE = ["#1f6feb", "#2ea043", "#d29922", "#8957e5", "#db61a2", "#1f9da6"].freeze

  def initialize
    @stack = []
    @next_id = 0
    @cascade = 0
  end

  def windows = @stack

  def focused = @stack.last

  # Cascade placement, Openbox-style: each new window steps down-and-right and
  # wraps once it would run off a notional screen.
  def spawn(title, w = 240, h = 150)
    @next_id += 1
    step = 28
    base_x = 60
    base_y = 60
    x = base_x + (@cascade % 6) * step
    y = base_y + (@cascade % 6) * step
    @cascade += 1
    fill = PALETTE[(@next_id - 1) % PALETTE.length]
    win = Window.new(@next_id, title, x, y, w, h, fill)
    @stack.push(win)
    focus(win)
    win
  end

  # Remove a window from the stack by object identity, returning a new array.
  # (rbgo's Array lacks #delete, so we filter by #equal? and rebuild.)
  def unstack(win)
    @stack.reject! { |w| w.equal?(win) }
  end

  # Raise + focus. Moving to the end of the array puts the window on top.
  def focus(win)
    return nil unless win
    unstack(win)
    @stack.push(win)
    @stack.each { |o| o.focused = false }
    win.focused = true
    win
  end

  def close(win)
    unstack(win)
    top = @stack.last
    top.focused = true if top
    win
  end

  # Top-most window under the pointer (search the stack top-down).
  def window_at(px, py)
    hit = nil
    @stack.each { |w| hit = w if w.contains?(px, py) } # last (topmost) wins
    hit
  end

  # Alt+Tab-ish cycle: send the current top to the bottom and focus the next
  # window down, so repeated presses walk the whole stack.
  def cycle
    return nil if @stack.length < 2
    top = @stack.last
    next_win = @stack[-2]
    unstack(top)            # drop the old top...
    @stack = [top] + @stack # ...and reinsert it at the bottom
    focus(next_win)         # then raise+focus the one that was just below
  end
end

# ---------------------------------------------------------------------------
# Compositor — owns the WM, the canvas and the input/render loop. This is the
# only part that talks to the JS bridge.
# ---------------------------------------------------------------------------
class Compositor
  def initialize(wm)
    @wm = wm
    @drag = nil      # {win:, mode: :move|:resize, dx:, dy:}
    @menu = nil      # {x:, y:, items: [[label, action]]}
    @frames = 0
    @last_t = 0.0
    @fps = 0.0
  end

  # --- bridge wiring -------------------------------------------------------
  def attach_to_canvas(canvas_id)
    @doc = JS.document
    @canvas = @doc.call("getElementById", canvas_id)
    @ctx = @canvas.call("getContext", "2d")
    fit_canvas
    JS.window.on("resize") { |_e| fit_canvas }
    install_input
  end

  def fit_canvas
    w = JS.window.get("innerWidth")
    h = JS.window.get("innerHeight")
    @canvas.set("width", w)
    @canvas.set("height", h)
    @width = w
    @height = h
  end

  def install_input
    @canvas.on("mousedown")   { |e| on_mousedown(e) }
    @canvas.on("mousemove")   { |e| on_mousemove(e) }
    @canvas.on("mouseup")     { |_e| @drag = nil }
    @canvas.on("contextmenu") { |e| on_contextmenu(e) }
    JS.window.on("keydown")   { |e| on_keydown(e) }
  end

  # --- input handlers ------------------------------------------------------
  def on_mousedown(e)
    mx = e.get("offsetX")
    my = e.get("offsetY")

    # A menu is open: a click either activates an item or dismisses it.
    if @menu
      handle_menu_click(mx, my)
      return
    end

    win = @wm.window_at(mx, my)
    unless win
      return # empty desktop, left button: nothing (right button = menu)
    end

    @wm.focus(win)

    if win.on_close?(mx, my)
      @wm.close(win)
    elsif win.on_resize?(mx, my)
      @drag = { win: win, mode: :resize, dx: win.right - mx, dy: win.bottom - my }
    elsif win.on_titlebar?(mx, my)
      @drag = { win: win, mode: :move, dx: mx - win.x, dy: my - win.y }
    end
  end

  def on_mousemove(e)
    return nil unless @drag
    mx = e.get("offsetX")
    my = e.get("offsetY")
    win = @drag[:win]
    if @drag[:mode] == :move
      win.move_to(mx - @drag[:dx], my - @drag[:dy])
    else
      win.resize_to(mx + @drag[:dx] - win.x, my + @drag[:dy] - win.y)
    end
  end

  def on_contextmenu(e)
    e.call("preventDefault")
    mx = e.get("offsetX")
    my = e.get("offsetY")
    win = @wm.window_at(mx, my)
    @menu = if win
      { x: mx, y: my, items: [
        ["Raise",  -> { @wm.focus(win) }],
        ["Close",  -> { @wm.close(win) }],
      ] }
    else
      { x: mx, y: my, items: [
        ["New window", -> { @wm.spawn("xterm ##{@wm.windows.length + 1}") }],
        ["New window (wide)", -> { w = @wm.spawn("editor", 320, 200) }],
      ] }
    end
  end

  def on_keydown(e)
    key = e.get("key")
    case key
    when "Tab"
      e.call("preventDefault")
      @wm.cycle
    when "Escape"
      @menu = nil
    end
  end

  MENU_ITEM_H = 22
  MENU_W = 150

  def handle_menu_click(mx, my)
    items = @menu[:items]
    x = @menu[:x]
    y = @menu[:y]
    if mx >= x && mx < x + MENU_W && my >= y && my < y + items.length * MENU_ITEM_H
      idx = (my - y) / MENU_ITEM_H
      items[idx][1].call
    end
    @menu = nil
  end

  # --- rendering -----------------------------------------------------------
  def start
    loop_frame = nil
    loop_frame = proc do |t|
      tick(t || 0.0)
      render
      JS.raf(&loop_frame)
    end
    JS.raf(&loop_frame)
  end

  def tick(t)
    @frames += 1
    if @last_t > 0 && t > @last_t
      inst = 1000.0 / (t - @last_t)
      @fps = @fps == 0.0 ? inst : (@fps * 0.9 + inst * 0.1) # smoothed
    end
    @last_t = t
  end

  def render
    draw_desktop
    @wm.windows.each { |win| draw_window(win) } # bottom-to-top
    draw_menu if @menu
    draw_hud
  end

  def fill_rect(rect, colour)
    x, y, w, h = rect
    @ctx.set("fillStyle", colour)
    @ctx.call("fillRect", x, y, w, h)
  end

  def stroke_rect(rect, colour, lw = 1)
    x, y, w, h = rect
    @ctx.set("strokeStyle", colour)
    @ctx.set("lineWidth", lw)
    @ctx.call("strokeRect", x + 0.5, y + 0.5, w - 1, h - 1)
  end

  def text(str, x, y, colour, size = 12)
    @ctx.set("fillStyle", colour)
    @ctx.set("font", "#{size}px ui-monospace, Menlo, monospace")
    @ctx.call("fillText", str, x, y)
  end

  def draw_desktop
    fill_rect([0, 0, @width, @height], Theme::DESKTOP)
    # A faint grid so window motion reads clearly.
    @ctx.set("strokeStyle", Theme::DESKTOP_GRID)
    @ctx.set("lineWidth", 1)
    step = 40
    gx = 0
    while gx < @width
      @ctx.call("beginPath")
      @ctx.call("moveTo", gx + 0.5, 0)
      @ctx.call("lineTo", gx + 0.5, @height)
      @ctx.call("stroke")
      gx += step
    end
    gy = 0
    while gy < @height
      @ctx.call("beginPath")
      @ctx.call("moveTo", 0, gy + 0.5)
      @ctx.call("lineTo", @width, gy + 0.5)
      @ctx.call("stroke")
      gy += step
    end
  end

  def draw_window(win)
    active = win.focused?

    # Titlebar.
    fill_rect(win.titlebar_rect, active ? Theme::TITLE_ACTIVE : Theme::TITLE_INACTIVE)
    tx, ty, _tw, _th = win.titlebar_rect
    text(win.title, tx + 6, ty + 15, Theme::TITLE_TEXT)

    # Close box.
    cx, cy, cw, ch = win.close_rect
    fill_rect(win.close_rect, Theme::CLOSE_BG)
    @ctx.set("strokeStyle", Theme::CLOSE_GLYPH)
    @ctx.set("lineWidth", 1.5)
    @ctx.call("beginPath")
    @ctx.call("moveTo", cx + 3, cy + 3)
    @ctx.call("lineTo", cx + cw - 3, cy + ch - 3)
    @ctx.call("moveTo", cx + cw - 3, cy + 3)
    @ctx.call("lineTo", cx + 3, cy + ch - 3)
    @ctx.call("stroke")

    # Client body.
    fill_rect(win.body_rect, win.fill)

    # Resize grip (bottom-right), drawn as two short diagonals.
    rx, ry, rw, rh = win.resize_rect
    @ctx.set("strokeStyle", Theme::RESIZE_GRIP)
    @ctx.set("lineWidth", 1)
    @ctx.call("beginPath")
    @ctx.call("moveTo", rx + rw, ry + rh * 0.4)
    @ctx.call("lineTo", rx + rw * 0.4, ry + rh)
    @ctx.call("moveTo", rx + rw, ry + rh * 0.75)
    @ctx.call("lineTo", rx + rw * 0.75, ry + rh)
    @ctx.call("stroke")

    # 1px decoration border around the whole frame.
    stroke_rect(win.frame_rect, active ? Theme::BORDER_ACTIVE : Theme::BORDER_INACTIVE, Theme::BORDER)
  end

  def draw_menu
    items = @menu[:items]
    x = @menu[:x]
    y = @menu[:y]
    h = items.length * MENU_ITEM_H
    fill_rect([x, y, MENU_W, h], Theme::MENU_BG)
    stroke_rect([x, y, MENU_W, h], Theme::MENU_BORDER, 1)
    items.each_with_index do |(label, _action), i|
      iy = y + i * MENU_ITEM_H
      text(label, x + 8, iy + 15, Theme::MENU_TEXT)
    end
  end

  def draw_hud
    n = @wm.windows.length
    line = "rbgo compositor — #{n} window#{n == 1 ? '' : 's'} — #{'%.0f' % @fps} fps — frame #{@frames}"
    text(line, 10, @height - 12, Theme::HUD_TEXT, 12)
  end
end

# ---------------------------------------------------------------------------
# Boot: build the WM, spawn a few clients, attach to the canvas and run.
# ---------------------------------------------------------------------------
wm = WindowManager.new
wm.spawn("xterm")
wm.spawn("editor", 300, 190)
wm.spawn("about rbgo", 220, 130)

comp = Compositor.new(wm)
comp.attach_to_canvas("screen")
comp.start

JS.log("rbgo compositor: started with #{wm.windows.length} windows")
