// SPDX-License-Identifier: BSD-3-Clause
//
// Package scene renders the go-widgets/toolkit showcase: one window whose
// layout is built entirely from the toolkit's box-layout container model rather
// than hand-computed toolkit.Rect placement.
//
// The app shell is a BorderLayout Container: a North band stacks the MenuBar,
// the Toolbar and a tab strip (a VBox of fixed-height rows); a South band docks
// the Statusbar; the Center holds the widget gallery. The gallery is a
// CardLayout Container — one card per widget family, exactly one visible at a
// time — and each card is itself a VBox/HBox composition of that family's demo
// widgets (fixed sizes via AddFixed, gutters via fixed spacers, centring and
// insets via flex spacers). Clicking a tab button swaps the CardLayout's Active
// index; a single root.SetBounds lays the whole tree out, root.Draw paints it,
// and root.OnEvent routes clicks into child-local space — no per-widget rect
// arithmetic and no manual hit-testing.
//
// scene is pure Go (no syscall/js) so the painter + every widget can
// be exercised in native unit tests too.
package scene

import (
	"sync"

	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
)

// aaOnce flips the toolkit's active font to anti-aliased, shaped OpenType text
// exactly once for this client process. It is package-scoped because SetFont is
// a process-global; a single opt-in matches the toolkit's "flip it once at
// start-up" contract.
var aaOnce sync.Once

// enableAAText installs the toolkit's bundled AA/shaped OpenType face (Atkinson
// Hyperlegible @16px, toolkit v0.77.0), so the menu bar, toolbar, tab strip,
// gallery widgets and status bar render as the shaped vector face. The bundled
// face never fails to parse (the error is documented as never-returned); on the
// impossible error path the toolkit leaves the still-working bitmap default
// active, so a swallowed error degrades to legible bitmap text, never to none.
// Every widget centres its 20px line box within its band, so the taller face
// re-centres itself and no widget rect moves.
func enableAAText() { aaOnce.Do(func() { _ = toolkit.UseOpenTypeText() }) }

// State is the showcase model. The toolkit composition lives here so
// the scene_test.go file can poke widgets without going through the
// wasm input pipe.
type State struct {
	W, H int

	theme   *toolkit.Theme
	root    *toolkit.Container // BorderLayout app shell
	menuBar *toolkit.MenuBar
	toolbar *toolkit.Toolbar
	status  *toolkit.Statusbar

	// cards is the Center gallery (a CardLayout Container); cardLayout is its
	// layout, so setActiveCard can flip which card is visible. tabButtons is the
	// North tab strip that drives it.
	cards      *toolkit.Container
	cardLayout *toolkit.CardLayout
	tabButtons []*toolkit.Button

	helloButton *toolkit.Button
	clickCount  int
	clickLabel  *toolkit.Label
	check1      *toolkit.CheckButton
	check2      *toolkit.CheckButton
	radioGroup  *toolkit.RadioGroup
	radioA      *toolkit.RadioButton
	radioB      *toolkit.RadioButton
	dropdown    *toolkit.DropDown

	// setFrame is the SDK-wired callback the Frame menu invokes to
	// post a set_frame message to the compositor. Nil in native unit
	// tests (Frame menu clicks become no-ops there — the tests still
	// assert the menu shape).
	setFrame func(name string)

	// activeFrame is the showcase's local idea of "which Frame
	// registry entry is currently active", used only to prefix the
	// Frame menu items with "* " (matching the root-menu convention).
	// Seeded from the page URL's ?frame= param at boot via
	// SetActiveFrame, updated on every Frame-menu click. Not
	// authoritative — if the user swaps the frame via the compositor's
	// root menu, this local value drifts until the next Frame-menu
	// interaction. Acceptable trade-off: "the marker is right 90 %
	// of the time" beats "no marker at all".
	activeFrame string
	entry       *toolkit.Entry
	textView    *toolkit.TextView
	tree        *toolkit.TreeView
	listBox     *toolkit.ListBox
	calendar    *toolkit.Calendar
	colorPick   *toolkit.ColorChooser
	progress    *toolkit.ProgressBar
	scale       *toolkit.Scale
	spin        *toolkit.SpinButton
}

// tabLabels names each gallery card in strip order — the same families the
// pre-migration Notebook exposed, in the same order (so the card index is a
// drop-in for the old notebook.Active index, e.g. the Input card is still 2).
var tabLabels = []string{
	"Button", "Toggles", "Input", "Tree+List", "Calendar", "Color", "Feedback",
}

// inputCard is the index of the Input card (the TextView-hosting one) — the only
// card that consumes keyboard events. Named so HandleKey/HandleChar don't hide a
// magic 2.
const inputCard = 2

// spacer is an invisible, zero-child Container used to reserve fixed gaps
// (AddFixed) or absorb slack (AddFlex) inside a box — the declarative
// replacement for the old hand-computed X/Y offsets. Each call returns a fresh
// instance so it can carry its own bounds in its slot.
func spacer() toolkit.Widget { return toolkit.NewContainer(nil) }

// rowLeft lays w at a fixed left inset and fixed width inside a full-width,
// gap-free HBox (trailing flex spacer soaks up the rest): reproduces a
// left-anchored widget at absolute X=left, width=width.
func rowLeft(left, width int, w toolkit.Widget) *toolkit.HBox {
	h := toolkit.NewHBox()
	h.Spacing = -1 // no inter-child gap; the spacers ARE the geometry
	h.AddFixed(spacer(), left)
	h.AddFixed(w, width)
	h.AddFlex(spacer(), 1)
	return h
}

// rowInset lays w flexibly between equal fixed left/right margins m — reproduces
// a full-bleed widget inset by m on each side (width = box - 2*m).
func rowInset(m int, w toolkit.Widget) *toolkit.HBox {
	h := toolkit.NewHBox()
	h.Spacing = -1
	h.AddFixed(spacer(), m)
	h.AddFlex(w, 1)
	h.AddFixed(spacer(), m)
	return h
}

// rowCenter centres a fixed-width widget on the main axis via equal flex spacers
// — reproduces an X = (box-width)/2 centred widget.
func rowCenter(width int, w toolkit.Widget) *toolkit.HBox {
	h := toolkit.NewHBox()
	h.Spacing = -1
	h.AddFlex(spacer(), 1)
	h.AddFixed(w, width)
	h.AddFlex(spacer(), 1)
	return h
}

// New builds a fully-wired showcase State sized W x H.
func New(w, h int) *State {
	enableAAText() // whole gallery renders with the AA/shaped OpenType face.
	s := &State{W: w, H: h, theme: toolkit.DefaultLight()}

	// MenuBar — the View menu is built from the embedded GTK themes so
	// the user can flip palettes live (validates the toolkit's
	// LoadGTKTheme end-to-end). Each Action also updates the status
	// bar's "theme:" segment so the user always sees which palette is
	// active (a poor man's URL-sync — the wire protocol has no set-URL
	// message + a full worker→compositor→main-thread pipe was
	// disproportionate for this v0.5 iteration; the status readout
	// makes the current theme trivially copy-pasteable if the user
	// wants to bookmark the wasmbox launch URL by hand).
	themeItems := make([]toolkit.MenuItem, 0, 8)
	for _, t := range Themes() {
		picked := t // capture loop var
		themeItems = append(themeItems, toolkit.MenuItem{
			Label: picked.Name,
			Action: func() {
				s.theme = picked.Theme
				s.setActiveThemeName(picked.Name)
			},
		})
	}
	// Frame menu: one entry per compositor FrameRegistry name.
	// Populated eagerly from the well-known 16 names (matches
	// wasmbox/compositor/02_frame.rb FrameRegistry::TABLE). Initial
	// build uses no active marker; main.go calls SetActiveFrame after
	// scene.New to seed the marker from the URL's ?frame= param
	// (which is what actually drove the compositor's boot Frame pick).
	// A placeholder Menu goes in the slot so the SetActiveFrame
	// rebuild has a menu to overwrite.
	s.menuBar = toolkit.NewMenuBar()
	s.menuBar.Names = []string{"File", "Edit", "View", "Frame", "Help"}
	s.menuBar.Menus = []*toolkit.Menu{
		buildMenu([]toolkit.MenuItem{{Label: "New"}, {Label: "Open"}, {Separator: true}, {Label: "Quit"}}),
		buildMenu([]toolkit.MenuItem{{Label: "Cut"}, {Label: "Copy"}, {Label: "Paste"}}),
		buildMenu(themeItems),
		buildMenu(nil), // placeholder — filled by SetActiveFrame below
		buildMenu([]toolkit.MenuItem{{Label: "About"}}),
	}
	// Seed the Frame menu with no active marker (empty string ≠ any
	// registered frame name → no "* " prefix). Called while s.status is
	// still nil, so it only builds the menu — the status segment is
	// seeded below when the Statusbar is created. main.go overrides
	// this at boot with the URL's ?frame= value.
	s.SetActiveFrame("")

	// Toolbar.
	s.toolbar = toolkit.NewToolbar([]toolkit.ToolbarItem{
		{Label: "N"}, {Label: "O"}, {Label: "S"}, {Separator: true},
		{Label: "C"}, {Label: "V"}, {Label: "X"}, {Separator: true},
		{Label: "?"},
	})

	// Card widgets.
	s.helloButton = toolkit.NewButton("Click me", nil)
	s.clickLabel = toolkit.NewLabel("clicked 0 times")
	s.helloButton.OnClick = func() {
		s.clickCount++
		s.clickLabel.Text().Set("clicked " + itoa(s.clickCount) + " times")
	}
	s.check1 = toolkit.NewCheckButton("Wrap long lines", true)
	s.check2 = toolkit.NewCheckButton("Show line numbers", false)
	s.radioGroup = toolkit.NewRadioGroup()
	s.radioA = toolkit.NewRadioButton("Spaces")
	s.radioB = toolkit.NewRadioButton("Tabs")
	s.radioGroup.Add(s.radioA)
	s.radioGroup.Add(s.radioB)
	s.radioA.Checked().Set(true)
	s.dropdown = toolkit.NewDropDown([]string{"UTF-8", "Latin-1", "Shift-JIS"}, 0)

	s.entry = toolkit.NewEntry("hello, world")
	s.textView = toolkit.NewTextView("// edit me!\nfunc main() {\n  fmt.Println(\"hi\")\n}")

	s.tree = toolkit.NewTreeView(&toolkit.TreeNode{
		Label:    "/",
		Expanded: true,
		Children: []*toolkit.TreeNode{
			{Label: "src", Expanded: true, Children: []*toolkit.TreeNode{
				{Label: "main.go"}, {Label: "scene.go"},
			}},
			{Label: "test", Children: []*toolkit.TreeNode{{Label: "scene_test.go"}}},
			{Label: "README.md"},
		},
	})
	s.listBox = toolkit.NewListBox([]string{"apple", "banana", "cherry", "date", "elderberry"})

	s.calendar = toolkit.NewCalendar(2026, 6, 30)
	s.calendar.SetToday(2026, 6, 30)
	s.colorPick = toolkit.NewColorChooser(toolkit.RGB(0x35, 0x84, 0xE4))

	s.progress = toolkit.NewProgressBar()
	s.progress.Fraction = 0.66
	s.scale = toolkit.NewScale(0, 100, 50)
	s.spin = toolkit.NewSpinButton(0, 100, 42, 1)

	// Statusbar.
	s.status = toolkit.NewStatusbar([]string{
		"35 widgets", "100% cov", "theme: Default Light", "frame: openbox",
	})

	// --- box-layout container tree ------------------------------------------
	//
	//	root  Container(BorderLayout)                 — the app shell
	//	├─ North  VBox                                — menu + toolbar + tabs
	//	│  ├─ menuBar   (fixed MenuBarH)
	//	│  ├─ toolbar   (fixed ToolbarButtonH)
	//	│  └─ tabStrip  HBox of tab Buttons (fixed NotebookTabStripH)
	//	├─ Center cards  Container(CardLayout)        — the widget gallery
	//	│  └─ card ×7  VBox/HBox composition per family
	//	└─ South  statusbar (fixed StatusbarH)
	//
	// The Center card body occupies exactly the rect the old Notebook body
	// did — MenuBarH+ToolbarButtonH+NotebookTabStripH down from the top,
	// StatusbarH up from the bottom — so every demo widget lands on its
	// historical rect (see the golden-rect test).
	s.buildCards()

	tabStrip := toolkit.NewHBox()
	tabStrip.Spacing = -1 // contiguous tabs, like the old notebook strip
	for i, label := range tabLabels {
		idx := i
		b := toolkit.NewButton(label, nil)
		b.OnClick = func() { s.setActiveCard(idx) }
		if i == 0 {
			b.Style = toolkit.ButtonProminent // active tab
		}
		tabStrip.AddFixed(b, toolkit.NotebookTabWidth)
		s.tabButtons = append(s.tabButtons, b)
	}

	north := toolkit.NewVBox()
	north.Spacing = -1 // no gaps: the bands abut like the old fixed rects
	north.AddFixed(s.menuBar, toolkit.MenuBarH)
	north.AddFixed(s.toolbar, toolkit.ToolbarButtonH)
	north.AddFixed(tabStrip, toolkit.NotebookTabStripH)
	northH := toolkit.MenuBarH + toolkit.ToolbarButtonH + toolkit.NotebookTabStripH

	root := toolkit.NewContainer(toolkit.BorderLayout{})
	// Insert order sets Draw order: North first (an open menu dropdown paints
	// under the body, as before), then the cards, then the status bar on top.
	root.Add(toolkit.Item{Widget: north, Region: toolkit.RegionNorth, Size: northH})
	root.AddWidget(s.cards) // Center
	root.Add(toolkit.Item{Widget: s.status, Region: toolkit.RegionSouth, Size: toolkit.StatusbarH})
	root.SetBounds(toolkit.Rect{X: 0, Y: 0, W: w, H: h})
	s.root = root

	return s
}

// buildCards constructs the seven gallery cards and wires them into a
// CardLayout Container (card 0 active). Each card reproduces its family's
// historical widget rects declaratively: a VBox of fixed-height rows (fixed
// spacers between them) where each row is an HBox positioning the widget with
// rowLeft/rowInset/rowCenter.
func (s *State) buildCards() {
	// Card 0 — Button: a centred button with a click-count label below it.
	c0 := toolkit.NewVBox()
	c0.Spacing = -1
	c0.AddFixed(spacer(), 20)
	c0.AddFixed(rowCenter(120, s.helloButton), 28)
	c0.AddFixed(spacer(), 12)
	c0.AddFixed(rowCenter(160, s.clickLabel), 20)
	c0.AddFlex(spacer(), 1)

	// Card 1 — Toggles: two checks, two radios, a dropdown, left-anchored.
	c1 := toolkit.NewVBox()
	c1.Spacing = -1
	c1.AddFixed(spacer(), 8)
	c1.AddFixed(rowLeft(8, 200, s.check1), 24)
	c1.AddFixed(rowLeft(8, 200, s.check2), 24)
	c1.AddFixed(spacer(), 4)
	c1.AddFixed(rowLeft(8, 120, s.radioA), 20)
	c1.AddFixed(rowLeft(8, 120, s.radioB), 20)
	c1.AddFixed(spacer(), 10)
	c1.AddFixed(rowLeft(8, 150, s.dropdown), 24)
	c1.AddFlex(spacer(), 1)

	// Card 2 — Input: an entry above a flex-filling text view (20px bottom pad).
	c2 := toolkit.NewVBox()
	c2.Spacing = -1
	c2.AddFixed(spacer(), 8)
	c2.AddFixed(rowInset(8, s.entry), 24)
	c2.AddFixed(spacer(), 8)
	c2.AddFlex(rowInset(8, s.textView), 1)
	c2.AddFixed(spacer(), 20)

	// Card 3 — Tree+List: two flex columns side by side, 8px gutters/margins.
	c3 := toolkit.NewVBox()
	c3.Spacing = -1
	c3.AddFixed(spacer(), 8)
	treeRow := toolkit.NewHBox()
	treeRow.Spacing = -1
	treeRow.AddFixed(spacer(), 8)
	treeRow.AddFlex(s.tree, 1)
	treeRow.AddFixed(spacer(), 8)
	treeRow.AddFlex(s.listBox, 1)
	treeRow.AddFixed(spacer(), 8)
	c3.AddFlex(treeRow, 1)
	c3.AddFixed(spacer(), 8)

	// Card 4 — Calendar: a centred, flex-tall calendar (8px top/bottom pad).
	c4 := toolkit.NewVBox()
	c4.Spacing = -1
	c4.AddFixed(spacer(), 8)
	c4.AddFlex(rowCenter(200, s.calendar), 1)
	c4.AddFixed(spacer(), 8)

	// Card 5 — Color: a full-bleed 100px colour chooser near the top.
	c5 := toolkit.NewVBox()
	c5.Spacing = -1
	c5.AddFixed(spacer(), 8)
	c5.AddFixed(rowInset(8, s.colorPick), 100)
	c5.AddFlex(spacer(), 1)

	// Card 6 — Feedback: a progress bar, a scale, and a fixed-width spin button.
	c6 := toolkit.NewVBox()
	c6.Spacing = -1
	c6.AddFixed(spacer(), 20)
	c6.AddFixed(rowInset(16, s.progress), 18)
	c6.AddFixed(spacer(), 22)
	c6.AddFixed(rowInset(16, s.scale), 20)
	c6.AddFixed(spacer(), 20)
	c6.AddFixed(rowLeft(16, 100, s.spin), 24)
	c6.AddFlex(spacer(), 1)

	s.cardLayout = &toolkit.CardLayout{Active: 0}
	s.cards = toolkit.NewContainer(s.cardLayout)
	for _, card := range []toolkit.Widget{c0, c1, c2, c3, c4, c5, c6} {
		s.cards.AddWidget(card)
	}
}

// setActiveCard shows the i-th gallery card: it points the CardLayout at i,
// re-arranges the cards container (so the newly-active card gets bounds and the
// rest collapse), and re-styles the tab strip so the active tab reads as
// prominent. Invoked by every tab button's OnClick.
func (s *State) setActiveCard(i int) {
	s.cardLayout.Active = i
	s.cards.SetBounds(s.cards.Bounds()) // re-run CardLayout with the new Active
	for j, b := range s.tabButtons {
		if j == i {
			b.Style = toolkit.ButtonProminent
		} else {
			b.Style = toolkit.ButtonDefault
		}
	}
}

func buildMenu(items []toolkit.MenuItem) *toolkit.Menu {
	return toolkit.NewMenu(items)
}

// SetFrameSetter wires the callback the Frame-menu Actions invoke.
// Called by main.go with a closure over the SDK's setFrame method
// (which posts a set_frame wire message to the compositor). Nil is
// a valid value (native unit tests take the no-op path).
func (s *State) SetFrameSetter(fn func(name string)) { s.setFrame = fn }

// SetActiveFrame records the currently-active Frame name so the
// Frame menu can mark it with "* " AND the status bar's frame
// segment stays in sync. Called by main.go at boot with the URL's
// ?frame= value (or "openbox" fallback). Also called on every
// Frame-menu click. Rebuilds the Frame menu + updates status
// segment[3] so the marker + readout paint on the next Draw.
func (s *State) SetActiveFrame(name string) {
	s.activeFrame = name
	if s.status != nil {
		s.status.SetSegment(3, "frame: "+name)
	}
	if s.menuBar == nil || len(s.menuBar.Menus) < 4 {
		return
	}
	items := make([]toolkit.MenuItem, 0, len(frameNames))
	for _, n := range frameNames {
		picked := n
		label := picked
		if picked == s.activeFrame {
			label = "* " + picked
		}
		items = append(items, toolkit.MenuItem{
			Label: label,
			Action: func() {
				if s.setFrame != nil {
					s.setFrame(picked)
				}
				s.SetActiveFrame(picked)
			},
		})
	}
	s.menuBar.Menus[3] = toolkit.NewMenu(items)
}

// frameNames mirrors the compositor's FrameRegistry::TABLE key order
// (wasmbox/compositor/02_frame.rb). Kept in sync manually — a
// mismatch causes a click on a missing name to be dropped by the
// compositor's set_frame arm (:ignored result), no crash. Update
// here when a new frame lands in the compositor.
var frameNames = []string{
	"openbox", "aqua",
	"openbox-adwaita-light", "openbox-adwaita-dark",
	"openbox-juno",
	"openbox-whitesur-light", "openbox-whitesur-dark",
	"openbox-solarized-light", "openbox-solarized-dark",
	"aqua-adwaita-light", "aqua-adwaita-dark",
	"aqua-juno",
	"aqua-whitesur-light", "aqua-whitesur-dark",
	"aqua-solarized-light", "aqua-solarized-dark",
}

// setActiveThemeName updates the status bar's theme segment. Called from
// every View-menu Action so the user always sees which palette is live.
// Segment index 2 is the theme slot (see New()); the status bar is
// created before the menu Actions run, so this assumes s.status is
// already wired.
func (s *State) setActiveThemeName(name string) {
	if s.status == nil {
		return
	}
	s.status.SetSegment(2, "theme: "+name)
}

// Render paints the full scene into buf (a 4*W*H RGBA byte slice): a background
// fill, then the whole container tree in one Draw.
func Render(s *State, buf []byte) {
	r := toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H}
	fill(buf, s.W, r, s.theme.Background)
	p := painter.NewPixelPainter(buf, s.W, s.H)
	s.root.Draw(p, s.theme)
}

// HandleMouse delivers a click at (x, y) by routing it through the container
// tree (translated into the root's local space); the matched leaf — a tab
// button, a menu, a demo widget — fires its own handler. Always returns true
// (the compositor re-renders on every click, as before).
func (s *State) HandleMouse(x, y int) bool {
	rb := s.root.Bounds()
	s.root.OnEvent(toolkit.Event{Kind: toolkit.EventClick, X: x - rb.X, Y: y - rb.Y})
	return true
}

// HandleKey delivers a keydown to the focused widget (the TextView on
// the Input card). Returns true when the Input card is active (the key
// was consumed), false otherwise.
func (s *State) HandleKey(code string) bool {
	if s.cardLayout.Active == inputCard {
		s.textView.Focused().Set(true)
		s.textView.OnEvent(toolkit.Event{Kind: toolkit.EventKeyDown, Code: code})
		return true
	}
	return false
}

// HandleChar delivers a printable rune sequence to the TextView.
func (s *State) HandleChar(text string) bool {
	if s.cardLayout.Active == inputCard {
		s.textView.OnEvent(toolkit.Event{Kind: toolkit.EventChar, Code: text})
		return true
	}
	return false
}

// --- helpers --------------------------------------------------------------

func fill(buf []byte, surfaceW int, r toolkit.Rect, c toolkit.RGBA) {
	for j := 0; j < r.H; j++ {
		for i := 0; i < r.W; i++ {
			off := ((r.Y+j)*surfaceW + (r.X + i)) * 4
			if off+3 >= len(buf) {
				return
			}
			buf[off+0] = c.R
			buf[off+1] = c.G
			buf[off+2] = c.B
			buf[off+3] = c.A
		}
	}
}

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
