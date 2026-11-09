// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package clipboard

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-widgets/toolkit"
)

// The Clipboard must satisfy the toolkit's interface.
var _ toolkit.Clipboard = (*Clipboard)(nil)

func TestCopySendsToTheWriter(t *testing.T) {
	var sent []string
	c := New(func(s string) { sent = append(sent, s) })
	c.SetClipboardText("héllo")
	if got := c.ClipboardText(); got != "héllo" {
		t.Fatalf("ClipboardText = %q", got)
	}
	if len(sent) != 1 || sent[0] != "héllo" {
		t.Fatalf("writer got %q, want one write of héllo", sent)
	}
}

func TestDeliverIsNotWrittenBack(t *testing.T) {
	var sent []string
	c := New(func(s string) { sent = append(sent, s) })
	c.Deliver("from the browser")
	if got := c.ClipboardText(); got != "from the browser" {
		t.Fatalf("ClipboardText = %q", got)
	}
	if len(sent) != 0 {
		t.Fatalf("a delivered paste was written back: %q", sent)
	}
}

func TestZeroValueIsInMemory(t *testing.T) {
	var c Clipboard
	c.SetClipboardText("x")
	if c.ClipboardText() != "x" {
		t.Fatal("zero Clipboard lost its text")
	}
	if New(nil).ClipboardText() != "" {
		t.Fatal("fresh clipboard is not empty")
	}
}

func TestBoundCutsOnARuneBoundary(t *testing.T) {
	if got := Bound("short"); got != "short" {
		t.Fatalf("Bound(short) = %q", got)
	}
	// Three-byte runes straddling the limit: the cut must not split one.
	s := strings.Repeat("€", MaxBytes/3+10)
	got := Bound(s)
	if len(got) > MaxBytes || !utf8.ValidString(got) {
		t.Fatalf("Bound: len %d valid=%v", len(got), utf8.ValidString(got))
	}
	if len(got) < MaxBytes-2 {
		t.Fatalf("Bound cut too much: %d", len(got))
	}
	c := New(nil)
	c.Deliver(s)
	if len(c.ClipboardText()) > MaxBytes {
		t.Fatal("Deliver did not bound the text")
	}
}

// A paste is delivered before its key: the widget's own paste then reads it.
func TestTerminalPasteReadsTheDeliveredText(t *testing.T) {
	c := New(nil)
	toolkit.SetClipboard(c)
	defer toolkit.SetClipboard(nil)
	term := toolkit.NewTerminal(20, 3, 10)
	term.SetBounds(toolkit.Rect{W: 160, H: 48})
	var typed string
	term.OnInput = func(s string) { typed += s }
	c.Deliver("echo a\nls")
	term.OnEvent(toolkit.Event{Kind: toolkit.EventKeyDown, Code: "V", Ctrl: true, Shift: true})
	if typed != "echo a\rls" {
		t.Fatalf("terminal received %q, want %q (newline as CR)", typed, "echo a\rls")
	}
}
