// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's cells.go adapts the go-widgets/toolkit TerminalView (a
// Cols×Rows grid of coloured character cells with its own block cursor and
// scroll bookkeeping) to the byte/palette-index editing API the terminal
// scene drives: Print / PrintString / Backspace / CRLF / Clear plus a current
// pen (FG / BG palette index). The TerminalView owns the cell buffer and the
// pixel rasterisation (its Draw blits every cell + the block cursor), so the
// old hand-rolled 8×8 upscaler is gone -- only this thin translation layer
// and the shell logic remain client-side.
//
// The fixed 16×16 cell geometry is pinned via TerminalView.SetCellSize (see
// NewGrid); the anti-aliased monospace face that rasterises the glyphs is
// attached with SetFont. Earlier this needed a bespoke `cellFont` metrics shim
// to report a padded 16×16 advance/height while delegating glyph drawing to the
// inner font -- SetCellSize is the toolkit's own escape hatch for that exact
// need, so the shim is gone and the geometry is now declared directly.
//
// The glyph face is JetBrains Mono (bundled by github.com/go-opentype/fonts)
// rendered anti-aliased at termFontPx, replacing the old ×2 5×7 bitmap. A
// monospace face is mandatory: the fixed 16×16 cell is a grid, so a
// proportional face would misalign columns. The face's own advance is
// irrelevant here -- SetCellSize pins every column to 16px regardless -- but a
// monospace face keeps each glyph's ink centred consistently within its cell.
//
// Colour model: the scene talks in palette indices (0 = green ink, 1 = cyan
// prompt, 2 = red error, 3 = white help), which map here to explicit RGBA pen
// colours the TerminalView stamps into each written cell. Keeping the index
// vocabulary means scene.go / shell.go did not have to change when the
// rasteriser moved into the toolkit.

package scene

import (
	"sync"

	"github.com/go-opentype/fonts/jetbrainsmono"
	"github.com/go-widgets/toolkit"
)

// termFontPx is the pixel size the anti-aliased JetBrains Mono face is rendered
// at. 12px yields a 16px line height -- exactly the fixed cell height -- so a
// glyph (and its descenders) fits within one cell without bleeding into the row
// below, while the cell WIDTH stays pinned at cellW by SetCellSize regardless of
// the face's own (narrower) advance.
const termFontPx = 12

// termFace lazily builds + caches the anti-aliased JetBrains Mono face once per
// process. NewTrueTypeFont's error is documented as never returned for a
// well-formed bundled blob; on the impossible parse-failure path we fall back
// to the still-legible ×2 bitmap so the grid degrades to bitmap glyphs, never
// to blank cells.
var (
	termFaceOnce sync.Once
	termFaceInst toolkit.Font
)

func termFace() toolkit.Font {
	termFaceOnce.Do(func() { termFaceInst = buildMonoFace(jetbrainsmono.TTF, termFontPx) })
	return termFaceInst
}

// buildMonoFace parses ttf into an anti-aliased face at px, falling back to the
// ×2 bitmap when the blob cannot be parsed. Split from termFace so the
// (never-reached-in-production) parse-failure branch is unit-testable with a
// deliberately malformed blob.
func buildMonoFace(ttf []byte, px int) toolkit.Font {
	f, err := toolkit.NewTrueTypeFont(ttf, px)
	if err != nil {
		return toolkit.NewBitmapFont(2)
	}
	return f
}

// cellW / cellH are the fixed terminal cell size in pixels. They match the
// previous renderer's 16×16 cells exactly so the grid keeps the SAME on-screen
// geometry (columns, rows, line pitch) the client always had -- the scaled
// glyph (12×14) is drawn top-left within the cell, leaving the historical
// inter-cell gap. The geometry is pinned onto the TerminalView with
// SetCellSize(cellW, cellH) in NewGrid, so the pixel-calibrated e2e probes stay
// valid unchanged.
const (
	cellW = 16
	cellH = 16
)

// Palette colours. The values match the previous PaletteFG / PaletteBG tables
// byte-for-byte so the terminal keeps its exact look (soft-green ink, cyan
// prompt, red errors, bright-white help on a near-black panel).
var (
	inkGreen = toolkit.RGBA{R: 0xa0, G: 0xe0, B: 0xa0, A: 0xff} // 0: default ink
	inkCyan  = toolkit.RGBA{R: 0x8a, G: 0xe1, B: 0xff, A: 0xff} // 1: prompt
	inkRed   = toolkit.RGBA{R: 0xff, G: 0x9b, B: 0x9b, A: 0xff} // 2: errors
	inkWhite = toolkit.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff} // 3: help
	panelBG  = toolkit.RGBA{R: 0x10, G: 0x10, B: 0x10, A: 0xff} // terminal panel
)

// paletteFG maps a pen index to its ink colour. Out-of-range indices fall back
// to entry 0 (the default ink), mirroring the old fgOf helper so a stray index
// can never crash a write.
var paletteFG = []toolkit.RGBA{inkGreen, inkCyan, inkRed, inkWhite}

// penColor returns the ink for pen index i, clamping to the default ink on an
// out-of-range value.
func penColor(i uint8) toolkit.RGBA {
	if int(i) >= len(paletteFG) {
		return paletteFG[0]
	}
	return paletteFG[i]
}

// Grid is the terminal screen buffer + cursor. It embeds a toolkit.TerminalView
// (which supplies Cols / Rows / Cells / CursorCol / CursorRow / ScrollUp /
// Resize / SetCell / Draw and the block cursor) and adds the byte-oriented pen
// the scene edits through. FG / BG are palette indices the next Print stamps.
type Grid struct {
	*toolkit.TerminalView
	// FG / BG are the palette indices the next Print writes. PrintString /
	// writePrompt set them, print, then restore -- exactly as the previous
	// Grid did.
	FG, BG uint8
}

// NewGrid allocates a fresh Cols×Rows terminal grid with the cursor homed at
// (0, 0), the default green pen, and the block cursor visible. A non-positive
// dimension panics (via the toolkit) -- a zero-sized buffer is an upstream bug,
// not a recoverable state.
func NewGrid(cols, rows int) *Grid {
	tv := toolkit.NewTerminalView(cols, rows)
	tv.SetFont(termFace())
	// Pin the fixed 16×16 cell box via the toolkit's own override rather than a
	// metrics-only font shim; the anti-aliased mono glyph still draws top-left
	// within each pinned cell.
	tv.SetCellSize(cellW, cellH)
	tv.DefaultFG = inkGreen
	tv.DefaultBG = panelBG
	tv.CursorVisible = true
	return &Grid{TerminalView: tv}
}

// Print writes one printable byte at the cursor using the current pen, then
// advances the cursor -- wrapping to the next row at end-of-line and scrolling
// one row when it would overflow the bottom. It delegates the cursor/wrap/
// scroll bookkeeping to the TerminalView by pointing its pen at the current
// palette colour and issuing a single-rune Write.
func (g *Grid) Print(b byte) {
	g.TerminalView.DefaultFG = penColor(g.FG)
	g.TerminalView.Write(string(rune(b)))
	g.TerminalView.DefaultFG = inkGreen
}

// PrintString writes a Go string byte-by-byte. CR homes the column, LF routes
// through CRLF, backspace through Backspace, and everything else through Print.
// This keeps the shell layer free of low-level cursor mechanics.
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

// Backspace moves the cursor one column left and clears that cell. At the start
// of a row it is a no-op (real terminals likewise do not wrap a BS across a
// line boundary by default).
func (g *Grid) Backspace() {
	if g.CursorCol == 0 {
		return
	}
	g.CursorCol--
	g.SetCell(g.CursorCol, g.CursorRow, 0, toolkit.RGBA{}, toolkit.RGBA{})
}

// CRLF moves the cursor to column 0 of the next row, scrolling one row when it
// would leave the bottom. Named CRLF (not NewLine) because it does both the
// carriage return and the line feed in one step -- TTY-style.
func (g *Grid) CRLF() {
	g.CursorCol = 0
	g.CursorRow++
	if g.CursorRow >= g.Rows {
		g.ScrollUp(1)
		g.CursorRow = g.Rows - 1
	}
}

// Clear zeroes every cell and homes the cursor. Used by the `clear` builtin.
func (g *Grid) Clear() {
	for i := range g.Cells {
		g.Cells[i] = toolkit.TermCell{}
	}
	g.CursorCol = 0
	g.CursorRow = 0
}
