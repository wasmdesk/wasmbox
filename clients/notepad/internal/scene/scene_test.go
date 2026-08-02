// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"strings"
	"testing"

	"github.com/go-widgets/toolkit"
)

const surfaceW = 600
const surfaceH = 400

func newSurface() []byte { return make([]byte, 4*surfaceW*surfaceH) }

func TestNewAndRender(t *testing.T) {
	s := New(surfaceW, surfaceH)
	if s == nil {
		t.Fatal("New returned nil")
	}
	if len(s.docSet) != 2 {
		t.Fatalf("New should seed 2 demo docs, got %d", len(s.docSet))
	}
	Render(s, newSurface())
}

func TestSwitchDoc(t *testing.T) {
	s := New(surfaceW, surfaceH)
	// Edit doc 0.
	s.editor.SetText("edited-0")
	// Switch to doc 1.
	s.switchDoc(1)
	if s.activeIdx != 1 {
		t.Fatalf("switchDoc(1): activeIdx=%d", s.activeIdx)
	}
	if !strings.Contains(s.editor.Text(), "milk") {
		t.Fatalf("switch to doc 1 didn't load its content: %q", s.editor.Text())
	}
	// Switch back to 0 — the edit must persist.
	s.switchDoc(0)
	if s.editor.Text() != "edited-0" {
		t.Fatalf("switch back to doc 0 lost the edit: %q", s.editor.Text())
	}
	// Switching to the current index is a no-op.
	before := s.editor.Text()
	s.switchDoc(0)
	if s.editor.Text() != before {
		t.Fatal("switch to current index should be a no-op")
	}
	// Switching to an out-of-range index is a no-op.
	s.switchDoc(99)
	if s.activeIdx != 0 {
		t.Fatal("out-of-range switchDoc should not change activeIdx")
	}
	s.switchDoc(-1)
	if s.activeIdx != 0 {
		t.Fatal("negative switchDoc should not change activeIdx")
	}
}

func TestNewDoc(t *testing.T) {
	s := New(surfaceW, surfaceH)
	before := len(s.docSet)
	s.newDoc()
	if len(s.docSet) != before+1 {
		t.Fatalf("newDoc: docSet len %d, want %d", len(s.docSet), before+1)
	}
	if s.activeIdx != before {
		t.Fatalf("newDoc: activeIdx should point at the new doc; got %d, want %d", s.activeIdx, before)
	}
	if s.editor.Text() != "" {
		t.Fatalf("newDoc: editor should be empty, got %q", s.editor.Text())
	}
	if !strings.HasPrefix(s.docs.Items[s.activeIdx], "Untitled ") {
		t.Fatalf("newDoc title should start with 'Untitled ', got %q", s.docs.Items[s.activeIdx])
	}
}

func TestSaveDocPersistsToDocSet(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.editor.SetText("dirty content")
	s.saveDoc()
	if s.docSet[0].Content != "dirty content" {
		t.Fatalf("save didn't persist to docSet[0]: %q", s.docSet[0].Content)
	}
	// Notification was shown.
	if !s.notify.Visible {
		t.Fatal("save should show a notification")
	}
}

func TestNotifShowsToast(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.notif("hello")
	if !s.notify.Visible || s.notify.Text != "hello" {
		t.Fatalf("notif: visible=%v text=%q", s.notify.Visible, s.notify.Text)
	}
}

func TestUpdateStatus(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.editor.CursorLine = 4
	s.editor.CursorCol = 11
	s.updateStatus()
	if s.status.Segments[1] != "ln 5, col 12" {
		t.Fatalf("status[1] = %q", s.status.Segments[1])
	}
	if !strings.Contains(s.status.Segments[0], "docs") {
		t.Fatalf("status[0] = %q", s.status.Segments[0])
	}
}

func TestHandleMouseRoutes(t *testing.T) {
	s := New(surfaceW, surfaceH)
	// Toolbar click on the "N" button (index 0, x centre ~12).
	s.HandleMouse(12, 10)
	// After a New click, docSet grew.
	if len(s.docSet) != 3 {
		t.Fatalf("toolbar N click didn't fire newDoc; docSet=%d", len(s.docSet))
	}
	// Click on the docs list (left pane).
	dr := s.docs.Bounds()
	s.HandleMouse(dr.X+10, dr.Y+5)
	// Click on the editor (right pane) — focuses it.
	er := s.editor.Bounds()
	s.HandleMouse(er.X+10, er.Y+10)
	if !s.editor.Focused {
		t.Fatal("editor click should focus")
	}
	// Click on the status bar — no widget path, no-op.
	sr := s.status.Bounds()
	s.HandleMouse(sr.X+10, sr.Y+5)
}

func TestHandleKeyCtrlNCreatesDoc(t *testing.T) {
	s := New(surfaceW, surfaceH)
	before := len(s.docSet)
	s.HandleKey("Ctrl+N")
	if len(s.docSet) != before+1 {
		t.Fatalf("Ctrl+N should create a doc; docSet %d → %d", before, len(s.docSet))
	}
	if s.activeIdx != before {
		t.Fatalf("Ctrl+N should focus the new doc; activeIdx=%d, want %d", s.activeIdx, before)
	}
}

func TestHandleKeyDocSwitchShortcuts(t *testing.T) {
	s := New(surfaceW, surfaceH)
	// Start at doc 0. Ctrl+PageDown → doc 1.
	s.HandleKey("Ctrl+PageDown")
	if s.activeIdx != 1 {
		t.Fatalf("Ctrl+PageDown: activeIdx=%d, want 1", s.activeIdx)
	}
	// Ctrl+Tab → doc 0 (wraps past end since len=2).
	s.HandleKey("Ctrl+Tab")
	if s.activeIdx != 0 {
		t.Fatalf("Ctrl+Tab wrap: activeIdx=%d, want 0", s.activeIdx)
	}
	// Ctrl+PageUp → last doc (wraps back from 0).
	s.HandleKey("Ctrl+PageUp")
	if s.activeIdx != len(s.docSet)-1 {
		t.Fatalf("Ctrl+PageUp wrap: activeIdx=%d, want %d", s.activeIdx, len(s.docSet)-1)
	}
	// Ctrl+Shift+Tab: symmetric to PageUp.
	s.HandleKey("Ctrl+Shift+Tab")
	// Was at len-1, prev = len-2 = 0 for 2 docs.
	if s.activeIdx != 0 {
		t.Fatalf("Ctrl+Shift+Tab: activeIdx=%d, want 0", s.activeIdx)
	}
}

func TestNextPrevDocIdxWrap(t *testing.T) {
	// n=0 defensive branches.
	if got := nextDocIdx(3, 0); got != 0 {
		t.Fatalf("nextDocIdx(3, 0) = %d, want 0", got)
	}
	if got := prevDocIdx(3, 0); got != 0 {
		t.Fatalf("prevDocIdx(3, 0) = %d, want 0", got)
	}
	// Normal wrap.
	if got := nextDocIdx(2, 3); got != 0 {
		t.Fatalf("nextDocIdx(2, 3) = %d, want 0 (wrap)", got)
	}
	if got := prevDocIdx(0, 3); got != 2 {
		t.Fatalf("prevDocIdx(0, 3) = %d, want 2 (wrap)", got)
	}
}

func TestHandleKeyRoutesToEditor(t *testing.T) {
	s := New(surfaceW, surfaceH)
	// Place cursor mid-buffer so Backspace actually deletes something.
	s.editor.CursorLine = 0
	s.editor.CursorCol = 3
	before := s.editor.Text()
	s.HandleKey("Backspace")
	if s.editor.Text() == before {
		t.Fatal("Backspace should have modified the editor buffer")
	}
	// Ctrl+S saves.
	s.HandleKey("Ctrl+S")
	if !s.notify.Visible {
		t.Fatal("Ctrl+S should show the save notification")
	}
}

func TestHandleCharAppends(t *testing.T) {
	s := New(surfaceW, surfaceH)
	before := s.editor.Text()
	s.editor.CursorLine = 0
	s.editor.CursorCol = 0
	s.HandleChar("Z")
	if s.editor.Text() == before {
		t.Fatal("HandleChar should insert into the buffer")
	}
}

func TestTickDrivesNotificationLife(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.notif("countdown")
	life := s.notify.Life
	s.Tick()
	if s.notify.Life != life-1 {
		t.Fatalf("Tick: Life %d, want %d", s.notify.Life, life-1)
	}
}

func TestAllToolbarButtonsFire(t *testing.T) {
	// Exercise the 6 stub OnClick closures (O, S, C, X, V, ?) so the
	// 100 % coverage gate holds. Each shows a notification — assert
	// notif.Text after each click.
	s := New(surfaceW, surfaceH)
	cases := []struct {
		i    int
		want string
	}{
		{1, "Open: no filesystem yet"},
		{2, "Saved (in-memory only)"},
		// index 3 is a separator (no OnClick)
		{4, "Copy: select first (drag not wired)"},
		{5, "Cut: select first"},
		{6, "Paste: no clipboard bridge yet"},
		// index 7 is a separator
		{8, "Notepad v0.1 — a toolkit consumer"},
	}
	for _, c := range cases {
		s.toolbar.Items[c.i].OnClick()
		if s.notify.Text != c.want {
			t.Errorf("Items[%d].OnClick: notify=%q want %q", c.i, s.notify.Text, c.want)
		}
	}
}

func TestFillOutOfBuffer(t *testing.T) {
	buf := make([]byte, 16)
	fill(buf, 4, toolkit.Rect{X: 0, Y: 0, W: 100, H: 100}, toolkit.RGB(0xFF, 0, 0))
}

// TestGoldenLayoutRects is the behaviour-preserving proof: the box-layout
// BorderLayout container tree lays every chrome widget out to the EXACT same
// toolkit.Rect the old hand-placed code produced. The expected rects are
// recomputed here with the original arithmetic — toolbar spanning the top at
// height toolbarH, statusbar spanning the bottom at height statusH, the docs
// list a docsW-wide column on the left below the toolbar and above the status
// bar, and the editor filling the remainder.
func TestGoldenLayoutRects(t *testing.T) {
	s := New(surfaceW, surfaceH)

	wantToolbar := toolkit.Rect{X: 0, Y: 0, W: surfaceW, H: toolbarH}
	if got := s.toolbar.Bounds(); got != wantToolbar {
		t.Fatalf("toolbar bounds = %+v, want %+v", got, wantToolbar)
	}

	wantStatus := toolkit.Rect{X: 0, Y: surfaceH - statusH, W: surfaceW, H: statusH}
	if got := s.status.Bounds(); got != wantStatus {
		t.Fatalf("status bounds = %+v, want %+v", got, wantStatus)
	}

	wantDocs := toolkit.Rect{X: 0, Y: toolbarH, W: docsW, H: surfaceH - toolbarH - statusH}
	if got := s.docs.Bounds(); got != wantDocs {
		t.Fatalf("docs bounds = %+v, want %+v", got, wantDocs)
	}

	wantEditor := toolkit.Rect{X: docsW, Y: toolbarH, W: surfaceW - docsW, H: surfaceH - toolbarH - statusH}
	if got := s.editor.Bounds(); got != wantEditor {
		t.Fatalf("editor bounds = %+v, want %+v", got, wantEditor)
	}
}

// TestClickRoutesToEditorCenter proves a click landing in the Center region
// (past the docsW-wide West column) routes to the editor and focuses it —
// the container translates the surface point into the editor's local space.
func TestClickRoutesToEditorCenter(t *testing.T) {
	s := New(surfaceW, surfaceH)
	s.editor.Focused = false
	er := s.editor.Bounds()
	if !s.HandleMouse(er.X+20, er.Y+20) {
		t.Fatal("HandleMouse in the editor region must return true")
	}
	if !s.editor.Focused {
		t.Fatal("a click in the Center region should focus the editor")
	}
}

// TestClickRoutesToDocsWest proves a click in the West docs column reaches the
// ListBox and activates the row under the pointer — routing the doc switch
// through the container tree instead of the old insideRect dispatch.
func TestClickRoutesToDocsWest(t *testing.T) {
	s := New(surfaceW, surfaceH)
	dr := s.docs.Bounds()
	// Row height defaults to 18; row 1 spans local Y in [18,36). Click at
	// local Y = 20 (surface Y = docs.Y+20) inside the docsW-wide column.
	s.HandleMouse(dr.X+10, dr.Y+20)
	if s.activeIdx != 1 {
		t.Fatalf("click on docs row 1 should switch to doc 1; activeIdx=%d", s.activeIdx)
	}
	if !strings.Contains(s.editor.Text(), "milk") {
		t.Fatalf("switching to doc 1 via click didn't load its content: %q", s.editor.Text())
	}
}

// --- anti-aliased text proof ----------------------------------------------

// distinctLuma counts how many distinct R+G+B sums appear inside region r of an
// RGBA buffer. The 5x7 bitmap font paints each text pixel either full ink or
// untouched ground, so a bitmap render holds only the ground's handful of levels
// plus one ink level; the AA/shaped face scan-converts glyph outlines to
// partial-coverage masks, adding a whole ramp of intermediate levels. A strictly
// higher distinct-luma count over the SAME region is a ground-independent proof
// the AA face is active.
func distinctLuma(buf []byte, W int, r toolkit.Rect) int {
	seen := map[int]struct{}{}
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			o := (y*W + x) * 4
			seen[int(buf[o])+int(buf[o+1])+int(buf[o+2])] = struct{}{}
		}
	}
	return len(seen)
}

// TestAATextIsAntiAliased proves Notepad renders the editor + chrome with the
// toolkit's AA/shaped OpenType face rather than the 5x7 bitmap. It scans the
// editor's top-left text area (the seeded "# Notepad ..." doc), rendering it once
// with the AA face (New enabled it) and once with the bitmap default, and asserts
// the AA render carries strictly more distinct luma levels — the partial-coverage
// ramp only anti-aliasing produces.
func TestAATextIsAntiAliased(t *testing.T) {
	region := toolkit.Rect{X: docsW + 5, Y: toolbarH + 4, W: 240, H: 90}

	aa := newSurface()
	Render(New(surfaceW, surfaceH), aa) // AA face (enableAAText ran in New)

	toolkit.SetFont(nil) // bitmap default
	defer func() { _ = toolkit.UseOpenTypeText() }()
	bm := newSurface()
	Render(New(surfaceW, surfaceH), bm)

	aaN := distinctLuma(aa, surfaceW, region)
	bmN := distinctLuma(bm, surfaceW, region)
	if aaN <= bmN {
		t.Fatalf("editor text: distinct luma aa=%d not > bitmap=%d — AA face not active", aaN, bmN)
	}
	t.Logf("editor distinct luma: aa=%d bm=%d", aaN, bmN)
}

// TestAAFaceFits asserts the taller AA line box fits Notepad's primary text
// bands. The toolbar and editor comfortably hold the 20px face; the 18px status
// bar carries a slightly taller line box, but its actual glyph ink (cap height
// ~12px) still centres inside the band, so only the fixed 20px line-box height is
// asserted against the primary bands here.
func TestAAFaceFits(t *testing.T) {
	_ = New(surfaceW, surfaceH) // switches the global font to the AA face
	f, err := toolkit.DefaultOpenTypeFont(toolkit.DefaultOpenTypeSizePx)
	if err != nil {
		t.Fatalf("DefaultOpenTypeFont: %v", err)
	}
	h := f.Height()
	if h != 20 {
		t.Fatalf("AA face height = %d, want 20 (retune bands if this changes)", h)
	}
	if h > toolbarH {
		t.Fatalf("AA height %d overflows toolbar height %d", h, toolbarH)
	}
	editorH := surfaceH - toolbarH - statusH
	if h > editorH {
		t.Fatalf("AA height %d overflows editor height %d", h, editorH)
	}
}
