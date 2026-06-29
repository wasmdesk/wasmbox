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

	"github.com/wasmdesk/coreutils/multicall"
	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
	"github.com/wasmdesk/wasmbox/clients/terminal/internal/complete"
)

// localBuiltinsForCompletion is the set of pure-shell builtins (cd / clear /
// help) the shell handles locally. The completion command-position candidate
// set is this list UNIONed with multicall.Names() -- so Tab after a fresh
// prompt sees every command the user can actually run.
var localBuiltinsForCompletion = []string{"cd", "clear", "help"}

// completionBuiltins returns the merged + sorted list. We build it on every
// Tab rather than cache it because multicall.Names() is already O(N log N)
// over a tiny N and the resulting slice is small (~45 entries).
func completionBuiltins() []string {
	tools := multicall.Names()
	out := make([]string, 0, len(localBuiltinsForCompletion)+len(tools))
	out = append(out, localBuiltinsForCompletion...)
	out = append(out, tools...)
	// sort.Strings would pull a fresh import; reuse complete.LongestCommonPrefix
	// for sorting? no -- inline insertion sort over a small N is fine.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

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
	case "Tab":
		return s.handleTab()
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

// handleTab runs one Tab autocompletion against the current edit line. The
// cursor is always at len(Line) because the scene has no cursor-motion keys
// (left/right arrows do nothing); we still pass it explicitly so a future
// arrow-key wiring can pipe a real cursor through unchanged.
//
// Outcomes (mirroring bash with show-all-if-ambiguous):
//
//   - 0 matches: ring a soft "no change" (return false) -- the user sees
//     the line untouched.
//   - 1 match:   splice the match in place of the partial word, advancing
//     the grid cursor for each new byte.
//   - >1 matches AND longest common prefix extends beyond Target:
//     autocomplete to that prefix WITHOUT listing -- exactly what
//     `fo` -> `foo` looks like when matches are foo.txt + foobar.txt.
//   - >1 matches AND no further common prefix: print the candidates in
//     column-packed rows (Bash readline default), then re-paint the
//     prompt + the unchanged edit line so the user can keep typing.
func (s *State) handleTab() bool {
	line := string(s.Shell.Line)
	cursor := len(line)
	r := complete.Complete(line, cursor, completionBuiltins(), s.Shell.VFS, s.Shell.Cwd)
	switch len(r.Matches) {
	case 0:
		return false
	case 1:
		return s.extendLine(r.Matches[0][len(r.Target):])
	default:
		// Try to advance by the longest common prefix first. If the LCP is
		// strictly longer than the target the user has typed, just extend
		// the line up to that prefix -- bash does this silently before ever
		// printing the menu, and it dramatically reduces redraw noise when
		// the user is one keystroke away from disambiguating.
		lcp := complete.LongestCommonPrefix(r.Matches)
		if len(lcp) > len(r.Target) {
			return s.extendLine(lcp[len(r.Target):])
		}
		// LCP didn't make progress -- show the menu in column-packed form
		// then redraw the prompt + the unchanged edit line.
		s.Grid.CRLF()
		for _, row := range complete.FormatColumns(s.Grid.Cols, r.Matches) {
			s.Grid.PrintString(row)
			s.Grid.CRLF()
		}
		s.writePrompt()
		// Re-echo the unchanged edit line into the grid so the user sees
		// what they have typed so far -- the prompt above wrote only the
		// PromptString, not the line bytes.
		for i := 0; i < len(s.Shell.Line); i++ {
			s.Grid.Print(s.Shell.Line[i])
		}
		return true
	}
}

// extendLine appends add to Shell.Line and paints each new byte into the
// grid (advancing the cursor identically to a user typing the bytes). It is
// the shared core of both single-match autocompletion and the multi-match
// "advance by longest common prefix" path. Returns false when add is empty
// (no change to signal -- the caller still owes the user a no-op).
func (s *State) extendLine(add string) bool {
	if add == "" {
		return false
	}
	for i := 0; i < len(add); i++ {
		b := add[i]
		s.Shell.Line = append(s.Shell.Line, b)
		s.Grid.Print(b)
	}
	return true
}
