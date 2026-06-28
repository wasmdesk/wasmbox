// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene drives the terminal client: a Grid (character buffer + cursor),
// a Shell (line-editor + builtin dispatch wired to a clients/sharedvfs VFS),
// and a renderer that rasterises the Grid into the SAB pixel buffer the
// compositor blits.
//
// The terminal is a real (if tiny) interactive shell: typing printable ASCII
// extends the edit line, Backspace shrinks it, Enter executes via Shell, and
// the shell's output lines are painted into the Grid above a fresh prompt.
// File commands (cat / cd / mkdir / touch / rm / ls / echo > path) speak the
// SAME VFS the files browser paints, so writes from the terminal show up in
// the file browser (and survive page reloads when the VFS is IDB-backed).
//
// Everything in this file is pure Go (no syscall/js) so the layer is unit-
// testable on any host. The wasm entry point (main.go) feeds keystrokes in
// through HandleKey() and pulls bytes out through Render() every frame.
package scene

import (
	"strings"

	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

// State is the top-of-package handle the wasm entry point holds. It carries
// the surface size, the character grid, and the shell. Surface size is fixed
// at construction time -- the compositor never re-grants a different surface
// after welcome -- so we do not auto-resize.
type State struct {
	W, H  int
	Grid  *Grid
	Shell *Shell
}

// New constructs a State for a width x height pixel surface backed by a
// freshly seeded demo InMemoryVFS. Tests + the non-wasm host path use this.
// The wasm boot path reaches for NewWithVFS to hand in an IDB-backed VFS so
// the terminal observes the same tree as the files browser.
func New(width, height int) *State {
	v := sharedvfs.NewInMemoryVFS()
	sharedvfs.SeedDemoTree(v)
	return NewWithVFS(width, height, v)
}

// NewWithVFS constructs a State whose Shell talks to the supplied VFS. Used
// by the wasm boot path to share an IDB-backed VFS with the files browser
// (and by tests that want a deterministic empty tree).
func NewWithVFS(width, height int, v sharedvfs.VFS) *State {
	cols := width / (FontW * 2)
	rows := height / (FontH * 2)
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	s := &State{
		W:     width,
		H:     height,
		Grid:  NewGrid(cols, rows),
		Shell: NewShellWithVFS(v),
	}
	s.writePrompt()
	return s
}

// Render fills buf (a 4*W*H byte slice, RGBA32 row-major) with the current
// grid. Panics on size mismatch -- a misshaped buffer is a caller bug.
func Render(s *State, buf []byte) {
	if len(buf) != 4*s.W*s.H {
		panic("scene: Render buffer size mismatch")
	}
	PaintGrid(buf, s.W, s.H, s.Grid)
}

// HandleKey routes one DOM-style keydown into the shell. Recognised keys:
//   - single printable ASCII (key.length == 1): append + echo into the grid.
//   - "Enter":     execute the line, paint output, render fresh prompt.
//   - "Backspace": pop one byte off the line + reverse-cursor the grid.
//   - anything else (arrows, modifiers): ignored.
//
// Returns true when the grid changed, so the caller can decide whether to
// re-render. We chose key-string dispatch over keycodes because the SDK
// already normalises e.key to the printable character for printable keys.
func (s *State) HandleKey(key string) bool {
	switch key {
	case "":
		return false
	case "Enter":
		s.Grid.CRLF()
		line := string(s.Shell.Line)
		s.Shell.Line = s.Shell.Line[:0]
		if IsClear(line) {
			s.Grid.Clear()
			s.writePrompt()
			return true
		}
		out := s.Shell.Execute(line)
		for _, ln := range out {
			s.Grid.PrintString(ln)
			s.Grid.CRLF()
		}
		s.writePrompt()
		return true
	case "Backspace":
		if len(s.Shell.Line) == 0 {
			return false
		}
		s.Shell.Line = s.Shell.Line[:len(s.Shell.Line)-1]
		s.Grid.Backspace()
		return true
	default:
		if len(key) == 1 {
			b := key[0]
			if b >= 0x20 && b <= 0x7E {
				s.Shell.Line = append(s.Shell.Line, b)
				s.Grid.Print(b)
				return true
			}
		}
		return false
	}
}

// writePrompt paints the shell's prompt into the grid at the current cursor.
// The prompt is composed of the cwd (in cyan, so it stands out as a path)
// followed by the trailing prompt string (also cyan, matching the previous
// `$ ` look). Tinting both keeps the line visually distinct from echoed
// input which lands in the default ink.
func (s *State) writePrompt() {
	prev := s.Grid.FG
	s.Grid.FG = 1 // cyan
	p := s.Shell.PromptString()
	for i := 0; i < len(p); i++ {
		s.Grid.Print(p[i])
	}
	s.Grid.FG = prev
}

// ExecuteForTest is a thin re-export of Shell.Execute that lets the scene_test
// harness drive the shell without reaching into the Shell field. Kept as a
// method on *State (not free function) so the doc shows up next to State.
func (s *State) ExecuteForTest(line string) []string {
	return s.Shell.Execute(strings.TrimRight(line, "\n"))
}
