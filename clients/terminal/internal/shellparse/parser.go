// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package shellparse

// RedirKind classifies a Redirect (read / write / append).
type RedirKind int

const (
	// RedirIn is '< path' -- stdin reads from the file.
	RedirIn RedirKind = iota + 1
	// RedirOut is '> path' -- stdout replaces the file's contents.
	RedirOut
	// RedirAppend is '>> path' -- stdout appends to the file.
	RedirAppend
)

// Redirect is one '<', '>', or '>>' on a command.
type Redirect struct {
	Kind RedirKind
	Path string
}

// Command is one tool invocation: its argv plus any redirects that attach
// to it (a pipeline's redirects sit on the first/last stages).
type Command struct {
	Argv      []string
	Redirects []Redirect
}

// Pipeline is one or more Commands joined by '|'.
type Pipeline struct {
	Stages []*Command
}

// Op classifies how a Pipeline chains onto the previous one in an AndOr.
type Op int

const (
	// OpNone applies to the head pipeline (no predecessor).
	OpNone Op = iota
	// OpAnd is '&&'.
	OpAnd
	// OpOr is '||'.
	OpOr
)

// Chain pairs a Pipeline with the operator that introduces it.
type Chain struct {
	Op   Op
	Pipe *Pipeline
}

// AndOr is a chain of Pipelines joined by '&&' / '||'.
type AndOr struct {
	Chains []Chain
}

// Line is one full input line: a sequence of AndOrs separated by ';'.
type Line struct {
	List []*AndOr
}

// Parse turns a token slice into a Line. Returns a typed *Error on the
// usual shapes (missing operand after '|', '&&', '||', ';', redirect target
// missing or being an operator, redirect with no preceding command, etc.).
func Parse(tokens []Token) (*Line, error) {
	p := &parser{toks: tokens}
	return p.parseLine()
}

// parser is a single-pass recursive-descent driver. pos is the index into
// toks; consume / peek / expect handle the cursor.
type parser struct {
	toks []Token
	pos  int
}

// peek returns the current token (or a zero-value Token with Kind 0 at EOF).
func (p *parser) peek() Token {
	if p.pos >= len(p.toks) {
		return Token{}
	}
	return p.toks[p.pos]
}

// consume advances past the current token.
func (p *parser) consume() { p.pos++ }

// done reports whether the cursor has hit EOF.
func (p *parser) done() bool { return p.pos >= len(p.toks) }

// parseLine is the top of the grammar: AndOr (';' AndOr)* [';'].
func (p *parser) parseLine() (*Line, error) {
	line := &Line{}
	// Empty input is a valid (no-op) line.
	if p.done() {
		return line, nil
	}
	for {
		ao, err := p.parseAndOr()
		if err != nil {
			return nil, err
		}
		line.List = append(line.List, ao)
		if p.done() {
			return line, nil
		}
		if p.peek().Kind != TokSemi {
			// parseAndOr stops at TokSemi or EOF; anything else here is a
			// parser bug, but bubble a clear error rather than panicking.
			return nil, &Error{Msg: "unexpected token after pipeline"}
		}
		p.consume() // ';'
		if p.done() {
			// Trailing ';' is fine.
			return line, nil
		}
	}
}

// parseAndOr is: pipeline (('&&' | '||') pipeline)*.
func (p *parser) parseAndOr() (*AndOr, error) {
	ao := &AndOr{}
	pipe, err := p.parsePipeline()
	if err != nil {
		return nil, err
	}
	ao.Chains = append(ao.Chains, Chain{Op: OpNone, Pipe: pipe})
	for !p.done() {
		k := p.peek().Kind
		if k != TokAnd && k != TokOr {
			break
		}
		op := OpAnd
		if k == TokOr {
			op = OpOr
		}
		p.consume()
		if p.done() {
			return nil, &Error{Msg: "missing command after '&&' or '||'"}
		}
		rhs, err := p.parsePipeline()
		if err != nil {
			return nil, err
		}
		ao.Chains = append(ao.Chains, Chain{Op: op, Pipe: rhs})
	}
	return ao, nil
}

// parsePipeline is: command ('|' command)*.
func (p *parser) parsePipeline() (*Pipeline, error) {
	pipe := &Pipeline{}
	cmd, err := p.parseCommand()
	if err != nil {
		return nil, err
	}
	pipe.Stages = append(pipe.Stages, cmd)
	for !p.done() && p.peek().Kind == TokPipe {
		p.consume()
		if p.done() {
			return nil, &Error{Msg: "missing command after '|'"}
		}
		next, err := p.parseCommand()
		if err != nil {
			return nil, err
		}
		pipe.Stages = append(pipe.Stages, next)
	}
	return pipe, nil
}

// parseCommand is: word+ (redirect | word)*. Words and redirects can
// interleave (e.g. `wc -l < /n.txt` and `cat /n.txt > /out.txt -E` both
// parse), matching POSIX. We require at least ONE leading word so an empty
// command (just redirects) is rejected with a clear message.
func (p *parser) parseCommand() (*Command, error) {
	cmd := &Command{}
	if p.done() || p.peek().Kind != TokWord {
		return nil, &Error{Msg: "expected command name"}
	}
	cmd.Argv = append(cmd.Argv, p.peek().Value)
	p.consume()
	for !p.done() {
		t := p.peek()
		switch t.Kind {
		case TokWord:
			cmd.Argv = append(cmd.Argv, t.Value)
			p.consume()
		case TokRedirIn, TokRedirOut, TokRedirAppend:
			kind := t.Kind
			p.consume()
			if p.done() || p.peek().Kind != TokWord {
				return nil, &Error{Msg: "missing path after redirect"}
			}
			path := p.peek().Value
			p.consume()
			cmd.Redirects = append(cmd.Redirects, Redirect{
				Kind: toRedirKind(kind),
				Path: path,
			})
		default:
			// Pipe / semi / and / or: end of this command.
			return cmd, nil
		}
	}
	return cmd, nil
}

// toRedirKind maps the lexer's operator token kind onto the parser's
// Redirect kind. The mapping is total over the three redirect kinds.
func toRedirKind(k Kind) RedirKind {
	switch k {
	case TokRedirIn:
		return RedirIn
	case TokRedirOut:
		return RedirOut
	case TokRedirAppend:
		return RedirAppend
	}
	// Unreachable -- caller only passes redirect kinds. Return RedirOut as
	// a safe default so the function stays total.
	return RedirOut
}
