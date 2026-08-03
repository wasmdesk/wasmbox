# ---------------------------------------------------------------------------
# Alt-Tab window switcher domain model — a Batch-4 window-management feature
# (2026-08-03), sibling of window snapping (04_window_manager.rb). Holding
# Alt+Tab pops a centered strip of window THUMBNAILS; each Tab advances the
# selection (Shift+Tab reverses); releasing Alt (or Enter) focuses the chosen
# window; Escape cancels. This file owns ONLY the pure state machine — the
# ordered candidate ring, the moving selection cursor, and the open/advance/
# commit/cancel transitions. The render + wire glue (thumbnail pixels, the
# Widgets strip, the centered overlay blit, the keyboard + probe hooks) lives
# in 15_alttab.rb, which is JS-touching and so loads after 06_core.rb.
#
# The split mirrors 05_applets.rb / 05_notifications.rb / 05_tray.rb: this file
# sorts BELOW 06_ so cmd/rbtest — which loads 01_..05_ only — exercises the
# whole open / advance / wrap / commit / cancel cursor logic without a browser,
# exactly like the WindowManager, the toast stack, the tray and the applet board
# next to it. AltTab holds no Window internals: it is handed an opaque, already-
# ordered candidate list (WindowManager#cycle_candidates) and only indexes into
# it, so the model is trivially testable with plain stand-in objects.
# ---------------------------------------------------------------------------

# The Alt-Tab switcher cursor. `items` is the ordered candidate ring the caller
# supplies (WindowManager#cycle_candidates — the cycleable windows on the active
# workspace, most-recently-focused FIRST); `index` is the current selection into
# it; `active?` is whether the overlay is up (a keyboard grab is in effect while
# it is). All transitions are no-ops unless they make sense, so a stray key or a
# double-release can never crash the compositor.
class AltTab
  attr_reader :items, :index

  def initialize
    @items  = []
    @index  = 0
    @active = false
  end

  # Is the switcher currently up (grabbing the keyboard)?
  def active? = @active

  # How many candidates are in the ring (0 when closed).
  def count = @items.length

  # Open the switcher over `items` (an already-ordered candidate list), with the
  # cursor on the FIRST entry — the currently-focused window. The first Tab then
  # advances OFF it onto the next window, the familiar Alt-Tab behaviour. An
  # empty / nil list leaves the switcher closed (returns nil); otherwise returns
  # self. Re-opening while already active is a no-op (returns self) so a repeated
  # first-press never resets a walk in progress.
  def open(items)
    return self if @active
    return nil if items.nil? || items.empty?
    @items  = items
    @index  = 0
    @active = true
    self
  end

  # Move the cursor one step: forward by default, backward when `reverse` is
  # true, wrapping at either end of the ring. No-op (returns nil) while closed or
  # over an empty ring. Returns the new index.
  def advance(reverse = false)
    return nil unless @active
    n = @items.length
    return nil if n == 0
    step = reverse ? -1 : 1
    @index = (@index + step) % n
  end

  # The currently-selected candidate, or nil while closed / empty.
  def selected
    return nil unless @active
    return nil if @items.empty?
    @items[@index]
  end

  # Close the switcher and return the selected candidate (the window to focus).
  # Idempotent: a second commit after close returns nil.
  def commit
    win = selected
    close
    win
  end

  # Close the switcher WITHOUT choosing (Escape). Returns nil.
  def cancel
    close
    nil
  end

  # Tear down the open state. Keeps `items` referenced only while active — clears
  # them on close so a closed switcher holds no window references.
  def close
    @active = false
    @items  = []
    @index  = 0
    nil
  end
end
