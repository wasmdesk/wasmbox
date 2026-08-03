// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"math"
	"testing"

	"github.com/go-widgets/toolkit"
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

// magnificationActive is true only when enabled + cursor inside + radius>0 +
// peak>1; each factor alone flips it off.
func TestMagnificationActive(t *testing.T) {
	s := New(tW, tH)
	s.SetCursor(300, tH/2, true)
	if !s.magnificationActive() {
		t.Fatalf("expected active with defaults + cursor inside")
	}
	off := *s
	off.CursorInside = false
	if off.magnificationActive() {
		t.Fatalf("cursor outside must be inactive")
	}
	off = *s
	off.Magnify.On = false
	if off.magnificationActive() {
		t.Fatalf("On=false must be inactive")
	}
	off = *s
	off.Magnify.Radius = 0
	if off.magnificationActive() {
		t.Fatalf("Radius=0 must be inactive")
	}
	off = *s
	off.Magnify.MaxScale = 1
	if off.magnificationActive() {
		t.Fatalf("MaxScale=1 must be inactive")
	}
}

// bump: 1 at the cursor, 0 at/after the edge, in-between elsewhere.
func TestBump(t *testing.T) {
	if got := bump(0); got != 1 {
		t.Fatalf("bump(0) = %v, want 1", got)
	}
	if got := bump(-0.5); got != 1 {
		t.Fatalf("bump(neg) = %v, want 1", got)
	}
	if got := bump(1); got != 0 {
		t.Fatalf("bump(1) = %v, want 0", got)
	}
	if got := bump(2); got != 0 {
		t.Fatalf("bump(2) = %v, want 0", got)
	}
	mid := bump(0.5)
	if mid <= 0 || mid >= 1 {
		t.Fatalf("bump(0.5) = %v, want in (0,1)", mid)
	}
	if math.Abs(mid-0.5) > 1e-9 {
		t.Fatalf("bump(0.5) = %v, want 0.5 (raised cosine midpoint)", mid)
	}
}

// laidOutSlots with magnification inactive is exactly restingSlots (byte-for-
// byte geometry), so every resting-geometry test stays valid.
func TestLaidOutSlotsInactiveEqualsResting(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "a"}, {Id: 2, Title: "b"}})
	// Cursor outside -> inactive.
	s.SetCursor(0, 0, false)
	rest := s.restingSlots()
	got := s.laidOutSlots()
	if len(got) != len(rest) {
		t.Fatalf("laidOut len %d != resting %d", len(got), len(rest))
	}
	for i := range rest {
		if got[i] != rest[i] {
			t.Fatalf("slot[%d] laidOut %+v != resting %+v", i, got[i], rest[i])
		}
	}
	// Empty iconbar (no apps, no windows) returns an empty slice unchanged.
	s2 := New(tW, tH)
	s2.Apps = nil
	s2.SetCursor(300, tH/2, true)
	if len(s2.laidOutSlots()) != 0 {
		t.Fatalf("empty iconbar should lay out no slots")
	}
}

// Under an active hover the slot nearest the cursor swells the most, widths grow
// and the row reflows left-to-right without overlap; the leftmost slot stays
// anchored at its resting left edge.
func TestLaidOutSlotsMagnifies(t *testing.T) {
	s := New(tW, tH)
	// Cursor over the centre of launcher button 1.
	bx, _, bw, _ := s.IconbarButtonRect(1)
	cursorX := bx + bw/2
	s.SetCursor(cursorX, tH/2, true)

	rest := s.restingSlots()
	got := s.laidOutSlots()

	// Cursor-anchored: the hovered icon stays under the pointer (does not slide
	// out from under it), so the cursor still falls inside the swollen slot 1.
	if !(cursorX >= got[1].x && cursorX < got[1].x+got[1].w) {
		t.Fatalf("cursor %d not over swollen hovered slot [%d,%d)", cursorX, got[1].x, got[1].x+got[1].w)
	}
	// The hovered slot (idx 1) is the widest + most scaled.
	if got[1].scale <= 1 {
		t.Fatalf("hovered slot scale %v not > 1", got[1].scale)
	}
	if got[1].w <= rest[1].w {
		t.Fatalf("hovered slot width %d not swollen from %d", got[1].w, rest[1].w)
	}
	// Hovered slot has the peak scale of the row.
	for i := range got {
		if got[i].scale > got[1].scale+1e-9 {
			t.Fatalf("slot[%d] scale %v exceeds hovered %v", i, got[i].scale, got[1].scale)
		}
	}
	// No overlap + left-to-right order: each slot starts at/after the previous
	// slot's right edge.
	for i := 1; i < len(got); i++ {
		if got[i].x < got[i-1].x+got[i-1].w {
			t.Fatalf("slot[%d] overlaps slot[%d]", i, i-1)
		}
	}
	// Height swells but stays within the surface (y >= 1, bottom == resting bottom).
	if got[1].y < 1 {
		t.Fatalf("swollen slot top %d rose above the border row", got[1].y)
	}
	if got[1].y+got[1].h != rest[1].y+rest[1].h {
		t.Fatalf("swollen slot not bottom-anchored: %d != %d", got[1].y+got[1].h, rest[1].y+rest[1].h)
	}
}

// A far-away slot (cursor beyond the falloff radius) keeps scale 1.
func TestLaidOutSlotsFarSlotUnscaled(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "w"}})
	// Cursor at the far left edge of the iconbar; the last window slot is far.
	ix, _, _, _ := s.IconbarRect()
	s.SetCursor(ix+1, tH/2, true)
	got := s.laidOutSlots()
	last := got[len(got)-1]
	if last.scale != 1 {
		t.Fatalf("far slot scale = %v, want 1", last.scale)
	}
}

// LauncherRects / WindowRects report the laid-out geometry, magnified when the
// cursor hovers.
func TestLauncherAndWindowRects(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "w"}})
	// Resting (cursor outside): launcher rects match IconbarButtonRect.
	s.SetCursor(0, 0, false)
	lr := s.LauncherRects()
	if len(lr) != len(s.Apps) {
		t.Fatalf("LauncherRects len %d, want %d", len(lr), len(s.Apps))
	}
	bx, by, bw, bh := s.IconbarButtonRect(0)
	if lr[0] != [4]int{bx, by, bw, bh} {
		t.Fatalf("resting LauncherRects[0] = %v, want %v", lr[0], [4]int{bx, by, bw, bh})
	}
	wr := s.WindowRects()
	if len(wr) != 1 {
		t.Fatalf("WindowRects len %d, want 1", len(wr))
	}
	wx, wy, ww, wh := s.WindowButtonRect(0)
	if wr[0] != [4]int{wx, wy, ww, wh} {
		t.Fatalf("resting WindowRects[0] = %v, want %v", wr[0], [4]int{wx, wy, ww, wh})
	}
	// Hovering launcher 0 magnifies its rect.
	s.SetCursor(bx+bw/2, tH/2, true)
	lr2 := s.LauncherRects()
	if lr2[0][2] <= bw {
		t.Fatalf("hovered LauncherRects[0] width %d not swollen from %d", lr2[0][2], bw)
	}
}

// glyphSize scales the glyph with the button and clamps it to the button
// height (and to a 1px floor for a degenerate button).
func TestGlyphSize(t *testing.T) {
	b := &launcherButton{scale: 1}
	if g := b.glyphSize(toolkit.Rect{H: 24}); g != IconGlyphPx {
		t.Fatalf("resting glyphSize = %d, want %d", g, IconGlyphPx)
	}
	b.scale = 2
	if g := b.glyphSize(toolkit.Rect{H: 24}); g != 20 { // 32 clamped to H-4=20
		t.Fatalf("scaled+clamped glyphSize = %d, want 20", g)
	}
	if g := b.glyphSize(toolkit.Rect{H: 3}); g != 1 { // max=-1 -> floor 1
		t.Fatalf("degenerate glyphSize = %d, want 1", g)
	}
}

// A magnified render with a running, focused, badged launcher exercises the
// swollen-glyph clamp, the running/focused indicator overlay, the attention
// badge and the magnified window-button paint in one pass.
func TestRenderMagnifiedWithIndicators(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "Terminal", Focused: true}})
	s.SetBadge("terminal", 5)
	bx, _, bw, _ := s.IconbarButtonRect(0)
	s.SetCursor(bx+bw/2, tH/2, true) // hover launcher 0
	buf := newBuf(s)
	Render(s, buf) // must not panic; covers magnified + indicator + badge paths
	// The hovered launcher swelled: its laid-out width exceeds the resting one.
	if s.LauncherRects()[0][2] <= bw {
		t.Fatalf("launcher did not magnify under hover")
	}
	// Some near-red badge ink is present (alert pill).
	found := false
	for i := 0; i+3 < len(buf) && !found; i += 4 {
		if buf[i] > 0xC0 && buf[i+1] < 0x60 && buf[i+2] < 0x60 {
			found = true
		}
	}
	if !found {
		t.Fatalf("attention badge (red pill) never painted")
	}
}
