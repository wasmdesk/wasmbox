// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's cells.go owns the terminal's character grid: a fixed
// Cols x Rows array of TerminalCell, plus the bookkeeping a real shell needs
// (cursor, line wrap, CR/LF, backspace, scroll, clear). The grid is the only
// piece of mutable terminal state -- the renderer reads it without taking a
// lock; only one goroutine (the main wasm goroutine) mutates it.
//
// Why a struct-of-arrays-of-structs (not a flat byte buffer): per-cell ink
// colour overrides are useful for prompts (cyan), errors (red), and the
// builtin help text (white). Keeping FG/BG per cell costs three bytes per
// cell -- 80 x 25 = 2 KiB total -- which is negligible for our use case.

package scene

// TerminalCell is the per-cell payload: a glyph byte (ASCII 0x20..0x7F)
// plus a foreground/background palette index. A glyph of 0 is rendered as
// the empty space.
type TerminalCell struct {
	Glyph byte
	FG    uint8 // palette index into PaletteFG (0 = default ink)
	BG    uint8 // palette index into PaletteBG (0 = default background)
}

// Grid is the terminal screen buffer + cursor. Cells is row-major; the cell
// at (col, row) lives at index row*Cols + col. The cursor is the next write
// position -- Print(b) writes to Cells[cursor], then advances.
type Grid struct {
	Cols, Rows int
	Cells      []TerminalCell
	CursorCol  int
	CursorRow  int
	// FG / BG are the colours the next Print writes into TerminalCell.FG/BG.
	// PrintString uses them as-is; callers wanting coloured output set them,
	// print, then restore.
	FG, BG uint8
}

// NewGrid allocates a fresh Cols x Rows grid with cursor at (0, 0) and the
// default palette. A non-positive dimension panics -- the terminal cannot
// usefully render a zero-sized buffer and a silent fallback would hide bugs.
func NewGrid(cols, rows int) *Grid {
	if cols <= 0 || rows <= 0 {
		panic("scene: NewGrid requires positive cols + rows")
	}
	return &Grid{
		Cols:  cols,
		Rows:  rows,
		Cells: make([]TerminalCell, cols*rows),
	}
}

// Print writes one printable byte at the cursor, advancing it; wraps to the
// next row at end-of-line; scrolls one row when it would overflow the bottom.
// Non-printable bytes that have semantic meaning are handled by the caller
// (CRLF for '\n', Backspace for 0x08). An unknown byte renders as the font's
// fallback glyph -- caller-driven control is simpler than a per-byte switch.
func (g *Grid) Print(b byte) {
	g.Cells[g.CursorRow*g.Cols+g.CursorCol] = TerminalCell{
		Glyph: b,
		FG:    g.FG,
		BG:    g.BG,
	}
	g.CursorCol++
	if g.CursorCol >= g.Cols {
		g.CursorCol = 0
		g.CursorRow++
		if g.CursorRow >= g.Rows {
			g.Scroll()
			g.CursorRow = g.Rows - 1
		}
	}
}

// PrintString writes a Go string byte-by-byte. CR and LF are routed through
// CRLF; backspace through Backspace; everything else through Print. This
// keeps the shell layer free of low-level cursor mechanics.
func (g *Grid) PrintString(s string) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\r':
			g.CursorCol = 0
		case '\n':
			g.CRLF()
		case 0x08:
			g.Backspace()
		default:
			g.Print(s[i])
		}
	}
}

// Backspace moves the cursor one column left and clears that cell. At the
// start of a row it is a no-op (real terminals likewise do not wrap a BS
// across a line boundary by default).
func (g *Grid) Backspace() {
	if g.CursorCol == 0 {
		return
	}
	g.CursorCol--
	g.Cells[g.CursorRow*g.Cols+g.CursorCol] = TerminalCell{}
}

// CRLF moves the cursor to the next row, column 0; scrolls if necessary.
// Named CRLF rather than NewLine because it does both carriage return and
// line feed in one step -- TTY-style.
func (g *Grid) CRLF() {
	g.CursorCol = 0
	g.CursorRow++
	if g.CursorRow >= g.Rows {
		g.Scroll()
		g.CursorRow = g.Rows - 1
	}
}

// Scroll consumes the top row: rows 1..Rows-1 shift up by one, the last row
// is blanked. The cursor row is NOT updated -- callers that scroll because
// of an overflow have already decided where the cursor should land.
func (g *Grid) Scroll() {
	copy(g.Cells, g.Cells[g.Cols:])
	tail := g.Cells[(g.Rows-1)*g.Cols:]
	for i := range tail {
		tail[i] = TerminalCell{}
	}
}

// Clear zeroes every cell and homes the cursor. Used by the `clear` builtin.
func (g *Grid) Clear() {
	for i := range g.Cells {
		g.Cells[i] = TerminalCell{}
	}
	g.CursorCol = 0
	g.CursorRow = 0
}

// Resize reshapes the grid to newCols x newRows, preserving the top-left
// rectangle of content that still fits. Content outside the new bounds is
// dropped; the cursor is clamped into the new grid. Used when the compositor
// grants a different surface than requested.
func (g *Grid) Resize(newCols, newRows int) {
	if newCols <= 0 || newRows <= 0 {
		panic("scene: Resize requires positive cols + rows")
	}
	out := make([]TerminalCell, newCols*newRows)
	copyCols := newCols
	if g.Cols < copyCols {
		copyCols = g.Cols
	}
	copyRows := newRows
	if g.Rows < copyRows {
		copyRows = g.Rows
	}
	for r := 0; r < copyRows; r++ {
		for c := 0; c < copyCols; c++ {
			out[r*newCols+c] = g.Cells[r*g.Cols+c]
		}
	}
	g.Cells = out
	g.Cols = newCols
	g.Rows = newRows
	if g.CursorCol >= g.Cols {
		g.CursorCol = g.Cols - 1
	}
	if g.CursorRow >= g.Rows {
		g.CursorRow = g.Rows - 1
	}
}
