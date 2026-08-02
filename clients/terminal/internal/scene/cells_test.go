// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import (
	"testing"

	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
)

// rune at (col,row) via the embedded TerminalView, for terse assertions.
func at(g *Grid, col, row int) rune { return g.Cell(col, row).Rune }

// NewGrid stores dimensions verbatim and allocates Cols*Rows cells with the
// default green pen + a visible block cursor.
func TestNewGridDims(t *testing.T) {
	g := NewGrid(40, 25)
	if g.Cols != 40 || g.Rows != 25 || len(g.Cells) != 40*25 {
		t.Fatalf("NewGrid(40,25): cols=%d rows=%d cells=%d", g.Cols, g.Rows, len(g.Cells))
	}
	if !g.CursorVisible {
		t.Fatal("NewGrid should make the block cursor visible")
	}
	if g.DefaultFG != inkGreen || g.DefaultBG != panelBG {
		t.Fatalf("NewGrid pen = fg %+v bg %+v, want green ink on panel", g.DefaultFG, g.DefaultBG)
	}
}

// NewGrid panics on a non-positive dimension (the toolkit enforces it) -- a
// zero-size buffer is an upstream bug, not a recoverable state.
func TestNewGridPanicsOnZero(t *testing.T) {
	for _, dim := range []struct{ c, r int }{{0, 1}, {1, 0}, {-1, 1}} {
		t.Run("", func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("NewGrid(%d,%d) did not panic", dim.c, dim.r)
				}
			}()
			NewGrid(dim.c, dim.r)
		})
	}
}

// Print at end of row wraps to the next row.
func TestPrintWrapsAtEndOfRow(t *testing.T) {
	g := NewGrid(3, 2)
	g.PrintString("abc")
	if g.CursorRow != 1 || g.CursorCol != 0 {
		t.Fatalf("after 'abc' on 3-col grid, cursor = (%d,%d), want (1,0)",
			g.CursorCol, g.CursorRow)
	}
	g.Print('d')
	if at(g, 0, 1) != 'd' {
		t.Fatalf("cell (0,1) = %q, want 'd'", at(g, 0, 1))
	}
}

// Print carries the current pen colour into the cell it writes.
func TestPrintUsesPenColour(t *testing.T) {
	g := NewGrid(4, 2)
	g.FG = 1 // cyan
	g.Print('P')
	c := g.Cell(0, 0)
	if c.Rune != 'P' || c.FG != inkCyan {
		t.Fatalf("Print with pen 1: cell = %+v, want 'P' in cyan", c)
	}
	// The pen is restored to the default ink after each Print so subsequent
	// writes are green again unless the caller re-sets FG.
	if g.DefaultFG != inkGreen {
		t.Fatalf("DefaultFG = %+v after Print, want restored to green", g.DefaultFG)
	}
}

// penColor clamps an out-of-range pen index to the default ink.
func TestPenColorFallback(t *testing.T) {
	if penColor(255) != inkGreen {
		t.Fatalf("penColor(255) = %+v, want default green", penColor(255))
	}
	if penColor(2) != inkRed {
		t.Fatalf("penColor(2) = %+v, want red", penColor(2))
	}
}

// Print overflowing the bottom triggers a scroll + parks the cursor on the
// last row.
func TestPrintScrollsOnOverflow(t *testing.T) {
	g := NewGrid(2, 2)
	// Fill four cells -- exhausts the grid. The next Print scrolls.
	g.PrintString("abcd")
	if g.CursorRow != 1 || g.CursorCol != 0 {
		t.Fatalf("expected cursor at (0,1) after wrap-onto-row-2, got (%d,%d)",
			g.CursorCol, g.CursorRow)
	}
	g.Print('e')
	// After scroll, the original row 1 (c,d) sits at row 0 and 'e' lands at (0,1).
	if at(g, 0, 0) != 'c' || at(g, 1, 0) != 'd' {
		t.Fatalf("after scroll, top row = %q%q, want 'cd'", at(g, 0, 0), at(g, 1, 0))
	}
	if at(g, 0, 1) != 'e' {
		t.Fatalf("after scroll, (0,1) = %q, want 'e'", at(g, 0, 1))
	}
}

// PrintString routes CR, LF, BS through their special-case paths.
func TestPrintStringControlBytes(t *testing.T) {
	g := NewGrid(8, 4)
	g.PrintString("ab\nc\rd\x08e")
	// 'ab' on row 0, '\n' moves to row 1 col 0, 'c' written at (0,1),
	// '\r' moves cursor to col 0 (overwrites 'c' with 'd'), '\x08' on col 1
	// pops back to col 0 (deleting 'd'), 'e' written at (0,1).
	if at(g, 0, 0) != 'a' || at(g, 1, 0) != 'b' {
		t.Fatalf("row 0 = %q%q, want 'ab'", at(g, 0, 0), at(g, 1, 0))
	}
	if at(g, 0, 1) != 'e' {
		t.Fatalf("row 1 col 0 = %q, want 'e'", at(g, 0, 1))
	}
}

// Backspace at column 0 is a no-op (line head sentinel).
func TestBackspaceAtColumnZero(t *testing.T) {
	g := NewGrid(4, 2)
	g.Backspace()
	if g.CursorCol != 0 || g.CursorRow != 0 {
		t.Fatalf("Backspace at (0,0) moved cursor to (%d,%d)", g.CursorCol, g.CursorRow)
	}
}

// Backspace clears the cell it steps back onto.
func TestBackspaceClearsCell(t *testing.T) {
	g := NewGrid(4, 2)
	g.PrintString("xy")
	g.Backspace()
	if at(g, 1, 0) != 0 {
		t.Fatalf("Backspace left cell (1,0) = %q, want cleared", at(g, 1, 0))
	}
	if g.CursorCol != 1 {
		t.Fatalf("cursor col = %d after Backspace, want 1", g.CursorCol)
	}
}

// CRLF wraps + scrolls at the bottom of the grid.
func TestCRLFScrolls(t *testing.T) {
	g := NewGrid(2, 2)
	g.PrintString("xy")
	g.CRLF() // cursor at (0,1)
	g.PrintString("zw")
	g.CRLF() // overflow: scroll, cursor parks at (0, Rows-1)
	if g.CursorRow != g.Rows-1 || g.CursorCol != 0 {
		t.Fatalf("after CRLF-overflow, cursor = (%d,%d), want (0,%d)",
			g.CursorCol, g.CursorRow, g.Rows-1)
	}
}

// Clear wipes everything + homes the cursor.
func TestClear(t *testing.T) {
	g := NewGrid(4, 2)
	g.PrintString("xy")
	g.Clear()
	for i, c := range g.Cells {
		if c.Rune != 0 {
			t.Fatalf("cell[%d] = %q after Clear, want 0", i, c.Rune)
		}
	}
	if g.CursorCol != 0 || g.CursorRow != 0 {
		t.Fatalf("cursor after Clear = (%d,%d)", g.CursorCol, g.CursorRow)
	}
}

// Resize: growing keeps the existing content.
func TestResizeGrow(t *testing.T) {
	g := NewGrid(2, 2)
	g.PrintString("ab")
	g.Resize(4, 4)
	if g.Cols != 4 || g.Rows != 4 || len(g.Cells) != 16 {
		t.Fatalf("Resize(4,4): %dx%d cells=%d", g.Cols, g.Rows, len(g.Cells))
	}
	if at(g, 0, 0) != 'a' || at(g, 1, 0) != 'b' {
		t.Fatalf("Resize grow lost content: %q%q", at(g, 0, 0), at(g, 1, 0))
	}
}

// Resize: shrinking drops out-of-bounds content + clamps the cursor.
func TestResizeShrink(t *testing.T) {
	g := NewGrid(4, 4)
	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			g.SetCell(c, r, rune('A'+r*4+c), inkGreen, panelBG)
		}
	}
	g.CursorCol = 3
	g.CursorRow = 3
	g.Resize(2, 2)
	// Top-left 2x2 retained.
	if at(g, 0, 0) != 'A' || at(g, 1, 0) != 'B' ||
		at(g, 0, 1) != 'E' || at(g, 1, 1) != 'F' {
		t.Fatalf("Resize shrink lost top-left: %q%q%q%q",
			at(g, 0, 0), at(g, 1, 0), at(g, 0, 1), at(g, 1, 1))
	}
	if g.CursorCol != 1 || g.CursorRow != 1 {
		t.Fatalf("cursor not clamped: (%d,%d)", g.CursorCol, g.CursorRow)
	}
}

// Resize panics on non-positive dimensions (enforced by the toolkit).
func TestResizePanicsOnZero(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Resize(0, 1) did not panic")
		}
	}()
	g := NewGrid(2, 2)
	g.Resize(0, 1)
}

// Draw blits a written glyph in its pen colour and paints the block cursor.
// This is the cell-precision test: write a coloured string, then assert the
// exact pixels the TerminalView lays down (mirrors the toolkit's own
// TerminalView Draw test style).
func TestGridDrawGlyphAndCursor(t *testing.T) {
	g := NewGrid(3, 1)
	g.FG = 2 // red
	g.Print('R')
	// Cursor is now at (1,0). SetBounds derives the cell size from the scaled
	// font so Draw has a positive cell box to blit into.
	g.SetBounds(toolkit.Rect{X: 0, Y: 0, W: 3 * baseAdvance(), H: baseHeight()})
	cw, ch := g.CellWidth(), g.CellHeight()
	if cw <= 0 || ch <= 0 {
		t.Fatalf("cell size not derived: %dx%d", cw, ch)
	}
	buf := make([]byte, 4*(cw*3)*ch)
	p := painter.NewPixelPainter(buf, cw*3, ch)
	g.Draw(p, toolkit.DefaultLight())

	// Cell (0,0) 'R' in red: some pixel inside the cell box must be red.
	if !scan(buf, cw*3, 0, 0, cw, ch, inkRed) {
		t.Fatal("no red 'R' ink found in cell (0,0)")
	}
	// Cell (2,0) is empty + non-cursor: its top-left pixel is the panel bg.
	if pix(buf, cw*3, 2*cw, 0) != panelBG {
		t.Fatalf("empty cell (2,0) bg = %+v, want panel", pix(buf, cw*3, 2*cw, 0))
	}
	// Cell (1,0) is the block cursor: empty + inverted, so the whole cell is
	// the (green) ink -- its top-left pixel equals inkGreen.
	if pix(buf, cw*3, cw, 0) != inkGreen {
		t.Fatalf("cursor cell top-left = %+v, want inverted green block",
			pix(buf, cw*3, cw, 0))
	}
}

// --- pixel helpers (mirror the toolkit's own test helpers) ----------------

func baseAdvance() int { return toolkit.NewBitmapFont(terminalScale).Advance() }
func baseHeight() int  { return toolkit.NewBitmapFont(terminalScale).Height() }

func pix(buf []byte, w, x, y int) toolkit.RGBA {
	o := (y*w + x) * 4
	return toolkit.RGBA{R: buf[o], G: buf[o+1], B: buf[o+2], A: buf[o+3]}
}

func scan(buf []byte, w, x0, y0, cw, ch int, c toolkit.RGBA) bool {
	for y := y0; y < y0+ch; y++ {
		for x := x0; x < x0+cw; x++ {
			if pix(buf, w, x, y) == c {
				return true
			}
		}
	}
	return false
}
