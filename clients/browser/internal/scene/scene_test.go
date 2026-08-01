// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"strings"
	"testing"

	"github.com/go-widgets/toolkit"
)

const (
	surfaceW = 760
	surfaceH = 500
)

func newState() *State { return New(surfaceW, surfaceH) }

func newSurface() []byte { return make([]byte, 4*surfaceW*surfaceH) }

// --- golden-rect proof ----------------------------------------------------

// TestGoldenRects recomputes every chrome + tile rect with the ORIGINAL
// hand-placement arithmetic and asserts the container tree lays each widget
// out at exactly the same bounds — proving the Sencha migration is pixel-for-
// pixel equivalent to the old manual toolkit.Rect placement.
func TestGoldenRects(t *testing.T) {
	s := newState()
	w := surfaceW

	// Toolbar controls (original arithmetic from the pre-migration scene).
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

	// Favourite tiles.
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

// TestGoldenRectsWidth checks the flex address bar tracks the surface width so
// the toolbar equivalence holds at more than one size.
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

// --- rendering ------------------------------------------------------------

func TestNew(t *testing.T) {
	s := newState()
	if len(s.favs) != 8 {
		t.Fatalf("favs = %d, want 8", len(s.favs))
	}
	if len(s.tiles) != len(s.favs) {
		t.Fatalf("tiles = %d, want %d", len(s.tiles), len(s.favs))
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
	buf := newSurface()
	Render(s, buf) // start page (heading + tiles, both upper() branches via mixed-case favs)
	s.navigate(2)
	Render(s, buf) // site page (siteCard, onSite content/ink branches)
	s.back()
	Render(s, buf) // start page after a visit -> Forward-enabled ink branch
}

func TestRenderThemeFallbacks(t *testing.T) {
	s := newState()
	delete(s.theme.Extra, "headerbar_bg_color") // -> headerBG fallback
	delete(s.theme.Extra, "accent_fg_color")    // -> tile onAccent fallback
	Render(s, newSurface())
}

// --- routing --------------------------------------------------------------

func TestNavigateViaTileClick(t *testing.T) {
	s := newState()
	r := s.tiles[0].Bounds()
	if !s.HandleMouse(r.X+r.W/2, r.Y+r.H/2) {
		t.Fatal("clicking a favourite tile should navigate + request a redraw")
	}
	if !s.onSite || s.cur != 0 || !s.visited {
		t.Errorf("after tile click: onSite=%v cur=%d visited=%v, want true/0/true", s.onSite, s.cur, s.visited)
	}
	// On a site page the start card collapses, so its tiles are neither drawn
	// nor hit-tested -> a click in the old tile area is a no-op.
	if s.HandleMouse(r.X+r.W/2, r.Y+r.H/2) {
		t.Error("tile click on a site page should be a no-op")
	}
	// The grid collapses to an empty rect while the site card is active.
	if got := s.grid.Bounds(); got != (toolkit.Rect{}) {
		t.Errorf("grid bounds on a site page = %+v, want empty", got)
	}
}

func TestBackForward(t *testing.T) {
	s := newState()
	back := func() bool { r := s.backBtn.Bounds(); return s.HandleMouse(r.X+r.W/2, r.Y+r.H/2) }
	fwd := func() bool { r := s.fwdBtn.Bounds(); return s.HandleMouse(r.X+r.W/2, r.Y+r.H/2) }

	// Back / Forward do nothing before any visit.
	if back() {
		t.Error("Back on the start page (never visited) should be a no-op")
	}
	if fwd() {
		t.Error("Forward before any visit should be a no-op")
	}
	// Visit, then Back to start.
	s.navigate(3)
	if !back() || s.onSite {
		t.Error("Back from a site page should return to the start page")
	}
	// Forward re-opens the last site.
	if !fwd() || !s.onSite || s.cur != 3 {
		t.Errorf("Forward should re-open favs[3]; onSite=%v cur=%d", s.onSite, s.cur)
	}
	// Forward again (already on the site) is a no-op.
	if fwd() {
		t.Error("Forward while already on a site should be a no-op")
	}
}

func TestAddButtonInert(t *testing.T) {
	s := newState()
	r := s.addBtn.Bounds()
	if s.HandleMouse(r.X+r.W/2, r.Y+r.H/2) {
		t.Error("the new-tab (+) button is drawn but inert; a click should be a no-op")
	}
}

func TestHandleMouseMissesGutter(t *testing.T) {
	s := newState()
	// A click on the invisible spacer between Back and Forward hits no control.
	gutterX := btnLeft + btnW + btnGap/2 // inside the 6px spacer
	if s.HandleMouse(gutterX, toolbarH/2) {
		t.Error("click on a toolbar gutter should be a no-op")
	}
	// A click on the heading band (above the tile grid) hits no tile.
	if s.HandleMouse(gridLeft, toolbarH+headingOffset) {
		t.Error("click on the Favourites heading should be a no-op")
	}
	// A click in empty content below the grid does nothing.
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
