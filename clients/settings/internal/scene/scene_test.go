// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"testing"

	"github.com/go-widgets/toolkit"
)

func newState() *State { return New(640, 460) }

// clickCat clicks the sidebar band for category idx via the full routing path
// (HandleMouse -> root.OnEvent -> Border -> sidebar VBox -> catRow).
func clickCat(s *State, idx int) bool {
	y := catTop + idx*catRowH + catRowH/2
	return s.HandleMouse(sidePad, y)
}

// wantCard recomputes a category's card rect with the ORIGINAL arithmetic.
func wantCard(s *State, numRows int) toolkit.Rect {
	x := sidebarW + cardMarginX
	return toolkit.Rect{X: x, Y: cardTop, W: s.W - x - cardMarginX, H: numRows * rowH}
}

func TestNewHasCategories(t *testing.T) {
	s := newState()
	if len(s.cats) != 5 {
		t.Fatalf("categories = %d, want 5", len(s.cats))
	}
	if s.selected != 0 {
		t.Errorf("initial selected = %d, want 0", s.selected)
	}
	// Every row got exactly one control widget.
	for ci := range s.cats {
		for ri := range s.cats[ci].rows {
			row := s.cats[ci].rows[ri]
			switch row.kind {
			case rowSwitch:
				if row.sw == nil || row.sc != nil {
					t.Errorf("cat %d row %d: switch row must have sw only", ci, ri)
				}
			case rowScale:
				if row.sc == nil || row.sw != nil {
					t.Errorf("cat %d row %d: scale row must have sc only", ci, ri)
				}
			}
		}
	}
	// The container tree was built with one sidebar row + one page per category.
	if len(s.catRows) != 5 || len(s.pages) != 5 {
		t.Fatalf("tree size: catRows=%d pages=%d, want 5/5", len(s.catRows), len(s.pages))
	}
}

func TestRenderDoesNotPanic(t *testing.T) {
	s := newState()
	buf := make([]byte, 4*s.W*s.H)
	Render(s, buf) // selected = Appearance
	clickCat(s, 2) // Sound (has scales)
	Render(s, buf)
}

func TestRenderFallbackOnAccentInk(t *testing.T) {
	// A theme without accent_fg_color drives the white-ink fallback for the
	// selected sidebar pill's label (catRow.Draw).
	s := newState()
	delete(s.theme.Extra, "accent_fg_color")
	Render(s, make([]byte, 4*s.W*s.H))
}

func TestSidebarSelect(t *testing.T) {
	s := newState()
	// Click the "Displays" band (index 3).
	if !clickCat(s, 3) {
		t.Fatal("clicking a new sidebar row should request a redraw")
	}
	if s.selected != 3 {
		t.Errorf("selected = %d, want 3", s.selected)
	}
	if s.cards.Active != 3 {
		t.Errorf("card stack Active = %d, want 3", s.cards.Active)
	}
	// Clicking the already-selected band is a no-op (selectCat early-returns).
	if clickCat(s, 3) {
		t.Error("clicking the selected row should not request a redraw")
	}
	// A click in the sidebar below every row (the flex filler) does nothing.
	if s.HandleMouse(sidePad, catTop+len(s.cats)*catRowH+5) {
		t.Error("click below the last sidebar row should be a no-op")
	}
	// A click in the sidebar title spacer (above the first row) does nothing.
	if s.HandleMouse(sidePad, catTop-5) {
		t.Error("click in the sidebar title area should be a no-op")
	}
}

func TestSwitchToggleViaWidgetBounds(t *testing.T) {
	s := newState() // Appearance selected; row 0 = "Dark Mode" (off)
	sw := s.cats[0].rows[0].sw
	if sw.On {
		t.Fatal("precondition: Dark Mode should start off")
	}
	b := sw.Bounds()
	if !s.HandleMouse(b.X+b.W/2, b.Y+b.H/2) {
		t.Fatal("clicking the switch should request a redraw")
	}
	if !sw.On {
		t.Error("Dark Mode switch did not turn on")
	}
}

func TestSwitchToggleAnywhereOnRow(t *testing.T) {
	s := newState()
	sw := s.cats[0].rows[1].sw // "Reduce Transparency"
	was := sw.On
	// Click in the row's text region (left of the switch), still toggles.
	ry := cardTop + 1*rowH
	if !s.HandleMouse(sidebarW+cardMarginX+40, ry+rowH/2) {
		t.Fatal("clicking a switch row should request a redraw")
	}
	if sw.On == was {
		t.Error("clicking the row body did not toggle the switch")
	}
}

func TestScaleClickSetsValue(t *testing.T) {
	s := newState()
	clickCat(s, 2) // Sound: row 0 = "Output Volume" (scale)
	sc := s.cats[2].rows[0].sc
	b := sc.Bounds()
	// Click near the right edge -> value near the max (100).
	if !s.HandleMouse(b.X+b.W-2, b.Y+b.H/2) {
		t.Fatal("clicking the scale should request a redraw")
	}
	if sc.Value < 90 {
		t.Errorf("scale value after right-edge click = %.1f, want >= 90", sc.Value)
	}
}

func TestContentClickMissIsNoOp(t *testing.T) {
	s := newState()
	clickCat(s, 2) // Sound; row 0/1 are scales (no whole-row toggle)
	// Click in the card's row body but not on any slider -> no redraw.
	if s.HandleMouse(sidebarW+cardMarginX+40, cardTop+rowH/2) {
		t.Error("clicking empty content (scale row body) should be a no-op")
	}
}

func TestHandleKeyArrows(t *testing.T) {
	s := newState()
	if !s.HandleKey("ArrowDown") || s.selected != 1 {
		t.Fatalf("ArrowDown: selected = %d, want 1", s.selected)
	}
	if s.cards.Active != 1 {
		t.Errorf("ArrowDown: card Active = %d, want 1", s.cards.Active)
	}
	if !s.HandleKey("ArrowUp") || s.selected != 0 {
		t.Fatalf("ArrowUp: selected = %d, want 0", s.selected)
	}
	// At the top, ArrowUp is a no-op.
	if s.HandleKey("ArrowUp") {
		t.Error("ArrowUp at top should return false")
	}
	// At the bottom, ArrowDown is a no-op.
	s.selectCat(len(s.cats) - 1)
	if s.HandleKey("ArrowDown") {
		t.Error("ArrowDown at bottom should return false")
	}
	// Unknown key.
	if s.HandleKey("KeyX") {
		t.Error("unknown key should return false")
	}
}

// TestGoldenLayoutRects is the behaviour-preserving proof: the Sencha container
// tree lays every widget out to the EXACT same toolkit.Rect the old hand-placed
// code produced. Sidebar catRows land at catTop+i*catRowH (full sidebar width);
// each page's card matches the old cardRect; and each row's control is right-
// aligned (rowPadX inset) and vertically centred inside its rowH band.
func TestGoldenLayoutRects(t *testing.T) {
	s := newState()

	// Sidebar bands.
	for i, cr := range s.catRows {
		want := toolkit.Rect{X: 0, Y: catTop + i*catRowH, W: sidebarW, H: catRowH}
		if got := cr.Bounds(); got != want {
			t.Fatalf("catRow %d (%q) bounds = %+v, want %+v", i, cr.name, got, want)
		}
	}

	// Content controls, per category. Activate each page so the card layout
	// arranges it, then compare against the original arithmetic.
	for ci := range s.cats {
		s.selectCat(ci)
		n := len(s.cats[ci].rows)
		card := wantCard(s, n)
		if got := s.pages[ci].card; got != card {
			t.Fatalf("cat %d card = %+v, want %+v", ci, got, card)
		}
		for ri := range s.cats[ci].rows {
			row := s.cats[ci].rows[ri]
			ry := card.Y + ri*rowH
			switch row.kind {
			case rowSwitch:
				want := toolkit.Rect{X: card.X + card.W - rowPadX - switchW, Y: ry + (rowH-switchH)/2, W: switchW, H: switchH}
				if got := row.sw.Bounds(); got != want {
					t.Fatalf("cat %d row %d switch bounds = %+v, want %+v", ci, ri, got, want)
				}
			case rowScale:
				want := toolkit.Rect{X: card.X + card.W - rowPadX - scaleW, Y: ry + (rowH-scaleH)/2, W: scaleW, H: scaleH}
				if got := row.sc.Bounds(); got != want {
					t.Fatalf("cat %d row %d scale bounds = %+v, want %+v", ci, ri, got, want)
				}
			}
		}
	}
}

// TestClickRoutingRightEdgeSwitch guards the routing geometry: a click at the
// far-right edge of a switch (its outer x, inside the card's rowPadX inset) still
// routes through Border -> content card -> page -> rows -> settingRowW and toggles.
func TestClickRoutingRightEdgeSwitch(t *testing.T) {
	s := newState() // Appearance; row 0 "Dark Mode" is a switch
	sw := s.cats[0].rows[0].sw
	b := sw.Bounds()
	if !s.HandleMouse(b.X+b.W-1, b.Y+1) {
		t.Fatal("click at the switch's far corner must route + toggle")
	}
	if !sw.On {
		t.Error("far-corner click did not toggle the switch")
	}
}

// TestClickAboveCardMisses proves a content click above the card (in the page
// title band) routes to no control (HandleMouse returns false).
func TestClickAboveCardMisses(t *testing.T) {
	s := newState()
	if s.HandleMouse(sidebarW+cardMarginX+10, titleTop) {
		t.Error("click in the page-title band must not route to a control")
	}
}

// TestSettingRowNonClickIgnored covers the non-click early-return in
// settingRowW.OnEvent: any event kind other than a click leaves the row inert.
func TestSettingRowNonClickIgnored(t *testing.T) {
	sw := toolkit.NewSwitch(false)
	fired := false
	row := &settingRowW{title: "X", kind: rowSwitch, sw: sw, notify: func() { fired = true }}
	row.OnEvent(toolkit.Event{Kind: toolkit.EventKeyDown, Code: "Enter"})
	if sw.On || fired {
		t.Errorf("non-click event mutated the row: On=%v notified=%v", sw.On, fired)
	}
}

func TestHandleMouseOffCanvas(t *testing.T) {
	s := newState()
	if s.HandleMouse(-10, -10) {
		t.Fatal("off-canvas click must return false")
	}
}
