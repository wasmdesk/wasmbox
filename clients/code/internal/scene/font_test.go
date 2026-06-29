// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import "testing"

func TestGlyph_KnownCode(t *testing.T) {
	g := Glyph('A')
	if g == [8]byte{} {
		t.Fatal("A glyph should not be empty")
	}
}

func TestGlyph_UnknownFallsBackToBlock(t *testing.T) {
	g := Glyph(0x10) // unknown
	want := [8]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	if g != want {
		t.Errorf("unknown fallback: %v, want solid block", g)
	}
}

func TestGlyph_SpaceIsEmpty(t *testing.T) {
	g := Glyph(' ')
	if g != [8]byte{} {
		t.Fatalf("space glyph: %v", g)
	}
}
