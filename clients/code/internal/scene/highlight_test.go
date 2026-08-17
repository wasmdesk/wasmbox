// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// highlight_test.go covers the client-specific highlighting glue that survived
// the migration to the shared toolkit CodeEditor: the Dark+ palette mapping
// handed to the rougelex highlighter, and the extension->language selector. The
// tokenizing itself now lives in (and is tested by) go-widgets/toolkit/rougelex,
// so there is no per-line lexer to test here anymore.

package scene

import "testing"

// TestDarkPlusPalette pins the exact Dark+ colours the rougelex highlighter is
// configured with, so `func` stays keyword-blue (#569CD6) the way the render +
// browser probes sample it, strings salmon, comments green and numbers pale
// green, with everything else the default editor foreground.
func TestDarkPlusPalette(t *testing.T) {
	p := darkPlusPalette()
	if p.Keyword != rgb(ColorKeyword) {
		t.Errorf("Keyword = %+v, want %+v", p.Keyword, rgb(ColorKeyword))
	}
	if p.Type != rgb(ColorKeyword) || p.Builtin != rgb(ColorKeyword) {
		t.Error("Type + Builtin should share the keyword blue")
	}
	if p.String != rgb(ColorString) {
		t.Errorf("String = %+v, want %+v", p.String, rgb(ColorString))
	}
	if p.Comment != rgb(ColorComment) {
		t.Errorf("Comment = %+v, want %+v", p.Comment, rgb(ColorComment))
	}
	if p.Number != rgb(ColorNumber) {
		t.Errorf("Number = %+v, want %+v", p.Number, rgb(ColorNumber))
	}
	// Everything not a keyword/string/comment/number is the editor foreground.
	text := rgb(ColorEditorText)
	if p.Default != text || p.Function != text || p.Class != text ||
		p.Operator != text || p.Punctuation != text {
		t.Error("Default/Function/Class/Operator/Punctuation should be the editor foreground")
	}
}

func TestLanguageForPath(t *testing.T) {
	cases := map[string]string{
		"main.go":           "go",
		"/src/app.rb":       "ruby",
		"script.py":         "python",
		"index.js":          "javascript",
		"style.CSS":         "css", // case-insensitive extension
		"page.html":         "html",
		"page.htm":          "html",
		"data.json":         "json",
		"conf.yaml":         "yaml",
		"conf.yml":          "yaml",
		"README.md":         "markdown",
		"notes.markdown":    "markdown",
		"run.sh":            "bash",
		"query.sql":         "sql",
		"Makefile":          "go", // no extension -> default Go
		"weird.unknownext":  "go", // unknown extension -> default Go
		"/Documents/notes.": "go", // trailing dot, empty ext -> default
		"noext":             "go",
		"archive.tar.gz":    "go", // .gz not mapped -> default
		"a.b.rb":            "ruby",
	}
	for path, want := range cases {
		if got := languageForPath(path); got != want {
			t.Errorf("languageForPath(%q) = %q, want %q", path, got, want)
		}
	}
}
