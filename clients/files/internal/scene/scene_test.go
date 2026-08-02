// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import (
	"testing"

	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

const (
	surfW = 720
	surfH = 440
)

func newSurface(w, h int) []byte { return make([]byte, 4*w*h) }

func newEmptyVFS() VFS { return sharedvfs.NewInMemoryVFS() }

// pixelAt reads the RGBA32 sample at (x,y) from a w-wide buffer.
func pixelAt(buf []byte, w, x, y int) [4]uint8 {
	off := (y*w + x) * 4
	return [4]uint8{buf[off], buf[off+1], buf[off+2], buf[off+3]}
}

func eqRGB(p [4]uint8, c toolkit.RGBA) bool {
	return p[0] == c.R && p[1] == c.G && p[2] == c.B
}

// countIn counts pixels inside (x,y,rw,rh) of a w-wide buffer matching c.
func countIn(buf []byte, w, x, y, rw, rh int, c toolkit.RGBA) int {
	n := 0
	for yy := y; yy < y+rh; yy++ {
		for xx := x; xx < x+rw; xx++ {
			if eqRGB(pixelAt(buf, w, xx, yy), c) {
				n++
			}
		}
	}
	return n
}

// list-row geometry helpers (surface coordinates).
func listTop() int      { return HeaderBarHeight + ColumnHeaderHeight }
func rowCenterY(i int) int { return listTop() + i*RowHeight + RowHeight/2 }

// firstBookmarkRowY is the y of the Home row (first entry under BOOKMARKS).
func firstBookmarkRowY() int {
	return HeaderBarHeight + SidebarTopPadding + SidebarSectionHeaderHeight
}

// --- construction + basic render -----------------------------------------

func TestNewSeedsRoot(t *testing.T) {
	s := New(surfW, surfH)
	if s.W != surfW || s.H != surfH {
		t.Fatalf("dims = (%d,%d)", s.W, s.H)
	}
	if s.Browser.CurrentPath != "/" {
		t.Errorf("CurrentPath = %q, want /", s.Browser.CurrentPath)
	}
	if len(s.Browser.Entries) != 4 {
		t.Errorf("root entries = %d, want 4", len(s.Browser.Entries))
	}
	if s.SidebarSelected != 0 {
		t.Errorf("SidebarSelected = %d, want 0 (Home owns /)", s.SidebarSelected)
	}
	if len(s.list.rows) != 4 {
		t.Errorf("list rows = %d, want 4", len(s.list.rows))
	}
	if got := s.crumbs.Segments; len(got) != 1 || got[0] != "Home" {
		t.Errorf("crumbs = %v, want [Home]", got)
	}
}

func TestRenderExactSize(t *testing.T) {
	s := New(surfW, surfH)
	Render(s, newSurface(surfW, surfH))
}

func TestRenderPanicsOnSizeMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on size mismatch")
		}
	}()
	Render(New(64, 64), make([]byte, 4))
}

// --- golden-rect proof ----------------------------------------------------

// TestGoldenRects asserts the container tree lays each chrome widget out at the
// exact bounds the pre-migration hand-placed renderer produced.
func TestGoldenRects(t *testing.T) {
	s := New(surfW, surfH)

	checks := []struct {
		name string
		got  toolkit.Rect
		want toolkit.Rect
	}{
		{"hamburger", s.hamburger.Bounds(), toolkit.Rect{X: 8, Y: 10, W: 28, H: 24}},
		{"back", s.backBtn.Bounds(), toolkit.Rect{X: 46, Y: 10, W: 28, H: 24}},
		{"forward", s.fwdBtn.Bounds(), toolkit.Rect{X: 78, Y: 10, W: 28, H: 24}},
		{"sidebar", s.sidebar.Bounds(), toolkit.Rect{X: 0, Y: 44, W: 160, H: 396}},
		{"content", s.content.Bounds(), toolkit.Rect{X: 160, Y: 44, W: 560, H: 396}},
		{"colHeader", s.colHeader.Bounds(), toolkit.Rect{X: 160, Y: 44, W: 560, H: 28}},
		{"list", s.list.Bounds(), toolkit.Rect{X: 160, Y: 72, W: 560, H: 368}},
		{"row0", s.list.rows[0].Bounds(), toolkit.Rect{X: 160, Y: 72, W: 560, H: 32}},
		{"row1", s.list.rows[1].Bounds(), toolkit.Rect{X: 160, Y: 104, W: 560, H: 32}},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s bounds = %+v, want %+v", c.name, c.got, c.want)
		}
	}
	// Breadcrumbs start after the nav buttons + flex to the surface edge.
	cb := s.crumbs.Bounds()
	if cb.X != 118 || cb.Y != 10 || cb.H != 24 || cb.W != surfW-118 {
		t.Errorf("crumbs bounds = %+v, want X=118 Y=10 H=24 W=%d", cb, surfW-118)
	}
	// Sidebar Home + Documents rows land at the walked y positions.
	if got := s.sidebarRows[0].Bounds(); got != (toolkit.Rect{X: 0, Y: 74, W: 160, H: 28}) {
		t.Errorf("Home row = %+v, want {0 74 160 28}", got)
	}
	if got := s.sidebarRows[1].Bounds(); got != (toolkit.Rect{X: 0, Y: 102, W: 160, H: 28}) {
		t.Errorf("Documents row = %+v, want {0 102 160 28}", got)
	}
}

// TestGoldenRectsWidth checks the flex members track the surface width.
func TestGoldenRectsWidth(t *testing.T) {
	for _, w := range []int{600, 900, 1280} {
		s := New(w, 480)
		if got := s.crumbs.Bounds().W; got != w-118 {
			t.Errorf("w=%d: crumbs width = %d, want %d", w, got, w-118)
		}
		if got := s.list.Bounds().W; got != w-SidebarWidth {
			t.Errorf("w=%d: list width = %d, want %d", w, got, w-SidebarWidth)
		}
	}
}

// --- pixel parity (mirrors test/probe-files.mjs) --------------------------

// TestPixelParity reproduces the headless probe's exact-colour assertions
// natively so the migration keeps the browser probe green (and so a palette /
// geometry regression fails in `go test` first).
func TestPixelParity(t *testing.T) {
	s := New(surfW, surfH)
	buf := newSurface(surfW, surfH)
	Render(s, buf)

	// Region background samples.
	if px := pixelAt(buf, surfW, SidebarWidth-4, HeaderBarHeight+100); !eqRGB(px, ColorSidebarBG) {
		t.Errorf("sidebar BG = %v, want %v", px, ColorSidebarBG)
	}
	if px := pixelAt(buf, surfW, surfW-4, surfH-4); !eqRGB(px, ColorWindowBG) {
		t.Errorf("window BG = %v, want %v", px, ColorWindowBG)
	}
	if px := pixelAt(buf, surfW, surfW-6, HeaderBarHeight/2); !eqRGB(px, ColorHeaderBarBG) {
		t.Errorf("header BG = %v, want %v", px, ColorHeaderBarBG)
	}

	// Selected row 0 accent strip at the list top.
	if px := pixelAt(buf, surfW, SidebarWidth+4, rowCenterY(0)); !eqRGB(px, ColorAccent) {
		t.Errorf("row 0 accent = %v, want %v", px, ColorAccent)
	}

	// Foreground content: folder icon + name + sidebar text (the "frame with
	// nothing inside" regression guard).
	iconY := listTop() + RowHeight + (RowHeight-IconSize)/2
	if n := countIn(buf, surfW, NameColX, iconY, 24, IconSize, ColorFolderFill); n < 30 {
		t.Errorf("list row 1 folder-fill pixels = %d, want >= 30", n)
	}
	if n := countIn(buf, surfW, NameColX+IconSize+10, listTop()+RowHeight, 160, RowHeight, ColorTextPrimary); n < 20 {
		t.Errorf("list row 1 name-text pixels = %d, want >= 20", n)
	}
	sbDocsY := firstBookmarkRowY() + SidebarRowHeight
	if n := countIn(buf, surfW, 30, sbDocsY, SidebarWidth-32, SidebarRowHeight, ColorTextPrimary); n < 6 {
		t.Errorf("sidebar Documents text = %d, want >= 6", n)
	}
	if n := countIn(buf, surfW, 8, firstBookmarkRowY(), SidebarWidth-16, SidebarRowHeight, ColorOnAccent); n < 8 {
		t.Errorf("sidebar Home selected white pixels = %d, want >= 8", n)
	}
	// Column-header labels in secondary ink.
	if n := countIn(buf, surfW, SidebarWidth, HeaderBarHeight, surfW-SidebarWidth, ColumnHeaderHeight, ColorTextSecondary); n < 10 {
		t.Errorf("column-header secondary ink = %d, want >= 10", n)
	}
}

// ArrowDown migrates the accent strip one row down.
func TestArrowDownMovesAccent(t *testing.T) {
	s := New(surfW, surfH)
	if !s.HandleKey("ArrowDown") {
		t.Fatal("ArrowDown returned false")
	}
	buf := newSurface(surfW, surfH)
	Render(s, buf)
	if px := pixelAt(buf, surfW, SidebarWidth+4, rowCenterY(0)); eqRGB(px, ColorAccent) {
		t.Errorf("row 0 still accent after ArrowDown: %v", px)
	}
	if px := pixelAt(buf, surfW, SidebarWidth+4, rowCenterY(1)); !eqRGB(px, ColorAccent) {
		t.Errorf("row 1 not accent after ArrowDown: %v", px)
	}
}

// --- keyboard -------------------------------------------------------------

func TestHandleKey(t *testing.T) {
	s := New(surfW, surfH)
	if !s.HandleKey("ArrowDown") || s.Browser.Cursor != 1 {
		t.Errorf("ArrowDown: cursor = %d", s.Browser.Cursor)
	}
	if !s.HandleKey("ArrowUp") || s.Browser.Cursor != 0 {
		t.Errorf("ArrowUp: cursor = %d", s.Browser.Cursor)
	}
	if s.HandleKey("ArrowUp") {
		t.Error("ArrowUp at top returned true")
	}
	if !s.HandleKey("Enter") || s.Browser.CurrentPath != "/Documents" {
		t.Errorf("Enter: path = %q", s.Browser.CurrentPath)
	}
	if s.SidebarSelected != 1 {
		t.Errorf("SidebarSelected after Enter = %d, want 1", s.SidebarSelected)
	}
	if got := s.crumbs.Segments; len(got) != 2 || got[1] != "Documents" {
		t.Errorf("crumbs after Enter = %v", got)
	}
	if !s.HandleKey("Escape") || s.Browser.CurrentPath != "/" {
		t.Errorf("Escape: path = %q", s.Browser.CurrentPath)
	}
	_ = s.HandleKey("Enter")
	if !s.HandleKey("Backspace") || s.Browser.CurrentPath != "/" {
		t.Errorf("Backspace: path = %q", s.Browser.CurrentPath)
	}
	if s.HandleKey("F1") {
		t.Error("F1 returned true")
	}
}

func TestHandleKeyEnterOnFileAndBackspaceAtRoot(t *testing.T) {
	s := New(surfW, surfH)
	s.Browser.Cursor = 3 // about.txt
	if s.HandleKey("Enter") {
		t.Error("Enter on file returned true")
	}
	if s.HandleKey("Backspace") {
		t.Error("Backspace at root returned true")
	}
}

// --- mouse: list rows -----------------------------------------------------

func TestClickFolderRowDescends(t *testing.T) {
	s := New(surfW, surfH)
	if !s.HandleMouse(SidebarWidth+50, rowCenterY(0)) {
		t.Fatal("click on folder row returned false")
	}
	if s.Browser.CurrentPath != "/Documents" {
		t.Errorf("path = %q, want /Documents", s.Browser.CurrentPath)
	}
	if len(s.list.rows) != 2 {
		t.Errorf("list rows after descent = %d, want 2", len(s.list.rows))
	}
}

func TestClickFileRowSelects(t *testing.T) {
	s := New(surfW, surfH)
	if !s.HandleMouse(SidebarWidth+50, rowCenterY(3)) {
		t.Fatal("click on file row returned false")
	}
	if s.Browser.Cursor != 3 {
		t.Errorf("cursor = %d, want 3", s.Browser.Cursor)
	}
	if s.Browser.CurrentPath != "/" {
		t.Errorf("path = %q, want /", s.Browser.CurrentPath)
	}
}

func TestClickColumnHeaderAndEmptyAreaAreNoops(t *testing.T) {
	s := New(surfW, surfH)
	if s.HandleMouse(SidebarWidth+50, HeaderBarHeight+ColumnHeaderHeight/2) {
		t.Error("column-header click returned true")
	}
	if s.HandleMouse(SidebarWidth+50, listTop()+10*RowHeight) {
		t.Error("empty-area click returned true")
	}
}

// A file selected on a later row exercises drawFileIcon's selected branch.
func TestRenderSelectedFileRow(t *testing.T) {
	s := New(surfW, surfH)
	s.Browser.Cursor = 3 // about.txt
	Render(s, newSurface(surfW, surfH))
}

// fileList.Draw clips rows past a short surface (the break branch).
func TestRenderClipsRowsBeyondSurface(t *testing.T) {
	h := HeaderBarHeight + ColumnHeaderHeight + RowHeight + 2
	s := New(surfW, h)
	Render(s, newSurface(surfW, h)) // must not panic
}

// fileList.OnEvent ignores non-click events + clicks that miss every row.
func TestFileListOnEventEdges(t *testing.T) {
	s := New(surfW, surfH)
	s.list.OnEvent(toolkit.Event{Kind: toolkit.EventKeyDown})
	// A click far below the last row hits no row.
	b := s.list.Bounds()
	s.dirty = false
	s.list.OnEvent(toolkit.Event{Kind: toolkit.EventClick, X: 10, Y: b.H - 1})
	if s.dirty {
		t.Error("miss click set dirty")
	}
	if s.list.rowAt(0, 0) != -1 {
		t.Error("rowAt outside bounds should be -1")
	}
}

// --- mouse: header controls ----------------------------------------------

func TestHeaderButtons(t *testing.T) {
	s := New(surfW, surfH)
	// Back at root is a no-op.
	if s.HandleMouse(s.backBtn.Bounds().X+5, 22) {
		t.Error("back at root returned true")
	}
	// Descend then back returns to root.
	_ = s.Browser.ActivateCurrent(s.VFS)
	s.refreshView()
	if !s.HandleMouse(s.backBtn.Bounds().X+5, 22) {
		t.Error("back after descent returned false")
	}
	if s.Browser.CurrentPath != "/" {
		t.Errorf("path after back = %q", s.Browser.CurrentPath)
	}
	// Hamburger + forward are no-op stubs.
	if s.HandleMouse(s.hamburger.Bounds().X+5, 22) {
		t.Error("hamburger returned true")
	}
	if s.HandleMouse(s.fwdBtn.Bounds().X+5, 22) {
		t.Error("forward returned true")
	}
}

// --- mouse: sidebar -------------------------------------------------------

func TestSidebarNavigation(t *testing.T) {
	s := New(surfW, surfH)
	homeY := firstBookmarkRowY() + SidebarRowHeight/2
	// Home at root is already selected -> no-op.
	if s.HandleMouse(20, homeY) {
		t.Error("Home at root returned true")
	}
	// Documents navigates.
	docsY := firstBookmarkRowY() + SidebarRowHeight + SidebarRowHeight/2
	if !s.HandleMouse(20, docsY) {
		t.Fatal("Documents click returned false")
	}
	if s.Browser.CurrentPath != "/Documents" || s.SidebarSelected != 1 {
		t.Errorf("after Documents: path=%q sel=%d", s.Browser.CurrentPath, s.SidebarSelected)
	}
	// Repeated click is a no-op.
	if s.HandleMouse(20, docsY) {
		t.Error("repeated Documents click returned true")
	}
}

// Computer at root shares "/" with Home but a different index, so the selection
// still moves (the "same path, different idx" branch).
func TestSidebarComputerAtRoot(t *testing.T) {
	s := New(surfW, surfH)
	computerY := firstBookmarkRowY() + 4*SidebarRowHeight + SidebarSectionHeaderHeight + SidebarRowHeight/2
	if !s.HandleMouse(20, computerY) {
		t.Fatal("Computer click returned false")
	}
	if s.SidebarSelected != 4 {
		t.Errorf("SidebarSelected = %d, want 4 (Computer)", s.SidebarSelected)
	}
}

// A click on the section-label band + below the last row are no-ops. Rendering
// after navigating away from "/" leaves Home unselected (star, unselected).
func TestSidebarSectionLabelAndUnselectedStar(t *testing.T) {
	s := New(surfW, surfH)
	labelY := HeaderBarHeight + SidebarTopPadding + SidebarSectionHeaderHeight/2
	if s.HandleMouse(20, labelY) {
		t.Error("section-label click returned true")
	}
	// Descend so Home (index 0) is no longer the selected sidebar row.
	_ = s.HandleKey("Enter") // -> /Documents, SidebarSelected = 1
	Render(s, newSurface(surfW, surfH))
	if s.SidebarSelected != 1 {
		t.Errorf("SidebarSelected = %d, want 1", s.SidebarSelected)
	}
}

// --- context menu ---------------------------------------------------------

// menuItemPoint returns the surface point at the centre of context-menu row i.
func menuItemPoint(s *State, i int) (int, int) {
	mb := s.ctxMenu.MenuBounds()
	return mb.X + 5, mb.Y + 2 + i*toolkit.MenuRowH + toolkit.MenuRowH/2
}

func TestRightClickRowMenu(t *testing.T) {
	s := New(surfW, surfH)
	if !s.HandleMouseButton(SidebarWidth+50, rowCenterY(0), 2, 1) {
		t.Fatal("right-click on row returned false")
	}
	if !s.ctxMenu.Open {
		t.Fatal("context menu not open")
	}
	if s.ctxTarget != "/Documents" {
		t.Errorf("ctxTarget = %q, want /Documents", s.ctxTarget)
	}
	if len(s.ctxMenu.Menu.Items) != 3 {
		t.Errorf("menu items = %d, want 3", len(s.ctxMenu.Menu.Items))
	}
	// Rendering with the menu open exercises ctxMenu.Draw.
	Render(s, newSurface(surfW, surfH))
	// Open (item 0) descends into the folder.
	mx, my := menuItemPoint(s, 0)
	if !s.HandleMouseButton(mx, my, 0, 1) {
		t.Fatal("menu Open click returned false")
	}
	if s.ctxMenu.Open {
		t.Error("menu still open after activation")
	}
	if s.Browser.CurrentPath != "/Documents" {
		t.Errorf("Open didn't descend: path = %q", s.Browser.CurrentPath)
	}
}

func TestRightClickEmptyAreaCreateMenu(t *testing.T) {
	s := New(surfW, surfH)
	if !s.HandleMouseButton(SidebarWidth+50, listTop()+8*RowHeight, 2, 1) {
		t.Fatal("right-click empty area returned false")
	}
	items := s.ctxMenu.Menu.Items
	if len(items) != 2 || items[0].Label != "New Folder" || items[1].Label != "New File" {
		t.Fatalf("create menu items = %+v", items)
	}
	// Click New File (item 1).
	mx, my := menuItemPoint(s, 1)
	_ = s.HandleMouseButton(mx, my, 0, 1)
	if _, err := s.VFS.Stat("/untitled.txt"); err != nil {
		t.Errorf("New File did not create /untitled.txt: %v", err)
	}
}

// Exercise the Rename (entry) + New Folder (create) menu items through the
// menu-click path so every built MenuItem Action closure fires.
func TestMenuRenameAndNewFolderViaMenu(t *testing.T) {
	s := New(surfW, surfH)
	// Rename (entry menu item 1) is a stub: menu closes, tree unchanged.
	_ = s.HandleMouseButton(SidebarWidth+50, rowCenterY(3), 2, 1) // about.txt
	mx, my := menuItemPoint(s, 1)
	_ = s.HandleMouseButton(mx, my, 0, 1)
	if s.ctxMenu.Open {
		t.Error("menu still open after Rename")
	}
	if _, err := s.VFS.Stat("/about.txt"); err != nil {
		t.Error("Rename clobbered the file")
	}
	// New Folder (create menu item 0).
	_ = s.HandleMouseButton(SidebarWidth+50, listTop()+8*RowHeight, 2, 1)
	mx, my = menuItemPoint(s, 0)
	_ = s.HandleMouseButton(mx, my, 0, 1)
	if !s.VFS.IsDir("/untitled") {
		t.Error("New Folder via menu did not create /untitled")
	}
}

func TestRightClickOutsideListNoMenu(t *testing.T) {
	s := New(surfW, surfH)
	// Right-click in the header band -> no menu (returns false).
	if s.HandleMouseButton(300, 20, 2, 1) {
		t.Error("right-click in header opened a menu")
	}
	if s.ctxMenu.Open {
		t.Error("menu open after header right-click")
	}
}

func TestMenuDeleteAndDismiss(t *testing.T) {
	s := New(surfW, surfH)
	if err := s.VFS.Write("/victim.txt", []byte("bye")); err != nil {
		t.Fatal(err)
	}
	s.Browser.Refresh(s.VFS)
	s.refreshView()
	// victim.txt is the last root entry now; right-click it.
	victimRow := len(s.Browser.Entries) - 1
	_ = s.HandleMouseButton(SidebarWidth+50, rowCenterY(victimRow), 2, 1)
	// Delete is item 2.
	mx, my := menuItemPoint(s, 2)
	if !s.HandleMouseButton(mx, my, 0, 1) {
		t.Fatal("Delete click returned false")
	}
	if _, err := s.VFS.Stat("/victim.txt"); err == nil {
		t.Error("/victim.txt still exists after Delete")
	}
	// Outside-click dismissal.
	_ = s.HandleMouseButton(SidebarWidth+50, rowCenterY(0), 2, 1)
	if !s.ctxMenu.Open {
		t.Fatal("menu should be open")
	}
	if !s.HandleMouseButton(5, 5, 0, 1) {
		t.Error("outside click returned false")
	}
	if s.ctxMenu.Open {
		t.Error("menu not dismissed by outside click")
	}
}

// Menu actions driven directly: Open-text preview, Rename stub, New Folder,
// New File disambiguation, delete-empty no-op, open non-text non-dir no-op.
func TestApplyMenuActions(t *testing.T) {
	s := New(surfW, surfH)
	_ = s.VFS.Write("/note.txt", []byte("hello\nworld\n"))
	s.Browser.Refresh(s.VFS)

	s.applyMenuAction("open", "/note.txt")
	if s.Preview == nil || len(s.Preview.Lines) != 2 || s.Preview.Lines[0] != "hello" {
		t.Errorf("open text preview = %+v", s.Preview)
	}
	s.Preview = nil

	// Open on a non-text, non-dir target is a silent no-op.
	_ = s.VFS.Write("/pic.png", []byte{1, 2, 3})
	s.applyMenuAction("open", "/pic.png")
	if s.Preview != nil {
		t.Error("open on .png set preview")
	}

	s.applyMenuAction("rename", "/note.txt") // stub, no change
	if _, err := s.VFS.Stat("/note.txt"); err != nil {
		t.Error("rename clobbered the file")
	}

	s.applyMenuAction("delete", "") // no-op
	s.applyMenuAction("newfolder", "")
	if !s.VFS.IsDir("/untitled") {
		t.Error("newfolder did not create /untitled")
	}
	_ = s.VFS.Write("/untitled.txt", nil)
	s.applyMenuAction("newfile", "")
	if _, err := s.VFS.Stat("/untitled-1.txt"); err != nil {
		t.Errorf("newfile disambiguation failed: %v", err)
	}
}

func TestCreateSiblingExhausts(t *testing.T) {
	s := New(surfW, surfH)
	_ = s.VFS.Write("/untitled.txt", nil)
	for i := 1; i < 1000; i++ {
		_ = s.VFS.Write("/untitled-"+itoa(i)+".txt", nil)
	}
	s.createSibling(false)
	if _, err := s.VFS.Stat("/untitled-1000.txt"); err == nil {
		t.Error("createSibling overshot the retry cap")
	}
}

// --- preview overlay ------------------------------------------------------

func TestDoubleClickPreview(t *testing.T) {
	s := New(surfW, surfH)
	// Row 3 = about.txt.
	if !s.HandleMouseButton(SidebarWidth+50, rowCenterY(3), 0, 2) {
		t.Fatal("double-click returned false")
	}
	if s.Preview == nil {
		t.Fatal("preview not opened")
	}
	// Rendering with the preview open exercises previewDialog.Draw.
	Render(s, newSurface(surfW, surfH))
	// The next click consumes the preview + drives the row it lands on.
	if s.HandleMouseButton(SidebarWidth+50, rowCenterY(3), 0, 1); s.Preview != nil {
		t.Error("preview not consumed by next click")
	}
}

func TestDoubleClickFolderDescends(t *testing.T) {
	s := New(surfW, surfH)
	if !s.HandleMouseButton(SidebarWidth+50, rowCenterY(0), 0, 2) {
		t.Fatal("double-click on folder returned false")
	}
	if s.Browser.CurrentPath != "/Documents" {
		t.Errorf("path = %q, want /Documents", s.Browser.CurrentPath)
	}
}

func TestDoubleClickEmptyAreaFallsThrough(t *testing.T) {
	s := New(surfW, surfH)
	// Double-click below the last row: previewAt misses, normal routing no-op.
	if s.HandleMouseButton(SidebarWidth+50, listTop()+9*RowHeight, 0, 2) {
		t.Error("double-click empty area returned true")
	}
}

func TestOpenPreviewMissingAndEmptyAndCapped(t *testing.T) {
	s := New(surfW, surfH)
	s.openPreview("/does-not-exist")
	if s.Preview != nil {
		t.Error("preview set on missing file")
	}
	_ = s.VFS.Write("/empty", nil)
	s.openPreview("/empty")
	if s.Preview == nil || len(s.Preview.Lines) != 1 || s.Preview.Lines[0] != "(empty file)" {
		t.Errorf("empty-file preview = %+v", s.Preview)
	}
	// A long file caps at PreviewMaxLines + exercises previewText's line-break.
	body := ""
	for i := 0; i < 25; i++ {
		body += "line " + itoa(i) + "\n"
	}
	_ = s.VFS.Write("/long.txt", []byte(body))
	s.openPreview("/long.txt")
	if len(s.Preview.Lines) != PreviewMaxLines {
		t.Errorf("capped lines = %d, want %d", len(s.Preview.Lines), PreviewMaxLines)
	}
	Render(s, newSurface(surfW, surfH))
}

// The preview panel clamps to the sidebar edge on a narrow surface.
func TestPreviewNarrowSurfaceClamp(t *testing.T) {
	w, h := 500, 400
	vfs := newEmptyVFS()
	_ = vfs.Write("/narrow.txt", []byte("hi\n"))
	s := NewWithVFS(w, h, vfs)
	s.openPreview("/narrow.txt")
	if got := s.previewDialog.Bounds().X; got != SidebarWidth+8 {
		t.Errorf("clamped preview X = %d, want %d", got, SidebarWidth+8)
	}
	Render(s, newSurface(w, h))
}

// --- misc coverage --------------------------------------------------------

func TestSyncSidebarUnknownPath(t *testing.T) {
	s := New(surfW, surfH)
	s.Browser.CurrentPath = "/nope"
	s.syncSidebar()
	if s.SidebarSelected != -1 {
		t.Errorf("SidebarSelected for /nope = %d, want -1", s.SidebarSelected)
	}
}

// HitTest of the non-interactive leaves returns false.
func TestNonInteractiveHitTests(t *testing.T) {
	if (&sectionLabel{}).HitTest(1, 1) {
		t.Error("sectionLabel HitTest should be false")
	}
	if (&columnHeader{}).HitTest(1, 1) {
		t.Error("columnHeader HitTest should be false")
	}
}

// Every file-type + sidebar icon glyph paints in both the plain + selected
// variant (both colour arms of each drawer).
func TestIconGlyphs(t *testing.T) {
	w, h := 64, 64
	for _, sel := range []bool{false, true} {
		buf := newSurface(w, h)
		p := painter.NewPixelPainter(buf, w, h)
		drawFolderIcon(p, 4, 4, sel)
		drawFileIcon(p, 30, 4, sel)
		for _, kind := range []string{"home", "computer", "trash", "folder", "unknown"} {
			drawSidebarIcon(p, 4, 40, kind, sel)
		}
		// Something must have been inked.
		any := false
		for _, b := range buf {
			if b != 0 {
				any = true
				break
			}
		}
		if !any {
			t.Errorf("icons (selected=%v) drew nothing", sel)
		}
	}
}
