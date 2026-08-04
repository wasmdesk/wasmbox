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

  # Post a toast from a decoded `notify` wire message. Normalizes the wire's
  # timeout (SECONDS, default 5; <= 0 = sticky) to the model's millisecond clock
  # and appends to the stack. `worker` is the posting client's worker ref (nil
  # for the test hook / an in-process poster); it + window_id let a toast action
  # fire back to the originating client. No-op when the flag is off or the
  # message carries neither a title nor a body.
  def post_notification(msg, worker)
    return nil unless NOTIFICATIONS
    title = msg[:title].to_s
    body  = msg[:body].to_s
    return nil if title.empty? && body.empty?
    to = msg[:timeout]
    tms = to.nil? ? NotificationStack::DEFAULT_TIMEOUT_MS : (to.to_f <= 0 ? 0 : (to.to_f * 1000).to_i)
    notifications.post({
      title:        title,
      body:         body,
      kind:         msg[:kind],
      icon:         msg[:icon],
      icon_w:       msg[:icon_w],
      icon_h:       msg[:icon_h],
      actions:      msg[:actions],
      action_label: msg[:action_label].to_s,
      action:       msg[:action],
      worker:       worker,
      window_id:    msg[:window_id],
      timeout_ms:   tms,
    }, notify_now)
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
    Base64.strict_encode64(img["pixels"])
  end

  # The toast under (px, py), or nil. Used by on_mousedown to let a click on a
  # toast dismiss it. No-op (nil) when the flag is off or the stack is empty.
  def notify_at(px, py)
    return nil unless NOTIFICATIONS
    return nil if notifications.empty?
    notifications.at(px, py, @width)
  end

  # A toast was clicked: fire its action back to the posting client (if any),
  # then remove it from the stack.
  def dismiss_notification(n)
    fire_notification_action(n) if n.has_action?
    notifications.dismiss(n.id)
  end

  # Post a toast's action id back to the client that raised it, as an `input`
  # event of kind "notification_action" carrying the action id + the toast id.
  # Routed by the poster's window_id so the SDK's onInput (keyed by surface)
  # delivers it. No-op for a toast with no worker (the test hook / an in-process
  # poster) or no action id.
  def fire_notification_action(n)
    return nil if n.worker.nil?
    return nil if n.action.nil?
    payload = JS.global.call("wasmboxMakeObject",
      "type", "input",
      "window_id", n.window_id,
      "event", JS.global.call("wasmboxMakeObject",
        "kind", "notification_action",
        "action", n.action,
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
# Remaining gap (a future notification center): a click on a multi-action pill is
# still dismiss-only compositor-side (dismiss_notification fires the legacy first
# action back to the client); per-BUTTON click routing would need the toolkit to
# expose each button's rect (or a host HitTest) so on_mousedown can fire exactly
# the button the pointer hit rather than the first.
# ---------------------------------------------------------------------------
