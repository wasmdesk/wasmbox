// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's state.go is the VS Code-inspired editor's UI state: a
// loaded TextBuffer + the current file's path + a flat top-level file tree
// (rooted at "/") + a transient status flash + a Live Server popup flag.
// Pure Go, no syscall/js, so the whole package builds + tests natively on
// every architecture this repo targets.

package scene

import "github.com/wasmdesk/wasmbox/clients/sharedvfs"

// FlashKind classifies the kind of status-bar flash to paint. Default is
// no flash; SaveOK is the green "saved" pulse the Cmd+S handler triggers;
// Info is the neutral blue flash the Live Server stub uses for its
// "protocol pending" message.
type FlashKind int

const (
	// FlashNone is the default: no flash, status bar paints its idle colours.
	FlashNone FlashKind = iota
	// FlashSaveOK is the green pulse Cmd+S triggers on a successful Write.
	FlashSaveOK
	// FlashInfo is the neutral pulse the Live Server stub triggers when the
	// user clicks Connect on the popup (the protocol is not wired yet).
	FlashInfo
)

// TickPerFlash is the (notional) number of HandleTick calls a flash lasts.
// The wasm side doesn't run a real animation timer in v0 -- the flash
// is cleared when the next interaction triggers a re-render. The constant
// is exported so a future timer-driven renderer can pin the duration.
const TickPerFlash = 60

// SceneState is the top-of-package handle the wasm entry point holds. The
// renderer reads every field; mutation funnels through HandleKey /
// HandleMouse so the cursor + flash + popup stay coherent.
type SceneState struct {
	W, H int
	VFS  sharedvfs.VFS

	// Buffer is the editable text store currently shown in the editor pane.
	// Always non-nil -- the constructor seeds an empty single-line buffer.
	Buffer *TextBuffer

	// FileTree is the cached top-level (sorted, dirs-first) listing of the
	// VFS root. Refreshed when the user writes a new file so the sidebar
	// reflects the latest tree.
	FileTree []sharedvfs.Entry

	// CurrentPath is the path of the file shown in the editor pane, or ""
	// when no file is open (the tab strip then shows "untitled" and saves
	// are no-ops).
	CurrentPath string

	// Flash drives the bottom-status-bar pulse; FlashKind picks the colour.
	// The flash is consumed by the next non-typing interaction.
	Flash FlashKind

	// LiveServerPopupOpen reports whether the "Connect to Live Server"
	// popup is currently shown; the next non-popup click dismisses it.
	LiveServerPopupOpen bool

	// LiveServerURL is the buffered "wss://..." URL the user has typed
	// into the popup. v0 stores it but does not connect; the Connect
	// button triggers a FlashInfo + closes the popup.
	LiveServerURL string
}

// New constructs a SceneState for a width x height pixel surface backed by
// the demo (in-memory) VFS, with no file open. Used by tests + by the wasm
// boot path when IndexedDB is unavailable.
func New(width, height int) *SceneState {
	return NewWithVFS(width, height, NewDemoVFS())
}

// NewWithVFS is the explicit constructor: builds a SceneState around the
// caller-supplied VFS so the wasm boot path can hand in an IDB-backed
// instance for persistence. Tests reach for New() (which routes through
// here with the in-memory demo VFS).
func NewWithVFS(width, height int, vfs sharedvfs.VFS) *SceneState {
	s := &SceneState{
		W:      width,
		H:      height,
		VFS:    vfs,
		Buffer: NewTextBuffer(""),
	}
	s.refreshTree()
	return s
}

// NewDemoVFS returns a fresh in-memory VFS seeded with the canonical demo
// tree. Pulled out so tests can construct the same VFS the wasm side gets.
func NewDemoVFS() sharedvfs.VFS {
	v := sharedvfs.NewInMemoryVFS()
	sharedvfs.SeedDemoTree(v)
	return v
}

// refreshTree re-lists "/" + caches the entries in FileTree. Called at
// construction time + after a successful Write so the sidebar always
// reflects what's on disk.
func (s *SceneState) refreshTree() {
	entries, err := s.VFS.List("/")
	if err != nil {
		s.FileTree = nil
		return
	}
	s.FileTree = entries
}

// OpenFile loads the file at path into the editor pane. The buffer is
// replaced, CurrentPath is updated, the cursor jumps to (0, 0), and any
// open popup / flash is dismissed. Returns true when the load succeeded
// (a missing file leaves the state unchanged).
func (s *SceneState) OpenFile(path string) bool {
	data, err := s.VFS.Read(path)
	if err != nil {
		return false
	}
	s.Buffer = NewTextBuffer(string(data))
	s.CurrentPath = path
	s.Flash = FlashNone
	s.LiveServerPopupOpen = false
	return true
}

// SaveCurrent writes the current buffer back to CurrentPath. A no-op (returns
// false) when no file is open; on success it flashes the status bar green.
func (s *SceneState) SaveCurrent() bool {
	if s.CurrentPath == "" {
		return false
	}
	if err := s.VFS.Write(s.CurrentPath, []byte(s.Buffer.String())); err != nil {
		return false
	}
	s.Flash = FlashSaveOK
	s.refreshTree()
	return true
}

// HandleKey routes one DOM-style keydown into the editor. The shape mirrors
// the files client's HandleKey: returns true when the visible state changed
// so the caller decides whether to re-render. The compositor reports
// keydown events as {key: "ArrowDown"|"a"|"Enter"|...} with no explicit
// modifier state, so Cmd+S / Ctrl+S come through as the bare two-letter
// keys "s" with no way to disambiguate; the wasm entry point synthesises
// a "Cmd+S" key when it sees the meta modifier (see main.go).
func (s *SceneState) HandleKey(key string) bool {
	switch key {
	case "ArrowDown":
		return s.Buffer.MoveCursor(1, 0)
	case "ArrowUp":
		return s.Buffer.MoveCursor(-1, 0)
	case "ArrowRight":
		return s.Buffer.MoveCursor(0, 1)
	case "ArrowLeft":
		return s.Buffer.MoveCursor(0, -1)
	case "Backspace":
		return s.Buffer.Delete()
	case "Enter":
		s.Buffer.Split()
		return true
	case "Tab":
		s.Buffer.Insert("    ")
		return true
	case "Cmd+S", "Ctrl+S":
		return s.SaveCurrent()
	default:
		// Printable single character. The compositor reports printable keys
		// as their literal character ("a", "A", "1", " "). Anything longer
		// than 1 byte is a modifier-named key we don't handle yet (Alt,
		// PageDown, F1, ...).
		if len(key) == 1 && key[0] >= 0x20 && key[0] < 0x7F {
			s.Buffer.Insert(key)
			return true
		}
		return false
	}
}

// HandleMouse routes one mousedown into the editor. (x, y) are surface-local
// pixel coordinates. Hit-zones (top to bottom):
//
//   - Live Server popup (when open) -> click outside dismisses; click on
//     Connect closes + flashes FlashInfo
//   - Sidebar file row -> OpenFile on the row's entry
//   - Status bar "Live Server: Not connected" region -> opens the popup
//   - Editor pane -> SetCursor on the clicked (line, col)
//
// Returns true when the visible state changed so the caller re-renders.
func (s *SceneState) HandleMouse(x, y int) bool {
	if s.LiveServerPopupOpen {
		// Connect button is at the right edge of the popup.
		if inRect(x, y, PopupConnectX, PopupConnectY, PopupConnectW, PopupConnectH) {
			s.LiveServerURL = ""
			s.LiveServerPopupOpen = false
			s.Flash = FlashInfo
			return true
		}
		// Click outside the popup region dismisses without flashing.
		if !inRect(x, y, PopupX, PopupY, PopupW, PopupH) {
			s.LiveServerPopupOpen = false
			return true
		}
		// Click inside the popup but not on Connect: no-op (input focus
		// would land on the URL field; v0 has no soft keyboard plumb).
		return false
	}
	// Status bar: Live Server segment is the right-most clickable region.
	if y >= s.H-StatusBarHeight && y < s.H {
		if x >= s.W-LiveServerWidth {
			s.LiveServerPopupOpen = true
			return true
		}
		return false
	}
	// Sidebar.
	if x >= 0 && x < SidebarWidth && y >= TabStripHeight {
		row := (y - TabStripHeight) / SidebarRowHeight
		if row < 0 || row >= len(s.FileTree) {
			return false
		}
		e := s.FileTree[row]
		if e.IsDir {
			return false
		}
		return s.OpenFile("/" + e.Name)
	}
	// Editor pane.
	if x >= SidebarWidth+GutterWidth && y >= TabStripHeight && y < s.H-StatusBarHeight {
		// Translate pixel (x, y) into (row, col).
		row := (y - TabStripHeight) / LineHeight
		colPx := x - (SidebarWidth + GutterWidth)
		col := colPx / (FontW * EditorFontScale)
		s.Buffer.SetCursor(row, col)
		return true
	}
	return false
}

// inRect reports whether (x, y) lies inside the rectangle (rx, ry, rw, rh).
func inRect(x, y, rx, ry, rw, rh int) bool {
	return x >= rx && x < rx+rw && y >= ry && y < ry+rh
}
