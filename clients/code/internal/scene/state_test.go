// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import (
	"strings"
	"testing"

	"github.com/go-widgets/toolkit"
	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

func newTestState(t *testing.T) *SceneState {
	t.Helper()
	v := sharedvfs.NewInMemoryVFS()
	sharedvfs.SeedDemoTree(v)
	return NewWithVFS(900, 460, v)
}

// brokenVFS errors on every op so refreshTree + SaveCurrent error branches
// are exercised.
type brokenVFS struct{}

func (brokenVFS) List(string) ([]sharedvfs.Entry, error) { return nil, sharedvfs.ErrNotFound }
func (brokenVFS) Stat(string) (sharedvfs.Entry, error) {
	return sharedvfs.Entry{}, sharedvfs.ErrNotFound
}
func (brokenVFS) IsDir(string) bool           { return false }
func (brokenVFS) Read(string) ([]byte, error) { return nil, sharedvfs.ErrNotFound }
func (brokenVFS) Write(string, []byte) error  { return sharedvfs.ErrNotFound }
func (brokenVFS) Mkdir(string) error          { return sharedvfs.ErrNotFound }
func (brokenVFS) Remove(string) error         { return sharedvfs.ErrNotFound }

func TestNew_UsesDemoVFS(t *testing.T) {
	s := New(900, 460)
	if s.VFS == nil {
		t.Fatal("VFS nil")
	}
	if len(s.FileTree) == 0 {
		t.Fatal("FileTree empty -- demo seed missing")
	}
	if s.Editor == nil {
		t.Fatal("Editor nil")
	}
}

func TestNewWithVFS_RefreshFails_NilTree(t *testing.T) {
	s := NewWithVFS(100, 100, brokenVFS{})
	if s.FileTree != nil {
		t.Fatalf("expected nil tree on broken VFS, got %+v", s.FileTree)
	}
	if s.sidebar.Items != nil {
		t.Fatalf("expected nil sidebar items, got %+v", s.sidebar.Items)
	}
}

func TestNewDemoVFS_Seeded(t *testing.T) {
	v := NewDemoVFS()
	if !v.IsDir("/Documents") {
		t.Fatal("demo VFS missing /Documents")
	}
}

func TestRefreshTree_MirrorsPrefixes(t *testing.T) {
	s := newTestState(t)
	if len(s.sidebar.Items) != len(s.FileTree) {
		t.Fatalf("items=%d tree=%d", len(s.sidebar.Items), len(s.FileTree))
	}
	sawDir, sawFile := false, false
	for i, e := range s.FileTree {
		it := s.sidebar.Items[i]
		if e.IsDir {
			sawDir = true
			if it[:2] != "> " {
				t.Errorf("dir row %q missing prefix", it)
			}
		} else {
			sawFile = true
			if it[:2] != "  " {
				t.Errorf("file row %q missing prefix", it)
			}
		}
	}
	if !sawDir || !sawFile {
		t.Fatalf("demo tree needs both a dir and a file (dir=%v file=%v)", sawDir, sawFile)
	}
}

func TestOpenFile_SuccessAndMissing(t *testing.T) {
	s := newTestState(t)
	if !s.OpenFile("/about.txt") {
		t.Fatal("OpenFile failed")
	}
	if s.CurrentPath != "/about.txt" {
		t.Fatalf("CurrentPath: %q", s.CurrentPath)
	}
	if s.Editor.Text().Get() == "" {
		t.Fatal("editor not loaded")
	}
	if s.tabLabel() != "about.txt" {
		t.Fatalf("tabLabel = %q", s.tabLabel())
	}
	if s.OpenFile("/nope.txt") {
		t.Fatal("OpenFile missing should return false")
	}
}

func TestTabLabel_Untitled(t *testing.T) {
	s := newTestState(t)
	if s.tabLabel() != "untitled" {
		t.Fatalf("tabLabel with no file = %q", s.tabLabel())
	}
}

func TestOpenFile_ClearsFlashAndPopup(t *testing.T) {
	s := newTestState(t)
	s.Flash = FlashSaveOK
	s.LiveServerPopupOpen = true
	if !s.OpenFile("/about.txt") {
		t.Fatal("open")
	}
	if s.Flash != FlashNone || s.LiveServerPopupOpen {
		t.Fatalf("flash/popup not cleared: flash=%d open=%v", s.Flash, s.LiveServerPopupOpen)
	}
}

func TestSaveCurrent_NoFile(t *testing.T) {
	s := newTestState(t)
	if s.SaveCurrent() {
		t.Fatal("SaveCurrent with no file should return false")
	}
}

func TestSaveCurrent_Success(t *testing.T) {
	s := newTestState(t)
	if !s.OpenFile("/about.txt") {
		t.Fatal("open")
	}
	s.Editor.CursorLine().Set(0)
	s.Editor.CursorCol().Set(0)
	s.Editor.OnEvent(toolkit.Event{Kind: toolkit.EventChar, Code: "X"})
	if !s.SaveCurrent() {
		t.Fatal("save")
	}
	if s.Flash != FlashSaveOK {
		t.Fatalf("flash: %d", s.Flash)
	}
	data, err := s.VFS.Read("/about.txt")
	if err != nil {
		t.Fatalf("post-save read: %v", err)
	}
	if data[0] != 'X' {
		t.Fatalf("post-save body: %q", data)
	}
}

func TestSaveCurrent_WriteError(t *testing.T) {
	s := NewWithVFS(100, 100, brokenVFS{})
	s.CurrentPath = "/whatever.txt"
	if s.SaveCurrent() {
		t.Fatal("SaveCurrent on broken VFS should return false")
	}
	if s.Flash == FlashSaveOK {
		t.Fatal("flash should not be SaveOK on failure")
	}
}

func TestHandleKey_CursorMovement(t *testing.T) {
	s := newTestState(t)
	s.Editor.SetText("ab\ncd")

	s.Editor.CursorLine().Set(0)
	s.Editor.CursorCol().Set(0)
	if !s.HandleKey("ArrowRight") {
		t.Fatal("ArrowRight should report change")
	}
	if s.Editor.CursorCol().Get() != 1 {
		t.Fatalf("col: %d", s.Editor.CursorCol().Get())
	}
	if !s.HandleKey("ArrowLeft") {
		t.Fatal("ArrowLeft should report change")
	}
	if !s.HandleKey("ArrowDown") {
		t.Fatal("ArrowDown should report change")
	}
	if s.Editor.CursorLine().Get() != 1 {
		t.Fatalf("row: %d", s.Editor.CursorLine().Get())
	}
	if !s.HandleKey("ArrowUp") {
		t.Fatal("ArrowUp should report change")
	}
	// Home / End move within the line.
	s.Editor.CursorCol().Set(1)
	if !s.HandleKey("Home") {
		t.Fatal("Home should report change")
	}
	if s.Editor.CursorCol().Get() != 0 {
		t.Fatalf("Home col: %d", s.Editor.CursorCol().Get())
	}
	if !s.HandleKey("End") {
		t.Fatal("End should report change")
	}
}

func TestHandleKey_NonMovingArrowReturnsFalse(t *testing.T) {
	s := newTestState(t)
	s.Editor.SetText("ab")
	s.Editor.CursorLine().Set(0)
	s.Editor.CursorCol().Set(0)
	// ArrowLeft at buffer start does not move -> no visible change.
	if s.HandleKey("ArrowLeft") {
		t.Fatal("ArrowLeft at (0,0) should return false")
	}
}

func TestHandleKey_BackspaceAtOrigin(t *testing.T) {
	s := newTestState(t)
	s.Editor.SetText("")
	if s.HandleKey("Backspace") {
		t.Fatal("Backspace at (0,0) should return false")
	}
}

func TestHandleKey_BackspaceEffective(t *testing.T) {
	s := newTestState(t)
	s.Editor.SetText("ab")
	s.Editor.CursorLine().Set(0)
	s.Editor.CursorCol().Set(2)
	if !s.HandleKey("Backspace") {
		t.Fatal("Backspace should return true")
	}
	if s.Editor.Text().Get() != "a" {
		t.Fatalf("after BS: %q", s.Editor.Text().Get())
	}
}

func TestHandleKey_EnterSplits(t *testing.T) {
	s := newTestState(t)
	s.Editor.SetText("abc")
	s.Editor.CursorLine().Set(0)
	s.Editor.CursorCol().Set(2)
	if !s.HandleKey("Enter") {
		t.Fatal("Enter should return true")
	}
	if len(strings.Split(s.Editor.Text().Get(), "\n")) != 2 {
		t.Fatalf("split: %q", strings.Split(s.Editor.Text().Get(), "\n"))
	}
}

func TestHandleKey_TabInserts4Spaces(t *testing.T) {
	s := newTestState(t)
	s.Editor.SetText("")
	if !s.HandleKey("Tab") {
		t.Fatal("Tab should return true")
	}
	if strings.Split(s.Editor.Text().Get(), "\n")[0] != "    " {
		t.Fatalf("Tab body: %q", strings.Split(s.Editor.Text().Get(), "\n")[0])
	}
}

func TestHandleKey_PrintableInsert(t *testing.T) {
	s := newTestState(t)
	s.Editor.SetText("")
	if !s.HandleKey("a") {
		t.Fatal("printable should return true")
	}
	if strings.Split(s.Editor.Text().Get(), "\n")[0] != "a" {
		t.Fatalf("body: %q", strings.Split(s.Editor.Text().Get(), "\n")[0])
	}
}

func TestHandleKey_UnknownIgnored(t *testing.T) {
	s := newTestState(t)
	if s.HandleKey("F1") {
		t.Fatal("F1 should return false")
	}
	if s.HandleKey("") {
		t.Fatal("empty key should return false")
	}
	if s.HandleKey("PageDown") {
		t.Fatal("PageDown should return false")
	}
	if s.HandleKey(string([]byte{0x01})) {
		t.Fatal("non-printable should return false")
	}
}

func TestHandleKey_SaveKeys(t *testing.T) {
	s := newTestState(t)
	if !s.OpenFile("/about.txt") {
		t.Fatal("open")
	}
	if !s.HandleKey("Cmd+S") {
		t.Fatal("Cmd+S should save")
	}
	if s.Flash != FlashSaveOK {
		t.Fatalf("flash: %d", s.Flash)
	}
	s.Flash = FlashNone
	if !s.HandleKey("Ctrl+S") {
		t.Fatal("Ctrl+S should save")
	}
}

func TestHandleKey_SaveNoFileFalse(t *testing.T) {
	s := newTestState(t)
	if s.HandleKey("Cmd+S") {
		t.Fatal("Cmd+S with no file should return false")
	}
}

func TestHandleMouse_SidebarRowOpensFile(t *testing.T) {
	s := newTestState(t)
	idx := -1
	for i, e := range s.FileTree {
		if e.Name == "about.txt" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("about.txt missing from tree")
	}
	sd := s.sidebar.Bounds()
	y := sd.Y + idx*SidebarRowHeight + 2
	if !s.HandleMouse(10, y) {
		t.Fatal("sidebar click should open file")
	}
	if s.CurrentPath != "/about.txt" {
		t.Fatalf("CurrentPath: %q", s.CurrentPath)
	}
}

func TestHandleMouse_SidebarRowOutOfBounds(t *testing.T) {
	s := newTestState(t)
	sd := s.sidebar.Bounds()
	y := sd.Y + 100*SidebarRowHeight
	// A y past the last row but still inside the sidebar bounds routes into
	// the ListBox, whose onClick no-ops (idx past len) -> false.
	if y >= sd.Y+sd.H {
		y = sd.Y + sd.H - 1
	}
	// Ensure it maps past the file rows.
	if s.HandleMouse(10, sd.Y+len(s.FileTree)*SidebarRowHeight+2) {
		t.Fatal("out-of-range sidebar click should return false")
	}
	_ = y
}

func TestHandleMouse_SidebarRowOnDirectory(t *testing.T) {
	s := newTestState(t)
	idx := -1
	for i, e := range s.FileTree {
		if e.IsDir {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no directory in tree")
	}
	sd := s.sidebar.Bounds()
	y := sd.Y + idx*SidebarRowHeight + 2
	if s.HandleMouse(10, y) {
		t.Fatal("clicking a directory row should return false")
	}
}

func TestHandleMouse_EditorCursorJump(t *testing.T) {
	s := newTestState(t)
	if !s.OpenFile("/about.txt") {
		t.Fatal("open")
	}
	ed := s.Editor.Bounds()
	adv := toolkit.TextWidth(" ")
	gutterW := toolkit.TextWidth("1") + 8 // single-digit line count
	textX := ed.X + 4 + gutterW
	x := textX + 2*adv + 1
	y := ed.Y + 4
	if !s.HandleMouse(x, y) {
		t.Fatal("editor click should report change")
	}
	if s.Editor.CursorLine().Get() != 0 || s.Editor.CursorCol().Get() != 2 {
		t.Fatalf("cursor: (%d,%d)", s.Editor.CursorLine().Get(), s.Editor.CursorCol().Get())
	}
}

func TestHandleMouse_StatusBarOpensPopup(t *testing.T) {
	s := newTestState(t)
	if !s.HandleMouse(s.W-10, s.H-5) {
		t.Fatal("status-bar right region should open popup")
	}
	if !s.LiveServerPopupOpen {
		t.Fatal("popup not open")
	}
}

func TestHandleMouse_StatusBarLeftRegion(t *testing.T) {
	s := newTestState(t)
	if s.HandleMouse(10, s.H-5) {
		t.Fatal("status-bar left region should return false")
	}
}

func TestHandleMouse_PopupConnectFlashes(t *testing.T) {
	s := newTestState(t)
	s.LiveServerPopupOpen = true
	s.LiveServerURL = "wss://example"
	s.layoutDialog()
	bb := s.connectBtn.Bounds()
	if !s.HandleMouse(bb.X+bb.W/2, bb.Y+bb.H/2) {
		t.Fatal("Connect should report change")
	}
	if s.LiveServerPopupOpen {
		t.Fatal("popup still open after Connect")
	}
	if s.Flash != FlashInfo {
		t.Fatalf("flash: %d", s.Flash)
	}
	if s.LiveServerURL != "" {
		t.Fatalf("URL not cleared: %q", s.LiveServerURL)
	}
}

func TestHandleMouse_PopupOutsideDismisses(t *testing.T) {
	s := newTestState(t)
	s.LiveServerPopupOpen = true
	if !s.HandleMouse(2, 2) {
		t.Fatal("outside-popup click should report change")
	}
	if s.LiveServerPopupOpen {
		t.Fatal("popup still open")
	}
}

func TestHandleMouse_PopupInsideNotConnect_NoOp(t *testing.T) {
	s := newTestState(t)
	s.LiveServerPopupOpen = true
	s.layoutDialog()
	db := s.dialog.Bounds()
	// Click inside the dialog title bar (not on a button).
	if s.HandleMouse(db.X+4, db.Y+4) {
		t.Fatal("popup body click should be a no-op")
	}
	if !s.LiveServerPopupOpen {
		t.Fatal("popup should still be open")
	}
}

func TestHandleMouse_OutsideKnownRegions(t *testing.T) {
	s := newTestState(t)
	// A click in the tab strip (above the editor, right of the sidebar) falls
	// through every hit-zone.
	if s.HandleMouse(SidebarWidth+50, 4) {
		t.Fatal("tab strip click should return false")
	}
}

func TestPlaceCursorAt_Clamps(t *testing.T) {
	s := newTestState(t)
	s.Editor.SetText("ab\ncde")
	ed := s.Editor.Bounds()
	// Row above the first line + col left of the text -> clamps to (0,0).
	s.placeCursorAt(ed.X, ed.Y-100)
	if s.Editor.CursorLine().Get() != 0 || s.Editor.CursorCol().Get() != 0 {
		t.Fatalf("clamp low: (%d,%d)", s.Editor.CursorLine().Get(), s.Editor.CursorCol().Get())
	}
	// Row + col far past the buffer -> clamps to the last line / its end.
	s.placeCursorAt(ed.X+10000, ed.Y+10000)
	if s.Editor.CursorLine().Get() != len(strings.Split(s.Editor.Text().Get(), "\n"))-1 {
		t.Fatalf("clamp row high: %d", s.Editor.CursorLine().Get())
	}
	if s.Editor.CursorCol().Get() != len([]rune(strings.Split(s.Editor.Text().Get(), "\n")[s.Editor.CursorLine().Get()])) {
		t.Fatalf("clamp col high: %d", s.Editor.CursorCol().Get())
	}
}

func TestEditorSig_ChangesWithEdit(t *testing.T) {
	s := newTestState(t)
	s.Editor.SetText("a")
	before := s.editorSig()
	s.Editor.OnEvent(toolkit.Event{Kind: toolkit.EventChar, Code: "b"})
	if s.editorSig() == before {
		t.Fatal("editorSig should change after an edit")
	}
}

func TestActivateSidebar_OutOfRange(t *testing.T) {
	s := newTestState(t)
	// Direct call with an index past the tree (ListBox guards this before
	// OnActivate in practice, but the defensive branch is still exercised).
	s.dirty = true
	s.activateSidebar(-1)
	if s.dirty {
		t.Fatal("out-of-range activate should leave dirty false")
	}
	s.dirty = true
	s.activateSidebar(len(s.FileTree) + 5)
	if s.dirty {
		t.Fatal("out-of-range activate should leave dirty false")
	}
}

// TestBuildMonoFace_FallbackOnBadBlob drives buildMonoFace's parse-failure
// branch (never reached with the bundled blob) by handing it garbage bytes:
// it must still return a usable (bitmap) font, never nil, so the editor
// degrades to legible bitmap text rather than to no text.
func TestBuildMonoFace_FallbackOnBadBlob(t *testing.T) {
	if f := buildMonoFace([]byte("not a font"), editorFontPx); f == nil {
		t.Fatal("buildMonoFace returned nil on a bad blob; want bitmap fallback")
	}
	// The success path is exercised by every New()/editorFace() call, but assert
	// it explicitly too so both return statements are self-documented.
	if f := editorFace(); f == nil || f.Advance() <= 0 {
		t.Fatalf("editorFace() = %v, want a usable monospace face", f)
	}
}
