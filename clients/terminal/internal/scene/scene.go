// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene paints the terminal placeholder client's surface: a dark
// terminal-looking panel with a few static lines of monospaced-style text
// ("prompt + caret"). It is pure Go (no syscall/js, no cgo) so the painter
// builds for any architecture and is unit-tested natively.
//
// The terminal client is a recognizably-titled placeholder spawned by the
// dock's "terminal" launch id. A full TTY is deliberately out of scope here;
// this just establishes that the dock's launch path produces a distinct
// "Terminal" window rather than a generic hello placeholder.
package scene

// State carries the surface geometry. The terminal placeholder has no mutable
// content yet (it does not echo input), so the type stays small — future TTY
// work would put scrollback / cursor state here.
type State struct {
	W, H int
}

// New makes a State for a surface of width x height pixels.
func New(width, height int) *State {
	return &State{W: width, H: height}
}

// Render fills buf (a 4*W*H byte slice, RGBA32 row-major) with the terminal
// surface. Panics on a size mismatch — a misshaped buffer in the caller is a
// bug, not a recoverable state.
func Render(s *State, buf []byte) {
	need := 4 * s.W * s.H
	if len(buf) != need {
		panic("scene: buffer size mismatch")
	}
	// Solid dark background — the canonical "terminal" colour.
	fill(s, buf, 0x10, 0x14, 0x1c)
	// Prompt line: "> _" near the top-left, drawn as small pixel runs.
	drawPrompt(s, buf, 10, 14)
}

// fill paints every pixel of buf with the given opaque RGB. Pure inner loop;
// the hello scene uses a gradient, this client wants a flat background.
func fill(s *State, buf []byte, r, g, b uint8) {
	for i := 0; i+3 < len(buf); i += 4 {
		buf[i] = r
		buf[i+1] = g
		buf[i+2] = b
		buf[i+3] = 0xFF
	}
}

// drawPrompt draws the chevron + underscore caret at (x, y). The shapes are
// intentionally small (5-7 pixels) so the result reads as a terminal prompt
// at a glance without requiring a font renderer.
func drawPrompt(s *State, buf []byte, x, y int) {
	ink := [3]uint8{0x9b, 0xe5, 0x9b} // soft green, classic terminal-on-black
	// ">" chevron: two diagonals meeting at +5,+3.
	for i := 0; i < 4; i++ {
		setPixel(s, buf, x+i, y+i, ink)
		setPixel(s, buf, x+i, y+6-i, ink)
	}
	// Underscore cursor 2px below.
	for i := 0; i < 8; i++ {
		setPixel(s, buf, x+8+i, y+6, ink)
	}
}

// setPixel writes an opaque RGB at (x, y) if in bounds. Out-of-bounds writes
// are silently ignored so the caller does not need to clip every coordinate.
func setPixel(s *State, buf []byte, x, y int, rgb [3]uint8) {
	if x < 0 || y < 0 || x >= s.W || y >= s.H {
		return
	}
	off := (y*s.W + x) * 4
	buf[off] = rgb[0]
	buf[off+1] = rgb[1]
	buf[off+2] = rgb[2]
	buf[off+3] = 0xFF
}
