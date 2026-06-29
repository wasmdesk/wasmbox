// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package shellparse

import (
	"strings"
	"testing"
)

// tok is a short-hand constructor used to make the lex table compact.
func tok(k Kind, v string) Token { return Token{Kind: k, Value: v} }

// TestLexTable covers every grammar arm of the lexer: bare words, quoted
// spans (both flavours), escapes, single + double-char operators, comments,
// $? sentinel emission, leading/trailing whitespace, empty input.
func TestLexTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []Token
	}{
		{"empty", "", nil},
		{"whitespace-only", "   \t  ", nil},
		{"comment-only", "# just a note", nil},
		{"bare-word", "echo", []Token{tok(TokWord, "echo")}},
		{"two-words", "echo hi", []Token{tok(TokWord, "echo"), tok(TokWord, "hi")}},
		{"tabs-between-words", "a\tb\tc", []Token{tok(TokWord, "a"), tok(TokWord, "b"), tok(TokWord, "c")}},
		{"comment-after-cmd", "echo hi # comment", []Token{tok(TokWord, "echo"), tok(TokWord, "hi")}},
		{"single-quoted", "echo 'hi there'", []Token{tok(TokWord, "echo"), tok(TokWord, "hi there")}},
		{"single-quoted-with-specials", "echo 'a|b>c;d'", []Token{tok(TokWord, "echo"), tok(TokWord, "a|b>c;d")}},
		{"double-quoted", `echo "hi there"`, []Token{tok(TokWord, "echo"), tok(TokWord, "hi there")}},
		{"double-quoted-with-escape", `echo "a\"b"`, []Token{tok(TokWord, "echo"), tok(TokWord, `a"b`)}},
		{"escape-outside-quotes", `echo a\ b`, []Token{tok(TokWord, "echo"), tok(TokWord, "a b")}},
		{"escape-operator", `echo a\>b`, []Token{tok(TokWord, "echo"), tok(TokWord, "a>b")}},
		{"pipe", "a|b", []Token{tok(TokWord, "a"), tok(TokPipe, ""), tok(TokWord, "b")}},
		{"redir-in", "wc<f", []Token{tok(TokWord, "wc"), tok(TokRedirIn, ""), tok(TokWord, "f")}},
		{"redir-out", "echo>f", []Token{tok(TokWord, "echo"), tok(TokRedirOut, ""), tok(TokWord, "f")}},
		{"redir-append", "echo>>f", []Token{tok(TokWord, "echo"), tok(TokRedirAppend, ""), tok(TokWord, "f")}},
		{"semi", "a;b", []Token{tok(TokWord, "a"), tok(TokSemi, ""), tok(TokWord, "b")}},
		{"and", "a&&b", []Token{tok(TokWord, "a"), tok(TokAnd, ""), tok(TokWord, "b")}},
		{"or", "a||b", []Token{tok(TokWord, "a"), tok(TokOr, ""), tok(TokWord, "b")}},
		{"dollar-q", "echo $?", []Token{tok(TokWord, "echo"), tok(TokWord, dollarQMarker)}},
		{"dollar-q-in-double", `echo "exit=$?"`, []Token{tok(TokWord, "echo"), tok(TokWord, "exit=" + dollarQMarker)}},
		{"dollar-not-q", "echo $X", []Token{tok(TokWord, "echo"), tok(TokWord, "$X")}},
		{"adjacent-quotes-join", `echo 'a'"b"c`, []Token{tok(TokWord, "echo"), tok(TokWord, "abc")}},
		{"mixed-op-and-word", "cat /a.txt|grep foo>>/log", []Token{
			tok(TokWord, "cat"),
			tok(TokWord, "/a.txt"),
			tok(TokPipe, ""),
			tok(TokWord, "grep"),
			tok(TokWord, "foo"),
			tok(TokRedirAppend, ""),
			tok(TokWord, "/log"),
		}},
		{"word-then-hash-mid-word", "abc#def", []Token{tok(TokWord, "abc#def")}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got, err := Lex(c.in)
			if err != nil {
				t.Fatalf("Lex(%q) error: %v", c.in, err)
			}
			if !tokensEqual(got, c.want) {
				t.Errorf("Lex(%q) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

// Lex error paths.
func TestLexErrors(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"unclosed-single", "echo 'hi", "unclosed single quote"},
		{"unclosed-double", `echo "hi`, "unclosed double quote"},
		{"trailing-backslash", `echo a\`, "trailing backslash"},
		{"trailing-backslash-in-double", `echo "a\`, "trailing backslash in double-quoted string"},
		{"bare-ampersand", "a & b", "unexpected '&'"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			_, err := Lex(c.in)
			if err == nil {
				t.Fatalf("Lex(%q) error = nil, want %q", c.in, c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("Lex(%q) = %q, want it to contain %q", c.in, err.Error(), c.want)
			}
			// Error type carries the wrapped Msg.
			if _, ok := err.(*Error); !ok {
				t.Errorf("Lex error is %T, want *Error", err)
			}
		})
	}
}

// Lex propagates errors out of double-quoted spans too.
func TestLexDoubleQuoteErrors(t *testing.T) {
	if _, err := Lex(`echo "`); err == nil {
		t.Fatal("expected unclosed double-quote error")
	}
}

// tokensEqual compares two token slices in order. nil and empty are equal
// (helps tests stay terse).
func tokensEqual(a, b []Token) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || a[i].Value != b[i].Value {
			return false
		}
	}
	return true
}

// Error.Error wraps the message with the "shell: " prefix.
func TestErrorFormat(t *testing.T) {
	e := &Error{Msg: "boom"}
	if e.Error() != "shell: boom" {
		t.Errorf("Error() = %q, want %q", e.Error(), "shell: boom")
	}
}
