// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import "testing"

func newBuf(s *State) []byte { return make([]byte, 4*s.W*s.H) }

// Render must fill an exactly-sized buffer without panicking.
func TestRenderFillsExactSize(t *testing.T) {
	s := New(200, 120)
	Render(s, newBuf(s))
}

// A buffer of the wrong length is a programmer bug — Render panics.
func TestRenderPanicsOnSizeMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on size mismatch")
		}
	}()
	s := New(8, 8)
	Render(s, make([]byte, 4))
}

// The background must be the canonical light-cream colour — sample a pixel
// in the gap between rows (or above the first row, in RowPad).
func TestRenderBackgroundIsCream(t *testing.T) {
	s := New(160, 100)
	buf := newBuf(s)
	Render(s, buf)
	// y < RowPad: above the first row, guaranteed pure background.
	x, y := 80, 2
	off := (y*s.W + x) * 4
	if buf[off] != 0xf2 || buf[off+1] != 0xee || buf[off+2] != 0xe4 || buf[off+3] != 0xFF {
		t.Fatalf("bg pixel = (%d,%d,%d,%d), want (242,238,228,255)",
			buf[off], buf[off+1], buf[off+2], buf[off+3])
	}
}

// The first row's icon tile must be the amber "selected folder" colour.
func TestRenderFirstRowIsAmber(t *testing.T) {
	s := New(200, 80)
	buf := newBuf(s)
	Render(s, buf)
	// Center of the first row's icon tile.
	x := 10 + IconSize/2
	y := RowPad + IconSize/2
	off := (y*s.W + x) * 4
	if buf[off] != 0xc8 || buf[off+1] != 0x9a || buf[off+2] != 0x4c {
		t.Fatalf("row 0 icon = (%d,%d,%d), want (200,154,76)",
			buf[off], buf[off+1], buf[off+2])
	}
}

// Subsequent rows' tiles must use the paler "unselected" tone — exercises
// the idx > 0 branch of drawRow.
func TestRenderSecondRowIsPale(t *testing.T) {
	s := New(200, 100) // tall enough for at least two rows
	buf := newBuf(s)
	Render(s, buf)
	x := 10 + IconSize/2
	y := RowPad + RowHeight + IconSize/2
	off := (y*s.W + x) * 4
	if buf[off] != 0xb8 || buf[off+1] != 0xb0 || buf[off+2] != 0x9a {
		t.Fatalf("row 1 icon = (%d,%d,%d), want (184,176,154)",
			buf[off], buf[off+1], buf[off+2])
	}
}

// A narrow surface (where the name-strip length would go negative) clamps
// to zero and still renders without panicking.
func TestRenderNarrowSurfaceClamps(t *testing.T) {
	s := New(20, 60) // 10+IconSize+8 = 32 > 20-12 = 8 -> stripLen would be negative
	buf := newBuf(s)
	Render(s, buf) // must not panic
}

// setPixel out-of-bounds is a silent no-op.
func TestSetPixelOutOfBounds(t *testing.T) {
	s := New(4, 4)
	buf := newBuf(s)
	setPixel(s, buf, -1, 0, [3]uint8{1, 2, 3})
	setPixel(s, buf, 0, -1, [3]uint8{1, 2, 3})
	setPixel(s, buf, 4, 0, [3]uint8{1, 2, 3})
	setPixel(s, buf, 0, 4, [3]uint8{1, 2, 3})
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("OOB setPixel leaked into buffer")
		}
	}
}

// New stores the dimensions verbatim.
func TestNewStoresDims(t *testing.T) {
	s := New(11, 9)
	if s.W != 11 || s.H != 9 {
		t.Fatalf("New(11,9) = %+v", s)
	}
}
