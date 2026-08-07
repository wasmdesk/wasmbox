# ---------------------------------------------------------------------------
# Desktop notification toasts — the compositor render + wire glue over the pure
# model in 05_notifications.rb. First feature of the DE (desktop-environment)
# spine (2026-08-03).
#
# An app posts `{type:"notify", title, body, kind, timeout, action_label,
# action, icon}` (client SDK: `client.notify(opts)`); the compositor stacks
# auto-expiring toasts in the top-right corner, rendered through the go-widgets
# DE overlay set:
#
#   Widgets.toast(text, kind, action_label, "") -> Widgets.set_visible ->
#   Widgets.render -> RGBA bytes -> base64 -> JS wasmboxBlitRGBAOver
#   (alpha-composited drawImage), the SAME seam the HUD (09_hud_widgets.rb) uses.
#
# Chosen render path: an overlay blit each frame, NOT a `notification`-role
# surface. A toast carries no app-backed SharedArrayBuffer — the compositor owns
# its pixels — so a role window would have no surface to blit; the overlay path
# is the cleaner one the task calls out for transient toasts, and it keeps the
# heavily co-edited WindowManager untouched (no new role threaded through focus /
# cycle / hit-test / dock / snapshot). A notification CENTER (history) surface is
# a natural follow-up and is where a role would earn its keep.
#
# Lifecycle is authoritative on the Ruby side (frame-driven expiry over a
# millisecond clock), which keeps it unit-testable off-wasm in cmd/rbtest; the
# toolkit Toast's own Life/Tick countdown is therefore not driven here (the
# widget is only ever asked for one still frame of pixels). See the gap notes at
# the bottom.
#
# Gated behind Compositor::NOTIFICATIONS so `main` stays shippable during the
# live co-edit — flip it to false to disable posting + drawing with zero other
# changes.
# ---------------------------------------------------------------------------
require "widgets"
require "base64"

# The Compositor caches each toast's per-button rectangles (Widgets.button_rects,
# go-widgets v0.114 Toast.ButtonRects / go-ruby-widgets v0.9) on the Notification
# alongside its pixel cache, so a click can be routed to the exact action button
# it hit. The pure model (05_notifications.rb) stays JS-free; this accessor is
# compositor-only render state, mirroring the @b64 / @blitted render-cache split.
class Notification
  # Per-button rectangles in the toast's LOCAL painted space — an Array of
  # { "x","y","w","h" } Hashes, one per action button in Actions order — filled
  # lazily by render_toast on first paint. Nil until then (and for an action-less
  # toast).
  attr_accessor :button_rects
end

class Compositor
  # Master switch for the whole notification feature. `false` makes every
  # post/draw/tick/click hook below a no-op, so the compositor behaves exactly as
  # it did before this feature landed.
  NOTIFICATIONS = true

  # The toast stack (model in 05_notifications.rb, which loads before this file).
  # Built lazily so Compositor#initialize (06_core.rb) needs no edit and pure
  # rbtest — which never loads this file — is unaffected.
  def notifications
    @notifications ||= NotificationStack.new
  end

  # Post a toast from a decoded `notify` wire message. Two field sets are
  # accepted, distinguished by Notification.freedesktop?:
  #
  #   * freedesktop.org — summary/body/urgency/expire_timeout/app_icon/
  #     image-data/actions — mapped by Notification.map_freedesktop, which mirrors
  #     go-freedesktop/notifications/toast.ToToast (urgency→kind, expire_timeout→
  #     sticky/timeout, summary+body→lines, app_icon/image-data→icon) so a browser
  #     post behaves like the native D-Bus path. expire_timeout is already in
  #     milliseconds; the mapper handles the sentinels.
  #
  #   * wasmbox-native — title/body/kind/timeout/icon/actions — the original
  #     shape, whose `timeout` is in SECONDS (default 5; <= 0 = sticky) and is
  #     normalized to the model's millisecond clock here.
  #
  # `worker` is the posting client's worker ref (nil for the test hook / an
  # in-process poster); it + window_id let a toast action fire back to the
  # originating client. No-op when the flag is off or the message carries nothing
  # to show (neither a summary/title nor a body).
  def post_notification(msg, worker)
    return nil unless NOTIFICATIONS
    if Notification.freedesktop?(msg)
      opts = Notification.map_freedesktop(msg)
      return nil if opts.nil?
    else
      title = msg[:title].to_s
      body  = msg[:body].to_s
      return nil if title.empty? && body.empty?
      to = msg[:timeout]
      tms = to.nil? ? NotificationStack::DEFAULT_TIMEOUT_MS : (to.to_f <= 0 ? 0 : (to.to_f * 1000).to_i)
      opts = {
        title:        title,
        body:         body,
        kind:         msg[:kind],
        icon:         msg[:icon],
        icon_w:       msg[:icon_w],
        icon_h:       msg[:icon_h],
        actions:      msg[:actions],
        action_label: msg[:action_label].to_s,
        action:       msg[:action],
        timeout_ms:   tms,
      }
    end
    opts[:worker]    = worker
    opts[:window_id] = msg[:window_id]
    notifications.post(opts, notify_now)
  end

  # Millisecond clock for expiry: the most recent rAF timestamp, captured each
  # tick. 0 before the first frame (a toast posted that early simply ages from 0).
  def notify_now = @now.nil? ? 0 : @now

  # Advance + expire the toast stack. Called from Compositor#tick every frame.
  # Expired toasts leave the column and the survivors reflow up on the next
  # render. No-op when the flag is off.
  def tick_notifications(t)
    return nil unless NOTIFICATIONS
    @now = t
    # Expire aged toasts. When any drop, force one repaint so their pixels are
    # cleared even if the stack becomes empty (an empty stack no longer keeps the
    # idle-repaint gate awake, so the clearing frame must be requested explicitly).
    dropped = notifications.tick(t)
    mark_dirty if dropped && !dropped.empty?
    nil
  end

  # Draw the toast column as a top-right overlay. Each toast's pixels are built
  # ONCE (Widgets.toast) and cached; later frames re-present the JS-side
  # ImageData (empty base64) at the toast's CURRENT stacked position — so a
  # reflow after an expiry just moves the cached pill, at one drawImage per toast
  # (the HUD's steady-state cost). Called last in Compositor#render so toasts sit
  # above every other stratum (windows, panels, menu, HUD). No-op when the flag
  # is off or the stack is empty.
  def draw_notifications
    return nil unless NOTIFICATIONS
    w = NotificationStack::TOAST_W
    h = NotificationStack::TOAST_H
    notifications.layout(@width).each do |r|
      n = r[:notif]
      if n.b64.nil?
        n.b64 = render_toast(n, w, h)
        n.blitted = false
      end
      if n.blitted
        JS.global.call("wasmboxBlitRGBAOver", @ctx, "", w, h, r[:x], r[:y], n.key)
      else
        JS.global.call("wasmboxBlitRGBAOver", @ctx, n.b64, w, h, r[:x], r[:y], n.key)
        n.blitted = true
      end
    end
  end

  # Build one toast's RGBA buffer (base64) via the go-widgets v0.86 DE overlay
  # set. The Toast paints only when Visible, so we flip it on before Render;
  # Render sizes the widget to the whole (w, h) buffer, so the Kind-coloured pill
  # fills it. Beyond the base pill we layer the v0.86 refinements:
  #   * set_toast_icon — a leading stock glyph (icon_w/icon_h zero) or a base64
  #     RGBA image (positive icon_w x icon_h), drawn left of the text.
  #   * set_toast_lines — a bold-reading title line over the body line when the
  #     post carries both (a single line is left as the plain Text so a
  #     title-only / body-only pill is byte-unchanged from the legacy look).
  #   * set_toast_actions — one button per parsed action (or the folded-in legacy
  #     single action) along the pill's right edge.
  # The buttons' own widget callbacks are left to fire nothing here: a click on a
  # toast is hit-tested + dispatched Ruby-side (dismiss_notification), so the
  # compositor stays the authority on the toast lifecycle.
  def render_toast(n, w, h)
    handle = Widgets.toast(n.text, n.kind, "", "")
    if n.has_icon?
      if n.image_icon?
        Widgets.set_toast_icon(handle, n.icon, n.icon_w, n.icon_h)
      else
        Widgets.set_toast_icon(handle, n.icon.to_s, 0, 0)
      end
    end
    ls = n.lines
    Widgets.set_toast_lines(handle, ls) if ls.length > 1
    acts = n.actions
    unless acts.empty?
      Widgets.set_toast_actions(handle,
        acts.map { |a| { "label" => a[:label].to_s, "callback" => a[:action].to_s } })
    end
    Widgets.set_visible(handle, true)
    img = Widgets.render(handle, w, h)
    # Cache the laid-out button rectangles (in the pill's LOCAL painted space) so
    # a later click can be routed to the exact action button it hit. Render sized
    # the widget to the whole (w, h) buffer, so these rects share the space of a
    # toast-local click (a screen click minus the pill's top-left). Nil for an
    # action-less pill.
    n.button_rects = acts.empty? ? nil : Widgets.button_rects(handle)
    Base64.strict_encode64(img["pixels"])
  end

  # The toast under (px, py), or nil. Used by on_mousedown to let a click on a
  # toast dismiss it. As a side effect it records the click's TOAST-LOCAL
  # position (px/py minus the hit pill's top-left) in @notify_click_local, which
  # dismiss_notification reads to route the click to the exact action button it
  # landed on. Later (visually lower) rows win a tie, mirroring
  # NotificationStack#at. No-op (nil) when the flag is off or the stack is empty.
  def notify_at(px, py)
    return nil unless NOTIFICATIONS
    return nil if notifications.empty?
    hit = nil
    notifications.layout(@width).each do |r|
      if px >= r[:x] && px < r[:x] + r[:w] && py >= r[:y] && py < r[:y] + r[:h]
        hit = r[:notif]
        @notify_click_local = [px - r[:x], py - r[:y]]
      end
    end
    hit
  end

  # A toast was clicked: route the click to the action button it landed on, fire
  # THAT action back to the posting client (if any), publish it for the headless
  # probe, then remove the toast. The click's toast-local position was stashed by
  # notify_at (called immediately before by on_mousedown).
  def dismiss_notification(n)
    act = clicked_action(n)
    publish_notify_action(act, n)
    fire_notification_action(n, act) if act && !act.empty?
    notifications.dismiss(n.id)
  end

  # The action id the recorded click selects, or nil. The stashed toast-local
  # click (@notify_click_local) is hit-tested against the cached per-button
  # rectangles render_toast fills on first paint: a hit returns that button's
  # action id. A miss returns the sole action for a SINGLE-button pill (the whole
  # pill is that action's target — preserving the legacy "click anywhere to act"
  # for a one-action toast) and nil for a MULTI-button pill (a body click just
  # dismisses). With no rects cached yet (a click before the first paint) it
  # falls back to the first action.
  def clicked_action(n)
    acts = n.actions
    return nil if acts.empty?
    rects = n.button_rects
    local = @notify_click_local
    if rects && !rects.empty? && local
      lx, ly = local
      rects.each_with_index do |r, i|
        if lx >= r["x"] && lx < r["x"] + r["w"] && ly >= r["y"] && ly < r["y"] + r["h"]
          return acts[i][:action].to_s
        end
      end
      return acts.length == 1 ? acts[0][:action].to_s : nil
    end
    acts[0][:action].to_s
  end

  # Publish the routed action id (+ the toast id) to an OPTIONAL JS observer a
  # headless probe installs (test/probe-notifications.mjs defines
  # globalThis.wasmboxPublishNotifyAction before it clicks, then reads back what
  # the compositor routed). Purely an observability seam: in production the global
  # is undefined, so JS.global.get returns nil and this is a complete no-op — no
  # worker.js hook needed. The empty string means "dismissed without firing".
  def publish_notify_action(act, n)
    return nil if JS.global.get("wasmboxPublishNotifyAction").nil?
    JS.global.call("wasmboxPublishNotifyAction", act.to_s, n.id)
  end

  # Post the CLICKED action id back to the client that raised the toast, as an
  # `input` event of kind "notification_action" carrying the action id + the toast
  # id. `action` is the button the click routed to (see clicked_action), so a
  # multi-action toast delivers the EXACT button pressed rather than always the
  # first. Routed by the poster's window_id so the SDK's onInput (keyed by
  # surface) delivers it. No-op for a toast with no worker (the test hook / an
  # in-process poster) or an empty action id.
  def fire_notification_action(n, action)
    return nil if n.worker.nil?
    return nil if action.nil? || action.empty?
    payload = JS.global.call("wasmboxMakeObject",
      "type", "input",
      "window_id", n.window_id,
      "event", JS.global.call("wasmboxMakeObject",
        "kind", "notification_action",
        "action", action,
        "notification_id", n.id))
    JS.global.call("wasmboxPostMessage", n.worker, payload)
  end
end

# ---------------------------------------------------------------------------
# go-ruby-widgets v0.86 CLOSED the three gaps this file used to carry — the
# Toast now paints a leading icon, a multi-line body and several action buttons,
# all wired above:
#   * ICON slot   — Widgets.set_toast_icon(handle, glyphOrPixels, w, h): a stock
#                   glyph (new/open/save/cut/copy/paste/undo/redo/search/settings)
#                   or a base64 RGBA image drawn left of the text.
#   * MULTI-LINE  — Widgets.set_toast_lines(handle, [title, body]): the title
#                   over the body instead of one joined "title — body" run.
#   * MULTI-ACTION— Widgets.set_toast_actions(handle, [{label,callback}, ...]):
#                   N right-edge buttons, superseding the single ActionLabel.
# Per-BUTTON click routing is now CLOSED too (go-widgets v0.114 Toast.ButtonRects
# / go-ruby-widgets v0.9 Widgets.button_rects): render_toast caches each button's
# laid-out rectangle (pill-local space) and dismiss_notification hit-tests the
# stashed click against them, firing the EXACT button the pointer hit rather than
# always the first. A future notification CENTER (history surface) is the
# remaining follow-up.
#
# freedesktop.org Desktop-Notification semantics are now spoken too: a post using
# the spec field set (summary/body/urgency/expire_timeout/app_icon/image-data/
# actions) is mapped by Notification.map_freedesktop (05_notifications.rb), which
# mirrors go-freedesktop/notifications/toast.ToToast field-for-field —
# urgency→kind (Critical=error, else info), expire_timeout/resident/Critical→
# sticky/timeout, summary+markup-stripped body→lines, image-data (else the
# app_icon glyph)→icon, and the actions scalar (default key skipped)→the per-
# button routing above. No D-Bus client runs in the browser: the fields arrive
# over the existing in-page message channel and this is purely the data-model →
# Toast mapping, so the browser toast behaves like the native D-Bus daemon path.
# ---------------------------------------------------------------------------
