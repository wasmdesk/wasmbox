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

func TestAddressText(t *testing.T) {
	s := newState()
	if txt, ph := s.addressText(); !ph || txt == "" {
		t.Errorf("empty address = (%q, ph=%v), want placeholder", txt, ph)
	}
	s.SetState("https://example.com/", "Example", false, false, false)
	if txt, ph := s.addressText(); ph || txt != "https://example.com/" {
		t.Errorf("address with url = (%q, ph=%v)", txt, ph)
	}
	s.focusAddr()
	s.addrText = "typing"
	if txt, ph := s.addressText(); ph || txt != "typing" {
		t.Errorf("focused address = (%q, ph=%v)", txt, ph)
	}
}

// --- rendering (covers every content panel + header fallback) --------------

func TestRenderContentModes(t *testing.T) {
	// Offline panel (fresh, disconnected).
	Render(newState(), newSurface())

	// Favourites start page (connected, no frame) — heading + tiles + upper().
	Render(connected(), newSurface())

	// Loading panel.
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
	s.focusAddr()
	s.addrText = "typing"
	Render(s, newSurface())
}

func TestRenderThemeFallbacks(t *testing.T) {
	s := connected()
	delete(s.theme.Extra, "headerbar_bg_color") // -> headerBG fallback
	delete(s.theme.Extra, "accent_fg_color")    // -> tile onAccent fallback
	Render(s, newSurface())
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
	if txt, _ := s.addressText(); txt != "https://a/" {
		t.Errorf("address not tracking state url: %q", txt)
	}
	if !s.canBack || s.canFwd {
		t.Errorf("history flags = back:%v fwd:%v", s.canBack, s.canFwd)
	}
	// While editing, the address text is not overwritten by a state update.
	s.focusAddr()
	s.addrText = "user typing"
	s.SetState("https://b/", "B", false, true, true)
	if s.addrText != "user typing" {
		t.Errorf("state update clobbered edited address: %q", s.addrText)
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

	// Click the address bar to focus it.
	r := s.addr.Bounds()
	if !s.HandleMouse(r.X+5, r.Y+5) {
		t.Fatal("clicking the address bar should focus it")
	}
	if !s.addrFocused {
		t.Fatal("address bar not focused after click")
	}
	// Type, backspace, then Enter.
	for _, k := range []string{"e", "x", "a", "m", "p", "l", "e", "z"} {
		if !s.HandleKey(k) {
			t.Fatalf("printable key %q not consumed", k)
		}
	}
	if !s.HandleKey("Backspace") { // delete the trailing 'z'
		t.Fatal("Backspace not consumed")
	}
	if s.addrText != "example" {
		t.Fatalf("edited text = %q, want example", s.addrText)
	}
	// A named key while editing is ignored.
	if s.HandleKey("Shift") {
		t.Error("named key should not be consumed as text")
	}
	if !s.HandleKey("Enter") {
		t.Fatal("Enter not consumed")
	}
	if navigated != "https://example" {
		t.Errorf("navigated to %q, want https://example", navigated)
	}
	if s.addrFocused {
		t.Error("Enter should blur the address bar")
	}
}

func TestAddressEscapeCancels(t *testing.T) {
	s := connected()
	s.SetState("https://keep/", "", false, false, false)
	s.focusAddr()
	s.addrText = "throwaway"
	if !s.HandleKey("Escape") {
		t.Fatal("Escape not consumed")
	}
	if s.addrFocused || s.addrText != "https://keep/" {
		t.Errorf("Escape did not restore: focused=%v text=%q", s.addrFocused, s.addrText)
	}
}

func TestEnterEmptyAddressIsNoop(t *testing.T) {
	s := connected()
	s.focusAddr() // addrText seeded from empty url
	if s.HandleKey("Enter") {
		t.Error("Enter on an empty address should not navigate")
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

	// Disabled while the server reports no history.
	if back() || fwd() {
		t.Error("back/forward should be no-ops with no history")
	}
	// Enable via a state update.
	s.SetState("u", "t", false, true, true)
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
	if trimLastRune("") != "" || trimLastRune("abc") != "ab" {
		t.Error("trimLastRune wrong")
	}
	if upper('a') != 'A' || upper('Z') != 'Z' {
		t.Error("upper wrong")
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
