// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package shellparse

import (
	"fmt"
	"strings"
)

// dollarQMarker is the in-band sentinel the lexer leaves where '$?' was. The
// executor substitutes it with the running LastExit just before dispatch so
// the chain `false ; echo $?` prints "1" (not the prior line's exit). The
// marker byte 0x01 is illegal in user input -- the lexer rejects it via the
// normal word path because input is captured byte-by-byte but real keyboards
// never produce it, so collisions are not a worry in v1.
const dollarQMarker = "\x01?"

// Kind classifies a Token. The lexer collapses adjacent word fragments
// (literal runs + escapes + quoted spans) into a single TokWord, so the
// parser never has to glue them.
type Kind int

const (
	// TokWord is a single shell word (the joined value of a run of
	// non-operator, non-whitespace characters with quoting/escapes resolved).
	TokWord Kind = iota + 1
	// TokPipe is '|'.
	TokPipe
	// TokRedirIn is '<'.
	TokRedirIn
	// TokRedirOut is '>'.
	TokRedirOut
	// TokRedirAppend is '>>'.
	TokRedirAppend
	// TokSemi is ';'.
	TokSemi
	// TokAnd is '&&'.
	TokAnd
	// TokOr is '||'.
	TokOr
)

// Token is one lexed unit. Operators carry an empty Value; words carry the
// fully-resolved string (quotes stripped, escapes applied, $? substituted).
type Token struct {
	Kind  Kind
	Value string
}

// Lex turns a command line into a token slice. '$?' is emitted as an
// in-band sentinel ([dollarQMarker]); the executor swaps it for the
// running LastExit at dispatch time so a chain like `false ; echo $?`
// prints the post-false exit (not the prior line's). Returns a typed
// *Error on unclosed quotes / trailing backslash so callers can show a
// useful message instead of a generic parse failure.
//
// The lexer is character-by-character (single pass, no regexp): we walk the
// line, peeling off whitespace + comments first, then matching the longest
// operator, then collecting a word.
func Lex(line string) ([]Token, error) {
	var out []Token
	i := 0
	n := len(line)
	for i < n {
		c := line[i]
		// Whitespace separates tokens.
		if c == ' ' || c == '\t' {
			i++
			continue
		}
		// '#' starts a comment ONLY at a word boundary. We get here only
		// after skipping whitespace, so this is always a word boundary.
		if c == '#' {
			break
		}
		// Two-character operators win over their single-char prefixes.
		if c == '>' && i+1 < n && line[i+1] == '>' {
			out = append(out, Token{Kind: TokRedirAppend})
			i += 2
			continue
		}
		if c == '&' && i+1 < n && line[i+1] == '&' {
			out = append(out, Token{Kind: TokAnd})
			i += 2
			continue
		}
		if c == '|' && i+1 < n && line[i+1] == '|' {
			out = append(out, Token{Kind: TokOr})
			i += 2
			continue
		}
		// Single-char operators.
		switch c {
		case '|':
			out = append(out, Token{Kind: TokPipe})
			i++
			continue
		case '>':
			out = append(out, Token{Kind: TokRedirOut})
			i++
			continue
		case '<':
			out = append(out, Token{Kind: TokRedirIn})
			i++
			continue
		case ';':
			out = append(out, Token{Kind: TokSemi})
			i++
			continue
		case '&':
			// Bare '&' (background) is a v2 feature; treat it as an error
			// in v1 so the user gets a clear message rather than silent
			// misbehaviour.
			return nil, &Error{Msg: "unexpected '&' (background jobs not supported)"}
		}
		// Otherwise it's the start of a word.
		w, j, err := lexWord(line, i)
		if err != nil {
			return nil, err
		}
		out = append(out, Token{Kind: TokWord, Value: w})
		i = j
	}
	return out, nil
}

// lexWord collects a single word starting at line[i], stopping at the first
// unquoted whitespace or operator. Returns the resolved string and the
// position immediately after the word.
//
// Inside a word we honour:
//   - '\<c>' escapes (the next byte becomes literal; '\' at EOL is an error)
//   - "...":  double-quoted span (literal bytes; backslash still escapes the
//     next byte, matching POSIX double-quote semantics minus $/`)
//   - '...':  single-quoted span (every byte literal up to the closing quote)
//   - '$?':   left as [dollarQMarker]; the executor expands it at run time
//
// Other '$' usages are left as-is (no $VAR / $0 yet).
func lexWord(line string, start int) (string, int, error) {
	var b strings.Builder
	i := start
	n := len(line)
	for i < n {
		c := line[i]
		if c == ' ' || c == '\t' {
			break
		}
		if c == '|' || c == '>' || c == '<' || c == ';' || c == '&' {
			break
		}
		if c == '\\' {
			if i+1 >= n {
				return "", 0, &Error{Msg: "trailing backslash"}
			}
			b.WriteByte(line[i+1])
			i += 2
			continue
		}
		if c == '\'' {
			j, err := readSingleQuoted(line, i+1, &b)
			if err != nil {
				return "", 0, err
			}
			i = j
			continue
		}
		if c == '"' {
			j, err := readDoubleQuoted(line, i+1, &b)
			if err != nil {
				return "", 0, err
			}
			i = j
			continue
		}
		if c == '$' && i+1 < n && line[i+1] == '?' {
			// Defer expansion to runtime so cross-stage $? sees the right
			// exit (executor swaps the marker for strconv.Itoa(curExit)).
			b.WriteString(dollarQMarker)
			i += 2
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String(), i, nil
}

// readSingleQuoted consumes a '...' span. Every byte is literal; the only
// way out is a closing single quote.
func readSingleQuoted(line string, i int, b *strings.Builder) (int, error) {
	n := len(line)
	for i < n {
		if line[i] == '\'' {
			return i + 1, nil
		}
		b.WriteByte(line[i])
		i++
	}
	return 0, &Error{Msg: "unclosed single quote"}
}

// readDoubleQuoted consumes a "..." span. Backslash still escapes the next
// byte (so \" embeds a quote), '$?' still leaves a [dollarQMarker] for
// run-time substitution; everything else is literal. Returns the index
// after the closing quote.
func readDoubleQuoted(line string, i int, b *strings.Builder) (int, error) {
	n := len(line)
	for i < n {
		c := line[i]
		if c == '"' {
			return i + 1, nil
		}
		if c == '\\' {
			if i+1 >= n {
				return 0, &Error{Msg: "trailing backslash in double-quoted string"}
			}
			b.WriteByte(line[i+1])
			i += 2
			continue
		}
		if c == '$' && i+1 < n && line[i+1] == '?' {
			b.WriteString(dollarQMarker)
			i += 2
			continue
		}
		b.WriteByte(c)
		i++
	}
	return 0, &Error{Msg: "unclosed double quote"}
}

// Error is the typed error the lexer + parser return so callers can show
// "shell: <msg>" without re-typing the prefix.
type Error struct{ Msg string }

// Error returns the wrapped message.
func (e *Error) Error() string { return fmt.Sprintf("shell: %s", e.Msg) }
