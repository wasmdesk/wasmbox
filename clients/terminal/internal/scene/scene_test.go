// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import "testing"

func newBuf(s *State) []byte { return make([]byte, 4*s.W*s.H) }

// Render must fill an exactly-sized buffer without panicking.
func TestRenderFillsExactSize(t *testing.T) {
	s := New(160, 100)
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

// The background must be the canonical dark-terminal colour: a sampled
// non-prompt pixel reads (0x10, 0x14, 0x1c, 0xff).
func TestRenderBackgroundIsDark(t *testing.T) {
	s := New(80, 60)
	buf := newBuf(s)
	Render(s, buf)
	// Sample near the bottom-right, far from the prompt at (10,14).
	x, y := 60, 50
	off := (y*s.W + x) * 4
	if buf[off] != 0x10 || buf[off+1] != 0x14 || buf[off+2] != 0x1c || buf[off+3] != 0xFF {
		t.Fatalf("bg pixel = (%d,%d,%d,%d), want (16,20,28,255)",
			buf[off], buf[off+1], buf[off+2], buf[off+3])
	}
}

// The prompt must lay down green-dominant ink (the chevron + caret). The
// terminal ink is (0x9b, 0xe5, 0x9b): G strictly greater than R and B, so a
// "G > R" / "G > B" sample inside the prompt region pins the ink colour
// without depending on the exact glyph layout.
func TestRenderHasGreenPromptInk(t *testing.T) {
	s := New(120, 80)
	buf := newBuf(s)
	Render(s, buf)
	// Scan a window around the prompt origin (10,14).
	found := false
	for y := 12; y < 24 && !found; y++ {
		for x := 8; x < 32 && !found; x++ {
			off := (y*s.W + x) * 4
			if buf[off+1] > buf[off] && buf[off+1] > buf[off+2] {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("no green-dominant prompt pixel found")
	}
}

// setPixel out-of-bounds is a no-op (no panic, no write).
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

// setPixel inside the surface writes the exact RGB + opaque alpha.
func TestSetPixelInBounds(t *testing.T) {
	s := New(4, 4)
	buf := newBuf(s)
	setPixel(s, buf, 1, 2, [3]uint8{0xAA, 0xBB, 0xCC})
	off := (2*s.W + 1) * 4
	if buf[off] != 0xAA || buf[off+1] != 0xBB || buf[off+2] != 0xCC || buf[off+3] != 0xFF {
		t.Fatalf("in-bounds setPixel = (%d,%d,%d,%d)", buf[off], buf[off+1], buf[off+2], buf[off+3])
	}
}

// drawPrompt off the surface (negative origin) leaves the buffer untouched
// because every setPixel falls out of bounds.
func TestDrawPromptOffSurface(t *testing.T) {
	s := New(4, 4)
	buf := newBuf(s)
	// Origin far enough negative that every drawn pixel is OOB.
	drawPrompt(s, buf, -100, -100)
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("OOB drawPrompt leaked into buffer")
		}
	}
}

// New stores the dimensions verbatim.
func TestNewStoresDims(t *testing.T) {
	s := New(13, 7)
	if s.W != 13 || s.H != 7 {
		t.Fatalf("New(13,7) = %+v", s)
	}
}
