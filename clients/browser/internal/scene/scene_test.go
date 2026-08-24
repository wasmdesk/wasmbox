// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"testing"

	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
)

const (
	surfaceW = 760
	surfaceH = 500
)

func newState() *State { return New(surfaceW, surfaceH) }

func newSurface() []byte { return make([]byte, 4*surfaceW*surfaceH) }

// connected returns a state whose proxy socket is "open" so the Favourites
// start page (not the offline panel) is the active content card.
func connected() *State {
	s := newState()
	s.SetConnected(true)
	return s
}

// framePixels builds an all-grey RGBA buffer of w*h for SetFrame.
func framePixels(w, h int) []byte {
	buf := make([]byte, w*h*4)
	for i := range buf {
		buf[i] = 0x40
	}
	return buf
}

// --- golden-rect proof ----------------------------------------------------

// TestGoldenRects recomputes every chrome + tile rect with the ORIGINAL
// hand-placement arithmetic and asserts the container tree lays each widget out
// at exactly the same bounds — proving the streaming rewrite keeps the toolbar
// pixel-for-pixel identical to the original shell.
func TestGoldenRects(t *testing.T) {
	s := connected() // start page active so the tiles are laid out
	w := surfaceW

	by := (toolbarH - btnH) / 2
	backRect := toolkit.Rect{X: btnLeft, Y: by, W: btnW, H: btnH}
	fwdRect := toolkit.Rect{X: btnLeft + btnW + btnGap, Y: by, W: btnW, H: btnH}
	addRect := toolkit.Rect{X: w - btnLeft - btnW, Y: by, W: btnW, H: btnH}
	addrX := fwdRect.X + btnW + 14
	addrRect := toolkit.Rect{X: addrX, Y: (toolbarH - addrH) / 2, W: addRect.X - 14 - addrX, H: addrH}

	checks := []struct {
		name string
		got  toolkit.Rect
		want toolkit.Rect
	}{
		{"back", s.backBtn.Bounds(), backRect},
		{"forward", s.fwdBtn.Bounds(), fwdRect},
		{"add", s.addBtn.Bounds(), addRect},
		{"address", s.addr.Bounds(), addrRect},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s rect = %+v, want %+v", c.name, c.got, c.want)
		}
	}

	for i := range s.favs {
		col := i % tileCols
		row := i / tileCols
		want := toolkit.Rect{
			X: gridLeft + col*(tileW+tileGapX),
			Y: gridTop + row*(tileH+tileGapY),
			W: tileW, H: tileH,
		}
		if got := s.tiles[i].Bounds(); got != want {
			t.Errorf("tile[%d] rect = %+v, want %+v", i, got, want)
		}
	}
}

// TestGoldenRectsWidth checks the flex address bar tracks the surface width.
func TestGoldenRectsWidth(t *testing.T) {
	for _, w := range []int{600, 900, 1280} {
		s := New(w, 500)
		wantAddrW := (w - btnLeft - btnW) - 14 - (btnLeft + btnW + btnGap + btnW + 14)
		if got := s.addr.Bounds().W; got != wantAddrW {
			t.Errorf("w=%d: address width = %d, want %d", w, got, wantAddrW)
		}
		if got := s.addBtn.Bounds().X; got != w-btnLeft-btnW {
			t.Errorf("w=%d: add button X = %d, want %d", w, got, w-btnLeft-btnW)
		}
	}
}

// --- construction ---------------------------------------------------------

func TestNew(t *testing.T) {
	s := newState()
	if len(s.favs) != 8 || len(s.tiles) != 8 {
		t.Fatalf("favs=%d tiles=%d, want 8/8", len(s.favs), len(s.tiles))
	}
	if s.connected || s.hasFrame {
		t.Error("browser should start disconnected with no frame")
	}
	if w, h := s.ContentSize(); w != surfaceW || h != surfaceH-toolbarH {
		t.Errorf("ContentSize = %d×%d, want %d×%d", w, h, surfaceW, surfaceH-toolbarH)
	}
}

// TestAddressField proves the toolkit AddressBar is wired as the URL field: it
// carries the scene's placeholder, shows the published URL while unfocused and
// the edit buffer while focused (its Value() single source of truth).
func TestAddressField(t *testing.T) {
	s := newState()
	// Empty + unfocused: the field carries the placeholder and an empty value.
	if s.addr.Placeholder != addrPlaceholder {
		t.Errorf("placeholder = %q, want %q", s.addr.Placeholder, addrPlaceholder)
	}
	if v := s.addr.Value(); v != "" {
		t.Errorf("empty address value = %q, want empty", v)
	}
	// A state update publishes the URL, shown while unfocused.
	s.SetState("https://example.com/", "Example", false, false, false)
	if v := s.addr.Value(); v != "https://example.com/" {
		t.Errorf("address value = %q, want the url", v)
	}
	// While focused it shows the in-progress edit buffer instead.
	s.addr.Focused().Set(true)
	s.addr.Editing().Set("typing")
	if v := s.addr.Value(); v != "typing" {
		t.Errorf("focused address value = %q, want typing", v)
	}
}

// --- rendering (covers every content panel + header fallback) --------------

func TestRenderContentModes(t *testing.T) {
	// Offline panel (fresh, disconnected).
	Render(newState(), newSurface())

	// Favourites start page (connected, no frame) — heading + tiles + upper().
	Render(connected(), newSurface())

	// Loading skeleton (a navigation in flight).
	s := connected()
	_ = s.startNavigate("example.com")
	Render(s, newSurface())

	// Error panel.
	s2 := connected()
	s2.SetError("boom")
	Render(s2, newSurface())

	// Streamed frame.
	s3 := connected()
	cw, ch := s3.ContentSize()
	s3.SetFrame(framePixels(cw, ch), cw, ch)
	Render(s3, newSurface())
}

// TestRenderFocusedChrome renders with the address bar focused and history
// enabled, covering the focused-stroke + caret and the enabled back/forward ink.
func TestRenderFocusedChrome(t *testing.T) {
	s := connected()
	s.SetState("https://ex/", "T", false, true, true) // canBack + canForward
	s.addr.Focused().Set(true)
	s.addr.Editing().Set("typing")
	Render(s, newSurface())
}

func TestRenderThemeFallbacks(t *testing.T) {
	s := connected()
	delete(s.theme.Extra, "headerbar_bg_color") // -> headerBG fallback
	delete(s.theme.Extra, "accent_fg_color")    // -> tile onAccent fallback
	Render(s, newSurface())
}

// --- migrated leaf widgets ------------------------------------------------

// TestTileComposition proves each favourites tile is composed from toolkit
// widgets — a Card frame, an accent Avatar carrying the site's initial and a
// centred Label with its name — rather than hand-drawn shapes, and that the
// avatar + label land inside the tile's bounds after layout.
func TestTileComposition(t *testing.T) {
	s := connected()
	t0 := s.tiles[0]
	if t0.avatar.Initials != "W" {
		t.Errorf("tile[0] avatar initial = %q, want W (weft)", t0.avatar.Initials)
	}
	if t0.label.Text().Get() != "weft" {
		t.Errorf("tile[0] label = %q, want weft", t0.label.Text().Get())
	}
	if t0.label.Align != toolkit.AlignCenter {
		t.Error("tile label should be centre-aligned")
	}
	tb, ab, lb := t0.Bounds(), t0.avatar.Bounds(), t0.label.Bounds()
	if ab.W != tileIconSize || ab.H != tileIconSize {
		t.Errorf("avatar square = %dx%d, want %d", ab.W, ab.H, tileIconSize)
	}
	if !tb.Contains(ab.X, ab.Y) || !tb.Contains(lb.X, lb.Y) {
		t.Errorf("avatar/label not inside tile: tile=%+v avatar=%+v label=%+v", tb, ab, lb)
	}
}

// TestOfflineAndErrorPanels proves the no-frame panels are driven through the
// stream card's EmptyState message/caption observables: the disconnected panel
// carries the proxy-not-connected copy, and an error carries the server message.
func TestOfflineAndErrorPanels(t *testing.T) {
	// Disconnected: the stream card is active; rendering it populates the panel.
	s := newState()
	Render(s, newSurface())
	if got := s.streamCrd.panel.Message().Get(); got != offlineHead {
		t.Errorf("offline message = %q, want %q", got, offlineHead)
	}
	if got := s.streamCrd.panel.Caption().Get(); got != offlineDetail {
		t.Errorf("offline caption = %q, want the proxy instructions", got)
	}
	// An error swaps the panel to the load-failure head + the server's message.
	s2 := connected()
	s2.SetError("kaboom")
	Render(s2, newSurface())
	if got := s2.streamCrd.panel.Message().Get(); got != errorHead {
		t.Errorf("error message = %q, want %q", got, errorHead)
	}
	if got := s2.streamCrd.panel.Caption().Get(); got != "kaboom" {
		t.Errorf("error caption = %q, want kaboom", got)
	}
}

// --- state sinks ----------------------------------------------------------

func TestSetConnected(t *testing.T) {
	s := newState()
	s.SetConnected(false)
	if s.status != "Browser proxy not connected" {
		t.Errorf("offline status = %q", s.status)
	}
	s.SetConnected(true) // clears the offline message
	if s.status != "" {
		t.Errorf("status after reconnect = %q, want empty", s.status)
	}
	// A non-offline status survives a (re)connect.
	s.SetError("real error")
	s.SetConnected(true)
	if s.status != "real error" {
		t.Errorf("error cleared by connect: %q", s.status)
	}
}

func TestSetStateTracksAddress(t *testing.T) {
	s := connected()
	s.SetState("https://a/", "A", false, true, false)
	if v := s.addr.Value(); v != "https://a/" {
		t.Errorf("address not tracking state url: %q", v)
	}
	if !s.canBack || s.canFwd {
		t.Errorf("history flags = back:%v fwd:%v", s.canBack, s.canFwd)
	}
	// While editing, the shown value stays the edit buffer, not the pushed URL.
	s.addr.Focused().Set(true)
	s.addr.Editing().Set("user typing")
	s.SetState("https://b/", "B", false, true, true)
	if v := s.addr.Value(); v != "user typing" {
		t.Errorf("state update clobbered the edit buffer: %q", v)
	}
	// The new URL is still published underneath, ready once the edit ends.
	if u := s.addr.URL().Get(); u != "https://b/" {
		t.Errorf("URL observable = %q, want https://b/", u)
	}
}

func TestSetFrame(t *testing.T) {
	s := connected()
	s.SetError("x")
	cw, ch := s.ContentSize()
	s.SetFrame(framePixels(cw, ch), cw, ch)
	if !s.hasFrame || s.loading || s.status != "" {
		t.Errorf("after SetFrame: hasFrame=%v loading=%v status=%q", s.hasFrame, s.loading, s.status)
	}
	// Invalid dimensions / short buffer are ignored.
	s2 := connected()
	s2.SetFrame([]byte{1, 2, 3}, 4, 4)
	if s2.hasFrame {
		t.Error("SetFrame accepted a too-short buffer")
	}
	s2.SetFrame(nil, 0, 0)
	if s2.hasFrame {
		t.Error("SetFrame accepted zero dims")
	}
}

func TestTitle(t *testing.T) {
	s := newState()
	s.SetState("u", "My Title", false, false, false)
	if s.Title() != "My Title" {
		t.Errorf("Title = %q", s.Title())
	}
}

// --- input: address bar editing -------------------------------------------

func TestAddressEditing(t *testing.T) {
	s := connected()
	var navigated string
	s.OnNavigate = func(u string) { navigated = u }

	// Click the address bar to focus it (the AddressBar owns focus + the buffer).
	r := s.addr.Bounds()
	if !s.HandleMouse(r.X+5, r.Y+5) {
		t.Fatal("clicking the address bar should focus it")
	}
	if !s.addr.Focused().Get() {
		t.Fatal("address bar not focused after click")
	}
	// Type (character events), backspace (an edit key), then Enter.
	for _, k := range []string{"e", "x", "a", "m", "p", "l", "e", "z"} {
		if !s.HandleKey(k) {
			t.Fatalf("printable key %q not consumed", k)
		}
	}
	if !s.HandleKey("Backspace") { // delete the trailing 'z'
		t.Fatal("Backspace not consumed")
	}
	if got := s.addr.Editing().Get(); got != "example" {
		t.Fatalf("edited text = %q, want example", got)
	}
	// A named key that the AddressBar ignores changes nothing → no redraw.
	if s.HandleKey("Shift") {
		t.Error("named key should not be consumed as text")
	}
	if !s.HandleKey("Enter") {
		t.Fatal("Enter not consumed")
	}
	if navigated != "https://example" {
		t.Errorf("navigated to %q, want https://example", navigated)
	}
	if s.addr.Focused().Get() {
		t.Error("Enter should blur the address bar")
	}
}

func TestAddressEscapeCancels(t *testing.T) {
	s := connected()
	s.SetState("https://keep/", "", false, false, false)
	s.addr.Focused().Set(true)
	s.addr.Editing().Set("throwaway")
	if !s.HandleKey("Escape") {
		t.Fatal("Escape not consumed")
	}
	if s.addr.Focused().Get() {
		t.Error("Escape should defocus the address bar")
	}
	if v := s.addr.Value(); v != "https://keep/" {
		t.Errorf("Escape did not restore the url: %q", v)
	}
}

func TestEnterEmptyAddressIsNoop(t *testing.T) {
	s := connected()
	var navigated bool
	s.OnNavigate = func(string) { navigated = true }
	s.addr.Focused().Set(true) // empty edit buffer
	s.HandleKey("Enter")
	if navigated {
		t.Error("Enter on an empty address should not navigate")
	}
	if s.addr.Focused().Get() {
		t.Error("Enter should defocus even when the buffer is empty")
	}
	// A blank/whitespace target normalises to empty and starts nothing.
	if s.startNavigate("   ") || navigated {
		t.Error("navigating a blank address should be a no-op")
	}
}

// --- input: favourites, back/forward, content -----------------------------

func TestTileNavigates(t *testing.T) {
	s := connected()
	var got string
	s.OnNavigate = func(u string) { got = u }
	r := s.tiles[0].Bounds()
	if !s.HandleMouse(r.X+r.W/2, r.Y+r.H/2) {
		t.Fatal("tile click should request a redraw")
	}
	if got != "https://weft.dev" {
		t.Errorf("tile navigated to %q, want https://weft.dev", got)
	}
	if !s.loading {
		t.Error("tile click should enter the loading state")
	}
}

func TestBackForwardButtons(t *testing.T) {
	s := connected()
	var backs, fwds int
	s.OnBack = func() { backs++ }
	s.OnForward = func() { fwds++ }
	back := func() bool { r := s.backBtn.Bounds(); return s.HandleMouse(r.X+r.W/2, r.Y+r.H/2) }
	fwd := func() bool { r := s.fwdBtn.Bounds(); return s.HandleMouse(r.X+r.W/2, r.Y+r.H/2) }

	// With no history the IconButtons are disabled (greyed + inert): a click is a
	// no-op, and the model-level goBack/goForward guard also refuses a direct call
	// and fires nothing.
	if !s.backBtn.Disabled().Get() || !s.fwdBtn.Disabled().Get() {
		t.Fatal("back/forward should start disabled while the server reports no history")
	}
	if back() || fwd() {
		t.Error("a disabled back/forward click should be a no-op")
	}
	if s.goBack() || s.goForward() || backs != 0 || fwds != 0 {
		t.Errorf("goBack/goForward must refuse with no history: backs=%d fwds=%d", backs, fwds)
	}
	// A state update reporting history un-greys and re-arms both buttons.
	s.SetState("u", "t", false, true, true)
	if s.backBtn.Disabled().Get() || s.fwdBtn.Disabled().Get() {
		t.Fatal("back/forward should be enabled once history is reported")
	}
	if !back() || backs != 1 {
		t.Errorf("Back not fired: backs=%d", backs)
	}
	if !fwd() || fwds != 1 {
		t.Errorf("Forward not fired: fwds=%d", fwds)
	}
}

func TestContentClickAndScrollAndKey(t *testing.T) {
	s := connected()
	cw, ch := s.ContentSize()
	s.SetFrame(framePixels(cw, ch), cw, ch)

	var clickX, clickY, scrollDy int
	var contentKey string
	s.OnContentClick = func(x, y int) { clickX, clickY = x, y }
	s.OnScroll = func(dy int) { scrollDy = dy }
	s.OnContentKey = func(k string) { contentKey = k }

	// A click in the content area maps to frame-local coords (origin at toolbarH).
	if !s.HandleMouse(100, toolbarH+30) {
		t.Fatal("content click should request a redraw")
	}
	if clickX != 100 || clickY != 30 {
		t.Errorf("content click = (%d,%d), want (100,30)", clickX, clickY)
	}
	// Wheel forwards as a scroll.
	if !s.HandleWheel(48) || scrollDy != 48 {
		t.Errorf("wheel scroll = %d", scrollDy)
	}
	// A non-address key with a frame is forwarded to the proxy.
	if !s.HandleKey("ArrowDown") || contentKey != "ArrowDown" {
		t.Errorf("content key = %q", contentKey)
	}
}

func TestWheelAndKeyWithoutFrame(t *testing.T) {
	s := connected() // no frame
	if s.HandleWheel(20) {
		t.Error("wheel without a frame should be a no-op")
	}
	if s.HandleWheel(0) { // zero delta ignored even with a frame later
		t.Error("zero wheel delta should be a no-op")
	}
	if s.HandleKey("ArrowDown") {
		t.Error("content key without a frame should be a no-op")
	}
}

func TestNilCallbacksAreSafe(t *testing.T) {
	// A state with no wired callbacks must not panic on any intent.
	s := connected()
	s.SetState("u", "t", false, true, true)
	cw, ch := s.ContentSize()
	s.SetFrame(framePixels(cw, ch), cw, ch)
	_ = s.startNavigate("x.com")
	_ = s.goBack()
	_ = s.goForward()
	s.HandleWheel(10)
	_ = s.HandleKey("ArrowDown")
	s.HandleMouse(100, toolbarH+10) // content click with nil OnContentClick
}

func TestHandleMouseMissesGutter(t *testing.T) {
	s := connected()
	gutterX := btnLeft + btnW + btnGap/2
	if s.HandleMouse(gutterX, toolbarH/2) {
		t.Error("click on a toolbar gutter should be a no-op")
	}
	if s.HandleMouse(gridLeft, toolbarH+headingOffset) {
		t.Error("click on the Favourites heading should be a no-op")
	}
	if s.HandleMouse(s.W-5, s.H-5) {
		t.Error("click in empty content should be a no-op")
	}
}

func TestAddButtonInert(t *testing.T) {
	s := connected()
	r := s.addBtn.Bounds()
	if s.HandleMouse(r.X+r.W/2, r.Y+r.H/2) {
		t.Error("the new-tab (+) button is inert; a click should be a no-op")
	}
}

// --- widget event edge cases (non-click events are ignored) ---------------

func TestNonClickEventsIgnored(t *testing.T) {
	s := connected()
	noop := toolkit.Event{Kind: toolkit.EventKeyDown}
	s.backBtn.OnEvent(noop)
	s.addr.OnEvent(noop)
	s.tiles[0].OnEvent(noop)
	s.streamCrd.OnEvent(noop)
	// streamCard click without a frame is ignored.
	s.streamCrd.OnEvent(toolkit.Event{Kind: toolkit.EventClick})
}

// TestStreamCardCollapsedDraw covers the empty-bounds guard: when the start
// card is active the stream card is collapsed and must draw nothing.
func TestStreamCardCollapsedDraw(t *testing.T) {
	s := connected() // start card active → stream card collapsed
	buf := newSurface()
	p := painter.NewPixelPainter(buf, s.W, s.H)
	s.streamCrd.SetBounds(toolkit.Rect{})
	s.streamCrd.Draw(p, s.theme) // r.W==0 → early return, no panic
}

// --- pure helpers ---------------------------------------------------------

func TestHelpers(t *testing.T) {
	if normalizeURL("  ") != "" {
		t.Error("blank normalizes to empty")
	}
	if got := normalizeURL(" example.com "); got != "https://example.com" {
		t.Errorf("normalizeURL host = %q", got)
	}
	if got := normalizeURL("http://x/"); got != "http://x/" {
		t.Errorf("normalizeURL keeps scheme = %q", got)
	}
	if !isPrintable("a") || isPrintable("Enter") || isPrintable("") {
		t.Error("isPrintable classification wrong")
	}
	if upper('a') != 'A' || upper('Z') != 'Z' {
		t.Error("upper wrong")
	}
}

// --- loading skeleton -----------------------------------------------------

// pixAt reads the RGBA at (x,y) in a tightly-packed surfaceW-wide buffer.
func pixAt(buf []byte, x, y int) toolkit.RGBA {
	o := (y*surfaceW + x) * 4
	return toolkit.RGBA{R: buf[o], G: buf[o+1], B: buf[o+2], A: buf[o+3]}
}

// Absolute rects of the two solid SkeletonRect placeholders in NewPageSkeleton
// (pad=12, gap=16, barH=24, paraH=42, imageH=90), offset by the content origin
// (0, toolbarH). These must stay in sync with toolkit.NewPageSkeleton's layout.
var (
	skelInnerW   = surfaceW - 2*12
	skelBarRect  = toolkit.Rect{X: 12, Y: toolbarH + 12, W: skelInnerW, H: 24}
	skelImg1Rect = toolkit.Rect{X: 12, Y: toolbarH + 12 + 24 + 16 + 42 + 16, W: skelInnerW, H: 90}
)

// loadingState returns a connected browser put into the loading (skeleton)
// state via BeginLoad, with the shimmer parked (phase 0 → flat grey bars).
func loadingState() *State {
	s := connected()
	s.BeginLoad()
	s.SetPhase(0)
	return s
}

// TestLoadingLifecycle exercises the load/skeleton state machine: BeginLoad and
// a navigation both enter it; a frame and an error both leave it; a disconnected
// or errored browser never shows the skeleton.
func TestLoadingLifecycle(t *testing.T) {
	s := connected()
	if s.Loading() {
		t.Fatal("a freshly connected, idle browser is not loading")
	}
	s.BeginLoad()
	if !s.Loading() || !s.streaming() {
		t.Fatal("BeginLoad should enter the loading (streaming) state")
	}
	cw, ch := s.ContentSize()
	s.SetFrame(framePixels(cw, ch), cw, ch)
	if s.Loading() {
		t.Fatal("the first frame should stop the skeleton")
	}
	// A navigation re-enters loading even though a stale frame exists.
	if !s.startNavigate("example.com") || !s.Loading() {
		t.Fatal("navigation should re-enter the loading state")
	}
	// An error leaves loading for the error panel.
	s.SetError("boom")
	if s.Loading() {
		t.Fatal("an error should drop the skeleton")
	}
	// Disconnected is never the skeleton state (that is the offline panel).
	d := newState()
	d.BeginLoad()
	if d.Loading() {
		t.Fatal("a disconnected browser must not show the skeleton")
	}
}

// TestSkeletonPixels proves the loading state paints the web-style skeleton into
// the content area: the two solid placeholder bars are the skeleton grey, the
// surrounding page ground is white, every skeleton pixel is contained below the
// toolbar, and the toolbar band carries none of the skeleton grey.
func TestSkeletonPixels(t *testing.T) {
	s := loadingState()
	buf := newSurface()
	Render(s, buf)

	// Both solid SkeletonRect bars are flat skeleton grey at phase 0. Sample the
	// interior (inset past the 6px corner radius + edge AA) so points land on the
	// solid fill, not a rounded corner.
	for _, r := range []toolkit.Rect{skelBarRect, skelImg1Rect} {
		for _, p := range []struct{ x, y int }{
			{r.X + 12, r.Y + 12}, {r.X + r.W/2, r.Y + r.H/2}, {r.X + r.W - 12, r.Y + r.H - 12},
		} {
			if got := pixAt(buf, p.x, p.y); got != skeletonGrey {
				t.Fatalf("skeleton bar pixel (%d,%d) = %+v, want skeletonGrey %+v", p.x, p.y, got, skeletonGrey)
			}
		}
	}

	// The page ground around the bars is white (Surface), not grey.
	white := s.theme.Surface
	for _, p := range []struct{ x, y int }{
		{5, toolbarH + 6},                      // left pad, above the first bar
		{surfaceW / 2, toolbarH + 12 + 24 + 8}, // gap between the top bar and the paragraph
	} {
		if got := pixAt(buf, p.x, p.y); got != white {
			t.Fatalf("page ground pixel (%d,%d) = %+v, want white %+v", p.x, p.y, got, white)
		}
	}

	// Bounds containment: the skeleton grey never bleeds into the toolbar band.
	for y := 0; y < toolbarH; y++ {
		for x := 0; x < surfaceW; x++ {
			if pixAt(buf, x, y) == skeletonGrey {
				t.Fatalf("skeleton grey leaked into the toolbar at (%d,%d)", x, y)
			}
		}
	}
}

// opaqueFrame builds a fully-opaque RGBA buffer of one solid colour, so the
// blit lands the exact colour (unlike framePixels, whose 0x40 alpha blends).
func opaqueFrame(w, h int, c toolkit.RGBA) []byte {
	buf := make([]byte, w*h*4)
	for i := 0; i < len(buf); i += 4 {
		buf[i], buf[i+1], buf[i+2], buf[i+3] = c.R, c.G, c.B, 0xff
	}
	return buf
}

// TestSkeletonReplacedByFrame proves the skeleton is gone once a frame streams
// in: the bar rect that was skeleton grey becomes the streamed frame's colour.
func TestSkeletonReplacedByFrame(t *testing.T) {
	s := loadingState()
	buf := newSurface()
	Render(s, buf)
	cx, cy := skelBarRect.X+skelBarRect.W/2, skelBarRect.Y+skelBarRect.H/2
	if got := pixAt(buf, cx, cy); got != skeletonGrey {
		t.Fatalf("pre-frame bar pixel = %+v, want skeletonGrey", got)
	}
	cw, ch := s.ContentSize()
	blue := toolkit.RGBA{R: 0x20, G: 0x40, B: 0xf0, A: 0xff}
	s.SetFrame(opaqueFrame(cw, ch, blue), cw, ch)
	Render(s, buf)
	if got := pixAt(buf, cx, cy); got != blue {
		t.Fatalf("post-frame bar pixel = %+v, want streamed frame %+v", got, blue)
	}
}

// TestSkeletonShimmerAnimates proves SetPhase drives the shimmer: at a mid-sweep
// phase the diagonal highlight lifts some bar pixels lighter than the flat base
// grey, whereas at the parked phase 0 every bar pixel is the flat base. It scans
// the top bar's solid interior (inset past the rounded corners + edge AA) so the
// white background around the rounded rect never counts as a lifted pixel.
func TestSkeletonShimmerAnimates(t *testing.T) {
	const inset = 10
	lifted := func(phase float64) int {
		s := connected()
		s.BeginLoad()
		s.SetPhase(phase)
		buf := newSurface()
		Render(s, buf)
		n := 0
		for y := skelBarRect.Y + inset; y < skelBarRect.Y+skelBarRect.H-inset; y++ {
			for x := skelBarRect.X + inset; x < skelBarRect.X+skelBarRect.W-inset; x++ {
				// Shimmer lifts the base grey toward (but not to) white; the solid
				// interior has no white, so any R above the base is shimmer.
				if int(pixAt(buf, x, y).R) > int(skeletonGrey.R) {
					n++
				}
			}
		}
		return n
	}
	if n := lifted(0); n != 0 {
		t.Fatalf("parked (phase 0) skeleton interior has %d lifted pixels, want 0 (flat grey)", n)
	}
	if n := lifted(0.5); n == 0 {
		t.Fatal("mid-sweep (phase 0.5) skeleton interior has no shimmer-lifted pixels")
	}
}

// TestSkelThemeGrey checks the dedicated skeleton theme carries the visible
// skeleton grey (WhiteSur's own SurfaceAlt is a near-white that would be
// invisible).
func TestSkelThemeGrey(t *testing.T) {
	s := newState()
	if s.skelTheme.SurfaceAlt != skeletonGrey {
		t.Fatalf("skelTheme.SurfaceAlt = %+v, want skeletonGrey %+v", s.skelTheme.SurfaceAlt, skeletonGrey)
	}
	if s.theme.SurfaceAlt == skeletonGrey {
		t.Fatal("base theme SurfaceAlt should be untouched (only the skeleton theme is greyed)")
	}
}

// --- anti-aliased text proof ----------------------------------------------

func distinctLuma(buf []byte, W int, r toolkit.Rect) int {
	seen := map[int]struct{}{}
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			o := (y*W + x) * 4
			seen[int(buf[o])+int(buf[o+1])+int(buf[o+2])] = struct{}{}
		}
	}
	return len(seen)
}

// TestAATextIsAntiAliased proves the browser renders its chrome + page text with
// the toolkit's AA/shaped OpenType face rather than the 5x7 bitmap. It scans the
// "Favourites" heading band (start page shown once connected) rendered with the
// AA face vs the bitmap default, asserting strictly more distinct luma levels.
func TestAATextIsAntiAliased(t *testing.T) {
	region := toolkit.Rect{X: gridLeft, Y: toolbarH + headingOffset - 4, W: 200, H: toolkit.GlyphHeight() + 8}

	aa := newSurface()
	Render(connected(), aa) // AA face (enableAAText ran in New)

	toolkit.SetFont(nil) // bitmap default
	defer func() { _ = toolkit.UseOpenTypeText() }()
	bm := newSurface()
	Render(connected(), bm)

	aaN := distinctLuma(aa, surfaceW, region)
	bmN := distinctLuma(bm, surfaceW, region)
	if aaN <= bmN {
		t.Fatalf("Favourites heading: distinct luma aa=%d not > bitmap=%d — AA face not active", aaN, bmN)
	}
	t.Logf("heading distinct luma: aa=%d bm=%d", aaN, bmN)
}

// TestAAFaceFits asserts the AA line box + shaped label widths fit the browser's
// fixed bands.
func TestAAFaceFits(t *testing.T) {
	s := newState()
	f, err := toolkit.DefaultOpenTypeFont(toolkit.DefaultOpenTypeSizePx)
	if err != nil {
		t.Fatalf("DefaultOpenTypeFont: %v", err)
	}
	h := f.Height()
	if h != 20 {
		t.Fatalf("AA face height = %d, want 20 (retune bands if this changes)", h)
	}
	if h > btnH || h > addrH {
		t.Fatalf("AA height %d overflows a chrome band (btnH=%d addrH=%d)", h, btnH, addrH)
	}
	for _, fv := range s.favs {
		if w := toolkit.TextWidth(fv.name); w > tileW {
			t.Fatalf("tile label %q width %d overflows tileW %d", fv.name, w, tileW)
		}
	}
}
