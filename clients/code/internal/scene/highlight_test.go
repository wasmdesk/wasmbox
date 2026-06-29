// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import "testing"

// joinText reassembles the Token stream back into the original line. The
// invariant Tokenize guarantees (no gaps, no overlaps) is asserted by every
// table case below.
func joinText(toks []Token) string {
	out := ""
	for _, t := range toks {
		out += t.Text
	}
	return out
}

func TestTokenize_Empty(t *testing.T) {
	toks := Tokenize("")
	if len(toks) != 1 {
		t.Fatalf("empty line: want 1 token, got %d", len(toks))
	}
	if toks[0].Text != "" {
		t.Fatalf("empty token text: %q", toks[0].Text)
	}
}

func TestTokenize_Keyword(t *testing.T) {
	toks := Tokenize("func main")
	if joinText(toks) != "func main" {
		t.Fatalf("roundtrip: %q", joinText(toks))
	}
	if toks[0].Color != ColorKeyword || toks[0].Text != "func" {
		t.Errorf("keyword tok: %+v", toks[0])
	}
}

func TestTokenize_AllKeywords(t *testing.T) {
	for kw := range keywords {
		toks := Tokenize(kw + " x")
		if toks[0].Color != ColorKeyword || toks[0].Text != kw {
			t.Errorf("kw %q not coloured: %+v", kw, toks[0])
		}
	}
}

func TestTokenize_String(t *testing.T) {
	toks := Tokenize(`x := "hello"`)
	if joinText(toks) != `x := "hello"` {
		t.Fatalf("roundtrip: %q", joinText(toks))
	}
	gotString := false
	for _, tok := range toks {
		if tok.Color == ColorString && tok.Text == `"hello"` {
			gotString = true
		}
	}
	if !gotString {
		t.Errorf("string token missing: %+v", toks)
	}
}

func TestTokenize_String_WithEscapedQuote(t *testing.T) {
	in := `"a\"b"`
	toks := Tokenize(in)
	if len(toks) != 1 || toks[0].Color != ColorString || toks[0].Text != in {
		t.Errorf("escaped quote: %+v", toks)
	}
}

func TestTokenize_String_Unterminated(t *testing.T) {
	in := `"hello`
	toks := Tokenize(in)
	if len(toks) != 1 || toks[0].Color != ColorString || toks[0].Text != in {
		t.Errorf("unterminated: %+v", toks)
	}
}

func TestTokenize_String_BackslashAtEnd(t *testing.T) {
	in := `"abc\`
	toks := Tokenize(in)
	if joinText(toks) != in {
		t.Errorf("roundtrip: %q", joinText(toks))
	}
}

func TestTokenize_LineComment(t *testing.T) {
	toks := Tokenize("x // hi")
	if joinText(toks) != "x // hi" {
		t.Fatalf("roundtrip: %q", joinText(toks))
	}
	// Last token should be the comment colour.
	last := toks[len(toks)-1]
	if last.Color != ColorComment || last.Text != "// hi" {
		t.Errorf("comment tok: %+v", last)
	}
}

func TestTokenize_Number(t *testing.T) {
	toks := Tokenize("x := 123")
	gotNum := false
	for _, tok := range toks {
		if tok.Color == ColorNumber && tok.Text == "123" {
			gotNum = true
		}
	}
	if !gotNum {
		t.Errorf("number tok missing: %+v", toks)
	}
}

func TestTokenize_DecimalNumber(t *testing.T) {
	toks := Tokenize("3.14")
	if len(toks) != 1 || toks[0].Color != ColorNumber || toks[0].Text != "3.14" {
		t.Errorf("decimal tok: %+v", toks)
	}
}

func TestTokenize_OperatorsAndPunctuation(t *testing.T) {
	toks := Tokenize("a + b")
	if joinText(toks) != "a + b" {
		t.Fatalf("roundtrip: %q", joinText(toks))
	}
}

func TestTokenize_IdentifierAfterDefault(t *testing.T) {
	// "x" then operators "+ " then "y" -- the default-run terminator must
	// break at the start of "y".
	toks := Tokenize("x + y")
	if joinText(toks) != "x + y" {
		t.Fatalf("roundtrip: %q", joinText(toks))
	}
}

func TestTokenize_DefaultBreaksOnDigitAndString(t *testing.T) {
	for _, in := range []string{
		"+ 1",
		`+ "s"`,
	} {
		toks := Tokenize(in)
		if joinText(toks) != in {
			t.Errorf("roundtrip %q -> %q", in, joinText(toks))
		}
	}
}

func TestTokenize_DefaultBreaksOnCommentStart(t *testing.T) {
	// A "/" outside the start should not be eaten by the comment lookahead.
	toks := Tokenize("a//c")
	if joinText(toks) != "a//c" {
		t.Errorf("roundtrip: %q", joinText(toks))
	}
	// Last token must be the comment.
	last := toks[len(toks)-1]
	if last.Color != ColorComment {
		t.Errorf("trailing comment colour: %+v", last)
	}
}

func TestTokenize_NonKeywordIdentifier(t *testing.T) {
	toks := Tokenize("foo")
	if len(toks) != 1 || toks[0].Color != ColorEditorText || toks[0].Text != "foo" {
		t.Errorf("identifier tok: %+v", toks)
	}
}

func TestIsIdentStart_Underscore(t *testing.T) {
	if !isIdentStart('_') {
		t.Fatal("underscore should start identifier")
	}
	if isIdentStart('1') {
		t.Fatal("digit should not start identifier")
	}
}

func TestIsIdentCont(t *testing.T) {
	if !isIdentCont('1') || !isIdentCont('A') || !isIdentCont('_') {
		t.Fatal("isIdentCont true cases")
	}
	if isIdentCont('+') {
		t.Fatal("operator should not continue identifier")
	}
}

func TestTokenize_LeadingSlashNotComment(t *testing.T) {
	// A single "/" at the start should NOT trigger the comment branch.
	toks := Tokenize("/a")
	if joinText(toks) != "/a" {
		t.Errorf("roundtrip: %q", joinText(toks))
	}
}
