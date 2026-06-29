// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import (
	"testing"

	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

func newTestState(t *testing.T) *SceneState {
	t.Helper()
	v := sharedvfs.NewInMemoryVFS()
	sharedvfs.SeedDemoTree(v)
	return NewWithVFS(900, 540, v)
}

func TestNew_UsesDemoVFS(t *testing.T) {
	s := New(900, 540)
	if s.VFS == nil {
		t.Fatal("VFS nil")
	}
	if len(s.FileTree) == 0 {
		t.Fatal("FileTree empty -- demo seed missing")
	}
}

func TestNewWithVFS_RefreshFails_NilFileTree(t *testing.T) {
	// A VFS whose List always errors (use an empty raw VFS without root via
	// our broken stub).
	s := NewWithVFS(100, 100, brokenVFS{})
	if s.FileTree != nil {
		t.Fatalf("expected nil tree on broken VFS, got %+v", s.FileTree)
	}
}

// brokenVFS errors on List so refreshTree's error branch is exercised.
type brokenVFS struct{}

func (brokenVFS) List(string) ([]sharedvfs.Entry, error) { return nil, sharedvfs.ErrNotFound }
func (brokenVFS) Stat(string) (sharedvfs.Entry, error)   { return sharedvfs.Entry{}, sharedvfs.ErrNotFound }
func (brokenVFS) IsDir(string) bool                      { return false }
func (brokenVFS) Read(string) ([]byte, error)            { return nil, sharedvfs.ErrNotFound }
func (brokenVFS) Write(string, []byte) error             { return sharedvfs.ErrNotFound }
func (brokenVFS) Mkdir(string) error                     { return sharedvfs.ErrNotFound }
func (brokenVFS) Remove(string) error                    { return sharedvfs.ErrNotFound }

func TestNewDemoVFS_Seeded(t *testing.T) {
	v := NewDemoVFS()
	if !v.IsDir("/Documents") {
		t.Fatal("demo VFS missing /Documents")
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
	if s.Buffer.String() == "" {
		t.Fatal("buffer not loaded")
	}
	// Missing file: no change.
	if s.OpenFile("/nope.txt") {
		t.Fatal("OpenFile missing should return false")
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
	s.Buffer.SetCursor(0, 0)
	s.Buffer.Insert("X")
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
	if string(data)[0] != 'X' {
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
	s.Buffer = NewTextBuffer("ab\ncd")

	// ArrowRight.
	s.Buffer.SetCursor(0, 0)
	if !s.HandleKey("ArrowRight") {
		t.Fatal("ArrowRight should report change")
	}
	if s.Buffer.Cursor.Col != 1 {
		t.Fatalf("col: %d", s.Buffer.Cursor.Col)
	}
	// ArrowLeft.
	if !s.HandleKey("ArrowLeft") {
		t.Fatal("ArrowLeft should report change")
	}
	// ArrowDown.
	if !s.HandleKey("ArrowDown") {
		t.Fatal("ArrowDown should report change")
	}
	if s.Buffer.Cursor.Row != 1 {
		t.Fatalf("row: %d", s.Buffer.Cursor.Row)
	}
	// ArrowUp.
	if !s.HandleKey("ArrowUp") {
		t.Fatal("ArrowUp should report change")
	}
}

func TestHandleKey_Backspace_DeleteAtOrigin(t *testing.T) {
	s := newTestState(t)
	s.Buffer = NewTextBuffer("")
	if s.HandleKey("Backspace") {
		t.Fatal("Backspace at (0,0) should return false")
	}
}

func TestHandleKey_Backspace_Effective(t *testing.T) {
	s := newTestState(t)
	s.Buffer = NewTextBuffer("ab")
	s.Buffer.SetCursor(0, 2)
	if !s.HandleKey("Backspace") {
		t.Fatal("Backspace should return true")
	}
	if s.Buffer.String() != "a" {
		t.Fatalf("after BS: %q", s.Buffer.String())
	}
}

func TestHandleKey_EnterSplits(t *testing.T) {
	s := newTestState(t)
	s.Buffer = NewTextBuffer("abc")
	s.Buffer.SetCursor(0, 2)
	if !s.HandleKey("Enter") {
		t.Fatal("Enter should always return true")
	}
	if len(s.Buffer.Lines) != 2 {
		t.Fatalf("split: %q", s.Buffer.Lines)
	}
}

func TestHandleKey_TabInserts4Spaces(t *testing.T) {
	s := newTestState(t)
	s.Buffer = NewTextBuffer("")
	if !s.HandleKey("Tab") {
		t.Fatal("Tab should return true")
	}
	if s.Buffer.Lines[0] != "    " {
		t.Fatalf("Tab body: %q", s.Buffer.Lines[0])
	}
}

func TestHandleKey_PrintableInsert(t *testing.T) {
	s := newTestState(t)
	s.Buffer = NewTextBuffer("")
	if !s.HandleKey("a") {
		t.Fatal("printable should return true")
	}
	if s.Buffer.Lines[0] != "a" {
		t.Fatalf("body: %q", s.Buffer.Lines[0])
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
	// 2-byte non-special name like "PageDown".
	if s.HandleKey("PageDown") {
		t.Fatal("PageDown should return false")
	}
	// Non-printable single byte.
	if s.HandleKey(string([]byte{0x01})) {
		t.Fatal("non-printable should return false")
	}
}

func TestHandleKey_CmdSAndCtrlS(t *testing.T) {
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
	// Reset.
	s.Flash = FlashNone
	if !s.HandleKey("Ctrl+S") {
		t.Fatal("Ctrl+S should save")
	}
}

func TestHandleKey_CmdS_NoFileReturnsFalse(t *testing.T) {
	s := newTestState(t)
	if s.HandleKey("Cmd+S") {
		t.Fatal("Cmd+S with no file should return false")
	}
}

func TestHandleMouse_SidebarRowOpensFile(t *testing.T) {
	s := newTestState(t)
	// Find the index of "about.txt" in the file tree.
	var idx int = -1
	for i, e := range s.FileTree {
		if e.Name == "about.txt" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("about.txt missing from tree")
	}
	y := TabStripHeight + idx*SidebarRowHeight + 2
	if !s.HandleMouse(10, y) {
		t.Fatal("sidebar click should open file")
	}
	if s.CurrentPath != "/about.txt" {
		t.Fatalf("CurrentPath: %q", s.CurrentPath)
	}
}

func TestHandleMouse_SidebarRowOutOfBounds(t *testing.T) {
	s := newTestState(t)
	// Click far below last row.
	y := TabStripHeight + 100*SidebarRowHeight
	if s.HandleMouse(10, y) {
		t.Fatal("out-of-range sidebar click should return false")
	}
}

func TestHandleMouse_SidebarRowOnDirectory(t *testing.T) {
	s := newTestState(t)
	// Find the index of a directory entry in the tree (e.g. "Documents").
	var idx int = -1
	for i, e := range s.FileTree {
		if e.IsDir {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no directory in tree")
	}
	y := TabStripHeight + idx*SidebarRowHeight + 2
	if s.HandleMouse(10, y) {
		t.Fatal("clicking a directory row should return false")
	}
}

func TestHandleMouse_EditorCursorJump(t *testing.T) {
	s := newTestState(t)
	if !s.OpenFile("/about.txt") {
		t.Fatal("open")
	}
	// Click at editor row 0, col ~2.
	x := SidebarWidth + GutterWidth + 2*(FontW*EditorFontScale) + 1
	y := TabStripHeight + 4
	if !s.HandleMouse(x, y) {
		t.Fatal("editor click should report change")
	}
	if s.Buffer.Cursor.Row != 0 || s.Buffer.Cursor.Col != 2 {
		t.Fatalf("cursor: (%d,%d)", s.Buffer.Cursor.Row, s.Buffer.Cursor.Col)
	}
}

func TestHandleMouse_StatusBarLiveServerOpensPopup(t *testing.T) {
	s := newTestState(t)
	x := s.W - 10
	y := s.H - 5
	if !s.HandleMouse(x, y) {
		t.Fatal("status-bar click should open popup")
	}
	if !s.LiveServerPopupOpen {
		t.Fatal("popup not open")
	}
}

func TestHandleMouse_StatusBarLeftRegion(t *testing.T) {
	s := newTestState(t)
	x := 10
	y := s.H - 5
	if s.HandleMouse(x, y) {
		t.Fatal("status-bar left region should return false")
	}
}

func TestHandleMouse_PopupConnectFlashes(t *testing.T) {
	s := newTestState(t)
	s.LiveServerPopupOpen = true
	s.LiveServerURL = "wss://example"
	if !s.HandleMouse(PopupConnectX+10, PopupConnectY+10) {
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
	// Click far from popup region.
	if !s.HandleMouse(5, 5) {
		t.Fatal("outside-popup click should report change")
	}
	if s.LiveServerPopupOpen {
		t.Fatal("popup still open")
	}
}

func TestHandleMouse_PopupInsideButNotConnect_NoOp(t *testing.T) {
	s := newTestState(t)
	s.LiveServerPopupOpen = true
	// Inside popup, but at top-left corner (not on Connect button).
	if s.HandleMouse(PopupX+2, PopupY+2) {
		t.Fatal("popup body click should be a no-op")
	}
	if !s.LiveServerPopupOpen {
		t.Fatal("popup should still be open")
	}
}

func TestHandleMouse_OutsideKnownRegions(t *testing.T) {
	s := newTestState(t)
	// Click in the tab strip area above the editor: y < TabStripHeight,
	// x past the sidebar -- falls through every branch.
	if s.HandleMouse(SidebarWidth+50, 4) {
		t.Fatal("tab strip click should return false")
	}
}

func TestInRect(t *testing.T) {
	if !inRect(5, 5, 0, 0, 10, 10) {
		t.Fatal("inside")
	}
	if inRect(-1, 5, 0, 0, 10, 10) {
		t.Fatal("outside-left")
	}
	if inRect(5, -1, 0, 0, 10, 10) {
		t.Fatal("outside-top")
	}
	if inRect(10, 5, 0, 0, 10, 10) {
		t.Fatal("outside-right")
	}
	if inRect(5, 10, 0, 0, 10, 10) {
		t.Fatal("outside-bottom")
	}
}
