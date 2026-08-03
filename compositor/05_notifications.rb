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
  attr_reader :id, :title, :body, :kind, :icon, :timeout_ms, :expire_at,
              :action_label, :action, :worker, :window_id
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
    # Optional base64 RGBA icon. Stored for the notification-center follow-up;
    # the current go-widgets Toast has no icon slot, so it is NOT rendered yet.
    @icon         = opts[:icon]
    @action_label = opts[:action_label].to_s
    @action       = opts[:action] # opaque callback id echoed back to the client
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
  TOAST_H = 44
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
