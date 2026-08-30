// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"testing"
	"time"

	icons "github.com/go-icons/iconoir"
	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
	"github.com/wasmdesk/wasmbox/clients/dock/internal/theme"
)

// Surface size used by most tests: 1280-wide x 28-tall, matching the worker
// surface dimensions.
const (
	tW = 1280
	tH = BarHeight
)

func newBuf(s *State) []byte { return make([]byte, 4*s.W*s.H) }

// inkAt reports whether the pixel at (x,y) of the tW-wide RGBA buffer is
// near-black glyph ink (all channels < 0x40).
func inkAt(buf []byte, x, y int) bool {
	off := (y*tW + x) * 4
	return buf[off] < 0x40 && buf[off+1] < 0x40 && buf[off+2] < 0x40
}

// inkedIn reports whether region [x0,x0+w) x [0,tH) holds any glyph ink.
func inkedIn(buf []byte, x0, w int) bool {
	for y := 0; y < tH; y++ {
		for x := x0; x < x0+w; x++ {
			if inkAt(buf, x, y) {
				return true
			}
		}
	}
	return false
}

// distinctLuma counts how many distinct R+G+B sums appear inside region r of an
// RGBA buffer. The 5x7 bitmap font paints each text pixel either full ink or
// untouched ground, so a bitmap render of a text region holds only the ground's
// handful of levels plus one ink level; the AA/shaped face scan-converts glyph
// outlines to partial-coverage masks, adding a whole ramp of intermediate levels
// the bitmap can never produce. A strictly higher distinct-luma count over the
// SAME region is therefore a ground-independent proof the AA face is active.
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

// TestAATextIsAntiAliased proves the dock renders labels with the toolkit's
// AA/shaped OpenType face rather than the 5x7 bitmap. It renders the iconbar
// (rich launcher-label text) with the AA face (New enabled it) and again with
// the bitmap default, and asserts the AA render carries strictly more distinct
// luma levels in that region — the partial-coverage ramp only anti-aliasing
// produces. The region spans a bevelled ground, so the assertion is deliberately
// ground-independent (see distinctLuma).
func TestAATextIsAntiAliased(t *testing.T) {
	const W, H = 400, BarHeight
	// The iconbar span [WorkspaceW, W-ClockW) carries the launcher labels.
	region := toolkit.Rect{X: WorkspaceW, Y: 0, W: W - WorkspaceW - ClockW, H: H}

	aa := make([]byte, 4*W*H)
	Render(New(W, H), aa) // AA face (enableAAText ran in New)

	toolkit.SetFont(nil) // bitmap default
	defer func() { _ = toolkit.UseOpenTypeText() }()
	bm := make([]byte, 4*W*H)
	Render(New(W, H), bm)

	aaN := distinctLuma(aa, W, region)
	bmN := distinctLuma(bm, W, region)
	if aaN <= bmN {
		t.Fatalf("iconbar labels: distinct luma aa=%d not > bitmap=%d — AA face not active", aaN, bmN)
	}
	t.Logf("iconbar distinct luma: aa=%d bm=%d", aaN, bmN)
}

// TestAAFaceFitsBar asserts the taller AA line box still fits the dock's fixed
// bands: the 20px face sits inside the toolbar height and the fixed-width
// workspace/clock zones hold their shaped text. If a future face/size overflowed
// a band this fails loudly instead of silently clipping.
func TestAAFaceFitsBar(t *testing.T) {
	s := New(tW, tH) // switches the global font to the AA face
	f, err := toolkit.DefaultOpenTypeFont(toolkit.DefaultOpenTypeSizePx)
	if err != nil {
		t.Fatalf("DefaultOpenTypeFont: %v", err)
	}
	h := f.Height()
	if h != 20 {
		t.Fatalf("AA face height = %d, want 20 (retune bands if this changes)", h)
	}
	if h > s.H {
		t.Fatalf("AA height %d overflows bar height %d", h, s.H)
	}
	// The clock reading fits its zone.
	if w := toolkit.TextWidth("00:00"); w > ClockW {
		t.Fatalf("clock reading width %d overflows ClockW %d", w, ClockW)
	}
}

// newPainter returns a zeroed RGBA buffer of w*h plus a PixelPainter over it,
// for tests that drive the low-level glyph helpers directly.
func newPainter(w, h int) ([]byte, *painter.PixelPainter) {
	buf := make([]byte, 4*w*h)
	return buf, painter.NewPixelPainter(buf, w, h)
}

func TestNewHasDefaults(t *testing.T) {
	s := New(tW, tH)
	if got, want := len(s.Apps), 6; got != want {
		t.Fatalf("default apps = %d, want %d", got, want)
	}
	want := []string{"terminal", "editor", "files", "hello", "vscode", "loom"}
	for i, a := range s.Apps {
		if a.Id != want[i] {
			t.Fatalf("app[%d].Id = %q, want %q", i, a.Id, want[i])
		}
	}
	if s.Workspace != "1 of 4" {
		t.Fatalf("default workspace = %q, want %q", s.Workspace, "1 of 4")
	}
	if s.Clock != "" {
		t.Fatalf("default clock = %q, want empty (worker will tick)", s.Clock)
	}
	if s.Theme.Border.Width != 1 {
		t.Fatalf("default theme missing border width")
	}
}

// The DockPanel lays the iconbar out at exactly [WorkspaceW, W-ClockW]: the
// WorkspacePager Leading zone consumes WorkspaceW, the Clock Trailing zone
// consumes ClockW, and the AppDock fills the middle.
func TestIconbarLandsBetweenEnds(t *testing.T) {
	s := New(tW, tH)
	ix, _, iw, ih := s.IconbarRect()
	if ix != WorkspaceW {
		t.Fatalf("iconbar x = %d, want %d", ix, WorkspaceW)
	}
	if iw != tW-WorkspaceW-ClockW {
		t.Fatalf("iconbar width = %d, want %d", iw, tW-WorkspaceW-ClockW)
	}
	if ih != tH {
		t.Fatalf("iconbar height = %d, want %d", ih, tH)
	}
}

// On a narrow surface where workspace + clock would overlap, the iconbar
// collapses to width 0 (never negative).
func TestIconbarClampsToZeroOnNarrowSurface(t *testing.T) {
	s := New(50, tH) // 50 < WorkspaceW (100) + ClockW (80)
	_, _, iw, _ := s.IconbarRect()
	if iw != 0 {
		t.Fatalf("iconbar width on narrow surface = %d, want 0", iw)
	}
}

// Launcher items lay out inside the iconbar at the fixed IconbarButtonW width,
// left to right in Apps order.
func TestLauncherRectsLayout(t *testing.T) {
	s := New(tW, tH)
	ix, _, _, _ := s.IconbarRect()
	lr := s.LauncherRects()
	if len(lr) != len(s.Apps) {
		t.Fatalf("LauncherRects len %d, want %d", len(lr), len(s.Apps))
	}
	for i := range lr {
		if lr[i][2] != IconbarButtonW {
			t.Fatalf("launcher[%d] width = %d, want %d", i, lr[i][2], IconbarButtonW)
		}
		if lr[i][0] < ix {
			t.Fatalf("launcher[%d] x = %d, before iconbar left %d", i, lr[i][0], ix)
		}
		if i > 0 && lr[i][0] < lr[i-1][0]+lr[i-1][2] {
			t.Fatalf("launcher[%d] overlaps launcher[%d]", i, i-1)
		}
	}
}

// A click at the center of launcher i must HitTest to i, and the resulting
// Apps[i].Id must be the documented launch string ("terminal"/"editor"/etc).
func TestClickAtLauncherCenterDispatchesExpectedApp(t *testing.T) {
	cases := []string{"terminal", "editor", "files", "hello", "vscode", "loom"}
	s := New(tW, tH)
	if got, want := len(s.Apps), len(cases); got != want {
		t.Fatalf("apps = %d, want %d", got, want)
	}
	lr := s.LauncherRects()
	for i, wantID := range cases {
		px := lr[i][0] + lr[i][2]/2
		py := lr[i][1] + lr[i][3]/2
		hit := s.HitTest(px, py)
		if hit != i {
			t.Fatalf("HitTest center of launcher %d = %d, want %d", i, hit, i)
		}
		if got := s.Apps[hit].Id; got != wantID {
			t.Fatalf("launcher %d dispatches %q, want %q", i, got, wantID)
		}
	}
}

// Clicks on the workspace switcher / clock are inert for HitTest (the AppDock's
// item rects live strictly inside the iconbar's x-range).
func TestClicksOnWorkspaceAndClockAreInert(t *testing.T) {
	s := New(tW, tH)
	if got := s.HitTest(WorkspaceW/2, tH/2); got != -1 {
		t.Fatalf("workspace click HitTest = %d, want -1", got)
	}
	if got := s.HitTest(tW-ClockW/2, tH/2); got != -1 {
		t.Fatalf("clock click HitTest = %d, want -1", got)
	}
}

// A click inside the iconbar but in the inter-item gap misses.
func TestClickInGapMisses(t *testing.T) {
	s := New(tW, tH)
	lr := s.LauncherRects()
	gapX := lr[0][0] + lr[0][2]
	if gapX >= lr[1][0] {
		t.Skip("no gap between launcher 0 and 1 in this layout")
	}
	if got := s.HitTest(gapX, tH/2); got != -1 {
		t.Fatalf("gap-click HitTest = %d, want -1", got)
	}
}

// Render fills the whole surface (no transparent pixels).
func TestRenderFillsAllPixelsOpaque(t *testing.T) {
	s := New(tW, tH)
	s.SetClock("12:34")
	buf := newBuf(s)
	Render(s, buf)
	for i := 3; i < len(buf); i += 4 {
		if buf[i] != 0xFF {
			t.Fatalf("non-opaque pixel at byte %d: alpha=%d", i, buf[i])
		}
	}
}

func TestRenderPanicsOnSizeMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on size mismatch")
		}
	}()
	s := New(16, BarHeight)
	Render(s, make([]byte, 4))
}

// The workspace switcher paints ink (the cell numbers) in its zone.
func TestRenderWorkspaceInked(t *testing.T) {
	s := New(tW, tH)
	buf := newBuf(s)
	Render(s, buf)
	if !inkedIn(buf, 0, WorkspaceW) {
		t.Fatalf("workspace switcher never inked")
	}
}

// With an explicit clock string the clock zone paints near-black ink.
func TestRenderClockInked(t *testing.T) {
	s := New(tW, tH)
	s.SetClock("09:42")
	buf := newBuf(s)
	Render(s, buf)
	if !inkedIn(buf, tW-ClockW, ClockW) {
		t.Fatalf("clock reading never inked")
	}
}

// An empty clock falls back to the placeholder "--:--" so the zone is always
// visually present.
func TestRenderClockFallback(t *testing.T) {
	s := New(tW, tH)
	s.SetClock("")
	buf := newBuf(s)
	Render(s, buf)
	if !inkedIn(buf, tW-ClockW, ClockW) {
		t.Fatalf("fallback clock '--:--' never inked")
	}
}

// clockReading formats a real time and falls back to the placeholder on the
// zero time.
func TestClockReading(t *testing.T) {
	if got := clockReading(time.Time{}); got != "--:--" {
		t.Fatalf("clockReading(zero) = %q, want %q", got, "--:--")
	}
	tm := time.Date(2026, 8, 24, 9, 5, 0, 0, time.UTC)
	if got := clockReading(tm); got != "09:05" {
		t.Fatalf("clockReading(09:05) = %q, want %q", got, "09:05")
	}
}

// SetClock parses a well-formed reading, resets to the placeholder on empty, and
// keeps the last reading on a malformed string (never blanks the display).
func TestSetClockParsing(t *testing.T) {
	s := New(tW, tH)
	s.SetClock("07:08")
	if got := s.clock.Time().Get().Format(clockLayout); got != "07:08" {
		t.Fatalf("after SetClock(07:08) clock = %q, want 07:08", got)
	}
	// Malformed: the stored reading stays 07:08, but State.Clock records the raw.
	s.SetClock("not-a-time")
	if got := s.clock.Time().Get().Format(clockLayout); got != "07:08" {
		t.Fatalf("malformed SetClock changed the clock to %q, want kept 07:08", got)
	}
	if s.Clock != "not-a-time" {
		t.Fatalf("State.Clock = %q, want raw %q", s.Clock, "not-a-time")
	}
	// Empty resets to the zero time (placeholder).
	s.SetClock("   ")
	if !s.clock.Time().Get().IsZero() {
		t.Fatalf("blank SetClock did not reset the clock to zero")
	}
}

// The top border row is the theme.Border.Color across the full width.
func TestRenderTopBorderColor(t *testing.T) {
	s := New(tW, tH)
	buf := newBuf(s)
	Render(s, buf)
	bc := s.Theme.Border.Color
	for x := 0; x < tW; x++ {
		off := (0*tW + x) * 4
		if buf[off] != bc[0] || buf[off+1] != bc[1] || buf[off+2] != bc[2] {
			t.Fatalf("top border at x=%d = %v, want %v", x, buf[off:off+3], bc)
		}
	}
}

// Disabling the border (Width = 0) skips the top stroke — row 0 then shows the
// bevel-gray ground, not the border colour.
func TestRenderTopBorderSkippedWhenWidthZero(t *testing.T) {
	s := New(tW, tH)
	th := s.Theme
	th.Border.Width = 0
	s.SetTheme(th)
	buf := newBuf(s)
	Render(s, buf)
	bc := s.Theme.Border.Color
	if buf[0] == bc[0] && buf[1] == bc[1] && buf[2] == bc[2] {
		t.Fatalf("top border still painted when Width=0")
	}
}

// The ground Backdrop's Fill tracks the theme's inactive-title bevel gray.
func TestGroundTracksTheme(t *testing.T) {
	s := New(tW, tH)
	want := rgba(s.Theme.Window.Inactive.Title.Bg.Color)
	if s.ground.Fill != want {
		t.Fatalf("ground Fill = %+v, want %+v", s.ground.Fill, want)
	}
	th := s.Theme
	th.Window.Inactive.Title.Bg.Color = theme.Color{0x44, 0x55, 0x66}
	s.SetTheme(th)
	if s.ground.Fill != toolkit.RGB(0x44, 0x55, 0x66) {
		t.Fatalf("ground Fill did not track SetTheme: %+v", s.ground.Fill)
	}
}

// toolkitTheme derives the DockPanel palette from the Openbox theme so the whole
// bar re-themes on a live switch: the toolbar face (SurfaceAlt / ground) tracks
// the inactive-title bg, the item face (Surface) the OSD bg, the ink the OSD
// label, the highlight (Accent) the active-title bg and the separator the border.
func TestToolkitThemeDerivesFromOpenbox(t *testing.T) {
	th := theme.Theme{}
	th.Window.Inactive.Title.Bg.Color = theme.Color{0x11, 0x11, 0x11} // toolbar face
	th.Window.Active.Title.Bg.Color = theme.Color{0x22, 0x33, 0x44}   // highlight
	th.Osd.Bg.Color = theme.Color{0x30, 0x30, 0x30}                   // item face
	th.Osd.Label.Color = theme.Color{0xF0, 0xF0, 0xF0}                // ink
	th.Border.Color = theme.Color{0x50, 0x50, 0x50}                   // separator
	tk := toolkitTheme(th)
	if tk.SurfaceAlt != toolkit.RGB(0x11, 0x11, 0x11) || tk.Background != toolkit.RGB(0x11, 0x11, 0x11) {
		t.Fatalf("SurfaceAlt/Background = %+v/%+v, want toolbar face", tk.SurfaceAlt, tk.Background)
	}
	if tk.Surface != toolkit.RGB(0x30, 0x30, 0x30) {
		t.Fatalf("Surface = %+v, want OSD bg", tk.Surface)
	}
	if tk.OnSurface != toolkit.RGB(0xF0, 0xF0, 0xF0) || tk.OnBackground != toolkit.RGB(0xF0, 0xF0, 0xF0) {
		t.Fatalf("ink = %+v/%+v, want OSD label", tk.OnSurface, tk.OnBackground)
	}
	if tk.Accent != toolkit.RGB(0x22, 0x33, 0x44) {
		t.Fatalf("Accent = %+v, want active-title bg", tk.Accent)
	}
	if tk.Border != toolkit.RGB(0x50, 0x50, 0x50) {
		t.Fatalf("Border = %+v, want border colour", tk.Border)
	}
}

// A live theme switch re-themes the whole bar: the DockPanel palette (tkTheme)
// tracks the new theme AND the rendered workspace-switcher face pixels shift, so
// the accessories follow the theme — not just the ground behind them. This is
// the regression the theme round-trip probe guards.
func TestLiveThemeSwitchRethemesAccessories(t *testing.T) {
	s := New(tW, tH)
	buf1 := newBuf(s)
	Render(s, buf1)
	// A non-current workspace cell's face (workspace 2 cell, left of its digit).
	sampleX, sampleY := WorkspaceW/2-20, tH/2
	before := buf1[(sampleY*tW+sampleX)*4]

	dark := theme.DefaultFluxboxLight()
	dark.Window.Inactive.Title.Bg = theme.Bg{Color: theme.Color{0x1a, 0x1a, 0x1a}, ColorTo: theme.Color{0x1a, 0x1a, 0x1a}}
	dark.Osd.Bg.Color = theme.Color{0x30, 0x30, 0x30}
	dark.Osd.Label.Color = theme.Color{0xf0, 0xf0, 0xf0}
	s.SetTheme(dark)
	if s.tkTheme.SurfaceAlt != toolkit.RGB(0x1a, 0x1a, 0x1a) {
		t.Fatalf("tkTheme SurfaceAlt did not follow the dark theme: %+v", s.tkTheme.SurfaceAlt)
	}
	buf2 := newBuf(s)
	Render(s, buf2)
	after := buf2[(sampleY*tW+sampleX)*4]
	if int(before)-int(after) < 40 {
		t.Fatalf("workspace-switcher face did not darken on theme switch: before R=%d after R=%d", before, after)
	}
}

// SetCursor / SetWorkspace / SetClock store their arguments.
func TestSetters(t *testing.T) {
	s := New(tW, tH)
	s.SetCursor(11, 22, true)
	if s.CursorX != 11 || s.CursorY != 22 || !s.CursorInside {
		t.Fatalf("SetCursor not stored: %+v", s)
	}
	s.SetWorkspace("3")
	if s.Workspace != "3" {
		t.Fatalf("SetWorkspace not stored: %q", s.Workspace)
	}
	s.SetClock("23:59")
	if s.Clock != "23:59" {
		t.Fatalf("SetClock not stored: %q", s.Clock)
	}
}

// ---- glyph helpers (painter-level) ---------------------------------------

// Each glyph + the default branch (unknown glyph) must paint at least one
// pixel of ink inside its tile.
func TestEachGlyphPaints(t *testing.T) {
	glyphs := []Glyph{GlyphTerminal, GlyphEditor, GlyphFiles, GlyphHello, GlyphCode, GlyphLoom, Glyph(99)}
	for _, g := range glyphs {
		buf, p := newPainter(tW, tH)
		for i := 0; i+3 < len(buf); i += 4 {
			buf[i], buf[i+1], buf[i+2], buf[i+3] = 0xC8, 0xC8, 0xC8, 0xFF
		}
		drawGlyph(p, g, toolkit.Rect{X: 10, Y: 10, W: IconGlyphPx, H: IconGlyphPx})
		painted := 0
		for y := 10; y < 10+IconGlyphPx; y++ {
			for x := 10; x < 10+IconGlyphPx; x++ {
				off := (y*tW + x) * 4
				if buf[off] < 0x40 {
					painted++
				}
			}
		}
		if painted == 0 {
			t.Fatalf("glyph %v left no ink pixels", g)
		}
	}
}

// glyphStem maps every bespoke launcher glyph to a real go-iconoir stem, with
// "square" as the fallback for an unknown glyph value.
func TestGlyphStem(t *testing.T) {
	cases := []struct {
		g    Glyph
		want string
	}{
		{GlyphTerminal, "terminal"},
		{GlyphHello, "home"},
		{GlyphCode, "code-brackets"},
		{GlyphLoom, "view-grid"},
		{Glyph(99), "square"},
	}
	names := map[string]bool{}
	for _, n := range icons.Names() {
		names[n] = true
	}
	for _, c := range cases {
		got := glyphStem(c.g)
		if got != c.want {
			t.Fatalf("glyphStem(%v) = %q, want %q", c.g, got, c.want)
		}
		if !names[got] {
			t.Fatalf("glyphStem(%v) = %q is not a real iconoir stem", c.g, got)
		}
	}
}

// A bespoke launcher glyph paints through go-iconoir: drawGlyph with an
// iconoir-backed glyph must ink pixels via the resolved stem.
func TestGlyphDrawsIconoir(t *testing.T) {
	for _, g := range []Glyph{GlyphTerminal, GlyphHello, GlyphCode, GlyphLoom} {
		buf, p := newPainter(64, 64)
		drawGlyph(p, g, toolkit.Rect{X: 8, Y: 8, W: 32, H: 32})
		if countNonZero(buf) == 0 {
			t.Fatalf("iconoir glyph %v painted nothing", g)
		}
	}
}

// drawGlyph with a non-positive size is a no-op.
func TestDrawGlyphDegenerate(t *testing.T) {
	buf, p := newPainter(40, BarHeight)
	drawGlyph(p, GlyphTerminal, toolkit.Rect{X: 0, Y: 0, W: 0, H: 10})
	drawGlyph(p, GlyphTerminal, toolkit.Rect{X: 0, Y: 0, W: 10, H: 0})
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("degenerate drawGlyph painted something: %d", b)
		}
	}
}

// ---- narrow-surface + overflow render paths -------------------------------

// A narrow iconbar renders without panic (the DockPanel clips the dock run).
func TestRenderNarrowIconbar(t *testing.T) {
	s := New(220, BarHeight) // iconbar width = 40
	buf := newBuf(s)
	Render(s, buf) // must not panic
	if buf[0] == 0 && buf[3] == 0 {
		t.Fatalf("narrow render did not paint top-left")
	}
}

// More launchers than fit renders without panic.
func TestRenderExtraLaunchers(t *testing.T) {
	s := New(400, BarHeight)
	s.SetApps([]App{
		{Id: "a", Glyph: GlyphTerminal, Label: "A"},
		{Id: "b", Glyph: GlyphEditor, Label: "B"},
		{Id: "c", Glyph: GlyphFiles, Label: "C"},
	})
	buf := newBuf(s)
	Render(s, buf) // must not panic
}

// When the iconbar shrinks to width 0 the AppDock must not paint / panic.
func TestRenderZeroWidthIconbar(t *testing.T) {
	s := New(WorkspaceW+ClockW, BarHeight) // iconbar collapses to 0
	buf := newBuf(s)
	Render(s, buf) // must not panic
}

// SetWindows stores the snapshot verbatim so the next render picks it up.
func TestSetWindowsStores(t *testing.T) {
	s := New(tW, tH)
	if len(s.Windows) != 0 {
		t.Fatalf("fresh state should have 0 windows, got %d", len(s.Windows))
	}
	s.SetWindows([]Window{
		{Id: 7, Title: "xterm", Focused: true},
		{Id: 12, Title: "editor", Minimized: true},
	})
	if got, want := len(s.Windows), 2; got != want {
		t.Fatalf("SetWindows length = %d, want %d", got, want)
	}
	if s.Windows[0].Id != 7 || s.Windows[0].Title != "xterm" || !s.Windows[0].Focused {
		t.Fatalf("Windows[0] = %+v, want {7 xterm focused}", s.Windows[0])
	}
	if !s.Windows[1].Minimized {
		t.Fatalf("Windows[1].Minimized = false, want true")
	}
	s.SetWindows(nil)
	if len(s.Windows) != 0 {
		t.Fatalf("SetWindows(nil) should clear, got %d", len(s.Windows))
	}
}

// Window task buttons follow the launcher row: their rects begin at or past the
// last launcher's right edge, in Windows order.
func TestWindowRectsFollowLaunchers(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "a"}, {Id: 2, Title: "b"}})
	lr := s.LauncherRects()
	lastLauncherRight := lr[len(lr)-1][0] + lr[len(lr)-1][2]
	wr := s.WindowRects()
	if len(wr) != 2 {
		t.Fatalf("WindowRects len %d, want 2", len(wr))
	}
	if wr[0][0] < lastLauncherRight {
		t.Fatalf("window[0].x = %d, before last launcher right %d", wr[0][0], lastLauncherRight)
	}
	if wr[1][0] < wr[0][0]+wr[0][2] {
		t.Fatalf("window[1] overlaps window[0]")
	}
}

// HitTestWindow returns the window index for clicks inside a window button
// and -1 for clicks outside (workspace, clock, launcher row).
func TestHitTestWindow(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 10, Title: "win10"}, {Id: 20, Title: "win20", Focused: true}})
	wr := s.WindowRects()
	for i := range s.Windows {
		px := wr[i][0] + wr[i][2]/2
		py := wr[i][1] + wr[i][3]/2
		if got := s.HitTestWindow(px, py); got != i {
			t.Fatalf("HitTestWindow center of window %d = %d, want %d", i, got, i)
		}
		if got := s.HitTest(px, py); got != -1 {
			t.Fatalf("HitTest center of window %d = %d, want -1 (launcher hit-test)", i, got)
		}
	}
	if got := s.HitTestWindow(WorkspaceW/2, tH/2); got != -1 {
		t.Fatalf("workspace HitTestWindow = %d, want -1", got)
	}
	if got := s.HitTestWindow(tW-ClockW/2, tH/2); got != -1 {
		t.Fatalf("clock HitTestWindow = %d, want -1", got)
	}
	lr := s.LauncherRects()
	if got := s.HitTestWindow(lr[0][0]+lr[0][2]/2, lr[0][1]+lr[0][3]/2); got != -1 {
		t.Fatalf("launcher click HitTestWindow = %d, want -1", got)
	}
}

// A window task button paints ink for its title.
func TestRenderWindowInked(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 7, Title: "alpha"}})
	buf := newBuf(s)
	Render(s, buf)
	wr := s.WindowRects()
	bx, by, bw, bh := wr[0][0], wr[0][1], wr[0][2], wr[0][3]
	found := false
	for y := by; y < by+bh && !found; y++ {
		for x := bx; x < bx+bw && !found; x++ {
			if inkAt(buf, x, y) {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("window button never inked any pixels")
	}
}

// Render does not panic with more window buttons than comfortably fit.
func TestRenderWindowOverflow(t *testing.T) {
	s := New(400, BarHeight) // narrow iconbar
	s.SetWindows([]Window{{Id: 1, Title: "off"}, {Id: 2, Title: "off2"}})
	buf := newBuf(s)
	Render(s, buf) // must not panic
}

// A focused window task button paints with a SUNKEN bevel (bright bottom
// stroke) while an unfocused one carries a RAISED bevel (dark bottom stroke) —
// the toolkit.BevelDockStyle Fluxbox focus cue.
func TestRenderFocusedSunkenBevel(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{
		{Id: 1, Title: "focused", Focused: true},
		{Id: 2, Title: "unfocused"},
	})
	buf := newBuf(s)
	Render(s, buf)
	wr := s.WindowRects()
	sample := func(r [4]int) int {
		x := r[0] + 2
		y := r[1] + r[3] - 1 // bottom bevel row
		return int(buf[(y*tW+x)*4])
	}
	fb := sample(wr[0]) // focused -> sunken -> bright bottom
	ub := sample(wr[1]) // unfocused -> raised -> dark bottom
	if fb <= ub+40 {
		t.Fatalf("focused bottom stroke (%d) not clearly brighter than unfocused (%d)", fb, ub)
	}
}

// A minimized window task button paints ink (the "[*] " prefix + title) and a
// RAISED bevel (it is not focused, so the bottom stroke is dark, not bright).
func TestRenderMinimizedStyle(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "alpha", Minimized: true}})
	buf := newBuf(s)
	Render(s, buf)
	wr := s.WindowRects()
	bx, by, bw, bh := wr[0][0], wr[0][1], wr[0][2], wr[0][3]
	found := false
	for y := by; y < by+bh && !found; y++ {
		for x := bx; x < bx+bw && !found; x++ {
			if buf[(y*tW+x)*4] < 0x40 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("minimized window button never inked any pixels")
	}
	bottom := int(buf[((by+bh-1)*tW+bx+2)*4])
	if bottom > 0xC0 {
		t.Fatalf("minimized window bottom stroke bright (%d) — should be raised (dark)", bottom)
	}
}

// ---- workspaces -----------------------------------------------------------

// New defaults: ActiveWorkspace=1, WorkspaceCount=4, label="1 of 4", and the
// pager reflects them (4 cells, cell 0 current).
func TestNewWorkspaceDefaults(t *testing.T) {
	s := New(tW, tH)
	if s.ActiveWorkspace != 1 {
		t.Fatalf("default ActiveWorkspace = %d, want 1", s.ActiveWorkspace)
	}
	if s.WorkspaceCount != 4 {
		t.Fatalf("default WorkspaceCount = %d, want 4", s.WorkspaceCount)
	}
	if s.Workspace != "1 of 4" {
		t.Fatalf("default Workspace label = %q, want %q", s.Workspace, "1 of 4")
	}
	if s.pager.Count != 4 {
		t.Fatalf("pager Count = %d, want 4", s.pager.Count)
	}
	if got := s.pager.Current().Get(); got != 0 {
		t.Fatalf("pager Current = %d, want 0", got)
	}
}

// SetActiveWorkspace clamps below + above the legal range, refreshes label + the
// pager highlight.
func TestSetActiveWorkspaceClampsAndUpdatesPager(t *testing.T) {
	s := New(tW, tH)
	s.SetActiveWorkspace(3)
	if s.ActiveWorkspace != 3 || s.Workspace != "3 of 4" {
		t.Fatalf("SetActiveWorkspace(3) = (%d,%q), want (3,%q)", s.ActiveWorkspace, s.Workspace, "3 of 4")
	}
	if got := s.pager.Current().Get(); got != 2 {
		t.Fatalf("pager Current after SetActiveWorkspace(3) = %d, want 2", got)
	}
	s.SetActiveWorkspace(0)
	if s.ActiveWorkspace != 1 {
		t.Fatalf("SetActiveWorkspace(0) ActiveWorkspace = %d, want 1", s.ActiveWorkspace)
	}
	s.SetActiveWorkspace(99)
	if s.ActiveWorkspace != s.WorkspaceCount {
		t.Fatalf("SetActiveWorkspace(99) ActiveWorkspace = %d, want %d", s.ActiveWorkspace, s.WorkspaceCount)
	}
	if got := s.pager.Current().Get(); got != s.WorkspaceCount-1 {
		t.Fatalf("pager Current after clamp-high = %d, want %d", got, s.WorkspaceCount-1)
	}
}

// SetWorkspaceCount keeps ActiveWorkspace coherent, re-renders the label + pager.
func TestSetWorkspaceCountRecomputes(t *testing.T) {
	s := New(tW, tH)
	s.SetActiveWorkspace(4)
	s.SetWorkspaceCount(2)
	if s.ActiveWorkspace != 2 || s.Workspace != "2 of 2" {
		t.Fatalf("after SetWorkspaceCount(2): (%d,%q), want (2,%q)", s.ActiveWorkspace, s.Workspace, "2 of 2")
	}
	if s.pager.Count != 2 {
		t.Fatalf("pager Count after SetWorkspaceCount(2) = %d, want 2", s.pager.Count)
	}
	if got := s.pager.Current().Get(); got != 1 {
		t.Fatalf("pager Current after count shrink = %d, want 1", got)
	}
	s.SetWorkspaceCount(0)
	if s.Workspace != "2" {
		t.Fatalf("WorkspaceCount=0 label = %q, want %q", s.Workspace, "2")
	}
	if s.pager.Count != 0 || s.pager.Occupied != nil {
		t.Fatalf("pager with count 0 should have 0 cells and nil occupancy")
	}
	s.SetWorkspaceCount(-1)
	if s.WorkspaceCount != 0 {
		t.Fatalf("WorkspaceCount after -1 = %d, want 0", s.WorkspaceCount)
	}
	s2 := New(tW, tH)
	s2.ActiveWorkspace = 0
	s2.SetWorkspaceCount(4)
	if s2.ActiveWorkspace != 1 {
		t.Fatalf("SetWorkspaceCount bump: ActiveWorkspace = %d, want 1", s2.ActiveWorkspace)
	}
}

// computeOccupied lights the cell a window sits on, folds an unset/out-of-range
// workspace onto the active one, and yields nil for a non-positive count.
func TestComputeOccupied(t *testing.T) {
	s := New(tW, tH)
	if occ := s.computeOccupied(0); occ != nil {
		t.Fatalf("computeOccupied(0) = %v, want nil", occ)
	}
	s.SetActiveWorkspace(2)
	s.SetWindows([]Window{
		{Id: 1, Title: "on-3", Workspace: 3},  // explicit workspace 3
		{Id: 2, Title: "unset", Workspace: 0}, // folds onto active (2)
		{Id: 3, Title: "oob", Workspace: 99},  // out of range -> active (2)
	})
	occ := s.computeOccupied(4)
	want := []bool{false, true, true, false} // ws2 (active) + ws3
	if len(occ) != 4 {
		t.Fatalf("computeOccupied(4) len = %d, want 4", len(occ))
	}
	for i := range want {
		if occ[i] != want[i] {
			t.Fatalf("computeOccupied[%d] = %v, want %v (%v)", i, occ[i], want[i], occ)
		}
	}
	// The pager picked the occupancy up through applyItems.
	if s.pager.Occupied[2] != true {
		t.Fatalf("pager Occupied not synced from windows: %v", s.pager.Occupied)
	}
}

// syncPager clamps a corrupt numeric model defensively: a negative count yields
// zero cells, an active index below 1 pins the highlight to cell 0, and an
// active index past the count pins it to the last cell. These states are
// unreachable through the public setters (which pre-clamp), so they are driven
// on the model fields directly.
func TestSyncPagerClamps(t *testing.T) {
	s := New(tW, tH)
	s.WorkspaceCount = -2
	s.ActiveWorkspace = 1
	s.syncPager()
	if s.pager.Count != 0 {
		t.Fatalf("syncPager negative count -> pager Count %d, want 0", s.pager.Count)
	}
	s.WorkspaceCount = 3
	s.ActiveWorkspace = 0
	s.syncPager()
	if got := s.pager.Current().Get(); got != 0 {
		t.Fatalf("syncPager active<1 -> Current %d, want 0", got)
	}
	s.WorkspaceCount = 3
	s.ActiveWorkspace = 10
	s.syncPager()
	if got := s.pager.Current().Get(); got != 2 {
		t.Fatalf("syncPager active>count -> Current %d, want 2 (count-1)", got)
	}
}

// NextWorkspace + PrevWorkspace wrap at the boundaries.
func TestCycleWorkspaceWraps(t *testing.T) {
	s := New(tW, tH)
	if got := s.NextWorkspace(); got != 2 {
		t.Fatalf("NextWorkspace from 1/4 = %d, want 2", got)
	}
	s.SetActiveWorkspace(4)
	if got := s.NextWorkspace(); got != 1 {
		t.Fatalf("NextWorkspace from 4/4 = %d, want 1 (wrap)", got)
	}
	s.SetActiveWorkspace(1)
	if got := s.PrevWorkspace(); got != 4 {
		t.Fatalf("PrevWorkspace from 1/4 = %d, want 4 (wrap)", got)
	}
	s.SetActiveWorkspace(3)
	if got := s.PrevWorkspace(); got != 2 {
		t.Fatalf("PrevWorkspace from 3/4 = %d, want 2", got)
	}
}

// Non-positive count makes Next/Prev no-op (returns the current active).
func TestCycleWorkspaceNoCountIsNoop(t *testing.T) {
	s := New(tW, tH)
	s.SetWorkspaceCount(0)
	s.ActiveWorkspace = 7
	if got := s.NextWorkspace(); got != 7 {
		t.Fatalf("NextWorkspace with count=0 = %d, want 7", got)
	}
	if got := s.PrevWorkspace(); got != 7 {
		t.Fatalf("PrevWorkspace with count=0 = %d, want 7", got)
	}
}

// HitTestWorkspace identifies clicks on the left switcher zone (everything left
// of the iconbar) and rejects the iconbar, the clock and negative-x clicks.
func TestHitTestWorkspace(t *testing.T) {
	s := New(tW, tH)
	if !s.HitTestWorkspace(WorkspaceW/2, tH/2) {
		t.Fatalf("center of workspace zone not detected")
	}
	if s.HitTestWorkspace(WorkspaceW+10, tH/2) {
		t.Fatalf("iconbar click reported as workspace hit")
	}
	if s.HitTestWorkspace(tW-1, tH/2) {
		t.Fatalf("clock click reported as workspace hit")
	}
	if s.HitTestWorkspace(-5, tH/2) {
		t.Fatalf("negative-x click reported as workspace hit")
	}
	if s.HitTestWorkspace(WorkspaceW/2, tH+5) {
		t.Fatalf("below-bar click reported as workspace hit")
	}
}

// Render paints the workspace switcher distinctly when ActiveWorkspace changes —
// the highlighted cell moves, so the rendered pixels differ.
func TestRenderWorkspaceHighlightChanges(t *testing.T) {
	s := New(tW, tH)
	buf1 := newBuf(s)
	Render(s, buf1)
	s.SetActiveWorkspace(3)
	buf2 := newBuf(s)
	Render(s, buf2)
	if bytesEqual(buf1, buf2) {
		t.Fatalf("workspace highlight did not change between workspace 1 and 3")
	}
}

// itoa exercises the zero, negative and multi-digit branches.
func TestItoa(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{{0, "0"}, {1, "1"}, {9, "9"}, {10, "10"}, {1234, "1234"}, {-1, "-1"}, {-42, "-42"}}
	for _, c := range cases {
		if got := itoa(c.in); got != c.want {
			t.Fatalf("itoa(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// workspaceLabel drops the "of <count>" suffix when the count is non-positive.
func TestWorkspaceLabel(t *testing.T) {
	if got := workspaceLabel(2, 4); got != "2 of 4" {
		t.Fatalf("workspaceLabel(2,4) = %q, want %q", got, "2 of 4")
	}
	if got := workspaceLabel(3, 0); got != "3" {
		t.Fatalf("workspaceLabel(3,0) = %q, want %q", got, "3")
	}
}

// Window.Workspace round-trips through SetWindows.
func TestWindowCarriesWorkspaceField(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "x", Workspace: 2}})
	if got := s.Windows[0].Workspace; got != 2 {
		t.Fatalf("Window.Workspace = %d, want 2", got)
	}
}

// SetTheme swaps the active theme + the next Render call uses the new colours.
func TestSetTheme(t *testing.T) {
	s := New(tW, tH)
	orig := s.Theme.Border.Color
	custom := theme.Theme{}
	custom.Border.Color = theme.Color{0xAB, 0xCD, 0xEF}
	s.SetTheme(custom)
	if s.Theme.Border.Color == orig {
		t.Fatalf("SetTheme did not swap the theme: still %v", s.Theme.Border.Color)
	}
	if s.Theme.Border.Color != (theme.Color{0xAB, 0xCD, 0xEF}) {
		t.Fatalf("SetTheme stored = %v", s.Theme.Border.Color)
	}
}

// rgba maps a theme.Color to an opaque toolkit.RGBA.
func TestRGBA(t *testing.T) {
	got := rgba(theme.Color{0x12, 0x34, 0x56})
	if got.R != 0x12 || got.G != 0x34 || got.B != 0x56 || got.A != 0xFF {
		t.Fatalf("rgba = %+v, want {0x12,0x34,0x56,0xFF}", got)
	}
}

// SetApps swaps the launcher row and republishes the iconbar: the new launcher
// count is reflected in the exposed LauncherRects on the next read.
func TestSetApps(t *testing.T) {
	s := New(tW, tH)
	if got := len(s.LauncherRects()); got != 6 {
		t.Fatalf("default LauncherRects = %d, want 6", got)
	}
	s.SetApps([]App{
		{Id: "x", Glyph: GlyphTerminal, Label: "X"},
		{Id: "y", Glyph: GlyphCode, Label: "Y"},
	})
	if got := len(s.Apps); got != 2 {
		t.Fatalf("SetApps len = %d, want 2", got)
	}
	if got := len(s.LauncherRects()); got != 2 {
		t.Fatalf("LauncherRects after SetApps = %d, want 2", got)
	}
	s.SetApps(nil)
	if got := len(s.LauncherRects()); got != 0 {
		t.Fatalf("LauncherRects after SetApps(nil) = %d, want 0", got)
	}
}

// The widget tree is PERSISTENT: the DockPanel, its accessories and the border
// backdrop are the SAME objects across state changes and renders — the migration
// bound one tree to observables instead of rebuilding per frame.
func TestPersistentWidgetTree(t *testing.T) {
	s := New(tW, tH)
	panel, dock, pager, clk, ground, border := s.panel, s.dock, s.pager, s.clock, s.ground, s.border
	buf := newBuf(s)
	Render(s, buf)
	s.SetWindows([]Window{{Id: 1, Title: "a", Focused: true}})
	s.SetClock("10:20")
	s.SetActiveWorkspace(2)
	s.SetCursor(200, tH/2, true)
	Render(s, buf)
	if s.panel != panel || s.dock != dock || s.pager != pager || s.clock != clk {
		t.Fatalf("a panel/dock/pager/clock widget was rebuilt — not a persistent tree")
	}
	if s.ground != ground || s.border != border {
		t.Fatalf("a ground/border backdrop was rebuilt — not a persistent tree")
	}
	if len(s.WindowRects()) != 1 {
		t.Fatalf("persistent dock did not pick up SetWindows")
	}
}

// The top border is a toolkit.Backdrop (state.border) whose Fill tracks the
// theme's border colour through applyTheme.
func TestTopBorderBackdropTracksTheme(t *testing.T) {
	s := New(tW, tH)
	want := rgba(s.Theme.Border.Color)
	if s.border.Fill != want {
		t.Fatalf("border Fill = %+v, want %+v", s.border.Fill, want)
	}
	if !s.borderOn {
		t.Fatalf("border should be on with default Width=1")
	}
	th := s.Theme
	th.Border.Color = theme.Color{0x11, 0x22, 0x33}
	s.SetTheme(th)
	if s.border.Fill != (toolkit.RGB(0x11, 0x22, 0x33)) {
		t.Fatalf("border Fill did not track SetTheme: %+v", s.border.Fill)
	}
}

// A cursor inside the iconbar magnifies; a cursor off-surface or over the ends
// does not — applyCursor gates the AppDock swell on the dock's x-range.
func TestApplyCursorMagnifyGating(t *testing.T) {
	s := New(tW, tH)
	ix, _, iw, _ := s.IconbarRect()
	// Resting widths with the cursor parked outside.
	rest := s.LauncherRects()
	// Cursor over the workspace end: no magnification.
	s.SetCursor(WorkspaceW/2, tH/2, true)
	for i, r := range s.LauncherRects() {
		if r[2] != rest[i][2] {
			t.Fatalf("launcher[%d] magnified from a workspace-end hover", i)
		}
	}
	// Cursor inside the iconbar: at least one launcher swells past its rest width.
	s.SetCursor(ix+iw/2, tH/2, true)
	swelled := false
	for i, r := range s.LauncherRects() {
		if r[2] > rest[i][2] {
			swelled = true
			break
		}
	}
	if !swelled {
		t.Fatalf("no launcher swelled with the cursor inside the iconbar")
	}
	// Cursor inside x-range but flagged off-surface: no magnification.
	s.SetCursor(ix+iw/2, tH/2, false)
	for i, r := range s.LauncherRects() {
		if r[2] != rest[i][2] {
			t.Fatalf("launcher[%d] magnified while cursor off-surface", i)
		}
	}
}

// bytesEqual is a tiny []byte compare so the workspace render-change test
// does not pull in reflect.DeepEqual.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
