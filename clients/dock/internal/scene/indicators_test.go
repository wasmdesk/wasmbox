// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"testing"

	"github.com/go-widgets/toolkit"
)

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

// badgeText caps at 99+ and blanks a non-positive count.
func TestBadgeText(t *testing.T) {
	if got := badgeText(0); got != "" {
		t.Fatalf("badgeText(0) = %q, want empty", got)
	}
	if got := badgeText(-5); got != "" {
		t.Fatalf("badgeText(-5) = %q, want empty", got)
	}
	if got := badgeText(7); got != "7" {
		t.Fatalf("badgeText(7) = %q, want 7", got)
	}
	if got := badgeText(150); got != "99+" {
		t.Fatalf("badgeText(150) = %q, want 99+", got)
	}
}

// drawRunningIndicator: degenerate rect no-ops; a normal rect inks a dot; the
// focused variant additionally inks the wider underline; a very short rect
// exercises the by<r.Y clamp.
func TestDrawRunningIndicator(t *testing.T) {
	s := New(tW, tH)
	// Degenerate: nothing painted.
	buf, p := newPainter(40, BarHeight)
	s.drawRunningIndicator(p, toolkit.Rect{X: 0, Y: 0, W: 0, H: 10}, false)
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("degenerate running indicator painted something")
		}
	}
	// Normal, unfocused: some ink appears near the bottom.
	buf, p = newPainter(40, BarHeight)
	s.drawRunningIndicator(p, toolkit.Rect{X: 4, Y: 2, W: 30, H: 24}, false)
	dot := countNonZero(buf)
	if dot == 0 {
		t.Fatalf("running dot painted nothing")
	}
	// Focused: strictly more ink (dot + underline).
	buf2, p2 := newPainter(40, BarHeight)
	s.drawRunningIndicator(p2, toolkit.Rect{X: 4, Y: 2, W: 30, H: 24}, true)
	if countNonZero(buf2) <= dot {
		t.Fatalf("focused underline added no ink")
	}
	// Very short rect: by clamps to r.Y, still paints, no panic.
	buf3, p3 := newPainter(40, BarHeight)
	s.drawRunningIndicator(p3, toolkit.Rect{X: 4, Y: 2, W: 30, H: 2}, false)
	if countNonZero(buf3) == 0 {
		t.Fatalf("short-rect running dot painted nothing")
	}
}

// drawBadge: zero count no-ops; degenerate rect no-ops; a positive count inks a
// pill; a narrow rect exercises the bx<r.X clamp.
func TestDrawBadge(t *testing.T) {
	buf, p := newPainter(60, BarHeight)
	drawBadge(p, toolkit.Rect{X: 0, Y: 0, W: 40, H: 24}, 0) // no count
	if countNonZero(buf) != 0 {
		t.Fatalf("zero-count badge painted something")
	}
	buf, p = newPainter(60, BarHeight)
	drawBadge(p, toolkit.Rect{X: 0, Y: 0, W: 0, H: 24}, 3) // degenerate
	if countNonZero(buf) != 0 {
		t.Fatalf("degenerate badge painted something")
	}
	buf, p = newPainter(60, BarHeight)
	drawBadge(p, toolkit.Rect{X: 4, Y: 2, W: 40, H: 24}, 5)
	if countNonZero(buf) == 0 {
		t.Fatalf("badge painted nothing")
	}
	// Narrow rect narrower than the pill: bx clamps to r.X, still paints.
	buf, p = newPainter(60, BarHeight)
	drawBadge(p, toolkit.Rect{X: 2, Y: 2, W: 4, H: 24}, 99)
	if countNonZero(buf) == 0 {
		t.Fatalf("narrow badge painted nothing")
	}
}

// countNonZero counts non-zero bytes in an RGBA buffer (any painted pixel).
func countNonZero(buf []byte) int {
	n := 0
	for _, b := range buf {
		if b != 0 {
			n++
		}
	}
	return n
}
