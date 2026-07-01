// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"testing"
	"testing/fstest"

	"github.com/wasmdesk/toolkit"
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
	// Click on the MenuBar (Y < MenuBarH).
	if !s.HandleMouse(20, 5) {
		t.Fatal("HandleMouse must request re-render")
	}
}

func TestHandleMouseRoutesToToolbar(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.HandleMouse(12, toolkit.MenuBarH+5)
}

func TestHandleMouseRoutesToNotebook(t *testing.T) {
	s := New(surfaceW, surfaceH)
	// Click well inside the notebook area on a tab strip cell.
	s.HandleMouse(150, toolkit.MenuBarH+toolkit.ToolbarButtonH+5)
}

func TestHandleMouseStatusBarNoOp(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.HandleMouse(20, surfaceH-5) // status bar area
}

func TestClickFiresHelloButton(t *testing.T) {
	s := New(surfaceW, surfaceH)
	// Notebook active=0 by default (Button tab). Locate the helloButton
	// rect + click its centre.
	b := s.helloButton.Bounds()
	cx := b.X + b.W/2
	cy := b.Y + b.H/2
	s.HandleMouse(cx, cy)
	if s.clickCount == 0 {
		t.Fatalf("hello button must increment clickCount; got %d", s.clickCount)
	}
}

func TestHandleKeyOnNonInputTab(t *testing.T) {
	s := New(surfaceW, surfaceH)
	// Active tab 0 (Button). HandleKey must return false (no input).
	if s.HandleKey("Enter") {
		t.Fatal("HandleKey on Button tab must return false")
	}
}

func TestHandleKeyOnInputTab(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.notebook.Active = 2 // Input tab
	if !s.HandleKey("Enter") {
		t.Fatal("HandleKey on Input tab must return true")
	}
}

func TestHandleCharOnNonInputTab(t *testing.T) {
	s := New(surfaceW, surfaceH)
	if s.HandleChar("x") {
		t.Fatal("HandleChar on Button tab must return false")
	}
}

func TestHandleCharOnInputTab(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.notebook.Active = 2
	if !s.HandleChar("hello") {
		t.Fatal("HandleChar on Input tab must return true")
	}
}

func TestRenderAllTabs(t *testing.T) {
	s := New(surfaceW, surfaceH)
	for tab := 0; tab < 7; tab++ {
		s.notebook.Active = tab
		Render(s, newSurface())
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

func TestInsideRect(t *testing.T) {
	r := toolkit.Rect{X: 10, Y: 10, W: 20, H: 20}
	if !insideRect(15, 15, r) {
		t.Fatal("centre must be inside")
	}
	if insideRect(0, 0, r) {
		t.Fatal("(0,0) must be outside")
	}
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

func TestLocalize(t *testing.T) {
	ev := localize(toolkit.Event{X: 25, Y: 15}, toolkit.Rect{X: 10, Y: 5})
	if ev.X != 15 || ev.Y != 10 {
		t.Fatalf("localize wrong: %+v", ev)
	}
}
