# ---------------------------------------------------------------------------
# Accessibility (a11y) domain model — the compositor's window/widget tree
# projected as a screen-reader-readable ARIA tree. Sibling of the Alt-Tab
# switcher (05_alttab.rb), the Spotlight launcher (05_spotlight.rb) and Exposé
# (05_expose.rb): this file owns ONLY the pure model — how a window becomes an
# ARIA node (role, label, focus flag, activatable actions), the compact damage
# signature used to publish only on change, and the JSON serialisation the JS
# bridge ships to the main thread. The JS-touching glue (the per-frame publish
# via wasmboxA11yPublish, the action bus listener, the tick chain) lives in
# 18_a11y.rb, which loads after 06_core.rb.
#
# This is the browser twin of go-widgets/window's AT-SPI bridge: there,
# WalkA11y walks the widget tree and exposes (role, label, actions) on the
# Linux accessibility bus so Orca can read + drive it; here we walk the
# compositor's window stack and mirror it into the browser's accessibility DOM
# (an ARIA tree the a11y-bridge.js reconciler materialises) so VoiceOver / Orca
# / NVDA read + drive the desktop. A canvas paints no accessible text; the ARIA
# tree is the ONLY thing a screen reader can perceive, so it must track every
# title / focus / minimise change.
#
# The split mirrors 05_expose.rb: this file sorts BELOW 06_ so cmd/rbtest —
# which loads 01_..05_ only — exercises the whole projection (node shape, action
# set per window state, the signature's sensitivity to title/focus/minimise, and
# the JSON round-trip) WITHOUT a browser. A11yTree holds no Window internals: it
# is handed window-like objects that answer id / title / role / focused? /
# minimized? / x / y / w / h, so the model is trivially testable with plain
# stand-in objects.
# ---------------------------------------------------------------------------

# A11yTree projects a list of compositor windows into an ARIA tree. Every method
# is a pure class method (no instance state) so a fresh tree is built each frame
# from the live window list and compared by signature — there is nothing to keep
# in sync. The window list handed in is ALREADY filtered by the caller
# (18_a11y.rb#a11y_windows drops panels, popups and off-workspace windows), so
# this file only projects; it never decides visibility policy.
class A11yTree
  # The desktop root's accessible name. The root carries role "application"
  # (matching the AT-SPI bridge's ROLE_APPLICATION / ROLE_FRAME root): it tells a
  # screen reader this subtree is an application that handles its own keyboard
  # navigation, which is exactly what a window manager is.
  DESKTOP_LABEL = "wasmbox desktop"

  # Build the ARIA tree from an ordered window list. Returns a plain Hash the
  # caller serialises with to_json:
  #   { role: "application", label:, active_id:, nodes: [ <window node>, ... ] }
  # active_id is the id of the focused window (what the ARIA root advertises via
  # aria-activedescendant), or nil when nothing is focused. Order is preserved
  # verbatim — the caller supplies stack order (bottom-to-top), which reads as a
  # stable list to a screen reader.
  def self.build(windows, label = DESKTOP_LABEL)
    nodes = []
    windows.each { |w| nodes << window_node(w) }
    { role: "application", label: label, active_id: active_id(windows), nodes: nodes }
  end

  # The focused window's id, or nil. A minimized window is never focused, so this
  # naturally skips folded windows. Scans rather than using find so it does not
  # depend on rbgo's block-return semantics.
  def self.active_id(windows)
    id = nil
    windows.each { |w| id = w.id if w.focused? }
    id
  end

  # Project one window into an ARIA node Hash. role "window" (announced via
  # aria-roledescription so a screen reader says "<title>, window"); the title is
  # the accessible name, with an explicit fallback so an untitled surface is
  # never a nameless node. Geometry is carried so an AT that positions a caret /
  # highlight has the on-screen rect.
  def self.window_node(w)
    title = window_label(w)
    { id: w.id, role: "window", label: title,
      focused: w.focused?, minimized: w.minimized?,
      x: w.x, y: w.y, w: w.w, h: w.h,
      actions: window_actions(w, title) }
  end

  # The accessible name for a window: its title, or "(untitled)" when blank so
  # the node is never anonymous.
  def self.window_label(w)
    t = w.title.to_s
    t.empty? ? "(untitled)" : t
  end

  # The activatable actions a screen reader can invoke on a window, each a
  # { name:, label: } pair. `name` is the verb the action bus dispatches
  # (18_a11y.rb#a11y_dispatch routes it to the SAME WM path the titlebar buttons
  # use); `label` is the button's accessible name. Every window can be activated
  # (raised + focused) and closed; a live window can be minimized, a folded one
  # restored — mutually exclusive, mirroring the titlebar's minimise button and
  # the dock iconbar's restore.
  def self.window_actions(w, title)
    acts = []
    acts << { name: "focus", label: "Activate " + title }
    if w.minimized?
      acts << { name: "restore", label: "Restore " + title }
    else
      acts << { name: "minimize", label: "Minimize " + title }
    end
    acts << { name: "close", label: "Close " + title }
    acts
  end

  # A compact signature of everything a screen reader would perceive: per-window
  # id / role / focus / minimise / title, plus the focused id. Publishing is
  # gated on this string changing, so the ARIA DOM is rewritten only on a real
  # change (a new/closed window, a focus move, a minimise, a retitle) — never
  # every frame. Geometry is deliberately EXCLUDED: a drag/resize moves pixels
  # but changes nothing a screen reader announces, so it must not churn the tree.
  def self.signature(windows)
    parts = []
    windows.each do |w|
      f = w.focused? ? "1" : "0"
      m = w.minimized? ? "1" : "0"
      parts << "#{w.id}:#{w.role}:#{f}:#{m}:#{window_label(w)}"
    end
    "a#{active_id(windows).inspect}|" + parts.join("|")
  end

  # Serialise a built tree to JSON. Pure string concatenation (rbgo ships no JSON
  # library — same approach as 06_core.rb#windows_json), stable key order so the
  # output is deterministic and diffable. The JS bridge JSON.parses this.
  def self.to_json(tree)
    s = '{"role":"application"'
    s += ',"label":"' + esc(tree[:label].to_s) + '"'
    s += ',"active_id":' + (tree[:active_id].nil? ? "null" : tree[:active_id].to_s)
    s += ',"nodes":['
    parts = []
    tree[:nodes].each { |n| parts << node_json(n) }
    s += parts.join(",")
    s += "]}"
    s
  end

  # Serialise one window node.
  def self.node_json(n)
    s = '{"id":' + n[:id].to_s
    s += ',"role":"' + esc(n[:role].to_s) + '"'
    s += ',"label":"' + esc(n[:label].to_s) + '"'
    s += ',"focused":' + (n[:focused] ? "true" : "false")
    s += ',"minimized":' + (n[:minimized] ? "true" : "false")
    s += ',"x":' + n[:x].to_i.to_s + ',"y":' + n[:y].to_i.to_s
    s += ',"w":' + n[:w].to_i.to_s + ',"h":' + n[:h].to_i.to_s
    s += ',"actions":['
    parts = []
    n[:actions].each do |a|
      parts << ('{"name":"' + esc(a[:name].to_s) + '","label":"' + esc(a[:label].to_s) + '"}')
    end
    s += parts.join(",")
    s += "]}"
    s
  end

  # JSON string escaping: backslash, double-quote and the C0 controls that would
  # otherwise break a JSON string literal. Every other character passes through
  # verbatim (UTF-8 titles survive). Mirrors 06_core.rb#json_escape.
  def self.esc(s)
    out = ""
    i = 0
    while i < s.length
      c = s[i]
      case c
      when "\\" then out += "\\\\"
      when '"'  then out += '\\"'
      when "\n" then out += "\\n"
      when "\r" then out += "\\r"
      when "\t" then out += "\\t"
      else out += c
      end
      i += 1
    end
    out
  end
end
