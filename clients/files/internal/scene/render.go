// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's render.go paints the file browser into an RGBA32 buffer.
// Layout: a dark path bar at the top (one row tall), then a vertical stack
// of file entries — each row is RowHeight pixels, with a small folder/file
// icon followed by the name.
//
// Everything is pure Go (no syscall/js, no cgo) so the renderer builds for
// every architecture the repo targets and is unit-tested natively.

package scene

// Visual constants — kept exported so tests can pin layout invariants.
const (
	// RowHeight is the vertical pitch of one entry row.
	RowHeight = 24
	// PathBarHeight is the height of the path bar at the top of the surface.
	PathBarHeight = 24
	// IconColW is the column width reserved for the icon (so the name column
	// starts at a fixed offset).
	IconColW = 24
	// IconSize is the side length of the icon glyph drawn inside IconColW.
	IconSize = 16
	// FontScale is the integer scale applied to the 8x8 font (so visible
	// glyphs are 16x16 pixels — readable at 480x360).
	FontScale = 2
)

// Palette. Colours are uint8 RGB triples (alpha is forced to 0xFF on write).
// Exposed so the playwright probe can sample for an exact pixel match.
var (
	// ColorBG is the panel background.
	ColorBG = [3]uint8{0xf2, 0xee, 0xe4}
	// ColorFG is the default text (name) ink.
	ColorFG = [3]uint8{0x20, 0x20, 0x28}
	// ColorPathBarBG is the dark strip at the top.
	ColorPathBarBG = [3]uint8{0x2a, 0x2e, 0x36}
	// ColorPathBarFG is the path text on the dark strip.
	ColorPathBarFG = [3]uint8{0xee, 0xee, 0xee}
	// ColorHighlightBG is the selected row's background (a deep blue).
	ColorHighlightBG = [3]uint8{0x2f, 0x6f, 0xd6}
	// ColorHighlightFG is the selected row's text ink (forced white on blue).
	ColorHighlightFG = [3]uint8{0xff, 0xff, 0xff}
	// ColorFolderFill is the body of the folder icon (warm yellow).
	ColorFolderFill = [3]uint8{0xe6, 0xc6, 0x6c}
	// ColorFolderEdge is the dark border around the folder icon.
	ColorFolderEdge = [3]uint8{0x3a, 0x2a, 0x10}
	// ColorFileFill is the body of the file icon (light blue).
	ColorFileFill = [3]uint8{0xbe, 0xd6, 0xee}
	// ColorFileEdge is the dark border around the file icon.
	ColorFileEdge = [3]uint8{0x2a, 0x3a, 0x50}
)

// Render paints the current scene into buf. Panics on size mismatch — a
// misshaped buffer in the caller is a bug, not a recoverable state.
func Render(s *State, buf []byte) {
	need := 4 * s.W * s.H
	if len(buf) != need {
		panic("scene: Render buffer size mismatch")
	}
	Paint(buf, s.W, s.H, s.Browser)
}

// Paint is the renderer's pure entry point — separated from Render so tests
// can pass a BrowserState directly without constructing a full *State.
func Paint(rgba []byte, w, h int, b *BrowserState) {
	// Panel background.
	fillRect(rgba, w, h, 0, 0, w, h, ColorBG)

	// Path bar at the top.
	fillRect(rgba, w, h, 0, 0, w, PathBarHeight, ColorPathBarBG)
	drawText(rgba, w, h, 6, (PathBarHeight-FontH*FontScale)/2, b.CurrentPath, ColorPathBarFG)

	// Entry rows, starting just below the path bar.
	y0 := PathBarHeight
	for i, e := range b.Entries {
		y := y0 + i*RowHeight
		if y >= h {
			break
		}
		paintRow(rgba, w, h, y, e, i == b.Cursor)
	}
}

// paintRow draws one entry row at vertical offset y. A selected row gets the
// highlight background + white ink; an unselected row uses the panel
// background + dark ink.
func paintRow(rgba []byte, w, h, y int, e Entry, selected bool) {
	bgC := ColorBG
	fgC := ColorFG
	if selected {
		bgC = ColorHighlightBG
		fgC = ColorHighlightFG
		fillRect(rgba, w, h, 0, y, w, RowHeight, bgC)
	}
	// Icon centered vertically in the row.
	iconY := y + (RowHeight-IconSize)/2
	iconX := (IconColW - IconSize) / 2
	if e.IsDir {
		drawFolderIcon(rgba, w, h, iconX, iconY)
	} else {
		drawFileIcon(rgba, w, h, iconX, iconY)
	}
	// Name column. We append a "/" suffix to folder names so the listing reads
	// like a familiar `ls -F` dump (folders are also visually distinct via the
	// icon, but the suffix helps when a screenshot is sampled by colour).
	name := e.Name
	if e.IsDir {
		name = name + "/"
	}
	textY := y + (RowHeight-FontH*FontScale)/2
	drawText(rgba, w, h, IconColW+4, textY, name, fgC)
}

// drawFolderIcon paints a stylised folder: a yellow body with a small tab on
// the top-left and a dark border around the whole thing.
func drawFolderIcon(rgba []byte, w, h, x, y int) {
	// Tab on top.
	fillRect(rgba, w, h, x+1, y+1, IconSize/2, 3, ColorFolderFill)
	// Body.
	fillRect(rgba, w, h, x, y+3, IconSize, IconSize-3, ColorFolderFill)
	// Border (top/bottom/left/right of the body).
	fillRect(rgba, w, h, x, y+3, IconSize, 1, ColorFolderEdge)
	fillRect(rgba, w, h, x, y+IconSize-1, IconSize, 1, ColorFolderEdge)
	fillRect(rgba, w, h, x, y+3, 1, IconSize-3, ColorFolderEdge)
	fillRect(rgba, w, h, x+IconSize-1, y+3, 1, IconSize-3, ColorFolderEdge)
}

// drawFileIcon paints a light-blue tile with a dark border + a "dog-eared"
// notch in the top-right corner so it reads as a sheet of paper.
func drawFileIcon(rgba []byte, w, h, x, y int) {
	fillRect(rgba, w, h, x+1, y+1, IconSize-2, IconSize-2, ColorFileFill)
	// Border.
	fillRect(rgba, w, h, x, y, IconSize, 1, ColorFileEdge)
	fillRect(rgba, w, h, x, y+IconSize-1, IconSize, 1, ColorFileEdge)
	fillRect(rgba, w, h, x, y, 1, IconSize, ColorFileEdge)
	fillRect(rgba, w, h, x+IconSize-1, y, 1, IconSize, ColorFileEdge)
	// Dog-ear notch in the upper-right (covers a triangle's worth of pixels).
	for i := 0; i < 4; i++ {
		fillRect(rgba, w, h, x+IconSize-1-i, y+1+i, 1, 1, ColorFileEdge)
	}
}

// drawText paints s starting at (x, y) using the 8x8 font scaled by FontScale.
// Out-of-range bytes are skipped (Glyph itself returns a solid block but we
// keep that path for printable bytes only, so titles like "/Documents/" stay
// readable).
func drawText(rgba []byte, w, h, x, y int, s string, col [3]uint8) {
	cx := x
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 {
			continue
		}
		drawGlyph(rgba, w, h, cx, y, c, col)
		cx += FontW * FontScale
		if cx >= w {
			break
		}
	}
}

// drawGlyph paints one 8x8 glyph at (x, y) using FontScale. Pixels are only
// written where the glyph bit is set — the row background is whatever the
// caller painted before calling drawText.
func drawGlyph(rgba []byte, w, h, x, y int, c byte, col [3]uint8) {
	g := Glyph(c)
	for gy := 0; gy < FontH; gy++ {
		row := g[gy]
		for gx := 0; gx < FontW; gx++ {
			if (row>>(7-gx))&1 == 0 {
				continue
			}
			for dy := 0; dy < FontScale; dy++ {
				yy := y + gy*FontScale + dy
				if yy < 0 || yy >= h {
					continue
				}
				for dx := 0; dx < FontScale; dx++ {
					xx := x + gx*FontScale + dx
					if xx < 0 || xx >= w {
						continue
					}
					off := (yy*w + xx) * 4
					rgba[off] = col[0]
					rgba[off+1] = col[1]
					rgba[off+2] = col[2]
					rgba[off+3] = 0xFF
				}
			}
		}
	}
}

// fillRect paints an opaque rectangle at (x, y) of size (rw, rh) with col.
// Clips to the surface; a zero-or-negative rectangle is a no-op.
func fillRect(rgba []byte, w, h, x, y, rw, rh int, col [3]uint8) {
	x0 := x
	y0 := y
	x1 := x + rw
	y1 := y + rh
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > w {
		x1 = w
	}
	if y1 > h {
		y1 = h
	}
	for yy := y0; yy < y1; yy++ {
		for xx := x0; xx < x1; xx++ {
			off := (yy*w + xx) * 4
			rgba[off] = col[0]
			rgba[off+1] = col[1]
			rgba[off+2] = col[2]
			rgba[off+3] = 0xFF
		}
	}
}

// PathBarSamplePoint returns the pixel coordinate the test harness can sample
// to verify the path bar drew — used by render_test.go and probe-files.mjs.
// Returns the middle of the path-bar strip horizontally, vertically centered.
func PathBarSamplePoint(w int) (int, int) {
	return w / 2, PathBarHeight / 2
}
