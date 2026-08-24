// SPDX-License-Identifier: BSD-3-Clause
//
// Package scene renders the wasmdesk web browser in the WhiteSur / Safari
// style: a #ebebeb toolbar (back / forward buttons + an editable rounded
// address bar + a new-tab button) above a content area. The browser is a thin
// front-end for the go-webengine **browserproxy**: it does not fetch or render
// pages itself. The proxy renders each page server-side and streams frames (as
// RGBA byte buffers) which this scene blits into the content area; the scene
// forwards the user's navigation (address-bar Enter, favourite-tile clicks,
// back/forward), content clicks, wheel scrolls and keys back to the proxy as
// intents. Because rendering is server-side, ANY site works — including pages
// that forbid framing — and the wasmbox page can stay under COEP:require-corp
// (a WebSocket is exempt from COEP/CORS).
//
// The scene knows nothing about WebSockets: the client's main.go opens the
// socket and wires the On* intent callbacks + the Set* frame/state sinks, so
// the whole scene is unit-testable in pure Go with fakes.
//
// Layout is built from the toolkit's box-layout container model (Dock + Box +
// Card containers) rather than hand-computed toolkit.Rect placement. The whole
// chrome is one tree:
//
//	root  Dock                                   — the browser shell
//	├─ (docked North, height toolbarH)  band  VBox — the toolbar band
//	│  └─ (centre row, height btnH)      row  HBox
//	│     [ pad · back · gap · fwd · gap · addr(flex) · gap · add · pad ]
//	└─ (body / Centre)  content  Container(CardLayout)
//	   ├─ card 0  startCard  — "Favourites" heading + tile grid (VBox of HBox)
//	   └─ card 1  streamCard — the streamed page frame, or an offline/loading panel
//
// The toolbar row's fixed pixel widths become AddFixed extents and its uneven
// gaps become invisible fixed spacers, so every control lands at exactly the
// rect the old hand-placed code produced (proven by the golden-rect test). A
// single root.SetBounds lays the whole tree out, root.Draw paints it, and
// root.OnEvent routes clicks into child-local space.

package scene

import (
	"strings"
	"sync"

	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
)

// aaOnce flips the toolkit's active font to anti-aliased, shaped OpenType text
// exactly once for this client process.
var aaOnce sync.Once

// enableAAText installs the toolkit's bundled AA/shaped OpenType face so the
// toolbar controls, address bar, favourites heading, tile labels and panels
// render as the shaped vector face. On the documented never-returned parse
// error the toolkit leaves the working bitmap default active.
func enableAAText() { aaOnce.Do(func() { _ = toolkit.UseOpenTypeText() }) }

// link is one favourite: a display name + the host shown/opened.
type link struct {
	name string
	url  string
}

// State holds the widget tree, the favourites, the connection/page model and
// the intent callbacks the client wires to the proxy WebSocket.
type State struct {
	W, H  int
	theme *toolkit.Theme
	favs  []link

	// Page/connection model, updated by the Set* sinks from server messages.
	url       string // current page URL (address bar shows it when not editing)
	title     string // current page title
	loading   bool   // a navigation is in flight
	canBack   bool   // server reports back history
	canFwd    bool   // server reports forward history
	connected bool   // the proxy WebSocket is open
	status    string // offline / error message for the content panel

	// Streamed frame (RGBA), blitted into the content area by streamCard.
	frameW, frameH int
	hasFrame       bool

	// Skeleton loading state. awaitingFrame is set when a load is in flight
	// locally (a navigation was issued, or the client just connected and asked
	// the proxy for an initial render) and no {frame} for it has arrived yet; it
	// is cleared by the first frame (or an error). While a load is in flight the
	// content area shows a web-style page skeleton (NewPageSkeleton) instead of a
	// blank ground, its shimmer swept by phase. phase is advanced by the client's
	// animation loop (SetPhase) only while Loading() holds, so an idle browser
	// never repaints.
	awaitingFrame bool
	phase         float64
	skel          *toolkit.SkeletonGroup
	skelTheme     *toolkit.Theme

	// Address-bar editing.
	addrFocused bool
	addrText    string // the text being edited (only meaningful while focused)

	// Intent callbacks — wired by main.go, all nil-safe.
	OnNavigate     func(url string) // address Enter or favourite tile
	OnBack         func()
	OnForward      func()
	OnContentClick func(x, y int) // content-area pixel coords (0,0 = frame top-left)
	OnScroll       func(dy int)   // wheel delta
	OnContentKey   func(key string)

	// Widget tree.
	root      *toolkit.Dock
	content   *toolkit.Container
	card      *toolkit.CardLayout
	startCard *startCard
	streamCrd *streamCard
	grid      *toolkit.VBox
	backBtn   *toolkit.IconButton
	fwdBtn    *toolkit.IconButton
	addBtn    *toolkit.IconButton
	addr      *addrBar
	tiles     []*tile
	frameImg  *toolkit.Image

	// dirty is set by a control's OnClick when a press did something, so
	// HandleMouse can tell the render loop whether to repaint.
	dirty bool
}

// Layout constants (pixels).
const (
	toolbarH = 46
	btnW     = 30
	btnH     = 28
	btnLeft  = 12
	btnGap   = 6
	addrGap  = 14
	addrH    = 28
	tileCols = 4
	tileW    = 150
	tileH    = 98
	tileGapX = 22
	tileGapY = 26
	gridTop  = toolbarH + 56
	gridLeft = 40

	btnY          = (toolbarH - btnH) / 2
	headingOffset = 28
	gridTopInset  = gridTop - toolbarH
	gridW         = tileCols*tileW + (tileCols-1)*tileGapX
	zeroGap       = -1
)

// skeletonGrey is the loading-skeleton placeholder tone: a light neutral grey
// (#d6d6da) that reads as the familiar "content coming" bar on a white content
// ground and stays clearly distinct from blank white (#fff), the window/header
// greys (#f5f5f5 / #ebebeb) and any streamed page frame — so the loading state
// is unambiguous both to the eye and to a pixel probe.
var skeletonGrey = toolkit.RGB(0xd6, 0xd6, 0xda)

// SkelCycleSeconds is the shimmer sweep period (seconds): the client advances
// the skeleton phase as elapsedSeconds/SkelCycleSeconds so one gleam crosses
// the page roughly this often.
const SkelCycleSeconds = 1.2

// New builds the browser sized W×H and lays out its widget tree. The client
// starts disconnected (the offline panel shows) until main.go opens the proxy
// WebSocket and calls SetConnected(true).
func New(w, h int) *State {
	enableAAText()
	s := &State{W: w, H: h, theme: toolkit.WhiteSurLight()}

	// The loading skeleton draws its placeholder bars in a dedicated theme whose
	// SurfaceAlt is a proper web-skeleton grey. WhiteSur's own SurfaceAlt (#fbfbfb)
	// is a near-white that would leave the skeleton invisible against the white
	// content ground; skeletonGrey (#e2e2e6) reads as the familiar grey placeholder
	// on white, distinct from both blank white and any streamed frame.
	skelTheme := *s.theme
	skelTheme.SurfaceAlt = skeletonGrey
	s.skelTheme = &skelTheme
	// A page skeleton sized to the content area (below the toolbar). streamCard
	// rebuilds it on every (re)layout so its bar widths track the window width.
	s.skel = toolkit.NewPageSkeleton(toolkit.Rect{X: 0, Y: toolbarH, W: w, H: h - toolbarH})
	s.favs = []link{
		{"weft", "weft.dev"},
		{"claimward", "claimward.io"},
		{"go-quake1", "go-quake1.dev"},
		{"go-widgets", "go-widgets.dev"},
		{"wasmdesk", "wasmdesk.org"},
		{"GitHub", "github.com"},
		{"Docs", "docs.wasmdesk.org"},
		{"Wiki", "wiki.wasmdesk.org"},
	}

	// Toolbar controls are toolkit IconButtons. Back / forward grey out through
	// their Disabled() observable (kept in sync with the server's history flags by
	// syncNav) instead of a hand-drawn dim-ink closure, so a disabled button is
	// both muted and inert. The new-tab "+" button is drawn but inert (as in the
	// original shell).
	s.backBtn = toolkit.NewIconButton("<", func() { s.dirty = s.goBack() })
	s.fwdBtn = toolkit.NewIconButton(">", func() { s.dirty = s.goForward() })
	s.addBtn = toolkit.NewIconButton("+", func() {})
	s.syncNav()
	s.addr = &addrBar{s: s, onClick: func() { s.focusAddr(); s.dirty = true }}

	row := toolkit.NewHBox()
	row.Spacing = zeroGap
	row.AddFixed(spacer(), btnLeft)
	row.AddFixed(s.backBtn, btnW)
	row.AddFixed(spacer(), btnGap)
	row.AddFixed(s.fwdBtn, btnW)
	row.AddFixed(spacer(), addrGap)
	row.AddFlex(s.addr, 1)
	row.AddFixed(spacer(), addrGap)
	row.AddFixed(s.addBtn, btnW)
	row.AddFixed(spacer(), btnLeft)

	band := toolkit.NewVBox()
	band.Spacing = zeroGap
	band.AddFixed(spacer(), btnY)
	band.AddFixed(row, btnH)
	band.AddFixed(spacer(), toolbarH-btnH-btnY)

	// Favourites tile grid.
	s.grid = toolkit.NewVBox()
	s.grid.Spacing = tileGapY
	s.tiles = make([]*tile, len(s.favs))
	var rowBox *toolkit.HBox
	for i := range s.favs {
		if i%tileCols == 0 {
			rowBox = toolkit.NewHBox()
			rowBox.Spacing = tileGapX
			s.grid.AddFixed(rowBox, tileH)
		}
		fav := s.favs[i]
		t := newTile(fav, func() { s.dirty = s.startNavigate(fav.url) })
		s.tiles[i] = t
		rowBox.AddFixed(t, tileW)
	}

	// Content cards: favourites start page + streamed-frame / panel card.
	s.frameImg = toolkit.NewImage(nil, 0, 0) // pixels/dims filled by SetFrame
	s.startCard = &startCard{s: s, grid: s.grid}
	s.streamCrd = newStreamCard(s)
	s.card = &toolkit.CardLayout{Active: 0}
	s.content = toolkit.NewContainer(s.card)
	s.content.AddWidget(s.startCard) // card 0
	s.content.AddWidget(s.streamCrd) // card 1

	s.root = toolkit.NewDock(s.content)
	s.root.Dock(band, toolkit.DockTop, toolbarH)
	s.root.SetBounds(toolkit.Rect{X: 0, Y: 0, W: w, H: h})
	s.syncCard()
	return s
}

// spacer is an invisible, inert box cell used to carry a fixed gap.
func spacer() toolkit.Widget { return toolkit.NewContainer(nil) }

// --- state sinks (called by main.go from server messages) -----------------

// SetConnected records whether the proxy WebSocket is open. Losing the
// connection shows the offline panel.
func (s *State) SetConnected(connected bool) {
	s.connected = connected
	if !connected {
		s.status = "Browser proxy not connected"
	} else if s.status == "Browser proxy not connected" {
		s.status = ""
	}
	s.syncCard()
}

// SetState applies a server {state} message: the current URL, title and history
// availability. While the user is editing the address bar its text is left
// untouched.
func (s *State) SetState(url, title string, loading, canBack, canForward bool) {
	s.url, s.title = url, title
	s.loading, s.canBack, s.canFwd = loading, canBack, canForward
	s.syncNav()
	if !s.addrFocused {
		s.addrText = url
	}
	s.syncCard()
}

// syncNav mirrors the server's history availability onto the back / forward
// IconButtons' Disabled() observables, so an unavailable direction greys out and
// swallows its own clicks (its OnEvent early-returns while disabled).
func (s *State) syncNav() {
	s.backBtn.Disabled().Set(!s.canBack)
	s.fwdBtn.Disabled().Set(!s.canFwd)
}

// SetFrame applies a server {frame} message: pixels is the RGBA byte buffer of a
// w×h page slice to blit into the content area.
func (s *State) SetFrame(pixels []byte, w, h int) {
	if w <= 0 || h <= 0 || len(pixels) < w*h*4 {
		return
	}
	s.frameImg.Pixels, s.frameImg.W, s.frameImg.H = pixels, w, h
	s.frameW, s.frameH = w, h
	s.hasFrame = true
	s.loading = false
	s.awaitingFrame = false // the load completed: stop the skeleton
	s.connected = true
	s.status = ""
	s.syncCard()
}

// SetError applies a server {error} message, shown in the content panel until
// the next successful frame.
func (s *State) SetError(msg string) {
	s.status = msg
	s.loading = false
	s.awaitingFrame = false // the load failed: drop the skeleton for the error panel
	s.syncCard()
}

// BeginLoad puts the browser into the loading (skeleton) state without a URL —
// used by the client right after the proxy socket opens, so the content area
// shows the page skeleton while it awaits the proxy's first streamed frame.
// The first {frame} (or an {error}) clears it.
func (s *State) BeginLoad() {
	s.awaitingFrame = true
	s.syncCard()
}

// Loading reports whether the content area is showing the loading skeleton
// (connected, no pending error, and a load in flight). The client keeps its
// shimmer animation loop running exactly while this holds, so an idle browser
// never repaints.
func (s *State) Loading() bool { return s.showSkeleton() }

// SetPhase records the shimmer sweep position for the loading skeleton. The
// client advances it every animation frame (typically elapsedSeconds /
// SkelCycleSeconds); the skeleton wraps it into [0,1).
func (s *State) SetPhase(t float64) { s.phase = t }

// showSkeleton reports whether the loading skeleton should be shown: the proxy
// is connected, no error panel is pending, and a load is in flight — either
// locally (awaitingFrame, set by a navigation or BeginLoad) or reported by the
// server ({state loading:true}).
func (s *State) showSkeleton() bool {
	return s.connected && s.status == "" && (s.awaitingFrame || s.loading)
}

// Title returns the current page title (for the window/tab chrome).
func (s *State) Title() string { return s.title }

// ContentSize returns the pixel size of the content area below the toolbar — the
// viewport the client should ask the proxy to render, and the space a streamed
// frame is blitted into.
func (s *State) ContentSize() (w, h int) { return s.W, s.H - toolbarH }

// --- rendering ------------------------------------------------------------

// Render paints the browser: background bands first, then the widget tree.
func Render(s *State, buf []byte) {
	p := painter.NewPixelPainter(buf, s.W, s.H)
	th := s.theme
	headerBG := th.Extra["headerbar_bg_color"]
	if headerBG == (toolkit.RGBA{}) {
		headerBG = th.Background
	}

	// Content ground: white when a page is shown or a skeleton is loading (a
	// loading web page is a white sheet with grey placeholders), window grey
	// otherwise.
	contentBG := th.Background
	if s.hasFrame || s.showSkeleton() {
		contentBG = th.Surface
	}
	fillBox(p, toolkit.Rect{X: 0, Y: toolbarH, W: s.W, H: s.H - toolbarH}, contentBG)

	// Toolbar band + bottom hairline.
	fillBox(p, toolkit.Rect{X: 0, Y: 0, W: s.W, H: toolbarH}, headerBG)
	fillBox(p, toolkit.Rect{X: 0, Y: toolbarH - 1, W: s.W, H: 1}, th.Border)

	s.root.Draw(p, th)
}

// --- input ----------------------------------------------------------------

// HandleMouse routes a click at surface coordinates through the widget tree.
// Returns true when a click triggered an action (the scene should re-render).
func (s *State) HandleMouse(x, y int) bool {
	s.dirty = false
	rb := s.root.Bounds()
	s.root.OnEvent(toolkit.Event{Kind: toolkit.EventClick, X: x - rb.X, Y: y - rb.Y})
	return s.dirty
}

// HandleWheel forwards a wheel delta to the proxy as a scroll intent when a page
// is shown. Returns true if the intent was sent.
func (s *State) HandleWheel(dy int) bool {
	if !s.hasFrame || s.OnScroll == nil || dy == 0 {
		return false
	}
	s.OnScroll(dy)
	return true
}

// HandleKey routes a key. While the address bar is focused it edits the address
// (Enter navigates, Escape cancels, Backspace deletes, printable keys append).
// Otherwise, when a page is shown, the key is forwarded to the proxy (for
// server-side scrolling). Returns true if the key was consumed.
func (s *State) HandleKey(key string) bool {
	if s.addrFocused {
		switch key {
		case "Enter":
			s.addrFocused = false
			return s.startNavigate(s.addrText)
		case "Escape":
			s.addrFocused = false
			s.addrText = s.url
			return true
		case "Backspace":
			s.addrText = trimLastRune(s.addrText)
			return true
		default:
			if isPrintable(key) {
				s.addrText += key
				return true
			}
			return false
		}
	}
	if s.hasFrame && s.OnContentKey != nil {
		s.OnContentKey(key)
		return true
	}
	return false
}

// --- model helpers --------------------------------------------------------

// focusAddr begins editing the address bar, seeding it with the current URL.
func (s *State) focusAddr() {
	s.addrFocused = true
	s.addrText = s.url
}

// startNavigate normalises url and emits a navigate intent, entering the
// loading state. It reports whether a navigation was started.
func (s *State) startNavigate(url string) bool {
	u := normalizeURL(url)
	if u == "" {
		return false
	}
	s.addrFocused = false
	s.loading = true
	s.awaitingFrame = true // show the skeleton until this navigation's first frame
	s.status = ""
	s.syncCard()
	if s.OnNavigate != nil {
		s.OnNavigate(u)
	}
	return true
}

func (s *State) goBack() bool {
	if !s.canBack {
		return false
	}
	s.loading = true
	s.awaitingFrame = true
	s.syncCard()
	if s.OnBack != nil {
		s.OnBack()
	}
	return true
}

func (s *State) goForward() bool {
	if !s.canFwd {
		return false
	}
	s.loading = true
	s.awaitingFrame = true
	s.syncCard()
	if s.OnForward != nil {
		s.OnForward()
	}
	return true
}

// streaming reports whether the content area shows the stream card (a frame or
// an offline/loading/error panel) rather than the favourites start page.
func (s *State) streaming() bool {
	return s.hasFrame || !s.connected || s.loading || s.awaitingFrame || s.status != ""
}

// syncCard points the CardLayout at the active content view and re-lays the
// content container so the shown card fills the body and the other collapses.
func (s *State) syncCard() {
	if s.streaming() {
		s.card.Active = 1
	} else {
		s.card.Active = 0
	}
	s.content.SetBounds(s.content.Bounds())
}

// addressText is what the address bar shows and whether it is placeholder text.
func (s *State) addressText() (string, bool) {
	if s.addrFocused {
		return s.addrText, false
	}
	if s.url != "" {
		return s.url, false
	}
	return "Search or enter website name", true
}

// --- leaf widgets ---------------------------------------------------------

// addrBar is the rounded white address pill. It is now interactive: a click
// focuses it (main.go's keydowns then edit the text), and it draws a caret
// while focused.
type addrBar struct {
	toolkit.Base
	s       *State
	onClick func()
}

func (a *addrBar) Draw(p painter.Painter, th *toolkit.Theme) {
	r := a.Bounds()
	p.FillRoundRect(r, addrH/2, th.Surface)
	stroke := th.Border
	if a.s.addrFocused {
		stroke = th.Accent
	}
	p.StrokeRoundRect(r, addrH/2, stroke, 1)
	txt, placeholder := a.s.addressText()
	ink := th.OnSurface
	if placeholder {
		ink = dim(th)
	}
	tx := r.X + 12
	ty := r.Y + (r.H-toolkit.GlyphHeight())/2
	toolkit.DrawText(p, tx, ty, txt, ink)
	if a.s.addrFocused {
		// A thin caret after the edited text.
		caretX := tx + toolkit.TextWidth(txt)
		p.FillRect(toolkit.Rect{X: caretX + 1, Y: ty, W: 1, H: toolkit.GlyphHeight()}, th.OnSurface)
	}
}

func (a *addrBar) OnEvent(ev toolkit.Event) {
	if ev.Kind == toolkit.EventClick && a.onClick != nil {
		a.onClick()
	}
}

// tileIconSize is the side length of a tile's accent avatar square.
const tileIconSize = 44

// tile is one Favourites bookmark, composed from toolkit widgets rather than
// hand-drawn shapes: a bordered [toolkit.Card] frame, an accent [toolkit.Avatar]
// carrying the site's initial, and a centred [toolkit.Label] with its name. A
// click navigates to the site via the proxy.
type tile struct {
	toolkit.Base
	fav     link
	onClick func()
	frame   *toolkit.Card
	avatar  *toolkit.Avatar
	label   *toolkit.Label
}

// newTile builds a favourites tile for fav whose click runs onClick.
func newTile(fav link, onClick func()) *tile {
	t := &tile{fav: fav, onClick: onClick}
	t.frame = toolkit.NewCard("", "", "")
	t.avatar = toolkit.NewAvatar(string(upper(fav.name[0])))
	t.label = toolkit.NewLabel(fav.name)
	t.label.Align = toolkit.AlignCenter
	return t
}

// SetBounds positions the card frame over the whole tile, the avatar as a centred
// square near the top and the name label along the bottom — the same geometry the
// old hand-placed tile produced.
func (t *tile) SetBounds(r toolkit.Rect) {
	t.Base.SetBounds(r)
	t.frame.SetBounds(r)
	ix := r.X + (r.W-tileIconSize)/2
	t.avatar.SetBounds(toolkit.Rect{X: ix, Y: r.Y + 14, W: tileIconSize, H: tileIconSize})
	t.label.SetBounds(toolkit.Rect{X: r.X, Y: r.Y + r.H - 16, W: r.W, H: toolkit.GlyphHeight()})
}

func (t *tile) Draw(p painter.Painter, th *toolkit.Theme) {
	t.frame.Draw(p, th)
	t.avatar.Draw(p, th)
	t.label.Draw(p, th)
}

func (t *tile) OnEvent(ev toolkit.Event) {
	if ev.Kind == toolkit.EventClick && t.onClick != nil {
		t.onClick()
	}
}

// startCard is the Favourites start page: heading + tile grid at their inset.
type startCard struct {
	toolkit.Base
	s    *State
	grid *toolkit.VBox
}

func (c *startCard) SetBounds(r toolkit.Rect) {
	c.Base.SetBounds(r)
	if r.W == 0 || r.H == 0 { // collapsed by CardLayout when inactive
		c.grid.SetBounds(toolkit.Rect{})
		return
	}
	c.grid.SetBounds(toolkit.Rect{
		X: r.X + gridLeft,
		Y: r.Y + gridTopInset,
		W: gridW,
		H: r.H - gridTopInset,
	})
}

func (c *startCard) Draw(p painter.Painter, th *toolkit.Theme) {
	r := c.Bounds()
	toolkit.DrawText(p, r.X+gridLeft, r.Y+headingOffset, "Favourites", th.OnBackground)
	c.grid.Draw(p, th)
}

func (c *startCard) OnEvent(ev toolkit.Event) {
	pr := c.Bounds()
	sx, sy := ev.X+pr.X, ev.Y+pr.Y
	gb := c.grid.Bounds()
	if gb.Contains(sx, sy) {
		fwd := ev
		fwd.X, fwd.Y = sx-gb.X, sy-gb.Y
		c.grid.OnEvent(fwd)
	}
}

// offlineHead / offlineDetail and errorHead are the panel copy for the two
// no-frame states, hoisted to package scope so a test can assert the EmptyState
// carries exactly this text.
const (
	offlineHead   = "Browser proxy not connected"
	offlineDetail = "Start a local go-webengine browserproxy " +
		"(go run ./cmd/browserproxy -addr :8090), then reload this window."
	errorHead = "Could not load the page"
)

// streamCard shows the streamed page frame, or — when there is no frame — an
// offline / error panel drawn as a centred [toolkit.EmptyState].
type streamCard struct {
	toolkit.Base
	s     *State
	panel *toolkit.EmptyState
}

// newStreamCard builds the stream card with its reusable EmptyState panel (its
// message + caption are re-set per draw for the offline vs error case).
func newStreamCard(s *State) *streamCard {
	c := &streamCard{s: s, panel: toolkit.NewEmptyState("")}
	c.panel.SetCaption("")
	return c
}

func (c *streamCard) SetBounds(r toolkit.Rect) {
	c.Base.SetBounds(r)
	c.s.frameImg.SetBounds(r)
	// Rebuild the page skeleton to the live content rect so its placeholder bars
	// span the current window width. CardLayout collapses an inactive card to a
	// zero rect; skip those so the skeleton keeps its last real geometry.
	if r.W > 0 && r.H > 0 {
		c.s.skel = toolkit.NewPageSkeleton(r)
	}
}

func (c *streamCard) Draw(p painter.Painter, th *toolkit.Theme) {
	r := c.Bounds()
	if r.W == 0 || r.H == 0 {
		return
	}
	// A load in flight (navigation or initial connect, no frame yet): a web-style
	// page skeleton with a shimmer swept by the client's phase clock. It is drawn
	// in skelTheme so the placeholder bars read as a visible grey on the white
	// content ground.
	if c.s.showSkeleton() {
		c.s.skel.SetPhase(c.s.phase)
		c.s.skel.Draw(p, c.s.skelTheme)
		return
	}
	if c.s.hasFrame {
		c.s.frameImg.Draw(p, th)
		return
	}
	// No frame and not loading: a centred EmptyState. The stream card is only
	// active (drawn) when the proxy is disconnected or an error is pending — so one
	// of these two cases always holds here. The message + caption observables are
	// re-set for whichever case applies, then the panel is centred in the content
	// rect and drawn (its caption renders in the theme's muted ink).
	if !c.s.connected {
		c.panel.Message().Set(offlineHead)
		c.panel.Caption().Set(offlineDetail)
	} else { // c.s.status != ""
		c.panel.Message().Set(errorHead)
		c.panel.Caption().Set(c.s.status)
	}
	c.panel.SetBounds(r)
	c.panel.Draw(p, th)
}

func (c *streamCard) OnEvent(ev toolkit.Event) {
	// ev is streamCard-local, so its origin is the content-area top-left, which
	// maps 1:1 onto the streamed frame's (0,0). Forward content clicks only when
	// a frame is shown.
	if ev.Kind != toolkit.EventClick || !c.s.hasFrame || c.s.OnContentClick == nil {
		return
	}
	c.s.OnContentClick(ev.X, ev.Y)
	c.s.dirty = true
}

// --- helpers --------------------------------------------------------------

// normalizeURL trims s and, when it has no scheme, assumes https. An empty
// string stays empty.
func normalizeURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	return s
}

// isPrintable reports whether a KeyboardEvent.key names a single printable
// character (so named keys like "Shift" or "ArrowDown" are not typed).
func isPrintable(key string) bool {
	return len([]rune(key)) == 1
}

// trimLastRune drops the final rune of s (Backspace).
func trimLastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(r[:len(r)-1])
}

// dim returns a muted ink for placeholder / disabled text.
func dim(*toolkit.Theme) toolkit.RGBA { return toolkit.RGB(0x80, 0x80, 0x88) }

func upper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 32
	}
	return b
}
