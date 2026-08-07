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

  # ---------------------------------------------------------------------------
  # freedesktop.org Desktop-Notification semantics.
  #
  # A browser client (or the in-page notification channel) may post a toast using
  # the freedesktop field set — summary/body/urgency/expire_timeout/app_icon/
  # image-data/actions — instead of the wasmbox-native title/body/kind/timeout.
  # These class methods MIRROR github.com/go-freedesktop/notifications/toast.ToToast
  # field-for-field so a browser post behaves exactly like the native D-Bus path:
  # the mapping is pure string/number work (no JS), so cmd/rbtest exercises it
  # off-wasm. There is deliberately NO D-Bus client in the browser — the
  # compositor already receives the fields over its in-page message channel; this
  # is purely the data-model → Toast mapping (map_freedesktop is called by
  # 12_notifications.rb#post_notification when #freedesktop? recognises the shape).
  # ---------------------------------------------------------------------------

  # The freedesktop "urgency" hint (a byte): Low/Normal are ordinary, Critical
  # must stay on screen until the user acts.
  URGENCY_LOW      = 0
  URGENCY_NORMAL   = 1
  URGENCY_CRITICAL = 2

  # The freedesktop expire_timeout sentinel meaning "let the server pick"
  # (-1 → DEFAULT_TIMEOUT_MS). expire_timeout == 0 means "never" (sticky).
  EXPIRE_DEFAULT = -1

  # Map an urgency onto a Toast kind, matching toast.KindFor exactly: Critical is
  # an error pill, Low and Normal are info pills (the advertised capability set
  # does not distinguish success/warning, so urgency never yields those two).
  def self.kind_for_urgency(u)
    u.to_i == URGENCY_CRITICAL ? "error" : "info"
  end

  # Whether a notification is sticky (never auto-expires), matching
  # Notification.Sticky(): true when expire_timeout is 0 (never), the resident
  # hint is set, or the urgency is Critical.
  def self.sticky_by?(expire_ms, resident, urgency)
    expire_ms.to_i == 0 || resident == true || urgency.to_i == URGENCY_CRITICAL
  end

  # Map expire_timeout (MILLISECONDS; -1 = server default, 0 = never) + resident
  # + urgency onto the model's timeout_ms, mirroring Sticky()/LifeFor: a sticky
  # notification is the 0 sentinel; a -1 (server default) is DEFAULT_TIMEOUT_MS;
  # any other value is passed through as-is. NB freedesktop expire_timeout is in
  # milliseconds, unlike the wasmbox-native wire `timeout` (seconds).
  def self.timeout_ms_for(expire_ms, resident, urgency)
    return 0 if sticky_by?(expire_ms, resident, urgency)
    ms = expire_ms.to_i
    ms < 0 ? NotificationStack::DEFAULT_TIMEOUT_MS : ms
  end

  # The five named XML entities the notification body-markup subset uses.
  MARKUP_ENTITIES = { "&amp;" => "&", "&lt;" => "<", "&gt;" => ">",
                      "&quot;" => "\"", "&apos;" => "'" }.freeze

  # Decode a leading XML entity in `s` (which begins with '&'): [replacement,
  # bytes-consumed], or ["", 0] when `s` does not start with a recognised entity.
  def self.read_entity(s)
    MARKUP_ENTITIES.each do |name, repl|
      return [repl, name.length] if s.start_with?(name)
    end
    ["", 0]
  end

  # Strip the notification body's hypertext-subset markup tags (<b>, <i>,
  # <a href>, ...) and decode the five named entities, yielding plain text —
  # mirroring toast.stripMarkup (honouring the advertised "body-markup"
  # capability). Forgiving: an unterminated '<' runs to end of string. A manual
  # char scan (not a regexp) so it matches the Go byte-for-byte.
  def self.strip_markup(s)
    str = s.to_s
    return str unless str.include?("<") || str.include?("&")
    parts = []
    in_tag = false
    i = 0
    n = str.length
    while i < n
      c = str[i]
      if c == "<"
        in_tag = true
      elsif c == ">"
        in_tag = false
      elsif in_tag
        # inside a tag: drop the content
      elsif c == "&"
        repl, consumed = read_entity(str[i..-1])
        if consumed > 0
          parts.push(repl)
          i += consumed - 1
        else
          parts.push(c)
        end
      else
        parts.push(c)
      end
      i += 1
    end
    parts.join
  end

  # The Toast rows for a freedesktop post, mirroring toast.linesFor: the
  # markup-stripped summary first, then each newline-separated line of the
  # markup-stripped body. Empty halves are dropped (the spec makes summary
  # required, so in practice this only guards a body-only edge) so no blank row
  # is ever painted.
  def self.lines_from(summary, body)
    ls = []
    s = strip_markup(summary)
    ls.push(s) unless s.empty?
    b = strip_markup(body)
    b.split("\n").each { |line| ls.push(line) } unless b.empty?
    ls
  end

  # Map the freedesktop actions onto our button list, mirroring the ToToast
  # action loop: the wire carries the flat pairs as the SAME compact
  # "Label|key;Label2|key2" scalar the native path uses (a scalar round-trips
  # through decode_message; an Array would not), where the "callback" half is the
  # action KEY that ActionInvoked echoes back. The reserved "default" key (an
  # activate-the-whole-notification action) is skipped, exactly like
  # Action.IsDefault().
  def self.fdo_actions(str)
    parse_actions(str).reject { |a| a[:action] == "default" }
  end

  # Does `msg` carry the freedesktop field set (vs. the wasmbox-native one)? True
  # when any freedesktop-only field is present, so post_notification can route it
  # through map_freedesktop. Additive: a purely native post (title/kind/timeout)
  # is never mistaken for a freedesktop one.
  def self.freedesktop?(msg)
    !msg[:summary].nil? || !msg[:urgency].nil? || !msg[:expire_timeout].nil? ||
      !msg[:app_icon].nil? || !msg[:image_data].nil? || !msg[:resident].nil?
  end

  # Map a decoded freedesktop notification message onto the canonical opts Hash
  # Notification.new consumes, mirroring toast.ToToast:
  #   * urgency        → kind        (kind_for_urgency: Critical=error, else info)
  #   * expire_timeout → timeout_ms  (timeout_ms_for: 0/resident/Critical sticky)
  #   * summary + body → lines       (lines_from + strip_markup)
  #   * actions        → actions_list(fdo_actions: default key skipped)
  #   * image-data / app_icon → icon (resolveIcon order: inline image wins, then
  #                                    the app_icon glyph name; image-path has no
  #                                    browser FS so it is not accepted here)
  # Returns nil when both summary and body are empty (nothing to show).
  def self.map_freedesktop(msg)
    summary = (msg[:summary].nil? ? msg[:title] : msg[:summary]).to_s
    body    = msg[:body].to_s
    return nil if summary.empty? && body.empty?
    urg      = msg[:urgency]
    resident = msg[:resident] == true || msg[:resident].to_s == "true"
    exp      = msg[:expire_timeout].nil? ? EXPIRE_DEFAULT : msg[:expire_timeout].to_i
    icon = nil
    iw   = 0
    ih   = 0
    img = msg[:image_data].to_s
    if !img.empty? && msg[:image_w].to_i > 0 && msg[:image_h].to_i > 0
      icon = img
      iw   = msg[:image_w].to_i
      ih   = msg[:image_h].to_i
    elsif glyph_icon?(msg[:app_icon])
      icon = msg[:app_icon].to_s
    end
    {
      title:        summary,
      body:         body,
      kind:         kind_for_urgency(urg),
      timeout_ms:   timeout_ms_for(exp, resident, urg),
      lines:        lines_from(summary, body),
      icon:         icon,
      icon_w:       iw,
      icon_h:       ih,
      actions_list: fdo_actions(msg[:actions]),
    }
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
    # Multi-action buttons (go-widgets v0.86 set_toast_actions). A pre-parsed
    # :actions_list (from Notification.map_freedesktop) wins; otherwise the wire's
    # compact "Label|cb;Label2|cb2" scalar is parsed here. Empty when the poster
    # used the legacy single action_label/action (folded in by #actions below).
    al            = opts[:actions_list]
    @actions      = al.is_a?(Array) ? al : Notification.parse_actions(opts[:actions])
    # Optional pre-composed message rows (Notification.map_freedesktop supplies
    # these from summary + a possibly multi-line body). When present they ARE the
    # pill's #lines verbatim; otherwise #lines derives [title, body] as before.
    lo            = opts[:lines]
    @lines_override = lo.is_a?(Array) ? lo : nil
    @worker       = opts[:worker]
    @window_id    = opts[:window_id]
    tms           = opts[:timeout_ms]
    @timeout_ms   = tms.nil? ? NotificationStack::DEFAULT_TIMEOUT_MS : tms.to_i
    @expire_at    = @timeout_ms > 0 ? now + @timeout_ms : 0
    @b64          = nil
    @blitted      = false
  end

  # The single pill line the Toast renders (the base Widgets.toast text, before
  # the render layer switches to the multi-line set_toast_lines): "title — body".
  # Either half may be empty (a body-only or title-only post), in which case the
  # separator is dropped so the line never starts/ends with a dangling em dash. A
  # freedesktop post (with a :lines override) joins its rows with the same em
  # dash so the base text is never blank.
  def text
    return @lines_override.join(" — ") unless @lines_override.nil?
    t = @title
    b = @body
    return t if b.empty?
    return b if t.empty?
    "#{t} — #{b}"
  end

  # The message rows for a multi-line pill (Widgets.set_toast_lines): the
  # freedesktop-mapped rows verbatim when supplied, else the title (bold-reading
  # first line) over the body, each dropped when empty. A title-only or body-only
  # post yields a single line — identical to the legacy single-Text look — and
  # the render layer only switches to set_toast_lines when there are two, so a
  # one-line toast is byte-unchanged.
  def lines
    return @lines_override unless @lines_override.nil?
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
