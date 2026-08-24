// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's render.go paints a VS Code Dark+-inspired editor into an
// RGBA32 buffer. The whole chrome is a go-widgets/toolkit widget tree laid
// out by containers rather than hand-computed rectangles:
//
//	root  Dock                                          — the editor shell
//	├─ (docked South, StatusBarHeight)  status  Statusbar
//	└─ (body)  HBox
//	   ├─ (fixed SidebarWidth)  sidebar  ListBox       — the file tree
//	   └─ (flex)  VBox
//	      ├─ (fixed TabStripHeight)  tabs  FolderTabs  — the active-file tab
//	      └─ (flex)  editor  TextView                  — gutter + highlighted text
//
// A single root.SetBounds lays the tree out (see state.go buildLayout); this
// file paints it. Because the VS Code look needs four distinct region
// backgrounds (sidebar #252526, editor #1E1E1E, tab strip #2D2D30, status
// bar #007ACC) that no single Theme field can express, each widget is drawn
// with the theme carrying its own region colours (sidebarTheme / editorTheme
// / tabTheme / statusTheme / popupTheme) rather than one shared theme — the
// container tree still owns geometry + event routing, this file only owns ink.
//
// Pure Go (no syscall/js, no cgo) so the renderer builds for every
// architecture this repo targets and is unit-tested natively.

package scene

import (
	"strconv"

	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
)

// Visual constants -- exported so tests + the playwright probe can pin
// layout invariants. The horizontal offsets reproduce VS Code Dark+
// proportions; the editor's line-number gutter is now owned by the TextView
// widget (ShowLineNumbers) and sized to the widest line number, so there is
// no longer a fixed GutterWidth constant here.
const (
	// SidebarWidth is the left navigation pane width.
	SidebarWidth = 200
	// TabStripHeight is the strip above the editor that hosts the file tab.
	TabStripHeight = 28
	// StatusBarHeight is the height of the bottom signature-blue strip.
	StatusBarHeight = 24
	// SidebarRowHeight is the height of one file-tree row in the sidebar.
	SidebarRowHeight = 20
	// DialogW / DialogH are the centred "Connect to Live Server" popup size.
	DialogW = 400
	DialogH = 180
)

// Palette -- VS Code Dark+. Colours are uint8 RGB triples (alpha is forced
// to 0xFF on paint via rgb()). Names match the VS Code token roles so the
// playwright probe can sample for an exact pixel match.
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
	// ColorFlashSaveOK paints the status bar green when a save succeeds (#16825D).
	ColorFlashSaveOK = [3]uint8{0x16, 0x82, 0x5D}
	// ColorFlashInfo paints the status bar a neutral darker blue on the Live
	// Server stub (#0E639C, VS Code's button-secondary).
	ColorFlashInfo = [3]uint8{0x0E, 0x63, 0x9C}
	// ColorSidebarTextDim is the dim ink for the sidebar (#CCCCCC).
	ColorSidebarTextDim = [3]uint8{0xCC, 0xCC, 0xCC}
	// ColorSelectionBG is the sidebar's active-row highlight (#094771,
	// VS Code list.activeSelectionBackground).
	ColorSelectionBG = [3]uint8{0x09, 0x47, 0x71}
	// ColorPopupBG is the popup panel background (#252526).
	ColorPopupBG = [3]uint8{0x25, 0x25, 0x26}
	// ColorPopupBorder is the popup panel border (#454545).
	ColorPopupBorder = [3]uint8{0x45, 0x45, 0x45}
	// ColorPopupButton is the Connect button accent (#0E639C).
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
// can pass a *SceneState directly. It fills the two backgrounds that the
// widgets don't cover themselves (window ground + sidebar band) and then
// draws the widget tree, each widget in its region's theme.
func Paint(rgba []byte, w, h int, s *SceneState) {
	p := painter.NewPixelPainter(rgba, w, h)

	// Window ground (also the editor + tab backgrounds are painted by their
	// widgets over this) and the sidebar band -- the ListBox only paints its
	// rows, so the empty area below the file list must be filled here so the
	// sidebar reads as one solid #252526 column top to bottom.
	fillBox(p, toolkit.Rect{X: 0, Y: 0, W: w, H: h}, rgb(ColorWindowBG))
	fillBox(p, toolkit.Rect{X: 0, Y: 0, W: SidebarWidth, H: h - StatusBarHeight}, rgb(ColorSidebarBG))

	// Live status-bar segments (path / Ln,Col / mode / Live Server) + the tab
	// label (the open file's basename), both refreshed from live state each frame.
	s.syncStatusSegments()
	s.syncTabs()

	s.sidebar.Draw(p, s.sidebarTheme)
	s.tabs.Draw(p, s.tabTheme)
	s.Editor.Draw(p, s.editorTheme)
	s.status.Draw(p, s.statusTheme())

	if s.LiveServerPopupOpen {
		s.layoutDialog()
		s.urlEntry.SetText(s.LiveServerURL)
		s.dialog.Draw(p, s.popupTheme)
	}
}

// statusTheme returns the theme the status bar draws with: its background
// (SurfaceAlt, which Statusbar fills) is the signature blue, or the flash
// colour when a save / Live-Server pulse is active. Border is set to the same
// colour so the widget's frame + segment dividers vanish into the strip
// (VS Code's status bar has neither).
func (s *SceneState) statusTheme() *toolkit.Theme {
	bg := ColorStatusBarBG
	switch s.Flash {
	case FlashSaveOK:
		bg = ColorFlashSaveOK
	case FlashInfo:
		bg = ColorFlashInfo
	}
	return &toolkit.Theme{
		SurfaceAlt: rgb(bg),
		OnSurface:  rgb(ColorStatusBarText),
		Border:     rgb(bg),
	}
}

// syncStatusSegments refreshes the status bar from the live editor state,
// modelling it as interactive StatusSegments so the toolkit — not hand-rolled
// `x >= W-liveServerWidth` math — owns the click hit-test. The Left group holds
// the non-interactive readouts (current path, 1-based cursor position, mode);
// the Right group holds the single clickable Live Server segment, whose OnClick
// opens the popup. Rebuilt each frame + before a status-bar hit-test.
func (s *SceneState) syncStatusSegments() {
	path := s.CurrentPath
	if path == "" {
		path = "[no file]"
	}
	lnCol := "Ln " + strconv.Itoa(s.Editor.CursorLine().Get()+1) + ", Col " + strconv.Itoa(s.Editor.CursorCol().Get()+1)
	s.status.Left = []toolkit.StatusSegment{
		{Text: path},
		{Text: lnCol},
		{Text: "TEXT"},
	}
	s.status.Right = []toolkit.StatusSegment{
		{Text: "Live Server: Not connected", OnClick: s.openLiveServer},
	}
}

// syncTabs mirrors the open file's basename into the FolderTabs strip's single
// tab label, so the tab reflects the current file without rebuilding the widget.
func (s *SceneState) syncTabs() {
	s.tabs.Labels = []string{s.tabLabel()}
}

// basename returns the trailing path component of p (the tab label). A local
// helper keeps the renderer's import set tight.
func basename(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}
