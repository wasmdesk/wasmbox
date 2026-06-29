// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package shellparse

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// fakeVFS is a stdlib-only in-memory VFS used to drive redirect tests
// without pulling sharedvfs into the shellparse package.
type fakeVFS struct {
	files   map[string][]byte
	readErr map[string]error
	writeOK func(path string) error
}

func newFakeVFS() *fakeVFS {
	return &fakeVFS{files: map[string][]byte{}, readErr: map[string]error{}}
}

func (f *fakeVFS) Read(p string) ([]byte, error) {
	if e, ok := f.readErr[p]; ok {
		return nil, e
	}
	b, ok := f.files[p]
	if !ok {
		return nil, errors.New("not found")
	}
	return b, nil
}

func (f *fakeVFS) Write(p string, data []byte) error {
	if f.writeOK != nil {
		if err := f.writeOK(p); err != nil {
			return err
		}
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	f.files[p] = cp
	return nil
}

// fakeDispatch returns a DispatchFunc that runs one of a registered set
// of fake "tools". The handler receives stdin/stdout/stderr and returns an
// exit code; missing names map to a "command not found" stderr line + exit 127.
type toolHandler func(argv []string, stdin io.Reader, stdout, stderr io.Writer) int

func fakeDispatch(tools map[string]toolHandler) DispatchFunc {
	return func(name string, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
		fn, ok := tools[name]
		if !ok {
			fmt.Fprintf(stderr, "%s: command not found\n", name)
			return 127
		}
		return fn(argv, stdin, stdout, stderr)
	}
}

// Library of fake tools used across tests.
func basicTools() map[string]toolHandler {
	return map[string]toolHandler{
		"echo": func(argv []string, _ io.Reader, stdout, _ io.Writer) int {
			fmt.Fprintln(stdout, strings.Join(argv[1:], " "))
			return 0
		},
		"true":  func([]string, io.Reader, io.Writer, io.Writer) int { return 0 },
		"false": func([]string, io.Reader, io.Writer, io.Writer) int { return 1 },
		"cat": func(_ []string, stdin io.Reader, stdout, _ io.Writer) int {
			b, _ := io.ReadAll(stdin)
			stdout.Write(b)
			return 0
		},
		"upper": func(_ []string, stdin io.Reader, stdout, _ io.Writer) int {
			b, _ := io.ReadAll(stdin)
			stdout.Write([]byte(strings.ToUpper(string(b))))
			return 0
		},
		"wc-l": func(_ []string, stdin io.Reader, stdout, _ io.Writer) int {
			b, _ := io.ReadAll(stdin)
			n := 0
			for _, c := range b {
				if c == '\n' {
					n++
				}
			}
			fmt.Fprintf(stdout, "%d\n", n)
			return 0
		},
		"err": func(_ []string, _ io.Reader, _, stderr io.Writer) int {
			fmt.Fprintln(stderr, "boom")
			return 2
		},
	}
}

// runLine is the common driver: lex + parse + Run. lastExit seeds the
// initial $? value the executor will substitute.
func runLine(t *testing.T, input string, lastExit int, vfs VFS, dispatch DispatchFunc) Result {
	t.Helper()
	toks, err := Lex(input)
	if err != nil {
		t.Fatalf("Lex(%q) = %v", input, err)
	}
	line, err := Parse(toks)
	if err != nil {
		t.Fatalf("Parse(%q) = %v", input, err)
	}
	return Run(line, lastExit, vfs, dispatch)
}

// Run with nil line is a no-op (no panic). The initial exit echoes back.
func TestRunNil(t *testing.T) {
	res := Run(nil, 0, newFakeVFS(), fakeDispatch(nil))
	if res.LastExit != 0 || len(res.Stdout) != 0 || len(res.Stderr) != 0 {
		t.Errorf("Run(nil) = %+v", res)
	}
}

// Run with an empty Line is a no-op too.
func TestRunEmptyLine(t *testing.T) {
	res := Run(&Line{}, 0, newFakeVFS(), fakeDispatch(nil))
	if res.LastExit != 0 || len(res.Stdout) != 0 || len(res.Stderr) != 0 {
		t.Errorf("Run(empty) = %+v", res)
	}
}

// Empty AndOr in a Line yields exit 0.
func TestRunAndOrEmpty(t *testing.T) {
	res := Run(&Line{List: []*AndOr{{}}}, 0, newFakeVFS(), fakeDispatch(nil))
	if res.LastExit != 0 {
		t.Errorf("empty AndOr exit = %d", res.LastExit)
	}
}

// One simple command captures its stdout + exit code.
func TestRunSingleCommand(t *testing.T) {
	res := runLine(t, "echo hello", 0, newFakeVFS(), fakeDispatch(basicTools()))
	if string(res.Stdout) != "hello\n" {
		t.Errorf("stdout = %q", res.Stdout)
	}
	if res.LastExit != 0 {
		t.Errorf("exit = %d", res.LastExit)
	}
}

// Stderr from a tool is collected to Result.Stderr.
func TestRunStderr(t *testing.T) {
	res := runLine(t, "err", 0, newFakeVFS(), fakeDispatch(basicTools()))
	if string(res.Stderr) != "boom\n" {
		t.Errorf("stderr = %q", res.Stderr)
	}
	if res.LastExit != 2 {
		t.Errorf("exit = %d", res.LastExit)
	}
}

// Pipelines pipe stage N's stdout to stage N+1's stdin.
func TestRunPipeline(t *testing.T) {
	res := runLine(t, "echo hello | upper", 0, newFakeVFS(), fakeDispatch(basicTools()))
	if string(res.Stdout) != "HELLO\n" {
		t.Errorf("stdout = %q", res.Stdout)
	}
}

// Three-stage pipeline.
func TestRunThreeStagePipeline(t *testing.T) {
	res := runLine(t, "echo a | upper | cat", 0, newFakeVFS(), fakeDispatch(basicTools()))
	if string(res.Stdout) != "A\n" {
		t.Errorf("stdout = %q", res.Stdout)
	}
}

// '&&' runs the RHS only on success.
func TestRunAndShortCircuit(t *testing.T) {
	res := runLine(t, "false && echo yes", 0, newFakeVFS(), fakeDispatch(basicTools()))
	if string(res.Stdout) != "" {
		t.Errorf("stdout = %q, want empty", res.Stdout)
	}
	if res.LastExit != 1 {
		t.Errorf("exit = %d", res.LastExit)
	}
	res = runLine(t, "true && echo yes", 0, newFakeVFS(), fakeDispatch(basicTools()))
	if string(res.Stdout) != "yes\n" {
		t.Errorf("stdout = %q", res.Stdout)
	}
}

// '||' runs the RHS only on failure.
func TestRunOrShortCircuit(t *testing.T) {
	res := runLine(t, "false || echo no", 0, newFakeVFS(), fakeDispatch(basicTools()))
	if string(res.Stdout) != "no\n" {
		t.Errorf("stdout = %q", res.Stdout)
	}
	if res.LastExit != 0 {
		t.Errorf("exit = %d", res.LastExit)
	}
	res = runLine(t, "true || echo no", 0, newFakeVFS(), fakeDispatch(basicTools()))
	if string(res.Stdout) != "" {
		t.Errorf("stdout = %q, want empty", res.Stdout)
	}
}

// ';' runs both commands regardless of exit code.
func TestRunSemi(t *testing.T) {
	res := runLine(t, "false ; echo done", 0, newFakeVFS(), fakeDispatch(basicTools()))
	if string(res.Stdout) != "done\n" {
		t.Errorf("stdout = %q", res.Stdout)
	}
	if res.LastExit != 0 {
		t.Errorf("exit = %d", res.LastExit)
	}
}

// '$?' expands at lex time to the previous exit code.
func TestRunDollarQuestion(t *testing.T) {
	res := runLine(t, "echo $?", 7, newFakeVFS(), fakeDispatch(basicTools()))
	if string(res.Stdout) != "7\n" {
		t.Errorf("stdout = %q", res.Stdout)
	}
}

// '> path' writes stdout to the VFS.
func TestRunRedirOut(t *testing.T) {
	v := newFakeVFS()
	res := runLine(t, "echo hello > /msg.txt", 0, v, fakeDispatch(basicTools()))
	if string(res.Stdout) != "" {
		t.Errorf("stdout leaked: %q", res.Stdout)
	}
	if got := string(v.files["/msg.txt"]); got != "hello\n" {
		t.Errorf("file body = %q", got)
	}
}

// '>> path' appends to an existing file.
func TestRunRedirAppend(t *testing.T) {
	v := newFakeVFS()
	v.files["/log"] = []byte("seed\n")
	runLine(t, "echo more >> /log", 0, v, fakeDispatch(basicTools()))
	if got := string(v.files["/log"]); got != "seed\nmore\n" {
		t.Errorf("appended body = %q", got)
	}
}

// '>> path' to a missing file creates it (no seed to concat).
func TestRunRedirAppendMissing(t *testing.T) {
	v := newFakeVFS()
	runLine(t, "echo first >> /new", 0, v, fakeDispatch(basicTools()))
	if got := string(v.files["/new"]); got != "first\n" {
		t.Errorf("body = %q", got)
	}
}

// '< path' reads stdin from the VFS.
func TestRunRedirIn(t *testing.T) {
	v := newFakeVFS()
	v.files["/in.txt"] = []byte("lower\n")
	res := runLine(t, "upper < /in.txt", 0, v, fakeDispatch(basicTools()))
	if string(res.Stdout) != "LOWER\n" {
		t.Errorf("stdout = %q", res.Stdout)
	}
}

// '< path' against a missing file fails the stage with exit 1 + stderr.
func TestRunRedirInMissing(t *testing.T) {
	v := newFakeVFS()
	res := runLine(t, "cat < /nope", 0, v, fakeDispatch(basicTools()))
	if res.LastExit != 1 {
		t.Errorf("exit = %d, want 1", res.LastExit)
	}
	if !strings.Contains(string(res.Stderr), "/nope") {
		t.Errorf("stderr = %q, want path mention", res.Stderr)
	}
}

// '> path' against a VFS that rejects the write surfaces exit 1 + stderr.
func TestRunRedirOutWriteError(t *testing.T) {
	v := newFakeVFS()
	v.writeOK = func(p string) error {
		return errors.New("disk full")
	}
	res := runLine(t, "echo hi > /full", 0, v, fakeDispatch(basicTools()))
	if res.LastExit != 1 {
		t.Errorf("exit = %d, want 1", res.LastExit)
	}
	if !strings.Contains(string(res.Stderr), "disk full") {
		t.Errorf("stderr = %q", res.Stderr)
	}
}

// Pipeline + '> path' on the LAST stage captures the pipeline output to file.
func TestRunPipelineToFile(t *testing.T) {
	v := newFakeVFS()
	runLine(t, "echo hi | upper > /out", 0, v, fakeDispatch(basicTools()))
	if got := string(v.files["/out"]); got != "HI\n" {
		t.Errorf("file body = %q", got)
	}
}

// Pipeline + '> path' on a MIDDLE stage swallows that stage's stdout; the
// next stage sees empty stdin.
func TestRunPipelineMidStageRedirect(t *testing.T) {
	v := newFakeVFS()
	res := runLine(t, "echo hi > /mid | upper", 0, v, fakeDispatch(basicTools()))
	if got := string(v.files["/mid"]); got != "hi\n" {
		t.Errorf("mid file body = %q", got)
	}
	// 'upper' receives empty stdin -> empty stdout.
	if string(res.Stdout) != "" {
		t.Errorf("downstream stdout = %q, want empty", res.Stdout)
	}
}

// Multi-line: ';' separated AndOrs both run, LastExit follows the LAST.
func TestRunMultiAndOr(t *testing.T) {
	res := runLine(t, "false ; true", 0, newFakeVFS(), fakeDispatch(basicTools()))
	if res.LastExit != 0 {
		t.Errorf("exit = %d", res.LastExit)
	}
	res = runLine(t, "true ; false", 0, newFakeVFS(), fakeDispatch(basicTools()))
	if res.LastExit != 1 {
		t.Errorf("exit = %d", res.LastExit)
	}
}

// Command not found surfaces dispatch's stderr + non-zero exit.
func TestRunCommandNotFound(t *testing.T) {
	res := runLine(t, "nope", 0, newFakeVFS(), fakeDispatch(basicTools()))
	if res.LastExit != 127 {
		t.Errorf("exit = %d", res.LastExit)
	}
	if !strings.Contains(string(res.Stderr), "command not found") {
		t.Errorf("stderr = %q", res.Stderr)
	}
}

// Redirect-then-pipe routing: a failed input redirect on a middle stage
// short-circuits THAT stage (exit 1) but the pipeline continues.
func TestRunPipelineMidStageRedirInFailure(t *testing.T) {
	v := newFakeVFS()
	// upper < /missing | cat -- 'upper' fails, 'cat' still runs against
	// the empty buffer left over from the failed stage.
	res := runLine(t, "upper < /missing | cat", 0, v, fakeDispatch(basicTools()))
	if !strings.Contains(string(res.Stderr), "/missing") {
		t.Errorf("stderr = %q", res.Stderr)
	}
	if res.LastExit != 0 {
		// 'cat' on empty input succeeds with exit 0, and the pipeline's
		// exit is the LAST stage's exit.
		t.Errorf("exit = %d, want 0 (cat succeeded after upper failed)", res.LastExit)
	}
}

// Last-writer-wins for stdout when multiple '>' redirects pile up.
func TestRunRedirOutLastWins(t *testing.T) {
	v := newFakeVFS()
	runLine(t, "echo hi > /a > /b", 0, v, fakeDispatch(basicTools()))
	if _, ok := v.files["/a"]; ok {
		t.Errorf("/a should NOT have been written (last-writer-wins)")
	}
	if got := string(v.files["/b"]); got != "hi\n" {
		t.Errorf("/b body = %q", got)
	}
}

// runPipeline on an empty Pipeline returns 0 (defensive; not reachable via
// parser, but keeps the loop total).
func TestRunPipelineEmpty(t *testing.T) {
	res := Run(&Line{List: []*AndOr{{Chains: []Chain{{Op: OpNone, Pipe: &Pipeline{}}}}}},
		0, newFakeVFS(), fakeDispatch(basicTools()))
	if res.LastExit != 0 {
		t.Errorf("empty pipeline exit = %d", res.LastExit)
	}
}

// '$?' is expanded LIVE at dispatch time. After `false ; echo $?` the echo
// must see the post-false exit (1), not the seed exit fed into Run (which
// is 0 here).
func TestRunDollarQLiveExpansion(t *testing.T) {
	res := runLine(t, "false ; echo $?", 0, newFakeVFS(), fakeDispatch(basicTools()))
	if string(res.Stdout) != "1\n" {
		t.Errorf("false ; echo $? stdout = %q, want %q", res.Stdout, "1\n")
	}
	res = runLine(t, "true ; echo $?", 0, newFakeVFS(), fakeDispatch(basicTools()))
	if string(res.Stdout) != "0\n" {
		t.Errorf("true ; echo $? stdout = %q, want %q", res.Stdout, "0\n")
	}
}

// expandArgv leaves argv items without a marker untouched.
func TestExpandArgvNoMarker(t *testing.T) {
	in := []string{"echo", "hello"}
	out := expandArgv(in, 9)
	if out[0] != "echo" || out[1] != "hello" {
		t.Errorf("expandArgv mutated argv: %v", out)
	}
}

// expandRedirects on an empty slice returns the same slice (cheap fast-path).
func TestExpandRedirectsEmpty(t *testing.T) {
	in := []Redirect{}
	out := expandRedirects(in, 0)
	if len(out) != 0 {
		t.Errorf("expandRedirects(empty) = %v", out)
	}
}

// expandRedirects swaps the marker in the path.
func TestExpandRedirectsMarker(t *testing.T) {
	in := []Redirect{{Kind: RedirOut, Path: "/tmp/" + dollarQMarker + ".log"}}
	out := expandRedirects(in, 42)
	if out[0].Path != "/tmp/42.log" {
		t.Errorf("expandRedirects = %q", out[0].Path)
	}
}

// '$?' in a redirect path resolves via expandRedirects.
func TestRunDollarQInRedirectPath(t *testing.T) {
	v := newFakeVFS()
	runLine(t, "echo hi > /tmp/$?", 7, v, fakeDispatch(basicTools()))
	if got := string(v.files["/tmp/7"]); got != "hi\n" {
		t.Errorf("file body for $?-resolved path = %q", got)
	}
}
