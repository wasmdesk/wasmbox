// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"testing"

	"github.com/go-widgets/toolkit"
)

// BuildLauncherMenu: out-of-range yields an empty menu; a not-running launcher
// yields just "New Window"; a running launcher adds a separator + Show + Close
// bound to the app's first window id.
func TestBuildLauncherMenu(t *testing.T) {
	s := New(tW, tH)
	if m := s.BuildLauncherMenu(-1); len(m.Entries) != 0 {
		t.Fatalf("out-of-range (-1) menu should be empty, got %d", len(m.Entries))
	}
	if m := s.BuildLauncherMenu(99); len(m.Entries) != 0 {
		t.Fatalf("out-of-range (99) menu should be empty")
	}
	// Not running: single New Window entry -> ActLaunch of that app.
	m := s.BuildLauncherMenu(0) // terminal
	if len(m.Entries) != 1 || m.Entries[0].Action != ActLaunch || m.Entries[0].App != "terminal" {
		t.Fatalf("not-running launcher menu = %+v", m.Entries)
	}
	if m.W < MenuMinW || m.H <= 0 {
		t.Fatalf("menu size = %dx%d, want >=MenuMinW x >0", m.W, m.H)
	}
	// Running: New Window, separator, Show(focus), Close(close) -> win id.
	s.SetWindows([]Window{{Id: 42, Title: "Terminal"}})
	m = s.BuildLauncherMenu(0)
	if len(m.Entries) != 4 {
		t.Fatalf("running launcher menu entries = %d, want 4", len(m.Entries))
	}
	if !m.Entries[1].Separator {
		t.Fatalf("entry 1 should be a separator")
	}
	if m.Entries[2].Action != ActFocus || m.Entries[2].Win != 42 {
		t.Fatalf("Show entry = %+v, want ActFocus win 42", m.Entries[2])
	}
	if m.Entries[3].Action != ActClose || m.Entries[3].Win != 42 {
		t.Fatalf("Close entry = %+v, want ActClose win 42", m.Entries[3])
	}
}

// BuildWindowMenu: out-of-range empty; normal yields Show + Close on that id.
func TestBuildWindowMenu(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 7, Title: "x"}})
	if m := s.BuildWindowMenu(-1); len(m.Entries) != 0 {
		t.Fatalf("out-of-range window menu should be empty")
	}
	if m := s.BuildWindowMenu(5); len(m.Entries) != 0 {
		t.Fatalf("out-of-range window menu should be empty")
	}
	m := s.BuildWindowMenu(0)
	if len(m.Entries) != 2 {
		t.Fatalf("window menu entries = %d, want 2", len(m.Entries))
	}
	if m.Entries[0].Action != ActFocus || m.Entries[0].Win != 7 {
		t.Fatalf("Show = %+v", m.Entries[0])
	}
	if m.Entries[1].Action != ActClose || m.Entries[1].Win != 7 {
		t.Fatalf("Close = %+v", m.Entries[1])
	}
}

// finishMenu widens the popup to the widest label past the floor.
func TestFinishMenuWidensForLongLabel(t *testing.T) {
	long := "A really really long menu entry label"
	m := finishMenu([]MenuEntry{{Label: long, Action: ActLaunch}})
	if m.W <= MenuMinW {
		t.Fatalf("long label did not widen menu past floor: W=%d", m.W)
	}
	if want := toolkit.TextWidth(long) + menuPad; m.W != want {
		t.Fatalf("menu width = %d, want %d", m.W, want)
	}
}

// MenuRender paints the menu body (non-blank) and panics on a size mismatch.
func TestMenuRender(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "Terminal"}})
	m := s.BuildLauncherMenu(0) // has a separator + rows
	buf := make([]byte, 4*m.W*m.H)
	m.MenuRender(buf, m.W, m.H, 2) // highlight row 2 (Show)
	if countNonZero(buf) == 0 {
		t.Fatalf("menu render painted nothing")
	}
	// Highlight -1 (no hover) also renders.
	buf2 := make([]byte, 4*m.W*m.H)
	m.MenuRender(buf2, m.W, m.H, -1)
	if countNonZero(buf2) == 0 {
		t.Fatalf("menu render (no hover) painted nothing")
	}
	// Size mismatch panics.
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic on menu buffer size mismatch")
			}
		}()
		m.MenuRender(make([]byte, 4), m.W, m.H, -1)
	}()
}

// MenuHitTest / MenuHover map a popup-relative y to the entry index, skipping
// separators and disabled rows, and return -1 outside any row.
func TestMenuHitTest(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 9, Title: "Terminal"}})
	m := s.BuildLauncherMenu(0) // [New Window, ---, Show, Close]

	// Row 0 (New Window) centre: 2 + MenuRowH/2.
	if got := m.MenuHitTest(2 + toolkit.MenuRowH/2); got != 0 {
		t.Fatalf("hit row 0 = %d, want 0", got)
	}
	// The separator band (after row 0) returns -1.
	sepY := 2 + toolkit.MenuRowH + toolkit.MenuSeparatorH/2
	if got := m.MenuHitTest(sepY); got != -1 {
		t.Fatalf("hit separator = %d, want -1", got)
	}
	// Row 2 (Show) sits after row0 + separator.
	showY := 2 + toolkit.MenuRowH + toolkit.MenuSeparatorH + toolkit.MenuRowH/2
	if got := m.MenuHitTest(showY); got != 2 {
		t.Fatalf("hit Show row = %d, want 2", got)
	}
	if got := m.MenuHover(showY); got != 2 {
		t.Fatalf("MenuHover Show row = %d, want 2", got)
	}
	// Below the last row -> -1.
	if got := m.MenuHitTest(m.H + 100); got != -1 {
		t.Fatalf("hit below menu = %d, want -1", got)
	}
	// A disabled (ActNone) row returns -1 even though it occupies a row band.
	dm := DockMenu{Entries: []MenuEntry{{Label: "disabled", Action: ActNone}}}
	if got := dm.MenuHitTest(2 + toolkit.MenuRowH/2); got != -1 {
		t.Fatalf("hit disabled row = %d, want -1", got)
	}
}

// RowCenters returns each entry's y-centre (separators -1), and MenuHitTest at
// a reported centre resolves back to that same entry.
func TestRowCenters(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 9, Title: "Terminal"}})
	m := s.BuildLauncherMenu(0) // [New Window, ---, Show, Close]
	c := m.RowCenters()
	if len(c) != len(m.Entries) {
		t.Fatalf("RowCenters len %d, want %d", len(c), len(m.Entries))
	}
	if c[1] != -1 {
		t.Fatalf("separator centre = %d, want -1", c[1])
	}
	for i, e := range m.Entries {
		if e.Separator {
			continue
		}
		if got := m.MenuHitTest(c[i]); got != i {
			t.Fatalf("MenuHitTest(center of entry %d)=%d, want %d", i, got, i)
		}
	}
}
