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
// # Persistent widget tree + MVVM binding
//
// The bar is a PERSISTENT go-widgets/toolkit widget tree built exactly once (in
// New) — never rebuilt per frame. The tree is three leaves painted at fixed
// bounds: the workspace `section`, ONE toolkit.AppDock iconbar, and the clock
// `section`, plus a 1-pixel toolkit.Backdrop that strokes the toolbar's top
// edge. Render only lays the leaves out (their bounds are constant, computed in
// New) and draws them; it allocates no widgets and copies no state.
//
// Every mutable, cross-boundary input the view depends on flows through an
// mvvm.Observable, the MVVM "property" primitive: the launcher/window/badge/
// magnify model (itemsO), the cursor (cursorO), the workspace label (wsLabelO),
// the clock string (clockO) and the Openbox theme (themeO). A setter mutates the
// State model and publishes to the matching observable; a subscription wired once
// in New pushes the change into the persistent widget (applyItems rebuilds the
// AppDock's Items in place, applyCursor re-aims magnification, applyTheme repaints
// the section/border palette). So the widget tree tracks the model on CHANGE, not
// on every paint, and paint / hit-testing / the exposed geometry all read one
// live AppDock.
//
// The AppDock carries the launchers (fixed-width icon items) followed by one
// variable-width task button per open window (its Width sized to the window
// title via toolkit.TextWidth), wears the toolkit.BevelDockStyle for the Fluxbox
// raised/sunken 3D bevels, and owns layout, hover magnification and hit-testing.
// The single index space maps back to launcher (index < len(Apps)) vs. window
// (index-len(Apps)); ItemRects publishes the live geometry the headless probes
// read through __wasmdockGeometry. Launcher glyphs are drawn with the go-iconoir
// line-icon library (terminal / home / code-brackets / view-grid), while the two
// that map onto stock artwork reuse the toolkit icon library (editor →
// DrawIconNew, files → DrawIconOpen). Text is drawn with the toolkit's active
// AA/shaped OpenType face (enabled once via UseOpenTypeText).
//
// scene is pure Go (no syscall/js, no cgo) so it builds for any architecture
// and is unit-tested natively. The wasm main only hands it a byte slice to
// fill plus mouse coordinates + clock-tick strings; all layout, hit-testing
// and RGBA painting live here.
package scene

import (
	"sync"

	"github.com/go-iconoir/iconoir"
	"github.com/go-widgets/mvvm"
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
	// GlyphTerminal draws a command prompt (go-iconoir "terminal").
	GlyphTerminal Glyph = iota
	// GlyphEditor draws a document with a folded corner (toolkit stock New icon).
	GlyphEditor
	// GlyphFiles draws a folder shape (toolkit stock Open icon).
	GlyphFiles
	// GlyphHello draws a house — the hello stub client's mark (go-iconoir "home").
	GlyphHello
	// GlyphCode draws angle brackets (go-iconoir "code-brackets") for vscode.
	GlyphCode
	// GlyphLoom draws a woven grid (go-iconoir "view-grid") for the loom launcher.
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

// State is the toolbar's mutable model (the ViewModel): surface size, the static
// launcher row, the active open-window row (one task button per non-panel window
// the compositor has open, including folded ones — flagged via Window.Minimized),
// the active workspace + workspace count (numeric model — Workspace string
// derives from them in Render), the current clock string, the cursor position
// (drives hover magnification) and the active Openbox-compatible Theme.
//
// The model's fields hold the authoritative values (read by the wasm shell and
// the exposed geometry); the persistent widget tree and the mvvm.Observables
// below bind the view to them — a setter mutates the field and publishes to the
// matching observable, whose subscription (wired once in New) updates the
// persistent widget. Nothing is rebuilt per frame.
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

	// --- persistent view (built once in New; Render only lays out + draws) ---
	dock      *toolkit.AppDock  // the iconbar
	wsView    *section          // workspace label (left end)
	clockView *section          // clock (right end)
	border    *toolkit.Backdrop // 1px top-edge stroke
	borderOn  bool              // whether the top border is drawn (Border.Width > 0)

	// --- MVVM binding channels (model change → persistent view) ---
	itemsO   *mvvm.Observable[dockModel]   // launchers + windows + badges + magnify
	cursorO  *mvvm.Observable[cursorState] // pointer position
	wsLabelO *mvvm.Observable[string]      // workspace label text
	clockO   *mvvm.Observable[string]      // clock text
	themeO   *mvvm.Observable[theme.Theme] // section/border palette
}

// dockModel is the value the itemsO observable carries: the full input the
// iconbar's item list depends on. Published (via publishItems) whenever the
// launcher set, window set, badges or magnification change.
type dockModel struct {
	apps    []App
	windows []Window
	badges  map[string]int
	magnify Magnify
}

// cursorState is the value the cursorO observable carries: the pointer position
// and whether it is over the surface, which together drive magnification.
type cursorState struct {
	x, y   int
	inside bool
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
//
// New also builds the PERSISTENT widget tree once (dock + the two section ends +
// the top-border backdrop), wires each mvvm.Observable to the subscription that
// keeps its widget in sync, and seeds the view from the initial model.
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

	// Persistent widgets, built exactly once. Their bounds are fixed by the
	// surface size, so they are laid out here and never again.
	s.dock = toolkit.NewAppDock()
	s.dock.Style = toolkit.BevelDockStyle{}
	s.wsView = &section{}
	s.wsView.SetBounds(rectOf(s.WorkspaceRect()))
	s.clockView = &section{}
	s.clockView.SetBounds(rectOf(s.ClockRect()))
	s.border = &toolkit.Backdrop{}
	s.border.SetBounds(toolkit.Rect{X: 0, Y: 0, W: width, H: 1})

	// MVVM channels, seeded with the initial model so the observable value and
	// the field never diverge.
	s.itemsO = mvvm.NewObservableEq(s.snapshotItems(), nil) // slices: always notify
	s.cursorO = mvvm.NewObservable(s.snapshotCursor())
	s.wsLabelO = mvvm.NewObservable(s.Workspace)
	s.clockO = mvvm.NewObservable(s.Clock)
	s.themeO = mvvm.NewObservable(s.Theme)

	s.itemsO.Subscribe(func(m dockModel) { s.applyItems(m) })
	s.cursorO.Subscribe(func(c cursorState) { s.applyCursor(c) })
	s.wsLabelO.Subscribe(func(v string) { s.wsView.text = v })
	s.clockO.Subscribe(func(v string) { s.clockView.text = displayClock(v) })
	s.themeO.Subscribe(func(th theme.Theme) { s.applyTheme(th) })

	// Seed the view from the initial observable values (Subscribe does not fire).
	s.applyItems(s.itemsO.Get())
	s.applyCursor(s.cursorO.Get())
	s.wsView.text = s.wsLabelO.Get()
	s.clockView.text = displayClock(s.clockO.Get())
	s.applyTheme(s.themeO.Get())
	return s
}

// rectOf packs an (x,y,w,h) tuple into a toolkit.Rect.
func rectOf(x, y, w, h int) toolkit.Rect { return toolkit.Rect{X: x, Y: y, W: w, H: h} }

// snapshotItems captures the current item model for publication on itemsO.
func (s *State) snapshotItems() dockModel {
	return dockModel{apps: s.Apps, windows: s.Windows, badges: s.badges, magnify: s.Magnify}
}

// snapshotCursor captures the current pointer model for publication on cursorO.
func (s *State) snapshotCursor() cursorState {
	return cursorState{x: s.CursorX, y: s.CursorY, inside: s.CursorInside}
}

// publishItems pushes the current item model onto itemsO (whose nil eq always
// notifies), rebuilding the persistent AppDock's items in place.
func (s *State) publishItems() { s.itemsO.Set(s.snapshotItems()) }

// displayClock is the clock text the clock section shows: the tick string, or
// the "--:--" placeholder when no tick has arrived, so the section is never blank.
func displayClock(t string) string {
	if t == "" {
		return "--:--"
	}
	return t
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

// SetCursor records the cursor position and whether it is over the surface, then
// re-aims the iconbar's magnification through cursorO.
func (s *State) SetCursor(x, y int, inside bool) {
	s.CursorX = x
	s.CursorY = y
	s.CursorInside = inside
	s.cursorO.Set(cursorState{x: x, y: y, inside: inside})
}

// SetClock records the latest "HH:MM" clock string posted by the worker and
// refreshes the clock section.
func (s *State) SetClock(t string) {
	s.Clock = t
	s.clockO.Set(t)
}

// SetTheme swaps in a new Openbox-compatible theme. The next Render call
// repaints every section with the new colours / gradients. The section/border
// palette is refreshed immediately through themeO.
func (s *State) SetTheme(th theme.Theme) {
	s.Theme = th
	s.themeO.Set(th)
}

// SetApps replaces the launcher row and refreshes the iconbar. The default set
// from New covers the common case; this is the seam a host uses to override it.
func (s *State) SetApps(apps []App) {
	s.Apps = apps
	s.publishItems()
}

// SetWorkspace records the active workspace label ("1", "2", ...). Kept as
// a legacy entry point for the `tick` event payload (worker.js may post
// a `workspace` field opportunistically). The numeric model is the
// authoritative source — SetActiveWorkspace / SetWorkspaceCount overwrite
// the label whenever they change so the two stay coherent.
func (s *State) SetWorkspace(w string) {
	s.Workspace = w
	s.wsLabelO.Set(w)
}

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
	s.wsLabelO.Set(s.Workspace)
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
	s.wsLabelO.Set(s.Workspace)
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
func (s *State) SetWindows(ws []Window) {
	s.Windows = ws
	s.publishItems()
}

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

// applyItems rebuilds the PERSISTENT AppDock's item list in place from the
// published model m: the launcher items first (fixed IconbarButtonW width, the
// iconoir/toolkit glyph as their Icon painter, running/active/badge state), then
// one variable-width task button per open window (Label = title, "[*] "-prefixed
// when minimized; Active = focused so BevelDockStyle draws it sunken). It also
// refreshes the dock's magnification knobs and bounds. It runs on every itemsO
// change (never per frame), so paint, hit-testing and the exposed rects all read
// one live layout.
func (s *State) applyItems(m dockModel) {
	running := s.launcherRunning()
	focusApp := s.focusedLauncher()

	items := make([]toolkit.AppDockItem, 0, len(m.apps)+len(m.windows))
	for i := range m.apps {
		g := m.apps[i].Glyph
		items = append(items, toolkit.AppDockItem{
			Id:      m.apps[i].Id,
			Label:   m.apps[i].Label,
			Icon:    func(p painter.Painter, r toolkit.Rect, ink toolkit.RGBA) { drawGlyph(p, g, r) },
			Running: running[i],
			Active:  i == focusApp,
			Badge:   badgeOf(m.badges, m.apps[i].Id),
			Width:   IconbarButtonW,
		})
	}
	widths := s.windowButtonWidths()
	for i := range m.windows {
		items = append(items, toolkit.AppDockItem{
			Id:     itoa(m.windows[i].Id),
			Label:  windowLabel(m.windows[i]),
			Active: m.windows[i].Focused,
			Width:  widths[i],
		})
	}

	s.dock.Items = items
	s.dock.Magnify = m.magnify.On
	s.dock.MaxScale = m.magnify.MaxScale
	s.dock.Radius = m.magnify.Radius
	s.dock.SetBounds(rectOf(s.IconbarRect()))
}

// applyCursor re-aims the persistent AppDock's magnification from the published
// cursor c: the swell is active only when the pointer is inside the iconbar's
// x-range (not the fixed workspace/clock ends), matching the AppDock's own
// "inside" contract.
func (s *State) applyCursor(c cursorState) {
	ix, _, iw, _ := s.IconbarRect()
	inside := c.inside && c.x >= ix && c.x < ix+iw
	s.dock.SetCursor(c.x, inside)
}

// applyTheme repaints the section ends' and top border's palette from the
// published theme th. The section ends draw from the richer Openbox theme
// (gradients + per-state title colours toolkit.Theme cannot express); the top
// border is a Backdrop stroking row 0 in the theme's border colour.
func (s *State) applyTheme(th theme.Theme) {
	s.wsView.bg = th.Window.Inactive.Title.Bg
	s.wsView.ink = th.Window.Inactive.Title.Label.Color
	s.clockView.bg = th.Osd.Bg
	s.clockView.ink = th.Osd.Label.Color
	s.border.Fill = rgba(th.Border.Color)
	s.borderOn = th.Border.Width > 0
}

// HitTest returns the LAUNCHER index under (x, y) in surface coordinates, or -1
// if (x, y) does not fall inside any launcher button. Window task buttons (which
// follow the launcher row in the same AppDock) are probed with HitTestWindow.
func (s *State) HitTest(x, y int) int {
	i := s.dock.HitTest(x, y)
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
	i := s.dock.HitTest(x, y)
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
// Render draws the PERSISTENT widget tree — the workspace `section`, the iconbar
// AppDock, the clock `section`, then the top-border Backdrop chrome. It builds
// no widgets and mutates no widget state: the tree was assembled in New and is
// kept current by the observable subscriptions, so Render is pure paint.
func Render(s *State, buf []byte) {
	need := 4 * s.W * s.H
	if len(buf) != need {
		panic("scene: buffer size mismatch")
	}
	p := painter.NewPixelPainter(buf, s.W, s.H)

	s.wsView.Draw(p, dockToolkitTheme)    // workspace section (left end)
	s.dock.Draw(p, dockToolkitTheme)      // iconbar (launchers + window buttons)
	s.clockView.Draw(p, dockToolkitTheme) // clock section (right end)

	// Outer border on the very top edge of the toolbar (the bottom edge sits at
	// the bottom of the canvas, so a bottom border is not visible). A 1-pixel
	// toolkit.Backdrop spanning the full surface width, drawn last so it strokes
	// over the sections/iconbar.
	if s.borderOn {
		s.border.Draw(p, dockToolkitTheme)
	}
}

// section is a fixed-width bevelled toolbar end (the workspace label + the
// clock): a gradient background, a raised bevel, and one line of centred
// text. Both ends share this leaf; only their bg / text / ink differ, kept
// current by applyTheme (bg/ink) and the label/clock subscriptions (text).
type section struct {
	toolkit.Base
	bg   theme.Bg
	text string
	ink  theme.Color
}

// Draw paints the section as a composed toolkit.Backdrop — a gradient (or flat)
// face under a raised Fluxbox bevel — then overlays the centred label. The
// Backdrop owns the last hand-drawn Fluxbox chrome the toolbar ends used to
// paint by hand (the per-pixel gradient + the 1-pixel raised bevel), so there
// is no bespoke shape-drawing left here; only the toolkit text stack.
func (w *section) Draw(p painter.Painter, _ *toolkit.Theme) {
	r := w.Bounds()
	bd := toolkit.Backdrop{
		Fill:  rgba(w.bg.Color),
		Bevel: toolkit.BevelRaised,
	}
	if w.bg.Gradient != theme.GradientFlat {
		bd.GradientTo = rgba(w.bg.ColorTo)
		bd.GradientDir = gradientDir(w.bg.Gradient)
	}
	bd.SetBounds(r)
	bd.Draw(p, dockToolkitTheme)
	tx := r.X + (r.W-toolkit.TextWidth(w.text))/2
	ty := r.Y + (r.H-toolkit.GlyphHeight())/2
	toolkit.DrawText(p, tx, ty, w.text, rgba(w.ink))
}

// gradientDir maps a theme.GradientType onto the toolkit.Backdrop gradient
// direction. Flat / unknown fall back to GradientVertical, though a Flat bg
// never reaches here (section.Draw gates the gradient on != GradientFlat).
func gradientDir(g theme.GradientType) toolkit.GradientDir {
	switch g {
	case theme.GradientHorizontal:
		return toolkit.GradientHorizontal
	case theme.GradientDiagonal:
		return toolkit.GradientDiagonal
	case theme.GradientCrossDiagonal:
		return toolkit.GradientCrossDiagonal
	default:
		return toolkit.GradientVertical
	}
}

// ---- glyph drawing -------------------------------------------------------

// drawGlyph paints one of the built-in launcher marks into r. The two glyphs
// that map cleanly onto the toolkit's stock icon library reuse it (editor →
// DrawIconNew, files → DrawIconOpen); every other glyph is a go-iconoir line
// icon (glyphStem picks the stem). It is the Icon painter each launcher
// AppDockItem hands the AppDock, so the glyph box comes from the widget.
func drawGlyph(p painter.Painter, g Glyph, r toolkit.Rect) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	ink := toolkit.RGB(0x1a, 0x1a, 0x1a)
	switch g {
	case GlyphEditor:
		toolkit.DrawIconNew(p, r, ink)
	case GlyphFiles:
		toolkit.DrawIconOpen(p, r, ink)
	default:
		iconoir.Draw(p, r, glyphStem(g), ink)
	}
}

// glyphStem is the go-iconoir stem that renders a built-in Glyph: a real line
// icon for the four bespoke launchers, and "square" as the visible fallback for
// any unknown glyph value (so an out-of-range slot still paints). Verified
// against iconoir.Names() for the pinned v0.2.0 icon set.
func glyphStem(g Glyph) string {
	switch g {
	case GlyphTerminal:
		return "terminal"
	case GlyphHello:
		return "home"
	case GlyphCode:
		return "code-brackets"
	case GlyphLoom:
		return "view-grid"
	default:
		return "square"
	}
}
