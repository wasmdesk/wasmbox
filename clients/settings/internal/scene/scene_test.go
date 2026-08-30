// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"testing"

	icons "github.com/go-icons/iconoir"
	"github.com/go-widgets/toolkit"
)

func newState() *State { return New(640, 460) }

// render paints s into a throwaway buffer, which is what positions the content
// controls (a SettingsGroup lays its rows out during Draw). Real clients render
// every frame before handling input, so tests do the same before asserting on
// or clicking widget bounds.
func render(s *State) { Render(s, make([]byte, 4*s.W*s.H)) }

// itemVisualRow maps a flat category index to its VISUAL row in the sectioned
// sidebar list (item rows plus the section-header rows above it), mirroring the
// sidebarGroups partition the ListBox renders.
func itemVisualRow(item int) int {
	vr, idx := 0, 0
	for _, g := range sidebarGroups {
		if g.title != "" {
			vr++ // section header row
		}
		for k := 0; k < g.n; k++ {
			if idx == item {
				return vr
			}
			vr++
			idx++
		}
	}
	return -1
}

// listTop is the surface Y of the first sidebar list row: the sidebar VBox docks
// the ListBox under the fixed catTop title band.
const listTop = catTop

// clickCat clicks the sidebar row for category idx via the full routing path
// (HandleMouse -> root -> sidebar VBox -> ListBox).
func clickCat(s *State, idx int) bool {
	y := listTop + itemVisualRow(idx)*catRowH + catRowH/2
	return s.HandleMouse(sidePad, y)
}

func TestNewHasCategories(t *testing.T) {
	s := newState()
	if len(s.cats) != 5 {
		t.Fatalf("categories = %d, want 5", len(s.cats))
	}
	if s.selected != 0 {
		t.Errorf("initial selected = %d, want 0", s.selected)
	}
	if got := s.list.Selected().Get(); got != 0 {
		t.Errorf("sidebar list Selected = %d, want 0", got)
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
	// The container tree was built with one page per category.
	if len(s.pages) != 5 {
		t.Fatalf("tree size: pages=%d, want 5", len(s.pages))
	}
}

// TestSectionsCoverCategories proves the sidebar sections partition the
// categories in model order, so the flat item index the ListBox reports (and
// hands to OnActivate) equals the category index.
func TestSectionsCoverCategories(t *testing.T) {
	s := newState()
	var flat []string
	for _, sec := range s.list.Sections {
		flat = append(flat, sec.Items...)
	}
	if len(flat) != len(s.cats) {
		t.Fatalf("sectioned items = %d, want %d", len(flat), len(s.cats))
	}
	for i, name := range flat {
		if name != s.cats[i].name {
			t.Errorf("flat item %d = %q, want %q (index must equal category index)", i, name, s.cats[i].name)
		}
	}
}

// TestCategoryIconsExist proves every sidebar icon stem is a real iconoir icon
// (a control-run against the library's own name registry) and that there is one
// per category.
func TestCategoryIconsExist(t *testing.T) {
	s := newState()
	if len(catIcons) != len(s.cats) {
		t.Fatalf("catIcons = %d, want %d (one per category)", len(catIcons), len(s.cats))
	}
	valid := map[string]bool{}
	for _, n := range icons.Names() {
		valid[n] = true
	}
	for i, stem := range catIcons {
		if !valid[stem] {
			t.Errorf("catIcons[%d] = %q is not an iconoir stem", i, stem)
		}
	}
}

func TestRenderDoesNotPanic(t *testing.T) {
	s := newState()
	render(s)      // selected = Appearance
	clickCat(s, 2) // Sound (has scales)
	render(s)
}

func TestSidebarSelectViaClick(t *testing.T) {
	s := newState()
	render(s)
	// Click the "Displays" row (category index 3).
	if !clickCat(s, 3) {
		t.Fatal("clicking a new sidebar row should request a redraw")
	}
	if s.selected != 3 {
		t.Errorf("selected = %d, want 3", s.selected)
	}
	if s.cards.Active != 3 {
		t.Errorf("card stack Active = %d, want 3", s.cards.Active)
	}
	if got := s.list.Selected().Get(); got != 3 {
		t.Errorf("sidebar list Selected = %d, want 3", got)
	}
	// Clicking the already-selected row is a no-op (selectCat early-returns).
	if clickCat(s, 3) {
		t.Error("clicking the selected row should not request a redraw")
	}
	// A click on a section-header row selects nothing.
	if s.HandleMouse(sidePad, listTop+catRowH/2) { // visual row 0 = "System" header
		t.Error("clicking a section header should be a no-op")
	}
	// A click below every row (the empty sidebar area) does nothing.
	if s.HandleMouse(sidePad, listTop+20*catRowH) {
		t.Error("click below the last sidebar row should be a no-op")
	}
	// A click in the sidebar title spacer (above the list) does nothing.
	if s.HandleMouse(sidePad, catTop-5) {
		t.Error("click in the sidebar title area should be a no-op")
	}
}

func TestSwitchToggleViaWidgetBounds(t *testing.T) {
	s := newState() // Appearance selected; row 0 = "Dark Mode" (off)
	render(s)       // positions the SettingRow controls
	sw := s.cats[0].rows[0].sw
	if sw.On().Get() {
		t.Fatal("precondition: Dark Mode should start off")
	}
	b := sw.Bounds()
	if !s.HandleMouse(b.X+b.W/2, b.Y+b.H/2) {
		t.Fatal("clicking the switch should request a redraw")
	}
	if !sw.On().Get() {
		t.Error("Dark Mode switch did not turn on")
	}
}

func TestScaleClickSetsValue(t *testing.T) {
	s := newState()
	clickCat(s, 2) // Sound: row 0 = "Output Volume" (scale)
	render(s)      // positions the scale
	sc := s.cats[2].rows[0].sc
	b := sc.Bounds()
	// Click near the right edge -> value near the max (100).
	if !s.HandleMouse(b.X+b.W-2, b.Y+b.H/2) {
		t.Fatal("clicking the scale should request a redraw")
	}
	if sc.Value().Get() < 90 {
		t.Errorf("scale value after right-edge click = %.1f, want >= 90", sc.Value().Get())
	}
}

func TestContentClickMissIsNoOp(t *testing.T) {
	s := newState()
	clickCat(s, 2) // Sound; row 0/1 are scales (no whole-row toggle)
	render(s)
	// Click in the card's row body but not on the slider control -> no redraw.
	if s.HandleMouse(sidebarW+cardMarginX+30, cardTop+15) {
		t.Error("clicking the label column of a row should be a no-op")
	}
}

func TestClickAboveCardMisses(t *testing.T) {
	s := newState()
	render(s)
	// A click in the page-title band (above the card) routes to no control.
	if s.HandleMouse(sidebarW+cardMarginX+10, 10) {
		t.Error("click in the page-title band must not route to a control")
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
	if got := s.list.Selected().Get(); got != 1 {
		t.Errorf("ArrowDown: sidebar list Selected = %d, want 1", got)
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

func TestHandleMouseOffCanvas(t *testing.T) {
	s := newState()
	if s.HandleMouse(-10, -10) {
		t.Fatal("off-canvas click must return false")
	}
}

// TestPageComposesTitleAndCard proves each page composes a title Label above a
// SettingsGroup card, exposes both through Children, and stacks the card below
// the title band.
func TestPageComposesTitleAndCard(t *testing.T) {
	s := newState()
	render(s)
	pg := s.pages[0]
	kids := pg.Children()
	if len(kids) != 2 {
		t.Fatalf("page Children = %d, want 2 (title + group)", len(kids))
	}
	if kids[0] != pg.title || kids[1] != pg.group {
		t.Error("page Children must be [title, group]")
	}
	if pg.title.Text().Get() != s.cats[0].name {
		t.Errorf("page title = %q, want %q", pg.title.Text().Get(), s.cats[0].name)
	}
	tb, gb := pg.title.Bounds(), pg.group.Bounds()
	if gb.Y < tb.Y+tb.H {
		t.Errorf("card top %d overlaps the title band (ends at %d)", gb.Y, tb.Y+tb.H)
	}
	if gb.H <= 0 {
		t.Errorf("card height = %d, want > 0", gb.H)
	}
}

// --- anti-aliased text proof ----------------------------------------------

// distinctLuma counts how many distinct R+G+B sums appear inside region r of an
// RGBA buffer. The 5x7 bitmap font paints each text pixel either full ink or
// untouched ground; the AA/shaped face scan-converts glyph outlines to
// partial-coverage masks, adding a ramp of intermediate levels. A strictly
// higher distinct-luma count over the SAME region proves the AA face is active.
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

func TestAATextIsAntiAliased(t *testing.T) {
	s := newState()
	region := toolkit.Rect{X: sidePad, Y: titleTop - 4, W: 130, H: toolkit.GlyphHeight() + 8}

	aa := make([]byte, 4*s.W*s.H)
	Render(s, aa) // AA face (enableAAText ran in New)

	toolkit.SetFont(nil) // bitmap default
	defer func() { _ = toolkit.UseOpenTypeText() }()
	bm := make([]byte, 4*s.W*s.H)
	Render(newState(), bm)

	aaN := distinctLuma(aa, s.W, region)
	bmN := distinctLuma(bm, s.W, region)
	if aaN <= bmN {
		t.Fatalf("Settings title: distinct luma aa=%d not > bitmap=%d — AA face not active", aaN, bmN)
	}
	t.Logf("title distinct luma: aa=%d bm=%d", aaN, bmN)
}

// TestAAFaceFits asserts the taller AA line box + shaped label widths fit the
// Settings bands: the 20px face sits inside the sidebar row and every category
// name fits the sidebar list's text column (right of the iconoir badge).
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
	if h > catRowH {
		t.Fatalf("AA height %d overflows sidebar row height %d", h, catRowH)
	}
	// Category names fit the sidebar list's text column (inset by the icon badge
	// on the left and catIconPad on the right).
	maxW := sidebarW - (catIconPad + catIconSize + catTextGap) - catIconPad
	for _, c := range s.cats {
		if w := toolkit.TextWidth(c.name); w > maxW {
			t.Fatalf("category %q width %d overflows sidebar column %d", c.name, w, maxW)
		}
	}
}
