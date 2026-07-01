// SPDX-License-Identifier: BSD-3-Clause
//
// Package scene renders the wasmdesk/toolkit-based Calculator: a display
// Entry across the top and a 5×4 button grid below (0..9, . + - * / = C ± %).
// A real toolkit consumer with tighter scope than the multi-tab
// showcase — validates that Grid + Button + Entry compose cleanly in a
// production layout, and that click handling flows through a chain of
// (button → scene handler → display update).

package scene

import (
	"strconv"

	"github.com/wasmdesk/toolkit"
)

// State bundles every widget + the calculator's arithmetic model.
type State struct {
	W, H int

	theme   *toolkit.Theme
	display *toolkit.Entry
	buttons []*toolkit.Button

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
	{"C", "±", "%", "/"},
	{"7", "8", "9", "*"},
	{"4", "5", "6", "-"},
	{"1", "2", "3", "+"},
	{"0", ".", "=", ""}, // last cell empty (the "=" spans nothing extra in v1)
}

// New builds a Calculator sized W×H.
func New(w, h int) *State {
	s := &State{W: w, H: h, theme: toolkit.DefaultLight()}
	s.display = toolkit.NewEntry("0")
	s.display.SetBounds(toolkit.Rect{X: sideMargin, Y: buttonPadTop, W: w - 2*sideMargin, H: displayH})

	baseY := buttonPadTop + displayH + buttonGap
	for r := 0; r < 5; r++ {
		for c := 0; c < 4; c++ {
			label := keys[r][c]
			if label == "" {
				continue
			}
			b := toolkit.NewButton(label, nil)
			b.SetBounds(toolkit.Rect{
				X: sideMargin + c*(colW+buttonGap),
				Y: baseY + r*(rowH+buttonGap),
				W: colW,
				H: rowH,
			})
			key := label // capture
			b.OnClick = func() { s.press(key) }
			s.buttons = append(s.buttons, b)
		}
	}
	return s
}

// Render paints every widget.
func Render(s *State, buf []byte) {
	fill(buf, s.W, toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H}, s.theme.Background)
	s.display.Draw(buf, s.W, s.theme)
	for _, b := range s.buttons {
		b.Draw(buf, s.W, s.theme)
	}
}

// HandleMouse dispatches to whichever button contains (x, y). Returns
// true if the scene should re-render.
func (s *State) HandleMouse(x, y int) bool {
	for _, b := range s.buttons {
		if insideRect(x, y, b.Bounds()) {
			ev := toolkit.Event{Kind: toolkit.EventClick, X: x, Y: y}
			b.OnEvent(ev)
			return true
		}
	}
	return false
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
	case "±":
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

func insideRect(x, y int, r toolkit.Rect) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}
