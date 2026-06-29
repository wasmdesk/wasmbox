// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// buffer_test.go drives every code path in TextBuffer to 100% line coverage:
// Insert / Delete / Split / MoveCursor / SetCursor / clampCursor / Round-trip
// via String + NewTextBuffer.

package scene

import "testing"

func TestNewTextBuffer_EmptyAndBody(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", []string{""}},
		{"one line", "hello", []string{"hello"}},
		{"two lines", "a\nb", []string{"a", "b"}},
		{"trailing newline", "a\n", []string{"a", ""}},
		{"only newline", "\n", []string{"", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewTextBuffer(tc.in)
			if len(b.Lines) != len(tc.want) {
				t.Fatalf("line count: got %d want %d (%q)", len(b.Lines), len(tc.want), b.Lines)
			}
			for i := range tc.want {
				if b.Lines[i] != tc.want[i] {
					t.Errorf("line %d: got %q want %q", i, b.Lines[i], tc.want[i])
				}
			}
		})
	}
}

func TestTextBuffer_String_RoundTrip(t *testing.T) {
	for _, body := range []string{"hello", "a\nb\nc", "a\n"} {
		b := NewTextBuffer(body)
		if got := b.String(); got != body {
			t.Errorf("round-trip %q -> %q", body, got)
		}
	}
	// Empty: NewTextBuffer("") gives Lines=[""], String() returns "".
	b := NewTextBuffer("")
	if got := b.String(); got != "" {
		t.Errorf("empty round-trip got %q want %q", got, "")
	}
}

func TestTextBuffer_Insert(t *testing.T) {
	b := NewTextBuffer("ab")
	b.SetCursor(0, 1)
	b.Insert("X")
	if b.Lines[0] != "aXb" {
		t.Fatalf("insert mid: got %q", b.Lines[0])
	}
	if b.Cursor.Col != 2 {
		t.Fatalf("insert cursor col: got %d", b.Cursor.Col)
	}
}

func TestTextBuffer_InsertAtEnd(t *testing.T) {
	b := NewTextBuffer("ab")
	b.SetCursor(0, 2)
	b.Insert("XY")
	if b.Lines[0] != "abXY" {
		t.Fatalf("insert end: got %q", b.Lines[0])
	}
	if b.Cursor.Col != 4 {
		t.Fatalf("insert end cursor col: got %d", b.Cursor.Col)
	}
}

func TestTextBuffer_Delete_MidLine(t *testing.T) {
	b := NewTextBuffer("abc")
	b.SetCursor(0, 2)
	if !b.Delete() {
		t.Fatal("Delete returned false")
	}
	if b.Lines[0] != "ac" {
		t.Fatalf("delete mid: got %q", b.Lines[0])
	}
	if b.Cursor.Col != 1 {
		t.Fatalf("delete mid cursor: got %d", b.Cursor.Col)
	}
}

func TestTextBuffer_Delete_AtRowStart_JoinsLines(t *testing.T) {
	b := NewTextBuffer("ab\ncd")
	b.SetCursor(1, 0)
	if !b.Delete() {
		t.Fatal("Delete returned false")
	}
	if len(b.Lines) != 1 {
		t.Fatalf("expected 1 line, got %d (%q)", len(b.Lines), b.Lines)
	}
	if b.Lines[0] != "abcd" {
		t.Fatalf("delete join: got %q", b.Lines[0])
	}
	if b.Cursor.Row != 0 || b.Cursor.Col != 2 {
		t.Fatalf("delete join cursor: got (%d,%d)", b.Cursor.Row, b.Cursor.Col)
	}
}

func TestTextBuffer_Delete_AtOrigin_NoOp(t *testing.T) {
	b := NewTextBuffer("abc")
	b.SetCursor(0, 0)
	if b.Delete() {
		t.Fatal("Delete at (0,0) should return false")
	}
}

func TestTextBuffer_Split(t *testing.T) {
	b := NewTextBuffer("abcdef")
	b.SetCursor(0, 3)
	b.Split()
	if len(b.Lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(b.Lines))
	}
	if b.Lines[0] != "abc" || b.Lines[1] != "def" {
		t.Fatalf("split: %q", b.Lines)
	}
	if b.Cursor.Row != 1 || b.Cursor.Col != 0 {
		t.Fatalf("split cursor: (%d,%d)", b.Cursor.Row, b.Cursor.Col)
	}
}

func TestTextBuffer_Split_AtEnd(t *testing.T) {
	b := NewTextBuffer("ab")
	b.SetCursor(0, 2)
	b.Split()
	if len(b.Lines) != 2 || b.Lines[0] != "ab" || b.Lines[1] != "" {
		t.Fatalf("split-at-end: %q", b.Lines)
	}
}

func TestTextBuffer_MoveCursor_VerticalAndHorizontal(t *testing.T) {
	b := NewTextBuffer("abc\ndef")
	b.SetCursor(0, 0)

	// Right within line.
	if !b.MoveCursor(0, 1) {
		t.Fatal("right move not detected")
	}
	if b.Cursor.Col != 1 {
		t.Fatalf("col after right: %d", b.Cursor.Col)
	}

	// Right past end -> wrap to next line.
	b.SetCursor(0, 3)
	b.MoveCursor(0, 1)
	if b.Cursor.Row != 1 || b.Cursor.Col != 0 {
		t.Fatalf("wrap-right: (%d,%d)", b.Cursor.Row, b.Cursor.Col)
	}

	// Right at very end -> no move.
	b.SetCursor(1, 3)
	if b.MoveCursor(0, 1) {
		t.Fatal("right at end-of-doc moved")
	}

	// Left within line.
	b.SetCursor(1, 2)
	b.MoveCursor(0, -1)
	if b.Cursor.Col != 1 {
		t.Fatalf("col after left: %d", b.Cursor.Col)
	}

	// Left at col 0 -> wrap to previous line end.
	b.SetCursor(1, 0)
	b.MoveCursor(0, -1)
	if b.Cursor.Row != 0 || b.Cursor.Col != 3 {
		t.Fatalf("wrap-left: (%d,%d)", b.Cursor.Row, b.Cursor.Col)
	}

	// Left at (0,0) -> no move.
	b.SetCursor(0, 0)
	if b.MoveCursor(0, -1) {
		t.Fatal("left at (0,0) moved")
	}

	// Down moves row, clamps col.
	b.SetCursor(0, 3)
	b.MoveCursor(1, 0)
	if b.Cursor.Row != 1 || b.Cursor.Col != 3 {
		t.Fatalf("down: (%d,%d)", b.Cursor.Row, b.Cursor.Col)
	}

	// Down past end clamps to last row.
	b.SetCursor(1, 0)
	b.MoveCursor(5, 0)
	if b.Cursor.Row != 1 {
		t.Fatalf("down clamp: %d", b.Cursor.Row)
	}

	// Up past origin clamps to row 0.
	b.SetCursor(1, 0)
	b.MoveCursor(-5, 0)
	if b.Cursor.Row != 0 {
		t.Fatalf("up clamp: %d", b.Cursor.Row)
	}

	// Down onto a shorter line clamps the column.
	b2 := NewTextBuffer("abcdef\nx")
	b2.SetCursor(0, 5)
	b2.MoveCursor(1, 0)
	if b2.Cursor.Row != 1 || b2.Cursor.Col != 1 {
		t.Fatalf("down-with-clamp: (%d,%d)", b2.Cursor.Row, b2.Cursor.Col)
	}
}

func TestTextBuffer_SetCursor_Clamps(t *testing.T) {
	b := NewTextBuffer("ab\ncde")
	// Negative -> (0,0).
	b.SetCursor(-1, -1)
	if b.Cursor.Row != 0 || b.Cursor.Col != 0 {
		t.Fatalf("neg clamp: (%d,%d)", b.Cursor.Row, b.Cursor.Col)
	}
	// Past-end row -> last row.
	b.SetCursor(99, 99)
	if b.Cursor.Row != 1 || b.Cursor.Col != 3 {
		t.Fatalf("past clamp: (%d,%d)", b.Cursor.Row, b.Cursor.Col)
	}
	// Valid.
	b.SetCursor(1, 2)
	if b.Cursor.Row != 1 || b.Cursor.Col != 2 {
		t.Fatalf("valid set: (%d,%d)", b.Cursor.Row, b.Cursor.Col)
	}
}

func TestTextBuffer_clampCursor_RehydratesEmpty(t *testing.T) {
	// Manually set Lines to empty -- mimics a malformed external mutation;
	// clampCursor (called by Insert) should rehydrate to a single empty line.
	b := &TextBuffer{Lines: nil, Cursor: Cursor{Row: 5, Col: 5}}
	b.Insert("X")
	if len(b.Lines) == 0 || b.Lines[0] != "X" {
		t.Fatalf("rehydrate: %q", b.Lines)
	}
	if b.Cursor.Row != 0 {
		t.Fatalf("rehydrate cursor row: %d", b.Cursor.Row)
	}
}

func TestTextBuffer_MoveCursor_NoMoveReturnsFalse(t *testing.T) {
	b := NewTextBuffer("a")
	b.SetCursor(0, 0)
	if b.MoveCursor(0, 0) {
		t.Fatal("zero move should return false")
	}
}

func TestTextBuffer_clampCursor_NegativeRowAndCol(t *testing.T) {
	// Direct field write to drive the Row<0 and Col<0 branches of clampCursor.
	b := &TextBuffer{Lines: []string{"abc"}, Cursor: Cursor{Row: -3, Col: -7}}
	b.Insert("X") // triggers clampCursor before Insert proceeds
	if b.Cursor.Row != 0 {
		t.Fatalf("Row after clamp: %d", b.Cursor.Row)
	}
	if b.Lines[0] != "Xabc" {
		t.Fatalf("Insert after clamp: %q", b.Lines[0])
	}
}
