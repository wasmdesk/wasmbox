// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene paints the files placeholder client's surface: a light panel
// with a simulated list of file entries (rows of icon + name strips). Pure
// Go, no syscall/js, no cgo, so the painter builds for any architecture and
// is unit-tested natively.
//
// The files client is a recognizably-titled placeholder spawned by the
// dock's "files" launch id. A real file browser is out of scope; this just
// establishes that the launch path produces a distinct "Files" window.
package scene

// State carries the surface geometry plus the placeholder entry count. The
// list is computed from the height so the painter naturally fills the
// surface — a future real file-manager would put live entries here.
type State struct {
	W, H int
}

// New makes a State for a surface of width x height pixels.
func New(width, height int) *State {
	return &State{W: width, H: height}
}

// Visual constants. Kept exported so tests can pin layout invariants.
const (
	// RowHeight is the vertical pitch of a placeholder entry row.
	RowHeight = 22
	// RowPad is the top inset before the first row.
	RowPad = 8
	// IconSize is the side length of the per-row tile.
	IconSize = 14
)

// Render fills buf (a 4*W*H byte slice, RGBA32 row-major) with the files
// surface. Panics on a size mismatch — a misshaped buffer in the caller is
// a bug, not a recoverable state.
func Render(s *State, buf []byte) {
	need := 4 * s.W * s.H
	if len(buf) != need {
		panic("scene: buffer size mismatch")
	}
	// Light panel background — a faint warm cream so the result reads as a
	// file browser at a glance against the dark desktop.
	fill(s, buf, 0xf2, 0xee, 0xe4)
	rows := (s.H - RowPad) / RowHeight
	for r := 0; r < rows; r++ {
		y := RowPad + r*RowHeight
		drawRow(s, buf, y, r)
	}
}

// fill paints every pixel of buf with the given opaque RGB. Mirrors the
// terminal client's helper — kept separate so the two scene packages are
// fully independent (each builds + tests on its own).
func fill(s *State, buf []byte, r, g, b uint8) {
	for i := 0; i+3 < len(buf); i += 4 {
		buf[i] = r
		buf[i+1] = g
		buf[i+2] = b
		buf[i+3] = 0xFF
	}
}

// drawRow paints one placeholder entry at vertical offset y. The icon tile
// uses a folder-like amber for the first entry and a paler tone for the
// rest, so the first row reads as a "selected" item.
func drawRow(s *State, buf []byte, y, idx int) {
	// Icon tile.
	tile := [3]uint8{0xc8, 0x9a, 0x4c}
	if idx > 0 {
		tile = [3]uint8{0xb8, 0xb0, 0x9a}
	}
	for dy := 0; dy < IconSize; dy++ {
		for dx := 0; dx < IconSize; dx++ {
			setPixel(s, buf, 10+dx, y+dy, tile)
		}
	}
	// "Name" strip: a thin horizontal bar to the right of the icon.
	strip := [3]uint8{0x55, 0x4a, 0x38}
	stripY := y + IconSize/2 - 1
	stripLen := s.W - (10 + IconSize + 8) - 12
	if stripLen < 0 {
		stripLen = 0
	}
	for dx := 0; dx < stripLen; dx++ {
		setPixel(s, buf, 10+IconSize+8+dx, stripY, strip)
		setPixel(s, buf, 10+IconSize+8+dx, stripY+1, strip)
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
