# ---------------------------------------------------------------------------
# Spotlight launcher domain model — a Batch-4 feature (2026-08-03), sibling of
# the Alt-Tab switcher (05_alttab.rb) and window snapping (04_window_manager.rb).
# ⌘/Ctrl-Space pops a centered search palette; typing fuzzy-filters the list of
# launchable apps; Up/Down move the highlight; Enter launches the selected app;
# Escape cancels. This file owns ONLY the pure state machine — the ordered app
# list, the live query string, the fuzzy-filtered result view and the moving
# selection cursor, plus the open/type/backspace/move/commit/cancel transitions.
# The render + wire glue (the Widgets search Entry + filtered ListBox, the
# centered overlay blit, the keyboard grab + probe hooks, and the do_launch
# dispatch) lives in 16_spotlight.rb, which is JS-touching and so loads after
# 06_core.rb.
#
# The split mirrors 05_alttab.rb / 05_applets.rb / 05_tray.rb: this file sorts
# BELOW 06_ so cmd/rbtest — which loads 01_..05_ only — exercises the whole
# open / type / filter / move / commit / cancel logic AND the launch-target
# resolution against the LAUNCHABLE registry WITHOUT a browser and WITHOUT ever
# spawning a client. Spotlight holds no compositor internals: it is handed an
# opaque, already-ordered app list (each entry a { id:, label: } Hash) and only
# indexes + filters it, so the model is trivially testable with plain Hashes.
# ---------------------------------------------------------------------------

# The Spotlight launcher cursor. `apps` is the ordered app list the caller
# supplies (Spotlight.app_list over the LAUNCHABLE registry); `query` is the
# live search text; `results` is the fuzzy-filtered subset (a view into `apps`,
# app order preserved); `index` is the selection into `results`; `active?` is
# whether the palette is up (a keyboard grab is in effect while it is). Every
# transition is a no-op unless it makes sense, so a stray key or a double-close
# can never crash the compositor.
class Spotlight
  attr_reader :query, :index

  def initialize
    @apps     = []
    @filtered = []
    @query    = ""
    @index    = 0
    @active   = false
  end

  # Is the palette currently up (grabbing the keyboard)?
  def active? = @active

  # The fuzzy-filtered result view (an ordered subset of the app list). Empty
  # while closed OR when the query matches nothing.
  def results = @filtered

  # How many results the current query matches (0 while closed).
  def count = @filtered.length

  # Open the palette over `apps` (an already-ordered [{ id:, label: }] list),
  # with an empty query (so every app shows) and the cursor on the FIRST result.
  # An empty / nil list leaves the palette closed (returns nil); otherwise
  # returns self. Re-opening while already active is a no-op (returns self) so a
  # repeated chord never wipes a query in progress.
  def open(apps)
    return self if @active
    return nil if apps.nil? || apps.empty?
    @apps   = apps
    @query  = ""
    @index  = 0
    @active = true
    refilter
    self
  end

  # The currently-highlighted app entry ({ id:, label: }), or nil while closed /
  # when nothing matches.
  def selected
    return nil unless @active
    return nil if @filtered.empty?
    @filtered[@index]
  end

  # Append one character to the query and re-filter, parking the selection back
  # on the first result. No-op (nil) while closed or on an empty character.
  # Returns the new query.
  def type(ch)
    return nil unless @active
    s = ch.to_s
    return @query if s.empty?
    @query = @query + s
    refilter
    @query
  end

  # Delete the last character of the query and re-filter. No-op on an empty
  # query (returns ""). Returns the new query.
  def backspace
    return nil unless @active
    return @query if @query.empty?
    @query = @query[0, @query.length - 1]
    refilter
    @query
  end

  # Replace the whole query and re-filter (the probe drives typing this way, and
  # it keeps the model editor-agnostic). No-op while closed. Returns the query.
  def set_query(q)
    return nil unless @active
    @query = q.to_s
    refilter
    @query
  end

  # Move the selection: forward when `delta >= 0`, backward otherwise, wrapping
  # at either end of the result list. No-op (nil) while closed or over an empty
  # result list. Returns the new index.
  def move(delta)
    return nil unless @active
    n = @filtered.length
    return nil if n == 0
    step = delta < 0 ? -1 : 1
    @index = (@index + step) % n
  end

  # Close the palette and return the selected app entry (the app to launch), or
  # nil when nothing is selected. Idempotent: a second commit after close is nil.
  def commit
    app = selected
    close
    app
  end

  # Close the palette WITHOUT choosing (Escape). Returns nil.
  def cancel
    close
    nil
  end

  # Tear down the open state. Clears the app list + query so a closed palette
  # holds no references and always reopens fresh.
  def close
    @active   = false
    @apps     = []
    @filtered = []
    @query    = ""
    @index    = 0
    nil
  end

  # Recompute the filtered result view from the current query (case-insensitive
  # fuzzy subsequence match on the label, app order preserved) and re-park the
  # selection on the first result. An empty query matches everything.
  def refilter
    q = @query.to_s.downcase
    @filtered = @apps.select do |app|
      q.empty? || Spotlight.fuzzy_match?(app[:label].to_s.downcase, q)
    end
    @index = 0
    nil
  end

  # Fuzzy subsequence match: every character of `query` appears in `text` in
  # order (both already lowercased). An empty query always matches. A plain
  # substring is a special case of a subsequence, so this covers "term" ->
  # "Terminal" AND "cl" -> "Calculator". Pure integer scan, no regexp.
  def self.fuzzy_match?(text, query)
    return true if query.empty?
    ti = 0
    qi = 0
    tlen = text.length
    qlen = query.length
    while ti < tlen && qi < qlen
      qi += 1 if text[ti, 1] == query[qi, 1]
      ti += 1
    end
    qi == qlen
  end

  # Build the ordered launchable app list [{ id:, label: }] from the LAUNCHABLE
  # registry (`launchable`), labelling each id via `labels` (an id -> friendly
  # label map; an unlabelled id falls back to the capitalized id) and skipping
  # every id in `hidden`. Order: labelled ids first, in `labels` insertion
  # order, then any remaining registry ids. This mirrors RootMenu.build_apps so
  # the Spotlight lists EXACTLY what the desktop's Applications menu does — the
  # LAUNCHABLE registry stays the single source of truth for what is launchable.
  def self.app_list(launchable, labels, hidden)
    hidden ||= []
    apps = []
    seen = {}
    labels.each do |id, label|
      next if hidden.include?(id)
      next unless launchable.key?(id)
      apps << { id: id, label: label }
      seen[id] = true
    end
    launchable.each do |id, _desc|
      next if seen[id]
      next if hidden.include?(id)
      apps << { id: id, label: id.to_s.capitalize }
    end
    apps
  end
end
