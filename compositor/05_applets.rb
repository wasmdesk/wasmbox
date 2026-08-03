# ---------------------------------------------------------------------------
# Desktop applets domain model — the THIRD feature of the DE (desktop-
# environment) spine (2026-08-03). An applet is a small LIVE tile pinned to the
# desktop BEHIND the windows (a clock, a month calendar, a system-monitor
# gauge): it is compositor-owned (no client, no SharedArrayBuffer), draggable on
# the empty desktop, added/removed from the root right-click menu, and its
# shown-set + positions persist across a reload. This file owns ONLY the model
# (what an applet is, which are shown, how the board is hit-tested, moved and
# serialized); the render + wire glue live in 14_applets.rb (Widgets tree ->
# Widgets.render -> overlay blit), which is JS-touching and so loads after
# 06_core.rb.
#
# The split mirrors 05_notifications.rb / 05_tray.rb: this file sorts BELOW 06_
# so cmd/rbtest — which loads 01_..05_ only — exercises add / remove / toggle /
# move-clamp / hit-test / serialize / parse without a browser, exactly like the
# WindowManager, the toast stack and the tray next to it.
#
# Stratum coordination: unlike toasts + the tray (which are the always-on-TOP
# overlay stratum, painted last), applets are the BOTTOM interactive stratum —
# painted right after the desktop backdrop and BEFORE the windows, so a window
# always composites OVER an applet (13_tray.rb / 12_notifications.rb sit above
# everything; applets sit below everything except the wallpaper). Input follows
# the same z-order: an applet only receives a click when NO window is under the
# point (see 06_core.rb on_mousedown), so a window on top keeps its clicks.
# ---------------------------------------------------------------------------

# A single applet tile: its kind, its top-left desktop position, and a per-tile
# render cache. The size is a fixed per-kind constant so the model can lay the
# tile out + hit-test it without ever rendering — the render layer fills exactly
# those (w, h) pixels. The cache fields (@b64 / @blitted / @sig) are the ONLY
# mutable state the Compositor touches; the model itself never reads them.
class Applet
  # Per-kind tile size in pixels. Fixed so #contains? / #move clamping / the
  # probe geometry all agree with where the pixels land.
  SIZE = {
    "clock"    => [232, 88],
    "calendar" => [210, 184],
    "monitor"  => [216, 96],
  }.freeze

  # Where a freshly-added applet lands: a left column, clear of the top-right
  # tray + notification strips (05_tray.rb / 05_notifications.rb) and the
  # bottom-left status HUD.
  DEFAULT_POS = {
    "clock"    => [40, 40],
    "calendar" => [40, 148],
    "monitor"  => [40, 352],
  }.freeze

  # Human-friendly labels for the root-menu "Applets" submenu.
  LABEL = {
    "clock"    => "Clock",
    "calendar" => "Calendar",
    "monitor"  => "System Monitor",
  }.freeze

  # The kinds a user can add, in menu order. Anything else is rejected by the
  # board so a malformed toggle / a stale persisted field can never reach the
  # render layer.
  KINDS = ["clock", "calendar", "monitor"].freeze

  def self.kind?(k) = KINDS.include?(k.to_s)
  def self.size(k) = SIZE[k.to_s] || [200, 100]
  def self.default_pos(k) = DEFAULT_POS[k.to_s] || [40, 40]
  def self.label(k) = LABEL[k.to_s] || k.to_s.capitalize

  attr_reader :kind
  attr_accessor :x, :y
  # Per-tile render cache, filled lazily by the Compositor (14_applets.rb): the
  # base64 RGBA buffer, an "already handed to the JS ImageData cache" flag, and
  # the content signature the buffer was rendered for (so a clock repaints once
  # a second, a monitor on a value change, a calendar on a month change — not
  # every frame). Nil in the pure model.
  attr_accessor :b64, :blitted, :sig

  def initialize(kind, x, y)
    @kind    = kind.to_s
    @x       = x.to_i
    @y       = y.to_i
    @b64     = nil
    @blitted = false
    @sig     = nil
  end

  def w = Applet.size(@kind)[0]
  def h = Applet.size(@kind)[1]

  # Per-tile key for the JS-side RGBA ImageData cache. Stable per kind (at most
  # one live tile of each kind), so a position-only change (a drag) re-presents
  # the SAME cached buffer at the new coordinates.
  def key = "applet##{@kind}"

  # Is (px, py) inside this tile's rectangle?
  def contains?(px, py)
    px >= @x && px < @x + w && py >= @y && py < @y + h
  end

  # Drop the render cache so the next frame repaints (a content change).
  def invalidate
    @b64     = nil
    @blitted = false
    @sig     = nil
    self
  end
end

# The live set of desktop applets, painted in insertion order (a later-added
# tile draws on top of an earlier one when they overlap — and wins a hit-test
# tie, mirroring WindowManager#window_at's last-wins scan). At most one tile per
# kind: re-adding a shown kind is a no-op.
class AppletBoard
  # Keep at least a tile's full extent on-screen when moving (no fully-off-screen
  # drop): #move clamps into [0 .. screen - tile] on each axis.
  EDGE = 0

  attr_reader :items

  def initialize
    @items = []
  end

  def empty? = @items.empty?
  def length = @items.length

  # The shown applet of `kind`, or nil. Walks with #each (no block-return, which
  # rbgo does not unwind — see Menu#hit_test).
  def find(kind)
    k = kind.to_s
    f = nil
    @items.each { |a| f = a if a.kind == k }
    f
  end

  def shown?(kind) = !find(kind).nil?

  # The kinds currently shown, in draw order.
  def shown_kinds
    @items.map { |a| a.kind }
  end

  # Add an applet of `kind` at (x, y) — defaulting to the kind's DEFAULT_POS.
  # A kind already on the board is returned unchanged (no duplicate). An unknown
  # kind is rejected (nil).
  def add(kind, x = nil, y = nil)
    return nil unless Applet.kind?(kind)
    existing = find(kind)
    return existing unless existing.nil?
    dp = Applet.default_pos(kind)
    a = Applet.new(kind, x.nil? ? dp[0] : x, y.nil? ? dp[1] : y)
    @items.push(a)
    a
  end

  # Remove an applet by kind. Returns it, or nil if it was not shown.
  def remove(kind)
    found = find(kind)
    @items.reject! { |a| a.kind == found.kind } unless found.nil?
    found
  end

  # Toggle a kind: add it at its default position if hidden, remove it if shown.
  # Returns [:added, applet] / [:removed, applet], or nil for an unknown kind.
  def toggle(kind)
    return nil unless Applet.kind?(kind)
    if shown?(kind)
      [:removed, remove(kind)]
    else
      [:added, add(kind)]
    end
  end

  # Move `applet` to (x, y), clamped so the whole tile stays on a
  # screen_w x screen_h desktop. Returns the applet (or nil if applet is nil).
  def move(applet, x, y, screen_w, screen_h)
    return nil if applet.nil?
    maxx = [screen_w - applet.w, EDGE].max
    maxy = [screen_h - applet.h, EDGE].max
    applet.x = [[x, 0].max, maxx].min
    applet.y = [[y, 0].max, maxy].min
    applet
  end

  # Topmost applet under (px, py), or nil. Later-added tiles (drawn on top) win
  # a tie, mirroring WindowManager#window_at.
  def at(px, py)
    hit = nil
    @items.each { |a| hit = a if a.contains?(px, py) }
    hit
  end

  # Serialize the shown applets + positions to a compact string round-tripped
  # through localStorage: "clock:40,40;monitor:300,120". Kind names carry no
  # separator character, so no escaping is needed.
  def serialize
    @items.map { |a| "#{a.kind}:#{a.x},#{a.y}" }.join(";")
  end

  # Parse a #serialize string back onto the board (replacing the current items),
  # clamped to the given desktop size. Malformed / unknown-kind fields are
  # skipped so a corrupt or forward-version blob can never crash the boot.
  # Returns self.
  def parse(text, screen_w, screen_h)
    @items = []
    return self if text.nil?
    text.to_s.split(";").each do |field|
      next if field.empty?
      parts = field.split(":")
      next if parts.length < 2
      kind = parts[0]
      next unless Applet.kind?(kind)
      xy = parts[1].split(",")
      next if xy.length < 2
      a = add(kind, xy[0].to_i, xy[1].to_i)
      move(a, a.x, a.y, screen_w, screen_h) unless a.nil?
    end
    self
  end
end
