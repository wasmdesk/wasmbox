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
    # A click on the calendar tile's header arrows navigates months instead of
    # starting a drag (the tile body still drags). Consumes the event either way.
    if a.kind == "calendar"
      dir = calendar_header_nav(a, mx, my)
      unless dir.nil?
        calendar_nav(dir)
        return true
      end
    end
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
      # The VIEWED month + selected day (the widget's navigable state), NOT the
      # wall clock — so the tile repaints once per month-navigation and stays
      # idle-quiet under the #88 gate. cal_ensure seeds it from today on first use.
      cal_ensure
      "cal:#{@cal_view.sig}"
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

  # Pixel font size for the clock's HH:MM:SS face. Chosen to dominate the tile
  # like a real clock applet yet stay inside the flex row: the clock tile is
  # 232x88, so after APPLET_PAD the flex time row is ~ (88 - 16 pad - 20 date) =
  # 52 px tall — a 34-px face renders large + legible with headroom, and the
  # probe asserts the painted glyphs stay within the tile bounds. Takes effect
  # via the toolkit's scalable OpenType face (enable_opentype_text_once); on a
  # bitmap fallback set_font_size degrades gracefully to the base size.
  CLOCK_FONT_PX = 34

  # Clock: a BIG HH:MM:SS line (flex, per-label font size) over a weekday-date
  # line. The time label is enlarged via Widgets.set_font_size so the clock reads
  # from across the desktop instead of at the toolkit's base text size.
  def render_clock(w, h)
    c = clock_now
    box = Widgets.v_box
    time_str = "#{pad2(c[:h])}:#{pad2(c[:m])}:#{pad2(c[:s])}"
    date_str = "#{WEEKDAYS_FULL[c[:dow]]} #{c[:day]} #{MONTHS[c[:month] - 1]} #{c[:year]}"
    time_lbl = Widgets.label(time_str)
    Widgets.set_font_size(time_lbl, CLOCK_FONT_PX)
    Widgets.add_flex(box, time_lbl, 1)
    Widgets.add_fixed(box, Widgets.label(date_str), 20)
    img = Widgets.render(box, w, h)
    Base64.strict_encode64(img["pixels"])
  end

  # Calendar: the go-widgets v0.86 Calendar widget — a real month grid (weekday
  # header + day cells + selected-day highlight) painted by the toolkit, replacing
  # the hand-assembled Grid-of-Labels-with-a-Badge. The persistent widget handle
  # (cal_ensure) carries the navigable view, so Render just paints its current
  # month at the tile size.
  def render_calendar(w, h)
    cal_ensure
    img = Widgets.render(@cal_widget, w, h)
    Base64.strict_encode64(img["pixels"])
  end

  # Pixel geometry of the calendar tile's clickable month-navigation zones: the
  # top HEADER_H band's left ARROW_W corner steps to the previous month, its right
  # ARROW_W corner to the next (the toolkit paints its own header arrows there; we
  # own the hit-testing so a click never has to reach into the widget tree). The
  # rest of the tile drags the applet as before.
  CAL_HEADER_H = 24
  CAL_ARROW_W  = 30

  # The persistent Calendar widget's handle, built lazily from today's date via
  # the go-widgets binding (Widgets.calendar), with its selection + month-change
  # callbacks wired. Also seeds @cal_view (05_applets.rb), the pure mirror the
  # dirty-signature reads. Idempotent: a cheap nil-check after the first call, so
  # calling it from applet_sig every frame costs nothing once built.
  def cal_ensure
    return unless @cal_view.nil?
    c = clock_now
    @cal_view   = CalendarView.new(c[:year], c[:month], c[:day])
    @cal_widget = Widgets.calendar(@cal_view.year, @cal_view.month, @cal_view.selected)
    # Wire the day-selection + month-change callbacks (fire ids the toolkit
    # invokes on a day click / a month step). The compositor drives navigation
    # itself, so these are wired for completeness (and to exercise the binding);
    # set_selected below keeps the mirror's day in sync with the widget's clamp.
    Widgets.on_select(@cal_widget, "cal_day")
    Widgets.on_month_change(@cal_widget, "cal_month")
    Widgets.set_selected(@cal_widget, @cal_view.selected)
    nil
  end

  # Step the calendar applet's month via the bound widget's PrevMonth / NextMonth
  # (dir "prev" / "next"), keeping the pure @cal_view mirror in lockstep and then
  # syncing the selected day back from Widgets.selected (the widget's own day
  # re-clamp on a 31 → 30/28 shrink). Invalidates the tile's render cache + marks
  # the frame dirty so the new month repaints exactly ONCE. No-op when the flag is
  # off or the calendar applet is not shown.
  def calendar_nav(dir)
    return nil unless APPLETS
    a = applets.find("calendar")
    return nil if a.nil?
    cal_ensure
    if dir.to_s == "prev"
      Widgets.prev_month(@cal_widget)
      @cal_view.prev_month
    else
      Widgets.next_month(@cal_widget)
      @cal_view.next_month
    end
    @cal_view.set_selected(Widgets.selected(@cal_widget).to_i)
    a.invalidate
    mark_dirty
    dir.to_s
  end

  # Which month-navigation zone (if any) a mousedown at (mx, my) fell in for the
  # calendar applet `a`: "prev" for the header's left corner, "next" for its right
  # corner, nil elsewhere (the click then starts a drag). Only the top CAL_HEADER_H
  # band arms navigation, so the body still drags the tile freely.
  def calendar_header_nav(a, mx, my)
    return nil unless my >= a.y && my < a.y + CAL_HEADER_H
    return "prev" if mx >= a.x && mx < a.x + CAL_ARROW_W
    return "next" if mx >= a.x + a.w - CAL_ARROW_W && mx < a.x + a.w
    nil
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
# go-ruby-widgets v0.86 CLOSED two of the three gaps this file used to carry:
#   * Per-Label font SIZE — Widgets.set_font_size(label, px) now draws the
#     clock's HH:MM:SS far larger than the date line (render_clock above),
#     instead of the process-global text size only.
#   * Native CALENDAR / month-grid widget — Widgets.calendar(year, month,
#     selected) + prev_month/next_month/on_select/on_month_change/set_selected/
#     selected replace the hand-assembled Grid-of-Labels-with-a-Badge; the
#     compositor drives navigation through the binding (calendar_nav) and the
#     pure CalendarView (05_applets.rb) mirrors it for the dirty signature.
# Remaining gap:
#   * LevelBar caption/threshold colouring is exposed by the v0.86 LevelBar
#     binding (label + thresholds args), but the monitor tile still uses a plain
#     bar + a separate Label; a follow-up could fold the CPU/MEM caption + a
#     red "danger" band past 80% into the LevelBar itself.
# ---------------------------------------------------------------------------
