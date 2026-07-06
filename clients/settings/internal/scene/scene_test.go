// SPDX-License-Identifier: BSD-3-Clause

package scene

import "testing"

func newState() *State { return New(640, 460) }

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
}

func TestRenderDoesNotPanic(t *testing.T) {
	s := newState()
	buf := make([]byte, 4*s.W*s.H)
	Render(s, buf) // selected = Appearance
	s.selected = 2 // Sound (has scales)
	Render(s, buf)
}

func TestRenderFallbackOnAccentInk(t *testing.T) {
	// A theme without accent_fg_color drives Render's white-ink fallback for
	// the selected sidebar pill's label.
	s := newState()
	delete(s.theme.Extra, "accent_fg_color")
	Render(s, make([]byte, 4*s.W*s.H))
}

func TestSidebarSelect(t *testing.T) {
	s := newState()
	// Click the "Displays" row (index 3).
	ry := catTop + 3*catRowH
	if !s.HandleMouse(20, ry+4) {
		t.Fatal("clicking a new sidebar row should request a redraw")
	}
	if s.selected != 3 {
		t.Errorf("selected = %d, want 3", s.selected)
	}
	// Clicking the already-selected row is a no-op.
	if s.HandleMouse(20, ry+4) {
		t.Error("clicking the selected row should not request a redraw")
	}
	// A click in the sidebar below every row does nothing.
	if s.HandleMouse(20, catTop+len(s.cats)*catRowH+5) {
		t.Error("click below the last sidebar row should be a no-op")
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
	if !s.HandleMouse(sidebarW+40, ry+rowH/2) {
		t.Fatal("clicking a switch row should request a redraw")
	}
	if sw.On == was {
		t.Error("clicking the row body did not toggle the switch")
	}
}

func TestScaleClickSetsValue(t *testing.T) {
	s := newState()
	s.selected = 2 // Sound: row 0 = "Output Volume" (scale)
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
	s.selected = 2 // Sound; row 0/1 are scales (no whole-row toggle)
	// Click in the content pane but not on any control -> no redraw.
	if s.HandleMouse(sidebarW+40, cardTop+rowH/2) {
		t.Error("clicking empty content (scale row body) should be a no-op")
	}
}

func TestHandleKeyArrows(t *testing.T) {
	s := newState()
	if !s.HandleKey("ArrowDown") || s.selected != 1 {
		t.Fatalf("ArrowDown: selected = %d, want 1", s.selected)
	}
	if !s.HandleKey("ArrowUp") || s.selected != 0 {
		t.Fatalf("ArrowUp: selected = %d, want 0", s.selected)
	}
	// At the top, ArrowUp is a no-op.
	if s.HandleKey("ArrowUp") {
		t.Error("ArrowUp at top should return false")
	}
	// At the bottom, ArrowDown is a no-op.
	s.selected = len(s.cats) - 1
	if s.HandleKey("ArrowDown") {
		t.Error("ArrowDown at bottom should return false")
	}
	// Unknown key.
	if s.HandleKey("KeyX") {
		t.Error("unknown key should return false")
	}
}
