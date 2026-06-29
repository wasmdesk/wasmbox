// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// extra_test.go covers the few defensive guards the main test files don't
// reach: clampCursor's Row<0 / Col<0 branches (callers that construct a
// TextBuffer directly with a negative cursor).

package scene

import "testing"

// clampCursor's "Row < 0" branch fires when a caller manually pokes a
// negative Row into an otherwise-valid buffer + then triggers a mutation.
func TestClampCursor_NegativeRow(t *testing.T) {
	b := NewTextBuffer("abc")
	b.Cursor.Row = -5
	b.Cursor.Col = 1
	b.Insert("X")
	if b.Cursor.Row != 0 {
		t.Fatalf("Row not clamped: %d", b.Cursor.Row)
	}
}

// clampCursor's "Col < 0" branch fires the same way for Col.
func TestClampCursor_NegativeCol(t *testing.T) {
	b := NewTextBuffer("abc")
	b.Cursor.Row = 0
	b.Cursor.Col = -7
	b.Insert("Y")
	if b.Cursor.Col < 0 {
		t.Fatalf("Col not clamped: %d", b.Cursor.Col)
	}
}
