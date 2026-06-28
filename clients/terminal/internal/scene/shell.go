// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's shell.go is the in-process "shell" the terminal client
// runs. It is intentionally small: a command line, an Execute() that
// dispatches to a fixed builtin table, and a cwd tracked as a real
// VFS-relative path. The shell now speaks clients/sharedvfs so its file
// commands (ls / cat / cd / mkdir / touch / rm / echo > path) observe and
// mutate the SAME tree the files browser paints.
//
// Why a separate Shell type rather than inlining into main: this layer is
// pure Go and unit-tested natively. The wasm side feeds it bytes from the
// input event stream and renders the output strings into the Grid; tests
// drive it through an InMemoryVFS without booting a browser.

package scene

import (
	"strings"

	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

// Shell is the per-window terminal state: the prompt template, the line
// being edited, the command history, the current working directory and a
// pluggable VFS the file commands act on. Cwd is always a Cleaned absolute
// path; the renderer formats the prompt as "<cwd> $ " each frame so users
// see where they are without typing pwd.
type Shell struct {
	// Prompt is the trailing prompt string drawn after the cwd (defaults to
	// " $ "). The renderer composes the visible prompt by concatenating
	// Cwd + Prompt so changing cwd updates the prompt automatically.
	Prompt string
	// Line is the byte buffer of the edit line (everything typed after the
	// prompt, before Enter); it is appended to by Print and shrunk by
	// Backspace via the State wrapper.
	Line []byte
	// History records the trimmed command lines executed in this session;
	// empty / whitespace-only lines do NOT enter history.
	History [][]byte
	// Cwd is the shell's current working directory. Defaults to "/", and is
	// updated by the `cd` builtin via sharedvfs.Resolve.
	Cwd string
	// VFS is the filesystem the shell's file commands speak to. Wired by
	// NewShellWithVFS; defaults (in NewShell) to a fresh demo InMemoryVFS so
	// the unit tests do not depend on the wasm entry point.
	VFS sharedvfs.VFS
}

// NewShell returns a Shell rooted at "/" with a freshly seeded demo
// InMemoryVFS. Used by tests + by the non-wasm host (rbtest, native runs)
// where the IDB-backed VFS is unavailable.
func NewShell() *Shell {
	v := sharedvfs.NewInMemoryVFS()
	sharedvfs.SeedDemoTree(v)
	return NewShellWithVFS(v)
}

// NewShellWithVFS returns a Shell that speaks the supplied VFS. The wasm
// entry point uses this to inject the IndexedDB-backed VFS so the shell and
// the files browser observe the same tree.
func NewShellWithVFS(v sharedvfs.VFS) *Shell {
	return &Shell{
		Prompt: " $ ",
		Cwd:    "/",
		VFS:    v,
	}
}

// PromptString returns the rendered prompt the State layer paints into the
// grid each time it lays down a fresh prompt line. We assemble cwd + Prompt
// here (rather than in writePrompt) so tests can assert the exact text.
func (sh *Shell) PromptString() string {
	return sh.Cwd + sh.Prompt
}

// Execute dispatches one command line and returns the output as a slice of
// lines (no trailing newline on the last line; the caller paints each as a
// CRLF-terminated row). An empty / whitespace-only line returns nil so the
// caller paints nothing but the next prompt.
//
// The dispatch is a fixed switch, not a registry, because the builtin set
// is closed by design -- a real shell would shell out to PATH but we have
// no PATH.
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
	case "echo":
		return []string{strings.Join(args, " ")}
	case "help":
		return []string{
			"builtins: echo help clear date pwd ls cd cat mkdir touch rm",
			"redirection: echo TEXT > PATH",
		}
	case "clear":
		// The clear builtin is interpreted by the caller (it owns the Grid);
		// returning a sentinel-free empty slice keeps the contract that the
		// caller paints whatever lines we hand back.
		return nil
	case "date":
		// Deterministic so the playwright probe can assert exact pixels.
		return []string{"Fri Jun 26 12:00:00 UTC 2026"}
	case "pwd":
		return []string{sh.Cwd}
	case "ls":
		return sh.runLs(args)
	case "cd":
		return sh.runCd(args)
	case "cat":
		return sh.runCat(args)
	case "mkdir":
		return sh.runMkdir(args)
	case "touch":
		return sh.runTouch(args)
	case "rm":
		return sh.runRm(args)
	default:
		return []string{cmd + ": command not found"}
	}
}

// IsClear reports whether a command line is the `clear` builtin (the caller
// uses this to wipe the Grid in addition to whatever Execute returned).
func IsClear(line string) bool {
	return strings.TrimSpace(line) == "clear"
}

// parseEchoRedirect detects the `echo TEXT > PATH` shape and returns the
// trimmed text + the destination path. Returns ok=false for lines that do
// not start with `echo`, do not contain `>`, or have no destination. The
// matcher is intentionally simple: the wasm shell has no globbing, no
// quoting, and no append (`>>`) -- everything between `echo ` and the first
// ` > ` is the literal write payload.
func parseEchoRedirect(line string) (text, path string, ok bool) {
	if !strings.HasPrefix(line, "echo ") && line != "echo" {
		return "", "", false
	}
	i := strings.Index(line, ">")
	if i < 0 {
		return "", "", false
	}
	// Strip the leading "echo" + the redirect token.
	left := strings.TrimSpace(line[len("echo"):i])
	right := strings.TrimSpace(line[i+1:])
	if right == "" {
		return "", "", false
	}
	return left, right, true
}

// runEchoRedirect writes text + "\n" to the destination path, resolved
// against the shell's cwd. Returns the empty slice on success (matching how
// `echo > path` produces no stdout in a real shell); the failure paths emit
// an error line.
func (sh *Shell) runEchoRedirect(text, path string) []string {
	abs := sharedvfs.Resolve(sh.Cwd, path)
	// strip a single pair of double quotes if the user wrote `echo "foo"`.
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		text = text[1 : len(text)-1]
	}
	if err := sh.VFS.Write(abs, []byte(text+"\n")); err != nil {
		return []string{"echo: " + path + ": " + errString(err)}
	}
	return nil
}

// runLs prints the directory listing -- directories first, then files,
// alphabetical within each group (sharedvfs already enforces that order in
// List). Args[0] selects a directory; missing args mean cwd.
func (sh *Shell) runLs(args []string) []string {
	dir := sh.Cwd
	if len(args) > 0 {
		dir = sharedvfs.Resolve(sh.Cwd, args[0])
	}
	entries, err := sh.VFS.List(dir)
	if err != nil {
		return []string{"ls: " + dir + ": " + errString(err)}
	}
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir {
			out = append(out, e.Name+"/")
			continue
		}
		out = append(out, e.Name)
	}
	return out
}

// runCd updates Cwd to the resolved target. An empty argv resolves to "/"
// (matching what `cd` does in plain bash without a HOME env). Targets that
// are not directories are reported as errors and leave Cwd unchanged.
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

// runCat prints the body of one or more files. A missing path is reported
// as a single error line; the rest of the args are still processed so the
// user sees every file that could not be read.
func (sh *Shell) runCat(args []string) []string {
	if len(args) == 0 {
		return []string{"cat: missing operand"}
	}
	var out []string
	for _, a := range args {
		abs := sharedvfs.Resolve(sh.Cwd, a)
		data, err := sh.VFS.Read(abs)
		if err != nil {
			out = append(out, "cat: "+a+": "+errString(err))
			continue
		}
		// Split on '\n' so each line is its own grid row; a trailing newline
		// is dropped (it would only emit an empty row that reads as a gap).
		text := string(data)
		for text != "" {
			i := strings.IndexByte(text, '\n')
			if i < 0 {
				out = append(out, text)
				text = ""
				break
			}
			out = append(out, text[:i])
			text = text[i+1:]
		}
	}
	return out
}

// runMkdir creates one or more directories at the resolved targets.
func (sh *Shell) runMkdir(args []string) []string {
	if len(args) == 0 {
		return []string{"mkdir: missing operand"}
	}
	var out []string
	for _, a := range args {
		abs := sharedvfs.Resolve(sh.Cwd, a)
		if err := sh.VFS.Mkdir(abs); err != nil {
			out = append(out, "mkdir: "+a+": "+errString(err))
		}
	}
	return out
}

// runTouch creates an empty file at each target (or no-op if it already
// exists -- real touch updates the mtime; the wasm clock makes that a moot
// point, so we only ensure the node exists).
func (sh *Shell) runTouch(args []string) []string {
	if len(args) == 0 {
		return []string{"touch: missing operand"}
	}
	var out []string
	for _, a := range args {
		abs := sharedvfs.Resolve(sh.Cwd, a)
		if _, err := sh.VFS.Stat(abs); err == nil {
			continue
		}
		if err := sh.VFS.Write(abs, nil); err != nil {
			out = append(out, "touch: "+a+": "+errString(err))
		}
	}
	return out
}

// runRm removes each target. Directory removal is recursive (sharedvfs
// guarantees that contract); a non-existent path is reported but does not
// abort the loop.
func (sh *Shell) runRm(args []string) []string {
	if len(args) == 0 {
		return []string{"rm: missing operand"}
	}
	var out []string
	for _, a := range args {
		abs := sharedvfs.Resolve(sh.Cwd, a)
		if err := sh.VFS.Remove(abs); err != nil {
			out = append(out, "rm: "+a+": "+errString(err))
		}
	}
	return out
}

// errString turns a sharedvfs sentinel into a short shell-style suffix.
// Kept local so the shell does not leak the package's full error prefix.
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
