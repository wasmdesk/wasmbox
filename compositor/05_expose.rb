# ---------------------------------------------------------------------------
# Exposé / "show all windows" domain model — the LAST Batch-4 window-management
# feature (2026-08-03), sibling of the Alt-Tab switcher (05_alttab.rb), the
# Spotlight launcher (05_spotlight.rb) and window snapping (04_window_manager.rb).
# A shortcut (F3 / Mission-Control-style) spreads every window on the current
# workspace as a NON-OVERLAPPING grid of thumbnails over a dimmed desktop; the
# mouse (hover highlights, click focuses+exits) or the keyboard (arrows move a
# selection, Enter commits, Escape cancels) picks a window to focus. This file
# owns ONLY the pure state machine — the ordered candidate list, the computed
# grid dimensions, the moving 2-D selection cursor, the per-tile rectangle math
# and the click→tile resolution, plus the open/move/select/commit/cancel
# transitions. The render + wire glue (the Widgets thumbnail tiles, the dim +
# spread blit, the keyboard/mouse grab + probe hooks) lives in 17_expose.rb,
# which is JS-touching and so loads after 06_core.rb.
#
# The split mirrors 05_alttab.rb / 05_spotlight.rb: this file sorts BELOW 06_ so
# cmd/rbtest — which loads 01_..05_ only — exercises the whole grid-layout math
# (rows/cols from N, non-overlapping tile rects that fit the bounds), the 2-D
# selection move/wrap, the click→window resolution and the commit/cancel cursor
# logic WITHOUT a browser. Exposé holds no Window internals: it is handed an
# opaque, already-ordered candidate list (WindowManager#cycle_candidates — the
# SAME ring Alt-Tab uses, so panels/popups/minimized/off-workspace windows are
# already excluded) and only indexes + lays it out, so the model is trivially
# testable with plain stand-in objects.
# ---------------------------------------------------------------------------

# The Exposé spread cursor. `items` is the ordered candidate list the caller
# supplies; `cols`/`rows` are the grid dimensions computed from the count on
# open; `index` is the selected tile (row-major); `active?` is whether the
# spread is up (a keyboard/mouse grab is in effect while it is). Every
# transition is a no-op unless it makes sense, so a stray key or a double-close
# can never crash the compositor.
class Expose
  attr_reader :items, :index, :cols, :rows

  def initialize
    @items = []
    @index = 0
    @cols  = 0
    @rows  = 0
    @active = false
  end

  # Is the spread currently up (grabbing the keyboard + mouse)?
  def active? = @active

  # How many windows are spread (0 when closed).
  def count = @items.length

  # Open the spread over `items` (an already-ordered candidate list), computing
  # the grid dimensions and parking the selection on the FIRST tile (the focused
  # window — cycle_candidates puts it at the head). An empty / nil list leaves
  # the spread closed (returns nil); otherwise returns self. Re-opening while
  # already active is a no-op (returns self) so a repeated trigger never resets a
  # selection in progress.
  def open(items)
    return self if @active
    return nil if items.nil? || items.empty?
    @items  = items
    @index  = 0
    dims    = Expose.grid_dims(items.length)
    @cols   = dims[0]
    @rows   = dims[1]
    @active = true
    self
  end

  # The currently-selected candidate, or nil while closed / empty.
  def selected
    return nil unless @active
    return nil if @items.empty?
    @items[@index]
  end

  # Move the selection one cell in `dir` (:left / :right / :up / :down), wrapping
  # within the grid. Left/right wrap within the current ROW (so the cursor stays
  # on its row); up/down wrap within the current COLUMN (respecting a short last
  # row, where a column may hold one fewer tile). No-op (nil) while closed or
  # over an empty grid. Returns the new index.
  def move(dir)
    return nil unless @active
    n = @items.length
    return nil if n == 0
    r = @index / @cols
    c = @index % @cols
    case dir
    when :left, :right
      rc = row_tiles(r)
      step = (dir == :right) ? 1 : -1
      c = (c + step) % rc
      @index = r * @cols + c
    when :up, :down
      rcol = rows_in_col(c)
      step = (dir == :down) ? 1 : -1
      r = (r + step) % rcol
      @index = r * @cols + c
    end
    @index
  end

  # Jump the selection straight to tile `i` (mouse hover / click resolution).
  # No-op (nil) while closed or for an out-of-range index. Returns the new index.
  def select_index(i)
    return nil unless @active
    return nil if i < 0 || i >= @items.length
    @index = i
  end

  # Close the spread and return the selected candidate (the window to focus).
  # Idempotent: a second commit after close returns nil.
  def commit
    win = selected
    close
    win
  end

  # Close the spread WITHOUT choosing (Escape / click on empty space). Returns nil.
  def cancel
    close
    nil
  end

  # Tear down the open state — clears the candidate list so a closed spread holds
  # no window references.
  def close
    @active = false
    @items  = []
    @index  = 0
    @cols   = 0
    @rows   = 0
    nil
  end

  # --- pure grid geometry (rbtest-covered) ----------------------------------

  # Grid dimensions [cols, rows] for `n` tiles: the columns are the smallest c
  # with c*c >= n (a near-square spread), the rows the count needed to hold n
  # tiles in c columns. n=1 -> 1x1, 2 -> 2x1, 3/4 -> 2x2, 5/6 -> 3x2, 7..9 ->
  # 3x3, ... Pure integer math (no Math.sqrt), so it is bit-identical on every
  # arch and needs no float.
  def self.grid_dims(n)
    return [0, 0] if n <= 0
    c = 1
    c += 1 while c * c < n
    r = (n + c - 1) / c
    [c, r]
  end

  # How many tiles sit in row `r`: a full `cols` for every row but (possibly) the
  # last, which holds the remainder. Clamped to [0, cols].
  def row_tiles(r)
    left = @items.length - r * @cols
    return 0 if left <= 0
    left >= @cols ? @cols : left
  end

  # How many rows contain a tile in column `c`: every column up to the last row's
  # width has the full `rows`; columns past it are one shorter (the short last
  # row). At least 1 for any occupied column.
  def rows_in_col(c)
    last = @items.length - (@rows - 1) * @cols # tiles in the last row
    c < last ? @rows : @rows - 1
  end

  # The [x, y, w, h] rectangle tile `i` paints into, inside the work area
  # (ax, ay, aw, ah) with `gap` pixels of breathing room on every side of each
  # tile. Tiles sit on a cols×rows cell grid (cell = aw/cols × ah/rows); a short
  # last row is horizontally centered. Non-overlapping by construction (each tile
  # is strictly inside its own cell) and always within the work-area bounds.
  def tile_rect(i, ax, ay, aw, ah, gap)
    cw = aw / @cols
    ch = ah / @rows
    r = i / @cols
    c = i % @cols
    rc = row_tiles(r)
    row_off = (@cols - rc) * cw / 2 # center a short last row
    x = ax + row_off + c * cw + gap
    y = ay + r * ch + gap
    w = cw - 2 * gap
    h = ch - 2 * gap
    [x, y, w, h]
  end

  # The tile index whose rectangle contains (px, py), or -1 when the point is
  # over the dim gap between tiles / outside the spread. Scans in draw order so
  # the resolution matches the painted tiles exactly (the tiles never overlap, so
  # at most one contains the point).
  def tile_at(px, py, ax, ay, aw, ah, gap)
    i = 0
    n = @items.length
    while i < n
      rr = tile_rect(i, ax, ay, aw, ah, gap)
      if px >= rr[0] && px < rr[0] + rr[2] && py >= rr[1] && py < rr[1] + rr[3]
        return i
      end
      i += 1
    end
    -1
  end
end
