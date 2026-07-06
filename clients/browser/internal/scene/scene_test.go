// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"strings"
	"testing"
)

func newState() *State { return New(760, 500) }

func TestNew(t *testing.T) {
	s := newState()
	if len(s.favs) != 8 {
		t.Fatalf("favs = %d, want 8", len(s.favs))
	}
	if len(s.tileRects) != len(s.favs) {
		t.Fatalf("tileRects = %d, want %d", len(s.tileRects), len(s.favs))
	}
	if s.onSite || s.visited {
		t.Error("browser should start on the Favourites page, unvisited")
	}
}

func TestAddressText(t *testing.T) {
	s := newState()
	if got := s.addressText(); !strings.Contains(got, "Search") {
		t.Errorf("start address text = %q, want the search placeholder", got)
	}
	s.navigate(0)
	if got := s.addressText(); got != s.favs[0].url {
		t.Errorf("site address text = %q, want %q", got, s.favs[0].url)
	}
}

func TestRenderStartAndSite(t *testing.T) {
	s := newState()
	buf := make([]byte, 4*s.W*s.H)
	Render(s, buf) // start page (renderStart + both upper() branches via mixed-case favs)
	s.navigate(2)
	Render(s, buf) // site page (renderSite, onSite content/ink branches)
	s.back()
	Render(s, buf) // start page after a visit -> Forward-enabled ink branch
}

func TestRenderThemeFallbacks(t *testing.T) {
	s := newState()
	delete(s.theme.Extra, "headerbar_bg_color") // -> headerBG fallback
	delete(s.theme.Extra, "accent_fg_color")    // -> renderStart onAccent fallback
	Render(s, make([]byte, 4*s.W*s.H))
}

func TestNavigateViaTileClick(t *testing.T) {
	s := newState()
	r := s.tileRects[0]
	if !s.HandleMouse(r.X+r.W/2, r.Y+r.H/2) {
		t.Fatal("clicking a favourite tile should navigate + request a redraw")
	}
	if !s.onSite || s.cur != 0 || !s.visited {
		t.Errorf("after tile click: onSite=%v cur=%d visited=%v, want true/0/true", s.onSite, s.cur, s.visited)
	}
	// On a site page, tile hit-testing is skipped -> a tile-area click is a no-op.
	if s.HandleMouse(r.X+r.W/2, r.Y+r.H/2) {
		t.Error("tile click on a site page should be a no-op")
	}
}

func TestBackForward(t *testing.T) {
	s := newState()
	// Back / Forward do nothing before any visit.
	if s.HandleMouse(s.backRect.X+1, s.backRect.Y+1) {
		t.Error("Back on the start page (never visited) should be a no-op")
	}
	if s.HandleMouse(s.fwdRect.X+1, s.fwdRect.Y+1) {
		t.Error("Forward before any visit should be a no-op")
	}
	// Visit, then Back to start.
	s.navigate(3)
	if !s.HandleMouse(s.backRect.X+1, s.backRect.Y+1) || s.onSite {
		t.Error("Back from a site page should return to the start page")
	}
	// Forward re-opens the last site.
	if !s.HandleMouse(s.fwdRect.X+1, s.fwdRect.Y+1) || !s.onSite || s.cur != 3 {
		t.Errorf("Forward should re-open favs[3]; onSite=%v cur=%d", s.onSite, s.cur)
	}
	// Forward again (already on the site) is a no-op.
	if s.HandleMouse(s.fwdRect.X+1, s.fwdRect.Y+1) {
		t.Error("Forward while already on a site should be a no-op")
	}
}

func TestHandleMouseMiss(t *testing.T) {
	s := newState()
	// A click in dead space (below the grid, not a button/tile) does nothing.
	if s.HandleMouse(s.W-5, s.H-5) {
		t.Error("click in empty content should be a no-op")
	}
}

func TestHandleKey(t *testing.T) {
	s := newState()
	s.navigate(1)
	if !s.HandleKey("Backspace") || s.onSite {
		t.Error("Backspace should act as Back")
	}
	s.navigate(1)
	if !s.HandleKey("Escape") || s.onSite {
		t.Error("Escape should act as Back")
	}
	if s.HandleKey("KeyA") {
		t.Error("unhandled key should return false")
	}
}
