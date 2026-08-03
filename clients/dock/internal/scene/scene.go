// SPDX-License-Identifier: BSD-3-Clause
//
// Package scene paints the wasmdock surface as a Fluxbox-style bottom
// toolbar: a full-width, 28-pixel-tall bevelled gray bar split into three
// sections that read left-to-right —
//
//   - a fixed-width workspace section on the left rendering the active
//     workspace as "<active> of <count>" (default "1 of 4"). Left-clicking
//     the section cycles to the next workspace; scroll-wheel up/down
//     cycles backward/forward. The active workspace is reported by the
//     compositor through the `workspace_changed` input event (kind:
//     "workspace_changed", payload {active:int, count:int});
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
// # Toolkit widget model
//
// The bar is composed as a go-widgets/toolkit widget tree rather than a
// sequence of hand-drawn rects: the shell is a toolkit.HBox with two
// fixed-width ends (the workspace label + the clock) and a flex iconbar in
// the middle, exactly mirroring the three-section geometry. The workspace /
// clock ends are `section` leaf widgets; the iconbar is a custom container
// that owns one `launcherButton` leaf per app and one `windowButton` leaf per
// open window (the three focus / minimized styles are Draw branches). Every
// leaf embeds toolkit.Base and paints through the painter.Painter primitive
// set; text is drawn with the toolkit's active font (toolkit.DrawText) —
// anti-aliased, shaped OpenType after New calls toolkit.UseOpenTypeText (v0.77.0)
// — instead of a private 5×7 table. The Fluxbox bevel /
// gradient section chrome has no stock equivalent, so it is kept as custom
// Draw over the painter (paintBg / drawBevel), and the app glyphs that are
// bespoke Fluxbox marks (terminal / hello / code / loom) stay custom Draw
// while the two that map cleanly onto stock artwork reuse the toolkit icon
// library (editor → DrawIconNew, files → DrawIconOpen).
//
// The State value remains the single source of truth for layout: the pure
// *Rect / HitTest* geometry methods are unchanged, so the wasm main's
// button/wheel-aware event dispatch and the __wasmdockGeometry probe hook
// (which reads WindowButtonRect) keep working byte-for-byte. Render rebuilds
// the (cheap) widget view from the current State each frame, so a direct
// field mutation is always reflected.
//
// scene is pure Go (no syscall/js, no cgo) so it builds for any architecture
// and is unit-tested natively. The wasm main only hands it a byte slice to
// fill plus mouse coordinates + clock-tick strings; all layout, hit-testing
// and RGBA painting live here.
package scene

import (
	"sync"

	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
	"github.com/wasmdesk/wasmbox/clients/dock/internal/theme"
)

// aaOnce flips the toolkit's active font to anti-aliased, shaped OpenType text
// exactly once for this client process. It is package-scoped because SetFont is
// a process-global; a single opt-in matches the toolkit's "flip it once at
// start-up" contract.
var aaOnce sync.Once

// enableAAText installs the toolkit's bundled AA/shaped OpenType face (Atkinson
// Hyperlegible @16px, toolkit v0.77.0), so the workspace label, launcher/window
// button labels and clock render as the shaped vector face. The bundled face
// never fails to parse (the error is documented as never-returned); on the
// impossible error path the toolkit leaves the still-working bitmap default
// active, so a swallowed error degrades to legible bitmap text, never to none.
func enableAAText() { aaOnce.Do(func() { _ = toolkit.UseOpenTypeText() }) }

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
	// Workspace mirrors the compositor's per-window workspace assignment
	// (1..WORKSPACE_COUNT). The compositor's windows_snapshot already
	// filters to the active workspace, so in v0 every entry sent over the
	// wire has Workspace == State.ActiveWorkspace — but the field is here
	// so a future "show all workspaces" view (e.g. a pager) needs no
	// schema change.
	Workspace int `json:"workspace"`
	// App is the launcher id the window was spawned from (e.g. "terminal").
	// Forward-compatible: the compositor's windows_snapshot does not send it
	// yet, so it rides the payload the moment it does; until then the running
	// / focused-app indicators fall back to matching the window Title against
	// a launcher Label (see appIndexForWindow).
	App string `json:"app"`
}

// Glyph enumerates the built-in icon drawings.
type Glyph int

const (
	// GlyphTerminal draws a command prompt: ">" caret + underscore cursor.
	GlyphTerminal Glyph = iota
	// GlyphEditor draws a document with a folded corner (toolkit stock New icon).
	GlyphEditor
	// GlyphFiles draws a folder shape (toolkit stock Open icon).
	GlyphFiles
	// GlyphHello draws a smile arc — the hello stub client's mark.
	GlyphHello
	// GlyphCode draws angle brackets "< >" — used by the vscode launcher.
	GlyphCode
	// GlyphLoom draws a 4-strand weave mark — used by the loom launcher.
	GlyphLoom
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
// the active workspace + workspace count (numeric model — Workspace string
// derives from them in Render), the current clock string, the cursor
// position (recorded for a future hover highlight; unused by the v0 paint
// pass) and the active Openbox-compatible Theme.
//
// ActiveWorkspace defaults to 1 and WorkspaceCount to 4 (matches the
// compositor's WORKSPACE_COUNT constant). The compositor pushes a
// `workspace_changed` input event on every switch carrying both fields, so
// the dock can render the bar without ever holding a stale value.
type State struct {
	W, H            int
	Apps            []App
	Windows         []Window
	ActiveWorkspace int
	WorkspaceCount  int
	Workspace       string // legacy display-only label; SetWorkspace fills it
	Clock           string
	CursorX         int
	CursorY         int
	CursorInside    bool
	Theme           theme.Theme
	// Magnify configures the macOS-style hover magnification (see magnify.go).
	Magnify Magnify
	// badges holds the per-launcher attention-badge counts, keyed by app id
	// (see indicators.go). nil until the first SetBadge; read via BadgeCount.
	badges map[string]int
}

// DefaultApps is the built-in launcher set the iconbar ships with.
func DefaultApps() []App {
	return []App{
		{Id: "terminal", Glyph: GlyphTerminal, Label: "Terminal"},
		{Id: "editor", Glyph: GlyphEditor, Label: "Editor"},
		{Id: "files", Glyph: GlyphFiles, Label: "Files"},
		{Id: "hello", Glyph: GlyphHello, Label: "Hello"},
		{Id: "vscode", Glyph: GlyphCode, Label: "VS Code"},
		{Id: "loom", Glyph: GlyphLoom, Label: "Loom"},
	}
}

// New makes a toolbar State for a surface of width × height pixels carrying
// the default launcher set + the default active workspace (1 of 4) + an
// empty clock string (the worker posts a tick on boot to fill it in) + the
// default Fluxbox-light theme. The cursor is parked outside the surface.
func New(width, height int) *State {
	enableAAText() // labels/clock render with the AA/shaped OpenType face.
	s := &State{
		W:               width,
		H:               height,
		Apps:            DefaultApps(),
		ActiveWorkspace: 1,
		WorkspaceCount:  4,
		Clock:           "",
		Theme:           theme.DefaultFluxboxLight(),
		Magnify:         DefaultMagnify(),
	}
	s.Workspace = workspaceLabel(s.ActiveWorkspace, s.WorkspaceCount)
	return s
}

// workspaceLabel formats the active/count pair as the text the bar renders.
// "1 of 4" is the chosen form: it reads like Fluxbox's "Workspace 1" but
// also surfaces the total count so the user knows how many slots cycle.
func workspaceLabel(active, count int) string {
	if count <= 0 {
		return itoa(active)
	}
	return itoa(active) + " of " + itoa(count)
}

// itoa is a tiny base-10 formatter for small non-negative ints. Keeping the
// dock's own formatter (instead of strconv.Itoa) keeps the wasm payload lean.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// SetCursor records the cursor position and whether it is over the surface.
func (s *State) SetCursor(x, y int, inside bool) {
	s.CursorX = x
	s.CursorY = y
	s.CursorInside = inside
}

// SetClock records the latest "HH:MM" clock string posted by the worker.
func (s *State) SetClock(t string) { s.Clock = t }

// SetTheme swaps in a new Openbox-compatible theme. The next Render call
// repaints every section with the new colours / gradients. Pure data; the
// caller (dock main.go) decides when to trigger a repaint.
func (s *State) SetTheme(th theme.Theme) { s.Theme = th }

// SetWorkspace records the active workspace label ("1", "2", ...). Kept as
// a legacy entry point for the `tick` event payload (worker.js may post
// a `workspace` field opportunistically). The numeric model is the
// authoritative source — SetActiveWorkspace / SetWorkspaceCount overwrite
// the label whenever they change so the two stay coherent.
func (s *State) SetWorkspace(w string) { s.Workspace = w }

// SetActiveWorkspace records the active workspace number (1..WorkspaceCount)
// and refreshes the rendered Workspace label. Clamped silently to the legal
// range so a malformed compositor payload cannot poison the model.
func (s *State) SetActiveWorkspace(n int) {
	if s.WorkspaceCount > 0 {
		if n < 1 {
			n = 1
		}
		if n > s.WorkspaceCount {
			n = s.WorkspaceCount
		}
	}
	s.ActiveWorkspace = n
	s.Workspace = workspaceLabel(s.ActiveWorkspace, s.WorkspaceCount)
}

// SetWorkspaceCount records the total workspace count (typically 4) and
// refreshes the rendered Workspace label. A non-positive count is treated
// as "unknown" and the label reduces to the active number only.
func (s *State) SetWorkspaceCount(n int) {
	if n < 0 {
		n = 0
	}
	s.WorkspaceCount = n
	if s.ActiveWorkspace < 1 {
		s.ActiveWorkspace = 1
	}
	if s.WorkspaceCount > 0 && s.ActiveWorkspace > s.WorkspaceCount {
		s.ActiveWorkspace = s.WorkspaceCount
	}
	s.Workspace = workspaceLabel(s.ActiveWorkspace, s.WorkspaceCount)
}

// NextWorkspace returns the index the bar should cycle to on a forward
// step (left-click on the workspace section, scroll-wheel down). Wraps
// from WorkspaceCount back to 1. Returns the current active workspace if
// the count is non-positive so the dock cannot dispatch a bogus index.
func (s *State) NextWorkspace() int {
	if s.WorkspaceCount <= 0 {
		return s.ActiveWorkspace
	}
	n := s.ActiveWorkspace + 1
	if n > s.WorkspaceCount {
		n = 1
	}
	return n
}

// PrevWorkspace returns the index the bar should cycle to on a backward
// step (scroll-wheel up). Wraps from 1 back to WorkspaceCount.
func (s *State) PrevWorkspace() int {
	if s.WorkspaceCount <= 0 {
		return s.ActiveWorkspace
	}
	n := s.ActiveWorkspace - 1
	if n < 1 {
		n = s.WorkspaceCount
	}
	return n
}

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
	bx, _, iw, _ := s.IconbarRect()
	// Anchor past the last launcher slot's right edge (NOT including the
	// trailing IconbarButtonGap — the SeparatorW replaces it).
	lastLauncherRight := bx + len(s.Apps)*(IconbarButtonW+IconbarButtonGap) - IconbarButtonGap
	if len(s.Apps) == 0 {
		lastLauncherRight = bx - SeparatorW // empty launcher row: window row starts at iconbar left
	}
	rowStart := lastLauncherRight + SeparatorW
	// Shrink the window buttons so N of them always FIT in the space left in the
	// iconbar (between the launcher row and the clock). At the full IconbarButtonW,
	// four+ windows overflow past the iconbar's right edge — and HitTestWindow
	// rejects buttons starting past it, so the rightmost entries (e.g. a
	// just-minimized window) would become unclickable. Cap at IconbarButtonW so a
	// nearly-empty desktop keeps the roomy default.
	bw := IconbarButtonW
	if n := len(s.Windows); n > 0 {
		avail := (bx + iw) - rowStart
		if fit := (avail - (n-1)*IconbarButtonGap) / n; fit < bw {
			bw = fit
		}
		if bw < 1 {
			bw = 1
		}
	}
	x = rowStart + i*(bw+IconbarButtonGap)
	y = IconbarVPad
	w = bw
	h = s.H - 2*IconbarVPad
	if h < 1 {
		h = 1
	}
	return
}

// HitTestWorkspace reports whether (x, y) falls inside the workspace section
// on the left edge of the toolbar. Used by the dock to recognize a
// left-click (cycle to next workspace) or scroll-wheel event (cycle
// back/forward) over the workspace UI.
func (s *State) HitTestWorkspace(x, y int) bool {
	wx, wy, ww, wh := s.WorkspaceRect()
	return x >= wx && x < wx+ww && y >= wy && y < wy+wh
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
	// Hit-test the LAID-OUT geometry (magnified under a hovering cursor, resting
	// otherwise) so a click lands on the button where it is actually painted.
	// The click is already confined to the iconbar by the range guard above, so
	// a slot that contains it is genuinely visible there — even a magnified
	// button whose anchor was cursor-shifted just outside the iconbar edge.
	for _, sl := range s.laidOutSlots() {
		if sl.isWindow {
			continue
		}
		if x >= sl.x && x < sl.x+sl.w && y >= sl.y && y < sl.y+sl.h {
			return sl.idx
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
	// Hit-test the LAID-OUT window buttons (magnified under a hovering cursor,
	// resting otherwise) so a click lands on the button where it is painted.
	// The range guard above already confines the click to the iconbar.
	for _, sl := range s.laidOutSlots() {
		if !sl.isWindow {
			continue
		}
		if x >= sl.x && x < sl.x+sl.w && y >= sl.y && y < sl.y+sl.h {
			return sl.idx
		}
	}
	return -1
}

// ---- painting: the toolkit widget tree -----------------------------------

// dockToolkitTheme is the toolkit.Theme handed to the widget tree's Draw
// pass. The dock's leaves paint from the richer Openbox theme.Theme stored on
// State (gradients + per-state title colours that toolkit.Theme cannot
// express), so this value is never consulted for colour — it exists only to
// satisfy the Widget.Draw(painter, *toolkit.Theme) signature.
var dockToolkitTheme = toolkit.DefaultLight()

// rgba converts a theme.Color (RGB triple) to an opaque toolkit.RGBA.
func rgba(c theme.Color) toolkit.RGBA { return toolkit.RGB(c[0], c[1], c[2]) }

// Render fills buf (a 4*W*H byte slice, RGBA32 row-major) with the toolbar
// at the current state. buf must be exactly the right size or Render panics
// (a size mismatch in the caller is a bug). The whole surface is opaque —
// the toolbar paints every pixel from edge to edge.
//
// Render builds the widget view from the current State, lays it out with one
// container pass and paints it, then overlays the 1-pixel top border chrome.
func Render(s *State, buf []byte) {
	need := 4 * s.W * s.H
	if len(buf) != need {
		panic("scene: buffer size mismatch")
	}
	p := painter.NewPixelPainter(buf, s.W, s.H)
	root := buildRoot(s)
	root.SetBounds(toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H})
	root.Draw(p, dockToolkitTheme)

	// Outer border on the very top edge of the toolbar (the bottom edge sits
	// at the bottom of the canvas, so a bottom border is not visible). One
	// pixel of theme.Border.Color spanning the full surface width.
	if s.Theme.Border.Width > 0 {
		bc := rgba(s.Theme.Border.Color)
		for x := 0; x < s.W; x++ {
			p.PutPixel(x, 0, bc)
		}
	}
}

// buildRoot assembles the dock's widget tree for the current State: an HBox
// shell with a fixed workspace label on the left, a flex iconbar in the
// middle, and a fixed clock on the right. It is cheap enough to rebuild every
// frame, which keeps State the single source of truth (a direct field
// mutation is reflected on the next Render without a separate sync step).
func buildRoot(s *State) *toolkit.HBox {
	root := toolkit.NewHBox()
	root.Spacing = 0

	ws := &section{
		bg:   s.Theme.Window.Inactive.Title.Bg,
		text: s.Workspace,
		ink:  s.Theme.Window.Inactive.Title.Label.Color,
	}
	root.AddFixed(ws, WorkspaceW)

	ib := &iconbar{s: s}
	root.AddFlex(ib, 1)

	clock := s.Clock
	if clock == "" {
		clock = "--:--"
	}
	ck := &section{
		bg:   s.Theme.Osd.Bg,
		text: clock,
		ink:  s.Theme.Osd.Label.Color,
	}
	root.AddFixed(ck, ClockW)
	return root
}

// section is a fixed-width bevelled toolbar end (the workspace label + the
// clock): a gradient background, a raised bevel, and one line of centred
// text. Both ends share this leaf; only their bg / text / ink differ.
type section struct {
	toolkit.Base
	bg   theme.Bg
	text string
	ink  theme.Color
}

// Draw paints the section's gradient face, bevel and centred label.
func (w *section) Draw(p painter.Painter, _ *toolkit.Theme) {
	r := w.Bounds()
	paintBg(p, r, w.bg)
	drawBevel(p, r)
	tx := r.X + (r.W-toolkit.TextWidth(w.text))/2
	ty := r.Y + (r.H-toolkit.GlyphHeight())/2
	toolkit.DrawText(p, tx, ty, w.text, rgba(w.ink))
}

// iconbar is the flex middle section: it paints the active-title background +
// bevel over the iconbar rect, then draws its launcher buttons, the launcher/
// window separator, and its window buttons — clipping + stopping exactly like
// the surface geometry dictates so a narrow surface degrades gracefully.
type iconbar struct {
	toolkit.Base
	s *State
}

// Draw paints the iconbar background then fans out to the button leaves,
// iterating the LAID-OUT slots so a hovering cursor's magnification is drawn
// exactly where HitTest expects it. Launcher buttons additionally carry their
// running / focused indicators + attention badge; the launcher/window separator
// is placed at the (possibly magnified) boundary between the two rows.
func (ib *iconbar) Draw(p painter.Painter, th *toolkit.Theme) {
	s := ib.s
	ix, _, iw, _ := s.IconbarRect()
	paintBg(p, toolkit.Rect{X: ix, Y: 0, W: iw, H: s.H}, s.Theme.Window.Active.Title.Bg)
	drawBevel(p, toolkit.Rect{X: ix, Y: 0, W: iw, H: s.H})

	running := s.launcherRunning()
	focusApp := s.focusedLauncher()
	slots := s.laidOutSlots()

	// Right edge of the last drawn launcher slot, for the separator placement.
	lastLauncherRight := -1
	for _, sl := range slots {
		// Skip buttons whose anchor falls outside the iconbar (very narrow
		// surface fallback).
		if sl.x >= ix+iw {
			continue
		}
		// Clip the right edge of the button to the iconbar's right.
		cw := sl.w
		if sl.x+cw > ix+iw {
			cw = ix + iw - sl.x
		}
		r := toolkit.Rect{X: sl.x, Y: sl.y, W: cw, H: sl.h}
		if sl.isWindow {
			wb := &windowButton{s: s, win: s.Windows[sl.idx], scale: sl.scale}
			wb.SetBounds(r)
			wb.Draw(p, th)
			continue
		}
		lastLauncherRight = sl.x + sl.w
		lb := &launcherButton{
			s: s, app: s.Apps[sl.idx], scale: sl.scale,
			running: running[sl.idx],
			focused: sl.idx == focusApp,
			badge:   s.BadgeCount(s.Apps[sl.idx].Id),
		}
		lb.SetBounds(r)
		lb.Draw(p, th)
	}

	// Separator between the static launcher row and the dynamic open-window
	// row: a 1-pixel-wide dark vertical line centered inside the SeparatorW
	// gap past the last launcher's right edge. Skipped entirely when no
	// launchers were drawn (empty Apps or all clipped away).
	if len(s.Apps) > 0 && lastLauncherRight >= 0 {
		sepX := lastLauncherRight + SeparatorW - SeparatorW/2 - 1
		if sepX >= ix && sepX < ix+iw {
			sepInk := toolkit.RGB(0x40, 0x40, 0x40)
			for jj := IconbarVPad; jj < s.H-IconbarVPad; jj++ {
				p.PutPixel(sepX, jj, sepInk)
			}
		}
	}
}

// launcherButton is one static app-launcher leaf: an inactive-title bevelled
// face, the app glyph at the left, and the truncated app label.
type launcherButton struct {
	toolkit.Base
	s   *State
	app App
	// scale is the magnification factor of this button (1 = resting); it grows
	// the glyph so a hovered launcher's icon swells with its button.
	scale float64
	// running / focused drive the running dot + active underline; badge is the
	// attention count (0 = none).
	running bool
	focused bool
	badge   int
}

// glyphSize returns the icon side length for a launcher slot: the resting
// IconGlyphPx scaled by the button's magnification, clamped so it never spills
// past the button height.
func (b *launcherButton) glyphSize(r toolkit.Rect) int {
	g := IconGlyphPx
	if b.scale > 1 {
		g = int(float64(IconGlyphPx)*b.scale + 0.5)
	}
	if max := r.H - 4; g > max {
		g = max
	}
	if g < 1 {
		g = 1
	}
	return g
}

// Draw paints the launcher button (bevelled face + glyph + truncated label),
// then its running / focused indicators and any attention badge on top.
func (b *launcherButton) Draw(p painter.Painter, _ *toolkit.Theme) {
	r := b.Bounds()
	paintBg(p, r, b.s.Theme.Window.Inactive.Title.Bg)
	drawBevel(p, r)
	// Glyph at the left, swelling with the button under magnification.
	gsz := b.glyphSize(r)
	gy := r.Y + (r.H-gsz)/2
	gx := r.X + IconGlyphLeftPad
	drawGlyph(p, b.app.Glyph, toolkit.Rect{X: gx, Y: gy, W: gsz, H: gsz})
	// Label to the right of the glyph, truncated to the remaining width.
	tx := gx + gsz + IconLabelGap
	ty := r.Y + (r.H-toolkit.GlyphHeight())/2
	maxW := r.X + r.W - tx - 2
	drawClippedText(p, b.app.Label, tx, ty, rgba(b.s.Theme.Window.Active.Title.Label.Color), maxW)
	// Running / focused indicators + attention badge overlay.
	if b.running {
		b.s.drawRunningIndicator(p, r, b.focused)
	}
	drawBadge(p, r, b.badge)
}

// windowButton is one open-window leaf. Its look follows Fluxbox-style
// semantics chosen from three Openbox theme states:
//
//   - Focused window: sunken bevel + active.title background gradient +
//     active.label ink — the "this is the current window" look.
//   - Unfocused open window: raised bevel + inactive.title background +
//     active.label ink — the "another open window, click to focus" look.
//   - Minimized window: raised bevel + inactive.title background +
//     inactive.label (dimmer) ink + "[*] " accent prefix on the label — the
//     "this window is folded into the iconbar" look.
type windowButton struct {
	toolkit.Base
	s   *State
	win Window
	// scale is the magnification factor of this button (1 = resting). The
	// window button is text-only, so the swell is carried by the button rect
	// (set by the iconbar from the laid-out slot); the field is kept for parity
	// with launcherButton and future glyphed window entries.
	scale float64
}

// Draw paints the window button in one of the three focus / minimized styles.
func (b *windowButton) Draw(p painter.Painter, _ *toolkit.Theme) {
	r := b.Bounds()
	s := b.s
	var bg theme.Bg
	var ink theme.Color
	label := b.win.Title
	switch {
	case b.win.Focused:
		bg = s.Theme.Window.Active.Title.Bg
		ink = s.Theme.Window.Active.Title.Label.Color
	case b.win.Minimized:
		bg = s.Theme.Window.Inactive.Title.Bg
		ink = s.Theme.Window.Inactive.Title.Label.Color
		label = "[*] " + label
	default:
		bg = s.Theme.Window.Inactive.Title.Bg
		ink = s.Theme.Window.Active.Title.Label.Color
	}
	paintBg(p, r, bg)
	if b.win.Focused {
		drawSunkenBevel(p, r)
	} else {
		drawBevel(p, r)
	}
	tx := r.X + IconGlyphLeftPad
	ty := r.Y + (r.H-toolkit.GlyphHeight())/2
	maxW := r.X + r.W - tx - 2
	drawClippedText(p, label, tx, ty, rgba(ink), maxW)
}

// ---- painter primitives (Fluxbox chrome) ---------------------------------

// paintBg fills r with a theme.Bg gradient. A flat fill is one FillRect;
// vertical / horizontal / diagonal / cross-diagonal interpolate per pixel via
// the same lerp the theme package uses so the pixels match byte-for-byte.
func paintBg(p painter.Painter, r toolkit.Rect, bg theme.Bg) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	if bg.Gradient == theme.GradientFlat {
		p.FillRect(r, rgba(bg.Color))
		return
	}
	for j := 0; j < r.H; j++ {
		for i := 0; i < r.W; i++ {
			p.PutPixel(r.X+i, r.Y+j, rgba(gradientAt(bg.Gradient, i, j, r.W, r.H, bg.Color, bg.ColorTo)))
		}
	}
}

// gradientAt resolves the colour at (i, j) inside an rw x rh section under the
// given gradient — a per-channel linear lerp between c1 and c2. Mirrors the
// theme package's interp so a widget-drawn gradient is pixel-identical to the
// raw-buffer PaintGradient it replaces. Unhandled (flat / bevel / recorded-
// only) variants return solid c1.
func gradientAt(g theme.GradientType, i, j, rw, rh int, c1, c2 theme.Color) theme.Color {
	switch g {
	case theme.GradientVertical:
		return lerpColor(c1, c2, j, rh-1)
	case theme.GradientHorizontal:
		return lerpColor(c1, c2, i, rw-1)
	case theme.GradientDiagonal:
		return lerpColor(c1, c2, i+j, (rw-1)+(rh-1))
	case theme.GradientCrossDiagonal:
		return lerpColor(c1, c2, (rw-1-i)+j, (rw-1)+(rh-1))
	default:
		return c1
	}
}

// lerpColor interpolates each RGB channel linearly between c1 (at step=0) and
// c2 (at step=denom). A non-positive denom collapses to c1 (1-pixel section).
func lerpColor(c1, c2 theme.Color, step, denom int) theme.Color {
	if denom <= 0 {
		return c1
	}
	if step < 0 {
		step = 0
	}
	if step > denom {
		step = denom
	}
	var out theme.Color
	for k := 0; k < 3; k++ {
		a := int(c1[k])
		b := int(c2[k])
		out[k] = uint8(a + (b-a)*step/denom)
	}
	return out
}

// drawBevel paints a 1-pixel raised bevel around r: a bright top + left, a
// dark bottom + right. The highlights are pure white / near-black so they read
// clearly against any gradient face.
func drawBevel(p painter.Painter, r toolkit.Rect) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	hi := toolkit.RGB(0xFF, 0xFF, 0xFF)
	lo := toolkit.RGB(0x40, 0x40, 0x40)
	for i := 0; i < r.W; i++ {
		p.PutPixel(r.X+i, r.Y, hi)
		p.PutPixel(r.X+i, r.Y+r.H-1, lo)
	}
	for j := 0; j < r.H; j++ {
		p.PutPixel(r.X, r.Y+j, hi)
		p.PutPixel(r.X+r.W-1, r.Y+j, lo)
	}
}

// drawSunkenBevel paints a 1-pixel sunken bevel around r: dark top + left,
// bright bottom + right — the inverse of drawBevel. Marks the focused
// open-window button (a Fluxbox-style "pressed" look).
func drawSunkenBevel(p painter.Painter, r toolkit.Rect) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	hi := toolkit.RGB(0xFF, 0xFF, 0xFF)
	lo := toolkit.RGB(0x40, 0x40, 0x40)
	for i := 0; i < r.W; i++ {
		p.PutPixel(r.X+i, r.Y, lo)
		p.PutPixel(r.X+i, r.Y+r.H-1, hi)
	}
	for j := 0; j < r.H; j++ {
		p.PutPixel(r.X, r.Y+j, lo)
		p.PutPixel(r.X+r.W-1, r.Y+j, hi)
	}
}

// drawClippedText paints txt at (x, y) in ink but stops once the next rune
// would extend past maxWidth pixels (relative to x). It measures each whole-rune
// prefix with toolkit.TextWidth and draws the widest one that still fits, so the
// clip is exact for the active font — proportional AA/shaped OpenType as well as
// the fixed-advance bitmap default. (The old maxWidth/charWidth character count
// assumed a fixed 6px advance and would overflow the button with the wider
// proportional face.)
func drawClippedText(p painter.Painter, txt string, x, y int, ink toolkit.RGBA, maxWidth int) {
	if maxWidth <= 0 || txt == "" {
		return
	}
	if toolkit.TextWidth(txt) <= maxWidth {
		toolkit.DrawText(p, x, y, txt, ink)
		return
	}
	// Grow the prefix rune by rune until the next rune would overflow.
	runes := []rune(txt)
	fit := 0
	for i := 1; i <= len(runes); i++ {
		if toolkit.TextWidth(string(runes[:i])) > maxWidth {
			break
		}
		fit = i
	}
	if fit == 0 {
		return
	}
	toolkit.DrawText(p, x, y, string(runes[:fit]), ink)
}

// ---- glyph drawing -------------------------------------------------------

// drawGlyph paints one of the built-in icon marks into r. The two glyphs that
// map cleanly onto the toolkit's stock icon library reuse it (editor →
// DrawIconNew, files → DrawIconOpen); the bespoke Fluxbox marks (terminal /
// hello / code / loom) stay custom Draw.
func drawGlyph(p painter.Painter, g Glyph, r toolkit.Rect) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	ink := toolkit.RGB(0x1a, 0x1a, 0x1a)
	switch g {
	case GlyphTerminal:
		drawGlyphTerminal(p, r, ink)
	case GlyphEditor:
		toolkit.DrawIconNew(p, r, ink)
	case GlyphFiles:
		toolkit.DrawIconOpen(p, r, ink)
	case GlyphHello:
		drawGlyphHello(p, r, ink)
	case GlyphCode:
		drawGlyphCode(p, r, ink)
	case GlyphLoom:
		drawGlyphLoom(p, r, ink)
	default:
		// Unknown glyph: paint a solid square so the slot is still visible.
		p.FillRect(r, ink)
	}
}

func drawGlyphTerminal(p painter.Painter, r toolkit.Rect, ink toolkit.RGBA) {
	x, y, w, h := r.X, r.Y, r.W, r.H
	// ">" caret + underscore cursor inside the box.
	cx := x + w*2/5
	cy := y + h/2
	arm := h / 4
	for t := 0; t <= arm; t++ {
		p.PutPixel(cx-arm+t, cy-arm+t, ink)
		p.PutPixel(cx-arm+t, cy+arm-t, ink)
	}
	uy := y + h*3/4
	for ux := x + 2; ux < x+w-2; ux++ {
		p.PutPixel(ux, uy, ink)
	}
}

func drawGlyphHello(p painter.Painter, r toolkit.Rect, ink toolkit.RGBA) {
	x, y, w, h := r.X, r.Y, r.W, r.H
	// Smile arc: bottom half of a "circle" inside the box.
	cx := x + w/2
	cy := y + h/2
	rad := w / 2
	if h/2 < rad {
		rad = h / 2
	}
	for i := -rad; i <= rad; i++ {
		p.PutPixel(cx+i, cy+(rad-abs(i))/2, ink)
	}
	// Two eyes.
	p.PutPixel(cx-rad/2, cy-rad/2, ink)
	p.PutPixel(cx+rad/2, cy-rad/2, ink)
}

// drawGlyphCode paints "< >" angle-brackets centred in r -- the near-universal
// "code" icon. Each bracket is a chevron from a vertical midpoint, mirrored
// across the box. Inset by 2 px so it sits inside the button border.
func drawGlyphCode(p painter.Painter, r toolkit.Rect, ink toolkit.RGBA) {
	x, y, w, h := r.X, r.Y, r.W, r.H
	cy := y + h/2
	armH := h/2 - 3
	if armH < 2 {
		armH = 2
	}
	// Left angle bracket "<": apex at (x+2, cy), opens to the right.
	leftX := x + 2
	for t := 0; t <= armH; t++ {
		p.PutPixel(leftX+t, cy-t, ink)
		p.PutPixel(leftX+t, cy+t, ink)
	}
	// Right angle bracket ">": apex at (x+w-3, cy), opens to the left.
	rightX := x + w - 3
	for t := 0; t <= armH; t++ {
		p.PutPixel(rightX-t, cy-t, ink)
		p.PutPixel(rightX-t, cy+t, ink)
	}
}

// drawGlyphLoom paints loom's 4-strand "weave" mark -- 4 horizontal warp
// threads + 4 vertical weft threads crossed in the centre. Echoes the
// openweft brand mark + reads as "fabric / weave" at icon size.
func drawGlyphLoom(p painter.Painter, r toolkit.Rect, ink toolkit.RGBA) {
	x, y, w, h := r.X, r.Y, r.W, r.H
	if w < 6 || h < 6 {
		// Fallback for tiny boxes: a simple grid.
		for j := 0; j < h; j += 2 {
			for i := 0; i < w; i++ {
				p.PutPixel(x+i, y+j, ink)
			}
		}
		return
	}
	// 4 horizontal warps spaced evenly across the height.
	for i := 0; i < 4; i++ {
		ly := y + 2 + i*((h-4)/3)
		for lx := x + 1; lx < x+w-1; lx++ {
			p.PutPixel(lx, ly, ink)
		}
	}
	// 4 vertical wefts spaced evenly across the width.
	for i := 0; i < 4; i++ {
		lx := x + 2 + i*((w-4)/3)
		for ly := y + 1; ly < y+h-1; ly++ {
			p.PutPixel(lx, ly, ink)
		}
	}
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}
