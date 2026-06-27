// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import (
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
// path the user is standing in without an extra pwd.
func TestPromptString(t *testing.T) {
	sh := newTestShell()
	if got, want := sh.PromptString(), "/ $ "; got != want {
		t.Errorf("PromptString at root = %q, want %q", got, want)
	}
	sh.Cwd = "/Documents"
	if got, want := sh.PromptString(), "/Documents $ "; got != want {
		t.Errorf("PromptString after cd = %q, want %q", got, want)
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

// `help` lists the (now larger) builtin set.
func TestExecuteHelp(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("help")
	if len(out) != 2 {
		t.Fatalf("help = %v, want 2 lines", out)
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

// `date` is deterministic so the playwright probe can assert exact pixels.
func TestExecuteDate(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("date")
	if len(out) != 1 || out[0] != "Fri Jun 26 12:00:00 UTC 2026" {
		t.Fatalf("date = %v", out)
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
func TestExecuteCatMultiple(t *testing.T) {
	sh := newTestShell()
	_ = sh.VFS.Write("/a.txt", []byte("A\n"))
	_ = sh.VFS.Write("/b.txt", []byte("B\n"))
	out := sh.Execute("cat /a.txt /missing /b.txt")
	if len(out) != 3 || out[0] != "A" || out[2] != "B" {
		t.Fatalf("cat multi = %v", out)
	}
	if out[1][:4] != "cat:" {
		t.Errorf("cat missing line = %q", out[1])
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

// `echo > PATH` with the parent missing reports an error.
func TestExecuteEchoRedirectFail(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("echo hi > /nope/x.txt")
	if len(out) != 1 || out[0][:5] != "echo:" {
		t.Fatalf("echo > bad-path = %v", out)
	}
}

// `echo TEXT >` (empty destination) is NOT a redirect; treated as a plain
// echo (which prints `TEXT >`).
func TestExecuteEchoRedirectEmptyPath(t *testing.T) {
	sh := newTestShell()
	out := sh.Execute("echo hi >")
	// The fallback is plain `echo hi >` -> prints "hi >".
	if len(out) != 1 || out[0] != "hi >" {
		t.Fatalf("echo hi > = %v", out)
	}
}

// parseEchoRedirect rejects lines that do not start with `echo`.
func TestParseEchoRedirectNonEcho(t *testing.T) {
	if _, _, ok := parseEchoRedirect("ls > x"); ok {
		t.Errorf("parseEchoRedirect(ls > x) ok=true, want false")
	}
	if _, _, ok := parseEchoRedirect("echo hi"); ok {
		t.Errorf("parseEchoRedirect(echo hi) ok=true, want false (no >)")
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
