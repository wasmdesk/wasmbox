// SPDX-License-Identifier: BSD-3-Clause
//
// Package scene renders the wasmdesk/toolkit-based Calculator: a display
// Entry across the top and a 5×4 button grid below (0..9, . + - * / = C +/- %).
// A real toolkit consumer with tighter scope than the multi-tab
// showcase — validates that Grid + Button + Entry compose cleanly in a
// production layout, and that click handling flows through a chain of
// (button → scene handler → display update).
//
// Layout is built from the toolkit's box-layout container model (VBox for the
// scene column + a toolkit.Grid for the keys) rather than hand-computed
// toolkit.Rect placement: the display is a fixed-height row at the top and the
// button grid fills the rest, each button an Attach'd cell of a fixed-track
// Grid. A single root.SetBounds lays the whole tree out, root.Draw paints it,
// and root.OnEvent routes clicks into child-local space — no per-widget rect
// arithmetic and no manual hit-testing.
//
// Text is anti-aliased, shaped OpenType (toolkit.UseOpenTypeText, shipped in
// toolkit v0.77.0): Calculator is the pilot client for the toolkit's bundled
// Atkinson Hyperlegible face at DefaultOpenTypeSizePx (16px). The switch is a
// single process-global opt-in, made once from New via aaOnce, so the running
// wasm client and the native scene tests both render (and assert) against the
// vector face. Every other wasmbox client stays on the compiled-in 5x7 bitmap
// default, since they never call UseOpenTypeText. The fixed Grid/VBox tracks
// (colW/rowH/displayH) are font-independent, so widget rects are unchanged; only
// the glyph pixels differ — the taller 20px AA face is auto-centred in each
// cell by Button/Entry (r.H-glyphHeight())/2 vertical placement.

package scene

import (
	"strconv"
	"sync"

	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
)

// aaOnce flips the toolkit's active font to anti-aliased, shaped OpenType text
// exactly once for this client process. It is deliberately package-scoped (not
// per-State) because SetFont is a process-global: re-parsing the bundled face
// on every New would be wasteful, and a single opt-in matches the toolkit's
// "flip it once at start-up" contract.
var aaOnce sync.Once

// enableAAText installs the toolkit's bundled AA/shaped face. The bundled face
// never fails to parse (the error is documented as never-returned), and on the
// impossible error path the toolkit leaves the still-working bitmap default
// active — so a swallowed error degrades to legible bitmap text, never to no
// text. Split out so its single call site in New stays a one-liner.
func enableAAText() { aaOnce.Do(func() { _ = toolkit.UseOpenTypeText() }) }

// State bundles every widget + the calculator's arithmetic model.
type State struct {
	W, H int

	theme   *toolkit.Theme
	root    *toolkit.VBox
	display *toolkit.Entry
	buttons []*toolkit.Button

	// dirty is set by a button's OnClick when a press mutates the model, so
	// HandleMouse can report to the render loop whether a click did anything
	// (the container routing itself has no hit/miss return value).
	dirty bool

	// Model. accum is the "left" operand once an op is picked; op is
	// the pending operator ('+', '-', '*', '/', or 0 for none). When
	// the user types digits AFTER pressing an op, we're editing the
	// right operand — display shows it. Pressing '=' folds accum ⊕ display
	// back into display + clears op.
	accum   float64
	op      byte
	freshOp bool // true right after an op → next digit starts a new number
}

// button-grid geometry.
const (
	displayH     = 32
	rowH         = 40
	colW         = 60
	buttonGap    = 4
	buttonPadTop = 4
	sideMargin   = 8
)

// keys layout (5 rows, 4 cols). Empty cells are gaps.
var keys = [5][4]string{
	{"C", "+/-", "%", "/"},
	{"7", "8", "9", "*"},
	{"4", "5", "6", "-"},
	{"1", "2", "3", "+"},
	{"0", ".", "=", ""}, // last cell empty (the "=" spans nothing extra in v1)
}

// New builds a Calculator sized W×H.
//
// The widget tree mirrors the visual structure declaratively:
//
//	root  VBox (Spacing=buttonGap)             — the whole scene
//	├─ displayRow  HBox (fixed height displayH)
//	│  ├─ display  Entry (fixed width w-2*sideMargin)
//	│  └─ spacer   empty Container (flex)       — the display's right inset
//	└─ grid  Grid 4×5 (flex, Spacing=buttonGap)
//	   └─ button ×N attached at (col, row); fixed colW×rowH tracks
//
// The button grid is a single toolkit.Grid (available from toolkit v0.64.0,
// which added Spacing gutters + fixed Col/RowWidths tracks) rather than a
// VBox-of-HBox workaround: Spacing carries the buttonGap gutter on both axes
// and the fixed ColWidths/RowHeights tracks pin each cell to the 60×40 key
// size. gridTracks then places cell (c, r) at offset c*(colW+buttonGap) /
// r*(rowH+buttonGap) — byte-identical to the old per-widget arithmetic. The
// grid is a full-width flex child of root, which keeps the right-most button
// column (which reaches the surface edge, wider than the inset display) fully
// hit-testable.
func New(w, h int) *State {
	enableAAText() // Calculator pilots the toolkit's AA/shaped OpenType face.
	s := &State{W: w, H: h, theme: toolkit.WhiteSurLight()}

	// Display row: the Entry at its historical width (w-2*sideMargin) plus a
	// flex spacer that carries the right-hand inset, so the Entry lands at the
	// exact same rect as the old hand-placed one without an Align dependency.
	s.display = toolkit.NewEntry("0")
	displayRow := toolkit.NewHBox()
	displayRow.AddFixed(s.display, w-2*sideMargin)
	displayRow.AddFlex(toolkit.NewContainer(nil), 1) // invisible right-margin spacer

	// Button grid: a 4-col × 5-row toolkit.Grid with buttonGap gutters and every
	// track pinned to the 60×40 key size, so each cell (c, r) lands at the exact
	// same rect the old VBox-of-HBox produced — Attach places the buttons, no
	// per-widget rect arithmetic.
	grid := toolkit.NewGrid(4, 5)
	grid.Spacing = buttonGap
	grid.ColWidths = []int{colW, colW, colW, colW}
	grid.RowHeights = []int{rowH, rowH, rowH, rowH, rowH}
	for r := 0; r < 5; r++ {
		for c := 0; c < 4; c++ {
			label := keys[r][c]
			if label == "" {
				continue
			}
			b := toolkit.NewButton(label, nil)
			b.Style = keyStyle(label)
			key := label // capture
			b.OnClick = func() {
				s.press(key)
				s.dirty = true
			}
			grid.Attach(b, c, r)
			s.buttons = append(s.buttons, b)
		}
	}

	root := toolkit.NewVBox()
	root.Spacing = buttonGap
	root.AddFixed(displayRow, displayH)
	root.AddFlex(grid, 1)
	// One layout pass positions the entire tree; the content region is inset by
	// the top pad + left margin, and spans to the surface's right/bottom edge
	// (the right-most button column reaches w).
	root.SetBounds(toolkit.Rect{X: sideMargin, Y: buttonPadTop, W: w - sideMargin, H: h - buttonPadTop})
	s.root = root
	return s
}

// keyStyle assigns each calculator key its macOS button hierarchy: operators
// and "=" are prominent (accent), the top-row functions (C, +/-, %) are
// secondary (muted grey), and the digits + "." stay the default surface keys.
func keyStyle(label string) toolkit.ButtonStyle {
	switch label {
	case "/", "*", "-", "+", "=":
		return toolkit.ButtonProminent
	case "C", "+/-", "%":
		return toolkit.ButtonSecondary
	default:
		return toolkit.ButtonDefault
	}
}

// Render paints the whole scene: a background fill, then the container tree.
func Render(s *State, buf []byte) {
	p := painter.NewPixelPainter(buf, s.W, s.H)
	fill(buf, s.W, toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H}, s.theme.Background)
	s.root.Draw(p, s.theme)
}

// HandleMouse routes a click at surface coordinates (x, y) through the
// container tree (translated into the root's local space); a button whose
// bounds contain the point fires its OnClick, which sets dirty. Returns true
// when a click triggered an action (the scene should re-render).
func (s *State) HandleMouse(x, y int) bool {
	s.dirty = false
	rb := s.root.Bounds()
	s.root.OnEvent(toolkit.Event{Kind: toolkit.EventClick, X: x - rb.X, Y: y - rb.Y})
	return s.dirty
}

// HandleKey maps a keyboard event's Code string to the equivalent
// calculator key + calls press(). Returns true if the key mapped to
// an action (and the scene should re-render), false otherwise.
//
// Supported:
//   - "0".."9"        → digit entry
//   - "."             → decimal
//   - "+" / "-" / "*" / "/" → binary ops
//   - "Enter" / "="   → evaluate
//   - "Escape" / "Delete" / "c" / "C" → clear
//   - "Backspace"     → also clears (pocket calculator has no backspace)
//   - "%"             → percent
//   - single-char letters map to the corresponding calculator label
//     when the label is exactly the character (e.g. "C" for clear)
func (s *State) HandleKey(code string) bool {
	switch code {
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		s.press(code)
		return true
	case ".":
		s.press(".")
		return true
	case "+", "-", "*", "/":
		s.press(code)
		return true
	case "Enter", "=":
		s.press("=")
		return true
	case "Escape", "Delete", "Backspace", "c", "C":
		s.press("C")
		return true
	case "%":
		s.press("%")
		return true
	}
	return false
}

// HandleChar is the printable-character sibling of HandleKey. Some
// SDKs deliver digits as keydown Codes, others as char events —
// support both routes.
func (s *State) HandleChar(text string) bool {
	if text == "" {
		return false
	}
	return s.HandleKey(text)
}

// press applies one key. Split out so scene_test can drive the model
// directly without going through a synthetic mouse-hit-test.
func (s *State) press(key string) {
	switch key {
	case "C":
		s.accum = 0
		s.op = 0
		s.freshOp = false
		s.display.Text = "0"
	case "+/-":
		if v, err := strconv.ParseFloat(s.display.Text, 64); err == nil {
			s.display.Text = formatNumber(-v)
		}
	case "%":
		if v, err := strconv.ParseFloat(s.display.Text, 64); err == nil {
			s.display.Text = formatNumber(v / 100)
		}
	case "+", "-", "*", "/":
		if v, err := strconv.ParseFloat(s.display.Text, 64); err == nil {
			s.accum = v
		}
		s.op = key[0]
		s.freshOp = true
	case "=":
		if s.op == 0 {
			return
		}
		right, err := strconv.ParseFloat(s.display.Text, 64)
		if err != nil {
			return
		}
		result := apply(s.accum, right, s.op)
		s.display.Text = formatNumber(result)
		s.accum = result
		s.op = 0
		s.freshOp = false
	default:
		// A digit or "."
		if s.freshOp || s.display.Text == "0" {
			if key == "." {
				s.display.Text = "0."
			} else {
				s.display.Text = key
			}
			s.freshOp = false
			return
		}
		if key == "." {
			// Only one decimal per number.
			for i := 0; i < len(s.display.Text); i++ {
				if s.display.Text[i] == '.' {
					return
				}
			}
		}
		s.display.Text += key
	}
}

// apply computes lhs op rhs. Division by zero returns +Inf so display
// still shows something (formatNumber trims to "Inf") — matches
// legacy pocket-calculator UX.
func apply(lhs, rhs float64, op byte) float64 {
	switch op {
	case '+':
		return lhs + rhs
	case '-':
		return lhs - rhs
	case '*':
		return lhs * rhs
	case '/':
		if rhs == 0 {
			return 0 // pragmatic: pocket-calc reset-to-0 on divide-by-zero
		}
		return lhs / rhs
	}
	return rhs
}

// formatNumber prints v with strconv.FormatFloat's -1 precision (shortest
// roundtrip). Special-cases integers so 2+3 shows "5", not "5.0".
func formatNumber(v float64) string {
	i := int64(v)
	if float64(i) == v {
		return strconv.FormatInt(i, 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
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
