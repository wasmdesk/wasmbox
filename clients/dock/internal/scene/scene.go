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
//     terminal / editor / files / hello / vscode / loom), then one task
//     button per OPEN WINDOW the compositor handed us via the
//     `windows_changed` input event. Window buttons render in three styles
//     that match Fluxbox semantics:
//   - focused window: sunken bevel — reads as the currently selected
//     button (BevelDockStyle draws the Active item pressed-in);
//   - unfocused open window: raised bevel — reads as a normal button;
//   - minimized window: raised bevel + "[*] " accent prefix on the
//     title — reads as a folded entry;
//     Left-clicking a window button posts a `focus` message to the
//     compositor (which raises + focuses it, restoring it first if it was
//     minimized); right-clicking posts a `close` message;
//   - a fixed-width clock ("HH:MM") on the right, kept in sync by a `tick`
//     event posted by the JS worker every 30 seconds.
//
// # Toolkit widget model
//
// The bar is composed from go-widgets/toolkit widgets rather than hand-drawn
// rects: the two fixed-width ends (the workspace label + the clock) are
// `section` leaf widgets, and the whole iconbar is ONE toolkit.AppDock. The
// AppDock carries the launchers (fixed-width icon items) followed by one
// variable-width task button per open window (its Width sized to the window
// title via toolkit.TextWidth), wears the toolkit.BevelDockStyle for the
// Fluxbox raised/sunken 3D bevels, and owns layout, hover magnification and
// hit-testing. The single index space maps back to launcher (index < len(Apps))
// vs. window (index-len(Apps)); ItemRects publishes the live geometry the
// headless probes read through __wasmdockGeometry, so paint, hit-testing and the
// exposed rects share one source of truth. The bespoke Fluxbox app glyphs
// (terminal / hello / code / loom) ride into each launcher item as its Icon
// painter, while the two that map onto stock artwork reuse the toolkit icon
// library (editor → DrawIconNew, files → DrawIconOpen). Text is drawn with the
// toolkit's active AA/shaped OpenType face (enabled once via UseOpenTypeText).
//
// The State value remains the single source of truth for layout: HitTest /
// HitTestWindow / LauncherRects / WindowRects all rebuild the (cheap) AppDock
// from the current State, so a direct field mutation is always reflected on the
// next call.
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
// surfaces as a task button. Id is the compositor's window id (echoed back in
// `focus` / `close` / `restore` messages); Title is the window title painted
// inside the button; Minimized is true iff the compositor reports the window
// as currently folded into the iconbar (rendered with a "[*]" prefix so the
// user reads it as a folded entry); Focused is true iff this is the
// keyboard-focused window (rendered with a sunken bevel so the user reads it as
// the selected button). Role mirrors the compositor's role attribute — panels
// are filtered out server-side so Role is always "window" in practice, but the
// field is here so a future iconbar style for non-window roles needs no schema
// change.
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
	// IconbarButtonW is the fixed pixel width of one launcher item in the
	// AppDock iconbar (window task buttons size to their title instead).
	IconbarButtonW = 120
	// IconGlyphPx is the side length of the reference icon tile used when a
	// glyph is drawn stand-alone (e.g. in unit tests). The live iconbar draws
	// each launcher glyph into the AppDock's own glyph box.
	IconGlyphPx = 16
)

// taskButtonPad is the extra device pixels a window task button reserves beyond
// its measured title: the AppDock reserves a glyph box (pad + glyph + label
// gap) to the left of every label even for an icon-less item, so a task button
// sized to fit its title must budget for that reserved run plus a small trailing
// margin.
func taskButtonPad() int {
	return toolkit.Scaled(toolkit.AppDockPadX) +
		toolkit.Scaled(toolkit.AppDockGlyphPx) +
		toolkit.Scaled(toolkit.AppDockLabelGap) +
		toolkit.Scaled(2) + 6
}

// State is the toolbar's mutable model: surface size, the static launcher
// row, the active open-window row (one task button per non-panel window the
// compositor has open, including folded ones — flagged via Window.Minimized),
// the active workspace + workspace count (numeric model — Workspace string
// derives from them in Render), the current clock string, the cursor
// position (drives hover magnification) and the active Openbox-compatible
// Theme.
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

// HitTestWorkspace reports whether (x, y) falls inside the workspace section
// on the left edge of the toolbar. Used by the dock to recognize a
// left-click (cycle to next workspace) or scroll-wheel event (cycle
// back/forward) over the workspace UI.
func (s *State) HitTestWorkspace(x, y int) bool {
	wx, wy, ww, wh := s.WorkspaceRect()
	return x >= wx && x < wx+ww && y >= wy && y < wy+wh
}

// ---- the iconbar AppDock -------------------------------------------------

// windowLabel is the label a window task button paints: the title, prefixed
// with the "[*] " folded marker when the window is minimized.
func windowLabel(w Window) string {
	if w.Minimized {
		return "[*] " + w.Title
	}
	return w.Title
}

// windowButtonWidths returns the device-pixel width of each window task button:
// the measured title width plus the AppDock's reserved glyph run + margin, so
// the whole title fits. When the natural row would overflow the iconbar's right
// edge the widths are capped uniformly (mirroring Fluxbox's shrink-to-fit) so
// even a just-minimized window's button stays inside the bar and clickable.
func (s *State) windowButtonWidths() []int {
	n := len(s.Windows)
	widths := make([]int, n)
	if n == 0 {
		return widths
	}
	pad := taskButtonPad()
	for i, w := range s.Windows {
		wd := pad + toolkit.TextWidth(windowLabel(w))
		if wd < 1 {
			wd = 1
		}
		widths[i] = wd
	}
	// Shrink-to-fit: AppDock lays the items out at bounds.X + gap, each item
	// consuming width+gap. The window row begins after the launcher row, so the
	// space left for the windows is the iconbar width minus the launcher run.
	_, _, iw, _ := s.IconbarRect()
	gap := toolkit.Scaled(toolkit.AppDockGap)
	consumed := gap + len(s.Apps)*(IconbarButtonW+gap)
	avail := iw - consumed
	total := 0
	for _, wd := range widths {
		total += wd + gap
	}
	if avail > 0 && total > avail {
		capW := (avail - n*gap) / n
		if capW < 1 {
			capW = 1
		}
		for i := range widths {
			if widths[i] > capW {
				widths[i] = capW
			}
		}
	}
	return widths
}

// iconDock builds the toolkit.AppDock that renders + hit-tests the iconbar for
// the current State: the launcher items first (fixed IconbarButtonW width, the
// bespoke glyph as their Icon painter, running/active/badge state), then one
// variable-width task button per open window (Label = title, "[*] "-prefixed
// when minimized; Active = focused so BevelDockStyle draws it sunken). The dock
// wears BevelDockStyle for the Fluxbox 3D look and is fed the cursor + the
// magnification config from State, so paint, hit-testing and the exposed rects
// all read one layout.
func (s *State) iconDock() *toolkit.AppDock {
	running := s.launcherRunning()
	focusApp := s.focusedLauncher()

	items := make([]toolkit.AppDockItem, 0, len(s.Apps)+len(s.Windows))
	for i := range s.Apps {
		g := s.Apps[i].Glyph
		items = append(items, toolkit.AppDockItem{
			Id:      s.Apps[i].Id,
			Label:   s.Apps[i].Label,
			Icon:    func(p painter.Painter, r toolkit.Rect, ink toolkit.RGBA) { drawGlyph(p, g, r) },
			Running: running[i],
			Active:  i == focusApp,
			Badge:   s.BadgeCount(s.Apps[i].Id),
			Width:   IconbarButtonW,
		})
	}
	widths := s.windowButtonWidths()
	for i := range s.Windows {
		items = append(items, toolkit.AppDockItem{
			Id:     itoa(s.Windows[i].Id),
			Label:  windowLabel(s.Windows[i]),
			Active: s.Windows[i].Focused,
			Width:  widths[i],
		})
	}

	d := toolkit.NewAppDock(items...)
	d.Style = toolkit.BevelDockStyle{}
	d.Magnify = s.Magnify.On
	d.MaxScale = s.Magnify.MaxScale
	d.Radius = s.Magnify.Radius

	ix, iy, iw, ih := s.IconbarRect()
	d.SetBounds(toolkit.Rect{X: ix, Y: iy, W: iw, H: ih})
	// Magnify only when the cursor actually hovers the iconbar (not the fixed
	// workspace/clock ends), matching the AppDock's own "inside" contract.
	inside := s.CursorInside && s.CursorX >= ix && s.CursorX < ix+iw
	d.SetCursor(s.CursorX, inside)
	return d
}

// HitTest returns the LAUNCHER index under (x, y) in surface coordinates, or -1
// if (x, y) does not fall inside any launcher button. Window task buttons (which
// follow the launcher row in the same AppDock) are probed with HitTestWindow.
func (s *State) HitTest(x, y int) int {
	i := s.iconDock().HitTest(x, y)
	if i >= 0 && i < len(s.Apps) {
		return i
	}
	return -1
}

// HitTestWindow returns the open-window index under (x, y) in surface
// coordinates, or -1 if (x, y) does not fall inside any window task button. The
// window row sits after the launcher row in the same AppDock, so a hit index at
// or past len(Apps) maps to window index (index - len(Apps)).
func (s *State) HitTestWindow(x, y int) int {
	i := s.iconDock().HitTest(x, y)
	if i >= len(s.Apps) {
		return i - len(s.Apps)
	}
	return -1
}

// ---- painting ------------------------------------------------------------

// dockToolkitTheme is the toolkit.Theme handed to the widget tree's Draw pass.
// The dock's `section` ends paint from the richer Openbox theme.Theme stored on
// State (gradients + per-state title colours that toolkit.Theme cannot express),
// so this value is never consulted for their colour. The iconbar AppDock /
// BevelDockStyle DOES read it — its Surface (item face), SurfaceAlt (ground) and
// OnSurface (label ink) are the gray Fluxbox-family bevel palette.
var dockToolkitTheme = toolkit.DefaultLight()

// rgba converts a theme.Color (RGB triple) to an opaque toolkit.RGBA.
func rgba(c theme.Color) toolkit.RGBA { return toolkit.RGB(c[0], c[1], c[2]) }

// Render fills buf (a 4*W*H byte slice, RGBA32 row-major) with the toolbar
// at the current state. buf must be exactly the right size or Render panics
// (a size mismatch in the caller is a bug). The whole surface is opaque —
// the toolbar paints every pixel from edge to edge.
//
// Render draws the three sections directly at their rects — the workspace
// `section`, the iconbar AppDock, the clock `section` — then overlays the
// 1-pixel top border chrome.
func Render(s *State, buf []byte) {
	need := 4 * s.W * s.H
	if len(buf) != need {
		panic("scene: buffer size mismatch")
	}
	p := painter.NewPixelPainter(buf, s.W, s.H)

	// Workspace section (left end).
	wx, wy, ww, wh := s.WorkspaceRect()
	ws := &section{
		bg:   s.Theme.Window.Inactive.Title.Bg,
		text: s.Workspace,
		ink:  s.Theme.Window.Inactive.Title.Label.Color,
	}
	ws.SetBounds(toolkit.Rect{X: wx, Y: wy, W: ww, H: wh})
	ws.Draw(p, dockToolkitTheme)

	// Iconbar (middle): one AppDock owns launchers + window task buttons.
	s.iconDock().Draw(p, dockToolkitTheme)

	// Clock section (right end).
	clock := s.Clock
	if clock == "" {
		clock = "--:--"
	}
	cx, cy, cw, ch := s.ClockRect()
	ck := &section{
		bg:   s.Theme.Osd.Bg,
		text: clock,
		ink:  s.Theme.Osd.Label.Color,
	}
	ck.SetBounds(toolkit.Rect{X: cx, Y: cy, W: cw, H: ch})
	ck.Draw(p, dockToolkitTheme)

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
// clearly against any gradient face. Used by the fixed workspace/clock ends;
// the iconbar's per-item bevels are drawn by toolkit.BevelDockStyle.
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

// ---- glyph drawing -------------------------------------------------------

// drawGlyph paints one of the built-in icon marks into r. The two glyphs that
// map cleanly onto the toolkit's stock icon library reuse it (editor →
// DrawIconNew, files → DrawIconOpen); the bespoke Fluxbox marks (terminal /
// hello / code / loom) stay custom Draw. It is the Icon painter each launcher
// AppDockItem hands the AppDock, so the glyph box + ink come from the widget.
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
