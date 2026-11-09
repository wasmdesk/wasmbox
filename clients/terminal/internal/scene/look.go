// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// look.go is the terminal's look: the fixed 16×16 cell, the JetBrains Mono
// face and the colours. The grid itself is go-widgets/toolkit's Terminal; the
// look is declared on it through SetFont, SetCellSize, Palette and the theme
// passed to Draw.
//
// The glyph face is JetBrains Mono (bundled by github.com/go-opentype/fonts)
// rendered anti-aliased at termFontPx. A monospace face is mandatory: the
// fixed 16×16 cell is a grid, so a proportional face would misalign columns.
// The face's own advance is irrelevant -- SetCellSize pins every column to
// 16px regardless -- but a monospace face keeps each glyph's ink centred
// consistently within its cell.

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

// cellW / cellH are the fixed terminal cell size in pixels: the grid's
// column/row/pitch geometry the pixel-calibrated e2e probes expect. The scaled
// glyph is drawn top-left within the cell, leaving the historical inter-cell
// gap.
const (
	cellW = 16
	cellH = 16
)

// Colours: soft-green ink, cyan prompt, red errors on a near-black panel.
var (
	inkGreen = toolkit.RGBA{R: 0xa0, G: 0xe0, B: 0xa0, A: 0xff} // default ink
	inkCyan  = toolkit.RGBA{R: 0x8a, G: 0xe1, B: 0xff, A: 0xff} // prompt (SGR 36 / bold 36)
	inkRed   = toolkit.RGBA{R: 0xff, G: 0x9b, B: 0x9b, A: 0xff} // errors (SGR 31 / bold 31)
	panelBG  = toolkit.RGBA{R: 0x10, G: 0x10, B: 0x10, A: 0xff} // terminal panel
)

// newTerminal builds the grid: cols×rows cells of cellW×cellH pixels in the
// terminal face, with the terminal's palette (the 16 xterm colours, but with
// this look's red and cyan so an escape-sequence colour matches the prompt
// and error ink).
func newTerminal(cols, rows int) *toolkit.Terminal {
	t := toolkit.NewTerminal(cols, rows, scrollback)
	t.SetFont(termFace())
	t.SetCellSize(cellW, cellH)
	t.Palette[1], t.Palette[9] = inkRed, inkRed
	t.Palette[6], t.Palette[14] = inkCyan, inkCyan
	return t
}

// scrollback is how many lines of history the terminal keeps.
const scrollback = 5000

// newTheme is the theme the Terminal draws with: its default ink and ground,
// and the selection colour (the ground on the ink, an inverse block).
func newTheme() *toolkit.Theme {
	th := *toolkit.DefaultLight()
	th.OnSurface = inkGreen
	th.Surface = panelBG
	th.Accent = inkGreen
	return &th
}
