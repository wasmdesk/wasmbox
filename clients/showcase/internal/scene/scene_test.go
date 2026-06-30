// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"testing"

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

func TestViewMenuThemeActions(t *testing.T) {
	// Light/Dark theme actions on the View menu must fire when invoked.
	s := New(surfaceW, surfaceH)
	viewMenu := s.menuBar.Menus[2]
	// View menu items: [Light Theme, Dark Theme].
	viewMenu.Items[1].Action() // dark
	if s.theme == toolkit.DefaultLight() {
		// pointer equality won't hold; check via Background instead.
	}
	if s.theme.Background == toolkit.DefaultLight().Background {
		t.Fatal("Dark theme action must change theme")
	}
	viewMenu.Items[0].Action() // light
	if s.theme.Background != toolkit.DefaultLight().Background {
		t.Fatal("Light theme action must restore light")
	}
}

func TestLocalize(t *testing.T) {
	ev := localize(toolkit.Event{X: 25, Y: 15}, toolkit.Rect{X: 10, Y: 5})
	if ev.X != 15 || ev.Y != 10 {
		t.Fatalf("localize wrong: %+v", ev)
	}
}
