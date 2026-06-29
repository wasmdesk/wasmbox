// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's buffer.go is the editable text store the code client paints:
// a slice of UTF-8 line strings + a (Row,Col) cursor. Pure Go, no syscall/js,
// so the whole package builds + tests natively on every architecture this
// repo targets.
//
// The buffer intentionally keeps mutation primitives small (Insert / Delete /
// Split / MoveCursor) so the dispatcher in state.go can compose them without
// caring about cursor bookkeeping; every primitive clamps the cursor into
// the new shape before returning so the caller never observes a
// past-the-end (row, col).

package scene

// TextBuffer is the editable text document the editor pane paints. Lines is
// always non-empty -- a "blank" buffer holds a single empty line so the
// cursor at (0, 0) is always inside the document. Callers should not mutate
// Lines directly; reach for the primitives below so the cursor stays valid.
type TextBuffer struct {
	Lines  []string
	Cursor Cursor
}

// Cursor is the editing caret. Row is a 0-based index into Lines; Col is a
// 0-based byte offset into the row text (inclusive of len(line) so the
// cursor can sit at end-of-line for insert-at-end). Both clamp on every
// mutation; callers never need to validate manually.
type Cursor struct {
	Row int
	Col int
}

// NewTextBuffer returns a buffer holding the supplied body, split on '\n'.
// An empty body yields a buffer with a single empty line so the cursor at
// (0, 0) is always valid. Trailing '\n' in body is honoured (it produces a
// trailing empty line); callers wishing to strip it should do so before the
// call.
func NewTextBuffer(body string) *TextBuffer {
	if body == "" {
		return &TextBuffer{Lines: []string{""}}
	}
	lines := []string{""}
	for i := 0; i < len(body); i++ {
		if body[i] == '\n' {
			lines = append(lines, "")
			continue
		}
		lines[len(lines)-1] += string(body[i])
	}
	return &TextBuffer{Lines: lines}
}

// String serialises the buffer back to a single string joined by '\n'.
// Round-trips with NewTextBuffer: NewTextBuffer(b.String()).String() == b.String()
// when the buffer was built from a NewTextBuffer call.
func (b *TextBuffer) String() string {
	out := ""
	for i, ln := range b.Lines {
		if i > 0 {
			out += "\n"
		}
		out += ln
	}
	return out
}

// Insert inserts s at the cursor, advancing the cursor past the inserted
// text. s must not contain '\n' (use Split for newline insertion); a
// '\n' inside s is treated as the literal byte 0x0A and inserted verbatim,
// which leaves the buffer in a state the renderer cannot show correctly.
// The state-machine guard rails (HandleKey) ensure this never happens.
func (b *TextBuffer) Insert(s string) {
	b.clampCursor()
	line := b.Lines[b.Cursor.Row]
	left := line[:b.Cursor.Col]
	right := line[b.Cursor.Col:]
	b.Lines[b.Cursor.Row] = left + s + right
	b.Cursor.Col += len(s)
}

// Delete deletes the character before the cursor (Backspace). At column 0
// of row R>0 the deletion joins row R into row R-1 (the cursor lands at the
// join point). At (0, 0) it is a no-op. Returns true when the buffer
// changed -- callers re-render only when true.
func (b *TextBuffer) Delete() bool {
	b.clampCursor()
	if b.Cursor.Col > 0 {
		line := b.Lines[b.Cursor.Row]
		b.Lines[b.Cursor.Row] = line[:b.Cursor.Col-1] + line[b.Cursor.Col:]
		b.Cursor.Col--
		return true
	}
	if b.Cursor.Row == 0 {
		return false
	}
	// Join row into the previous line.
	prev := b.Lines[b.Cursor.Row-1]
	cur := b.Lines[b.Cursor.Row]
	newCol := len(prev)
	b.Lines = append(b.Lines[:b.Cursor.Row-1], append([]string{prev + cur}, b.Lines[b.Cursor.Row+1:]...)...)
	b.Cursor.Row--
	b.Cursor.Col = newCol
	return true
}

// Split splits the current line at the cursor (Enter). The text to the
// right of the cursor becomes a new line inserted below; the cursor jumps
// to (Row+1, 0).
func (b *TextBuffer) Split() {
	b.clampCursor()
	line := b.Lines[b.Cursor.Row]
	left := line[:b.Cursor.Col]
	right := line[b.Cursor.Col:]
	// Insert a new line after Row by reshaping the slice.
	newLines := make([]string, 0, len(b.Lines)+1)
	newLines = append(newLines, b.Lines[:b.Cursor.Row]...)
	newLines = append(newLines, left, right)
	newLines = append(newLines, b.Lines[b.Cursor.Row+1:]...)
	b.Lines = newLines
	b.Cursor.Row++
	b.Cursor.Col = 0
}

// MoveCursor shifts the cursor by (dRow, dCol), clamping to the document
// shape. Horizontal moves wrap at line ends in the typical text-editor
// sense: ArrowRight at end-of-line jumps to (Row+1, 0); ArrowLeft at (R,0)
// jumps to (R-1, lastCol). Returns true when the cursor actually moved.
func (b *TextBuffer) MoveCursor(dRow, dCol int) bool {
	b.clampCursor()
	oldR := b.Cursor.Row
	oldC := b.Cursor.Col
	switch {
	case dCol > 0:
		// ArrowRight.
		for i := 0; i < dCol; i++ {
			if b.Cursor.Col < len(b.Lines[b.Cursor.Row]) {
				b.Cursor.Col++
			} else if b.Cursor.Row < len(b.Lines)-1 {
				b.Cursor.Row++
				b.Cursor.Col = 0
			}
		}
	case dCol < 0:
		// ArrowLeft.
		for i := 0; i < -dCol; i++ {
			if b.Cursor.Col > 0 {
				b.Cursor.Col--
			} else if b.Cursor.Row > 0 {
				b.Cursor.Row--
				b.Cursor.Col = len(b.Lines[b.Cursor.Row])
			}
		}
	}
	if dRow != 0 {
		b.Cursor.Row += dRow
		if b.Cursor.Row < 0 {
			b.Cursor.Row = 0
		}
		if b.Cursor.Row >= len(b.Lines) {
			b.Cursor.Row = len(b.Lines) - 1
		}
		if b.Cursor.Col > len(b.Lines[b.Cursor.Row]) {
			b.Cursor.Col = len(b.Lines[b.Cursor.Row])
		}
	}
	return b.Cursor.Row != oldR || b.Cursor.Col != oldC
}

// SetCursor moves the cursor to (row, col), clamping into the buffer's
// shape. Used by mouse-click handling (which maps a pixel coordinate to a
// (row, col) and then jumps the cursor there).
func (b *TextBuffer) SetCursor(row, col int) {
	if row < 0 {
		row = 0
	}
	if row >= len(b.Lines) {
		row = len(b.Lines) - 1
	}
	if col < 0 {
		col = 0
	}
	if col > len(b.Lines[row]) {
		col = len(b.Lines[row])
	}
	b.Cursor.Row = row
	b.Cursor.Col = col
}

// clampCursor pins the cursor into the document. Called at the head of every
// mutation so callers never have to think about stale (row, col) values.
func (b *TextBuffer) clampCursor() {
	if len(b.Lines) == 0 {
		b.Lines = []string{""}
	}
	if b.Cursor.Row < 0 {
		b.Cursor.Row = 0
	}
	if b.Cursor.Row >= len(b.Lines) {
		b.Cursor.Row = len(b.Lines) - 1
	}
	if b.Cursor.Col < 0 {
		b.Cursor.Col = 0
	}
	if b.Cursor.Col > len(b.Lines[b.Cursor.Row]) {
		b.Cursor.Col = len(b.Lines[b.Cursor.Row])
	}
}
