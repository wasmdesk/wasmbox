// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene drives the terminal client. The terminal itself is
// go-widgets/shellterm, shared with the go-wasmcontainers dashboard:
//
//   - a toolkit.Terminal draws the screen (escape sequences, colours,
//     scrollback, selection, copy and paste);
//   - a shellterm.Session is the model: a go-widgets/lineedit line editor
//     (cursor motion, kill/yank, history, Tab completion, Ctrl-R) in front of
//     a Shell;
//   - Shell (shell.go) is this client's part: shellparse + the coreutils
//     multicall over the shared VFS, with its Tab completion in completer.go.
//
// Everything in this package is pure Go (no syscall/js), so it is unit-
// testable on any host. The wasm entry point (main.go) feeds keystrokes in
// through HandleKey, runs the Post queue through Drain, and pulls bytes out
// through Render.
//
// Threads: shellterm runs keys and commands on goroutines of its own and hands
// everything the view must show to Options.Post, which State queues; Drain
// runs the queue on the UI goroutine. A mutex serialises Drain, HandleKey and
// Render, so the Terminal is only ever touched by one at a time.
package scene

import (
	"sync"

	"github.com/go-widgets/painter"
	"github.com/go-widgets/shellterm"
	"github.com/go-widgets/toolkit"
	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

// State is the top-of-package handle the wasm entry point holds. Surface size
// is fixed at construction time -- the compositor never re-grants a different
// surface after welcome -- so we do not auto-resize.
type State struct {
	W, H    int
	Term    *toolkit.Terminal
	Shell   *Shell
	Session *shellterm.Session

	theme *toolkit.Theme

	ui      sync.Mutex // serialises Drain, HandleKey, HandleMouse, Render
	pressed bool       // a mouse press (a selection drag) is in progress

	qmu   sync.Mutex
	queue []func()
	wake  chan struct{}
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

// NewWithVFS constructs a State whose Shell talks to the supplied VFS (used
// by the wasm boot path to share an IDB-backed VFS with the files browser,
// and by tests that want a deterministic tree).
func NewWithVFS(width, height int, v sharedvfs.VFS) *State {
	// The cell size is fixed (cellW×cellH), so the grid dimensions fall straight
	// out of the surface size -- the SAME arithmetic the previous renderer used
	// (width/16 × height/16), which keeps the column/row/pitch geometry the
	// pixel-calibrated probes expect.
	cols, rows := max(width/cellW, 1), max(height/cellH, 1)
	term := newTerminal(cols, rows)

	// Centre the grid in the surface. The margin outside the view's bounds is
	// painted with the panel background in Render, so the whole surface reads as
	// one terminal panel.
	gridW, gridH := cols*cellW, rows*cellH
	term.SetBounds(toolkit.Rect{X: (width - gridW) / 2, Y: (height - gridH) / 2, W: gridW, H: gridH})

	s := &State{
		W: width, H: height,
		Term:  term,
		Shell: NewShellWithVFS(v),
		theme: newTheme(),
		wake:  make(chan struct{}, 1),
	}
	s.Session = shellterm.New(shellterm.Options{Post: s.post})
	s.Session.SetShell(s.Shell, s.Shell.Completer())
	// Wire the Terminal and the session together: typed bytes go to the
	// session, its output comes back through the Post queue. The grid was
	// built at its final size, so the width is told explicitly.
	shellterm.Bind(term, s.Session, nil)
	s.Session.Resize(cols)
	return s
}

// post queues f for Drain and wakes whoever waits on Wake. It never blocks.
func (s *State) post(f func()) {
	s.qmu.Lock()
	s.queue = append(s.queue, f)
	s.qmu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Wake is signalled when the session queued something for Drain.
func (s *State) Wake() <-chan struct{} { return s.wake }

// Drain runs what the session queued, on the calling (UI) goroutine, and
// reports how many functions ran -- when it is non-zero the screen changed and
// the caller should render.
func (s *State) Drain() int {
	n := 0
	for {
		s.qmu.Lock()
		q := s.queue
		s.queue = nil
		s.qmu.Unlock()
		if len(q) == 0 {
			return n
		}
		s.ui.Lock()
		for _, f := range q {
			f()
		}
		s.ui.Unlock()
		n += len(q)
	}
}

// Render fills buf (a 4*W*H byte slice, RGBA32 row-major) with the current
// screen. Panics on size mismatch -- a misshaped buffer is a caller bug. The
// panel background is painted into the four margin strips around the centred
// grid (the Terminal's own Draw fills its bounds), so the whole surface reads
// as one terminal panel without redundantly filling the grid area twice.
func Render(s *State, buf []byte) {
	if len(buf) != 4*s.W*s.H {
		panic("scene: Render buffer size mismatch")
	}
	s.ui.Lock()
	defer s.ui.Unlock()
	p := painter.NewPixelPainter(buf, s.W, s.H)
	b := s.Term.Bounds()
	// Top, bottom, left, right margins (any of which may be empty).
	fillBox(p, toolkit.Rect{X: 0, Y: 0, W: s.W, H: b.Y}, panelBG)
	fillBox(p, toolkit.Rect{X: 0, Y: b.Y + b.H, W: s.W, H: s.H - (b.Y + b.H)}, panelBG)
	fillBox(p, toolkit.Rect{X: 0, Y: b.Y, W: b.X, H: b.H}, panelBG)
	fillBox(p, toolkit.Rect{X: b.X + b.W, Y: b.Y, W: s.W - (b.X + b.W), H: b.H}, panelBG)
	s.Term.Draw(p, s.theme)
}

// Key is one keydown as the compositor relays it: the DOM key plus the
// modifier state.
type Key struct {
	Key                    string // KeyboardEvent.key: "a", "Enter", "ArrowUp", ...
	Ctrl, Meta, Alt, Shift bool
}

// HandleKey routes one keydown into the terminal and reports whether the
// caller should re-render (the output the key causes arrives later, through
// Wake/Drain). A printable key without Ctrl or Meta is typed text; anything
// else is a key press, which the Terminal turns into what a terminal sends
// (Enter, arrows, Ctrl-C, Alt-b ...) or acts on itself (copy, paste, Shift+
// PageUp scrollback). Meta+V and Ctrl+Shift+V paste, Meta+C and Ctrl+Shift+C
// copy the selection; a plain Ctrl+V is ^V for the shell.
func (s *State) HandleKey(k Key) bool {
	if k.Key == "" {
		return false
	}
	ev := toolkit.Event{Kind: toolkit.EventKeyDown, Code: k.Key, Ctrl: k.Ctrl, Meta: k.Meta, Alt: k.Alt, Shift: k.Shift}
	if len([]rune(k.Key)) == 1 && !k.Ctrl && !k.Meta {
		ev = toolkit.Event{Kind: toolkit.EventChar, Code: k.Key, Alt: k.Alt}
	}
	s.ui.Lock()
	defer s.ui.Unlock()
	s.Term.OnEvent(ev)
	return true
}

// HandleMouse routes a mouse event (kind "mousedown", "mousemove", "mouseup",
// or "wheel"), with x, y relative to the window body, into the Terminal:
// press and drag select text, the wheel scrolls the history. dy is the wheel's
// vertical delta. A "mousemove" without a press in progress changes nothing
// (and reports false, so a hovering pointer costs no repaint).
// It reports whether the screen may have changed.
func (s *State) HandleMouse(kind string, x, y int, dy float64) bool {
	s.ui.Lock()
	defer s.ui.Unlock()
	b := s.Term.Bounds()
	ev := toolkit.Event{X: x - b.X, Y: y - b.Y}
	switch kind {
	case "mousedown":
		ev.Kind = toolkit.EventClick
		s.pressed = true
	case "mousemove":
		if !s.pressed {
			return false
		}
		ev.Kind = toolkit.EventMouseDrag
	case "mouseup":
		ev.Kind = toolkit.EventMouseUp
		s.pressed = false
	case "wheel":
		// A downward wheel (dy > 0) moves toward the live bottom.
		switch {
		case dy > 0:
			ev.Kind, ev.Delta = toolkit.EventScroll, wheelLines
		case dy < 0:
			ev.Kind, ev.Delta = toolkit.EventScroll, -wheelLines
		default:
			return false
		}
	default:
		return false
	}
	s.Term.OnEvent(ev)
	return true
}

// wheelLines is how many lines one wheel notch scrolls.
const wheelLines = 3
