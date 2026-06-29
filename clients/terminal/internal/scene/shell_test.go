// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import (
	"strings"
	"testing"

	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

// newTestShell returns a Shell over an empty, freshly-rooted InMemoryVFS so
// tests start from a deterministic tree (no demo seed leaking into ls/cat
// outputs). cwd starts at "/".
func newTestShell() *Shell {
	return NewShellWithVFS(sharedvfs.NewInMemoryVFS())
}

// NewShell stamps the default prompt + cwd + a seeded demo VFS.
func TestNewShellDefaults(t *testing.T) {
	sh := NewShell()
	if sh.Prompt != " $ " {
		t.Fatalf("default prompt = %q, want %q", sh.Prompt, " $ ")
	}
	if sh.Cwd != "/" {
		t.Fatalf("default cwd = %q, want %q", sh.Cwd, "/")
	}
	if sh.Line != nil || len(sh.History) != 0 {
		t.Fatalf("freshly built shell has non-empty edit state: %+v", sh)
	}
	// The default constructor wires a seeded demo VFS so /Documents exists.
	if !sh.VFS.IsDir("/Documents") {
		t.Fatalf("NewShell did not seed demo VFS (no /Documents)")
	}
}

// PromptString composes cwd + Prompt so the wasm renderer can paint the
// path the user is standing in without an extra pwd. When the last command
// failed (LastExit != 0) the prompt prepends "[N] " so the user notices
// without typing echo $?.
func TestPromptString(t *testing.T) {
	sh := newTestShell()
	if got, want := sh.PromptString(), "/ $ "; got != want {
		t.Errorf("PromptString at root = %q, want %q", got, want)
	}
	sh.Cwd = "/Documents"
	if got, want := sh.PromptString(), "/Documents $ "; got != want {
		t.Errorf("PromptString after cd = %q, want %q", got, want)
	}
	sh.LastExit = 7
	if got, want := sh.PromptString(), "[7] /Documents $ "; got != want {
		t.Errorf("PromptString with LastExit = %q, want %q", got, want)
	}
}

// Empty / whitespace-only lines produce no output and do NOT enter history.
func TestExecuteEmptyLine(t *testing.T) {
	sh := newTestShell()
	for _, in := range []string{"", "   ", "\t  \t"} {
		if out := sh.Execute(in); out != nil {
			t.Fatalf("Execute(%q) = %v, want nil", in, out)
		}
	}
	if len(sh.History) != 0 {
		t.Fatalf("empty lines polluted history: %v", sh.History)
	}
}

// `echo` echoes its args back, joined by single spaces.
func TestExecuteEcho(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("echo hello")
	if len(out) != 1 || out[0] != "hello" {
		t.Fatalf("echo hello = %v", out)
	}
	out = sh.Execute("echo  a  b  c")
	if len(out) != 1 || out[0] != "a b c" {
		t.Fatalf("echo a b c = %v", out)
	}
	out = sh.Execute("echo")
	if len(out) != 1 || out[0] != "" {
		t.Fatalf("bare echo = %v", out)
	}
}

// `help` lists the (now larger) builtin set plus the four shell-grammar
// hints (redirection, pipelines, chaining).
func TestExecuteHelp(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("help")
	if len(out) != 4 {
		t.Fatalf("help = %v, want 4 lines", out)
	}
	if want := "builtins:"; out[0][:len(want)] != want {
		t.Fatalf("help first line = %q", out[0])
	}
}

// `clear` returns no output (the caller wipes the grid via IsClear()).
func TestExecuteClear(t *testing.T) {
	sh := newTestShell()
	if out := sh.Execute("clear"); out != nil {
		t.Fatalf("clear = %v, want nil", out)
	}
}

// `date` routes to the coreutils date builtin (UTC RFC1123). We only
// pattern-match on the suffix so the test stays clock-agnostic.
func TestExecuteDate(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("date")
	if len(out) != 1 {
		t.Fatalf("date = %v, want 1 line", out)
	}
	if !strings.HasSuffix(out[0], " UTC") {
		t.Fatalf("date %q lacks UTC suffix", out[0])
	}
}

// `pwd` reflects sh.Cwd.
func TestExecutePwd(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("pwd")
	if len(out) != 1 || out[0] != "/" {
		t.Fatalf("pwd = %v", out)
	}
	sh.Cwd = "/tmp"
	out = sh.Execute("pwd")
	if out[0] != "/tmp" {
		t.Fatalf("pwd after cwd change = %v", out)
	}
}

// `ls` lists the VFS, directories first; with no args it uses cwd.
func TestExecuteLs(t *testing.T) {
	sh := newTestShell()
	_ = sh.VFS.Mkdir("/dir")
	_ = sh.VFS.Write("/file1.txt", []byte("a"))
	_ = sh.VFS.Write("/file2.txt", []byte("b"))
	out := sh.Execute("ls")
	if len(out) != 3 || out[0] != "dir/" || out[1] != "file1.txt" || out[2] != "file2.txt" {
		t.Fatalf("ls = %v", out)
	}
}

// `ls /missing` reports an error.
func TestExecuteLsMissing(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("ls /missing")
	if len(out) != 1 || out[0][:3] != "ls:" {
		t.Fatalf("ls /missing = %v", out)
	}
}

// `ls` of an empty dir returns nil (no rows).
func TestExecuteLsEmptyDir(t *testing.T) {
	sh := newTestShell()
	_ = sh.VFS.Mkdir("/empty")
	out := sh.Execute("ls /empty")
	if out != nil {
		t.Fatalf("ls /empty = %v, want nil", out)
	}
}

// `cd` updates Cwd; `cd` with no args returns to "/".
func TestExecuteCd(t *testing.T) {
	sh := newTestShell()
	_ = sh.VFS.Mkdir("/a")
	_ = sh.VFS.Mkdir("/a/b")
	if out := sh.Execute("cd /a"); out != nil {
		t.Fatalf("cd /a = %v, want nil", out)
	}
	if sh.Cwd != "/a" {
		t.Fatalf("cwd after cd = %q, want /a", sh.Cwd)
	}
	if out := sh.Execute("cd b"); out != nil || sh.Cwd != "/a/b" {
		t.Fatalf("cd b: out=%v cwd=%q", out, sh.Cwd)
	}
	if out := sh.Execute("cd .."); out != nil || sh.Cwd != "/a" {
		t.Fatalf("cd .. : out=%v cwd=%q", out, sh.Cwd)
	}
	if out := sh.Execute("cd"); out != nil || sh.Cwd != "/" {
		t.Fatalf("bare cd: out=%v cwd=%q", out, sh.Cwd)
	}
	if out := sh.Execute("cd ~"); out != nil || sh.Cwd != "/" {
		t.Fatalf("cd ~ : out=%v cwd=%q", out, sh.Cwd)
	}
}

// `cd` to a non-directory reports an error and leaves Cwd unchanged.
func TestExecuteCdNotDir(t *testing.T) {
	sh := newTestShell()
	_ = sh.VFS.Write("/file.txt", []byte("x"))
	out := sh.Execute("cd /file.txt")
	if len(out) != 1 || out[0][:3] != "cd:" {
		t.Fatalf("cd /file.txt = %v", out)
	}
	if sh.Cwd != "/" {
		t.Errorf("cwd after failed cd = %q, want /", sh.Cwd)
	}
}

// `cat` prints file body line-by-line.
func TestExecuteCat(t *testing.T) {
	sh := newTestShell()
	_ = sh.VFS.Write("/hello.txt", []byte("line1\nline2\n"))
	out := sh.Execute("cat /hello.txt")
	if len(out) != 2 || out[0] != "line1" || out[1] != "line2" {
		t.Fatalf("cat = %v", out)
	}
}

// `cat` without a trailing newline still emits the last line.
func TestExecuteCatNoTrailingNL(t *testing.T) {
	sh := newTestShell()
	_ = sh.VFS.Write("/x.txt", []byte("alpha\nbeta"))
	out := sh.Execute("cat /x.txt")
	if len(out) != 2 || out[1] != "beta" {
		t.Fatalf("cat no-trailing-nl = %v", out)
	}
}

// `cat` of multiple files concatenates outputs and reports per-file errors.
// Since stdout (the file bodies) and stderr (the missing-file diagnostic)
// are routed through separate buffers and concatenated, stdout precedes
// stderr in the merged output regardless of the order the tool wrote them.
func TestExecuteCatMultiple(t *testing.T) {
	sh := newTestShell()
	_ = sh.VFS.Write("/a.txt", []byte("A\n"))
	_ = sh.VFS.Write("/b.txt", []byte("B\n"))
	out := sh.Execute("cat /a.txt /missing /b.txt")
	if len(out) != 3 || out[0] != "A" || out[1] != "B" {
		t.Fatalf("cat multi = %v", out)
	}
	if out[2][:4] != "cat:" {
		t.Errorf("cat missing line = %q", out[2])
	}
}

// `cat` with no args reports an error.
func TestExecuteCatNoArgs(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("cat")
	if len(out) != 1 || out[0][:4] != "cat:" {
		t.Fatalf("bare cat = %v", out)
	}
}

// `cat` of an empty file produces no rows.
func TestExecuteCatEmpty(t *testing.T) {
	sh := newTestShell()
	_ = sh.VFS.Write("/e.txt", nil)
	out := sh.Execute("cat /e.txt")
	if out != nil {
		t.Fatalf("cat empty = %v, want nil", out)
	}
}

// `mkdir` creates a directory at the resolved path.
func TestExecuteMkdir(t *testing.T) {
	sh := newTestShell()
	if out := sh.Execute("mkdir /scratch"); out != nil {
		t.Fatalf("mkdir = %v, want nil", out)
	}
	if !sh.VFS.IsDir("/scratch") {
		t.Errorf("/scratch not created")
	}
}

// `mkdir` of an existing path reports an error.
func TestExecuteMkdirExisting(t *testing.T) {
	sh := newTestShell()
	_ = sh.VFS.Mkdir("/already")
	out := sh.Execute("mkdir /already")
	if len(out) != 1 || out[0][:6] != "mkdir:" {
		t.Fatalf("mkdir /already = %v", out)
	}
}

// `mkdir` with no args reports an error.
func TestExecuteMkdirNoArgs(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("mkdir")
	if len(out) != 1 || out[0][:6] != "mkdir:" {
		t.Fatalf("bare mkdir = %v", out)
	}
}

// `touch` creates an empty file (and is a no-op on an existing one).
func TestExecuteTouch(t *testing.T) {
	sh := newTestShell()
	if out := sh.Execute("touch /a.txt"); out != nil {
		t.Fatalf("touch = %v, want nil", out)
	}
	if _, err := sh.VFS.Stat("/a.txt"); err != nil {
		t.Errorf("/a.txt not created: %v", err)
	}
	// Second touch on the same path is a no-op (no error rows).
	if out := sh.Execute("touch /a.txt"); out != nil {
		t.Fatalf("second touch = %v, want nil", out)
	}
}

// `touch` of a path whose parent does not exist reports an error.
func TestExecuteTouchMissingParent(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("touch /nope/a.txt")
	if len(out) != 1 || out[0][:6] != "touch:" {
		t.Fatalf("touch missing-parent = %v", out)
	}
}

// `touch` with no args reports an error.
func TestExecuteTouchNoArgs(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("touch")
	if len(out) != 1 || out[0][:6] != "touch:" {
		t.Fatalf("bare touch = %v", out)
	}
}

// `rm` removes files; a missing path reports an error.
func TestExecuteRm(t *testing.T) {
	sh := newTestShell()
	_ = sh.VFS.Write("/gone.txt", []byte("x"))
	if out := sh.Execute("rm /gone.txt"); out != nil {
		t.Fatalf("rm = %v, want nil", out)
	}
	if _, err := sh.VFS.Stat("/gone.txt"); err == nil {
		t.Errorf("/gone.txt still exists")
	}
	out := sh.Execute("rm /gone.txt")
	if len(out) != 1 || out[0][:3] != "rm:" {
		t.Fatalf("rm of missing = %v", out)
	}
}

// `rm` with no args reports an error.
func TestExecuteRmNoArgs(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("rm")
	if len(out) != 1 || out[0][:3] != "rm:" {
		t.Fatalf("bare rm = %v", out)
	}
}

// `echo TEXT > PATH` writes TEXT+"\n" to PATH.
func TestExecuteEchoRedirect(t *testing.T) {
	sh := newTestShell()
	if out := sh.Execute("echo hello world > /msg.txt"); out != nil {
		t.Fatalf("echo > = %v, want nil", out)
	}
	body, err := sh.VFS.Read("/msg.txt")
	if err != nil {
		t.Fatalf("Read /msg.txt = %v", err)
	}
	if string(body) != "hello world\n" {
		t.Errorf("/msg.txt body = %q, want %q", string(body), "hello world\n")
	}
}

// `echo "quoted" > PATH` strips a single surrounding pair of double quotes.
func TestExecuteEchoRedirectQuoted(t *testing.T) {
	sh := newTestShell()
	_ = sh.Execute(`echo "hi there" > /q.txt`)
	body, _ := sh.VFS.Read("/q.txt")
	if string(body) != "hi there\n" {
		t.Errorf("quoted echo = %q, want %q", string(body), "hi there\n")
	}
}

// `echo > PATH` with the parent missing surfaces the VFS error via the
// shellparse executor's "shell: PATH: <reason>" line on stderr, and the
// shell records exit 1.
func TestExecuteEchoRedirectFail(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("echo hi > /nope/x.txt")
	if len(out) != 1 || !strings.HasPrefix(out[0], "shell: /nope/x.txt:") {
		t.Fatalf("echo > bad-path = %v", out)
	}
	if sh.LastExit != 1 {
		t.Fatalf("LastExit = %d, want 1", sh.LastExit)
	}
}

// `echo hi >` (empty destination) is a parser error.
func TestExecuteEchoRedirectEmptyPath(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("echo hi >")
	if len(out) != 1 || !strings.Contains(out[0], "missing path after redirect") {
		t.Fatalf("echo hi > = %v", out)
	}
	if sh.LastExit != 2 {
		t.Fatalf("LastExit = %d, want 2 (parse error)", sh.LastExit)
	}
}

// Unknown command -> not-found line.
func TestExecuteUnknown(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("frobnicate -x")
	if len(out) != 1 || out[0] != "frobnicate: command not found" {
		t.Fatalf("unknown = %v", out)
	}
}

// Execute populates history with the trimmed line.
func TestExecuteHistory(t *testing.T) {
	sh := newTestShell()
	sh.Execute("  echo a  ")
	sh.Execute("ls")
	if len(sh.History) != 2 {
		t.Fatalf("history len = %d, want 2", len(sh.History))
	}
	if string(sh.History[0]) != "echo a" || string(sh.History[1]) != "ls" {
		t.Fatalf("history content = %q, %q", sh.History[0], sh.History[1])
	}
}

// IsClear is true for `clear` (any surrounding whitespace), false otherwise.
func TestIsClear(t *testing.T) {
	if !IsClear("clear") {
		t.Fatal("IsClear(\"clear\") = false")
	}
	if !IsClear("  clear  ") {
		t.Fatal("IsClear(\"  clear  \") = false")
	}
	if IsClear("clears") {
		t.Fatal("IsClear(\"clears\") = true")
	}
	if IsClear("") {
		t.Fatal("IsClear(\"\") = true")
	}
}

// errString formats each sharedvfs sentinel into the shell-style suffix.
// We exercise every branch so a future VFS sentinel addition fails the
// switch loudly.
func TestErrString(t *testing.T) {
	cases := []struct {
		in   error
		want string
	}{
		{sharedvfs.ErrNotFound, "no such file or directory"},
		{sharedvfs.ErrNotDir, "not a directory"},
		{sharedvfs.ErrIsDir, "is a directory"},
		{sharedvfs.ErrExists, "file exists"},
		{sharedvfs.ErrInvalid, "invalid argument"},
	}
	for _, c := range cases {
		if got := errString(c.in); got != c.want {
			t.Errorf("errString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	// Unknown errors fall through to err.Error().
	if got := errString(errBoom); got != "boom" {
		t.Errorf("errString(boom) = %q, want %q", got, "boom")
	}
}

// errBoom is a placeholder unknown error used to drive the fall-through
// branch in errString. Declared here (not in shell.go) because the runtime
// never needs it.
var errBoom = boomError{}

type boomError struct{}

func (boomError) Error() string { return "boom" }

// A lexer-level error (unclosed single quote) surfaces as a "shell: ..."
// line and sets LastExit to 2.
func TestExecuteLexError(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("echo 'hi")
	if len(out) != 1 || !strings.Contains(out[0], "unclosed single quote") {
		t.Fatalf("lex error = %v", out)
	}
	if sh.LastExit != 2 {
		t.Fatalf("LastExit after lex error = %d, want 2", sh.LastExit)
	}
}

// A parser-level error (missing command after &&) surfaces as a "shell:
// ..." line and sets LastExit to 2.
func TestExecuteParseError(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("true &&")
	if len(out) != 1 || !strings.Contains(out[0], "missing command after") {
		t.Fatalf("parse error = %v", out)
	}
	if sh.LastExit != 2 {
		t.Fatalf("LastExit after parse error = %d, want 2", sh.LastExit)
	}
}

// '< path' threads the file's bytes into the stage's stdin via the
// vfsExecAdapter.Read path. The stdin-bridge then materializes them to a
// synthetic file (rewritten to "-" in user-visible output) so wc, which
// doesn't stream stdin, can still count them.
func TestExecuteRedirectIn(t *testing.T) {
	sh := newTestShell()
	_ = sh.VFS.Write("/n.txt", []byte("a\nb\nc\n"))
	out := sh.Execute("wc -l < /n.txt")
	if len(out) != 1 || !strings.HasPrefix(strings.TrimSpace(out[0]), "3") {
		t.Fatalf("wc -l < /n.txt = %v", out)
	}
	if sh.LastExit != 0 {
		t.Fatalf("LastExit = %d", sh.LastExit)
	}
}

// '< path' against a missing file routes the VFS error through
// vfsExecAdapter.Read -> shellparse's "shell: PATH: <reason>" line + exit 1.
func TestExecuteRedirectInMissing(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("wc -l < /nope")
	if len(out) == 0 || !strings.Contains(out[len(out)-1], "/nope") {
		t.Fatalf("redirect-in missing = %v", out)
	}
	if sh.LastExit != 1 {
		t.Fatalf("LastExit = %d, want 1", sh.LastExit)
	}
}

// Pipeline (cat | wc -l) wires the bytes through. wc prints "N -" since
// the stdin-bridge presents stdin as the synthetic file renamed "-".
func TestExecutePipeline(t *testing.T) {
	sh := newTestShell()
	_ = sh.VFS.Write("/p.txt", []byte("one\ntwo\nthree\n"))
	out := sh.Execute("cat /p.txt | wc -l")
	if len(out) != 1 || !strings.HasPrefix(strings.TrimSpace(out[0]), "3") {
		t.Fatalf("pipeline = %v", out)
	}
}

// '&&' runs the RHS only on success; the chain's exit is the last stage's.
func TestExecuteAndChain(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("true && echo yes")
	if len(out) != 1 || out[0] != "yes" {
		t.Fatalf("true && echo yes = %v", out)
	}
	out = sh.Execute("false && echo yes")
	if out != nil {
		t.Fatalf("false && echo yes = %v, want no output", out)
	}
	if sh.LastExit != 1 {
		t.Fatalf("LastExit = %d, want 1 (false's exit)", sh.LastExit)
	}
}

// '||' runs the RHS only on failure.
func TestExecuteOrChain(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("false || echo no")
	if len(out) != 1 || out[0] != "no" {
		t.Fatalf("false || echo no = %v", out)
	}
	if sh.LastExit != 0 {
		t.Fatalf("LastExit = %d, want 0", sh.LastExit)
	}
}

// ';' runs both regardless; '$?' expands to the previous exit code.
func TestExecuteSemiAndDollarQ(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("false; echo $?")
	if len(out) != 1 || out[0] != "1" {
		t.Fatalf("false; echo $? = %v", out)
	}
	out = sh.Execute("true; echo $?")
	if len(out) != 1 || out[0] != "0" {
		t.Fatalf("true; echo $? = %v", out)
	}
}

// 'unknown' on a line free of metacharacters routes to dispatch (since
// it's not a local builtin), surfaces "command not found" + non-zero exit.
// In a pipe the trailing 'cat' sees empty stdin AND no positional file, so
// it usage-errors -- exit code is cat's (2), not the missing tool's (127).
func TestExecuteUnknownThroughParser(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("frobnicate | cat")
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "command not found") {
		t.Fatalf("unknown-in-pipe = %v (no 'command not found')", out)
	}
}
