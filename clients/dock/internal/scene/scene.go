// SPDX-License-Identifier: BSD-3-Clause
//
// Package scene paints the wasmdock surface as a Fluxbox-style bottom
// toolbar: a full-width, 28-pixel-tall bevelled gray bar split into three
// sections that read left-to-right —
//
//   - a fixed-width workspace label on the left ("1" by default),
//   - an iconbar in the middle. Reading left-to-right inside the iconbar:
//     first the static LAUNCHERS (one button per known app —
//     terminal / editor / files / hello), then a 1-pixel separator, then
//     one button per OPEN WINDOW the compositor handed us via the
//     `windows_changed` input event. Window buttons render in three styles
//     that match Fluxbox semantics:
//       - focused window: sunken bevel + active-title background +
//         active-label ink — reads as the currently selected button;
//       - unfocused open window: raised bevel + inactive-title background
//         + active-label ink — reads as a normal button;
//       - minimized window: raised bevel + inactive-title background +
//         inactive-label ink + "[*] " accent prefix — reads as a folded
//         entry;
//     Left-clicking a window button posts a `focus` message to the
//     compositor (which raises + focuses it, restoring it first if it was
//     minimized); right-clicking posts a `close` message;
//   - a fixed-width clock ("HH:MM") on the right, kept in sync by a `tick`
//     event posted by the JS worker every 30 seconds.
//
// All section backgrounds + ink colours come from a theme.Theme value (see
// the sibling internal/theme package) so the toolbar honours the Openbox
// attribute hierarchy and a future themerc file could repaint it without
// touching this package.
//
// scene is pure Go (no syscall/js, no cgo) so it builds for any architecture
// and is unit-tested natively. The wasm main only hands it a byte slice to
// fill plus mouse coordinates + clock-tick strings; all layout, hit-testing
// and RGBA painting live here.
package scene

import "github.com/wasmdesk/wasmbox/clients/dock/internal/theme"

// App identifies one launchable application the iconbar offers. Id is the
// string sent to the compositor in a {type:"launch", app:Id} message; Glyph
// selects the built-in drawn icon (no external assets); Label is the short
// human-readable text painted to the right of the glyph inside the button.
type App struct {
	Id    string
	Glyph Glyph
	Label string
}

// Window identifies one open (or folded) compositor window the iconbar
// surfaces as a button. Id is the compositor's window id (echoed back in
// `focus` / `close` / `restore` messages); Title is the window title painted
// inside the button; Minimized is true iff the compositor reports the window
// as currently folded into the iconbar (rendered with a "[*]" prefix +
// inactive-label dim ink so the user reads it as a folded entry); Focused is
// true iff this is the keyboard-focused window (rendered with a sunken bevel
// + active-title background so the user reads it as the selected button).
// Role mirrors the compositor's role attribute — panels are filtered out
// server-side so Role is always "window" in practice, but the field is here
// so a future iconbar style for non-window roles needs no schema change.
type Window struct {
	Id        int    `json:"id"`
	Title     string `json:"title"`
	Minimized bool   `json:"minimized"`
	Focused   bool   `json:"focused"`
	Role      string `json:"role"`
}

// Glyph enumerates the built-in icon drawings.
type Glyph int

const (
	// GlyphTerminal draws a command prompt: ">" caret + underscore cursor.
	GlyphTerminal Glyph = iota
	// GlyphEditor draws a document with horizontal "text" lines.
	GlyphEditor
	// GlyphFiles draws a folder shape.
	GlyphFiles
	// GlyphHello draws a smile arc — the hello stub client's mark.
	GlyphHello
)

// Geometry constants, in surface pixels. The toolbar hugs the bottom of the
// surface; the surface itself is sized 1280 x BarHeight by the worker.
const (
	// BarHeight is the toolbar's vertical extent (and the surface height).
	BarHeight = 28
	// WorkspaceW is the fixed pixel width of the workspace section on the
	// left edge of the toolbar.
	WorkspaceW = 100
	// ClockW is the fixed pixel width of the clock section on the right
	// edge of the toolbar.
	ClockW = 80
	// IconbarButtonW is the resting pixel width of one iconbar button.
	IconbarButtonW = 120
	// IconbarButtonH is the inner height of an iconbar button (the toolbar
	// reserves 2px of vertical breathing room above + below).
	IconbarButtonH = 24
	// IconbarButtonGap is the horizontal spacing between adjacent buttons.
	IconbarButtonGap = 2
	// IconbarVPad is the vertical padding between the toolbar top/bottom
	// and the iconbar button row.
	IconbarVPad = 2
	// IconGlyphPx is the side length of the icon drawn inside a button.
	IconGlyphPx = 16
	// IconGlyphLeftPad is the gap between the button's left bevel and the
	// glyph.
	IconGlyphLeftPad = 4
	// IconLabelGap is the gap between the glyph and the start of the label
	// text.
	IconLabelGap = 4
	// SeparatorW is the horizontal width reserved between the static launcher
	// row and the dynamic open-window row. The separator is painted as a
	// 1-pixel dark line centered inside this gap so the user reads the two
	// sub-sections as distinct stripes.
	SeparatorW = 8
)

// State is the toolbar's mutable model: surface size, the static launcher
// row, the active open-window row (one button per non-panel window the
// compositor has open, including folded ones — flagged via Window.Minimized),
// the active workspace label, the current clock string, the cursor position
// (recorded for a future hover highlight; unused by the v0 paint pass) and
// the active Openbox-compatible Theme.
type State struct {
	W, H         int
	Apps         []App
	Windows      []Window
	Workspace    string
	Clock        string
	CursorX      int
	CursorY      int
	CursorInside bool
	Theme        theme.Theme
}

// DefaultApps is the built-in launcher set the iconbar ships with.
func DefaultApps() []App {
	return []App{
		{Id: "terminal", Glyph: GlyphTerminal, Label: "Terminal"},
		{Id: "editor", Glyph: GlyphEditor, Label: "Editor"},
		{Id: "files", Glyph: GlyphFiles, Label: "Files"},
		{Id: "hello", Glyph: GlyphHello, Label: "Hello"},
	}
}

// New makes a toolbar State for a surface of width × height pixels carrying
// the default launcher set + the default workspace label "1" + an empty
// clock string (the worker posts a tick on boot to fill it in) + the default
// Fluxbox-light theme. The cursor is parked outside the surface.
func New(width, height int) *State {
	return &State{
		W:         width,
		H:         height,
		Apps:      DefaultApps(),
		Workspace: "1",
		Clock:     "",
		Theme:     theme.DefaultFluxboxLight(),
	}
}

// SetCursor records the cursor position and whether it is over the surface.
func (s *State) SetCursor(x, y int, inside bool) {
	s.CursorX = x
	s.CursorY = y
	s.CursorInside = inside
}

// SetClock records the latest "HH:MM" clock string posted by the worker.
func (s *State) SetClock(t string) { s.Clock = t }

// SetWorkspace records the active workspace label ("1", "2", ...).
func (s *State) SetWorkspace(w string) { s.Workspace = w }

// SetWindows replaces the open-window list (open + minimized, flagged via
// Window.Minimized). The slice is stored directly (callers must not mutate
// it after the call); the caller is the compositor's `windows_changed` event
// handler, which posts a fresh list on every change (new window, close,
// minimize, restore, focus shift, title rename).
func (s *State) SetWindows(ws []Window) { s.Windows = ws }

// ---- section geometry ----------------------------------------------------

// WorkspaceRect returns the workspace section rectangle.
func (s *State) WorkspaceRect() (x, y, w, h int) {
	return 0, 0, WorkspaceW, s.H
}

// ClockRect returns the clock section rectangle.
func (s *State) ClockRect() (x, y, w, h int) {
	return s.W - ClockW, 0, ClockW, s.H
}

// IconbarRect returns the iconbar (middle) section rectangle, expanding to
// fill the gap between the workspace label and the clock.
func (s *State) IconbarRect() (x, y, w, h int) {
	x = WorkspaceW
	w = s.W - WorkspaceW - ClockW
	if w < 0 {
		w = 0
	}
	return x, 0, w, s.H
}

// IconbarButtonRect returns the rectangle (in surface coordinates) of the
// i-th iconbar button. Buttons sit one after another, left-to-right,
// separated by IconbarButtonGap. The button height scales to fill the
// toolbar (s.H - 2*IconbarVPad) so the toolbar reads cleanly at any
// granted surface height (the compositor floors panel heights at
// Theme::MIN_H = 60).
func (s *State) IconbarButtonRect(i int) (x, y, w, h int) {
	bx, _, _, _ := s.IconbarRect()
	x = bx + i*(IconbarButtonW+IconbarButtonGap)
	y = IconbarVPad
	w = IconbarButtonW
	h = s.H - 2*IconbarVPad
	if h < 1 {
		h = 1
	}
	return
}

// WindowButtonRect returns the rectangle (in surface coordinates) of the
// i-th open-window button. The open-window row sits AFTER the launcher row
// in the same iconbar, separated by SeparatorW pixels of gap so the user
// reads the two sub-sections as distinct stripes. Same width / gap / height
// rules as the launcher buttons. The first window button starts at
// (last_launcher_right + SeparatorW); subsequent buttons cascade with
// IconbarButtonGap between them.
func (s *State) WindowButtonRect(i int) (x, y, w, h int) {
	bx, _, _, _ := s.IconbarRect()
	// Anchor past the last launcher slot's right edge (NOT including the
	// trailing IconbarButtonGap — the SeparatorW replaces it).
	lastLauncherRight := bx + len(s.Apps)*(IconbarButtonW+IconbarButtonGap) - IconbarButtonGap
	if len(s.Apps) == 0 {
		lastLauncherRight = bx - SeparatorW // empty launcher row: window row starts at iconbar left
	}
	x = lastLauncherRight + SeparatorW + i*(IconbarButtonW+IconbarButtonGap)
	y = IconbarVPad
	w = IconbarButtonW
	h = s.H - 2*IconbarVPad
	if h < 1 {
		h = 1
	}
	return
}

// HitTest returns the iconbar-button index under (x, y) in surface
// coordinates, or -1 if (x, y) does not fall inside any LAUNCHER button.
// Clicks on the workspace label or the clock are intentionally inert in v0.
// Use HitTestWindow to probe the open-window buttons (which sit to the right
// of the launcher row, past a SeparatorW gap).
func (s *State) HitTest(x, y int) int {
	// Reject anything outside the iconbar's horizontal range up front so a
	// click on the workspace label / clock never matches.
	ix, _, iw, _ := s.IconbarRect()
	if x < ix || x >= ix+iw {
		return -1
	}
	for i := range s.Apps {
		bx, by, bw, bh := s.IconbarButtonRect(i)
		if x >= bx && x < bx+bw && y >= by && y < by+bh {
			// The button might overflow the iconbar's right edge when the
			// surface is narrow; only count the click if the button start
			// is still inside the iconbar.
			if bx >= ix && bx < ix+iw {
				return i
			}
		}
	}
	return -1
}

// HitTestWindow returns the open-window button index under (x, y) in surface
// coordinates, or -1 if (x, y) does not fall inside any window button. The
// open-window row sits past the launcher row + SeparatorW gap in the same
// iconbar; we reject anything outside the iconbar's horizontal range and
// skip window buttons whose anchor falls past the iconbar's right edge (very
// narrow surface fallback — the iconbar paints what fits, the rest is
// dropped).
func (s *State) HitTestWindow(x, y int) int {
	ix, _, iw, _ := s.IconbarRect()
	if x < ix || x >= ix+iw {
		return -1
	}
	for i := range s.Windows {
		bx, by, bw, bh := s.WindowButtonRect(i)
		if bx >= ix+iw {
			return -1
		}
		if x >= bx && x < bx+bw && y >= by && y < by+bh {
			if bx >= ix && bx < ix+iw {
				return i
			}
		}
	}
	return -1
}

// ---- painting ------------------------------------------------------------

// Render fills buf (a 4*W*H byte slice, RGBA32 row-major) with the toolbar
// at the current state. buf must be exactly the right size or Render panics
// (a size mismatch in the caller is a bug). The whole surface is opaque —
// the toolbar paints every pixel from edge to edge.
func Render(s *State, buf []byte) {
	need := 4 * s.W * s.H
	if len(buf) != need {
		panic("scene: buffer size mismatch")
	}
	// Workspace section — inactive-title look.
	wx, _, ww, _ := s.WorkspaceRect()
	bg := s.Theme.Window.Inactive.Title.Bg
	theme.PaintGradient(buf, s.W, s.H, wx, 0, ww, s.H, bg.Gradient, bg.Color, bg.ColorTo)
	drawBevel(s, buf, wx, 0, ww, s.H)
	drawText(s, buf, s.Workspace, wx+(ww-textWidth(s.Workspace))/2, (s.H-glyphHeight)/2, s.Theme.Window.Inactive.Title.Label.Color)

	// Iconbar section background — fill the gap behind the buttons with the
	// active-title look (then the buttons are painted on top).
	ix, _, iw, _ := s.IconbarRect()
	abg := s.Theme.Window.Active.Title.Bg
	theme.PaintGradient(buf, s.W, s.H, ix, 0, iw, s.H, abg.Gradient, abg.Color, abg.ColorTo)
	drawBevel(s, buf, ix, 0, iw, s.H)

	for i, app := range s.Apps {
		bx, by, bw, bh := s.IconbarButtonRect(i)
		// Skip buttons whose anchor falls outside the iconbar (very narrow
		// surface fallback).
		if bx >= ix+iw {
			break
		}
		// Clip the right edge of the last button to the iconbar's right
		// (bx < ix+iw is guaranteed by the break above, so cw stays > 0).
		cw := bw
		if bx+cw > ix+iw {
			cw = ix + iw - bx
		}
		drawIconbarButton(s, buf, bx, by, cw, bh, app)
	}

	// Separator between the static launcher row and the dynamic open-window
	// row: a 1-pixel-wide dark vertical line centered inside the SeparatorW
	// gap. Skipped entirely when no launchers exist (empty Apps).
	if len(s.Apps) > 0 {
		sepRight := ix + len(s.Apps)*(IconbarButtonW+IconbarButtonGap) - IconbarButtonGap + SeparatorW
		sepX := sepRight - SeparatorW/2 - 1
		if sepX >= ix && sepX < ix+iw {
			sepInk := [3]uint8{0x40, 0x40, 0x40}
			for jj := IconbarVPad; jj < s.H-IconbarVPad; jj++ {
				setPixel(s, buf, sepX, jj, sepInk)
			}
		}
	}

	// Open-window row — one button per non-panel compositor window (open +
	// minimized). Painted past the SeparatorW gap so the user reads the
	// launcher row + the window row as two distinct iconbar stripes. Each
	// button picks one of three styles via drawWindowButton:
	//   - focused: sunken bevel + active title bg + active label ink
	//   - unfocused open: raised bevel + inactive title bg + active label ink
	//   - minimized: raised bevel + inactive title bg + inactive (dim)
	//     label ink + "[*] " accent prefix
	for i, win := range s.Windows {
		bx, by, bw, bh := s.WindowButtonRect(i)
		if bx >= ix+iw {
			break
		}
		cw := bw
		if bx+cw > ix+iw {
			cw = ix + iw - bx
		}
		drawWindowButton(s, buf, bx, by, cw, bh, win)
	}

	// Clock section — OSD look.
	cx, _, cw, _ := s.ClockRect()
	obg := s.Theme.Osd.Bg
	theme.PaintGradient(buf, s.W, s.H, cx, 0, cw, s.H, obg.Gradient, obg.Color, obg.ColorTo)
	drawBevel(s, buf, cx, 0, cw, s.H)
	clock := s.Clock
	if clock == "" {
		clock = "--:--"
	}
	drawText(s, buf, clock, cx+(cw-textWidth(clock))/2, (s.H-glyphHeight)/2, s.Theme.Osd.Label.Color)

	// Outer border on the very top edge of the toolbar (the bottom edge sits
	// at the bottom of the canvas, so a bottom border is not visible). One
	// pixel of theme.Border.Color spanning the full surface width.
	if s.Theme.Border.Width > 0 {
		for x := 0; x < s.W; x++ {
			setPixel(s, buf, x, 0, [3]uint8(s.Theme.Border.Color))
		}
	}
}

// drawBevel paints a 1-pixel raised bevel around the (x, y, w, h) section: a
// bright top + left, a dark bottom + right. The bevel highlights are drawn
// in pure white / pure black so they read clearly against any gradient face.
func drawBevel(s *State, buf []byte, x, y, w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	hi := [3]uint8{0xFF, 0xFF, 0xFF}
	lo := [3]uint8{0x40, 0x40, 0x40}
	for i := 0; i < w; i++ {
		setPixel(s, buf, x+i, y, hi)
		setPixel(s, buf, x+i, y+h-1, lo)
	}
	for j := 0; j < h; j++ {
		setPixel(s, buf, x, y+j, hi)
		setPixel(s, buf, x+w-1, y+j, lo)
	}
}

// drawSunkenBevel paints a 1-pixel sunken bevel around the (x, y, w, h)
// section: dark top + left, bright bottom + right — the inverse of drawBevel.
// Used to mark the focused open-window button so the user reads it as the
// currently selected one (a Fluxbox-style "pressed" look).
func drawSunkenBevel(s *State, buf []byte, x, y, w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	hi := [3]uint8{0xFF, 0xFF, 0xFF}
	lo := [3]uint8{0x40, 0x40, 0x40}
	for i := 0; i < w; i++ {
		setPixel(s, buf, x+i, y, lo)
		setPixel(s, buf, x+i, y+h-1, hi)
	}
	for j := 0; j < h; j++ {
		setPixel(s, buf, x, y+j, lo)
		setPixel(s, buf, x+w-1, y+j, hi)
	}
}

// drawWindowButton paints a single open-window iconbar button. The look
// follows Fluxbox-style semantics chosen from the three Openbox theme states
// the toolbar can render:
//
//   - Focused window: sunken bevel + active.title background gradient +
//     active.label ink — the "this is the current window" look.
//   - Unfocused open window: raised bevel + inactive.title background +
//     active.label ink — the "another open window, click to focus" look.
//   - Minimized window: raised bevel + inactive.title background +
//     inactive.label (dimmer) ink + "[*] " accent prefix on the label — the
//     "this window is folded into the iconbar" look.
//
// Painted at the launcher-row offset past the SeparatorW gap so the user
// reads it as a separate stripe from the static launchers.
func drawWindowButton(s *State, buf []byte, x, y, w, h int, win Window) {
	var bg theme.Bg
	var ink theme.Color
	label := win.Title
	if win.Focused {
		bg = s.Theme.Window.Active.Title.Bg
		ink = s.Theme.Window.Active.Title.Label.Color
	} else if win.Minimized {
		bg = s.Theme.Window.Inactive.Title.Bg
		ink = s.Theme.Window.Inactive.Title.Label.Color
		label = "[*] " + label
	} else {
		bg = s.Theme.Window.Inactive.Title.Bg
		ink = s.Theme.Window.Active.Title.Label.Color
	}
	theme.PaintGradient(buf, s.W, s.H, x, y, w, h, bg.Gradient, bg.Color, bg.ColorTo)
	if win.Focused {
		drawSunkenBevel(s, buf, x, y, w, h)
	} else {
		drawBevel(s, buf, x, y, w, h)
	}
	tx := x + IconGlyphLeftPad
	ty := y + (h-glyphHeight)/2
	max := x + w - tx - 2
	drawTextClipped(s, buf, label, tx, ty, ink, max)
}

// drawIconbarButton paints a single iconbar button (bevelled face + glyph +
// truncated label).
func drawIconbarButton(s *State, buf []byte, x, y, w, h int, app App) {
	bg := s.Theme.Window.Inactive.Title.Bg
	theme.PaintGradient(buf, s.W, s.H, x, y, w, h, bg.Gradient, bg.Color, bg.ColorTo)
	drawBevel(s, buf, x, y, w, h)
	// Glyph at the left.
	gy := y + (h-IconGlyphPx)/2
	gx := x + IconGlyphLeftPad
	drawGlyph(s, buf, app.Glyph, gx, gy, IconGlyphPx, IconGlyphPx)
	// Label to the right of the glyph, truncated to the remaining width.
	tx := gx + IconGlyphPx + IconLabelGap
	ty := y + (h-glyphHeight)/2
	max := x + w - tx - 2
	drawTextClipped(s, buf, app.Label, tx, ty, s.Theme.Window.Active.Title.Label.Color, max)
}

// ---- glyph drawing ------------------------------------------------------

// drawGlyph paints one of the built-in icon marks into (x, y, w, h).
func drawGlyph(s *State, buf []byte, g Glyph, x, y, w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	ink := [3]uint8{0x1a, 0x1a, 0x1a}
	switch g {
	case GlyphTerminal:
		drawGlyphTerminal(s, buf, x, y, w, h, ink)
	case GlyphEditor:
		drawGlyphEditor(s, buf, x, y, w, h, ink)
	case GlyphFiles:
		drawGlyphFiles(s, buf, x, y, w, h, ink)
	case GlyphHello:
		drawGlyphHello(s, buf, x, y, w, h, ink)
	default:
		// Unknown glyph: paint a solid square so the slot is still visible.
		for j := 0; j < h; j++ {
			for i := 0; i < w; i++ {
				setPixel(s, buf, x+i, y+j, ink)
			}
		}
	}
}

func drawGlyphTerminal(s *State, buf []byte, x, y, w, h int, ink [3]uint8) {
	// ">" caret + underscore cursor inside the (w x h) box.
	cx := x + w*2/5
	cy := y + h/2
	arm := h / 4
	for t := 0; t <= arm; t++ {
		setPixel(s, buf, cx-arm+t, cy-arm+t, ink)
		setPixel(s, buf, cx-arm+t, cy+arm-t, ink)
	}
	uy := y + h*3/4
	for ux := x + 2; ux < x+w-2; ux++ {
		setPixel(s, buf, ux, uy, ink)
	}
}

func drawGlyphEditor(s *State, buf []byte, x, y, w, h int, ink [3]uint8) {
	for i := 0; i < 3; i++ {
		ly := y + 3 + i*((h-6)/3+1)
		end := x + w - 2
		if i == 2 {
			end = x + w*3/4
		}
		for lx := x + 2; lx < end; lx++ {
			setPixel(s, buf, lx, ly, ink)
		}
	}
}

func drawGlyphFiles(s *State, buf []byte, x, y, w, h int, ink [3]uint8) {
	fx0 := x + 1
	fx1 := x + w - 2
	fy0 := y + h/2
	fy1 := y + h - 2
	for fy := fy0; fy <= fy1; fy++ {
		for fx := fx0; fx <= fx1; fx++ {
			if fy == fy0 || fy == fy1 || fx == fx0 || fx == fx1 {
				setPixel(s, buf, fx, fy, ink)
			}
		}
	}
	// Folder tab.
	for fx := fx0; fx < fx0+w/3; fx++ {
		setPixel(s, buf, fx, fy0-1, ink)
	}
}

func drawGlyphHello(s *State, buf []byte, x, y, w, h int, ink [3]uint8) {
	// Smile arc: bottom half of a "circle" inside the box.
	cx := x + w/2
	cy := y + h/2
	r := w / 2
	if h/2 < r {
		r = h / 2
	}
	for i := -r; i <= r; i++ {
		setPixel(s, buf, cx+i, cy+(r-abs(i))/2, ink)
	}
	// Two eyes.
	setPixel(s, buf, cx-r/2, cy-r/2, ink)
	setPixel(s, buf, cx+r/2, cy-r/2, ink)
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}

// ---- bitmap font --------------------------------------------------------

// glyphHeight is the per-character vertical extent of the bitmap font. The
// font is intentionally tiny so the labels fit inside a 24-px-tall iconbar
// button (a bitmap line-height matches IconGlyphPx).
const glyphHeight = 7

// font5x7 is a sparse 5-column x 7-row bitmap font. Each glyph is one row of
// 5 bytes whose low 5 bits encode one column from top to bottom — bit 0 =
// top row, bit 6 = bottom row. Only the characters used by the toolbar
// labels + clock + workspace name are populated; an unknown char prints as
// a blank.
var font5x7 = map[byte][5]byte{
	'0': {0x3E, 0x51, 0x49, 0x45, 0x3E},
	'1': {0x00, 0x42, 0x7F, 0x40, 0x00},
	'2': {0x42, 0x61, 0x51, 0x49, 0x46},
	'3': {0x21, 0x41, 0x45, 0x4B, 0x31},
	'4': {0x18, 0x14, 0x12, 0x7F, 0x10},
	'5': {0x27, 0x45, 0x45, 0x45, 0x39},
	'6': {0x3C, 0x4A, 0x49, 0x49, 0x30},
	'7': {0x01, 0x71, 0x09, 0x05, 0x03},
	'8': {0x36, 0x49, 0x49, 0x49, 0x36},
	'9': {0x06, 0x49, 0x49, 0x29, 0x1E},
	':': {0x00, 0x36, 0x36, 0x00, 0x00},
	'-': {0x08, 0x08, 0x08, 0x08, 0x08},
	' ': {0x00, 0x00, 0x00, 0x00, 0x00},
	'.': {0x00, 0x60, 0x60, 0x00, 0x00},
	'A': {0x7E, 0x11, 0x11, 0x11, 0x7E},
	'B': {0x7F, 0x49, 0x49, 0x49, 0x36},
	'C': {0x3E, 0x41, 0x41, 0x41, 0x22},
	'D': {0x7F, 0x41, 0x41, 0x22, 0x1C},
	'E': {0x7F, 0x49, 0x49, 0x49, 0x41},
	'F': {0x7F, 0x09, 0x09, 0x09, 0x01},
	'G': {0x3E, 0x41, 0x49, 0x49, 0x7A},
	'H': {0x7F, 0x08, 0x08, 0x08, 0x7F},
	'I': {0x00, 0x41, 0x7F, 0x41, 0x00},
	'L': {0x7F, 0x40, 0x40, 0x40, 0x40},
	'M': {0x7F, 0x02, 0x0C, 0x02, 0x7F},
	'N': {0x7F, 0x04, 0x08, 0x10, 0x7F},
	'O': {0x3E, 0x41, 0x41, 0x41, 0x3E},
	'P': {0x7F, 0x09, 0x09, 0x09, 0x06},
	'R': {0x7F, 0x09, 0x19, 0x29, 0x46},
	'S': {0x46, 0x49, 0x49, 0x49, 0x31},
	'T': {0x01, 0x01, 0x7F, 0x01, 0x01},
	'U': {0x3F, 0x40, 0x40, 0x40, 0x3F},
	'V': {0x1F, 0x20, 0x40, 0x20, 0x1F},
	'W': {0x7F, 0x20, 0x18, 0x20, 0x7F},
	'X': {0x63, 0x14, 0x08, 0x14, 0x63},
	'Y': {0x07, 0x08, 0x70, 0x08, 0x07},
	'a': {0x20, 0x54, 0x54, 0x54, 0x78},
	'b': {0x7F, 0x48, 0x44, 0x44, 0x38},
	'c': {0x38, 0x44, 0x44, 0x44, 0x20},
	'd': {0x38, 0x44, 0x44, 0x48, 0x7F},
	'e': {0x38, 0x54, 0x54, 0x54, 0x18},
	'f': {0x08, 0x7E, 0x09, 0x01, 0x02},
	'g': {0x0C, 0x52, 0x52, 0x52, 0x3E},
	'h': {0x7F, 0x08, 0x04, 0x04, 0x78},
	'i': {0x00, 0x44, 0x7D, 0x40, 0x00},
	'l': {0x00, 0x41, 0x7F, 0x40, 0x00},
	'm': {0x7C, 0x04, 0x18, 0x04, 0x78},
	'n': {0x7C, 0x08, 0x04, 0x04, 0x78},
	'o': {0x38, 0x44, 0x44, 0x44, 0x38},
	'p': {0x7C, 0x14, 0x14, 0x14, 0x08},
	'r': {0x7C, 0x08, 0x04, 0x04, 0x08},
	's': {0x48, 0x54, 0x54, 0x54, 0x20},
	't': {0x04, 0x3F, 0x44, 0x40, 0x20},
	'u': {0x3C, 0x40, 0x40, 0x20, 0x7C},
	'v': {0x1C, 0x20, 0x40, 0x20, 0x1C},
	'w': {0x3C, 0x40, 0x30, 0x40, 0x3C},
	'x': {0x44, 0x28, 0x10, 0x28, 0x44},
	'y': {0x0C, 0x50, 0x50, 0x50, 0x3C},
	'z': {0x44, 0x64, 0x54, 0x4C, 0x44},
}

// charWidth returns the width in pixels of a single character (5 columns
// + 1 pixel inter-glyph gap). Unknown characters consume the same slot so
// the layout stays stable.
const charWidth = 6

func textWidth(s string) int { return charWidth * len(s) }

// drawText paints s into buf at (x, y) using the bitmap font, in ink colour
// c. y is the top edge of the glyph row.
func drawText(s *State, buf []byte, txt string, x, y int, c theme.Color) {
	drawTextClipped(s, buf, txt, x, y, c, 1<<30)
}

// drawTextClipped paints s at (x, y) in ink c but stops as soon as the next
// glyph would extend past maxRight (relative to x — so the call painted at
// most maxRight pixels wide). Used by the iconbar to truncate long labels.
func drawTextClipped(s *State, buf []byte, txt string, x, y int, c theme.Color, maxWidth int) {
	if maxWidth <= 0 {
		return
	}
	ink := [3]uint8{c[0], c[1], c[2]}
	for k := 0; k < len(txt); k++ {
		if (k+1)*charWidth > maxWidth {
			return
		}
		ch := txt[k]
		bits, ok := font5x7[ch]
		if !ok {
			continue
		}
		for col := 0; col < 5; col++ {
			cb := bits[col]
			for row := 0; row < glyphHeight; row++ {
				if cb&(1<<row) != 0 {
					setPixel(s, buf, x+k*charWidth+col, y+row, ink)
				}
			}
		}
	}
}

// ---- pixel I/O ----------------------------------------------------------

// setPixel writes an opaque RGB at (x, y) if in bounds. Alpha is forced to
// 0xFF (the toolbar is fully opaque).
func setPixel(s *State, buf []byte, x, y int, c [3]uint8) {
	if x < 0 || y < 0 || x >= s.W || y >= s.H {
		return
	}
	off := (y*s.W + x) * 4
	buf[off] = c[0]
	buf[off+1] = c[1]
	buf[off+2] = c[2]
	buf[off+3] = 0xFF
}
