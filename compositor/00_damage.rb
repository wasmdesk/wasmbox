# ---------------------------------------------------------------------------
# DamageSet — the per-frame dirty-rectangle accumulator.
#
# The compositor used to re-composite the WHOLE screen on every animation
# frame (draw_desktop + every window + HUD), regardless of whether anything
# had actually changed. That is a lot of wasm<->JS bridge crossings per frame
# even when the desktop is idle. DamageSet is the Ruby half of a dirty-rect
# model: Compositor#compute_damage diffs the current scene against the last
# composited one and records, as screen-space rectangles, exactly the regions
# that changed. Compositor#render then either
#   - skips entirely (no damage → idle → zero work), or
#   - re-composites only the union of the damaged rectangles (draw_regions),
#     clipping each so out-of-region pixels are retained from the prior frame.
#
# This extends the existing commit-seq gate (which already decided *whether*
# to re-copy an external surface's pixels on the JS side) to also decide
# *which region* of the compositor's own canvas to repaint on the Ruby side.
#
# A `full` flag short-circuits the rect machinery for the cases where a
# whole-screen repaint is simplest and safest (first frame, viewport resize,
# frame/palette swap, workspace switch, an open menu). When too many disjoint
# rectangles accumulate the set also collapses to `full`, bounding the
# per-frame book-keeping.
#
# Pure data + geometry: no JS here, so it is exercised natively by cmd/rbtest.
# ---------------------------------------------------------------------------
class DamageSet
  # Above this many disjoint rectangles a full recomposite is cheaper than
  # walking + clipping each region separately, so we collapse to `full`.
  MAX_RECTS = 8

  def initialize
    @rects = []
    @full = false
  end

  def clear
    @rects = []
    @full = false
    self
  end

  # Force a whole-screen recomposite this frame.
  def full!
    @full = true
    self
  end

  def full? = @full

  # Nothing to do this frame: no full flag and no accumulated rectangles.
  def empty? = !@full && @rects.empty?

  # The accumulated rectangles (each a { x:, y:, w:, h: } Hash). Meaningless
  # when full? — callers check full? first.
  def rects = @rects

  # Add a screen-space damaged rectangle. Empty/negative rects are dropped.
  # Accumulating past MAX_RECTS collapses the set to a full recomposite so the
  # per-frame region walk never grows unbounded.
  def add(x, y, w, h)
    return self if @full
    return self if w <= 0 || h <= 0
    @rects << { x: x, y: y, w: w, h: h }
    @full = true if @rects.length > MAX_RECTS
    self
  end

  # Add a rect given as [x, y, w, h] (the shape frame_rect / body_rect return).
  def add_rect(r)
    add(r[0], r[1], r[2], r[3])
  end

  # Total damaged area across all rectangles (overlaps double-counted). Used by
  # callers/tests that want a coarse "how much are we repainting" figure.
  def total_area
    a = 0
    @rects.each { |r| a += r[:w] * r[:h] }
    a
  end

  # Axis-aligned bounding-box overlap between a damage rect Hash and a bounds
  # array [x, y, w, h]. Half-open on the far edges, matching Window#hit?.
  def self.rect_intersects?(r, bounds)
    bx, by, bw, bh = bounds
    rx = r[:x]; ry = r[:y]; rw = r[:w]; rh = r[:h]
    return false if rx >= bx + bw || bx >= rx + rw
    return false if ry >= by + bh || by >= ry + rh
    true
  end

  # Bounding rectangle of two [x, y, w, h] arrays, returned as a { x:, y:, w:,
  # h: } Hash. Used to fold a window's OLD and NEW extents into a single
  # damaged region on a move/resize (the classic dirty-rect union), so a small
  # drag damages one modest rectangle instead of two.
  def self.union(a, b)
    x0 = [a[0], b[0]].min
    y0 = [a[1], b[1]].min
    x1 = [a[0] + a[2], b[0] + b[2]].max
    y1 = [a[1] + a[3], b[1] + b[3]].max
    { x: x0, y: y0, w: x1 - x0, h: y1 - y0 }
  end
end
