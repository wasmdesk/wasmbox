// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package shellparse

import (
	"strings"
	"testing"
)

// mustLex is a helper that runs the lexer and fails the test on a
// lex-time error (the parser tests are about parser shapes).
func mustLex(t *testing.T, line string) []Token {
	t.Helper()
	toks, err := Lex(line)
	if err != nil {
		t.Fatalf("Lex(%q) = %v", line, err)
	}
	return toks
}

// Empty token slice parses to an empty Line.
func TestParseEmpty(t *testing.T) {
	l, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse(nil) = %v", err)
	}
	if len(l.List) != 0 {
		t.Fatalf("Parse(nil).List = %v, want empty", l.List)
	}
}

// Single command -> single AndOr with one OpNone chain.
func TestParseSingleCommand(t *testing.T) {
	l, err := Parse(mustLex(t, "echo hi"))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(l.List) != 1 {
		t.Fatalf("Line list = %d, want 1", len(l.List))
	}
	ao := l.List[0]
	if len(ao.Chains) != 1 || ao.Chains[0].Op != OpNone {
		t.Fatalf("AndOr chains shape = %+v", ao.Chains)
	}
	pipe := ao.Chains[0].Pipe
	if len(pipe.Stages) != 1 {
		t.Fatalf("Pipeline stages = %d, want 1", len(pipe.Stages))
	}
	if got, want := strings.Join(pipe.Stages[0].Argv, ","), "echo,hi"; got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
	if len(pipe.Stages[0].Redirects) != 0 {
		t.Errorf("unexpected redirects: %+v", pipe.Stages[0].Redirects)
	}
}

// Pipelines + redirects + and/or + semi all compose.
func TestParseFullLine(t *testing.T) {
	l, err := Parse(mustLex(t, "cat /a | sort -r > /b && echo ok || echo bad ; pwd"))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(l.List) != 2 {
		t.Fatalf("Line list = %d, want 2 AndOrs", len(l.List))
	}
	// First AndOr: 3 chains (pipeline, && pipeline, || pipeline).
	ao := l.List[0]
	if len(ao.Chains) != 3 {
		t.Fatalf("first AndOr chains = %d, want 3", len(ao.Chains))
	}
	if ao.Chains[0].Op != OpNone || ao.Chains[1].Op != OpAnd || ao.Chains[2].Op != OpOr {
		t.Errorf("ops = %v %v %v", ao.Chains[0].Op, ao.Chains[1].Op, ao.Chains[2].Op)
	}
	// First pipeline has two stages, second stage has a '>' redirect.
	pipe0 := ao.Chains[0].Pipe
	if len(pipe0.Stages) != 2 {
		t.Fatalf("first pipeline stages = %d, want 2", len(pipe0.Stages))
	}
	stage1 := pipe0.Stages[1]
	if len(stage1.Redirects) != 1 || stage1.Redirects[0].Kind != RedirOut || stage1.Redirects[0].Path != "/b" {
		t.Errorf("stage1 redirects = %+v", stage1.Redirects)
	}
	// Second AndOr is a single pwd.
	if len(l.List[1].Chains) != 1 || l.List[1].Chains[0].Pipe.Stages[0].Argv[0] != "pwd" {
		t.Errorf("second AndOr shape = %+v", l.List[1])
	}
}

// Trailing semicolon is fine.
func TestParseTrailingSemi(t *testing.T) {
	l, err := Parse(mustLex(t, "echo a ;"))
	if err != nil || len(l.List) != 1 {
		t.Fatalf("Parse(\"echo a ;\") = %+v err=%v", l, err)
	}
}

// Redirect tokens can interleave with args.
func TestParseInterleavedRedirects(t *testing.T) {
	l, err := Parse(mustLex(t, "wc -l < /n.txt > /o.txt"))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	cmd := l.List[0].Chains[0].Pipe.Stages[0]
	if len(cmd.Argv) != 2 || cmd.Argv[1] != "-l" {
		t.Errorf("argv = %v", cmd.Argv)
	}
	if len(cmd.Redirects) != 2 {
		t.Fatalf("redirects = %+v", cmd.Redirects)
	}
	if cmd.Redirects[0].Kind != RedirIn || cmd.Redirects[0].Path != "/n.txt" {
		t.Errorf("redir 0 = %+v", cmd.Redirects[0])
	}
	if cmd.Redirects[1].Kind != RedirOut || cmd.Redirects[1].Path != "/o.txt" {
		t.Errorf("redir 1 = %+v", cmd.Redirects[1])
	}
}

// '>>' parses to RedirAppend.
func TestParseAppendRedirect(t *testing.T) {
	l, err := Parse(mustLex(t, "echo hi >> /log"))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	r := l.List[0].Chains[0].Pipe.Stages[0].Redirects[0]
	if r.Kind != RedirAppend || r.Path != "/log" {
		t.Errorf("redirect = %+v", r)
	}
}

// Parser error shapes.
func TestParseErrors(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"missing-after-and", "true &&", "missing command after '&&' or '||'"},
		{"missing-after-or", "true ||", "missing command after '&&' or '||'"},
		{"missing-after-pipe", "cat /a |", "missing command after '|'"},
		{"missing-after-redir", "echo >", "missing path after redirect"},
		{"redir-then-pipe", "echo > | cat", "missing path after redirect"},
		{"leading-operator", "| cat", "expected command name"},
		{"leading-semi", "; echo a", "expected command name"},
		{"only-redirect", "> /tmp/x", "expected command name"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(mustLex(t, c.in))
			if err == nil {
				t.Fatalf("Parse(%q) error = nil, want %q", c.in, c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("Parse(%q) = %q, want it to contain %q", c.in, err.Error(), c.want)
			}
		})
	}
}

// toRedirKind covers every redirect kind, and falls back to RedirOut for
// any non-redirect kind (defensive default).
func TestToRedirKind(t *testing.T) {
	if toRedirKind(TokRedirIn) != RedirIn {
		t.Errorf("toRedirKind(TokRedirIn) wrong")
	}
	if toRedirKind(TokRedirOut) != RedirOut {
		t.Errorf("toRedirKind(TokRedirOut) wrong")
	}
	if toRedirKind(TokRedirAppend) != RedirAppend {
		t.Errorf("toRedirKind(TokRedirAppend) wrong")
	}
	if toRedirKind(TokWord) != RedirOut {
		t.Errorf("toRedirKind(TokWord) = %v, want fallback RedirOut", toRedirKind(TokWord))
	}
}

// Error from the RHS of an && chain bubbles up through parseAndOr.
func TestParseAndOrRHSError(t *testing.T) {
	// `true && cat |` -- the second pipeline fails because '|' has no
	// trailing command, so parsePipeline returns an error that parseAndOr
	// must propagate.
	_, err := Parse(mustLex(t, "true && cat |"))
	if err == nil {
		t.Fatal("expected error from RHS pipeline parse")
	}
	if !strings.Contains(err.Error(), "missing command after '|'") {
		t.Errorf("got %q", err.Error())
	}
}

// Error from a downstream pipeline stage bubbles up through parsePipeline.
func TestParsePipelineDownstreamCommandError(t *testing.T) {
	// `cat | > /x` -- second command is empty (only a redirect, no name).
	_, err := Parse(mustLex(t, "cat | > /x"))
	if err == nil {
		t.Fatal("expected error from downstream command parse")
	}
	if !strings.Contains(err.Error(), "expected command name") {
		t.Errorf("got %q", err.Error())
	}
}

// peek on an empty parser returns a zero token without panicking.
func TestParserPeekEOF(t *testing.T) {
	p := &parser{}
	if p.peek().Kind != 0 {
		t.Errorf("peek on empty = %+v", p.peek())
	}
}

// parseLine surfaces an error if the post-AndOr cursor is not on a TokSemi.
// We can't trigger this through Lex+Parse because parseAndOr exits on any
// non-pipe/non-and/non-or token, and parseLine immediately rechecks for
// TokSemi -- but a hand-crafted token slice can drive the path (a leading
// TokWord then a TokRedirIn-with-no-arg would: but we already covered
// missing-path-after-redirect). The defensive branch is the "unexpected
// token after pipeline" message; we trigger it by giving Parse a Pipe-only
// token, but Parse(`|`) fails earlier with "expected command name". To
// reach the branch in isolation we drive the parser directly with a forged
// token stream where parseAndOr consumes its pipeline and leaves a token
// that is neither TokSemi nor end-of-input.
//
// We construct: [TokWord(echo), TokWord(hi), TokPipe(unused-by-AndOr-loop)?]
// That doesn't work either -- TokPipe is consumed by parsePipeline. The
// branch is genuinely unreachable from valid lexer output, but it's the
// kind of defensive guard the parser deserves. We exercise it by directly
// constructing a parser with a forged stream.
func TestParseLineUnexpectedTokenAfterPipeline(t *testing.T) {
	// Forge: a word followed by a redirect-out token but then the parser
	// is told to stop. To do this we hand-craft tokens that parseAndOr
	// will refuse to consume.
	//
	// parseAndOr stops on anything that is not && or ||. parseLine then
	// expects TokSemi or EOF. So a stream like:
	//   word("echo"), word("hi"), TokRedirIn (with no following word)
	// makes parseCommand error out FIRST with "missing path after redirect".
	//
	// We exercise the unreachable branch by feeding a stream where
	// parseAndOr terminates cleanly at a TokRedirOut (parser sees one
	// non-and/or/pipe/word after the command, command loop returns, then
	// parseLine sees an unexpected token). The trick: use parseAndOr
	// directly via Parse with a custom slice.
	tokens := []Token{
		{Kind: TokWord, Value: "echo"},
		{Kind: 99}, // bogus kind not in the parser's switch
	}
	_, err := Parse(tokens)
	if err == nil {
		t.Fatal("expected error on bogus trailing token")
	}
	if !strings.Contains(err.Error(), "unexpected token after pipeline") {
		t.Errorf("got %q, want 'unexpected token after pipeline'", err.Error())
	}
}
