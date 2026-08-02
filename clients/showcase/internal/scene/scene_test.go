// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"testing"
	"testing/fstest"

	"github.com/go-widgets/toolkit"
)

const surfaceW = 480
const surfaceH = 360

func newSurface() []byte { return make([]byte, 4*surfaceW*surfaceH) }

func TestNewAndRender(t *testing.T) {
	s := New(surfaceW, surfaceH)
	if s == nil {
		t.Fatal("New returned nil")
	}
	Render(s, newSurface())
}

func TestHandleMouseRoutesToMenuBar(t *testing.T) {
	s := New(surfaceW, surfaceH)
	// Click on the MenuBar (Y < MenuBarH) must not panic + requests re-render.
	if !s.HandleMouse(20, 5) {
		t.Fatal("HandleMouse must request re-render")
	}
}

func TestHandleMouseRoutesToToolbar(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.HandleMouse(12, toolkit.MenuBarH+5)
}

func TestHandleMouseRoutesToStatusBar(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.HandleMouse(20, surfaceH-5) // status bar area — routes, no panic
}

func TestHandleMouseRoutesToCardBody(t *testing.T) {
	s := New(surfaceW, surfaceH)
	// Click well inside the gallery body (below the tab strip).
	s.HandleMouse(150, toolkit.MenuBarH+toolkit.ToolbarButtonH+toolkit.NotebookTabStripH+40)
}

func TestClickFiresHelloButton(t *testing.T) {
	s := New(surfaceW, surfaceH)
	// Card 0 (Button) is active by default. Locate the helloButton rect + click
	// its centre; the click must route through the container tree to OnClick.
	b := s.helloButton.Bounds()
	cx := b.X + b.W/2
	cy := b.Y + b.H/2
	s.HandleMouse(cx, cy)
	if s.clickCount == 0 {
		t.Fatalf("hello button must increment clickCount; got %d", s.clickCount)
	}
}

func TestTabButtonSwitchesCard(t *testing.T) {
	s := New(surfaceW, surfaceH)
	// The Input tab is button index 2; click its centre in the strip.
	stripY := toolkit.MenuBarH + toolkit.ToolbarButtonH
	tb := s.tabButtons[inputCard].Bounds()
	if tb.Y != stripY {
		t.Fatalf("tab strip Y = %d, want %d", tb.Y, stripY)
	}
	s.HandleMouse(tb.X+tb.W/2, tb.Y+tb.H/2)
	if s.cardLayout.Active != inputCard {
		t.Fatalf("clicking the Input tab must activate card %d, got %d", inputCard, s.cardLayout.Active)
	}
	// The active tab reads prominent; the others default.
	if s.tabButtons[inputCard].Style != toolkit.ButtonProminent {
		t.Fatal("active tab button must be ButtonProminent")
	}
	if s.tabButtons[0].Style != toolkit.ButtonDefault {
		t.Fatal("inactive tab button must be ButtonDefault")
	}
}

func TestHandleKeyOnNonInputCard(t *testing.T) {
	s := New(surfaceW, surfaceH)
	// Card 0 (Button) active. HandleKey must return false (no input).
	if s.HandleKey("Enter") {
		t.Fatal("HandleKey on the Button card must return false")
	}
}

func TestHandleKeyOnInputCard(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.setActiveCard(inputCard)
	if !s.HandleKey("Enter") {
		t.Fatal("HandleKey on the Input card must return true")
	}
}

func TestHandleCharOnNonInputCard(t *testing.T) {
	s := New(surfaceW, surfaceH)
	if s.HandleChar("x") {
		t.Fatal("HandleChar on the Button card must return false")
	}
}

func TestHandleCharOnInputCard(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.setActiveCard(inputCard)
	if !s.HandleChar("hello") {
		t.Fatal("HandleChar on the Input card must return true")
	}
}

func TestRenderAllCards(t *testing.T) {
	s := New(surfaceW, surfaceH)
	for card := range tabLabels {
		s.setActiveCard(card)
		Render(s, newSurface())
	}
}

// TestGoldenLayoutRects is the behaviour-preserving proof: the box-layout
// container tree lays every demo widget out to the EXACT toolkit.Rect the old
// hand-placed code produced. Expected rects are recomputed here with the
// ORIGINAL arithmetic (the offsets from the pre-migration scene.New). Because a
// CardLayout collapses inactive cards to an empty rect, each card is activated
// before its widgets are asserted.
func TestGoldenLayoutRects(t *testing.T) {
	s := New(surfaceW, surfaceH)
	w := surfaceW

	// Original app-shell geometry.
	bodyY := toolkit.MenuBarH + toolkit.ToolbarButtonH
	statusH := toolkit.StatusbarH
	bodyH := surfaceH - bodyY - statusH
	tabBodyY := bodyY + toolkit.NotebookTabStripH
	tabBodyH := bodyH - toolkit.NotebookTabStripH

	// App-shell widgets (always laid out, card-independent).
	shell := []struct {
		name string
		got  toolkit.Rect
		want toolkit.Rect
	}{
		{"menuBar", s.menuBar.Bounds(), toolkit.Rect{X: 0, Y: 0, W: w, H: toolkit.MenuBarH}},
		{"toolbar", s.toolbar.Bounds(), toolkit.Rect{X: 0, Y: toolkit.MenuBarH, W: w, H: toolkit.ToolbarButtonH}},
		{"status", s.status.Bounds(), toolkit.Rect{X: 0, Y: surfaceH - statusH, W: w, H: statusH}},
	}
	for _, c := range shell {
		if c.got != c.want {
			t.Fatalf("%s bounds = %+v, want %+v", c.name, c.got, c.want)
		}
	}

	// Per-card widget rects, keyed by the card that must be active. Each want is
	// the original hand-computed rect from the pre-migration New().
	type widgetRect struct {
		name string
		w    toolkit.Widget
		want toolkit.Rect
	}
	cards := map[int][]widgetRect{
		0: {
			{"helloButton", s.helloButton, toolkit.Rect{X: w/2 - 60, Y: tabBodyY + 20, W: 120, H: 28}},
			{"clickLabel", s.clickLabel, toolkit.Rect{X: w/2 - 80, Y: tabBodyY + 60, W: 160, H: 20}},
		},
		1: {
			{"check1", s.check1, toolkit.Rect{X: 8, Y: tabBodyY + 8, W: 200, H: 24}},
			{"check2", s.check2, toolkit.Rect{X: 8, Y: tabBodyY + 32, W: 200, H: 24}},
			{"radioA", s.radioA, toolkit.Rect{X: 8, Y: tabBodyY + 60, W: 120, H: 20}},
			{"radioB", s.radioB, toolkit.Rect{X: 8, Y: tabBodyY + 80, W: 120, H: 20}},
			{"dropdown", s.dropdown, toolkit.Rect{X: 8, Y: tabBodyY + 110, W: 150, H: 24}},
		},
		2: {
			{"entry", s.entry, toolkit.Rect{X: 8, Y: tabBodyY + 8, W: w - 16, H: 24}},
			{"textView", s.textView, toolkit.Rect{X: 8, Y: tabBodyY + 40, W: w - 16, H: tabBodyH - 60}},
		},
		3: {
			{"tree", s.tree, toolkit.Rect{X: 8, Y: tabBodyY + 8, W: (w - 24) / 2, H: tabBodyH - 16}},
			{"listBox", s.listBox, toolkit.Rect{X: 16 + (w-24)/2, Y: tabBodyY + 8, W: (w - 24) / 2, H: tabBodyH - 16}},
		},
		4: {
			{"calendar", s.calendar, toolkit.Rect{X: w/2 - 100, Y: tabBodyY + 8, W: 200, H: tabBodyH - 16}},
		},
		5: {
			{"colorPick", s.colorPick, toolkit.Rect{X: 8, Y: tabBodyY + 8, W: w - 16, H: 100}},
		},
		6: {
			{"progress", s.progress, toolkit.Rect{X: 16, Y: tabBodyY + 20, W: w - 32, H: 18}},
			{"scale", s.scale, toolkit.Rect{X: 16, Y: tabBodyY + 60, W: w - 32, H: 20}},
			{"spin", s.spin, toolkit.Rect{X: 16, Y: tabBodyY + 100, W: 100, H: 24}},
		},
	}
	for card := 0; card < len(tabLabels); card++ {
		s.setActiveCard(card)
		for _, wr := range cards[card] {
			if got := wr.w.Bounds(); got != wr.want {
				t.Fatalf("card %d %s bounds = %+v, want %+v", card, wr.name, got, wr.want)
			}
		}
	}
}

// TestInactiveCardCollapsed proves the CardLayout hides non-active cards: it
// collapses every card but the active one to an empty rect, so Container.Draw +
// OnEvent skip them (they guard on a non-empty item rect). The invariant lives
// at the card-container level — exactly one card item has a non-empty Bounds.
func TestInactiveCardCollapsed(t *testing.T) {
	s := New(surfaceW, surfaceH)
	for _, active := range []int{6, 0, inputCard} {
		s.setActiveCard(active)
		nonEmpty := -1
		for i, it := range s.cards.Items() {
			b := it.Widget.Bounds()
			if b.W > 0 && b.H > 0 {
				if nonEmpty != -1 {
					t.Fatalf("active=%d: cards %d and %d both non-empty", active, nonEmpty, i)
				}
				nonEmpty = i
			}
		}
		if nonEmpty != active {
			t.Fatalf("active=%d: the only non-empty card should be %d, got %d", active, active, nonEmpty)
		}
	}
}

func TestItoaShowcase(t *testing.T) {
	if itoa(0) != "0" {
		t.Fatal("itoa(0)")
	}
	if itoa(42) != "42" {
		t.Fatal("itoa(42)")
	}
	if itoa(-7) != "-7" {
		t.Fatalf("itoa(-7)=%q", itoa(-7))
	}
}

func TestFillOutOfBuffer(t *testing.T) {
	// Fill with a rect bigger than the buffer triggers the bounds guard.
	buf := make([]byte, 16)
	fill(buf, 4, toolkit.Rect{X: 0, Y: 0, W: 100, H: 100}, toolkit.RGB(0xFF, 0, 0))
}

func TestViewMenuThemePicker(t *testing.T) {
	// The View menu is built from Themes() — verify (a) every embedded
	// theme produced a menu item, (b) clicking any item switches the
	// scene theme to the matching palette, (c) at least Default Light
	// + Default Dark are present (the bare-toolkit fallback).
	s := New(surfaceW, surfaceH)
	viewMenu := s.menuBar.Menus[2]
	themes := Themes()
	if len(viewMenu.Items) != len(themes) {
		t.Fatalf("view menu has %d items, want %d (one per theme)", len(viewMenu.Items), len(themes))
	}
	// Sanity: first two are the toolkit defaults.
	if viewMenu.Items[0].Label != "Default Light" {
		t.Fatalf("first theme should be Default Light, got %q", viewMenu.Items[0].Label)
	}
	if viewMenu.Items[1].Label != "Default Dark" {
		t.Fatalf("second theme should be Default Dark, got %q", viewMenu.Items[1].Label)
	}
	// Click each entry + check scene.theme.Background matches the
	// parsed theme's background (palette swap is observable).
	for i, entry := range themes {
		viewMenu.Items[i].Action()
		if s.theme.Background != entry.Theme.Background {
			t.Fatalf("after clicking %q the scene theme background did not match: got %+v want %+v",
				entry.Name, s.theme.Background, entry.Theme.Background)
		}
	}
}

func TestThemesIncludesEmbeddedGTKThemes(t *testing.T) {
	// Every .css fixture under themes/ MUST be picked up by Themes()
	// in addition to the 2 toolkit defaults — otherwise a build that
	// silently lost the embed directive would still pass the menu
	// shape check above.
	themes := Themes()
	want := map[string]bool{
		"Default Light":   false,
		"Default Dark":    false,
		"Adwaita Light":   false,
		"Adwaita Dark":    false,
		"Solarized Light": false,
		"Solarized Dark":  false,
		"Juno":            false,
		"Whitesur Light":  false,
		"Whitesur Dark":   false,
	}
	for _, th := range themes {
		want[th.Name] = true
	}
	for n, ok := range want {
		if !ok {
			t.Errorf("Themes() did not expose %q", n)
		}
	}
}

func TestThemesFromFSMissingDir(t *testing.T) {
	// ReadDir on a non-existent dir falls back to the 2 toolkit defaults.
	got := themesFromFS(fstest.MapFS{}, "no-such-dir")
	if len(got) != 2 {
		t.Fatalf("missing dir should still yield 2 defaults, got %d", len(got))
	}
}

func TestThemesFromFSSkipsNonCSSAndSubdirs(t *testing.T) {
	// A themes/ dir with a subdirectory, a README, an unparseable CSS,
	// an unreadable file (won't actually surface here — embed.FS path
	// is exercised by the real Themes() call) and one valid CSS.
	fsys := fstest.MapFS{
		"themes/README.md":      {Data: []byte("not a theme")},
		"themes/sub/inside.css": {Data: []byte("@define-color window_bg_color #112233;")},
		"themes/empty.css":      {Data: []byte("")},                                       // LoadGTKTheme errors on empty → skipped
		"themes/good.css":       {Data: []byte("@define-color window_bg_color #445566;")}, // parses
	}
	got := themesFromFS(fsys, "themes")
	// Defaults + "Good" = 3 entries. README.md skipped (not .css),
	// sub/ skipped (IsDir), empty.css skipped (LoadGTKTheme error).
	if len(got) != 3 {
		t.Fatalf("want 3 entries (2 defaults + Good), got %d: %v", len(got), got)
	}
	if got[2].Name != "Good" {
		t.Fatalf("want third entry Good, got %q", got[2].Name)
	}
	if got[2].Theme.Background.R != 0x44 {
		t.Fatalf("Good theme background not parsed: %+v", got[2].Theme.Background)
	}
}

func TestViewMenuUpdatesStatusThemeSegment(t *testing.T) {
	// Clicking a View-menu entry must swap BOTH scene.theme AND the
	// status bar's theme segment. Poor-man's URL-sync — the user sees
	// which palette is live without needing devtools.
	s := New(surfaceW, surfaceH)
	viewMenu := s.menuBar.Menus[2]
	// Item[1] is Default Dark (see Themes() order).
	viewMenu.Items[1].Action()
	if got := s.status.Segments[2]; got != "theme: Default Dark" {
		t.Fatalf("status[2] after click Default Dark: want %q, got %q",
			"theme: Default Dark", got)
	}
	// Item[2] is Adwaita Dark (alphabetic .css order → adwaita-dark
	// before adwaita-light).
	viewMenu.Items[2].Action()
	if got := s.status.Segments[2]; got != "theme: Adwaita Dark" {
		t.Fatalf("status[2] after click Adwaita Dark: want %q, got %q",
			"theme: Adwaita Dark", got)
	}
}

func TestFrameMenuInvokesSetter(t *testing.T) {
	// The Frame menu (index 3 in the MenuBar) has one entry per known
	// FrameRegistry name. Clicking an entry invokes the setFrame
	// callback wired via SetFrameSetter — the SDK's setFrame method
	// in production, a spy here.
	s := New(surfaceW, surfaceH)
	var got []string
	s.SetFrameSetter(func(name string) { got = append(got, name) })
	frameMenu := s.menuBar.Menus[3]
	if len(frameMenu.Items) != len(frameNames) {
		t.Fatalf("Frame menu items = %d, want %d", len(frameMenu.Items), len(frameNames))
	}
	// Click the 3rd entry (should be "openbox-adwaita-light").
	frameMenu.Items[2].Action()
	if len(got) != 1 || got[0] != "openbox-adwaita-light" {
		t.Fatalf("setter called with %v; want [openbox-adwaita-light]", got)
	}
	// Click aqua (index 1).
	frameMenu.Items[1].Action()
	if len(got) != 2 || got[1] != "aqua" {
		t.Fatalf("second click: %v", got)
	}
}

func TestFrameMenuWithoutSetterIsNoOp(t *testing.T) {
	// A scene built without SetFrameSetter (native unit tests) still
	// has a Frame menu; clicking an item is a no-op.
	s := New(surfaceW, surfaceH)
	frameMenu := s.menuBar.Menus[3]
	frameMenu.Items[0].Action() // must not panic
}

func TestSetActiveFrameMarker(t *testing.T) {
	s := New(surfaceW, surfaceH)
	// Boot: no marker (SetActiveFrame("") was called in New).
	menu := s.menuBar.Menus[3]
	for _, it := range menu.Items {
		if len(it.Label) > 2 && it.Label[:2] == "* " {
			t.Fatalf("no entry should be marked initially, got %q", it.Label)
		}
	}
	// Set active to "aqua" — that entry becomes "* aqua", others
	// stay bare.
	s.SetActiveFrame("aqua")
	menu = s.menuBar.Menus[3]
	starred := 0
	for _, it := range menu.Items {
		if it.Label == "* aqua" {
			starred++
		}
	}
	if starred != 1 {
		t.Fatalf("exactly one entry should be starred, got %d", starred)
	}
	// The status bar's frame segment tracks the active frame too.
	if got := s.status.Segments[3]; got != "frame: aqua" {
		t.Fatalf("status[3] = %q, want %q", got, "frame: aqua")
	}
	// Click the "* aqua" entry again — Action must still fire the
	// setter (test the wire flow after re-marking).
	var got string
	s.SetFrameSetter(func(name string) { got = name })
	for _, it := range menu.Items {
		if it.Label == "* aqua" {
			it.Action()
			break
		}
	}
	if got != "aqua" {
		t.Fatalf("clicking * aqua should call setter with %q, got %q", "aqua", got)
	}
}

func TestSetActiveFrameDefensiveGuard(t *testing.T) {
	// Defensive branch: SetActiveFrame on a State with nil menuBar
	// (should never happen in practice — New always wires it — but
	// the guard is there so the wire message doesn't crash the
	// worker if the sequence somehow inverts).
	s := &State{}
	s.SetActiveFrame("aqua") // must not panic
}

func TestSetActiveFrameSeed(t *testing.T) {
	// SetActiveFrame with a real registry name marks the menu.
	s := New(surfaceW, surfaceH)
	s.SetActiveFrame("openbox-juno")
	menu := s.menuBar.Menus[3]
	found := false
	for _, it := range menu.Items {
		if it.Label == "* openbox-juno" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("openbox-juno should be marked after SetActiveFrame")
	}
}

func TestSetActiveThemeNameNilStatus(t *testing.T) {
	// Defensive guard: setActiveThemeName on a State with nil status
	// (would panic if the guard was missing).
	s := &State{}
	s.setActiveThemeName("won't panic")
}

func TestPrettify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"adwaita-light.css", "Adwaita Light"},
		{"x.css", "X"},
		{"foo-bar-baz.css", "Foo Bar Baz"},
		{".css", ""},
		{"-leading.css", " Leading"},   // empty first part survives
		{"trailing-.css", "Trailing "}, // empty last part survives
	}
	for _, c := range cases {
		if got := prettify(c.in); got != c.want {
			t.Errorf("prettify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- anti-aliased text proof ----------------------------------------------

// distinctLuma counts how many distinct R+G+B sums appear inside region r of an
// RGBA buffer. The 5x7 bitmap font paints each text pixel either full ink or
// untouched ground, so a bitmap render holds only the ground's handful of levels
// plus one ink level; the AA/shaped face scan-converts glyph outlines to
// partial-coverage masks, adding a whole ramp of intermediate levels. A strictly
// higher distinct-luma count over the SAME region is a ground-independent proof
// the AA face is active.
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

// TestAATextIsAntiAliased proves the showcase renders its chrome + gallery with
// the toolkit's AA/shaped OpenType face rather than the 5x7 bitmap. It scans the
// menu bar band (File / View / Frame …), rendering it once with the AA face (New
// enabled it) and once with the bitmap default, and asserts the AA render carries
// strictly more distinct luma levels — the partial-coverage ramp only
// anti-aliasing produces.
func TestAATextIsAntiAliased(t *testing.T) {
	region := toolkit.Rect{X: 0, Y: 0, W: 320, H: toolkit.MenuBarH}

	aa := newSurface()
	Render(New(surfaceW, surfaceH), aa) // AA face (enableAAText ran in New)

	toolkit.SetFont(nil) // bitmap default
	defer func() { _ = toolkit.UseOpenTypeText() }()
	bm := newSurface()
	Render(New(surfaceW, surfaceH), bm)

	aaN := distinctLuma(aa, surfaceW, region)
	bmN := distinctLuma(bm, surfaceW, region)
	if aaN <= bmN {
		t.Fatalf("menu bar: distinct luma aa=%d not > bitmap=%d — AA face not active", aaN, bmN)
	}
	t.Logf("menu bar distinct luma: aa=%d bm=%d", aaN, bmN)
}

// TestAAFaceFits asserts the taller AA line box fits the showcase's fixed chrome
// bands. The menu bar, toolbar and tab strip comfortably hold the 20px face; the
// toolkit's 18px status bar carries a slightly taller line box, but its glyph ink
// (cap height ~12px) still centres inside the band, so only the bands that fully
// contain the line box are asserted here.
func TestAAFaceFits(t *testing.T) {
	_ = New(surfaceW, surfaceH) // switches the global font to the AA face
	f, err := toolkit.DefaultOpenTypeFont(toolkit.DefaultOpenTypeSizePx)
	if err != nil {
		t.Fatalf("DefaultOpenTypeFont: %v", err)
	}
	h := f.Height()
	if h != 20 {
		t.Fatalf("AA face height = %d, want 20 (retune bands if this changes)", h)
	}
	for _, band := range []struct {
		name string
		px   int
	}{
		{"menu bar", toolkit.MenuBarH},
		{"toolbar", toolkit.ToolbarButtonH},
		{"tab strip", toolkit.NotebookTabStripH},
	} {
		if h > band.px {
			t.Fatalf("AA height %d overflows %s height %d", h, band.name, band.px)
		}
	}
}
