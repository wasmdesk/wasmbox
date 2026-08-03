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
    notifications.tick(t)
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

  # Build one toast's RGBA buffer (base64) via the go-widgets DE overlay set. The
  # Toast paints only when Visible, so we flip it on before Render; Render sizes
  # the widget to the whole (w, h) buffer, so the Kind-coloured pill fills it. The
  # action label (when present) is passed so the widget paints its button; the
  # click/fire is handled Ruby-side (see dismiss_notification), so the widget's
  # own action callback is left empty.
  def render_toast(n, w, h)
    handle = Widgets.toast(n.text, n.kind, n.action_label, "")
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
# go-ruby-widgets gaps hit while wiring this (for the toolkit backlog):
#   * Toast has no ICON slot — a `notify` icon (base64 RGBA) is accepted on the
#     wire + stored on the model but cannot be painted into the pill yet. Add an
#     optional leading icon to toolkit.Toast (like Badge's fill) to render it.
#   * Toast is SINGLE-LINE — title + body are joined into one "title — body"
#     line. A two-line pill (bold title over a dimmer body) would read as a
#     proper desktop notification; the toolkit Toast draws one Text run only.
#   * Multi-ACTION is not modelled — toolkit.Toast carries one ActionLabel/Action
#     pair, so the wire exposes a single action_label/action (the SDK folds
#     opts.actions[0] into it). A future notification center wants N actions.
# ---------------------------------------------------------------------------
