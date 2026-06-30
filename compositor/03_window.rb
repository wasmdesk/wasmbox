# ---------------------------------------------------------------------------
class Window
  attr_accessor :id, :title, :x, :y, :w, :h, :fill, :focused, :role, :minimized, :workspace, :shaded, :lock_aspect

  def initialize(id, title, x, y, w, h, fill, role = "window")
    @id = id
    @title = title
    @x = x
    @y = y
    @w = w
    @h = h
    @fill = fill
    @focused = false
    # Aspect-ratio lock for interactive resize: when > 0, the compositor
    # preserves w / h == @lock_aspect each time resize_to is called. 0 means
    # free resize (the default). Set from the hello-handshake's optional
    # `lock_aspect` field, OR after the fact via the additive `set_lock_aspect`
    # wire message (Quake takes that path: its worker.js cannot reach the SDK's
    # hello payload, so it posts set_lock_aspect from the port shim after the
    # welcome lands). The lock is purely a UI policy; the SAB native_w/native_h
    # are unchanged, so the scale-fit blit still works at any locked size.
    @lock_aspect = 0.0
    # role is "window" (normal, decorated, cascade-placed) or "panel" (the dock:
    # undecorated, bottom-center anchored, always-on-top). An unknown role MUST
    # behave like a normal window, so anything that is not exactly "panel" is
    # treated as a window. See docs/protocol.md + wasmdock INTEGRATION.md.
    @role = role
    # Openbox-style minimize: a minimized window is removed from the render
    # pipeline (decoration + body) but kept in the stack so a click on its
    # entry in the dock's iconbar can restore it. Panels are never minimized.
    @minimized = false
    # Fluxbox-style workspaces: every normal window belongs to exactly one
    # workspace (1..WorkspaceCount). Only the active workspace's windows
    # render and appear in the iconbar. Panels ignore the workspace field —
    # they are always visible (workspace = 0 sentinel, set by spawn paths).
    # WindowManager.register_external / WindowManager.spawn fill this in at
    # creation time so the default here only matters for direct Window.new
    # callers in tests.
    @workspace = 1
    # Window-shade ("roll-up", a.k.a. replier/déplier): a shaded window collapses
    # to just its titlebar — the body is not rendered and not hit-testable, but
    # the window stays put + draggable + closable from its titlebar. Toggled by a
    # two-finger swipe over the titlebar (up = fold, down = unfold). Distinct from
    # minimize (which folds into the dock iconbar). Panels/popups never shade.
    @shaded = false
  end

  def focused? = @focused
  def minimized? = @minimized
  def shaded? = @shaded

  # A panel (dock-style surface) has no decoration and is anchored, never
  # cascade-placed. A popup is a child surface anchored to a parent window
  # (menus / tooltips): also undecorated, but placed parent-relative and
  # grab-dismissed. Anything else is a normal, decorated window.
  def panel? = @role == "panel"
  def popup? = @role == "popup"
  # Decorated = a normal window: titlebar, close/minimize/resize boxes,
  # draggable, focusable. Panels and popups carry no decoration.
  def decorated? = !panel? && !popup?

  # Outer frame (decoration included): the titlebar sits above the body. The
  # titlebar height comes from the current chrome (OpenboxFrame = 22 px,
  # AquaFrame = 28 px, custom chromes set their own). For a panel there is
  # no titlebar, so the frame top is the body top.
  def frame_top = decorated? ? @y - Frame.current.title_h : @y
  def right     = @x + @w
  def bottom    = @y + @h

  # Rectangles, each as [x, y, w, h]. Panels carry no decoration rectangles
  # so the titlebar / close / resize hit-rects collapse to empty (zero-size)
  # and the frame equals the body. All decorated-window geometry delegates
  # to the current Frame strategy; Window itself only owns body_rect.
  def titlebar_rect = Frame.current.titlebar_rect(self)
  def body_rect     = [@x, @y, @w, @h]
  def close_rect    = Frame.current.close_rect(self)
  def minimize_rect = Frame.current.minimize_rect(self)
  def maximize_rect = Frame.current.maximize_rect(self)
  def resize_rect   = Frame.current.resize_rect(self)
  def frame_rect    = Frame.current.frame_rect(self)

  def hit?(rect, px, py)
    rx, ry, rw, rh = rect
    px >= rx && px < rx + rw && py >= ry && py < ry + rh
  end

  def contains?(px, py)   = hit?(frame_rect, px, py)
  # Only a decorated window is draggable, closable-by-box, minimizable or
  # resizable; for panels and popups every decoration hit-test reports "no hit"
  # so those gestures can never start. on_maximize? returns false unless the
  # chrome opts in via has_maximize? (Aqua does, Openbox doesn't).
  def on_titlebar?(px, py)= decorated? ? hit?(titlebar_rect, px, py) : false
  def on_close?(px, py)   = decorated? ? hit?(close_rect, px, py) : false
  def on_minimize?(px, py)= decorated? ? hit?(minimize_rect, px, py) : false
  def on_maximize?(px, py)= (decorated? && Frame.current.has_maximize?) ? hit?(maximize_rect, px, py) : false
  def on_resize?(px, py)  = decorated? ? hit?(resize_rect, px, py) : false

  def move_to(nx, ny)
    @x = nx
    @y = ny
  end

  def resize_to(nw, nh)
    # Aspect-lock policy: when @lock_aspect > 0 the user's drag delta on EITHER
    # axis is treated as the leader and the OTHER axis snaps so w/h matches the
    # locked ratio. We pick width as the leader (the bottom-right grip's
    # horizontal drag dominates the visual; the vertical follows). nh is then
    # round(nw / lock_aspect). MIN_W/MIN_H clamps apply AFTER the lock so a
    # tiny request still yields the smaller of the two bounds without breaking
    # the ratio. Backward-compat: a 0.0 lock leaves the free-resize behaviour
    # the SDK clients have always seen.
    if @lock_aspect > 0
      lw = [nw, Theme::MIN_W].max
      lh = (lw / @lock_aspect).round
      if lh < Theme::MIN_H
        lh = Theme::MIN_H
        lw = (lh * @lock_aspect).round
      end
      @w = lw
      @h = lh
    else
      @w = [nw, Theme::MIN_W].max
      @h = [nh, Theme::MIN_H].max
    end
  end

  # In-process windows ("step A"): painted by the compositor itself from #fill.
  # ExternalWindow overrides this to true.
  def external? = false
end

# ---------------------------------------------------------------------------
# ExternalWindow — a window whose body pixels live in a SharedArrayBuffer
# owned by an external client (Web Worker / separate wasm instance). The
# compositor still owns the decoration; the client only paints inside the
# body rectangle (surface-local coordinates).
#
# The class itself does NOT touch JS — it just stores the worker ref, the SAB
# ref and a (lazily built) ImageData ref. The Compositor pulls those refs out
# when it blits the body, and WindowManager#handle_client_message routes
# protocol messages to the matching ExternalWindow.
# ---------------------------------------------------------------------------
class ExternalWindow < Window
  attr_accessor :worker, :sab, :ctl, :image_data, :stride, :pending_damage
  # The SAB-backed surface is allocated ONCE at welcome time at the granted
  # (w, h) dimensions; @w/@h then drift independently as the user drags the
  # resize grip. Compositor#blit_external uses @native_w/@native_h to address
  # the SAB and scales into @w/@h on present. Without this pair a resized
  # window would read past the SAB end and show garbage / black bands.
  attr_accessor :native_w, :native_h
  # For popups: the window_id of the parent surface this popup is anchored to
  # (nil for windows/panels). Used to dismiss a popup when its parent goes.
  attr_accessor :parent_id

  # No fill colour: an ExternalWindow's body is the SAB's RGBA bytes. We pass
  # a sentinel through to Window#initialize because the existing decoration
  # path never reads `fill` for external windows (Compositor.draw_window
  # branches on #external? before fill_rect).
  def initialize(id, title, x, y, w, h, role = "window")
    super(id, title, x, y, w, h, "#000000", role)
    @worker = nil
    @sab = nil
    @image_data = nil
    @stride = 4 * w
    # Native SAB dimensions -- frozen at construction (the client only
    # allocated this many RGBA bytes). @w/@h may drift via resize_to; the
    # SAB+ImageData never resize, the blit scale-fits instead.
    @native_w = w
    @native_h = h
    # Until the first commit lands, paint the full body so we don't flash an
    # uninitialised SAB. After that, the client tells us which rect changed.
    @pending_damage = { x: 0, y: 0, w: w, h: h }
  end

  def external? = true

  # Replace the pending damage with the union of (previous, new). A client
  # can post several commits between two render frames; the compositor only
  # blits once per frame and needs the union.
  def merge_damage(d)
    if @pending_damage.nil?
      @pending_damage = d
      return
    end
    a = @pending_damage
    x0 = [a[:x], d[:x]].min
    y0 = [a[:y], d[:y]].min
    x1 = [a[:x] + a[:w], d[:x] + d[:w]].max
    y1 = [a[:y] + a[:h], d[:y] + d[:h]].max
    @pending_damage = { x: x0, y: y0, w: x1 - x0, h: y1 - y0 }
  end

  # Clip the damage rect to the surface bounds and to a maximum-area-1 sanity
  # bound, returning nil if there is nothing left. The bound is the NATIVE SAB
  # size, not the current window size -- the SAB never grows, only the on-
  # canvas presentation rect does. Without this, a window dragged smaller than
  # its native surface would silently truncate the damage and the blit would
  # only refresh the visible top-left slice on resize-up.
  def clipped_damage
    return nil unless @pending_damage
    d = @pending_damage
    x = [d[:x], 0].max
    y = [d[:y], 0].max
    w = [d[:w] + [d[:x], 0].min, @native_w - x].min
    h = [d[:h] + [d[:y], 0].min, @native_h - y].min
    return nil if w <= 0 || h <= 0
    { x: x, y: y, w: w, h: h }
  end

  def clear_damage
    @pending_damage = nil
  end
end

# ---------------------------------------------------------------------------
# DOMWindow — a window whose chrome is painted by the compositor (titlebar,
# borders, resize grip, close button) but whose BODY is a real HTML <iframe>
# overlaid on top of the canvas at the body-rect. Used to embed browser-only
# apps (code-server / vscodium, jupyter, ...) inside a wasmbox window while
# keeping the desktop's WM semantics: the user can drag/resize/raise/close
# the iframe-bearing window like any other.
#
# Lifecycle:
#   - spawn_dom: WindowManager appends a DOMWindow + calls
#     JS.global.call("wasmboxIframeAttach", id, url, body_x, body_y, body_w, body_h)
#   - Compositor.draw_window: skips fill_rect for dom_window? bodies (the
#     iframe paints itself) and instead calls "wasmboxIframeMove" with the
#     current body rect so the overlay tracks the WM-managed position.
#   - close_window: calls "wasmboxIframeDetach" to remove the overlay.
#
# The class is pure-Ruby + only references JS lazily at the integration
# points, so unit tests can construct + mutate a DOMWindow without booting
# a browser.
# ---------------------------------------------------------------------------
class DOMWindow < Window
  attr_accessor :url

  def initialize(id, title, x, y, w, h, url, role = "window")
    super(id, title, x, y, w, h, "#000000", role)
    @url = url
  end

  def dom? = true
end

# Default predicate so non-DOM windows (Window + ExternalWindow) answer
# `dom?` without exploding on the dispatch site that just asks the question.
class Window
  def dom? = false
end

# ---------------------------------------------------------------------------
# WindowManager — stacking order, focus policy and placement. Pure logic,
# fully exercisable without a browser. Also routes external-client messages
# (handle_client_message) — that's all pure dispatch, no JS.
#
# The stack is bottom-to-top: stack.last is the topmost / most-recently-raised
# window. Focus follows the top of the stack (click-to-focus, raise-on-focus).
# ---------------------------------------------------------------------------
