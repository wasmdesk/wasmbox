// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's render.go paints a macOS Finder-inspired file browser into
// an RGBA32 buffer. Layout (720x440):
//
//	+---------------------------------------------------+ toolbar (40)
//	| [<] Documents                                     |
//	+----------+----------------------------------------+
//	| Favorites| Name        Date Modified       Size   | column headers (24)
//	|          +----------------------------------------+
//	| Documents| [F] Documents    Jun 25 2026     --    | list rows (28)
//	| Pictures | [F] Pictures     Jun 25 2026     --    |
//	| Downloads| [F] Downloads    Jun 25 2026     --    |
//	| ...      | [d] about.txt    Jun 25 2026   234 B   |
//	+----------+----------------------------------------+
//
// Everything is pure Go (no syscall/js, no cgo) so the renderer builds for
// every architecture the repo targets and is unit-tested natively. Each
// paint stage is its own helper so tests can exercise them in isolation.

package scene

import "strconv"

// Visual constants -- exported so tests + the playwright probe can pin
// layout invariants. The defaults reproduce the macOS Big Sur / Sonoma
// proportions on a 720x440 surface.
const (
	// RowHeight is the vertical pitch of one entry row.
	RowHeight = 28
	// ToolbarHeight is the height of the top toolbar (back-arrow + breadcrumb).
	ToolbarHeight = 40
	// ColumnHeaderHeight is the band between toolbar and list that holds the
	// Name / Date Modified / Size labels.
	ColumnHeaderHeight = 24
	// SidebarWidth is the width of the left Favorites pane.
	SidebarWidth = 140
	// SidebarHeaderHeight is the empty padding above the first sidebar row
	// (visually balances the column-header band on the right pane).
	SidebarHeaderHeight = 8
	// SidebarRowHeight is the height of one Favorites row.
	SidebarRowHeight = 24
	// IconSize is a generic side length we use to position icons (folders are
	// 16x12, files are 12x16 inside this box; the box centres them).
	IconSize = 16
	// FontScale is the integer scale applied to the 8x8 font on the list rows.
	FontScale = 2

	// BackBtnX / BackBtnY / BackBtnW / BackBtnH frame the back-arrow button.
	// Kept tight on the left so the breadcrumb has room.
	BackBtnX = 10
	BackBtnY = 8
	BackBtnW = 28
	BackBtnH = 24

	// Column x-anchors. NameColX is the left edge of the icon+name cell.
	// DateColX / SizeColX are the LEFT edges of the right-aligned text bands;
	// the renderer right-aligns date/size against these so columns line up.
	NameColX     = SidebarWidth + 12
	DateColRight = 600 // right edge of the Date Modified column
	SizeColRight = 700 // right edge of the Size column
)

// Palette. Colours are uint8 RGB triples (alpha is forced to 0xFF on write).
// The names match macOS Big Sur / Sonoma roles -- exposed so the playwright
// probe can sample for an exact pixel match.
var (
	// ColorWindowBG is the right-pane background (very light gray, near-white).
	ColorWindowBG = [3]uint8{246, 246, 247}
	// ColorSidebarBG is the left Favorites pane background (light gray).
	ColorSidebarBG = [3]uint8{232, 232, 236}
	// ColorToolbarBG is the top toolbar background (slightly darker than the
	// window so the bar reads as chrome, not content).
	ColorToolbarBG = [3]uint8{238, 238, 240}
	// ColorDivider is the 1px line between toolbar/sidebar/header bands.
	ColorDivider = [3]uint8{217, 217, 219}
	// ColorTextPrimary is the default text ink.
	ColorTextPrimary = [3]uint8{28, 28, 30}
	// ColorTextSecondary is the dimmed ink for the Date Modified / Size cells
	// (Finder draws non-name columns in a softer gray).
	ColorTextSecondary = [3]uint8{120, 120, 124}
	// ColorAccent is the focused-selection blue.
	ColorAccent = [3]uint8{0, 99, 233}
	// ColorOnAccent is the text/icon ink on top of the accent fill (white).
	ColorOnAccent = [3]uint8{255, 255, 255}
	// ColorFolderFill / ColorFolderShadow paint the two-tone folder icon.
	ColorFolderFill   = [3]uint8{118, 178, 235}
	ColorFolderShadow = [3]uint8{86, 132, 195}
	// ColorFilePaper / ColorFileBorder paint the page-with-folded-corner icon.
	ColorFilePaper  = [3]uint8{252, 252, 253}
	ColorFileBorder = [3]uint8{180, 180, 185}
)

// Render paints the current scene into buf. Panics on size mismatch -- a
// misshaped buffer in the caller is a bug, not a recoverable state.
func Render(s *State, buf []byte) {
	need := 4 * s.W * s.H
	if len(buf) != need {
		panic("scene: Render buffer size mismatch")
	}
	Paint(buf, s.W, s.H, s)
}

// Paint is the renderer's pure entry point -- separated from Render so tests
// can pass a *State directly without constructing through New().
func Paint(rgba []byte, w, h int, s *State) {
	// Window background (fills everything, then overpainted by toolbar +
	// sidebar so the right pane is what shows through underneath).
	fillRect(rgba, w, h, 0, 0, w, h, ColorWindowBG)
	paintSidebar(rgba, w, h, s)
	paintToolbar(rgba, w, h, s)
	paintColumnHeaders(rgba, w, h)
	paintListRows(rgba, w, h, s)
}

// paintToolbar draws the top chrome: a flat gray bar with a back-arrow
// button on the left and the current-path breadcrumb to the right of it.
// A 1px divider sits at the bottom edge so the toolbar reads as separate
// from the list pane.
func paintToolbar(rgba []byte, w, h int, s *State) {
	fillRect(rgba, w, h, 0, 0, w, ToolbarHeight, ColorToolbarBG)
	// Back-arrow button: rounded(ish) rectangle drawn as a filled rect with
	// a darker chevron inside. We do not blend corners (the 8x8 font ethos
	// extends to icons) -- a 1px-inset border gives enough "button" feel.
	paintBackButton(rgba, w, h)
	// Breadcrumb text -- the basename of CurrentPath, or "/" for the root.
	label := Basename(s.Browser.CurrentPath)
	textX := BackBtnX + BackBtnW + 12
	textY := (ToolbarHeight - FontH*FontScale) / 2
	drawText(rgba, w, h, textX, textY, label, FontScale, ColorTextPrimary)
	// Bottom divider.
	fillRect(rgba, w, h, 0, ToolbarHeight-1, w, 1, ColorDivider)
}

// paintBackButton draws the chevron button. The button face is the same
// gray as the toolbar so the bar reads flat; the chevron is the primary
// text ink. A 1px border separates the button from its background.
func paintBackButton(rgba []byte, w, h int) {
	// Border.
	fillRect(rgba, w, h, BackBtnX, BackBtnY, BackBtnW, BackBtnH, ColorDivider)
	// Face (1px inset).
	fillRect(rgba, w, h, BackBtnX+1, BackBtnY+1, BackBtnW-2, BackBtnH-2, ColorToolbarBG)
	// Chevron. We hand-paint a 9px-tall "<" using two diagonal strokes that
	// share the leftmost pixel. cx,cy is the chevron's tip.
	cx := BackBtnX + BackBtnW/2 - 3
	cy := BackBtnY + BackBtnH/2
	for i := 0; i < 5; i++ {
		fillRect(rgba, w, h, cx+i, cy-i, 2, 1, ColorTextPrimary)
		fillRect(rgba, w, h, cx+i, cy+i, 2, 1, ColorTextPrimary)
	}
}

// paintSidebar draws the left Favorites pane: background + a "Favorites"
// header (small caps in the Finder; we lowercase here to fit the 8x8 font's
// budget) + one row per SidebarEntry. The currently-selected Favorite gets
// the accent fill with white ink; others use the primary ink on the sidebar
// background.
func paintSidebar(rgba []byte, w, h int, s *State) {
	fillRect(rgba, w, h, 0, ToolbarHeight, SidebarWidth, h-ToolbarHeight, ColorSidebarBG)
	// Right edge divider.
	fillRect(rgba, w, h, SidebarWidth-1, ToolbarHeight, 1, h-ToolbarHeight, ColorDivider)
	// Header (8x8 unscaled so it reads as "label" rather than "row").
	drawText(rgba, w, h, 10, ToolbarHeight+SidebarHeaderHeight-FontH-2, "Favorites", 1, ColorTextSecondary)
	// Rows.
	y0 := ToolbarHeight + SidebarHeaderHeight
	for i, e := range s.Sidebar {
		y := y0 + i*SidebarRowHeight
		selected := i == s.SidebarSelected
		fg := ColorTextPrimary
		if selected {
			fillRect(rgba, w, h, 4, y, SidebarWidth-8, SidebarRowHeight-2, ColorAccent)
			fg = ColorOnAccent
		}
		// Mini folder icon, then the label. Use scale 1 so the label fits in
		// SidebarWidth even for long names like "Downloads".
		iconY := y + (SidebarRowHeight-12)/2
		paintMiniFolder(rgba, w, h, 10, iconY, selected)
		labelY := y + (SidebarRowHeight-FontH)/2
		drawText(rgba, w, h, 28, labelY, e.Name, 1, fg)
	}
}

// paintMiniFolder is a small (14x10) folder glyph used in the sidebar. When
// the row is selected the colours invert toward white so it stays visible
// on the accent background.
func paintMiniFolder(rgba []byte, w, h, x, y int, selected bool) {
	face := ColorFolderFill
	shadow := ColorFolderShadow
	if selected {
		face = ColorOnAccent
		shadow = [3]uint8{220, 230, 250}
	}
	// Tab on top-left.
	fillRect(rgba, w, h, x, y, 6, 2, shadow)
	// Body.
	fillRect(rgba, w, h, x, y+2, 14, 8, face)
	// Bottom shadow strip.
	fillRect(rgba, w, h, x, y+9, 14, 1, shadow)
}

// paintColumnHeaders draws the Name / Date Modified / Size labels in the
// band between the toolbar and the first list row. The text is heavier (the
// drawText helper redraws each glyph 1px to the right to fake bold).
func paintColumnHeaders(rgba []byte, w, h int) {
	y := ToolbarHeight
	fillRect(rgba, w, h, SidebarWidth, y, w-SidebarWidth, ColumnHeaderHeight, ColorToolbarBG)
	// Bottom divider.
	fillRect(rgba, w, h, SidebarWidth, y+ColumnHeaderHeight-1, w-SidebarWidth, 1, ColorDivider)
	textY := y + (ColumnHeaderHeight-FontH*FontScale)/2
	drawTextBold(rgba, w, h, NameColX, textY, "Name", FontScale, ColorTextPrimary)
	// Date and Size column labels are right-aligned to their *Right edges so
	// they line up with the data they label.
	dateLabel := "Date Modified"
	sizeLabel := "Size"
	drawTextRight(rgba, w, h, DateColRight, textY, dateLabel, FontScale, ColorTextPrimary, true)
	drawTextRight(rgba, w, h, SizeColRight, textY, sizeLabel, FontScale, ColorTextPrimary, true)
}

// paintListRows paints one row per Entry, starting just below the column
// header band. Each row carries: icon + name (left), date (right-aligned
// at DateColRight), size (right-aligned at SizeColRight). The selected row
// gets the accent fill across the entire right-pane width with white ink.
func paintListRows(rgba []byte, w, h int, s *State) {
	y0 := ToolbarHeight + ColumnHeaderHeight
	for i, e := range s.Browser.Entries {
		y := y0 + i*RowHeight
		if y >= h {
			break
		}
		selected := i == s.Browser.Cursor
		fg := ColorTextPrimary
		fgSecondary := ColorTextSecondary
		if selected {
			fillRect(rgba, w, h, SidebarWidth, y, w-SidebarWidth, RowHeight, ColorAccent)
			fg = ColorOnAccent
			fgSecondary = ColorOnAccent
		}
		// Icon centred vertically inside the row.
		iconX := NameColX
		iconY := y + (RowHeight-IconSize)/2
		if e.IsDir {
			paintFolderIcon(rgba, w, h, iconX, iconY, selected)
		} else {
			paintFileIcon(rgba, w, h, iconX, iconY, selected)
		}
		// Name.
		nameX := iconX + IconSize + 8
		nameY := y + (RowHeight-FontH*FontScale)/2
		drawText(rgba, w, h, nameX, nameY, e.Name, FontScale, fg)
		// Date (right-aligned at DateColRight).
		drawTextRight(rgba, w, h, DateColRight, nameY, e.ModTime, FontScale, fgSecondary, false)
		// Size.
		size := "--"
		if !e.IsDir {
			size = formatSize(e.Size)
		}
		drawTextRight(rgba, w, h, SizeColRight, nameY, size, FontScale, fgSecondary, false)
	}
}

// paintFolderIcon paints a Finder-style 16x12 folder: a lighter-blue face,
// a small tab on the top-left, and a darker rim along the bottom. When the
// row is selected the colours flip so the icon reads on the accent fill.
func paintFolderIcon(rgba []byte, w, h, x, y int, selected bool) {
	face := ColorFolderFill
	shadow := ColorFolderShadow
	if selected {
		face = ColorOnAccent
		shadow = [3]uint8{220, 230, 250}
	}
	// Tab.
	fillRect(rgba, w, h, x, y+1, 7, 2, shadow)
	// Body (lighter face).
	fillRect(rgba, w, h, x, y+3, 16, 9, face)
	// Top edge highlight (the rim sits 1px under the tab).
	fillRect(rgba, w, h, x, y+3, 16, 1, shadow)
	// Bottom rim.
	fillRect(rgba, w, h, x, y+11, 16, 1, shadow)
}

// paintFileIcon paints a 12x16 page with a folded top-right corner. White
// paper with a gray border on a normal row; on a selected row the paper
// flips white-on-accent so it stays visible.
func paintFileIcon(rgba []byte, w, h, x, y int, selected bool) {
	paper := ColorFilePaper
	border := ColorFileBorder
	if selected {
		paper = ColorOnAccent
		border = [3]uint8{220, 230, 250}
	}
	// Body (12 wide x 16 tall) -- offset x by +2 so the icon stays centered
	// inside the 16-wide IconSize box.
	fillRect(rgba, w, h, x+2, y, 12, 16, paper)
	// Border.
	fillRect(rgba, w, h, x+2, y, 12, 1, border)
	fillRect(rgba, w, h, x+2, y+15, 12, 1, border)
	fillRect(rgba, w, h, x+2, y, 1, 16, border)
	fillRect(rgba, w, h, x+13, y, 1, 16, border)
	// Folded top-right corner: a 4x4 triangle of border ink that "cuts" the
	// corner. We paint the diagonal and the area above it in the border
	// colour so the corner reads as turned over.
	for i := 0; i < 4; i++ {
		fillRect(rgba, w, h, x+2+12-4+i, y, 4-i, 1, border)
	}
}

// formatSize prettifies a byte count for the Size column. We render a
// folder as "--" (paintListRows takes care of that); for files we use
// "234 B", "1.2 KB", "89.1 KB", "12.3 MB", ... -- one decimal where the
// value would otherwise lose precision.
func formatSize(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n) / 1024.0
	idx := 0
	for v >= 1024.0 && idx < len(units)-1 {
		v /= 1024.0
		idx++
	}
	// One decimal place. We avoid fmt.Sprintf so the wasm binary stays slim
	// (and the helper stays unit-testable without round-tripping).
	whole := int64(v)
	frac := int64((v - float64(whole)) * 10)
	return strconv.FormatInt(whole, 10) + "." + strconv.FormatInt(frac, 10) + " " + units[idx]
}

// DrawText is the exported text-paint primitive: paints s starting at (x, y)
// using the 8x8 font scaled by `scale`. Out-of-range bytes are skipped.
// Exported so future clients can reuse the renderer's text engine without
// reaching into package-internal drawText.
func DrawText(rgba []byte, w, h, x, y, scale int, fg [3]uint8, s string) {
	drawText(rgba, w, h, x, y, s, scale, fg)
}

// drawText paints s starting at (x, y) using the 8x8 font scaled by scale.
// Out-of-range bytes (< 0x20) are skipped.
func drawText(rgba []byte, w, h, x, y int, s string, scale int, col [3]uint8) {
	cx := x
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 {
			continue
		}
		drawGlyph(rgba, w, h, cx, y, c, scale, col)
		cx += FontW * scale
		if cx >= w {
			break
		}
	}
}

// drawTextBold paints s twice -- once at (x,y) and once at (x+1,y) -- so
// the strokes thicken. Used by paintColumnHeaders to fake a bold-weight
// font without shipping a second bitmap.
func drawTextBold(rgba []byte, w, h, x, y int, s string, scale int, col [3]uint8) {
	drawText(rgba, w, h, x, y, s, scale, col)
	drawText(rgba, w, h, x+1, y, s, scale, col)
}

// drawTextRight paints s so it ends at (rx, y) -- i.e. right-aligned with
// rx as the right edge. The `bold` flag fakes weight via drawTextBold.
func drawTextRight(rgba []byte, w, h, rx, y int, s string, scale int, col [3]uint8, bold bool) {
	width := len(s) * FontW * scale
	x := rx - width
	if bold {
		drawTextBold(rgba, w, h, x, y, s, scale, col)
		return
	}
	drawText(rgba, w, h, x, y, s, scale, col)
}

// drawGlyph paints one 8x8 glyph at (x, y) scaled by `scale`. Pixels are
// only written where the glyph bit is set -- the row background is whatever
// the caller painted before calling drawText.
func drawGlyph(rgba []byte, w, h, x, y int, c byte, scale int, col [3]uint8) {
	g := Glyph(c)
	for gy := 0; gy < FontH; gy++ {
		row := g[gy]
		for gx := 0; gx < FontW; gx++ {
			if (row>>(7-gx))&1 == 0 {
				continue
			}
			for dy := 0; dy < scale; dy++ {
				yy := y + gy*scale + dy
				if yy < 0 || yy >= h {
					continue
				}
				for dx := 0; dx < scale; dx++ {
					xx := x + gx*scale + dx
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

// SidebarSamplePoint returns a coordinate guaranteed to be on the sidebar
// background (no icon, no label) -- used by render_test + probe-files.mjs
// to sample the sidebar colour for an exact pixel match. We aim *between*
// rows (the 2px gap left by SidebarRowHeight-2 selection fills) so the
// point never overlaps glyphs even when a row is selected.
func SidebarSamplePoint() (int, int) {
	// One row below the last Favorite (DefaultSidebar has 5 entries), so the
	// y lands in pure sidebar BG no matter which row is selected.
	return SidebarWidth / 2, ToolbarHeight + SidebarHeaderHeight + 5*SidebarRowHeight + 4
}

// WindowBGSamplePoint returns a coordinate inside the right pane, below
// the column headers, in a corner with no row content.
func WindowBGSamplePoint(w, h int) (int, int) {
	return w - 4, h - 4
}
