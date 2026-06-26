// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import "testing"

// NewGrid stores dimensions verbatim and allocates Cols*Rows cells.
func TestNewGridDims(t *testing.T) {
	g := NewGrid(40, 25)
	if g.Cols != 40 || g.Rows != 25 || len(g.Cells) != 40*25 {
		t.Fatalf("NewGrid(40,25): cols=%d rows=%d cells=%d", g.Cols, g.Rows, len(g.Cells))
	}
}

// NewGrid panics on a non-positive dimension -- a zero-size buffer is an
// upstream bug, not a recoverable state.
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
	if g.Cells[3].Glyph != 'd' {
		t.Fatalf("cell[3] = %q, want 'd'", g.Cells[3].Glyph)
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
	// After scroll, the original row 1 (c,d) sits at row 0 and 'e' lands at (1,0).
	if g.Cells[0].Glyph != 'c' || g.Cells[1].Glyph != 'd' {
		t.Fatalf("after scroll, top row = %q%q, want 'cd'",
			g.Cells[0].Glyph, g.Cells[1].Glyph)
	}
}

// PrintString routes CR, LF, BS through their special-case methods.
func TestPrintStringControlBytes(t *testing.T) {
	g := NewGrid(8, 4)
	g.PrintString("ab\nc\rd\x08e")
	// 'ab' on row 0, '\n' moves to row 1 col 0, 'c' written at (0,1),
	// '\r' moves cursor to col 0 (overwrites 'c' with 'd'), '\x08' on col 1
	// pops back to col 0 (deleting 'd'), 'e' written at (0,1).
	if g.Cells[0].Glyph != 'a' || g.Cells[1].Glyph != 'b' {
		t.Fatalf("row 0 = %q%q, want 'ab'", g.Cells[0].Glyph, g.Cells[1].Glyph)
	}
	if g.Cells[8].Glyph != 'e' {
		t.Fatalf("row 1 col 0 = %q, want 'e'", g.Cells[8].Glyph)
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

// Scroll directly shifts rows up + blanks the last row.
func TestScrollDirect(t *testing.T) {
	g := NewGrid(2, 3)
	g.Cells[0] = TerminalCell{Glyph: 'a'}
	g.Cells[2] = TerminalCell{Glyph: 'b'}
	g.Cells[4] = TerminalCell{Glyph: 'c'}
	g.Scroll()
	if g.Cells[0].Glyph != 'b' || g.Cells[2].Glyph != 'c' {
		t.Fatalf("scroll: rows did not shift up: %v", g.Cells)
	}
	if g.Cells[4].Glyph != 0 || g.Cells[5].Glyph != 0 {
		t.Fatalf("scroll: last row not blanked: %v", g.Cells[4:])
	}
}

// Clear wipes everything + homes the cursor.
func TestClear(t *testing.T) {
	g := NewGrid(4, 2)
	g.PrintString("xy")
	g.Clear()
	for i, c := range g.Cells {
		if c.Glyph != 0 {
			t.Fatalf("cell[%d] = %q after Clear, want 0", i, c.Glyph)
		}
	}
	if g.CursorCol != 0 || g.CursorRow != 0 {
		t.Fatalf("cursor after Clear = (%d,%d)", g.CursorCol, g.CursorRow)
	}
}

// Resize: growing keeps the existing content + does not move the cursor.
func TestResizeGrow(t *testing.T) {
	g := NewGrid(2, 2)
	g.PrintString("ab")
	g.Resize(4, 4)
	if g.Cols != 4 || g.Rows != 4 || len(g.Cells) != 16 {
		t.Fatalf("Resize(4,4): %dx%d cells=%d", g.Cols, g.Rows, len(g.Cells))
	}
	if g.Cells[0].Glyph != 'a' || g.Cells[1].Glyph != 'b' {
		t.Fatalf("Resize grow lost content: %v", g.Cells[:2])
	}
}

// Resize: shrinking drops out-of-bounds content + clamps the cursor.
func TestResizeShrink(t *testing.T) {
	g := NewGrid(4, 4)
	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			g.Cells[r*4+c] = TerminalCell{Glyph: byte('A' + r*4 + c)}
		}
	}
	g.CursorCol = 3
	g.CursorRow = 3
	g.Resize(2, 2)
	// Top-left 2x2 retained.
	if g.Cells[0].Glyph != 'A' || g.Cells[1].Glyph != 'B' ||
		g.Cells[2].Glyph != 'E' || g.Cells[3].Glyph != 'F' {
		t.Fatalf("Resize shrink lost top-left: %v", g.Cells)
	}
	if g.CursorCol != 1 || g.CursorRow != 1 {
		t.Fatalf("cursor not clamped: (%d,%d)", g.CursorCol, g.CursorRow)
	}
}

// Resize panics on non-positive dimensions.
func TestResizePanicsOnZero(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Resize(0, 1) did not panic")
		}
	}()
	g := NewGrid(2, 2)
	g.Resize(0, 1)
}
