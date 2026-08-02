# SPDX-License-Identifier: BSD-3-Clause
#
# app.rb is the ENTIRE wasmbox "Tip Calculator (Ruby)" client: its UI and its
# logic are authored in Ruby and run on wasmbox's embedded go-embedded-ruby
# (rbgo) interpreter. Unlike every other wasmbox client (whose scene is Go
# toolkit code compiled to wasm), this scene is built with `require "widgets"`
# -- the go-ruby-widgets binding over the go-widgets pixel-blitting toolkit.
#
# The path proven here, end-to-end, is:
#
#   require "widgets"                 -> the Widgets module (handle-addressed
#                                        widget tree: constructors, layout,
#                                        render-to-RGBA, event dispatch)
#   Widgets.v_box / .button / ...     -> build a widget tree, handles kept in
#                                        Ruby ivars
#   Widgets.render(root, w, h)        -> {"pixels"=><RGBA String>, "stride"=>,
#                                        "w"=>, "h"=>}
#   Base64 + __wbPresent (worker.js)  -> the RGBA frame is blitted into the
#                                        client's SharedArrayBuffer and committed
#   Widgets.dispatch(root, ev)        -> route a compositor input event into the
#                                        tree; {"fired"=>[cb ids], "repaint"=>b}
#
# Why base64: the go-widgets render buffer is raw RGBA bytes (values 0..255).
# A Go string handed across syscall/js is re-encoded as UTF-8, which corrupts
# any byte >= 0x80, so the frame cannot cross the bridge as a plain String.
# Base64 keeps every byte in the ASCII range; worker.js's __wbPresent decodes it
# straight into the SAB. The compositor input events (mousedown/keydown/...) are
# re-emitted by worker.js as a "wbinput" DOM event this script subscribes to via
# the JS bridge's only cross-language callback primitive, JS::Ref#on.

require "widgets"
require "base64"

# TipCalculator owns the Ruby widget tree + the arithmetic model. Every widget
# is an integer handle returned by a Widgets constructor; the class keeps the
# handles it later mutates (the entry, the rate buttons, the result labels) in
# ivars and addresses them by handle.
class TipCalculator
  RATES = [10, 15, 18, 20]

  def initialize(client)
    @client = client
    @w = client.get("w").to_i
    @h = client.get("h").to_i
    @rate = 15   # active tip rate (percent)
    @split = 1   # number of people splitting the bill
    build
    recompute
  end

  # build assembles the whole scene. A border-layout Container provides the
  # outer margin (the widgets render always fills the surface edge-to-edge, so
  # padding has to live inside the tree); its centre is a VBox column of rows.
  def build
    Widgets.set_theme("light")

    content = Widgets.v_box
    Widgets.set_spacing(content, 8)

    # Title.
    Widgets.add_fixed(content, Widgets.label("Tip Calculator (Ruby)"), 22)

    # Bill row: a "Bill $" caption + the amount entry (fires "bill" on change).
    bill_row = Widgets.h_box
    Widgets.set_spacing(bill_row, 6)
    Widgets.add_fixed(bill_row, Widgets.label("Bill $"), 56)
    @bill_entry = Widgets.entry("50.00", "bill")
    Widgets.add_flex(bill_row, @bill_entry, 1)
    Widgets.add_fixed(content, bill_row, 30)

    # Tip-rate buttons: one per RATES entry, each fires "rate<N>".
    Widgets.add_fixed(content, Widgets.label("Tip %"), 18)
    rate_row = Widgets.h_box
    Widgets.set_spacing(rate_row, 6)
    @rate_buttons = {}
    RATES.each do |r|
      b = Widgets.button("#{r}%", "rate#{r}")
      @rate_buttons[r] = b
      Widgets.add_flex(rate_row, b, 1)
    end
    Widgets.add_fixed(content, rate_row, 34)

    # Split row: a - / + pair around the current head count.
    split_row = Widgets.h_box
    Widgets.set_spacing(split_row, 6)
    dec = Widgets.button("-", "sdec")
    Widgets.set_style(dec, "secondary")
    inc = Widgets.button("+", "sinc")
    Widgets.set_style(inc, "secondary")
    @split_label = Widgets.label("Split: 1")
    Widgets.add_fixed(split_row, dec, 44)
    Widgets.add_flex(split_row, @split_label, 1)
    Widgets.add_fixed(split_row, inc, 44)
    Widgets.add_fixed(content, split_row, 34)

    # Result readouts.
    @tip_label   = Widgets.label("Tip:   $0.00")
    @total_label = Widgets.label("Total: $0.00")
    @each_label  = Widgets.label("Each:  $0.00")
    Widgets.add_fixed(content, @tip_label, 22)
    Widgets.add_fixed(content, @total_label, 22)
    Widgets.add_fixed(content, @each_label, 22)

    # Outer margin via a Border widget: empty edge regions inset the centre.
    # @scene is the DISPATCH root -- input events route into this subtree.
    @scene = Widgets.border
    Widgets.set_region(@scene, Widgets.container("fit"), "north", 8)
    Widgets.set_region(@scene, Widgets.container("fit"), "south", 8)
    Widgets.set_region(@scene, Widgets.container("fit"), "west", 10)
    Widgets.set_region(@scene, Widgets.container("fit"), "east", 10)
    Widgets.set_region(@scene, content, "center", 0) # size ignored for the centre

    # Widgets.render paints onto a zeroed (transparent) buffer, so anything the
    # widget tree does not cover would blit as black. A full-bounds Backdrop
    # under the scene gives every label a solid light ground. A "fit" Container
    # stacks backdrop + scene over the whole surface, drawn in insertion order.
    #
    # @root is the RENDER root (backdrop then scene). Events, however, dispatch
    # straight into @scene: the toolkit routes a click to the first child whose
    # bounds contain it and stops, so a full-cover backdrop as the first item
    # would swallow every click. The fit layout places @scene at (0,0,w,h) --
    # the same coordinate space it would occupy as the root -- so dispatching
    # into @scene with the compositor's surface-local coordinates is exact.
    @root = Widgets.container("fit")
    Widgets.add_widget(@root, Widgets.backdrop("#f5f5f5", "", 0))
    Widgets.add_widget(@root, @scene)
  end

  # bill_amount reads the entry text as a non-negative Float.
  def bill_amount
    v = Widgets.text(@bill_entry).to_s.to_f
    v < 0 ? 0.0 : v
  end

  # recompute folds the model (bill, rate, split) into the result labels and
  # re-styles the active rate button. Called after every state change.
  def recompute
    bill  = bill_amount
    tip   = bill * @rate / 100.0
    total = bill + tip
    each  = @split > 0 ? total / @split : total
    Widgets.set_text(@tip_label,   "Tip:   $" + ("%.2f" % tip))
    Widgets.set_text(@total_label, "Total: $" + ("%.2f" % total))
    Widgets.set_text(@each_label,  "Each:  $" + ("%.2f" % each))
    Widgets.set_text(@split_label, "Split: #{@split}")
    @rate_buttons.each do |r, b|
      Widgets.set_style(b, r == @rate ? "prominent" : "default")
    end
  end

  # apply runs the app logic for one fired callback id, then recomputes.
  def apply(id)
    case id
    when "rate10" then @rate = 10
    when "rate15" then @rate = 15
    when "rate18" then @rate = 18
    when "rate20" then @rate = 20
    when "sinc"   then @split += 1 if @split < 99
    when "sdec"   then @split -= 1 if @split > 1
    # "bill" carries no extra work; recompute re-reads the entry.
    end
    recompute
  end

  # render lays the tree out to the surface, encodes the RGBA frame as base64
  # and hands it to worker.js's __wbPresent, which blits it into the SAB and
  # commits full-surface damage.
  def render
    res = Widgets.render(@root, @w, @h)
    JS.global.call("__wbPresent", Base64.strict_encode64(res["pixels"]))
  end

  # on_input maps one compositor input event (a JS object) into a Widgets
  # dispatch, runs any fired callbacks and repaints. A mousedown becomes a
  # click; a printable keydown becomes a char (so the entry types); a named
  # keydown (Backspace/Enter/Arrow*) stays a keydown.
  def on_input(d)
    kind = d.get("kind").to_s
    if kind == "mousedown"
      x = d.get("x").to_i
      y = d.get("y").to_i
      # Echo the surface-local coordinates the compositor delivered, so the
      # headless probe can derive this window's screen origin (win.x = screen_x
      # - x) and click a precise control regardless of cascade placement.
      JS.global.call("__wbSetGeom", "lastclick", x, y, 0, 0)
      r = Widgets.dispatch(@scene, { "kind" => "click", "x" => x, "y" => y })
      r["fired"].each { |id| apply(id) }
      render
    elsif kind == "keydown"
      key = d.get("key").to_s
      if key.length == 1
        ev = { "kind" => "char", "code" => key }
      else
        ev = { "kind" => "keydown", "code" => key }
      end
      r = Widgets.dispatch(@scene, ev)
      r["fired"].each { |id| apply(id) }
      render if r["repaint"] || !r["fired"].empty?
    end
  end

  # publish_geometry exposes each rate button's surface rectangle on the worker
  # global (via worker.js's __wbSetGeom) so the headless probe can click a
  # precise button without hardcoding the layout. Call after a render so the
  # layout pass has run and Widgets.bounds is valid.
  def publish_geometry
    @rate_buttons.each do |r, b|
      rect = Widgets.bounds(b)
      JS.global.call("__wbSetGeom", "rate#{r}", rect["x"], rect["y"], rect["w"], rect["h"])
    end
  end
end

app = TipCalculator.new(JS.global.get("wasmboxClient"))
app.render
app.publish_geometry
JS.global.on("wbinput") { |e| app.on_input(e.get("detail")) }
