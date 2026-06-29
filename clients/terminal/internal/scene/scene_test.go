// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import (
	"strings"
	"testing"

	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

// newBuf allocates a buffer the right size for state s.
func newBuf(s *State) []byte { return make([]byte, 4*s.W*s.H) }

// New produces a state whose grid fits the chosen surface at 2x scale.
func TestNewSizesGrid(t *testing.T) {
	s := New(640, 400)
	if s.Grid.Cols != 40 || s.Grid.Rows != 25 {
		t.Fatalf("New(640,400): grid = %dx%d, want 40x25", s.Grid.Cols, s.Grid.Rows)
	}
	if s.W != 640 || s.H != 400 {
		t.Fatalf("New stored dims = (%d,%d), want (640,400)", s.W, s.H)
	}
}

// A tiny surface still produces at least a 1x1 grid.
func TestNewClampsToMinimum(t *testing.T) {
	s := New(1, 1)
	if s.Grid.Cols != 1 || s.Grid.Rows != 1 {
		t.Fatalf("New(1,1): grid = %dx%d, want 1x1", s.Grid.Cols, s.Grid.Rows)
	}
}

// Render fills an exactly-sized buffer without panicking.
func TestRenderFillsExactSize(t *testing.T) {
	s := New(160, 100)
	Render(s, newBuf(s))
}

// Render panics on a wrongly-sized buffer.
func TestRenderPanicsOnSizeMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on size mismatch")
		}
	}()
	s := New(16, 16)
	Render(s, make([]byte, 4))
}

// HandleKey: a printable key appends to the edit line + paints into the grid.
func TestHandleKeyPrintable(t *testing.T) {
	s := New(160, 100)
	startLine := len(s.Shell.Line)
	if !s.HandleKey("a") {
		t.Fatal("HandleKey('a') did not signal a change")
	}
	if len(s.Shell.Line) != startLine+1 || s.Shell.Line[len(s.Shell.Line)-1] != 'a' {
		t.Fatalf("Shell.Line = %q, want trailing 'a'", string(s.Shell.Line))
	}
}

// HandleKey: empty string + non-printable returns false (no change).
func TestHandleKeyEmpty(t *testing.T) {
	s := New(160, 100)
	if s.HandleKey("") {
		t.Fatal("HandleKey('') unexpectedly reported a change")
	}
	if s.HandleKey("ArrowLeft") {
		t.Fatal("HandleKey('ArrowLeft') unexpectedly reported a change")
	}
	// A single non-printable byte (e.g. tab) is also ignored.
	if s.HandleKey("\x01") {
		t.Fatal("HandleKey(0x01) unexpectedly reported a change")
	}
}

// HandleKey: Backspace at an empty line is a no-op.
func TestHandleKeyBackspaceEmpty(t *testing.T) {
	s := New(160, 100)
	if s.HandleKey("Backspace") {
		t.Fatal("Backspace on empty line should not signal a change")
	}
}

// HandleKey: Backspace shrinks the edit line + reverse-cursors the grid.
func TestHandleKeyBackspaceNonEmpty(t *testing.T) {
	s := New(160, 100)
	s.HandleKey("a")
	s.HandleKey("b")
	if !s.HandleKey("Backspace") {
		t.Fatal("Backspace on non-empty line should signal a change")
	}
	if string(s.Shell.Line) != "a" {
		t.Fatalf("after Backspace, Shell.Line = %q, want \"a\"", string(s.Shell.Line))
	}
}

// HandleKey: Enter executes the line + paints output into the grid.
func TestHandleKeyEnterEcho(t *testing.T) {
	s := New(320, 200)
	for _, k := range []string{"e", "c", "h", "o", " ", "h", "i"} {
		s.HandleKey(k)
	}
	if !s.HandleKey("Enter") {
		t.Fatal("Enter should signal a change")
	}
	if len(s.Shell.Line) != 0 {
		t.Fatalf("Shell.Line not reset after Enter: %q", string(s.Shell.Line))
	}
	// The grid should now contain "hi" somewhere -- find the 'h' followed by 'i'.
	found := false
	for i := 0; i+1 < len(s.Grid.Cells); i++ {
		if s.Grid.Cells[i].Glyph == 'h' && s.Grid.Cells[i+1].Glyph == 'i' {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("echo output 'hi' not present in grid after Enter")
	}
}

// HandleKey: Enter on `clear` blanks the grid + paints a fresh prompt.
func TestHandleKeyEnterClear(t *testing.T) {
	s := New(320, 200)
	for _, k := range []string{"c", "l", "e", "a", "r"} {
		s.HandleKey(k)
	}
	s.HandleKey("Enter")
	// After clear, only the prompt cells should carry ink. Count non-zero
	// glyph cells -- should equal len(prompt).
	nonZero := 0
	for _, c := range s.Grid.Cells {
		if c.Glyph != 0 {
			nonZero++
		}
	}
	// The prompt the shell paints after a clear is the full PromptString()
	// (cwd + trailing prompt), not just sh.Prompt.
	if nonZero != len(s.Shell.PromptString()) {
		t.Fatalf("after `clear`, non-zero cells = %d, want prompt-length %d",
			nonZero, len(s.Shell.PromptString()))
	}
}

// ExecuteForTest wraps Shell.Execute -- exercise it to keep it covered.
func TestExecuteForTest(t *testing.T) {
	s := New(160, 100)
	out := s.ExecuteForTest("echo hi\n")
	if len(out) != 1 || out[0] != "hi" {
		t.Fatalf("ExecuteForTest('echo hi\\n') = %v, want [\"hi\"]", out)
	}
}

// completionBuiltins merges the three local builtins with every coreutils
// tool name. The list must (a) include "cd" (local) and "echo" (multicall)
// and (b) be sorted -- the completion contract requires sorted input.
func TestCompletionBuiltinsMerged(t *testing.T) {
	got := completionBuiltins()
	if len(got) < 4 {
		t.Fatalf("completionBuiltins() too short: %d", len(got))
	}
	have := map[string]bool{}
	for _, b := range got {
		have[b] = true
	}
	for _, w := range []string{"cd", "clear", "help", "echo"} {
		if !have[w] {
			t.Errorf("completionBuiltins() missing %q (got %v)", w, got)
		}
	}
	// Sorted invariant.
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Errorf("completionBuiltins() not sorted at %d: %q then %q", i, got[i-1], got[i])
		}
	}
}

// newSceneWithVFS spins up a State that owns a fresh InMemoryVFS the test
// can pre-populate. Surface is large enough (640x400 => 40x25 cells) that
// multi-match menus + the redrawn prompt all fit on screen.
func newSceneWithVFS(t *testing.T, seed func(v sharedvfs.VFS)) *State {
	t.Helper()
	v := sharedvfs.NewInMemoryVFS()
	if seed != nil {
		seed(v)
	}
	return NewWithVFS(640, 400, v)
}

// gridString joins the grid's glyphs row-by-row into a single string for
// substring assertions. Empty cells (Glyph==0) become spaces so the result
// reads like a screen dump.
func gridString(s *State) string {
	g := s.Grid
	var b strings.Builder
	for r := 0; r < g.Rows; r++ {
		for c := 0; c < g.Cols; c++ {
			ch := g.Cells[r*g.Cols+c].Glyph
			if ch == 0 {
				b.WriteByte(' ')
			} else {
				b.WriteByte(ch)
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// TestHandleKeyTabSingleMatch: typing "ec" + Tab autocompletes to "echo"
// (single match in command position) -- Line grows and the new bytes are
// painted into the grid.
func TestHandleKeyTabSingleMatch(t *testing.T) {
	s := newSceneWithVFS(t, nil)
	for _, k := range []string{"e", "c"} {
		s.HandleKey(k)
	}
	if !s.HandleKey("Tab") {
		t.Fatal("Tab on 'ec' should signal a change")
	}
	if string(s.Shell.Line) != "echo" {
		t.Fatalf("Shell.Line = %q, want %q", string(s.Shell.Line), "echo")
	}
	if !strings.Contains(gridString(s), "echo") {
		t.Fatal("grid does not contain 'echo' after Tab autocompletion")
	}
}

// TestHandleKeyTabMultiMatch: typing "r" + Tab in command position has
// two matches (rm, rmdir) -- Line should stay "r" but the matches should
// be painted into the grid below the prompt.
func TestHandleKeyTabMultiMatch(t *testing.T) {
	s := newSceneWithVFS(t, nil)
	s.HandleKey("r")
	if !s.HandleKey("Tab") {
		t.Fatal("Tab on 'r' should signal a change (printed menu)")
	}
	if string(s.Shell.Line) != "r" {
		t.Fatalf("Shell.Line = %q, want unchanged %q", string(s.Shell.Line), "r")
	}
	grid := gridString(s)
	if !strings.Contains(grid, "rm") {
		t.Fatal("grid missing 'rm' candidate after multi-match Tab")
	}
	if !strings.Contains(grid, "rmdir") {
		t.Fatal("grid missing 'rmdir' candidate after multi-match Tab")
	}
}

// TestHandleKeyTabNoMatch: typing a prefix that matches nothing returns
// false (no grid change) -- the user sees their line as-is.
func TestHandleKeyTabNoMatch(t *testing.T) {
	s := newSceneWithVFS(t, nil)
	for _, k := range []string{"z", "z", "z"} {
		s.HandleKey(k)
	}
	if s.HandleKey("Tab") {
		t.Fatal("Tab on 'zzz' should NOT signal a change (no matches)")
	}
	if string(s.Shell.Line) != "zzz" {
		t.Fatalf("Shell.Line = %q, want %q", string(s.Shell.Line), "zzz")
	}
}

// TestHandleKeyTabArgFilenameSingle: typing "ls /scr" + Tab autocompletes
// the directory to "/scratch/" -- the new bytes land in Line + grid.
func TestHandleKeyTabArgFilenameSingle(t *testing.T) {
	s := newSceneWithVFS(t, func(v sharedvfs.VFS) {
		if err := v.Mkdir("/scratch"); err != nil {
			t.Fatal(err)
		}
	})
	for _, k := range []string{"l", "s", " ", "/", "s", "c", "r"} {
		s.HandleKey(k)
	}
	if !s.HandleKey("Tab") {
		t.Fatal("Tab on 'ls /scr' should signal a change")
	}
	if string(s.Shell.Line) != "ls /scratch/" {
		t.Fatalf("Shell.Line = %q, want %q", string(s.Shell.Line), "ls /scratch/")
	}
}

// TestHandleKeyTabSingleMatchNoExtension: the single match exactly equals
// the target -- no bytes to add, so handleTab returns false (no change).
func TestHandleKeyTabSingleMatchNoExtension(t *testing.T) {
	s := newSceneWithVFS(t, nil)
	for _, k := range []string{"e", "c", "h", "o"} {
		s.HandleKey(k)
	}
	// "echo" matches exactly one builtin ("echo") with nothing to extend.
	if s.HandleKey("Tab") {
		t.Fatal("Tab on full 'echo' should NOT signal a change")
	}
}
