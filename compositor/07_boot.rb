# query param ?chrome=aqua, or the WASMBOX_FRAME env passed via the page
# bootstrap). Unknown names fall back to OpenboxFrame — never break the
# default.
chrome_name = JS.global.get("WASMBOX_FRAME").to_s
chrome_name = "openbox" if chrome_name.nil? || chrome_name.empty? || chrome_name == "undefined"
Frame.current = FrameRegistry[chrome_name]
JS.log("rbgo compositor: chrome=#{chrome_name}")

wm = WindowManager.new
comp = Compositor.new(wm)
comp.restore_layout # localStorage -> wm.saved_layout, before the spawns apply it

wm.spawn("xterm")
wm.spawn("editor", 300, 190)
wm.spawn("about rbgo", 220, 130)

comp.attach_to_canvas("screen")
comp.start

JS.log("rbgo compositor: started with #{wm.windows.length} windows")
