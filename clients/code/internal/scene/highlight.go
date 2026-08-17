// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's highlight.go pins the editor's VS Code "Dark+" palette and
// maps it onto the SHARED go-widgets/toolkit CodeEditor's pluggable highlighter
// (github.com/go-widgets/toolkit/rougelex, backed by the pure-Go rouge lexers).
//
// This client used to carry its own tiny per-line Go tokenizer here; it now
// consumes the toolkit's CodeEditor widget instead, so every wasmdesk code
// surface (this client, go-loom, the reader preview) shares one implementation
// and gains real multi-language highlighting (Go, Ruby, Python, JavaScript,
// CSS, HTML, JSON, YAML, SQL, shell, Markdown, diff) for free. Only the Dark+
// palette + the extension->language mapping remain client-specific.
//
// Pure Go, no syscall/js -- testable natively on every architecture this repo
// targets.

package scene

import (
	"strings"

	"github.com/go-widgets/toolkit"
	"github.com/go-widgets/toolkit/rougelex"
)

// VS Code Dark+ syntactic palette. Exported so render.go + tests can pin the
// exact RGB triples without re-reading hex strings; the playwright probe samples
// for ColorKeyword inside the editor pane to prove syntax highlighting paints.
var (
	// ColorEditorText is the default editor foreground (Dark+ token "text").
	ColorEditorText = [3]uint8{0xD4, 0xD4, 0xD4}
	// ColorKeyword paints keywords (#569CD6).
	ColorKeyword = [3]uint8{0x56, 0x9C, 0xD6}
	// ColorString paints string literals (#CE9178).
	ColorString = [3]uint8{0xCE, 0x91, 0x78}
	// ColorComment paints comments (#6A9955).
	ColorComment = [3]uint8{0x6A, 0x99, 0x55}
	// ColorNumber paints numeric literals (#B5CEA8).
	ColorNumber = [3]uint8{0xB5, 0xCE, 0xA8}
)

// rgb converts a Dark+ palette RGB triple into the toolkit's RGBA (alpha forced
// opaque). Keeping the palette as [3]uint8 preserves the exact hex triples the
// playwright probe duplicates while letting widgets consume a toolkit.RGBA.
func rgb(c [3]uint8) toolkit.RGBA { return toolkit.RGB(c[0], c[1], c[2]) }

// darkPlusPalette maps the rouge token categories onto the Dark+ colours the
// editor has always used: keywords (and type/builtin keywords) blue, strings
// salmon, comments green, numbers pale green, and everything else the default
// #D4D4D4 editor foreground. It is handed to the CodeEditor's rougelex
// highlighter so `func` stays #569CD6 -- the colour the syntax-highlight probe
// samples -- across the migration from the old hand-rolled tokenizer.
func darkPlusPalette() rougelex.Palette {
	text := rgb(ColorEditorText)
	kw := rgb(ColorKeyword)
	return rougelex.Palette{
		Default:     text,
		Keyword:     kw,
		Type:        kw,
		Function:    text,
		Class:       text,
		Builtin:     kw,
		String:      rgb(ColorString),
		Number:      rgb(ColorNumber),
		Comment:     rgb(ColorComment),
		Operator:    text,
		Punctuation: text,
	}
}

// languageForPath picks the rouge lexer tag from a file's extension, so opening
// a .rb / .py / .css / .md file highlights it as that language rather than as
// Go. An unknown or extensionless path falls back to Go (the seed default and
// the language the demo tree's sources are written in).
func languageForPath(path string) string {
	dot := strings.LastIndexByte(path, '.')
	if dot < 0 {
		return "go"
	}
	switch strings.ToLower(path[dot+1:]) {
	case "rb":
		return "ruby"
	case "py":
		return "python"
	case "js":
		return "javascript"
	case "css":
		return "css"
	case "html", "htm":
		return "html"
	case "json":
		return "json"
	case "yaml", "yml":
		return "yaml"
	case "md", "markdown":
		return "markdown"
	case "sh", "bash":
		return "bash"
	case "sql":
		return "sql"
	default:
		return "go"
	}
}
