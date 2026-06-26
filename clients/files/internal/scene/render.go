// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's render.go paints a GNOME Nautilus-inspired file browser into
// an RGBA32 buffer. Layout (720x440):
//
//	+---------------------------------------------------+ header bar (44)
//	| [=]  [<] [>]   Home > Documents                   |
//	+----------+----------------------------------------+
//	| BOOKMARKS|  Name                              Size| column headers (28)
//	|  Home    +----------------------------------------+
//	|  Docs    |  [F] Documents                       --|
//	|  Pics    |  [F] Pictures                        --| list rows (32)
//	|  ...     |  [F] Downloads                       --|
//	| OTHER... |  [d] about.txt                234 bytes|
//	|  Comp    |                                        |
//	|  Trash   |                                        |
//	+----------+----------------------------------------+
//
// Everything is pure Go (no syscall/js, no cgo) so the renderer builds for
// every architecture the repo targets and is unit-tested natively. Each
// paint stage is its own helper so tests can exercise them in isolation.
//
// Colour roles match Adwaita (light) -- @view_bg / @sidebar_bg / @headerbar_bg
// / @accent_bg_color etc. -- so the UI reads as a stock GTK4 / libadwaita
// application rather than a re-skinned macOS Finder.

package scene

import "strconv"

// Visual constants -- exported so tests + the playwright probe can pin
// layout invariants. The defaults reproduce GTK4 / libadwaita proportions on
// a 720x440 surface.
const (
	// RowHeight is the vertical pitch of one entry row (Nautilus default ~32).
	RowHeight = 32
	// HeaderBarHeight is the height of the top header bar (hamburger + nav
	// buttons + breadcrumb path-bar). Taller than Finder's 40 to match GTK4.
	HeaderBarHeight = 44
	// ToolbarHeight is kept as an alias for HeaderBarHeight so callers that
	// think of "the strip above the content" stay readable.
	ToolbarHeight = HeaderBarHeight
	// ColumnHeaderHeight is the band that holds the Name / Size labels.
	ColumnHeaderHeight = 28
	// SidebarWidth is the width of the left navigation pane.
	SidebarWidth = 160
	// SidebarSectionHeaderHeight is the height of a section label band
	// ("BOOKMARKS", "OTHER LOCATIONS").
	SidebarSectionHeaderHeight = 22
	// SidebarRowHeight is the height of one navigation row.
	SidebarRowHeight = 28
	// SidebarTopPadding is the vertical gap between the header bar and the
	// first section label.
	SidebarTopPadding = 8
	// IconSize is a generic side length used to position list-row icons. The
	// row itself centres the actual icon inside this box.
	IconSize = 18
	// FontScale is the integer scale applied to the 8x8 font on the list rows.
	FontScale = 2

	// Header-bar button geometry. Hamburger sits leftmost, then back, then
	// forward. Each button is rendered as a flat square button with a 1px
	// border (Adwaita's "circular" style is too round for an 8x8 ethos).
	HamburgerBtnX = 8
	HamburgerBtnY = 10
	HamburgerBtnW = 28
	HamburgerBtnH = 24

	BackBtnX = 46
	BackBtnY = 10
	BackBtnW = 28
	BackBtnH = 24

	ForwardBtnX = 78
	ForwardBtnY = 10
	ForwardBtnW = 28
	ForwardBtnH = 24

	// Path-bar starts after the nav buttons. Each crumb is rendered as a
	// rounded-ish button-like region; the active (last) crumb is filled in a
	// slightly darker shade so it reads as "you are here".
	PathBarX     = 118
	PathBarY     = 10
	PathBarH     = 24
	PathCrumbPad = 6

	// Column x-anchors. NameColX is the left edge of the icon+name cell.
	// SizeColRight is the right edge of the right-aligned Size column.
	NameColX     = SidebarWidth + 12
	SizeColRight = 700
)

// Palette. Colours are uint8 RGB triples (alpha is forced to 0xFF on write).
// The names match Adwaita / libadwaita's light-theme roles -- exposed so the
// playwright probe can sample for an exact pixel match.
var (
	// ColorWindowBG is the right-pane background (Adwaita @view_bg_color, light).
	ColorWindowBG = [3]uint8{250, 250, 250}
	// ColorSidebarBG is the left navigation pane background.
	ColorSidebarBG = [3]uint8{241, 241, 241}
	// ColorHeaderBarBG is the top header-bar background.
	ColorHeaderBarBG = [3]uint8{248, 248, 248}
	// ColorToolbarBG is an alias kept for callers that still talk about the
	// "toolbar" -- the band is identical to the header bar.
	ColorToolbarBG = ColorHeaderBarBG
	// ColorDivider is the 1px line between header/sidebar/column-header bands.
	ColorDivider = [3]uint8{218, 220, 224}
	// ColorTextPrimary is the default text ink (Adwaita @view_fg_color).
	ColorTextPrimary = [3]uint8{46, 52, 54}
	// ColorTextSecondary is the dimmed ink for column metadata + section
	// labels (Adwaita @theme_unfocused_fg).
	ColorTextSecondary = [3]uint8{146, 153, 159}
	// ColorAccent is the focused-selection blue (Adwaita @accent_bg_color).
	ColorAccent = [3]uint8{53, 132, 228}
	// ColorOnAccent is the text/icon ink on top of the accent fill (white).
	ColorOnAccent = [3]uint8{255, 255, 255}
	// ColorButtonFace is the fill of the rest-state nav buttons (slightly
	// raised vs the header bar so they read as "tappable").
	ColorButtonFace = [3]uint8{240, 240, 240}
	// ColorButtonDisabled is the dimmed ink for an unavailable button (e.g.
	// the forward arrow when there is no forward history).
	ColorButtonDisabled = [3]uint8{200, 204, 208}
	// ColorCrumbActiveBG is the fill of the active path-bar segment (the
	// last crumb -- "you are here").
	ColorCrumbActiveBG = [3]uint8{226, 228, 232}
	// ColorFolderFill / ColorFolderTab / ColorFolderStroke paint the
	// Nautilus-style two-tone folder icon.
	ColorFolderFill   = [3]uint8{95, 161, 224}
	ColorFolderTab    = [3]uint8{120, 184, 235}
	ColorFolderStroke = [3]uint8{53, 113, 184}
	// ColorFilePaper / ColorFileBorder paint the page-with-folded-corner icon.
	ColorFilePaper  = [3]uint8{255, 255, 255}
	ColorFileBorder = [3]uint8{190, 195, 200}
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
	// Window background (fills everything, then overpainted by header bar +
	// sidebar so the right pane is what shows through underneath).
	fillRect(rgba, w, h, 0, 0, w, h, ColorWindowBG)
	paintSidebar(rgba, w, h, s)
	paintHeaderBar(rgba, w, h, s)
	paintColumnHeaders(rgba, w, h)
	paintListRows(rgba, w, h, s)
}

// paintHeaderBar draws the top chrome: a flat gray bar with the hamburger
// button (left), back + forward navigation buttons, then a breadcrumb
// path-bar covering the remaining width. A 1px divider sits at the bottom
// edge so the header bar reads as separate from the list pane.
func paintHeaderBar(rgba []byte, w, h int, s *State) {
	fillRect(rgba, w, h, 0, 0, w, HeaderBarHeight, ColorHeaderBarBG)
	paintHamburger(rgba, w, h)
	paintBackButton(rgba, w, h)
	paintForwardButton(rgba, w, h, false) // no forward history in v0 -- grayed
	paintPathBar(rgba, w, h, s)
	// Bottom divider.
	fillRect(rgba, w, h, 0, HeaderBarHeight-1, w, 1, ColorDivider)
}

// paintHamburger draws the leftmost menu button: a flat button face with
// three short horizontal lines stacked vertically inside.
func paintHamburger(rgba []byte, w, h int) {
	fillRect(rgba, w, h, HamburgerBtnX, HamburgerBtnY, HamburgerBtnW, HamburgerBtnH, ColorDivider)
	fillRect(rgba, w, h, HamburgerBtnX+1, HamburgerBtnY+1, HamburgerBtnW-2, HamburgerBtnH-2, ColorButtonFace)
	lx := HamburgerBtnX + 7
	ly := HamburgerBtnY + 7
	for i := 0; i < 3; i++ {
		fillRect(rgba, w, h, lx, ly+i*4, 14, 2, ColorTextPrimary)
	}
}

// paintBackButton draws the back-navigation button: flat button face + a
// chevron pointing left in the primary ink.
func paintBackButton(rgba []byte, w, h int) {
	fillRect(rgba, w, h, BackBtnX, BackBtnY, BackBtnW, BackBtnH, ColorDivider)
	fillRect(rgba, w, h, BackBtnX+1, BackBtnY+1, BackBtnW-2, BackBtnH-2, ColorButtonFace)
	cx := BackBtnX + BackBtnW/2 - 3
	cy := BackBtnY + BackBtnH/2
	for i := 0; i < 5; i++ {
		fillRect(rgba, w, h, cx+i, cy-i, 2, 1, ColorTextPrimary)
		fillRect(rgba, w, h, cx+i, cy+i, 2, 1, ColorTextPrimary)
	}
}

// paintForwardButton draws the forward-navigation button: flat button face +
// a chevron pointing right. When enabled is false (no forward history) the
// chevron renders in the disabled ink so it reads as inactive.
func paintForwardButton(rgba []byte, w, h int, enabled bool) {
	fillRect(rgba, w, h, ForwardBtnX, ForwardBtnY, ForwardBtnW, ForwardBtnH, ColorDivider)
	fillRect(rgba, w, h, ForwardBtnX+1, ForwardBtnY+1, ForwardBtnW-2, ForwardBtnH-2, ColorButtonFace)
	ink := ColorButtonDisabled
	if enabled {
		ink = ColorTextPrimary
	}
	cx := ForwardBtnX + ForwardBtnW/2 + 2
	cy := ForwardBtnY + ForwardBtnH/2
	for i := 0; i < 5; i++ {
		fillRect(rgba, w, h, cx-i, cy-i, 2, 1, ink)
		fillRect(rgba, w, h, cx-i, cy+i, 2, 1, ink)
	}
}

// paintPathBar draws the breadcrumb segments to the right of the nav buttons.
// Each path component renders as a button-like region with the label inside;
// the last (active) crumb gets a slightly darker fill so it reads as "you
// are here". Separator chevrons ">" sit between crumbs.
func paintPathBar(rgba []byte, w, h int, s *State) {
	crumbs := PathCrumbs(s.Browser.CurrentPath)
	x := PathBarX
	for i, c := range crumbs {
		active := i == len(crumbs)-1
		labelW := len(c) * FontW * FontScale
		segW := labelW + 2*PathCrumbPad
		if active {
			fillRect(rgba, w, h, x, PathBarY, segW, PathBarH, ColorCrumbActiveBG)
		}
		labelY := PathBarY + (PathBarH-FontH*FontScale)/2
		drawText(rgba, w, h, x+PathCrumbPad, labelY, c, FontScale, ColorTextPrimary)
		x += segW
		if !active {
			// Chevron separator (small ">" in secondary ink).
			drawText(rgba, w, h, x+2, labelY, ">", FontScale, ColorTextSecondary)
			x += FontW*FontScale + 4
		}
	}
}

// PathCrumbs splits a Clean path into its display segments. "/" renders as
// just "Home"; "/Documents" becomes ["Home", "Documents"]. The first segment
// is always "Home" so the active crumb at the root is the Home crumb itself.
func PathCrumbs(p string) []string {
	c := Clean(p)
	if c == "/" {
		return []string{"Home"}
	}
	out := []string{"Home"}
	cur := ""
	for i := 1; i < len(c); i++ {
		if c[i] == '/' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(c[i])
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// paintSidebar draws the left navigation pane: background + two sections
// (Bookmarks and Other Locations) separated by labelled headers. Each
// section header renders in dimmed ink; the entries underneath get a small
// glyph-icon (folder / home star / computer / trash) and the label. The
// currently-selected entry gets the accent fill spanning the full row.
func paintSidebar(rgba []byte, w, h int, s *State) {
	fillRect(rgba, w, h, 0, HeaderBarHeight, SidebarWidth, h-HeaderBarHeight, ColorSidebarBG)
	// Right edge divider.
	fillRect(rgba, w, h, SidebarWidth-1, HeaderBarHeight, 1, h-HeaderBarHeight, ColorDivider)
	// Walk the sidebar entries in order, emitting a section label each time
	// the Section field changes; this lets DefaultSidebar own the section
	// shape without the renderer hard-coding the section list.
	y := HeaderBarHeight + SidebarTopPadding
	prevSection := ""
	for i, e := range s.Sidebar {
		if e.Section != prevSection {
			drawText(rgba, w, h, 12, y+(SidebarSectionHeaderHeight-FontH)/2, e.Section, 1, ColorTextSecondary)
			y += SidebarSectionHeaderHeight
			prevSection = e.Section
		}
		selected := i == s.SidebarSelected
		fg := ColorTextPrimary
		if selected {
			fillRect(rgba, w, h, 0, y, SidebarWidth-1, SidebarRowHeight, ColorAccent)
			fg = ColorOnAccent
		}
		iconY := y + (SidebarRowHeight-12)/2
		paintSidebarIcon(rgba, w, h, 12, iconY, e.Kind, selected)
		labelY := y + (SidebarRowHeight-FontH)/2
		drawText(rgba, w, h, 32, labelY, e.Name, 1, fg)
		y += SidebarRowHeight
	}
}

// paintSidebarIcon dispatches to the correct mini-glyph for the entry kind.
// Selection inverts each glyph's ink to ColorOnAccent so it stays visible on
// the accent fill.
func paintSidebarIcon(rgba []byte, w, h, x, y int, kind string, selected bool) {
	switch kind {
	case "home":
		paintStarIcon(rgba, w, h, x, y, selected)
	case "computer":
		paintComputerIcon(rgba, w, h, x, y, selected)
	case "trash":
		paintTrashIcon(rgba, w, h, x, y, selected)
	default:
		paintMiniFolder(rgba, w, h, x, y, selected)
	}
}

// paintMiniFolder is a small (14x10) folder glyph used in the Bookmarks
// section. Two-tone (tab + body) with a 1px stroke; when selected it inverts
// to white-on-accent.
func paintMiniFolder(rgba []byte, w, h, x, y int, selected bool) {
	face := ColorFolderFill
	tab := ColorFolderTab
	stroke := ColorFolderStroke
	if selected {
		face = ColorOnAccent
		tab = ColorOnAccent
		stroke = [3]uint8{220, 230, 250}
	}
	// Tab.
	fillRect(rgba, w, h, x, y, 6, 2, tab)
	// Body.
	fillRect(rgba, w, h, x, y+2, 14, 8, face)
	// Stroke.
	fillRect(rgba, w, h, x, y+9, 14, 1, stroke)
	fillRect(rgba, w, h, x, y+2, 1, 8, stroke)
	fillRect(rgba, w, h, x+13, y+2, 1, 8, stroke)
}

// paintStarIcon draws a small 5-point star used for the Home entry. The
// star is rendered as a small filled diamond with two horizontal arms so it
// reads at the 12px scale without sub-pixel anti-aliasing.
func paintStarIcon(rgba []byte, w, h, x, y int, selected bool) {
	ink := [3]uint8{240, 180, 70}
	if selected {
		ink = ColorOnAccent
	}
	// Central diamond.
	fillRect(rgba, w, h, x+5, y, 4, 2, ink)
	fillRect(rgba, w, h, x+3, y+2, 8, 2, ink)
	fillRect(rgba, w, h, x+1, y+4, 12, 2, ink)
	fillRect(rgba, w, h, x+3, y+6, 8, 2, ink)
	// Bottom legs.
	fillRect(rgba, w, h, x+2, y+8, 3, 2, ink)
	fillRect(rgba, w, h, x+9, y+8, 3, 2, ink)
}

// paintComputerIcon draws a small monitor: a rectangle with a stand
// underneath. Used for the "Computer" entry in Other Locations.
func paintComputerIcon(rgba []byte, w, h, x, y int, selected bool) {
	face := ColorTextPrimary
	if selected {
		face = ColorOnAccent
	}
	// Monitor frame.
	fillRect(rgba, w, h, x, y, 14, 8, face)
	// Screen (light cutout for the non-selected variant; on the accent fill
	// we keep the screen the same as the frame to maintain contrast).
	inside := ColorSidebarBG
	if selected {
		inside = ColorOnAccent
	}
	fillRect(rgba, w, h, x+1, y+1, 12, 6, inside)
	// Stand.
	fillRect(rgba, w, h, x+5, y+8, 4, 2, face)
	fillRect(rgba, w, h, x+3, y+10, 8, 1, face)
}

// paintTrashIcon draws a small bin: a lid (1px) above the body with two
// vertical ink strokes that read as the bin's slats. Used for the "Trash"
// entry in Other Locations.
func paintTrashIcon(rgba []byte, w, h, x, y int, selected bool) {
	face := ColorTextPrimary
	if selected {
		face = ColorOnAccent
	}
	// Lid + handle.
	fillRect(rgba, w, h, x+1, y, 12, 1, face)
	fillRect(rgba, w, h, x+5, y-1, 4, 1, face)
	// Body outline.
	fillRect(rgba, w, h, x+2, y+1, 1, 9, face)
	fillRect(rgba, w, h, x+11, y+1, 1, 9, face)
	fillRect(rgba, w, h, x+2, y+9, 10, 1, face)
	// Slats.
	fillRect(rgba, w, h, x+5, y+2, 1, 6, face)
	fillRect(rgba, w, h, x+8, y+2, 1, 6, face)
}

// paintColumnHeaders draws the Name / Size labels in the band between the
// header bar and the first list row. The text is heavier (drawTextBold).
// Nautilus's default list view shows just Name + Size in this band.
func paintColumnHeaders(rgba []byte, w, h int) {
	y := HeaderBarHeight
	fillRect(rgba, w, h, SidebarWidth, y, w-SidebarWidth, ColumnHeaderHeight, ColorWindowBG)
	// Bottom divider.
	fillRect(rgba, w, h, SidebarWidth, y+ColumnHeaderHeight-1, w-SidebarWidth, 1, ColorDivider)
	textY := y + (ColumnHeaderHeight-FontH*FontScale)/2
	drawTextBold(rgba, w, h, NameColX, textY, "Name", FontScale, ColorTextSecondary)
	drawTextRight(rgba, w, h, SizeColRight, textY, "Size", FontScale, ColorTextSecondary, true)
}

// paintListRows paints one row per Entry, starting just below the column
// header band. Each row carries: icon + name (left), size (right-aligned at
// SizeColRight). The selected row gets the accent fill across the entire
// right-pane width with white ink.
func paintListRows(rgba []byte, w, h int, s *State) {
	y0 := HeaderBarHeight + ColumnHeaderHeight
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
		nameX := iconX + IconSize + 10
		nameY := y + (RowHeight-FontH*FontScale)/2
		drawText(rgba, w, h, nameX, nameY, e.Name, FontScale, fg)
		// Size (right-aligned).
		size := "--"
		if !e.IsDir {
			size = formatSize(e.Size)
		}
		drawTextRight(rgba, w, h, SizeColRight-16, nameY, size, FontScale, fgSecondary, false)
	}
}

// paintFolderIcon paints a Nautilus-style 24x18 folder: a brighter tab on
// top-left, a slightly darker body extending right + down, with a 1px stroke
// around the whole glyph. When the row is selected the colours flip so the
// icon reads on the accent fill.
func paintFolderIcon(rgba []byte, w, h, x, y int, selected bool) {
	face := ColorFolderFill
	tab := ColorFolderTab
	stroke := ColorFolderStroke
	if selected {
		face = ColorOnAccent
		tab = ColorOnAccent
		stroke = [3]uint8{220, 230, 250}
	}
	// Tab on top-left (~30% of icon width).
	fillRect(rgba, w, h, x, y, 8, 3, tab)
	// Body (lighter face) -- 24 wide, 14 tall, starting 3px below the top.
	fillRect(rgba, w, h, x, y+3, 24, 14, face)
	// 1px stroke around the body.
	fillRect(rgba, w, h, x, y+3, 24, 1, stroke)
	fillRect(rgba, w, h, x, y+16, 24, 1, stroke)
	fillRect(rgba, w, h, x, y+3, 1, 14, stroke)
	fillRect(rgba, w, h, x+23, y+3, 1, 14, stroke)
	// Stroke under the tab.
	fillRect(rgba, w, h, x, y, 8, 1, stroke)
	fillRect(rgba, w, h, x, y, 1, 3, stroke)
	fillRect(rgba, w, h, x+7, y, 1, 3, stroke)
}

// paintFileIcon paints an 18x22 page with a folded top-right corner triangle.
// White paper, gray stroke, fold line. On a selected row the paper flips
// white-on-accent so it stays visible.
func paintFileIcon(rgba []byte, w, h, x, y int, selected bool) {
	paper := ColorFilePaper
	stroke := ColorFileBorder
	if selected {
		paper = ColorOnAccent
		stroke = [3]uint8{220, 230, 250}
	}
	// Body (18 wide x 22 tall) -- centred inside the 18-wide IconSize box.
	fillRect(rgba, w, h, x, y, 18, 22, paper)
	// Stroke.
	fillRect(rgba, w, h, x, y, 18, 1, stroke)
	fillRect(rgba, w, h, x, y+21, 18, 1, stroke)
	fillRect(rgba, w, h, x, y, 1, 22, stroke)
	fillRect(rgba, w, h, x+17, y, 1, 22, stroke)
	// Folded top-right corner: triangular cut starting at (x+12, y).
	// Paint the diagonal in stroke colour + fill the corner above with the
	// sidebar/window BG so the fold reads as "turned over".
	for i := 0; i < 6; i++ {
		fillRect(rgba, w, h, x+12+i, y, 6-i, 1, stroke)
	}
	// Fold diagonal line.
	for i := 0; i < 6; i++ {
		fillRect(rgba, w, h, x+12+i, y+5-i, 1, 1, stroke)
	}
}

// formatSize prettifies a byte count for the Size column. We render a folder
// as "--" (paintListRows takes care of that); for files we use "234 bytes",
// "1.2 KB", "89.1 KB", ... -- Nautilus shows "bytes" for sub-1024 sizes.
func formatSize(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " bytes"
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n) / 1024.0
	idx := 0
	for v >= 1024.0 && idx < len(units)-1 {
		v /= 1024.0
		idx++
	}
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

// drawTextBold paints s twice -- once at (x,y) and once at (x+1,y) -- so the
// strokes thicken. Used by paintColumnHeaders to fake a bold-weight font
// without shipping a second bitmap.
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
// background (no icon, no label, no section header) -- used by render_test
// + probe-files.mjs to sample the sidebar colour for an exact pixel match.
// We aim past the end of the last entry where the BG fill extends down to
// the surface bottom.
func SidebarSamplePoint() (int, int) {
	// One row below the last sidebar entry. DefaultSidebar has 6 entries plus
	// 2 section labels, so 8 SidebarRowHeight-ish bands below HeaderBarHeight
	// is well past the content into pure background.
	return SidebarWidth / 2, HeaderBarHeight + SidebarTopPadding + 2*SidebarSectionHeaderHeight + 6*SidebarRowHeight + 8
}

// WindowBGSamplePoint returns a coordinate inside the right pane, below the
// column headers, in a corner with no row content.
func WindowBGSamplePoint(w, h int) (int, int) {
	return w - 4, h - 4
}

// HeaderBarSamplePoint returns a coordinate inside the header bar, between
// the path bar and the right edge, guaranteed to be empty header-bar BG.
func HeaderBarSamplePoint(w int) (int, int) {
	return w - 6, HeaderBarHeight / 2
}
