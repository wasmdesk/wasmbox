# ---------------------------------------------------------------------------
# Accessibility bridge — the JS-touching half of the a11y feature (pure model in
# 05_a11y.rb). It (1) publishes the ARIA tree to the main thread whenever it
# changes, damage-gated by A11yTree.signature so it never posts on an idle or
# geometry-only frame; (2) receives screen-reader-driven actions on the Ruby
# event bus and routes them to the SAME WindowManager paths the titlebar buttons
# use; and (3) chains the per-frame tick so publishing needs no edit to
# 06_core.rb's render loop.
#
# The compositor runs in a Web Worker with no DOM; a screen reader reads the
# accessibility DOM on the MAIN thread. So the tree cannot be built here as DOM —
# it is serialised to JSON and posted to the main thread, where a11y-bridge.js
# reconciles it into a live ARIA element tree (role=application > role=window >
# button). This is the exact shape of the AT-SPI bridge, only the transport
# differs: there the tree crosses D-Bus to at-spi2; here it crosses postMessage
# to the accessibility DOM. Actions travel back the same way in reverse
# (a11y-bridge.js -> worker wasmboxA11yAction -> bus -> a11y_dispatch).
#
# Loads after 06_core.rb, so `class Compositor` is already defined and we reopen
# it. The tick alias is installed at load time (before the first requestAnimation-
# Frame fires), so every subsequent frame runs the original tick and then the
# a11y publish.
class Compositor
  # The windows exposed to a screen reader: decorated surfaces (panels + popups
  # excluded — a dock/menu is chrome, not an app window) that are either on the
  # active workspace or minimized (a folded window must still be reachable to be
  # restored). Bottom-to-top stack order, matching the iconbar's left-to-right
  # order. Scans explicitly rather than chaining select so it does not lean on
  # rbgo block-return semantics.
  def a11y_windows
    active = @wm.active_workspace
    list = []
    @wm.windows.each do |w|
      next unless w.decorated?
      next unless w.minimized? || w.workspace == active
      list << w
    end
    list
  end

  # Per-frame publish (called from the aliased tick). Builds the current tree,
  # compares its signature to the last published one, and ships JSON to the main
  # thread only on a real change. Registers the action bus listener lazily on the
  # first call, once @bus exists (created at boot in expose_external_spawner).
  def publish_a11y
    wire_a11y_bus unless @a11y_wired
    wins = a11y_windows
    sig = A11yTree.signature(wins)
    return nil if sig == @a11y_sig
    @a11y_sig = sig
    json = A11yTree.to_json(A11yTree.build(wins))
    JS.global.call("wasmboxA11yPublish", json)
    nil
  end

  # Register the screen-reader action listener on the Ruby event bus. The main
  # thread posts an `a11y_action` message; the worker's wasmboxA11yAction hook
  # dispatches a `wasmbox-a11y-action` CustomEvent here carrying { action, id }.
  # No-op until @bus exists (the very first frame may precede it in pathological
  # boots); @a11y_wired latches only once the listener is actually attached, so a
  # missed early frame simply retries next frame.
  def wire_a11y_bus
    return nil if @bus.nil?
    @a11y_wired = true
    @bus.on("wasmbox-a11y-action") do |e|
      d = e.get("detail")
      a11y_dispatch(d.get("action").to_s, d.get("id").to_i)
    end
    nil
  end

  # Perform a screen-reader action on window `id`. Each arm reuses the EXACT
  # sequence the pointer path runs (06_core.rb#on_mousedown titlebar buttons /
  # the dock restore), so an AT activation is indistinguishable from a click:
  # focus + raise, minimise, restore, or the full close (dismiss child popups,
  # unstack, tell the client, refresh the iconbar). Unknown id / action is a
  # silent no-op. Returns the affected window, or nil.
  def a11y_dispatch(action, id)
    win = @wm.find(id)
    return nil if win.nil?
    case action
    when "focus"
      @wm.focus(win)
      notify_windows_changed
    when "minimize"
      @wm.minimize(win)
      notify_windows_changed
    when "restore"
      @wm.restore_window(win)
      notify_windows_changed
    when "close"
      dismiss_popups(@wm.child_popups(win.id))
      @wm.close(win)
      notify_closed(win, "user")
      notify_windows_changed
    else
      return nil
    end
    win
  end

  # Chain the per-frame tick so the ARIA tree publishes after every frame's WM
  # state settles, WITHOUT editing 06_core.rb. alias_method captures the original
  # tick under a private name; the new tick runs it, then publishes (itself
  # signature-gated, so this adds one cheap string build + compare per frame and
  # a postMessage only on change).
  alias_method :__a11y_pre_tick, :tick
  def tick(t)
    __a11y_pre_tick(t)
    publish_a11y
  end
end
