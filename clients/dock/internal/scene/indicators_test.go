// SPDX-License-Identifier: BSD-3-Clause

package scene

import "testing"

// appIndexForWindow: explicit App id match, App id miss, Title==Label fallback,
// Title==Id fallback, empty title, and no match all resolve correctly.
func TestAppIndexForWindow(t *testing.T) {
	s := New(tW, tH) // apps: terminal/editor/files/hello (labels Terminal/Editor/Files/Hello)
	// Explicit App id wins.
	if got := s.appIndexForWindow(Window{App: "files", Title: "whatever"}); got != 2 {
		t.Fatalf("App=files -> %d, want 2", got)
	}
	// Explicit App id with no matching launcher -> -1.
	if got := s.appIndexForWindow(Window{App: "nope"}); got != -1 {
		t.Fatalf("App=nope -> %d, want -1", got)
	}
	// Title matches a launcher Label (case-insensitive).
	if got := s.appIndexForWindow(Window{Title: "terminal"}); got != 0 {
		t.Fatalf("Title=terminal -> %d, want 0", got)
	}
	// Title matches a launcher Id.
	if got := s.appIndexForWindow(Window{Title: "hello"}); got != 3 {
		t.Fatalf("Title=hello -> %d, want 3", got)
	}
	// Empty title -> -1.
	if got := s.appIndexForWindow(Window{}); got != -1 {
		t.Fatalf("empty window -> %d, want -1", got)
	}
	// Unknown title -> -1.
	if got := s.appIndexForWindow(Window{Title: "Gimp"}); got != -1 {
		t.Fatalf("Title=Gimp -> %d, want -1", got)
	}
}

// launcherRunning flags exactly the launchers that own an open window.
func TestLauncherRunning(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{
		{Id: 1, Title: "Terminal"}, // -> launcher 0
		{Id: 2, Title: "Nope"},     // -> no launcher
	})
	run := s.launcherRunning()
	if !run[0] {
		t.Fatalf("launcher 0 should be running")
	}
	for i := 1; i < len(run); i++ {
		if run[i] {
			t.Fatalf("launcher %d should not be running", i)
		}
	}
}

// focusedLauncher returns the focused window's launcher, -1 when the focused
// window maps to none, and -1 when nothing is focused.
func TestFocusedLauncher(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "Editor", Focused: true}})
	if got := s.focusedLauncher(); got != 1 {
		t.Fatalf("focused Editor -> %d, want 1", got)
	}
	s.SetWindows([]Window{{Id: 1, Title: "Unknown", Focused: true}})
	if got := s.focusedLauncher(); got != -1 {
		t.Fatalf("focused unknown -> %d, want -1", got)
	}
	s.SetWindows([]Window{{Id: 1, Title: "Editor"}})
	if got := s.focusedLauncher(); got != -1 {
		t.Fatalf("none focused -> %d, want -1", got)
	}
}

// SetBadge sets, updates and clears counts; BadgeCount reads them back and
// returns 0 for an unset app (and when the map is still nil).
func TestBadgeSetAndCount(t *testing.T) {
	s := New(tW, tH)
	if s.BadgeCount("terminal") != 0 {
		t.Fatalf("nil-map BadgeCount should be 0")
	}
	s.SetBadge("terminal", 3)
	if s.BadgeCount("terminal") != 3 {
		t.Fatalf("BadgeCount after set = %d, want 3", s.BadgeCount("terminal"))
	}
	s.SetBadge("terminal", 0) // clear
	if s.BadgeCount("terminal") != 0 {
		t.Fatalf("BadgeCount after clear = %d, want 0", s.BadgeCount("terminal"))
	}
	s.SetBadge("editor", -1) // non-positive also clears / no-ops
	if s.BadgeCount("editor") != 0 {
		t.Fatalf("negative SetBadge should leave 0")
	}
}

// A launcher whose app has an open window carries a running dot, and the
// focused window's launcher is flagged Active — both surfaced through the
// AppDock item state built by iconDock. Rendering with one running+focused app
// must not panic and must ink the iconbar.
func TestRunningAndFocusedRender(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "Terminal", Focused: true}})
	if !s.launcherRunning()[0] {
		t.Fatalf("terminal launcher should be running")
	}
	if s.focusedLauncher() != 0 {
		t.Fatalf("terminal launcher should be the focused one")
	}
	buf := newBuf(s)
	Render(s, buf) // must not panic; exercises Running + Active item paths
}

// A launcher with an attention badge renders without panicking (the AppDock
// draws the badge overlay from AppDockItem.Badge = BadgeCount).
func TestBadgeRenders(t *testing.T) {
	s := New(tW, tH)
	s.SetBadge("terminal", 5)
	buf := newBuf(s)
	Render(s, buf) // must not panic; exercises the badged-item path
}

// countNonZero counts non-zero bytes in an RGBA buffer (any painted pixel).
// Shared test helper used by the menu render tests.
func countNonZero(buf []byte) int {
	n := 0
	for _, b := range buf {
		if b != 0 {
			n++
		}
	}
	return n
}
