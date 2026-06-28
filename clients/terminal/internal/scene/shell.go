// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's shell.go is the in-process "shell" the terminal client
// runs. It is intentionally small: a command line, an Execute() that
// dispatches to a fixed builtin table, and a cwd tracked as a real
// VFS-relative path.
//
// Most file-touching builtins (cat / ls / mkdir / touch / rm / pwd / echo /
// cp / mv / rmdir / head / tail / wc / grep / find) are delegated to the
// wasmdesk/coreutils suite via multicall.Dispatch. The terminal wraps its
// sharedvfs handle into a coreutils fsx.FS adapter and pipes the tool's
// stdout/stderr into the grid the renderer paints. Pure-shell builtins
// (cd / clear / date / help / `echo TEXT > PATH`) stay local because they
// either mutate shell state (cd) or do something the dispatcher does not
// model (clear).

package scene

import (
	"bytes"
	"strings"

	"github.com/wasmdesk/coreutils/multicall"
	corefsx "github.com/wasmdesk/coreutils/pkg/fsx"
	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

// Shell is the per-window terminal state.
type Shell struct {
	Prompt  string
	Line    []byte
	History [][]byte
	Cwd     string
	VFS     sharedvfs.VFS
}

// NewShell returns a Shell rooted at "/" with a freshly seeded demo
// InMemoryVFS. Used by tests + by the non-wasm host (rbtest, native runs)
// where the IDB-backed VFS is unavailable.
func NewShell() *Shell {
	v := sharedvfs.NewInMemoryVFS()
	sharedvfs.SeedDemoTree(v)
	return NewShellWithVFS(v)
}

// NewShellWithVFS returns a Shell that speaks the supplied VFS.
func NewShellWithVFS(v sharedvfs.VFS) *Shell {
	return &Shell{Prompt: " $ ", Cwd: "/", VFS: v}
}

// PromptString returns the rendered prompt.
func (sh *Shell) PromptString() string {
	return sh.Cwd + sh.Prompt
}

// Execute dispatches one command line. The pure-shell builtins (cd / clear /
// date / help / `echo TEXT > PATH`) stay local; everything else routes
// through coreutils.Dispatch so the terminal grows new commands by adding
// them to the dispatch table (zero terminal code per new tool).
func (sh *Shell) Execute(line string) []string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}
	sh.History = append(sh.History, []byte(trimmed))

	// `echo TEXT > PATH` is handled before generic field splitting so the
	// quoting-free "everything between echo and >" stays intact (a plain
	// strings.Fields would collapse double-spaces).
	if rest, path, ok := parseEchoRedirect(trimmed); ok {
		return sh.runEchoRedirect(rest, path)
	}

	parts := strings.Fields(trimmed)
	cmd, args := parts[0], parts[1:]
	switch cmd {
	case "help":
		return []string{
			"builtins: " + strings.Join(append([]string{"cd", "clear", "date", "help"}, multicall.Names()...), " "),
			"redirection: echo TEXT > PATH",
		}
	case "clear":
		return nil
	case "date":
		return []string{"Fri Jun 26 12:00:00 UTC 2026"}
	case "cd":
		return sh.runCd(args)
	}
	if multicall.Has(cmd) {
		return sh.runViaCoreutils(cmd, args)
	}
	return []string{cmd + ": command not found"}
}

// runViaCoreutils builds an fsx.Env, dispatches, and turns the tool's
// stdout/stderr bytes into the slice of lines the renderer paints. Each
// invocation builds a fresh env (cheap: a pointer to the same sharedvfs).
func (sh *Shell) runViaCoreutils(cmd string, args []string) []string {
	var out, errb bytes.Buffer
	env := &corefsx.Env{
		Args:   append([]string{cmd}, args...),
		Stdin:  bytes.NewReader(nil),
		Stdout: &out,
		Stderr: &errb,
		Cwd:    sh.Cwd,
		FS:     newVFSAdapter(sh.VFS),
	}
	_ = multicall.Dispatch(cmd, env)
	// Merge stdout + stderr so the user sees both inline (matching how a
	// real shell paints them onto the same TTY).
	return splitLines(out.String() + errb.String())
}

// splitLines turns a captured stdout/stderr blob into the slice the
// renderer paints. Trailing newlines are dropped (they would otherwise emit
// an empty row that reads as a gap).
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			return out
		}
		out = append(out, s[:i])
		s = s[i+1:]
	}
	return out
}

// IsClear reports whether a command line is the `clear` builtin.
func IsClear(line string) bool {
	return strings.TrimSpace(line) == "clear"
}

// parseEchoRedirect detects the `echo TEXT > PATH` shape.
func parseEchoRedirect(line string) (text, path string, ok bool) {
	if !strings.HasPrefix(line, "echo ") && line != "echo" {
		return "", "", false
	}
	i := strings.Index(line, ">")
	if i < 0 {
		return "", "", false
	}
	left := strings.TrimSpace(line[len("echo"):i])
	right := strings.TrimSpace(line[i+1:])
	if right == "" {
		return "", "", false
	}
	return left, right, true
}

// runEchoRedirect writes text + "\n" to the destination path. Kept local
// because coreutils.echo is pure stdout (no redirect), and adding shell
// redirection to coreutils would muddy a per-process tool interface.
func (sh *Shell) runEchoRedirect(text, path string) []string {
	abs := sharedvfs.Resolve(sh.Cwd, path)
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		text = text[1 : len(text)-1]
	}
	if err := sh.VFS.Write(abs, []byte(text+"\n")); err != nil {
		return []string{"echo: " + path + ": " + errString(err)}
	}
	return nil
}

// runCd updates Cwd. Stays local because changing shell state is not a
// per-process tool concern.
func (sh *Shell) runCd(args []string) []string {
	target := "/"
	if len(args) > 0 {
		target = sharedvfs.Resolve(sh.Cwd, args[0])
	}
	if !sh.VFS.IsDir(target) {
		return []string{"cd: " + target + ": not a directory"}
	}
	sh.Cwd = target
	return nil
}

// errString turns a sharedvfs sentinel into a short shell-style suffix.
func errString(err error) string {
	switch err {
	case sharedvfs.ErrNotFound:
		return "no such file or directory"
	case sharedvfs.ErrNotDir:
		return "not a directory"
	case sharedvfs.ErrIsDir:
		return "is a directory"
	case sharedvfs.ErrExists:
		return "file exists"
	case sharedvfs.ErrInvalid:
		return "invalid argument"
	}
	return err.Error()
}
