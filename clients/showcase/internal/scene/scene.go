// SPDX-License-Identifier: BSD-3-Clause
//
// Package scene renders the wasmdesk/toolkit showcase: one window holding
// a MenuBar + Toolbar + a Notebook of tabs, each tab exercising a
// different widget family. The scene's only purpose is to drive the
// toolkit end-to-end in production conditions — the wasm main below
// pipes input events in, the scene composes widgets and writes pixels.
//
// scene is pure Go (no syscall/js) so the painter + every widget can
// be exercised in native unit tests too.
package scene

import (
	"github.com/wasmdesk/toolkit"
)

// State is the showcase model. The toolkit composition lives here so
// the scene_test.go file can poke widgets without going through the
// wasm input pipe.
type State struct {
	W, H int

	theme    *toolkit.Theme
	menuBar  *toolkit.MenuBar
	toolbar  *toolkit.Toolbar
	notebook *toolkit.Notebook
	status   *toolkit.Statusbar

	helloButton *toolkit.Button
	clickCount  int
	clickLabel  *toolkit.Label
	check1      *toolkit.CheckButton
	check2      *toolkit.CheckButton
	radioGroup  *toolkit.RadioGroup
	radioA      *toolkit.RadioButton
	radioB      *toolkit.RadioButton
	dropdown    *toolkit.DropDown
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

// New builds a fully-wired showcase State sized W x H.
func New(w, h int) *State {
	s := &State{W: w, H: h, theme: toolkit.DefaultLight()}

	// MenuBar — the View menu is built from the embedded GTK themes so
	// the user can flip palettes live (validates the toolkit's
	// LoadGTKTheme end-to-end).
	themeItems := make([]toolkit.MenuItem, 0, 8)
	for _, t := range Themes() {
		picked := t // capture loop var
		themeItems = append(themeItems, toolkit.MenuItem{
			Label:  picked.Name,
			Action: func() { s.theme = picked.Theme },
		})
	}
	s.menuBar = toolkit.NewMenuBar()
	s.menuBar.Names = []string{"File", "Edit", "View", "Help"}
	s.menuBar.Menus = []*toolkit.Menu{
		buildMenu([]toolkit.MenuItem{{Label: "New"}, {Label: "Open"}, {Separator: true}, {Label: "Quit"}}),
		buildMenu([]toolkit.MenuItem{{Label: "Cut"}, {Label: "Copy"}, {Label: "Paste"}}),
		buildMenu(themeItems),
		buildMenu([]toolkit.MenuItem{{Label: "About"}}),
	}
	s.menuBar.SetBounds(toolkit.Rect{X: 0, Y: 0, W: w, H: toolkit.MenuBarH})

	// Toolbar.
	s.toolbar = toolkit.NewToolbar([]toolkit.ToolbarItem{
		{Label: "N"}, {Label: "O"}, {Label: "S"}, {Separator: true},
		{Label: "C"}, {Label: "V"}, {Label: "X"}, {Separator: true},
		{Label: "?"},
	})
	s.toolbar.SetBounds(toolkit.Rect{X: 0, Y: toolkit.MenuBarH, W: w, H: toolkit.ToolbarButtonH})

	// Tab widgets.
	s.helloButton = toolkit.NewButton("Click me", nil)
	s.clickLabel = &toolkit.Label{Text: "clicked 0 times"}
	s.helloButton.OnClick = func() {
		s.clickCount++
		s.clickLabel.Text = "clicked " + itoa(s.clickCount) + " times"
	}
	s.check1 = toolkit.NewCheckButton("Wrap long lines", true)
	s.check2 = toolkit.NewCheckButton("Show line numbers", false)
	s.radioGroup = toolkit.NewRadioGroup()
	s.radioA = toolkit.NewRadioButton("Spaces")
	s.radioB = toolkit.NewRadioButton("Tabs")
	s.radioGroup.Add(s.radioA)
	s.radioGroup.Add(s.radioB)
	s.radioA.Checked = true
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

	// Notebook (one tab per family). The "page" passed to AddTab is
	// the *primary* widget; auxiliaries are painted on top in Render.
	s.notebook = toolkit.NewNotebook()
	s.notebook.AddTab("Button", s.helloButton)
	s.notebook.AddTab("Toggles", s.check1)
	s.notebook.AddTab("Input", s.entry)
	s.notebook.AddTab("Tree+List", s.tree)
	s.notebook.AddTab("Calendar", s.calendar)
	s.notebook.AddTab("Color", s.colorPick)
	s.notebook.AddTab("Feedback", s.progress)

	bodyY := toolkit.MenuBarH + toolkit.ToolbarButtonH
	statusH := toolkit.StatusbarH
	bodyH := h - bodyY - statusH
	s.notebook.SetBounds(toolkit.Rect{X: 0, Y: bodyY, W: w, H: bodyH})

	s.status = toolkit.NewStatusbar([]string{
		"34 widgets", "100% cov", "v0.4", "wasmdesk/toolkit",
	})
	s.status.SetBounds(toolkit.Rect{X: 0, Y: h - statusH, W: w, H: statusH})

	// Lay out tab contents inside the Notebook body.
	tabBodyY := bodyY + toolkit.NotebookTabStripH
	tabBodyH := bodyH - toolkit.NotebookTabStripH
	s.helloButton.SetBounds(toolkit.Rect{X: w/2 - 60, Y: tabBodyY + 20, W: 120, H: 28})
	s.clickLabel.SetBounds(toolkit.Rect{X: w/2 - 80, Y: tabBodyY + 60, W: 160, H: 20})
	s.check1.SetBounds(toolkit.Rect{X: 8, Y: tabBodyY + 8, W: 200, H: 24})
	s.check2.SetBounds(toolkit.Rect{X: 8, Y: tabBodyY + 32, W: 200, H: 24})
	s.radioA.SetBounds(toolkit.Rect{X: 8, Y: tabBodyY + 60, W: 120, H: 20})
	s.radioB.SetBounds(toolkit.Rect{X: 8, Y: tabBodyY + 80, W: 120, H: 20})
	s.dropdown.SetBounds(toolkit.Rect{X: 8, Y: tabBodyY + 110, W: 150, H: 24})
	s.entry.SetBounds(toolkit.Rect{X: 8, Y: tabBodyY + 8, W: w - 16, H: 24})
	s.textView.SetBounds(toolkit.Rect{X: 8, Y: tabBodyY + 40, W: w - 16, H: tabBodyH - 60})
	s.tree.SetBounds(toolkit.Rect{X: 8, Y: tabBodyY + 8, W: (w - 24) / 2, H: tabBodyH - 16})
	s.listBox.SetBounds(toolkit.Rect{X: 16 + (w-24)/2, Y: tabBodyY + 8, W: (w - 24) / 2, H: tabBodyH - 16})
	s.calendar.SetBounds(toolkit.Rect{X: w/2 - 100, Y: tabBodyY + 8, W: 200, H: tabBodyH - 16})
	s.colorPick.SetBounds(toolkit.Rect{X: 8, Y: tabBodyY + 8, W: w - 16, H: 100})
	s.progress.SetBounds(toolkit.Rect{X: 16, Y: tabBodyY + 20, W: w - 32, H: 18})
	s.scale.SetBounds(toolkit.Rect{X: 16, Y: tabBodyY + 60, W: w - 32, H: 20})
	s.spin.SetBounds(toolkit.Rect{X: 16, Y: tabBodyY + 100, W: 100, H: 24})

	return s
}

func buildMenu(items []toolkit.MenuItem) *toolkit.Menu {
	return toolkit.NewMenu(items)
}

// Render paints the full scene into buf (a 4*W*H RGBA byte slice).
func Render(s *State, buf []byte) {
	r := toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H}
	fill(buf, s.W, r, s.theme.Background)

	s.menuBar.Draw(buf, s.W, s.theme)
	s.toolbar.Draw(buf, s.W, s.theme)
	s.notebook.Draw(buf, s.W, s.theme)

	switch s.notebook.Active {
	case 0:
		s.clickLabel.Draw(buf, s.W, s.theme)
	case 1:
		s.check2.Draw(buf, s.W, s.theme)
		s.radioA.Draw(buf, s.W, s.theme)
		s.radioB.Draw(buf, s.W, s.theme)
		s.dropdown.Draw(buf, s.W, s.theme)
	case 2:
		s.textView.Draw(buf, s.W, s.theme)
	case 3:
		s.listBox.Draw(buf, s.W, s.theme)
	case 6:
		s.scale.Draw(buf, s.W, s.theme)
		s.spin.Draw(buf, s.W, s.theme)
	}
	s.status.Draw(buf, s.W, s.theme)
}

// HandleMouse delivers a click at (x, y). Returns true if the scene
// should re-render.
func (s *State) HandleMouse(x, y int) bool {
	ev := toolkit.Event{Kind: toolkit.EventClick, X: x, Y: y}
	switch {
	case insideRect(x, y, s.menuBar.Bounds()):
		s.menuBar.OnEvent(localize(ev, s.menuBar.Bounds()))
	case insideRect(x, y, s.toolbar.Bounds()):
		s.toolbar.OnEvent(localize(ev, s.toolbar.Bounds()))
	case insideRect(x, y, s.status.Bounds()):
	default:
		s.notebook.OnEvent(localize(ev, s.notebook.Bounds()))
	}
	return true
}

// HandleKey delivers a keydown to the focused widget (the TextView on
// tab 2 of the showcase).
func (s *State) HandleKey(code string) bool {
	if s.notebook.Active == 2 {
		s.textView.Focused = true
		s.textView.OnEvent(toolkit.Event{Kind: toolkit.EventKeyDown, Code: code})
		return true
	}
	return false
}

// HandleChar delivers a printable rune sequence to the TextView.
func (s *State) HandleChar(text string) bool {
	if s.notebook.Active == 2 {
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

func insideRect(x, y int, r toolkit.Rect) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

func localize(ev toolkit.Event, r toolkit.Rect) toolkit.Event {
	ev.X -= r.X
	ev.Y -= r.Y
	return ev
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
