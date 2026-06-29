// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's shell.go is the in-process "shell" the terminal client
// runs. v1 was a Fields-split builtin dispatcher; v2 layers a real POSIX-ish
// front-end (clients/terminal/internal/shellparse) on top, so the terminal
// now supports:
//
//   - pipelines via '|' (each stage's stdout becomes the next stage's stdin)
//   - I/O redirection '<' / '>' / '>>'
//   - statement sequencing with ';' / '&&' / '||'
//   - '$?' expansion to the previous command's exit code
//   - single- + double-quoted words and backslash escapes
//
// Built-ins that mutate shell state (cd / clear / help) STILL stay local
// because they aren't per-process tools; everything else routes through
// wasmdesk/coreutils via multicall.Dispatch using a per-stage fsx.Env.
//
// The prompt is decorated with the previous command's exit code when
// non-zero (e.g. "[1] /tmp $ "), so a failed command is visible at a glance
// without typing `echo $?`.

package scene

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	"github.com/wasmdesk/coreutils/multicall"
	corefsx "github.com/wasmdesk/coreutils/pkg/fsx"
	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
	"github.com/wasmdesk/wasmbox/clients/terminal/internal/shellparse"
)

// stdinBridgeSeq names the synthetic VFS file the stdin-bridge wrapper uses
// to materialize a pipeline stage's incoming stdin so tools that don't
// stream stdin (cat / head / tail / wc / grep / ...) still receive piped
// bytes. The counter keeps successive bridges distinct so a tool that
// leaves the file around (none currently does) doesn't collide across
// pipelines or terminals.
var stdinBridgeSeq uint64

// stdinBridgePath returns the next unique synthetic-stdin path. We put it
// under "/" with a leading dot so a stray `ls` in the terminal will not
// surface it. The path is removed by the wrapper after the tool returns.
func stdinBridgePath() string {
	n := atomic.AddUint64(&stdinBridgeSeq, 1)
	return fmt.Sprintf("/.shellstdin.%d", n)
}

// historyCap caps the in-memory command history at the same order of
// magnitude as Bash's default HISTSIZE (500). Older entries fall off the
// front when the cap is reached -- bounded memory, newest-wins.
const historyCap = 500

// Shell is the per-window terminal state. LastExit carries the most recent
// command's exit code so the prompt can paint "[N]" and the lexer can
// expand $? on the next line.
//
// Bash-style history recall: every successful Execute pushes the trimmed
// line into History (subject to dedup + cap). The HistIdx cursor and Stash
// support the Up / Down arrow navigation wired in scene.HandleKey:
//
//   - HistIdx == -1 means "user is editing a fresh line"; Up jumps the
//     cursor to the most recent entry after first stashing Line into Stash
//     so Down can restore it.
//   - HistIdx in [0, len(History)-1] points at the entry currently echoed
//     back into Line; Up decrements, Down increments, clamping at 0; Down
//     past the newest pops back to -1 and restores Stash.
//
// Any non-arrow key edit (printable / Backspace / Enter / Tab) resets
// HistIdx to -1 so the next Up restarts navigation from the new line --
// matches Bash's "edit detaches you from history" behaviour.
type Shell struct {
	Prompt   string
	Line     []byte
	History  [][]byte
	HistIdx  int    // -1 = fresh line; otherwise index into History.
	Stash    []byte // saved Line when navigation started from a non-empty edit.
	Cwd      string
	VFS      sharedvfs.VFS
	LastExit int
}

// NewShell returns a Shell rooted at "/" with a freshly seeded demo
// InMemoryVFS. Used by tests + by the non-wasm host (rbtest, native runs)
// where the IDB-backed VFS is unavailable.
func NewShell() *Shell {
	v := sharedvfs.NewInMemoryVFS()
	sharedvfs.SeedDemoTree(v)
	return NewShellWithVFS(v)
}

// NewShellWithVFS returns a Shell that speaks the supplied VFS. HistIdx is
// seeded to -1 (the "fresh line" sentinel) so a first Up arrow correctly
// jumps to the newest entry rather than into a zero-valued slot.
func NewShellWithVFS(v sharedvfs.VFS) *Shell {
	return &Shell{Prompt: " $ ", Cwd: "/", VFS: v, HistIdx: -1}
}

// PromptString returns the rendered prompt. When the last command failed
// we prepend "[N] " so the failure is visible without typing echo $?.
func (sh *Shell) PromptString() string {
	if sh.LastExit != 0 {
		return fmt.Sprintf("[%d] %s%s", sh.LastExit, sh.Cwd, sh.Prompt)
	}
	return sh.Cwd + sh.Prompt
}

// Execute parses one command line (with pipes/redirects/&&/||/;/$?) and
// runs it. The returned slice is the lines the renderer paints; LastExit
// is updated so the next prompt + the next line's $? expansion are right.
//
// Pure-shell builtins (cd / clear / help) short-circuit BEFORE the parser
// because they mutate Shell state and don't fit the per-stage tool model.
// `clear` is handled by the scene above (via IsClear); we keep a no-op
// branch so a Shell driven outside the scene (e.g. tests) still does the
// right thing.
func (sh *Shell) Execute(line string) []string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}
	sh.pushHistory(trimmed)

	// Local builtins that mutate shell state -- only intercept when the
	// line is JUST that builtin (no pipes/redirects/chains). A user typing
	// `cd /tmp && ls` should still hit the parser; `cd /tmp` alone takes
	// the fast path.
	if out, ok := sh.tryLocalBuiltin(trimmed); ok {
		return out
	}

	toks, err := shellparse.Lex(trimmed)
	if err != nil {
		sh.LastExit = 2
		return []string{err.Error()}
	}
	tree, err := shellparse.Parse(toks)
	if err != nil {
		sh.LastExit = 2
		return []string{err.Error()}
	}

	res := shellparse.Run(tree, sh.LastExit, &vfsExecAdapter{v: sh.VFS, cwd: sh.Cwd}, sh.dispatch)
	sh.LastExit = res.LastExit
	return splitLines(string(res.Stdout) + string(res.Stderr))
}

// pushHistory appends trimmed to History with two Bash-style rules:
//
//   - HISTCONTROL=ignoredups (the Bash default): if trimmed is identical to
//     the immediately previous entry, drop it -- repeated Enter on the same
//     command leaves history at the same size.
//   - HISTSIZE=historyCap: once the slice reaches the cap, drop the oldest
//     entry before appending so memory is bounded and newest-wins.
//
// We do NOT touch HistIdx here; it is the caller's job (HandleKey at Enter
// time) to reset navigation state, because pushHistory is also called from
// codepaths that may want to keep the cursor (none today, but the seam is
// useful for a future :history-merge / readline-style reload).
func (sh *Shell) pushHistory(trimmed string) {
	if n := len(sh.History); n > 0 && string(sh.History[n-1]) == trimmed {
		return
	}
	if len(sh.History) >= historyCap {
		// Drop the oldest entry. We could use sh.History[1:] but that
		// keeps the backing array growing without bound across many
		// rollovers; copy-in-place keeps the slice header stable.
		copy(sh.History, sh.History[1:])
		sh.History = sh.History[:len(sh.History)-1]
	}
	sh.History = append(sh.History, []byte(trimmed))
}

// HistoryUp moves the recall cursor one step further into the past and
// returns the line the caller should display in Line. When called from
// HistIdx == -1 it first stashes the current Line into Stash so a matching
// Down can restore it. ok=false means "no change" (history empty, already
// at oldest, etc.) -- HandleKey forwards that into a false return so the
// renderer skips a no-op repaint.
func (sh *Shell) HistoryUp() (line []byte, ok bool) {
	if len(sh.History) == 0 {
		return nil, false
	}
	if sh.HistIdx == -1 {
		// Stash a copy so subsequent Line mutations don't alias it.
		sh.Stash = append(sh.Stash[:0], sh.Line...)
		sh.HistIdx = len(sh.History) - 1
		return append([]byte(nil), sh.History[sh.HistIdx]...), true
	}
	if sh.HistIdx == 0 {
		// Clamped at oldest -- Bash beeps; we just no-op.
		return nil, false
	}
	sh.HistIdx--
	return append([]byte(nil), sh.History[sh.HistIdx]...), true
}

// HistoryDown moves the recall cursor one step toward the present. From a
// fresh line (HistIdx == -1) it no-ops; from the newest entry it falls back
// to -1 + the stashed line (which is the empty string when the user pressed
// Up from an empty Line). ok=false means "no change".
func (sh *Shell) HistoryDown() (line []byte, ok bool) {
	if sh.HistIdx == -1 {
		return nil, false
	}
	if sh.HistIdx < len(sh.History)-1 {
		sh.HistIdx++
		return append([]byte(nil), sh.History[sh.HistIdx]...), true
	}
	// At newest entry: pop back to the fresh-line slot + return the stash.
	sh.HistIdx = -1
	out := append([]byte(nil), sh.Stash...)
	sh.Stash = sh.Stash[:0]
	return out, true
}

// HistoryReset detaches the recall cursor without touching Line. Called by
// HandleKey on any input event other than ArrowUp / ArrowDown so the next
// Up restarts navigation from the just-edited line (Bash semantics).
func (sh *Shell) HistoryReset() {
	sh.HistIdx = -1
	sh.Stash = sh.Stash[:0]
}

// tryLocalBuiltin handles the three lines that don't compose with the
// shell grammar: `cd ...`, `clear`, `help`. Returns (output, true) when it
// handled the line. Anything with shell metacharacters (|, >, <, ;, &, $)
// falls through so the parser sees it. Caller has already trimmed leading
// and trailing whitespace, so strings.Fields below is guaranteed to return
// at least one element on a non-empty input.
func (sh *Shell) tryLocalBuiltin(line string) ([]string, bool) {
	if strings.ContainsAny(line, "|<>;&$\"'\\") {
		return nil, false
	}
	parts := strings.Fields(line)
	switch parts[0] {
	case "help":
		sh.LastExit = 0
		return []string{
			"builtins: " + strings.Join(append([]string{"cd", "clear", "help"}, multicall.Names()...), " "),
			"redirection: cmd > path / cmd >> path / cmd < path",
			"pipelines:   cmd1 | cmd2 | cmd3",
			"chaining:    a && b   |   a || b   |   a ; b   |   echo $?",
		}, true
	case "clear":
		sh.LastExit = 0
		return nil, true
	case "cd":
		sh.LastExit = 0
		out := sh.runCd(parts[1:])
		if len(out) > 0 {
			sh.LastExit = 1
		}
		return out, true
	}
	return nil, false
}

// dispatch is the DispatchFunc the shellparse executor calls for every
// pipeline stage. It builds a per-stage fsx.Env (cheap: a pointer to the
// same sharedvfs adapter) and routes to coreutils.Dispatch. Unknown names
// emit a "command not found" line + exit 127, matching real shells.
//
// Pipeline stdin bridge: most coreutils tools (cat/head/tail/wc/grep/...)
// take a positional FILE rather than streaming env.Stdin. To make
// pipelines like `cat /n.txt | head -n 2 | wc -l` work WITHOUT modifying
// coreutils, we run the tool a first time; if it returns Usage (2) with a
// "missing operand" stderr AND we have piped-in stdin bytes, we
// materialize those bytes to a synthetic VFS file and re-dispatch with
// the path appended to argv. The synthetic file is removed on return.
// Tools that don't need bridging (success on first try) pay only the
// drain cost (one ReadAll of the piped bytes).
func (sh *Shell) dispatch(name string, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if !multicall.Has(name) {
		fmt.Fprintf(stderr, "%s: command not found\n", name)
		return 127
	}
	// Drain stdin once so we can retry; cheap (in-memory bytes from the
	// upstream pipeline's bytes.Buffer).
	var inBytes []byte
	if stdin != nil {
		if b, err := io.ReadAll(stdin); err == nil {
			inBytes = b
		}
	}

	// First attempt: stdin as-is, original argv.
	var firstStdout, firstStderr bytes.Buffer
	env := &corefsx.Env{
		Args:   argv,
		Stdin:  bytes.NewReader(inBytes),
		Stdout: &firstStdout,
		Stderr: &firstStderr,
		Cwd:    sh.Cwd,
		FS:     newVFSAdapter(sh.VFS),
	}
	code := multicall.Dispatch(name, env)

	// Retry path: tool wants a file operand but we have piped-in bytes.
	// Match the suite's two failure shapes: "missing operand" (cat / head
	// / tail / wc) and "usage:" (grep needs PATTERN + FILE).
	if code == 2 && len(inBytes) > 0 && isMissingOperand(firstStderr.String()) {
		path := stdinBridgePath()
		if werr := sh.VFS.Write(path, inBytes); werr == nil {
			defer func() { _ = sh.VFS.Remove(path) }()
			var retryStdout, retryStderr bytes.Buffer
			retryArgv := append(append([]string{}, argv...), path)
			retryEnv := &corefsx.Env{
				Args:   retryArgv,
				Stdin:  bytes.NewReader(inBytes),
				Stdout: &retryStdout,
				Stderr: &retryStderr,
				Cwd:    sh.Cwd,
				FS:     newVFSAdapter(sh.VFS),
			}
			retryCode := multicall.Dispatch(name, retryEnv)
			// Hide the synthetic stdin path from the user-visible output.
			// wc / head / tail print the file name; sort / grep can echo
			// it back via -n prefixes. Replace it with "-" so the line
			// reads like a normal pipeline (the dash is the POSIX
			// convention for "stdin").
			cleanOut := bytes.ReplaceAll(retryStdout.Bytes(), []byte(path), []byte("-"))
			cleanErr := bytes.ReplaceAll(retryStderr.Bytes(), []byte(path), []byte("-"))
			_, _ = stdout.Write(cleanOut)
			_, _ = stderr.Write(cleanErr)
			return retryCode
		}
	}

	_, _ = stdout.Write(firstStdout.Bytes())
	_, _ = stderr.Write(firstStderr.Bytes())
	return code
}

// isMissingOperand recognises the two stderr shapes coreutils emits when a
// tool expected a FILE operand it didn't get: GNU-style "missing operand"
// (cat / head / tail / wc) and grep's compact "usage:" message. Either
// triggers the stdin-bridge retry in dispatch.
func isMissingOperand(s string) bool {
	return strings.Contains(s, "missing operand") || strings.Contains(s, "usage:")
}

// vfsExecAdapter wraps a sharedvfs.VFS for the shellparse VFS contract --
// the executor only needs Read + Write (for '<' / '>' / '>>'), so we keep
// the interface intentionally narrow. Path resolution against cwd happens
// here so callers can write `> out.txt` without an absolute path.
type vfsExecAdapter struct {
	v   sharedvfs.VFS
	cwd string
}

// Read translates the sharedvfs error vocabulary into a shell-shaped
// message ("no such file or directory" etc.) so the user sees the right
// suffix on redirect failures.
func (a *vfsExecAdapter) Read(p string) ([]byte, error) {
	b, err := a.v.Read(sharedvfs.Resolve(a.cwd, p))
	if err != nil {
		return nil, &vfsErr{msg: errString(err)}
	}
	return b, nil
}

// Write delegates and translates errors the same way.
func (a *vfsExecAdapter) Write(p string, data []byte) error {
	if err := a.v.Write(sharedvfs.Resolve(a.cwd, p), data); err != nil {
		return &vfsErr{msg: errString(err)}
	}
	return nil
}

// vfsErr is the typed error vfsExecAdapter returns so the executor's
// "shell: <path>: <err>" line shows a humane message instead of the
// sharedvfs sentinel prose.
type vfsErr struct{ msg string }

// Error returns the wrapped suffix (the executor prepends "shell: <path>:").
func (e *vfsErr) Error() string { return e.msg }

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

