// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package clipboard is a go-widgets toolkit.Clipboard over the wasmbox
// clipboard protocol (docs/protocol.md, "Clipboard"). A client runs in a
// worker behind the compositor and cannot reach the browser's clipboard, so:
//
//   - Copy: SetClipboardText keeps the text and hands it to the function given
//     to New, which sends a `clipboard_write` message (sdk.js writeClipboard);
//     the compositor passes it to the page, which writes the browser's
//     clipboard.
//   - Paste: the page reads the browser's clipboard when the user presses a
//     paste shortcut, and the compositor sends this window an input event
//     { kind: "paste", text } BEFORE the shortcut's own keydown. Deliver
//     records that text, so the widget's own paste (a terminal's Meta+V or
//     Ctrl+Shift+V, which reads ClipboardText) finds what the user copied.
//
// Install it once at startup with toolkit.SetClipboard (Install does, in the
// wasm build). The model is go-widgets/webcanvas's clipboard bridge; the
// difference is the transport.
package clipboard

import (
	"sync"
	"unicode/utf8"
)

// MaxBytes bounds the text the clipboard keeps, in bytes: a paste is the
// user's, but a widget must not be handed an unbounded string. The page and
// the compositor apply the same bound.
const MaxBytes = 1 << 20

// Clipboard is a text clipboard. It has the method set of toolkit.Clipboard.
// The zero value is an in-memory clipboard (a copy stays in the window).
type Clipboard struct {
	mu    sync.Mutex
	text  string
	write func(string)
}

// New returns a clipboard that sends every copied text to write (nil: keep
// it in memory only).
func New(write func(string)) *Clipboard { return &Clipboard{write: write} }

// ClipboardText returns the current text: the last paste delivered, or the
// last text copied.
func (c *Clipboard) ClipboardText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text
}

// SetClipboardText replaces the text and sends it to the page's clipboard.
func (c *Clipboard) SetClipboardText(s string) {
	s = Bound(s)
	c.mu.Lock()
	c.text = s
	w := c.write
	c.mu.Unlock()
	if w != nil {
		w(s)
	}
}

// Deliver records the text of a `paste` input event. It is not written back
// to the browser (it came from there).
func (c *Clipboard) Deliver(s string) {
	s = Bound(s)
	c.mu.Lock()
	c.text = s
	c.mu.Unlock()
}

// Bound cuts s to MaxBytes on a rune boundary.
func Bound(s string) string {
	if len(s) <= MaxBytes {
		return s
	}
	i := MaxBytes
	// s[i] is the byte just past the kept text: back up until it starts a rune,
	// at most utf8.UTFMax-1 steps for valid UTF-8.
	for k := 0; k < utf8.UTFMax-1 && !utf8.RuneStart(s[i]); k++ {
		i--
	}
	return s[:i]
}
