package scene

import "testing"

// surface size used by most tests: wide enough to hold the default 3-icon row
// comfortably, tall enough for the bar plus magnification headroom.
const (
	tW = 320
	tH = 120
)

func newBuf(s *State) []byte { return make([]byte, 4*s.W*s.H) }

func TestNewHasDefaultApps(t *testing.T) {
	s := New(tW, tH)
	if got := len(s.Apps); got != 3 {
		t.Fatalf("default apps = %d, want 3", got)
	}
	want := []string{"terminal", "editor", "files"}
	for i, a := range s.Apps {
		if a.Id != want[i] {
			t.Fatalf("app[%d].Id = %q, want %q", i, a.Id, want[i])
		}
	}
}

func TestRenderFillsExactSize(t *testing.T) {
	s := New(tW, tH)
	buf := newBuf(s)
	Render(s, buf) // must not panic
}

func TestRenderPanicsOnSizeMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on size mismatch")
		}
	}()
	s := New(16, 16)
	Render(s, make([]byte, 4))
}

// The corners outside the bar must stay fully transparent (panel surface).
func TestRenderCornersTransparent(t *testing.T) {
	s := New(tW, tH)
	buf := newBuf(s)
	Render(s, buf)
	// Top-left pixel.
	if buf[3] != 0 {
		t.Fatalf("top-left alpha = %d, want 0 (transparent)", buf[3])
	}
}

// The bar region must contain visibly painted (non-transparent) pixels.
func TestRenderBarIsPainted(t *testing.T) {
	s := New(tW, tH)
	buf := newBuf(s)
	Render(s, buf)
	bx, by, bw, bh := s.BarRect()
	// Sample the bar center.
	cx := bx + bw/2
	cy := by + bh/2
	off := (cy*s.W + cx) * 4
	if buf[off+3] == 0 {
		t.Fatalf("bar center alpha = 0, expected painted bar")
	}
}

func TestBarRectWithinSurface(t *testing.T) {
	s := New(tW, tH)
	bx, by, bw, bh := s.BarRect()
	if bx < 0 || by < 0 || bx+bw > s.W || by+bh > s.H {
		t.Fatalf("bar rect (%d,%d,%d,%d) escapes surface %dx%d", bx, by, bw, bh, s.W, s.H)
	}
	// Bar must be horizontally centered: left and right margins roughly equal.
	leftMargin := bx
	rightMargin := s.W - (bx + bw)
	if d := leftMargin - rightMargin; d < -1 || d > 1 {
		t.Fatalf("bar not centered: left=%d right=%d", leftMargin, rightMargin)
	}
}

// At rest (cursor outside), every icon scales to exactly 1.0.
func TestRestScaleIsUnity(t *testing.T) {
	s := New(tW, tH)
	s.SetCursor(0, 0, false)
	for i := range s.Apps {
		if got := s.scaleFor(i); got != 1.0 {
			t.Fatalf("rest scale[%d] = %v, want 1.0", i, got)
		}
	}
}

// The icon directly under the cursor magnifies the most; far icons stay near 1.
func TestMagnificationPeaksUnderCursor(t *testing.T) {
	s := New(tW, tH)
	// Park cursor on icon 0's center.
	cx := int(s.iconCenterX(0))
	s.SetCursor(cx, tH-20, true)
	s0 := s.scaleFor(0)
	s2 := s.scaleFor(2)
	if s0 <= 1.0 {
		t.Fatalf("center icon scale = %v, want > 1", s0)
	}
	if s0 > MaxScale+1e-9 {
		t.Fatalf("center icon scale = %v exceeds MaxScale %v", s0, MaxScale)
	}
	if s0 <= s2 {
		t.Fatalf("under-cursor scale %v should exceed far scale %v", s0, s2)
	}
}

// An icon far beyond the influence radius rests at unity even with cursor inside.
func TestScaleBeyondInfluenceIsUnity(t *testing.T) {
	s := New(tW, tH)
	// Cursor far to the right of icon 0, beyond Influence*pitch.
	cx := int(s.iconCenterX(0) + Influence*pitch() + 5)
	s.SetCursor(cx, tH-20, true)
	if got := s.scaleFor(0); got != 1.0 {
		t.Fatalf("beyond-influence scale = %v, want 1.0", got)
	}
}

// scaleFor must handle a cursor to the LEFT of an icon (negative dx branch).
func TestScaleCursorLeftOfIcon(t *testing.T) {
	s := New(tW, tH)
	// Cursor a little left of icon 1's center but within influence.
	cx := int(s.iconCenterX(1) - pitch()/2)
	s.SetCursor(cx, tH-20, true)
	if got := s.scaleFor(1); got <= 1.0 {
		t.Fatalf("near-left scale = %v, want > 1", got)
	}
}

// HitTest returns the icon under a point, and -1 elsewhere.
func TestHitTest(t *testing.T) {
	s := New(tW, tH)
	for i := range s.Apps {
		ix, iy, iw, ih := s.IconRect(i)
		px := ix + iw/2
		py := iy + ih/2
		if got := s.HitTest(px, py); got != i {
			t.Fatalf("HitTest center of icon %d = %d, want %d", i, got, i)
		}
	}
	// A point in the far top-left corner hits nothing.
	if got := s.HitTest(0, 0); got != -1 {
		t.Fatalf("HitTest corner = %d, want -1", got)
	}
}

// IconRect for a magnified icon must be larger than the resting base.
func TestIconRectMagnifiedLarger(t *testing.T) {
	s := New(tW, tH)
	s.SetCursor(int(s.iconCenterX(1)), tH-20, true)
	_, _, w, _ := s.IconRect(1)
	if w <= IconBase {
		t.Fatalf("magnified icon width %d not larger than base %d", w, IconBase)
	}
}

func TestDefaultAppsGlyphs(t *testing.T) {
	apps := DefaultApps()
	wantGlyphs := []Glyph{GlyphTerminal, GlyphEditor, GlyphFiles}
	for i, a := range apps {
		if a.Glyph != wantGlyphs[i] {
			t.Fatalf("app[%d].Glyph = %v, want %v", i, a.Glyph, wantGlyphs[i])
		}
	}
}

// Exercise every glyph drawing path (including the default branch) and ensure
// each leaves painted pixels in its tile.
func TestEachGlyphRenders(t *testing.T) {
	glyphs := []Glyph{GlyphTerminal, GlyphEditor, GlyphFiles, Glyph(99)}
	for _, g := range glyphs {
		s := New(tW, tH)
		s.Apps = []App{{Id: "x", Glyph: g}}
		buf := newBuf(s)
		Render(s, buf)
		ix, iy, iw, ih := s.IconRect(0)
		// Count non-transparent pixels in the icon rect.
		painted := 0
		for y := iy; y < iy+ih; y++ {
			for x := ix; x < ix+iw; x++ {
				if x < 0 || y < 0 || x >= s.W || y >= s.H {
					continue
				}
				if buf[(y*s.W+x)*4+3] != 0 {
					painted++
				}
			}
		}
		if painted == 0 {
			t.Fatalf("glyph %v left no painted pixels", g)
		}
	}
}

// restRowWidth / rowLeft handle the empty-app edge case without dividing by
// zero or panicking.
func TestEmptyAppsRender(t *testing.T) {
	s := New(tW, tH)
	s.Apps = nil
	if got := s.restRowWidth(); got != 0 {
		t.Fatalf("empty restRowWidth = %v, want 0", got)
	}
	buf := newBuf(s)
	Render(s, buf) // bar still draws; no icons
	if got := s.HitTest(tW/2, tH/2); got != -1 {
		t.Fatalf("HitTest with no apps = %d, want -1", got)
	}
}

// blend must cover all three branches: fully transparent (no-op), fully opaque,
// and partial alpha (the src-over path).
func TestBlendBranches(t *testing.T) {
	s := New(4, 4)
	buf := newBuf(s)
	// Pre-fill with a known opaque base.
	for i := 0; i+3 < len(buf); i += 4 {
		buf[i], buf[i+1], buf[i+2], buf[i+3] = 0x10, 0x20, 0x30, 0xFF
	}
	// Transparent: no change.
	blend(s, buf, 0, 0, [4]uint8{0xFF, 0xFF, 0xFF, 0})
	if buf[0] != 0x10 {
		t.Fatalf("transparent blend modified pixel: %d", buf[0])
	}
	// Opaque: full replace.
	blend(s, buf, 1, 0, [4]uint8{0xAA, 0xBB, 0xCC, 0xFF})
	off := (0*s.W + 1) * 4
	if buf[off] != 0xAA || buf[off+3] != 0xFF {
		t.Fatalf("opaque blend = (%d,%d,%d,%d)", buf[off], buf[off+1], buf[off+2], buf[off+3])
	}
	// Partial: composited value between base and src.
	blend(s, buf, 2, 0, [4]uint8{0xFF, 0xFF, 0xFF, 0x80})
	off = (0*s.W + 2) * 4
	if buf[off] <= 0x10 || buf[off] >= 0xFF {
		t.Fatalf("partial blend R = %d, expected between base and src", buf[off])
	}
}

// blend and setPixel must ignore out-of-bounds coordinates.
func TestOutOfBoundsIgnored(t *testing.T) {
	s := New(4, 4)
	buf := newBuf(s)
	// These must be silent no-ops, not panics.
	blend(s, buf, -1, 0, [4]uint8{1, 1, 1, 0xFF})
	blend(s, buf, 0, -1, [4]uint8{1, 1, 1, 0xFF})
	blend(s, buf, 4, 0, [4]uint8{1, 1, 1, 0xFF})
	blend(s, buf, 0, 4, [4]uint8{1, 1, 1, 0xFF})
	setPixel(s, buf, -1, 0, [4]uint8{1, 1, 1, 0xFF})
	setPixel(s, buf, 0, -1, [4]uint8{1, 1, 1, 0xFF})
	setPixel(s, buf, 4, 0, [4]uint8{1, 1, 1, 0xFF})
	setPixel(s, buf, 0, 4, [4]uint8{1, 1, 1, 0xFF})
	// Nothing should have been written.
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("OOB write leaked into buffer")
		}
	}
}

// fillRoundRect with degenerate sizes is a no-op; with a large radius it clamps.
func TestFillRoundRectEdgeCases(t *testing.T) {
	s := New(40, 40)
	buf := newBuf(s)
	fillRoundRect(s, buf, 0, 0, 0, 10, 4, colBar) // zero width: no-op
	fillRoundRect(s, buf, 0, 0, 10, 0, 4, colBar) // zero height: no-op
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("degenerate fillRoundRect painted something")
		}
	}
	// Radius larger than half the box: must clamp and still paint.
	fillRoundRect(s, buf, 0, 0, 20, 10, 100, colBar)
	painted := false
	for i := 3; i < len(buf); i += 4 {
		if buf[i] != 0 {
			painted = true
			break
		}
	}
	if !painted {
		t.Fatalf("clamped-radius fillRoundRect painted nothing")
	}
}

// inRoundRect must cover all four corner branches plus the interior fast paths.
func TestInRoundRectCorners(t *testing.T) {
	w, h, r := 20, 20, 6
	// Interior fast paths.
	if !inRoundRect(10, 0, w, h, r) { // mid-x column, top row
		t.Fatal("mid-x top should be inside")
	}
	if !inRoundRect(0, 10, w, h, r) { // left column, mid-y
		t.Fatal("left mid-y should be inside")
	}
	// Each corner: a point well outside the arc must be excluded.
	if inRoundRect(0, 0, w, h, r) {
		t.Fatal("top-left arc corner pixel should be outside")
	}
	if inRoundRect(w-1, 0, w, h, r) {
		t.Fatal("top-right arc corner pixel should be outside")
	}
	if inRoundRect(0, h-1, w, h, r) {
		t.Fatal("bottom-left arc corner pixel should be outside")
	}
	if inRoundRect(w-1, h-1, w, h, r) {
		t.Fatal("bottom-right arc corner pixel should be outside")
	}
	// A point just inside a corner arc must be included.
	if !inRoundRect(r, r, w, h, r) {
		t.Fatal("corner arc center should be inside")
	}
}

// drawIcon with a non-positive size is a no-op (defends the wasm glue from a
// degenerate magnified rect).
func TestDrawIconDegenerate(t *testing.T) {
	s := New(20, 20)
	buf := newBuf(s)
	drawIcon(s, buf, GlyphTerminal, 0, 0, 0, 10)
	drawIcon(s, buf, GlyphTerminal, 0, 0, 10, 0)
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("degenerate drawIcon painted something")
		}
	}
}

// cosApprox must be ~1 at 0 and ~-1 at pi (the endpoints the magnifier feeds).
func TestCosApprox(t *testing.T) {
	if got := cosApprox(0); got < 0.999 || got > 1.001 {
		t.Fatalf("cos(0) = %v, want ~1", got)
	}
	if got := cosApprox(pi); got > -0.95 {
		t.Fatalf("cos(pi) = %v, want ~-1", got)
	}
}

func TestSetCursorStored(t *testing.T) {
	s := New(tW, tH)
	s.SetCursor(42, 17, true)
	if s.CursorX != 42 || s.CursorY != 17 || !s.CursorInside {
		t.Fatalf("SetCursor not stored: %+v", s)
	}
}
