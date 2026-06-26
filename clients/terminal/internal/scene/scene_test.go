// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import "testing"

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
	if nonZero != len(s.Shell.Prompt) {
		t.Fatalf("after `clear`, non-zero cells = %d, want prompt-length %d",
			nonZero, len(s.Shell.Prompt))
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
