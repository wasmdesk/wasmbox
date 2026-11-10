// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-widgets/toolkit"
	"github.com/wasmdesk/wasmbox/clients/clipboard"
	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

// newSceneWithVFS builds a 640x400 scene over a VFS seeded by seed.
func newSceneWithVFS(t *testing.T, seed func(v sharedvfs.VFS)) *State {
	t.Helper()
	v := sharedvfs.NewInMemoryVFS()
	if seed != nil {
		seed(v)
	}
	return NewWithVFS(640, 400, v)
}

// screen is the visible text of the terminal, one line per row.
func screen(s *State) string {
	s.ui.Lock()
	defer s.ui.Unlock()
	return s.Term.A11y().Value
}

// waitFor drains the session's queue until cond holds or two seconds pass.
func waitFor(t *testing.T, s *State, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.Drain()
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; screen:\n%s", what, screen(s))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitScreen waits for the screen to contain want.
func waitScreen(t *testing.T, s *State, want string) {
	t.Helper()
	waitFor(t, s, "screen to contain "+want, func() bool { return strings.Contains(screen(s), want) })
}

// idle waits until no command is running (a paste while one runs is dropped).
func idle(t *testing.T, s *State) {
	t.Helper()
	waitFor(t, s, "the command to finish", func() bool { return !s.Session.Running.Get() })
}

func key(s *State, k string) { s.HandleKey(Key{Key: k}) }

// typeText types text, with "\n" as Enter.
func typeText(s *State, text string) {
	for _, r := range text {
		if r == '\n' {
			key(s, "Enter")
		} else {
			key(s, string(r))
		}
	}
}

// run types a command line and waits for it to finish.
func run(t *testing.T, s *State, line string) {
	t.Helper()
	typeText(s, line+"\n")
	waitFor(t, s, "the command to start", func() bool { return true })
	// A command starts on the session's goroutine: wait for the prompt to
	// come back after it (the line has been echoed by then).
	idle(t, s)
	time.Sleep(20 * time.Millisecond)
	s.Drain()
	idle(t, s)
}

func TestNewSizesGrid(t *testing.T) {
	s := New(640, 400)
	cols, rows := s.Term.Screen().Size()
	if cols != 40 || rows != 25 {
		t.Fatalf("grid = %dx%d, want 40x25", cols, rows)
	}
	if s.W != 640 || s.H != 400 {
		t.Fatalf("surface = %dx%d", s.W, s.H)
	}
	if b := s.Term.Bounds(); b.X != 0 || b.Y != 0 || b.W != 640 || b.H != 400 {
		t.Fatalf("bounds = %+v, want the whole surface", b)
	}
}

func TestNewClampsToMinimum(t *testing.T) {
	s := New(1, 1)
	cols, rows := s.Term.Screen().Size()
	if cols != 1 || rows != 1 {
		t.Fatalf("grid = %dx%d, want 1x1", cols, rows)
	}
}

// A surface that is not a multiple of the cell is centred; the margin is panel.
func TestRenderFillsMargin(t *testing.T) {
	s := New(650, 410) // 40x25 cells = 640x400, leaving 10px on each axis
	buf := make([]byte, 4*s.W*s.H)
	Render(s, buf)
	for _, xy := range [][2]int{{0, 0}, {649, 409}, {2, 200}, {647, 200}, {300, 2}, {300, 407}} {
		if got := pix(buf, s.W, xy[0], xy[1]); got != panelBG {
			t.Errorf("margin pixel %v = %+v, want panel %+v", xy, got, panelBG)
		}
	}
}

func TestRenderPanicsOnSizeMismatch(t *testing.T) {
	s := New(64, 64)
	defer func() {
		if recover() == nil {
			t.Fatal("Render with a short buffer did not panic")
		}
	}()
	Render(s, make([]byte, 10))
}

func pix(buf []byte, w, x, y int) toolkit.RGBA {
	o := (y*w + x) * 4
	return toolkit.RGBA{R: buf[o], G: buf[o+1], B: buf[o+2], A: buf[o+3]}
}

// count counts the pixels of colour c in the surface.
func count(s *State, c toolkit.RGBA) int {
	buf := make([]byte, 4*s.W*s.H)
	Render(s, buf)
	n := 0
	for y := 0; y < s.H; y++ {
		for x := 0; x < s.W; x++ {
			if pix(buf, s.W, x, y) == c {
				n++
			}
		}
	}
	return n
}

func TestBuildMonoFaceFallbackOnBadBlob(t *testing.T) {
	if f := buildMonoFace([]byte("not a font"), termFontPx); f == nil {
		t.Fatal("buildMonoFace returned nil on a bad blob; want bitmap fallback")
	}
	if f := termFace(); f == nil || f.Advance() <= 0 {
		t.Fatalf("termFace() = %v, want a usable monospace face", f)
	}
}

// The look is declared through the Terminal: a 16x16 cell, not the face's own.
func TestCellSizePinned(t *testing.T) {
	s := New(640, 400)
	if s.Term.CellWidth() != cellW || s.Term.CellHeight() != cellH {
		t.Fatalf("cell = %dx%d, want %dx%d", s.Term.CellWidth(), s.Term.CellHeight(), cellW, cellH)
	}
	f := termFace()
	if s.Term.CellWidth() == f.Advance() && s.Term.CellHeight() == f.Height() {
		t.Fatal("cell size collapsed to raw font metrics; SetCellSize override lost")
	}
}

func TestFaceIsMonospace(t *testing.T) {
	f := termFace()
	if f.Measure("iiii") != f.Measure("MMMM") {
		t.Fatalf("non-monospace face: Measure(iiii)=%d != Measure(MMMM)=%d", f.Measure("iiii"), f.Measure("MMMM"))
	}
}

func TestPromptShownAtStart(t *testing.T) {
	s := New(640, 400)
	if got := screen(s); !strings.HasPrefix(got, "/ $") {
		t.Fatalf("no prompt on the fresh screen: %q", got)
	}
	if n := count(s, inkCyan); n == 0 {
		t.Fatal("the prompt is not painted in cyan")
	}
}

func TestTypedTextAndEcho(t *testing.T) {
	s := New(640, 400)
	typeText(s, "echo hello")
	waitScreen(t, s, "$ echo hello")
	key(s, "Enter")
	waitFor(t, s, "hello on its own row", func() bool {
		return strings.Contains(screen(s), "\nhello\n") || strings.Contains(screen(s), "\nhello ")
	})
	if n := count(s, inkGreen); n < 200 {
		t.Fatalf("echo output painted %d ink pixels, want >= 200", n)
	}
}

func TestBackspaceAndEmptyLine(t *testing.T) {
	s := New(640, 400)
	typeText(s, "echo xy")
	key(s, "Backspace")
	waitScreen(t, s, "$ echo x")
	if strings.Contains(screen(s), "echo xy") {
		t.Fatal("Backspace did not erase the last character")
	}
	if s.HandleKey(Key{}) {
		t.Fatal("an empty key reported a change")
	}
}

func TestLeftArrowAndInsertEdit(t *testing.T) {
	s := New(640, 400)
	typeText(s, "echo ac")
	key(s, "ArrowLeft")
	typeText(s, "b")
	waitScreen(t, s, "$ echo abc")
}

func TestClearBuiltin(t *testing.T) {
	s := New(640, 400)
	run(t, s, "echo gone")
	waitScreen(t, s, "gone")
	run(t, s, "clear")
	waitFor(t, s, "the screen to clear", func() bool { return !strings.Contains(screen(s), "gone") })
	if !strings.HasPrefix(screen(s), "/ $") {
		t.Fatalf("no prompt after clear: %q", screen(s))
	}
}

// The prompt is the cwd and "$ " (the single source is Shell.Prompt): it
// follows a cd, and carries no status prefix (shellterm shows the status).
func TestPromptFollowsCwd(t *testing.T) {
	s := New(640, 400)
	run(t, s, "cd /Documents")
	waitFor(t, s, "the new prompt", func() bool { return strings.Contains(screen(s), "\n/Documents $") })
	run(t, s, "frobnicate")
	waitScreen(t, s, "[exit status 127]")
	waitFor(t, s, "the prompt after a failure", func() bool {
		return strings.HasSuffix(strings.TrimRight(screen(s), " \n"), "/Documents $")
	})
	if strings.Contains(screen(s), "[127]") {
		t.Fatal("the prompt carries a status prefix next to shellterm's status line")
	}
}

func TestShellPrompt(t *testing.T) {
	sh := newTestShell()
	if got := sh.Prompt(); got != "/ $ " {
		t.Fatalf("Prompt at root = %q", got)
	}
	sh.Cwd = "/Documents"
	sh.LastExit = 7
	if got := sh.Prompt(); got != "/Documents $ " {
		t.Fatalf("Prompt after cd = %q", got)
	}
}

func TestCommandExitStatusShown(t *testing.T) {
	s := New(640, 400)
	run(t, s, "frobnicate")
	waitScreen(t, s, "frobnicate: command not found")
	waitScreen(t, s, "[exit status 127]")
}

func TestTabCompletesCommand(t *testing.T) {
	s := New(640, 400)
	typeText(s, "ech")
	key(s, "Tab")
	waitScreen(t, s, "$ echo")
}

func TestTabCompletesFilename(t *testing.T) {
	s := newSceneWithVFS(t, func(v sharedvfs.VFS) {
		if err := v.Write("/zebra-stripes.txt", []byte("x")); err != nil {
			t.Fatal(err)
		}
	})
	typeText(s, "cat zeb")
	key(s, "Tab")
	waitScreen(t, s, "cat zebra-stripes.txt")
}

func TestTabListsAmbiguousMatches(t *testing.T) {
	s := newSceneWithVFS(t, func(v sharedvfs.VFS) {
		for _, p := range []string{"/aa.txt", "/ab.txt"} {
			if err := v.Write(p, []byte("x")); err != nil {
				t.Fatal(err)
			}
		}
	})
	typeText(s, "ls a")
	key(s, "Tab") // common prefix "a" adds nothing: the first Tab lists, as bash
	key(s, "Tab")
	waitFor(t, s, "both matches listed", func() bool {
		return strings.Contains(screen(s), "aa.txt") && strings.Contains(screen(s), "ab.txt")
	})
}

func TestHistoryRecall(t *testing.T) {
	s := New(640, 400)
	run(t, s, "echo one")
	run(t, s, "echo two")
	key(s, "ArrowUp")
	waitFor(t, s, "echo two recalled", func() bool { return strings.Count(screen(s), "echo two") == 2 })
	key(s, "ArrowUp")
	waitFor(t, s, "echo one recalled", func() bool { return strings.HasSuffix(strings.TrimRight(screen(s), " \n"), "echo one") })
}

// A command's escape sequences reach the screen: the red text is painted in
// the look's red, not as literal characters.
func TestEscapeSequencesColourOutput(t *testing.T) {
	s := New(640, 400)
	if n := count(s, inkRed); n != 0 {
		t.Fatalf("red ink on a fresh screen: %d", n)
	}
	run(t, s, `printf '\033[31mRED\033[0m plain\n'`)
	waitScreen(t, s, "RED plain")
	// The typed command line holds the text `\033[31m`; its OUTPUT must not.
	if strings.Contains(screen(s), "RED\x1b") || strings.Contains(screen(s), "\nRED plain\x1b") ||
		strings.Contains(screen(s), "\n[31mRED") {
		t.Fatalf("escape sequence shown literally: %q", screen(s))
	}
	if n := count(s, inkRed); n < 20 { // 30 exact-colour pixels today; the rest is anti-aliasing
		t.Fatalf("red text painted %d red pixels, want >= 20", n)
	}
}

func TestPasteShortcuts(t *testing.T) {
	clip := clipboard.New(nil)
	toolkit.SetClipboard(clip)
	defer toolkit.SetClipboard(nil)
	for _, k := range []Key{
		{Key: "V", Ctrl: true, Shift: true}, // Linux/Windows terminals
		{Key: "v", Meta: true},              // macOS
	} {
		s := New(640, 400)
		clip.Deliver("echo pasted")
		s.HandleKey(k)
		waitScreen(t, s, "$ echo pasted")
		key(s, "Enter")
		waitFor(t, s, "the pasted command's output", func() bool {
			return strings.Count(screen(s), "pasted") >= 2
		})
	}
}

// A plain Ctrl+V is not a paste: the shell gets ^V, the clipboard stays unread.
func TestPlainCtrlVIsNotPaste(t *testing.T) {
	clip := clipboard.New(nil)
	toolkit.SetClipboard(clip)
	defer toolkit.SetClipboard(nil)
	s := New(640, 400)
	clip.Deliver("never typed")
	s.HandleKey(Key{Key: "v", Ctrl: true})
	typeText(s, "x")
	waitScreen(t, s, "x")
	if strings.Contains(screen(s), "never typed") {
		t.Fatal("Ctrl+V pasted the clipboard")
	}
}

// A paste with a control character is cleaned by the Terminal: no escape
// sequence reaches the line editor.
func TestPasteStripsEscapes(t *testing.T) {
	clip := clipboard.New(nil)
	toolkit.SetClipboard(clip)
	defer toolkit.SetClipboard(nil)
	s := New(640, 400)
	clip.Deliver("echo a\x1b[31mb")
	s.HandleKey(Key{Key: "V", Ctrl: true, Shift: true})
	waitScreen(t, s, "echo a")
	if strings.Contains(screen(s), "\x1b") {
		t.Fatalf("escape reached the screen: %q", screen(s))
	}
}

// Selecting output with the mouse and pressing the copy shortcut sends the
// selected text to the clipboard (here, to the writer behind it).
func TestSelectAndCopy(t *testing.T) {
	var sent []string
	toolkit.SetClipboard(clipboard.New(func(s string) { sent = append(sent, s) }))
	defer toolkit.SetClipboard(nil)
	s := New(640, 400)
	run(t, s, "echo hello")
	waitFor(t, s, "hello output", func() bool { return strings.Contains(screen(s), "\nhello") })
	b := s.Term.Bounds()
	y := b.Y + cellH + cellH/2 // row 1: the output line
	s.HandleMouse("mousemove", b.X+10, y, 0)
	if len(sent) != 0 {
		t.Fatal("a hovering mouse selected something")
	}
	s.HandleMouse("mousedown", b.X+2, y, 0)
	s.HandleMouse("mousemove", b.X+4*cellW+2, y, 0)
	s.HandleMouse("mouseup", b.X+4*cellW+2, y, 0)
	s.HandleKey(Key{Key: "C", Ctrl: true, Shift: true})
	if len(sent) != 1 || sent[0] != "hello" {
		t.Fatalf("copied %q, want [hello]", sent)
	}
}

func TestWheelScrollsWithoutPanic(t *testing.T) {
	s := New(640, 400)
	for i := 0; i < 40; i++ {
		run(t, s, "echo line")
	}
	if !s.HandleMouse("wheel", 10, 10, -1) {
		t.Fatal("wheel up reported no change")
	}
	if s.Term.ScrollOffset().Get() == 0 {
		t.Fatal("wheel up did not scroll back")
	}
	s.HandleMouse("wheel", 10, 10, 1)
	if s.HandleMouse("wheel", 10, 10, 0) || s.HandleMouse("bogus", 0, 0, 0) {
		t.Fatal("a zero wheel / an unknown kind reported a change")
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	s := New(320, 160)
	a, b := make([]byte, 4*s.W*s.H), make([]byte, 4*s.W*s.H)
	Render(s, a)
	Render(s, b)
	if string(a) != string(b) {
		t.Fatal("two renders of an unchanged scene differ")
	}
}

// BenchmarkRenderScreenOfText is the keystroke cost: one full repaint of a
// 640x400 terminal whose screen is full of text. toolkit v0.362.1 no longer
// allocates once per cell in Terminal.Draw (toolkit#550); allocs/op stays flat.
func BenchmarkRenderScreenOfText(b *testing.B) {
	s := New(640, 400)
	line := strings.Repeat("the quick brown fox ", 2)[:39]
	for i := 0; i < 30; i++ {
		_, _ = s.Term.Write([]byte(line + "\r\n"))
	}
	buf := make([]byte, 4*s.W*s.H)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Render(s, buf)
	}
}

// BenchmarkKeystrokeRender: a keystroke (type a, erase it) each followed by a
// full Render, after the session's output has been drained.
func BenchmarkKeystrokeRender(b *testing.B) {
	s := New(640, 400)
	line := strings.Repeat("the quick brown fox ", 2)[:39]
	for i := 0; i < 30; i++ {
		_, _ = s.Term.Write([]byte(line + "\r\n"))
	}
	buf := make([]byte, 4*s.W*s.H)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.HandleKey(Key{Key: "a"})
		drainWait(s)
		Render(s, buf)
		s.HandleKey(Key{Key: "Backspace"})
		drainWait(s)
		Render(s, buf)
	}
}

// drainWait runs the session's queue once it has something. It yields while it
// waits: on js/wasm there is one thread and no preemption, so a spin without
// Gosched would starve the session goroutine it waits for.
func drainWait(s *State) {
	for s.Drain() == 0 {
		runtime.Gosched()
	}
}
