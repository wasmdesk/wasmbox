// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's highlight.go is the tiny Go-flavoured tokenizer the
// editor pane uses to colour keywords / strings / line comments / numbers
// the VS Code "Dark+" way. Per-line tokenization is sufficient (no multi-
// line string support v0) -- the renderer walks each line and emits
// drawTextColored runs whose colour comes straight off the returned Token
// stream.
//
// Pure Go, no syscall/js -- testable natively on every architecture this
// repo targets.

package scene

// VS Code Dark+ syntactic palette. Exported so render.go + tests can pin
// the exact RGB triples without re-reading hex strings; the playwright
// probe samples for ColorKeyword inside the editor pane to prove syntax
// highlighting actually paints.
var (
	// ColorEditorText is the default editor foreground (Dark+ token "text").
	ColorEditorText = [3]uint8{0xD4, 0xD4, 0xD4}
	// ColorKeyword paints Go keywords (#569CD6).
	ColorKeyword = [3]uint8{0x56, 0x9C, 0xD6}
	// ColorString paints double-quoted string literals (#CE9178).
	ColorString = [3]uint8{0xCE, 0x91, 0x78}
	// ColorComment paints "// ..." line comments (#6A9955).
	ColorComment = [3]uint8{0x6A, 0x99, 0x55}
	// ColorNumber paints numeric literals (#B5CEA8).
	ColorNumber = [3]uint8{0xB5, 0xCE, 0xA8}
)

// Token is one coloured run in a tokenized line. Text is the raw substring
// the renderer paints; Color is the RGB triple to paint it in. The tokens
// for a line concatenate back to the original line text without gaps
// (Tokenize keeps whitespace inside Text runs, never as separators), so
// drawTextColored can paint the line by walking the slice and advancing
// the x cursor by len(tok.Text) * FontW * scale per token.
type Token struct {
	Color [3]uint8
	Text  string
}

// keywords is the Go keyword set the highlighter recognises. We use a map
// for O(1) membership; the set is small enough that the cost vs a slice
// scan is a wash -- the map is clearer at the call site.
var keywords = map[string]bool{
	"func": true, "var": true, "if": true, "else": true, "for": true,
	"return": true, "package": true, "import": true, "type": true,
	"struct": true, "interface": true, "const": true, "range": true,
	"break": true, "continue": true, "switch": true, "case": true,
	"default": true,
}

// Tokenize splits one line of Go-ish source into coloured Token runs. The
// returned slice concatenates back to the input line (each Token's Text
// includes any leading whitespace that belongs to the same colour run).
// An empty line returns one zero-length default-coloured token so callers
// can iterate without nil-checking.
func Tokenize(line string) []Token {
	if line == "" {
		return []Token{{Color: ColorEditorText, Text: ""}}
	}
	var out []Token
	// emit appends one Token to out. Every call site is guaranteed to pass
	// non-empty text (the tokenizer never produces a zero-length run inside
	// its main loop), so no defensive guard here -- a guarded branch would
	// be unreachable and drag coverage below 100%.
	emit := func(col [3]uint8, text string) {
		out = append(out, Token{Color: col, Text: text})
	}
	i := 0
	for i < len(line) {
		c := line[i]
		// "// ..." line comment -- the rest of the line is one comment run.
		if c == '/' && i+1 < len(line) && line[i+1] == '/' {
			emit(ColorComment, line[i:])
			return out
		}
		// "..." string literal. Honours backslash escapes so an escaped
		// quote inside the literal does not end it; an unclosed string
		// runs to end-of-line and still paints as a string (matches what
		// VS Code does mid-keystroke).
		if c == '"' {
			j := i + 1
			for j < len(line) {
				if line[j] == '\\' && j+1 < len(line) {
					j += 2
					continue
				}
				if line[j] == '"' {
					j++
					break
				}
				j++
			}
			emit(ColorString, line[i:j])
			i = j
			continue
		}
		// Number literal: a run of digits (optional decimal point). v0
		// keeps it simple -- no hex / octal / exponent / underscore parsing.
		if c >= '0' && c <= '9' {
			j := i + 1
			for j < len(line) && ((line[j] >= '0' && line[j] <= '9') || line[j] == '.') {
				j++
			}
			emit(ColorNumber, line[i:j])
			i = j
			continue
		}
		// Identifier-ish run: leading [A-Za-z_] then [A-Za-z0-9_]*. The
		// run is then checked against the keyword set.
		if isIdentStart(c) {
			j := i + 1
			for j < len(line) && isIdentCont(line[j]) {
				j++
			}
			word := line[i:j]
			if keywords[word] {
				emit(ColorKeyword, word)
			} else {
				emit(ColorEditorText, word)
			}
			i = j
			continue
		}
		// Everything else (operators, whitespace, punctuation) goes into a
		// single default-coloured run until the next interesting byte. We
		// peel them off one at a time so a switch from default to (say)
		// keyword starts a new token at exactly the right boundary.
		j := i + 1
		for j < len(line) {
			d := line[j]
			if d == '/' && j+1 < len(line) && line[j+1] == '/' {
				break
			}
			if d == '"' || isIdentStart(d) || (d >= '0' && d <= '9') {
				break
			}
			j++
		}
		emit(ColorEditorText, line[i:j])
		i = j
	}
	return out
}

// isIdentStart reports whether c may start an identifier ([A-Za-z_]).
func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

// isIdentCont reports whether c may continue an identifier ([A-Za-z0-9_]).
func isIdentCont(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}
