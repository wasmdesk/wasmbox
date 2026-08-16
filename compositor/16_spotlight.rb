# ---------------------------------------------------------------------------
# Spotlight launcher — the compositor render + wire glue over the pure cursor
# model in 05_spotlight.rb. A Batch-4 feature (2026-08-03), sibling of the
# Alt-Tab switcher (15_alttab.rb) and window snapping (06_core.rb).
#
# Press ⌘/Ctrl-Space and a centered search palette pops up: a query field over a
# filtered list of the launchable apps (the SAME LAUNCHABLE registry the dock +
# root-menu use). Typing fuzzy-filters the list, Up/Down move the highlight,
# Enter launches the highlighted app (through the existing do_launch path — the
# very path the root menu's "Applications" entries use), Escape cancels. The
# palette is a go-widgets tree:
#
#   Widgets.entry(query) + Widgets.list_box(labels) -> Widgets.v_box
#   -> Widgets.render -> RGBA bytes -> base64 -> JS wasmboxBlitRGBAOver
#   (alpha-composited drawImage) centered OVER the windows — the SAME seam the
#   HUD (09), toasts (12), applets (14) and the Alt-Tab strip (15) use.
#
# Driven through the go-widgets v0.86 CommandPalette accessors (the gap the
# v0.7.0 render-only black box left is now closed): a persistent
# Widgets.command_palette holds the launchable commands, and this file feeds it
# the host's keystrokes + queries and reads its filtered view back:
#   * set_query / query               — seed + read the live search text
#   * handle_key(id, {kind, code})    — route a typed char / Backspace / arrows /
#                                        Enter / Escape straight into the palette
#                                        (Enter surfaces {fired, repaint}, fired
#                                        carrying the chosen command's action)
#   * move_selection / selected        — host-driven Up/Down + read the cursor
#   * filtered_commands                — the exact visible rows this file renders
# The palette's own substring filter REPLACES the compositor's hand-rolled fuzzy
# match: draw_spotlight + the probe publish both read filtered_commands / query /
# selected, so the widget is the single authority on what matches. The pure
# Spotlight model (05_spotlight.rb, rbtest-tested) is kept ONLY for the lifecycle
# (active? / open / cancel) and for building the ordered app list — neither of
# which is JS-touching — so cmd/rbtest still exercises the launcher's open/close
# + app-list resolution off-wasm. Each command's action id IS its launchable app
# id, so Enter's fired id dispatches straight through do_launch.
#
# Because a headless ⌘/Ctrl+Space chord is unreliable, a probe-drivable hook
# __wasmboxSpotlight(cmd) drives the exact same transitions the real keydown path
# does ("open" / "query:<text>" / "type:<chars>" / "backspace" / "down" / "up" /
# "commit" / "cancel"), and draw_spotlight publishes the live overlay geometry +
# query + selection + last-launched app through wasmboxPublishSpotlight so
# test/probe-spotlight.mjs can assert the centered palette, the fuzzy filter and
# the launch deterministically without racing a client's wasm load.
#
# Gated behind Compositor::SPOTLIGHT so `main` stays shippable during the live
# co-edit — flip it to false to disable the overlay + every keydown/probe hook
# below with zero other changes.
# ---------------------------------------------------------------------------
require "widgets"
require "base64"

class Compositor
  # Master switch for the whole Spotlight launcher feature. `false` makes every
  # keydown/probe hook + draw_spotlight a no-op.
  SPOTLIGHT = true

  # Overlay geometry. A fixed-width card plate (translucent surface + hairline
  # border, like the Alt-Tab / applet plate) centered on the desktop, holding a
  # query row above a result list. The list caps at SPOTLIGHT_MAX_ROWS visible
  # rows so a long registry never grows the panel off-screen.
  SPOTLIGHT_W        = 440
  SPOTLIGHT_PAD      = 16
  SPOTLIGHT_QUERY_H  = 30
  SPOTLIGHT_ROW_H    = 22
  SPOTLIGHT_GAP      = 10
  SPOTLIGHT_MAX_ROWS = 8
  SPOTLIGHT_PLATE    = "rgba(28, 30, 40, 0.95)"
  SPOTLIGHT_BORDER   = "rgba(90, 96, 114, 0.95)"
  # Placeholder shown in the query field before the user types anything.
  SPOTLIGHT_PROMPT   = "Search apps"

  # Lazily-built launcher cursor (model in 05_spotlight.rb, loaded before this
  # file). Built on first access so Compositor#initialize (06_core.rb) needs no
  # edit and pure rbtest — which never loads this file — is unaffected.
  def spotlight
    @spotlight ||= Spotlight.new
  end

  # Is the palette currently up (a keyboard grab is in effect)?
  def spotlight_active? = SPOTLIGHT && spotlight.active?

  # The ordered launchable app list [{ id:, label: }] the palette searches,
  # built from the LAUNCHABLE registry via RootMenu's friendly labels + hidden
  # set (the single source of truth the dock + root-menu already share).
  def spotlight_app_list
    Spotlight.app_list(WindowManager::LAUNCHABLE, RootMenu::APP_LABELS, RootMenu::HIDDEN)
  end

  # The persistent CommandPalette handle backing the launcher (go-widgets v0.86).
  # (Re)built from the current app list on each open — each command is
  # { "label" => friendly label, "action" => the launchable app id }, so Enter's
  # fired action id dispatches straight through do_launch. Rebuilt (not reused)
  # per open so a change to the launchable set between opens is always reflected;
  # set_visible(true) on open clears any prior query + selection.
  def spotlight_build_palette(apps)
    cmds = apps.map { |a| { "label" => a[:label].to_s, "action" => a[:id].to_s } }
    @palette = Widgets.command_palette(cmds)
    Widgets.set_visible(@palette, true)
    @palette
  end

  # --- transitions (shared by the real keydown path + the probe hook) ---------

  # Open the palette over the current app list: mark the lifecycle active (the
  # pure model) AND build the CommandPalette widget that owns the filtering. No-op
  # when the flag is off, the palette is already up, or there is nothing
  # launchable. Returns self / nil.
  def spotlight_open
    return nil unless SPOTLIGHT
    return nil if spotlight.active?
    apps = spotlight_app_list
    return nil if apps.empty?
    spotlight.open(apps)
    spotlight_build_palette(apps)
    spotlight
  end

  # Feed one typed character into the palette's query via handle_key (a printable
  # keydown, or a char from the probe's "type:" command) — the widget extends its
  # query + re-filters. No-op when the palette is not up.
  def spotlight_type(ch)
    return nil unless spotlight_active?
    Widgets.handle_key(@palette, { "kind" => "char", "code" => ch.to_s })
  end

  # Delete the last query character via the palette's Backspace key. No-op when
  # not up.
  def spotlight_backspace
    return nil unless spotlight_active?
    Widgets.handle_key(@palette, { "kind" => "keydown", "code" => "Backspace" })
  end

  # Replace the whole palette query (the probe's "query:" command drives typing
  # this way), re-filtering as typing would. No-op when not up.
  def spotlight_set_query(q)
    return nil unless spotlight_active?
    Widgets.set_query(@palette, q.to_s)
  end

  # Move the palette highlight (delta >= 0 = down, else up), clamped at both ends
  # by the widget. No-op when not up.
  def spotlight_move(delta)
    return nil unless spotlight_active?
    Widgets.move_selection(@palette, delta)
  end

  # Commit the highlighted app: route an Enter into the palette (the widget fires
  # the selected command's action + dismisses itself), close the lifecycle, and
  # LAUNCH the fired app id through the same do_launch path the root menu uses.
  # handle_key returns a Dispatch-shaped { "fired" => [action, ...] }, so the
  # launched id is the first fired action. Records the launched id + a launch
  # counter (published for the probe) only when do_launch actually dispatched a
  # spawn, so the signal is deterministic even though the spawned client's window
  # registers asynchronously. No-op (nil) when the palette is not up or nothing
  # is selected. Returns the launched id.
  def spotlight_commit
    return nil unless spotlight_active?
    res = Widgets.handle_key(@palette, { "kind" => "keydown", "code" => "Enter" })
    spotlight.cancel
    fired = res.nil? ? nil : res["fired"]
    return nil if fired.nil? || fired.empty?
    id = fired[0].to_s
    return nil if id.empty?
    if do_launch(id) == :launched
      @spotlight_launched   = id
      @spotlight_launch_seq = (@spotlight_launch_seq || 0) + 1
    end
    id
  end

  # Cancel (Escape): dismiss the palette widget + close the lifecycle, launching
  # nothing.
  def spotlight_cancel
    return nil unless SPOTLIGHT
    Widgets.set_visible(@palette, false) unless @palette.nil?
    spotlight.cancel
    nil
  end

  # Route a keydown while the palette owns the keyboard. Enter launches, Escape
  # cancels, arrows move, Backspace edits, and any single printable character
  # (a KeyboardEvent.key of length 1) filters — as long as no ⌘/Ctrl/Alt chord
  # is held (so the ⌘/Ctrl+Space that opened it, and other chords, never leak in
  # as typed text). Every other named key is swallowed so nothing reaches a
  # client mid-search.
  def spotlight_key(key, e)
    case key
    when "Enter", "Return" then spotlight_commit
    when "Escape"          then spotlight_cancel
    when "ArrowDown"       then spotlight_move(1)
    when "ArrowUp"         then spotlight_move(-1)
    when "Backspace"       then spotlight_backspace
    else
      spotlight_type(key) if spotlight_printable?(key, e)
    end
    nil
  end

  # A keydown is a printable character to feed into the query when its key is a
  # single character (letters/digits/space/punctuation report length-1 keys;
  # named keys like "Shift"/"ArrowLeft"/"Meta" are longer) AND no chord modifier
  # is held (⌘/Ctrl/Alt + letter is a shortcut, not text).
  def spotlight_printable?(key, e)
    return false unless key.to_s.length == 1
    return false if e[:metaKey] || e[:ctrlKey] || e[:altKey]
    true
  end

  # The Spotlight open chord: ⌘/Ctrl + Space. ⌘+Space is the primary binding
  # (macOS Spotlight muscle memory); Ctrl+Space is accepted as the equivalent
  # the probe (and users whose OS eats ⌘+Space) can always drive — the same
  # ⌘-or-Ctrl idiom the snap + Alt-Tab chords use. A KeyboardEvent for Space
  # reports key " ".
  def spotlight_open_chord?(key, e)
    return false unless key == " "
    e[:metaKey] || e[:ctrlKey] ? true : false
  end

  # Probe entry point (__wasmboxSpotlight in compositor.worker.js dispatches here
  # via the bus). Drives the SAME transitions the keyboard does. The detail is a
  # command string, optionally "<cmd>:<arg>":
  #   open | commit | cancel | backspace | down | up
  #   query:<text>   set the whole query (fuzzy-filter to it)
  #   type:<chars>   append each character in turn (exercises the type() path)
  # Unknown commands are ignored.
  def spotlight_probe(cmd)
    s = cmd.to_s
    arg = ""
    name = s
    idx = s.index(":")
    unless idx.nil?
      name = s[0, idx]
      arg  = s[(idx + 1), s.length - idx - 1]
    end
    case name
    when "open"      then spotlight_open
    when "commit"    then spotlight_commit
    when "cancel"    then spotlight_cancel
    when "backspace" then spotlight_backspace
    when "down"      then spotlight_move(1)
    when "up"        then spotlight_move(-1)
    when "query"     then spotlight_set_query(arg)
    when "type"      then spotlight_type_string(arg)
    end
  end

  # Append every character of `str` to the query one at a time, through the same
  # single-char type() the keyboard uses (so the probe's "type:term" walks the
  # exact per-keystroke filter path).
  def spotlight_type_string(str)
    i = 0
    s = str.to_s
    n = s.length
    while i < n
      spotlight_type(s[i, 1])
      i += 1
    end
    nil
  end

  # --- render -----------------------------------------------------------------
  # Draw the palette overlay: a centered card plate holding a query field over
  # the filtered result list, the highlighted row selected, blitted OVER the
  # windows. Publishes the live overlay state every frame (active or not) so the
  # probe can read it. No-op paint when the flag is off or the palette is closed
  # — but the publish still runs so the probe sees "inactive".
  def draw_spotlight
    return nil unless SPOTLIGHT
    unless spotlight.active?
      publish_spotlight_inactive
      return nil
    end
    # Read the live view from the CommandPalette widget (the filtering authority):
    # the query text, the filtered rows and the selected index.
    query    = Widgets.query(@palette).to_s
    results  = Widgets.filtered_commands(@palette)
    index    = Widgets.selected(@palette).to_i
    rows    = spotlight_visible_rows(results.length)
    inner_w = SPOTLIGHT_W
    inner_h = SPOTLIGHT_QUERY_H + SPOTLIGHT_GAP + rows * SPOTLIGHT_ROW_H
    panel_w = inner_w + 2 * SPOTLIGHT_PAD
    panel_h = inner_h + 2 * SPOTLIGHT_PAD
    panel_x = (@width - panel_w) / 2
    panel_y = (@height - panel_h) / 2

    # Card plate (cheap ctx fill each frame) drawn first so the transparent-ground
    # widget content composites over a solid panel.
    fill_rect([panel_x, panel_y, panel_w, panel_h], SPOTLIGHT_PLATE)
    stroke_rect([panel_x, panel_y, panel_w, panel_h], SPOTLIGHT_BORDER, 1)

    sig = spotlight_paint_sig(query, results, index, inner_w, inner_h)
    if @spotlight_b64.nil? || @spotlight_sig != sig
      @spotlight_b64 = render_spotlight_panel(query, results, index, inner_w, inner_h)
      @spotlight_sig = sig
      @spotlight_blitted = false
    end
    dx = panel_x + SPOTLIGHT_PAD
    dy = panel_y + SPOTLIGHT_PAD
    if @spotlight_blitted
      JS.global.call("wasmboxBlitRGBAOver", @ctx, "", inner_w, inner_h, dx, dy, "spotlight")
    else
      JS.global.call("wasmboxBlitRGBAOver", @ctx, @spotlight_b64, inner_w, inner_h, dx, dy, "spotlight")
      @spotlight_blitted = true
    end

    publish_spotlight_active(query, results, index, panel_x, panel_y, panel_w, panel_h)
    nil
  end

  # The number of result rows to draw: at least one (so an empty match still
  # gives the panel body height + a visible "no results" gap), capped at
  # SPOTLIGHT_MAX_ROWS so the panel stays on-screen for a long registry.
  def spotlight_visible_rows(n)
    return 1 if n < 1
    n > SPOTLIGHT_MAX_ROWS ? SPOTLIGHT_MAX_ROWS : n
  end

  # Build the palette's RGBA buffer (base64): a v_box of a query Entry over a
  # ListBox of the filtered_commands labels, the highlighted row selected. The
  # rows ARE the widget's filtered_commands (each a { "label" => ... } Hash), so
  # what paints is exactly what the CommandPalette matched.
  def render_spotlight_panel(query, results, sel, w, h)
    col = Widgets.v_box
    Widgets.set_spacing(col, SPOTLIGHT_GAP)
    entry = Widgets.entry(spotlight_query_display(query), "")
    Widgets.add_fixed(col, entry, SPOTLIGHT_QUERY_H)
    labels = []
    results.each { |cmd| labels << cmd["label"] }
    list = Widgets.list_box(labels, "")
    Widgets.select(list, sel) unless results.empty?
    Widgets.add_flex(col, list, 1)
    Widgets.layout(col, w, h)
    img = Widgets.render(col, w, h)
    Base64.strict_encode64(img["pixels"])
  end

  # The query field's displayed text: the live query, or the placeholder prompt
  # when it is empty so the palette reads as a search box before any typing.
  def spotlight_query_display(query)
    q = query.to_s
    q.empty? ? SPOTLIGHT_PROMPT : q
  end

  # A content signature that changes exactly when the panel's pixels must: the
  # query, the ordered filtered_commands labels, the selected index and the
  # buffer size.
  def spotlight_paint_sig(query, results, sel, w, h)
    parts = ["#{w}x#{h}", "q:#{query}", "s#{sel}"]
    results.each { |cmd| parts << cmd["label"].to_s }
    parts.join("|")
  end

  # --- probe publish ----------------------------------------------------------
  # Publish the live overlay state to the JS test hook every frame. The probe
  # reads it via __wasmboxSpotlightState(): the active flag, the result count,
  # the selected index, the centered panel rect (to assert centering), the live
  # query + the selected label (to assert the fuzzy filter), and the last-
  # launched app id + a monotonic launch counter (to assert Enter launched the
  # highlighted app and Escape did not).
  def publish_spotlight_active(query, results, index, px, py, pw, ph)
    sel_label = ""
    unless results.empty?
      cmd = results[index]
      sel_label = cmd["label"].to_s unless cmd.nil?
    end
    JS.global.call("wasmboxPublishSpotlight",
      1, results.length, index, px, py, pw, ph,
      query.to_s, sel_label,
      (@spotlight_launched || "").to_s, (@spotlight_launch_seq || 0))
    nil
  end

  def publish_spotlight_inactive
    JS.global.call("wasmboxPublishSpotlight",
      0, 0, 0, 0, 0, 0, 0,
      "", "",
      (@spotlight_launched || "").to_s, (@spotlight_launch_seq || 0))
    nil
  end
end
