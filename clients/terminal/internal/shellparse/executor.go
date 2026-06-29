// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package shellparse

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// VFS is the storage contract the executor needs for I/O redirection.
// Kept minimal so the scene package's sharedvfs adapter is a 6-line wrapper
// and tests can pass an in-memory map.
type VFS interface {
	Read(path string) ([]byte, error)
	Write(path string, data []byte) error
}

// DispatchFunc runs one command. It receives the command name (argv[0]),
// its argv (including argv[0]), the resolved stdin/stdout/stderr streams,
// and returns the command's exit code (0 = success).
type DispatchFunc func(name string, argv []string, stdin io.Reader, stdout, stderr io.Writer) int

// Result is the outcome of running one Line: the captured stdout (the bytes
// the user sees in the terminal), the captured stderr (also shown inline),
// and the exit code of the LAST evaluated unit (the value '$?' takes on
// after this line).
type Result struct {
	Stdout   []byte
	Stderr   []byte
	LastExit int
}

// Run executes line against the supplied vfs + dispatcher. initExit seeds
// the running LastExit -- the value '$?' expands to BEFORE this line runs.
// stdin to the first pipeline stage is empty (the terminal does not pipe
// shell-level stdin into the line); each pipeline collects its own
// stdout/stderr, merged into the Result's buffers in the order produced.
func Run(line *Line, initExit int, vfs VFS, dispatch DispatchFunc) Result {
	res := Result{LastExit: initExit}
	if line == nil || len(line.List) == 0 {
		return res
	}
	for _, ao := range line.List {
		ec := runAndOr(ao, vfs, dispatch, &res)
		res.LastExit = ec
	}
	return res
}

// runAndOr walks an AndOr chain honouring the '&&' / '||' guards. The first
// pipeline always runs; subsequent ones run only if the previous exit code
// matches the operator. Returns the exit code of the LAST evaluated stage.
// res.LastExit is updated AFTER each pipeline so later pipelines (and the
// $? expansion their argv may contain) see the live value.
func runAndOr(ao *AndOr, vfs VFS, dispatch DispatchFunc, res *Result) int {
	if len(ao.Chains) == 0 {
		return 0
	}
	last := runPipeline(ao.Chains[0].Pipe, vfs, dispatch, res)
	res.LastExit = last
	for _, ch := range ao.Chains[1:] {
		switch ch.Op {
		case OpAnd:
			if last != 0 {
				continue
			}
		case OpOr:
			if last == 0 {
				continue
			}
		}
		last = runPipeline(ch.Pipe, vfs, dispatch, res)
		res.LastExit = last
	}
	return last
}

// runPipeline runs the stages in order, threading each stage's stdout into
// the next stage's stdin via in-memory bytes.Buffers. Redirects override
// the pipe wires: '< path' replaces stdin, '> path' / '>> path' capture
// stdout to a buffer that is flushed to the VFS after the stage returns.
//
// Per-stage stderr is collected into res.Stderr regardless of pipes (real
// shells route stderr to the terminal, not to the next stage).
//
// The pipeline's exit code is the LAST stage's exit code. A redirect open
// error short-circuits the stage with exit 1 + a "shell: ..." stderr line.
func runPipeline(pipe *Pipeline, vfs VFS, dispatch DispatchFunc, res *Result) int {
	var prevOut bytes.Buffer
	var last int
	isLast := func(i int) bool { return i == len(pipe.Stages)-1 }
	for i, cmd := range pipe.Stages {
		var stdin io.Reader = &prevOut
		var stdout io.Writer
		var captureBuf *bytes.Buffer
		var redirOutPath string
		var redirAppend bool

		// Default stdout = next stage's stdin buffer, or the terminal's
		// stdout buffer if this is the last stage.
		var nextBuf bytes.Buffer
		if isLast(i) {
			stdout = &nextBuf // collected -> res.Stdout after the loop
		} else {
			stdout = &nextBuf // collected -> piped into next stage
		}

		// Expand any '$?' markers in argv + redirect paths using the
		// LIVE LastExit (so `false ; echo $?` sees the post-false exit).
		argv := expandArgv(cmd.Argv, res.LastExit)
		expandedRedirs := expandRedirects(cmd.Redirects, res.LastExit)

		// Apply this stage's redirects. We honour them in declared order,
		// so `> a > b` ends up writing to b (last-writer-wins for stdout).
		errBuf := bytes.Buffer{}
		stageStderr := io.Writer(&errBuf)
		failed := false
		for _, r := range expandedRedirs {
			switch r.Kind {
			case RedirIn:
				data, err := vfs.Read(r.Path)
				if err != nil {
					fmt.Fprintf(stageStderr, "shell: %s: %s\n", r.Path, err.Error())
					failed = true
					continue
				}
				stdin = bytes.NewReader(data)
			case RedirOut:
				captureBuf = &bytes.Buffer{}
				stdout = captureBuf
				redirOutPath = r.Path
				redirAppend = false
			case RedirAppend:
				captureBuf = &bytes.Buffer{}
				stdout = captureBuf
				redirOutPath = r.Path
				redirAppend = true
			}
		}

		var code int
		if failed {
			code = 1
		} else {
			code = dispatch(argv[0], argv, stdin, stdout, stageStderr)
		}

		// Flush a captured stdout to the VFS. Append re-reads + concats,
		// matching POSIX '>>'.
		if redirOutPath != "" && !failed {
			data := captureBuf.Bytes()
			if redirAppend {
				if old, err := vfs.Read(redirOutPath); err == nil {
					data = append(old, data...)
				}
			}
			if err := vfs.Write(redirOutPath, data); err != nil {
				fmt.Fprintf(stageStderr, "shell: %s: %s\n", redirOutPath, err.Error())
				code = 1
			}
		}

		// Stderr always flows to the terminal (never piped).
		res.Stderr = append(res.Stderr, errBuf.Bytes()...)

		// If this stage produced output that wasn't redirected, that output
		// becomes the next stage's stdin (or the user-visible stdout for the
		// last stage).
		if captureBuf == nil {
			if isLast(i) {
				res.Stdout = append(res.Stdout, nextBuf.Bytes()...)
			} else {
				prevOut = nextBuf
			}
		} else if !isLast(i) {
			// Captured to a file -> downstream stages see EMPTY stdin.
			prevOut = bytes.Buffer{}
		}

		last = code
	}
	return last
}

// expandArgv copies argv, replacing every dollarQMarker with the decimal
// of exit. We never mutate the parsed Command -- the AST may be reused.
func expandArgv(argv []string, exit int) []string {
	out := make([]string, len(argv))
	repl := strconv.Itoa(exit)
	for i, a := range argv {
		if strings.Contains(a, dollarQMarker) {
			out[i] = strings.ReplaceAll(a, dollarQMarker, repl)
		} else {
			out[i] = a
		}
	}
	return out
}

// expandRedirects mirrors expandArgv for redirect paths so `cat > /tmp/$?`
// resolves as the user expects.
func expandRedirects(rs []Redirect, exit int) []Redirect {
	if len(rs) == 0 {
		return rs
	}
	out := make([]Redirect, len(rs))
	repl := strconv.Itoa(exit)
	for i, r := range rs {
		out[i] = r
		if strings.Contains(r.Path, dollarQMarker) {
			out[i].Path = strings.ReplaceAll(r.Path, dollarQMarker, repl)
		}
	}
	return out
}
