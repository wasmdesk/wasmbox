# Copyright (c) 2026 the wasmdesk/wasmbox authors. All rights reserved.
# Use of this source code is governed by a BSD-3-Clause license that can be
# found in the LICENSE file at the root of this repository.
#
# counter.rb -- an MVVM-driven widgets demo: a `require "mvvm"` Observable is the
# single source of truth, and a `require "widgets"` UI is bound to it. Clicking
# the "+1" button bumps the Observable; the Observable's change event rebinds the
# Label's text, and re-rendering reflects the new state (mvvm -> widgets).
#
# Run it through rbgo (github.com/go-embedded-ruby/ruby):
#
#     rbgo clients/mvvm-counter/counter.rb
#
# On success it prints a single "OK ..." line; any mismatch raises.

require "mvvm"
require "widgets"

# Model: an observable counter (the single source of truth).
count = Mvvm.observable(0)

# View: a vertical box holding a Label (shows the count) and a "+1" Button.
root  = Widgets.v_box
label = Widgets.label("count: 0")
inc   = Widgets.button("+1", "on_inc")
Widgets.add_widget(root, label)
Widgets.add_widget(root, inc)

# Binding: subscribe the Label to the Observable. When count changes, the queued
# event carries the new value and we rebind the Label text -- the data-binding
# seam the mvvm adapter is built around (register a callback id, drain events,
# dispatch to the Ruby-owned block).
count.subscribe("render_count")

apply = lambda do
  Mvvm.drain_events.each do |ev|
    if ev["callback_id"] == "render_count" && ev["kind"] == "changed"
      Widgets.set_text(label, "count: #{ev["value"]}")
    end
  end
end

Widgets.layout(root, 160, 60)
img0 = Widgets.render(root, 160, 60)
raise "dims"  unless img0["w"] == 160 && img0["h"] == 60
raise "start" unless Widgets.text(label) == "count: 0"

# Simulate three "+1" clicks. Each click fires on_inc; the Ruby handler bumps
# the Observable, whose changed-event rebinds the Label.
bb = Widgets.bounds(inc)
3.times do
  fired = Widgets.dispatch(inc, {"kind" => "click", "x" => bb["x"] + 1, "y" => bb["y"] + 1})["fired"]
  count.set(count.get + 1) if fired.include?("on_inc")
  apply.call
end

raise "count" unless count.get == 3
raise "bound" unless Widgets.text(label) == "count: 3"

# The render must reflect the new bound state: a different count -> the Label
# text changed -> the rendered pixels differ from the initial frame.
img1 = Widgets.render(root, 160, 60)
raise "render did not change with state" if img0["pixels"] == img1["pixels"]

puts "OK count=#{count.get} label=#{Widgets.text(label)}"
