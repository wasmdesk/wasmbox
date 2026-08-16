# ---------------------------------------------------------------------------
# System-tray / status-area domain model — the SECOND feature of the DE
# (desktop-environment) spine (2026-08-03). An app (or the compositor itself)
# registers a status icon via `tray_add`; the compositor paints them in a
# right-to-left status strip along the top of the screen and turns a click into
# an event back to the owner, a right-click into its menu. This file owns ONLY
# the model (what a tray item is, how the strip is laid out + hit-tested); the
# render + wire glue live in 13_tray.rb (Widgets.status_area / status_icon ->
# Widgets.render -> overlay blit), which is JS-touching and so loads after
# 06_core.rb.
#
# The split mirrors 05_notifications.rb: this file sorts BELOW 06_ (and AFTER
# 05_notifications.rb, whose NotificationStack geometry it reads) so cmd/rbtest —
# which loads 01_..05_ only — exercises add / remove / update / re-flow /
# hit-test / owner-cleanup without a browser, exactly like the WindowManager and
# the toast stack next to it.
#
# Strip coordination: the notification toasts (05_notifications.rb) own a
# 300-px column pinned to the TOP-RIGHT corner. To leave that column clear, the
# tray lays its icons out RIGHT-TO-LEFT starting just LEFT of the reserved
# notification zone — so the two top strips share the top edge but never overlap
# (menu-bar-extras to the left, toasts stacking down the right). The reserve is
# read from NotificationStack so the two files keep a single source of truth.
# ---------------------------------------------------------------------------

# A single tray item: an immutable-ish record of what to show (a stock glyph
# name OR a base64 RGBA image + its size), the hover tooltip, an optional Ruby
# menu (compositor-owned items only), and the owner it fires back to. @owner is
# the posting client's opaque worker ref (nil for a compositor-owned indicator
# or the test hook); @owner_window is the poster's window id, used to drop the
# item when that window closes. The render cache fields (@b64 / @blitted) are the
# ONLY mutable state the Compositor touches; the model never reads them.
class TrayItem
  attr_reader :id, :owner, :owner_window, :glyph, :icon, :w, :h, :tooltip, :menu
  # Per-item render cache, filled lazily by the Compositor on first paint (the
  # base64 RGBA cell + a "already handed to the JS ImageData cache" flag). Nil in
  # the pure model; only 13_tray.rb writes them.
  attr_accessor :b64, :blitted

  # `id` is the caller-chosen unique key (a String). opts carries:
  #   :owner        opaque worker ref (nil = compositor-owned)
  #   :owner_window window id of the poster (nil = compositor-owned / hook)
  #   :glyph        stock glyph name ("settings"/"search"/...) or nil
  #   :icon         base64 RGBA image (used when :glyph is absent) or nil
  #   :w, :h        source dimensions of :icon
  #   :tooltip      hover text
  #   :menu         Array of {label:, action:} entries (compositor-owned items
  #                 open this as a real compositor menu; app items forward a
  #                 tray_menu event instead, so this is nil for them)
  def initialize(id, opts)
    @id           = id.to_s
    @owner        = opts[:owner]
    @owner_window = opts[:owner_window]
    @glyph        = opts[:glyph]
    @icon         = opts[:icon]
    @w            = opts[:w]
    @h            = opts[:h]
    @tooltip      = opts[:tooltip].to_s
    @menu         = opts[:menu]
    @b64          = nil
    @blitted      = false
  end

  # A compositor-owned indicator (a built-in clock/settings glyph, or one added
  # through the test hook) has no worker to fire back to; a left-click opens its
  # own menu instead of posting an event.
  def compositor_owned? = @owner.nil?

  # True when this item renders a stock vector glyph rather than a client image.
  def glyph? = !@glyph.nil? && !@glyph.to_s.empty?

  # Per-item key for the JS-side RGBA ImageData cache. Ids are caller-unique, so
  # keys never collide; invalidating @b64 on #update forces a fresh present.
  def key = "tray##{@id}"

  # A content signature that changes EXACTLY when the cell's pixels must: its id,
  # the stock glyph, the tooltip and whether it carries an image icon. The
  # dirty-rect compositor (06_core.rb#tray_signature) joins these across the
  # strip so a tray_update (a new glyph/tooltip on a live id) damages the strip
  # region even though the item set — and every #key — is unchanged.
  def sig
    "#{@id}|#{@glyph}|#{@tooltip}|#{@icon.nil? ? 0 : 1}"
  end

  # Apply a `tray_update`: only the fields actually supplied (non-nil) overwrite,
  # so a partial update (e.g. just a new tooltip) leaves the rest intact. Any
  # change drops the render cache so the next frame repaints the cell.
  def update(opts)
    @glyph   = opts[:glyph]   unless opts[:glyph].nil?
    @icon    = opts[:icon]    unless opts[:icon].nil?
    @w       = opts[:w]       unless opts[:w].nil?
    @h       = opts[:h]       unless opts[:h].nil?
    @tooltip = opts[:tooltip].to_s unless opts[:tooltip].nil?
    @menu    = opts[:menu]    unless opts[:menu].nil?
    @b64     = nil
    @blitted = false
    self
  end
end

# The live set of tray items, painted oldest-first from the top-right corner
# leftward. Insertion order is stable (newest added ends up furthest left);
# re-adding a known id updates it in place rather than duplicating.
class TrayArea
  # Square cell side (px) and the gap between cells.
  ICON = 28
  GAP  = 8
  # Distance of the strip from the top edge — aligned with the toast column's
  # top margin so the two strips share a baseline.
  MARGIN = 16

  # Horizontal room reserved on the right for the notification toast column so
  # the two top-right strips never overlap. Mirrors NotificationStack's pill
  # width + both side margins (single source of truth; NotificationStack loads
  # before this file, so the constant is defined).
  def self.notif_reserve
    NotificationStack::TOAST_W + 2 * NotificationStack::TOAST_MARGIN
  end

  attr_reader :items

  def initialize
    @items = []
  end

  def empty? = @items.empty?
  def length = @items.length

  # Look up an item by id without changing anything.
  def find(id)
    key = id.to_s
    f = nil
    @items.each { |it| f = it if it.id == key }
    f
  end

  # Register (or, for a known id, update) a tray item. Returns the item. Re-adding
  # a live id is treated as an update so a client re-announcing a slot never
  # stacks duplicates.
  def add(id, opts)
    existing = find(id)
    return existing.update(opts) unless existing.nil?
    it = TrayItem.new(id, opts)
    @items.push(it)
    it
  end

  # Update a known item from a partial opts hash. Returns the item, or nil if the
  # id is unknown (a stray update from a client that never added it).
  def update(id, opts)
    it = find(id)
    it.nil? ? nil : it.update(opts)
  end

  # Remove an item by id. Returns it, or nil if unknown.
  def remove(id)
    found = find(id)
    @items.reject! { |it| it.id == found.id } unless found.nil?
    found
  end

  # Drop every item posted by a given window (its client closed / disconnected).
  # Compositor-owned items (owner_window nil) are never touched. Returns the
  # dropped items so the caller can release their render caches.
  def remove_for_window(win_id)
    dropped = @items.select { |it| !it.owner_window.nil? && it.owner_window == win_id }
    @items.reject! { |it| !it.owner_window.nil? && it.owner_window == win_id }
    dropped
  end

  # Placement for each item against a `screen_w`-wide desktop: a top strip laid
  # RIGHT-TO-LEFT starting just left of the reserved notification column. Item 0
  # (oldest) sits nearest the right; each later item steps ICON + GAP further
  # left. Returns [{item:, x:, y:, w:, h:}, ...] in item order.
  def layout(screen_w)
    out = []
    right = screen_w - TrayArea.notif_reserve
    i = 0
    @items.each do |it|
      out.push({ item: it,
                 x: right - ICON - i * (ICON + GAP),
                 y: MARGIN, w: ICON, h: ICON })
      i += 1
    end
    out
  end

  # The item whose cell is under (px, py), or nil. Later items (further left)
  # win a tie, but cells never overlap so at most one ever matches.
  def at(px, py, screen_w)
    hit = nil
    layout(screen_w).each do |r|
      hit = r[:item] if px >= r[:x] && px < r[:x] + r[:w] && py >= r[:y] && py < r[:y] + r[:h]
    end
    hit
  end
end
