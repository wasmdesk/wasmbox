// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"testing"
)

// DefaultMagnify is on with a >1 peak + positive radius.
func TestDefaultMagnify(t *testing.T) {
	m := DefaultMagnify()
	if !m.On || m.MaxScale <= 1 || m.Radius <= 0 {
		t.Fatalf("DefaultMagnify = %+v, want on/peak>1/radius>0", m)
	}
	if New(tW, tH).Magnify != m {
		t.Fatalf("New did not seed DefaultMagnify")
	}
}

// SetMagnify swaps the config.
func TestSetMagnify(t *testing.T) {
	s := New(tW, tH)
	s.SetMagnify(Magnify{On: false})
	if s.Magnify.On {
		t.Fatalf("SetMagnify did not store")
	}
}

// With magnification inactive (cursor outside the surface) the AppDock lays out
// flat, so every launcher rect keeps its resting width IconbarButtonW.
func TestRectsInactiveAreResting(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "a"}, {Id: 2, Title: "b"}})
	s.SetCursor(0, 0, false)
	lr := s.LauncherRects()
	if len(lr) != len(s.Apps) {
		t.Fatalf("LauncherRects len %d, want %d", len(lr), len(s.Apps))
	}
	for i, r := range lr {
		if r[2] != IconbarButtonW {
			t.Fatalf("resting launcher[%d] width = %d, want %d", i, r[2], IconbarButtonW)
		}
	}
	if got := len(s.WindowRects()); got != 2 {
		t.Fatalf("WindowRects len %d, want 2", got)
	}
}

// Hovering a launcher swells its rect: the hovered launcher's laid-out width
// exceeds the resting IconbarButtonW, and the row stays left-to-right without
// overlap.
func TestLauncherRectsMagnify(t *testing.T) {
	s := New(tW, tH)
	// Resting rect of launcher 1, to place the cursor over its centre.
	rest := s.LauncherRects()
	r1 := rest[1]
	cursorX := r1[0] + r1[2]/2
	s.SetCursor(cursorX, tH/2, true)

	got := s.LauncherRects()
	if got[1][2] <= IconbarButtonW {
		t.Fatalf("hovered launcher width %d not swollen from %d", got[1][2], IconbarButtonW)
	}
	// No overlap + left-to-right order across the launcher row.
	for i := 1; i < len(got); i++ {
		if got[i][0] < got[i-1][0]+got[i-1][2] {
			t.Fatalf("launcher[%d] overlaps launcher[%d]", i, i-1)
		}
	}
}

// Magnification off (Magnify.On=false) keeps a flat layout even with the cursor
// over the iconbar.
func TestMagnifyOffStaysFlat(t *testing.T) {
	s := New(tW, tH)
	s.SetMagnify(Magnify{On: false})
	rest := s.LauncherRects()
	s.SetCursor(rest[1][0]+rest[1][2]/2, tH/2, true)
	got := s.LauncherRects()
	for i := range got {
		if got[i][2] != IconbarButtonW {
			t.Fatalf("launcher[%d] width %d with magnify off, want flat %d", i, got[i][2], IconbarButtonW)
		}
	}
}

// A cursor over the fixed workspace/clock ends (outside the iconbar's x-range)
// does not magnify the iconbar.
func TestCursorOutsideIconbarNoMagnify(t *testing.T) {
	s := New(tW, tH)
	// Cursor deep in the workspace section (x < WorkspaceW).
	s.SetCursor(WorkspaceW/2, tH/2, true)
	got := s.LauncherRects()
	for i := range got {
		if got[i][2] != IconbarButtonW {
			t.Fatalf("launcher[%d] magnified from a workspace-section hover", i)
		}
	}
}

// A window task button's width tracks its title: a long title yields a wider
// button than a short one.
func TestWindowButtonWidthTracksTitle(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{
		{Id: 1, Title: "x"},
		{Id: 2, Title: "a-much-longer-window-title"},
	})
	wr := s.WindowRects()
	if len(wr) != 2 {
		t.Fatalf("WindowRects len %d, want 2", len(wr))
	}
	if wr[1][2] <= wr[0][2] {
		t.Fatalf("longer title width %d not > shorter %d", wr[1][2], wr[0][2])
	}
}

// A minimized window's "[*] " prefix widens its button versus the same title
// un-minimized.
func TestMinimizedPrefixWidensButton(t *testing.T) {
	open := New(tW, tH)
	open.SetWindows([]Window{{Id: 1, Title: "editor"}})
	min := New(tW, tH)
	min.SetWindows([]Window{{Id: 1, Title: "editor", Minimized: true}})
	if min.WindowRects()[0][2] <= open.WindowRects()[0][2] {
		t.Fatalf("minimized width %d not wider than open %d (missing [*] prefix width)",
			min.WindowRects()[0][2], open.WindowRects()[0][2])
	}
}

// On a narrow iconbar the window task buttons shrink so their right edges stay
// inside the iconbar (shrink-to-fit keeps a just-minimized window clickable).
func TestWindowButtonsShrinkToFit(t *testing.T) {
	// Narrow iconbar (width = 300 - 100 - 80 = 120) and NO launchers, so the
	// window row alone must be shrunk to fit inside the iconbar's right edge.
	s := New(300, BarHeight)
	s.Apps = nil
	s.SetWindows([]Window{
		{Id: 1, Title: "one"}, {Id: 2, Title: "two"}, {Id: 3, Title: "three"},
	})
	_, _, iw, _ := s.IconbarRect()
	right := WorkspaceW + iw
	for i, r := range s.WindowRects() {
		if r[0]+r[2] > right {
			t.Fatalf("window[%d] right edge %d exceeds iconbar right %d (shrink failed)", i, r[0]+r[2], right)
		}
	}
}

// On an extremely narrow iconbar with many windows the uniform shrink cap would
// go non-positive; it is floored at 1 so every button keeps a positive width and
// the render never divides by or paints a zero-width slot.
func TestWindowButtonsShrinkCapFloor(t *testing.T) {
	s := New(190, BarHeight) // iconbar width = 190 - 100 - 80 = 10
	s.Apps = nil
	s.SetWindows([]Window{
		{Id: 1, Title: "a"}, {Id: 2, Title: "b"}, {Id: 3, Title: "c"},
		{Id: 4, Title: "d"}, {Id: 5, Title: "e"},
	})
	for i, r := range s.WindowRects() {
		if r[2] < 1 {
			t.Fatalf("window[%d] width = %d, want >= 1 (floor)", i, r[2])
		}
	}
	buf := newBuf(s)
	Render(s, buf) // must not panic
}
