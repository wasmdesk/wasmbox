# query param ?frame=aqua, or the WASMBOX_FRAME env passed via the page
# bootstrap). Unknown names fall back to OpenboxFrame — never break the
# default. FrameRegistry.select registers the name on Frame.current_name
# so the root-menu Frame submenu can mark the active entry with "* ".
frame_name = JS.global.get("WASMBOX_FRAME").to_s
frame_name = "openbox" if frame_name.nil? || frame_name.empty? || frame_name == "undefined"
FrameRegistry.select(frame_name)
JS.log("rbgo compositor: frame=#{frame_name}")

wm = WindowManager.new
comp = Compositor.new(wm)
comp.restore_layout # localStorage -> wm.saved_layout, before the spawns apply it

wm.spawn("xterm")
wm.spawn("editor", 300, 190)
wm.spawn("about rbgo", 220, 130)

comp.attach_to_canvas("screen")
comp.start

JS.log("rbgo compositor: started with #{wm.windows.length} windows")
