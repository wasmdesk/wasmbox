# ---------------------------------------------------------------------------
# Desktop applets — the compositor render + wire glue over the pure model in
# 05_applets.rb. THIRD feature of the DE (desktop-environment) spine
# (2026-08-03).
#
# An applet is a compositor-OWNED live tile pinned to the desktop behind the
# windows: a Clock, a month Calendar, a System Monitor gauge. Each is a small
# go-widgets tree rendered to an RGBA buffer and blitted onto a card plate:
#
#   Widgets.v_box / grid / label / level_bar / badge -> Widgets.render -> RGBA
#   bytes -> base64 -> JS wasmboxBlitRGBAOver (alpha-composited drawImage), the
#   SAME seam the HUD (09_hud_widgets.rb), toasts (12) and tray (13) use.
#
# Chosen stratum: a compositor-drawn OVERLAY (like the tray), NOT a new window
# role. But unlike the toast/tray overlays — which are the always-on-TOP stratum
# painted last — applets are the BOTTOM interactive stratum: draw_applets runs in
# Compositor#render right after draw_desktop (the wallpaper) and BEFORE the
# window loop, so every window composites OVER an applet. This keeps the heavily
# co-edited WindowManager untouched (no role threaded through focus / cycle /
# window_at / iconbar / snapshot) — a role would only earn its keep if applets
# needed to be focusable or client-backed, which they are not.
#
# Input follows the same z-order: an applet drag starts (on_mousedown,
# 06_core.rb) only when @wm.window_at is nil — i.e. no window is under the
# pointer — so a window on top always keeps its clicks. Add/remove is driven from
# the root right-click menu's "Applets" submenu (RootMenu.build_applets, 05_menu)
# via the [:applet, kind] action; the shown-set + positions persist to
# localStorage (APPLETS_KEY), mirroring the window-layout persistence in
# 06_core.rb.
#
# Each tile's pixels are cached and only re-rendered on a CONTENT change — the
# clock once a second, the monitor when its (demo) values step, the calendar on a
# month change — never every frame. A position-only change (a drag) re-presents
# the cached JS-side ImageData at the new coordinates (empty base64), one
# drawImage per tile, exactly like a toast reflow.
#
# Gated behind Compositor::APPLETS so `main` stays shippable during the live
# co-edit — flip it to false to disable every draw / drag / menu / persistence
# hook below with zero other changes (the "Applets" menu entry then vanishes and
# the desktop behaves exactly as it did before this feature landed).
# ---------------------------------------------------------------------------
require "widgets"
require "base64"

class Compositor
  # Master switch for the whole applets feature. `false` makes applets_for_menu
  # return nil (so RootMenu omits the "Applets" entry) and every draw/drag/toggle
  # hook a no-op.
  APPLETS = true

  # localStorage key the shown-set + positions round-trip through, mirroring
  # LAYOUT_KEY. A fresh desktop (no key) starts with NO applets — the user adds
  # them from the root menu's "Applets" submenu — so the out-of-the-box desktop
  # is unchanged and every existing bare-desktop probe stays valid; once the user
  # adds any, their set + positions persist across reloads.
  APPLETS_KEY = "wasmbox.applets"

  # Card plate behind each tile: a translucent lifted surface + hairline border
  # (like the tray's TRAY_PLATE), painted under the widget buffer so the tile
  # reads as a solid card regardless of the transparent widget ground. APPLET_PAD
  # insets the widget content within the plate.
  APPLET_PLATE  = "rgba(42, 45, 58, 0.92)"
  APPLET_BORDER = "rgba(90, 96, 114, 0.95)"
  APPLET_PAD    = 8

  # Calendar / clock label vocab (the toolkit bitmap/opentype font is ASCII, so
  # short weekday + month names).
  WEEKDAYS      = ["Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"].freeze
  WEEKDAYS_FULL = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"].freeze
  MONTHS = ["January", "February", "March", "April", "May", "June", "July",
            "August", "September", "October", "November", "December"].freeze

  # The applet board (model in 05_applets.rb, which loads before this file).
  # Built lazily so Compositor#initialize (06_core.rb) needs no edit and pure
  # rbtest — which never loads this file — is unaffected. On first access it is
  # restored from localStorage (empty on a fresh desktop).
  def applets
    @applets ||= restore_applets(AppletBoard.new)
  end

  # Load the persisted shown-set + positions into `board`. With no storage or no
  # saved key (a fresh desktop) the board stays empty — the user adds applets
  # from the root menu. @width/@height are set by attach_to_canvas before the
  # first render, so the parse clamp has a real desktop size.
  def restore_applets(board)
    store = JS.window.get("localStorage")
    return board if store.nil?
    raw = store.call("getItem", APPLETS_KEY)
    board.parse(raw.to_s, @width, @height) unless raw.nil?
    board
  end

  # Persist the current shown-set + positions. Called after every mutation (a
  # toggle, a place, a drag end). Degrades to a no-op when storage is unavailable
  # (private mode), like save_layout.
  def save_applets
    store = JS.window.get("localStorage")
    return nil if store.nil?
    store.call("setItem", APPLETS_KEY, applets.serialize)
    nil
  end

  # The AppletBoard for the root menu's "Applets" submenu, or nil when the
  # feature is off (RootMenu then omits the entry, keeping the menu shippable).
  def applets_for_menu
    APPLETS ? applets : nil
  end

  # Toggle an applet kind on/off from the root menu ([:applet, kind]) or the test
  # hook. Persists the new set. No-op when the flag is off or the kind is unknown.
  def toggle_applet(kind)
    return nil unless APPLETS
    return nil unless Applet.kind?(kind)
    applets.toggle(kind)
    save_applets
  end

  # Ensure `kind` is shown and move it to (x, y) (clamped to the desktop). Used
  # by the __wasmboxAppletPlace test hook to position a tile deterministically.
  # Persists. No-op when the flag is off or the kind is unknown.
  def place_applet(kind, x, y)
    return nil unless APPLETS
    return nil unless Applet.kind?(kind)
    a = applets.add(kind)
    return nil if a.nil?
    applets.move(a, x, y, @width, @height)
    save_applets
    a
  end

  # Hide an applet kind. Persists. No-op when the flag is off.
  def remove_applet(kind)
    return nil unless APPLETS
    applets.remove(kind)
    save_applets
  end

  # --- drag ---------------------------------------------------------------
  # An applet drag begins on a LEFT mousedown over a tile on the empty desktop
  # (no window under the point — 06_core.rb only calls this from the `window_at
  # nil` arm, so a window on top keeps its clicks). Returns true when a drag was
  # started (the caller then consumes the event). No-op (false) when the flag is
  # off, the button is not the left button, or no tile is under the pointer.
  def applet_mousedown(mx, my, e)
    return false unless APPLETS
    btn = e.get("button")
    return false unless btn.nil? || btn == 0
    a = applets.at(mx, my)
    return false if a.nil?
    @applet_drag = { applet: a, dx: mx - a.x, dy: my - a.y }
    true
  end

  # Continue an in-progress applet drag: reposition the tile under the pointer,
  # clamped to the desktop. Returns true while a drag is live so on_mousemove
  # stops before forwarding the move to any client. No-op (false) otherwise.
  def applet_drag_move(mx, my)
    return false if @applet_drag.nil?
    a = @applet_drag[:applet]
    applets.move(a, mx - @applet_drag[:dx], my - @applet_drag[:dy], @width, @height)
    true
  end

  # Finish an applet drag: clear the drag state + persist the new position.
  # Returns true when a drag was active (the caller then consumes the mouseup).
  def applet_drag_end
    return false if @applet_drag.nil?
    @applet_drag = nil
    save_applets
    true
  end

  # --- render -------------------------------------------------------------
  # Draw the applet stratum: a card plate + the cached widget buffer for each
  # shown tile. Called from Compositor#render AFTER draw_desktop and BEFORE the
  # window loop, so windows composite over applets. No-op when the flag is off or
  # nothing is shown.
  def draw_applets
    return nil unless APPLETS
    b = applets
    return nil if b.empty?
    b.items.each do |a|
      # Card plate (cheap ctx fill each frame, follows a drag) drawn first so the
      # transparent-ground widget buffer composites over a solid tile.
      fill_rect([a.x, a.y, a.w, a.h], APPLET_PLATE)
      stroke_rect([a.x, a.y, a.w, a.h], APPLET_BORDER, 1)
      iw = a.w - 2 * APPLET_PAD
      ih = a.h - 2 * APPLET_PAD
      dx = a.x + APPLET_PAD
      dy = a.y + APPLET_PAD
      sig = applet_sig(a)
      if a.b64.nil? || a.sig != sig
        a.b64 = render_applet(a, iw, ih)
        a.sig = sig
        a.blitted = false
      end
      if a.blitted
        JS.global.call("wasmboxBlitRGBAOver", @ctx, "", iw, ih, dx, dy, a.key)
      else
        JS.global.call("wasmboxBlitRGBAOver", @ctx, a.b64, iw, ih, dx, dy, a.key)
        a.blitted = true
      end
    end
  end

  # A content signature that changes exactly when a tile's pixels must: the clock
  # every second, the monitor when its demo values step, the calendar on a month
  # change (the day component drives the today-highlight).
  def applet_sig(a)
    case a.kind
    when "clock"
      c = clock_now
      "#{c[:h]}:#{c[:m]}:#{c[:s]}|#{c[:year]}-#{c[:month]}-#{c[:day]}"
    when "calendar"
      c = clock_now
      "#{c[:year]}-#{c[:month]}-#{c[:day]}"
    when "monitor"
      v = monitor_values
      "cpu#{v[0]}|mem#{v[1]}"
    else
      a.kind
    end
  end

  # Build one applet's RGBA buffer (base64) sized to (w, h) via a go-widgets tree.
  def render_applet(a, w, h)
    case a.kind
    when "calendar" then render_calendar(w, h)
    when "monitor"  then render_monitor(w, h)
    else render_clock(w, h)
    end
  end

  # Clock: a big HH:MM:SS line (flex) over a weekday-date line.
  def render_clock(w, h)
    c = clock_now
    box = Widgets.v_box
    time_str = "#{pad2(c[:h])}:#{pad2(c[:m])}:#{pad2(c[:s])}"
    date_str = "#{WEEKDAYS_FULL[c[:dow]]} #{c[:day]} #{MONTHS[c[:month] - 1]} #{c[:year]}"
    Widgets.add_flex(box, Widgets.label(time_str), 1)
    Widgets.add_fixed(box, Widgets.label(date_str), 20)
    img = Widgets.render(box, w, h)
    Base64.strict_encode64(img["pixels"])
  end

  # Calendar: a month title over a 7x7 grid — weekday headers on row 0, the day
  # numbers laid out from the month's first weekday, today painted as a filled
  # Badge so it reads as highlighted.
  def render_calendar(w, h)
    c = clock_now
    outer = Widgets.v_box
    Widgets.add_fixed(outer, Widgets.label("#{MONTHS[c[:month] - 1]} #{c[:year]}"), 20)
    grid = Widgets.grid(7, 7)
    col = 0
    while col < 7
      Widgets.attach(grid, Widgets.label(WEEKDAYS[col]), col, 0)
      col += 1
    end
    first = c[:first_dow]
    dim   = c[:days_in_month]
    today = c[:day]
    d = 1
    while d <= dim
      idx = first + (d - 1)
      cell = if d == today
        Widgets.badge(d.to_s, "#3b82f6", "#ffffff")
      else
        Widgets.label(d.to_s)
      end
      Widgets.attach(grid, cell, idx % 7, 1 + idx / 7)
      d += 1
    end
    Widgets.add_flex(outer, grid, 1)
    img = Widgets.render(outer, w, h)
    Base64.strict_encode64(img["pixels"])
  end

  # System monitor: CPU + MEM percentage labels each over a LevelBar. The values
  # are SYNTHESIZED demo metrics (a slow deterministic walk off the frame counter
  # — wasm has no real per-core telemetry), animated so the gauges visibly move.
  def render_monitor(w, h)
    v = monitor_values
    cpu = v[0]
    mem = v[1]
    box = Widgets.v_box
    Widgets.add_fixed(box, Widgets.label("CPU #{cpu}%"), 16)
    cpu_bar = Widgets.level_bar(20)
    Widgets.set_value(cpu_bar, cpu * 20 / 100)
    Widgets.add_fixed(box, cpu_bar, 16)
    Widgets.add_fixed(box, Widgets.label("MEM #{mem}%"), 16)
    mem_bar = Widgets.level_bar(20)
    Widgets.set_value(mem_bar, mem * 20 / 100)
    Widgets.add_fixed(box, mem_bar, 16)
    img = Widgets.render(box, w, h)
    Base64.strict_encode64(img["pixels"])
  end

  # Current wall-clock fields from the JS Date (h/m/s + calendar math), memoised
  # per frame so the sig + the render share one JS call. wasmboxClock is a shim in
  # compositor.worker.js returning { h, m, s, year, month(1-12), day, dow(0=Sun),
  # first_dow, days_in_month }.
  def clock_now
    if @clock_frame != @frames
      r = JS.global.call("wasmboxClock")
      @clock = {
        h: r.get("h").to_i, m: r.get("m").to_i, s: r.get("s").to_i,
        year: r.get("year").to_i, month: r.get("month").to_i,
        day: r.get("day").to_i, dow: r.get("dow").to_i,
        first_dow: r.get("first_dow").to_i, days_in_month: r.get("days_in_month").to_i,
      }
      @clock_frame = @frames
    end
    @clock
  end

  # Synthesized CPU / MEM percentages (demo only): a slow sawtooth off the frame
  # counter, distinct periods so the two gauges drift apart. Range ~25..84.
  def monitor_values
    t = @frames / 45
    cpu = 25 + (t % 60)
    mem = 40 + ((t * 7) % 45)
    [cpu, mem]
  end

  # Zero-pad a small non-negative integer to two digits (the bitmap/opentype font
  # is ASCII; sprintf width specifiers are avoided for portability).
  def pad2(n)
    n < 10 ? "0#{n}" : n.to_s
  end
end

# ---------------------------------------------------------------------------
# go-ruby-widgets/widgets notes hit while wiring this (for the toolkit backlog):
#   * No per-Label font SIZE — the clock's HH:MM:SS cannot be drawn larger than
#     the date line (the toolkit exposes only a process-global font via
#     use_opentype_text / use_opentype_text_size). A per-widget text scale (or a
#     dedicated big-numeral "Clock"/"Gauge" widget) would let the time dominate
#     the tile the way a real clock applet does.
#   * No native CALENDAR / month-grid widget — the calendar is hand-assembled
#     from a Grid of Labels with a Badge for today. A toolkit MonthView (weekday
#     header + today highlight + optional week numbers) would remove the date
#     math + the Badge-as-highlight workaround here.
#   * LevelBar has no label/threshold colouring — the CPU/MEM percentages are a
#     separate Label above each bar, and a "danger" band (red past 80%) would
#     need SetKind-style styling the LevelBar binding does not expose yet.
# ---------------------------------------------------------------------------
