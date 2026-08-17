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

// LauncherRects reports one laid-out rect per launcher, resting when the cursor
// is off-surface. The dock forwards State.Magnify onto the widget, so hovering a
// launcher swells its rect. Both are read back through the composed AppDock.
func TestLauncherRects(t *testing.T) {
	s := New(tW, tH)
	// Resting (cursor outside): one rect per app, all non-empty.
	s.SetCursor(0, 0, false)
	lr := s.LauncherRects()
	if len(lr) != len(s.Apps) {
		t.Fatalf("LauncherRects len %d, want %d", len(lr), len(s.Apps))
	}
	for i, r := range lr {
		if r[2] <= 0 || r[3] <= 0 {
			t.Fatalf("resting launcher %d rect empty: %v", i, r)
		}
	}
	restW := lr[0][2]
	// Hover launcher 0: its laid-out width swells past the resting width.
	cx := lr[0][0] + lr[0][2]/2
	s.SetCursor(cx, tH/2, true)
	lr2 := s.LauncherRects()
	if lr2[0][2] <= restW {
		t.Fatalf("hovered LauncherRects[0] width %d not swollen from %d", lr2[0][2], restW)
	}
}

// Turning magnification off keeps the layout flat under a hovering cursor: the
// dock is handed Magnify=false so no rect swells.
func TestLauncherRectsFlatWhenMagnifyOff(t *testing.T) {
	s := New(tW, tH)
	s.SetMagnify(Magnify{On: false})
	rest := s.LauncherRects()
	// Hover launcher 0 with magnification disabled.
	cx := rest[0][0] + rest[0][2]/2
	s.SetCursor(cx, tH/2, true)
	got := s.LauncherRects()
	for i := range rest {
		if got[i] != rest[i] {
			t.Fatalf("magnify off: launcher %d moved %v -> %v under hover", i, rest[i], got[i])
		}
	}
}

// A magnified render with a running, focused, badged launcher exercises the
// dock's magnified glyph, running/active indicator and attention badge in one
// paint pass.
func TestRenderMagnifiedWithIndicators(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "Terminal", Focused: true}})
	s.SetBadge("terminal", 5)
	lr := s.LauncherRects()
	restW := lr[0][2]
	s.SetCursor(lr[0][0]+lr[0][2]/2, tH/2, true) // hover launcher 0
	buf := newBuf(s)
	Render(s, buf) // must not panic; covers magnified + indicator + badge paths
	// The hovered launcher swelled: its laid-out width exceeds the resting one.
	if s.LauncherRects()[0][2] <= restW {
		t.Fatalf("launcher did not magnify under hover")
	}
}
