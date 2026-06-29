// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's render.go paints a VS Code Dark+-inspired editor into an
// RGBA32 buffer. Layout (900x540):
//
//	+----------+----+----------------------------------------+ tab strip (28)
//	| sidebar  | tab| (active tab fill)                      |
//	|  (200)   +----+----------------------------------------+
//	|          | gutter | editor pane                        | (rows of LineHeight)
//	|          | (50)   |                                    |
//	|          |        |                                    |
//	|          |        |                                    |
//	+----------+--------+------------------------------------+ status bar (24)
//	| path | Ln L, Col C | TEXT | Live Server: Not connected |
//	+-------------------------------------------------------+
//
// Pure Go (no syscall/js, no cgo) so the renderer builds for every
// architecture this repo targets and is unit-tested natively. Each paint
// stage is its own helper so tests can exercise them in isolation.

package scene

import "strconv"

// Visual constants -- exported so tests + the playwright probe can pin
// layout invariants. The defaults reproduce VS Code Dark+ proportions on
// a 900x540 surface.
const (
	// SidebarWidth is the left navigation pane width.
	SidebarWidth = 200
	// TabStripHeight is the strip above the editor that hosts the file tab.
	TabStripHeight = 28
	// GutterWidth is the line-number gutter width to the right of the sidebar.
	GutterWidth = 50
	// StatusBarHeight is the height of the bottom signature-blue strip.
	StatusBarHeight = 24
	// LineHeight is the vertical pitch of one editor line (font height * scale + a 2 px padding).
	LineHeight = 18
	// SidebarRowHeight is the height of one file-tree row in the sidebar.
	SidebarRowHeight = 20
	// EditorFontScale is the integer scale applied to the 8x8 font on the
	// editor pane (FontH=8 -> 16 px tall glyphs, comfortable on the 900x540 surface).
	EditorFontScale = 2
	// SidebarFontScale is the scale used on sidebar rows + status bar text.
	SidebarFontScale = 1
	// LiveServerWidth is the right-most clickable region in the status bar.
	LiveServerWidth = 220

	// Popup geometry. The "Connect to Live Server" popup is a centred
	// panel; its Connect button sits in the bottom-right corner.
	PopupX        = 250
	PopupY        = 180
	PopupW        = 400
	PopupH        = 160
	PopupConnectX = PopupX + PopupW - 100
	PopupConnectY = PopupY + PopupH - 40
	PopupConnectW = 80
	PopupConnectH = 28
)

// Palette -- VS Code Dark+. Colours are uint8 RGB triples (alpha is forced
// to 0xFF on write). Names match the VS Code token roles so the playwright
// probe can sample for an exact pixel match.
var (
	// ColorWindowBG is the editor pane / window background (#1E1E1E).
	ColorWindowBG = [3]uint8{0x1E, 0x1E, 0x1E}
	// ColorSidebarBG is the left navigation pane background (#252526).
	ColorSidebarBG = [3]uint8{0x25, 0x25, 0x26}
	// ColorTabStripBG is the tab strip background (#2D2D30).
	ColorTabStripBG = [3]uint8{0x2D, 0x2D, 0x30}
	// ColorActiveTabBG is the active-tab fill (#1E1E1E -- same as editor BG).
	ColorActiveTabBG = ColorWindowBG
	// ColorGutterText is the dim ink used by the line-number gutter (#858585).
	ColorGutterText = [3]uint8{0x85, 0x85, 0x85}
	// ColorStatusBarBG is the signature blue (#007ACC).
	ColorStatusBarBG = [3]uint8{0x00, 0x7A, 0xCC}
	// ColorStatusBarText is white (#FFFFFF).
	ColorStatusBarText = [3]uint8{0xFF, 0xFF, 0xFF}
	// ColorFlashSaveOK paints the status bar green when a save succeeds (#16825D, VS Code's git-added green).
	ColorFlashSaveOK = [3]uint8{0x16, 0x82, 0x5D}
	// ColorFlashInfo paints the status bar a neutral darker blue when the
	// Live Server stub trips (#0E639C, VS Code's button-secondary).
	ColorFlashInfo = [3]uint8{0x0E, 0x63, 0x9C}
	// ColorSidebarTextDim is the dim ink for the sidebar (#CCCCCC).
	ColorSidebarTextDim = [3]uint8{0xCC, 0xCC, 0xCC}
	// ColorCursor is the editor caret colour (#AEAFAD -- VS Code default caret).
	ColorCursor = [3]uint8{0xAE, 0xAF, 0xAD}
	// ColorPopupBG is the popup panel background (#252526).
	ColorPopupBG = [3]uint8{0x25, 0x25, 0x26}
	// ColorPopupBorder is the popup panel border (#454545).
	ColorPopupBorder = [3]uint8{0x45, 0x45, 0x45}
	// ColorPopupButton is the Connect button background (#0E639C).
	ColorPopupButton = [3]uint8{0x0E, 0x63, 0x9C}
)

// Render paints the current scene into buf. Panics on size mismatch -- a
// misshaped buffer in the caller is a bug, not a recoverable state.
func Render(s *SceneState, buf []byte) {
	need := 4 * s.W * s.H
	if len(buf) != need {
		panic("scene: Render buffer size mismatch")
	}
	Paint(buf, s.W, s.H, s)
}

// Paint is the renderer's pure entry point -- separated from Render so tests
// can pass a *SceneState directly without constructing through New().
func Paint(rgba []byte, w, h int, s *SceneState) {
	// Window background underpaints everything (then sidebar + tab strip +
	// status bar + popup overpaint as needed).
	fillRect(rgba, w, h, 0, 0, w, h, ColorWindowBG)
	paintSidebar(rgba, w, h, s)
	paintTabStrip(rgba, w, h, s)
	paintEditor(rgba, w, h, s)
	paintStatusBar(rgba, w, h, s)
	if s.LiveServerPopupOpen {
		paintLiveServerPopup(rgba, w, h, s)
	}
}

// paintSidebar paints the left navigation pane background + a flat
// top-level file listing. Each row carries the entry name; folders get a
// "[F]" prefix to keep the icon paint cheap (8x8 font, no extra glyph
// table). Below the last entry the sidebar background fills to the bottom.
func paintSidebar(rgba []byte, w, h int, s *SceneState) {
	fillRect(rgba, w, h, 0, 0, SidebarWidth, h, ColorSidebarBG)
	y := TabStripHeight
	for _, e := range s.FileTree {
		if y+SidebarRowHeight > h-StatusBarHeight {
			break
		}
		prefix := "  "
		if e.IsDir {
			prefix = "> "
		}
		drawText(rgba, w, h, 8, y+(SidebarRowHeight-FontH)/2, prefix+e.Name, SidebarFontScale, ColorSidebarTextDim)
		y += SidebarRowHeight
	}
}

// paintTabStrip paints the strip above the editor that hosts a single
// file-tab. The strip background covers the entire surface width; the
// active tab is a SidebarWidth-aligned segment with the file basename.
func paintTabStrip(rgba []byte, w, h int, s *SceneState) {
	fillRect(rgba, w, h, SidebarWidth, 0, w-SidebarWidth, TabStripHeight, ColorTabStripBG)
	label := "untitled"
	if s.CurrentPath != "" {
		label = sharedvfsBasename(s.CurrentPath)
	}
	tabW := len(label)*FontW*SidebarFontScale + 24
	if tabW < 100 {
		tabW = 100
	}
	fillRect(rgba, w, h, SidebarWidth, 0, tabW, TabStripHeight, ColorActiveTabBG)
	drawText(rgba, w, h, SidebarWidth+12, (TabStripHeight-FontH)/2, label, SidebarFontScale, ColorSidebarTextDim)
}

// paintEditor paints the line-number gutter + the editor pane (syntax-
// highlighted lines + cursor). Lines render top-down starting just below
// the tab strip; the visible row count is bounded by what fits between the
// tab strip and the status bar.
func paintEditor(rgba []byte, w, h int, s *SceneState) {
	y0 := TabStripHeight
	y1 := h - StatusBarHeight
	// Gutter background (same as editor BG so the seam is invisible).
	fillRect(rgba, w, h, SidebarWidth, y0, GutterWidth, y1-y0, ColorWindowBG)
	visible := (y1 - y0) / LineHeight
	for i, line := range s.Buffer.Lines {
		if i >= visible {
			break
		}
		ly := y0 + i*LineHeight + (LineHeight-FontH*EditorFontScale)/2
		// Line number, right-aligned inside the gutter.
		ln := strconv.Itoa(i + 1)
		lnX := SidebarWidth + GutterWidth - 8 - len(ln)*FontW*SidebarFontScale
		drawText(rgba, w, h, lnX, ly+(FontH*EditorFontScale-FontH)/2, ln, SidebarFontScale, ColorGutterText)
		// Tokenized line.
		tx := SidebarWidth + GutterWidth
		for _, tok := range Tokenize(line) {
			drawText(rgba, w, h, tx, ly, tok.Text, EditorFontScale, tok.Color)
			tx += len(tok.Text) * FontW * EditorFontScale
		}
	}
	// Cursor: 2-px-wide vertical bar at the (Row, Col) position.
	cr := s.Buffer.Cursor.Row
	cc := s.Buffer.Cursor.Col
	if cr < visible {
		cx := SidebarWidth + GutterWidth + cc*FontW*EditorFontScale
		cy := y0 + cr*LineHeight + (LineHeight-FontH*EditorFontScale)/2
		fillRect(rgba, w, h, cx, cy, 2, FontH*EditorFontScale, ColorCursor)
	}
}

// paintStatusBar paints the bottom signature-blue strip with the current
// file path + (Ln L, Col C) + "TEXT" + "Live Server: Not connected". The
// strip flashes green on save (FlashSaveOK) or neutral blue on Live Server
// stubs (FlashInfo); the flash colour overpaints the signature blue for
// the entire strip width.
func paintStatusBar(rgba []byte, w, h int, s *SceneState) {
	bg := ColorStatusBarBG
	switch s.Flash {
	case FlashSaveOK:
		bg = ColorFlashSaveOK
	case FlashInfo:
		bg = ColorFlashInfo
	}
	fillRect(rgba, w, h, 0, h-StatusBarHeight, w, StatusBarHeight, bg)
	textY := h - StatusBarHeight + (StatusBarHeight-FontH)/2
	// Left: current file path or "[no file]".
	left := s.CurrentPath
	if left == "" {
		left = "[no file]"
	}
	drawText(rgba, w, h, 12, textY, left, SidebarFontScale, ColorStatusBarText)
	// Middle: Ln L, Col C.
	mid := "Ln " + strconv.Itoa(s.Buffer.Cursor.Row+1) + ", Col " + strconv.Itoa(s.Buffer.Cursor.Col+1)
	midX := w/2 - len(mid)*FontW*SidebarFontScale/2 - 40
	drawText(rgba, w, h, midX, textY, mid, SidebarFontScale, ColorStatusBarText)
	// Right of middle: "TEXT" indicator.
	modeX := w/2 + 60
	drawText(rgba, w, h, modeX, textY, "TEXT", SidebarFontScale, ColorStatusBarText)
	// Right: Live Server status.
	liveLabel := "Live Server: Not connected"
	liveX := w - len(liveLabel)*FontW*SidebarFontScale - 12
	drawText(rgba, w, h, liveX, textY, liveLabel, SidebarFontScale, ColorStatusBarText)
}

// paintLiveServerPopup paints the "Connect to Live Server" overlay: a
// centred panel with a header label, a flat "wss://" URL field, and a
// Connect button in the bottom-right corner. The button is the only
// clickable region; clicking outside the panel dismisses without flashing.
func paintLiveServerPopup(rgba []byte, w, h int, s *SceneState) {
	// Drop-shadow row for depth.
	fillRect(rgba, w, h, PopupX+4, PopupY+4, PopupW, PopupH, ColorPopupBorder)
	// Panel + 1px border.
	fillRect(rgba, w, h, PopupX, PopupY, PopupW, PopupH, ColorPopupBG)
	fillRect(rgba, w, h, PopupX, PopupY, PopupW, 1, ColorPopupBorder)
	fillRect(rgba, w, h, PopupX, PopupY+PopupH-1, PopupW, 1, ColorPopupBorder)
	fillRect(rgba, w, h, PopupX, PopupY, 1, PopupH, ColorPopupBorder)
	fillRect(rgba, w, h, PopupX+PopupW-1, PopupY, 1, PopupH, ColorPopupBorder)
	// Header label.
	drawText(rgba, w, h, PopupX+16, PopupY+16, "Connect to Live Server", SidebarFontScale, ColorSidebarTextDim)
	// URL field placeholder (a flat darker rectangle with a "wss://" prefix).
	fillRect(rgba, w, h, PopupX+16, PopupY+56, PopupW-32, 28, ColorWindowBG)
	url := "wss://"
	if s.LiveServerURL != "" {
		url = s.LiveServerURL
	}
	drawText(rgba, w, h, PopupX+24, PopupY+56+(28-FontH)/2, url, SidebarFontScale, ColorSidebarTextDim)
	// Connect button.
	fillRect(rgba, w, h, PopupConnectX, PopupConnectY, PopupConnectW, PopupConnectH, ColorPopupButton)
	drawText(rgba, w, h, PopupConnectX+14, PopupConnectY+(PopupConnectH-FontH)/2, "Connect", SidebarFontScale, ColorStatusBarText)
}

// sharedvfsBasename returns the trailing path component of p. Pulled into a
// local helper so the renderer keeps a tight import set (no sharedvfs.Basename
// pulled in, which would otherwise transitively pull every path helper into
// the renderer's symbol table).
func sharedvfsBasename(p string) string {
	if p == "" {
		return ""
	}
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

// drawText paints s starting at (x, y) using the 8x8 font scaled by scale.
// Out-of-range bytes (< 0x20) are skipped. Mirrors the files/terminal
// renderers' helper so the call sites read the same here.
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

// drawGlyph paints one 8x8 glyph at (x, y) scaled by scale. Pixels are only
// written where the glyph bit is set -- the row background is whatever the
// caller painted before calling drawText.
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
