# ---------------------------------------------------------------------------
# Desktop notification (toast) domain model — the FIRST feature of the DE
# (desktop-environment) spine. Pure data + math, no JS: an app posts a `notify`
# wire message and the compositor stacks auto-expiring toasts in the top-right
# corner of the screen. This file owns ONLY the model (what a toast is, how the
# stack ages + reflows + is hit-tested); the render + wire glue live in
# 12_notifications.rb (Widgets.toast -> Widgets.render -> overlay blit), which is
# JS-touching and so loads after 06_core.rb.
#
# The split is deliberate: this file sorts BELOW 06_ so cmd/rbtest (which loads
# 01_..05_ only) exercises post / expire / stack-reflow / dismiss / hit-test
# without a browser, exactly like the WindowManager logic next to it.
# ---------------------------------------------------------------------------

# A single toast: an immutable-ish record of what to show plus a millisecond
# expiry deadline. The render cache fields (@b64 / @blitted) are the ONLY mutable
# state the Compositor touches — the model itself never reads them.
class Notification
  attr_reader :id, :title, :body, :kind, :icon, :icon_w, :icon_h, :timeout_ms,
              :expire_at, :action_label, :action, :worker, :window_id
  # Per-toast render cache, filled lazily by the Compositor on first paint (the
  # base64 RGBA pill + a "already handed to the JS ImageData cache" flag). Nil in
  # the pure model; only 12_notifications.rb writes them.
  attr_accessor :b64, :blitted

  # The four severities the go-widgets Toast paints a distinct pill colour for.
  # An unknown/absent kind normalizes to "info" so a malformed post can never
  # reach the binding (Widgets.toast raises on an unknown kind).
  KINDS = ["info", "success", "warning", "error"].freeze

  def self.normalize_kind(k)
    s = k.to_s
    KINDS.include?(s) ? s : "info"
  end

  # The stock icon glyphs the go-widgets Toast can paint as a leading icon
  # (Widgets.set_toast_icon with a name; see 12_notifications.rb). A wire `icon`
  # that names one of these — with NO icon_w/icon_h — is drawn as a vector glyph;
  # any other non-empty `icon` string is treated as base64 RGBA pixels (needing a
  # positive icon_w x icon_h). Kept here (the pure model) so the render layer and
  # rbtest agree on which names are glyphs vs. pixel data.
  ICON_GLYPHS = ["new", "open", "save", "cut", "copy", "paste", "undo", "redo",
                 "search", "settings"].freeze

  def self.glyph_icon?(name) = ICON_GLYPHS.include?(name.to_s)

  # Parse the wire's compact multi-action string — "Label|callback;Label2|cb2" —
  # into an ordered [{ label:, action: }, ...] list (the shape 12_notifications.rb
  # hands to Widgets.set_toast_actions). A field with no "|callback" half carries
  # an empty action; an empty label is skipped so a stray ";" can never produce a
  # blank button. A nil / "" string yields []. Pure string work (no JS), so a
  # client's `actions` field round-trips through decode_message as one scalar and
  # is tested in cmd/rbtest without a browser or an Array on the wire.
  def self.parse_actions(str)
    out = []
    return out if str.nil?
    str.to_s.split(";").each do |field|
      next if field.empty?
      parts = field.split("|")
      label = parts[0].to_s
      next if label.empty?
      out.push({ label: label, action: parts.length > 1 ? parts[1].to_s : "" })
    end
    out
  end

  # `now` is a millisecond clock (the rAF timestamp on wasm, a fake counter in
  # tests). timeout_ms == 0 is the "sticky" sentinel — the toast never
  # auto-expires and must be dismissed by a click. opts carries the wire fields
  # plus the opaque :worker / :window_id used to fire an action back to the
  # posting client.
  def initialize(id, opts, now)
    @id           = id
    @title        = opts[:title].to_s
    @body         = opts[:body].to_s
    @kind         = Notification.normalize_kind(opts[:kind])
    # Optional leading icon (go-widgets v0.86 Toast icon slot; rendered in
    # 12_notifications.rb). @icon is EITHER a stock glyph name (see ICON_GLYPHS,
    # with icon_w/icon_h zero) OR base64 RGBA pixel data (with a positive
    # icon_w x icon_h source size). nil / "" means no icon.
    @icon         = opts[:icon]
    @icon_w       = opts[:icon_w].to_i
    @icon_h       = opts[:icon_h].to_i
    @action_label = opts[:action_label].to_s
    @action       = opts[:action] # opaque callback id echoed back to the client
    # Multi-action buttons (go-widgets v0.86 set_toast_actions), parsed from the
    # wire's compact "Label|cb;Label2|cb2" scalar. Empty when the poster used the
    # legacy single action_label/action (folded in by #actions below).
    @actions      = Notification.parse_actions(opts[:actions])
    @worker       = opts[:worker]
    @window_id    = opts[:window_id]
    tms           = opts[:timeout_ms]
    @timeout_ms   = tms.nil? ? NotificationStack::DEFAULT_TIMEOUT_MS : tms.to_i
    @expire_at    = @timeout_ms > 0 ? now + @timeout_ms : 0
    @b64          = nil
    @blitted      = false
  end

  # The single pill line the Toast renders: "title — body". Either half may be
  # empty (a body-only or title-only post), in which case the separator is
  # dropped so the line never starts/ends with a dangling em dash.
  def text
    t = @title
    b = @body
    return t if b.empty?
    return b if t.empty?
    "#{t} — #{b}"
  end

  # The message rows for a multi-line pill (Widgets.set_toast_lines): the title
  # (bold-reading first line) over the body, each dropped when empty. A
  # title-only or body-only post yields a single line — identical to the legacy
  # single-Text look — and the render layer only switches to set_toast_lines when
  # there are two, so a one-line toast is byte-unchanged.
  def lines
    ls = []
    ls.push(@title) unless @title.empty?
    ls.push(@body)  unless @body.empty?
    ls
  end

  # The action buttons for the pill (Widgets.set_toast_actions), each a
  # { label:, action: } Hash: the parsed multi-action list when the poster
  # supplied one, else the legacy single action_label/action folded into a
  # one-element list, else [] (a plain, button-less toast).
  def actions
    return @actions unless @actions.empty?
    return [{ label: @action_label, action: @action.to_s }] if has_action?
    []
  end

  # Does this toast paint a leading icon? True for a non-empty @icon (a glyph
  # name or base64 pixel data).
  def has_icon? = !@icon.nil? && !@icon.to_s.empty?

  # Is @icon base64 RGBA pixel data (a positive source size) rather than a glyph
  # name? Drives which Widgets.set_toast_icon overload the render layer uses.
  def image_icon? = has_icon? && @icon_w > 0 && @icon_h > 0

  def sticky? = @expire_at == 0
  def expired?(now) = !sticky? && now >= @expire_at
  def has_action? = !@action_label.empty?

  # Per-toast key for the JS-side RGBA ImageData cache. Monotonic ids mean keys
  # never collide across a toast's lifetime, so a reused key can never re-present
  # a stale pill.
  def key = "notif##{@id}"
end

# The live stack of toasts. Newest is appended (and so painted lowest); when a
# toast expires the stack reflows upward on the next layout. Capped so a chatty
# app can never grow the column without bound.
class NotificationStack
  # How many toasts are visible at once. A post beyond the cap drops the OLDEST
  # (Fluxbox/GNOME-style: the newest news wins the limited real estate).
  MAX_VISIBLE = 4

  # Default auto-dismiss budget when a post omits a timeout, in milliseconds.
  DEFAULT_TIMEOUT_MS = 5000

  # Toast pill geometry + top-right stacking metrics. The SINGLE source of truth
  # shared by the Compositor render (12_notifications.rb) and the hit-test here,
  # so a click can never disagree with where the pixels landed.
  TOAST_W = 300
  # Two message rows (title over body) + a leading icon + action buttons need
  # more height than the original single-line pill (44). 64 fits a bold title
  # line over a body line with the toolkit's opentype face, the icon square and
  # the right-edge buttons; TOAST_W is unchanged so the tray's notif_reserve and
  # every tray-column probe stay valid.
  TOAST_H = 64
  TOAST_GAP = 10
  TOAST_MARGIN = 16

  attr_reader :items

  def initialize
    @items = []
    @next_id = 0
  end

  def empty? = @items.empty?
  def length = @items.length

  # Append a new toast built from `opts` at clock `now`. Oldest toasts beyond
  # MAX_VISIBLE are dropped so the column stays bounded. Returns the Notification.
  def post(opts, now)
    @next_id += 1
    n = Notification.new(@next_id, opts, now)
    @items.push(n)
    @items.shift while @items.length > MAX_VISIBLE
    n
  end

  # Drop every toast whose auto-dismiss deadline has passed at `now`; the
  # survivors reflow upward on the next #layout. Returns the dropped toasts so a
  # caller can release their render caches.
  def tick(now)
    dropped = @items.select { |n| n.expired?(now) }
    @items.reject! { |n| n.expired?(now) }
    dropped
  end

  # Remove a toast by id (a click dismissal). Returns it, or nil if unknown.
  def dismiss(id)
    found = nil
    @items.each { |n| found = n if n.id == id }
    @items.reject! { |n| n.id == id } if found
    found
  end

  # Look up a toast by id without removing it.
  def find(id)
    f = nil
    @items.each { |n| f = n if n.id == id }
    f
  end

  # Placement for each visible toast against a `screen_w`-wide desktop: a
  # top-right column, index 0 nearest the top edge, each row TOAST_H tall with a
  # TOAST_GAP between. Returns [{notif:, x:, y:, w:, h:}, ...] top-to-bottom.
  def layout(screen_w)
    out = []
    i = 0
    @items.each do |n|
      out.push({ notif: n,
                 x: screen_w - TOAST_W - TOAST_MARGIN,
                 y: TOAST_MARGIN + i * (TOAST_H + TOAST_GAP),
                 w: TOAST_W, h: TOAST_H })
      i += 1
    end
    out
  end

  # The top-most toast under (px, py), or nil. Later (visually lower) rows win a
  # tie, mirroring WindowManager#window_at's last-wins scan.
  def at(px, py, screen_w)
    hit = nil
    layout(screen_w).each do |r|
      hit = r[:notif] if px >= r[:x] && px < r[:x] + r[:w] && py >= r[:y] && py < r[:y] + r[:h]
    end
    hit
  end
end
