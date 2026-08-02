// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's model.go is the file browser's pure (toolkit-free) UI state:
// a current path, a cached listing of its entries, the cursor index inside
// that listing, and a sectioned sidebar that mirrors GNOME Nautilus's left
// pane (Bookmarks + Other Locations). It also carries the small display
// helpers (breadcrumb splitting, byte-count formatting, text-preview parsing).
//
// Everything here is plain Go (no syscall/js, no cgo, no toolkit) so it builds
// + tests natively on every architecture the repo targets; scene.go layers the
// go-widgets/toolkit widget tree + event routing on top of it.

package scene

// SidebarEntry is one row in the navigation sidebar. Section groups entries
// in the rendered list ("BOOKMARKS", "OTHER LOCATIONS"); Kind picks the glyph
// the renderer paints next to the label ("home" star, "folder", "computer"
// monitor, "trash" bin); Path is the VFS path the row navigates to when
// clicked.
type SidebarEntry struct {
	Section string
	Kind    string
	Name    string
	Path    string
}

// DefaultSidebar is the canonical Nautilus-style navigation list shown in the
// left pane. Two sections:
//
//   - BOOKMARKS: Home, Documents, Pictures, Downloads
//   - OTHER LOCATIONS: Computer, Trash
//
// Home points at the root "/" so the breadcrumb's "Home" segment lights up the
// right sidebar row. Computer and Trash are placeholders -- they point at the
// root because the demo VFS has no real disk inventory or trashcan; clicking
// them still feels reactive (the selected-row highlight moves).
func DefaultSidebar() []SidebarEntry {
	return []SidebarEntry{
		{Section: "BOOKMARKS", Kind: "home", Name: "Home", Path: "/"},
		{Section: "BOOKMARKS", Kind: "folder", Name: "Documents", Path: "/Documents"},
		{Section: "BOOKMARKS", Kind: "folder", Name: "Pictures", Path: "/Pictures"},
		{Section: "BOOKMARKS", Kind: "folder", Name: "Downloads", Path: "/Downloads"},
		{Section: "OTHER LOCATIONS", Kind: "computer", Name: "Computer", Path: "/"},
		{Section: "OTHER LOCATIONS", Kind: "trash", Name: "Trash", Path: "/"},
	}
}

// PreviewOverlay is the simple "double-click .txt" overlay content: a file
// name + up to PreviewMaxLines of its text body. scene.go renders it as a
// centred modal panel; the next click clears it.
type PreviewOverlay struct {
	Path  string
	Lines []string
}

// BrowserState owns the navigation cursor. CurrentPath is always normalised
// (see vfs.Clean); Entries is the cached List(CurrentPath) result. Cursor
// indexes into Entries; we keep it in [0, len(Entries)) by clamping on every
// mutation.
type BrowserState struct {
	CurrentPath string
	Entries     []Entry
	Cursor      int
}

// Refresh re-lists CurrentPath, swaps the cached Entries, and clamps Cursor
// into the new range. Called whenever CurrentPath changes (e.g. ActivateCurrent
// on a folder, GoUp) and at construction time so the renderer never sees a
// stale or nil Entries slice.
func (b *BrowserState) Refresh(vfs VFS) {
	entries, err := vfs.List(b.CurrentPath)
	if err != nil {
		// A missing or non-dir path falls back to the root rather than
		// leaving the browser stuck on an unreadable location.
		b.CurrentPath = "/"
		entries, _ = vfs.List("/")
	}
	b.Entries = entries
	b.clampCursor()
}

// MoveCursor shifts the cursor by dy and clamps it into [0, len(Entries)).
// Callers pass +1 for "down arrow" and -1 for "up arrow"; larger steps work
// the same way (no PageDown today but the math holds).
func (b *BrowserState) MoveCursor(dy int) {
	b.Cursor += dy
	b.clampCursor()
}

// ActivateCurrent enters the currently-selected entry: a directory becomes the
// new CurrentPath (and Refresh re-lists it); a file is a no-op. Returns true
// when CurrentPath changed, so the caller can decide whether to re-render.
func (b *BrowserState) ActivateCurrent(vfs VFS) bool {
	if b.Cursor < 0 || b.Cursor >= len(b.Entries) {
		return false
	}
	e := b.Entries[b.Cursor]
	if !e.IsDir {
		return false
	}
	b.CurrentPath = Join(b.CurrentPath, e.Name)
	b.Cursor = 0
	b.Refresh(vfs)
	return true
}

// GoUp navigates to the parent of CurrentPath. If we are already at the root it
// is a no-op (Parent("/") == "/"). Returns true when CurrentPath changed.
func (b *BrowserState) GoUp(vfs VFS) bool {
	parent := Parent(b.CurrentPath)
	if parent == b.CurrentPath {
		return false
	}
	b.CurrentPath = parent
	b.Cursor = 0
	b.Refresh(vfs)
	return true
}

// clampCursor pins Cursor into [0, len(Entries)). For an empty directory the
// cursor becomes 0; the renderer paints no selection bar in that case (no row
// exists to highlight).
func (b *BrowserState) clampCursor() {
	if len(b.Entries) == 0 {
		b.Cursor = 0
		return
	}
	if b.Cursor < 0 {
		b.Cursor = 0
	}
	if b.Cursor >= len(b.Entries) {
		b.Cursor = len(b.Entries) - 1
	}
}

// PathCrumbs splits a Clean path into its display segments. "/" renders as just
// "Home"; "/Documents" becomes ["Home", "Documents"]. The first segment is
// always "Home" so the active crumb at the root is the Home crumb itself.
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

// hasTextExt reports whether name ends with one of the extensions the preview
// overlay knows how to render (.txt, .md). Pulled out so the double-click + the
// menu's Open both agree on "is this previewable?".
func hasTextExt(name string) bool {
	if len(name) >= 4 && name[len(name)-4:] == ".txt" {
		return true
	}
	if len(name) >= 3 && name[len(name)-3:] == ".md" {
		return true
	}
	return false
}

// splitLines splits body on '\n' returning at most maxLines lines. Empty
// trailing line from a final '\n' is dropped.
func splitLines(body string, maxLines int) []string {
	if body == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(body) && len(out) < maxLines; i++ {
		if body[i] == '\n' {
			out = append(out, body[start:i])
			start = i + 1
		}
	}
	if len(out) < maxLines && start < len(body) {
		out = append(out, body[start:])
	}
	return out
}

// itoa is a tiny base-10 formatter used by createSibling to disambiguate
// "untitled-1.txt" / "untitled-2.txt". Pulled local so the package keeps
// strconv out of its import list where it can.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// formatSize prettifies a byte count for the Size column. A folder renders as
// "--" (the row widget takes care of that); for files we use "234 bytes",
// "1.2 KB", "89.1 KB", ... -- Nautilus shows "bytes" for sub-1024 sizes.
func formatSize(n int64) string {
	if n < 1024 {
		return itoa64(n) + " bytes"
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
	return itoa64(whole) + "." + itoa64(frac) + " " + units[idx]
}

// itoa64 is the int64 sibling of itoa, kept local so formatSize does not pull
// strconv into the package.
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
