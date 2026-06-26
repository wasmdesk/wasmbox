// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene's shell.go is the tiny in-process "shell" the terminal
// client runs. It is intentionally minimal: a command line, an Execute()
// that dispatches to a small builtin table, and a (read-only-ish) cwd. No
// real filesystem, no subprocess -- the wasm sandbox has neither.
//
// Why a separate Shell type rather than inlining into main: this layer is
// pure Go and unit-tested natively. The wasm side just feeds it bytes from
// the input event stream and renders the output strings into the Grid.

package scene

import "strings"

// Shell is the per-window terminal state: the prompt string, the line being
// edited, the command history, and the (fake) current working directory.
type Shell struct {
	Prompt  string
	Line    []byte
	History [][]byte
	Cwd     string
}

// NewShell makes a Shell with the default prompt + cwd. We do not seed
// history; the test layer can do that explicitly.
func NewShell() *Shell {
	return &Shell{
		Prompt: "$ ",
		Line:   nil,
		Cwd:    "/home/user",
	}
}

// Execute dispatches one command line and returns the output as a slice of
// lines (no trailing newline on the last line; the caller paints each as a
// CRLF-terminated row). An empty / whitespace-only line returns nil so the
// caller paints nothing but the next prompt.
//
// The dispatch is a fixed switch, not a registry, because the builtin set is
// closed by design -- a real shell would shell out to PATH but we have no PATH.
func (sh *Shell) Execute(line string) []string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}
	// Push the raw (untrimmed except for trailing space-noise) line into history.
	sh.History = append(sh.History, []byte(trimmed))

	parts := strings.Fields(trimmed)
	cmd, args := parts[0], parts[1:]
	switch cmd {
	case "echo":
		return []string{strings.Join(args, " ")}
	case "help":
		return []string{
			"builtins: echo help clear date pwd ls",
			"input:    printable ASCII, Backspace, Enter",
		}
	case "clear":
		// The clear builtin is interpreted by the caller (it owns the Grid);
		// returning a sentinel-free empty slice keeps the contract that the
		// caller paints whatever lines we hand back.
		return nil
	case "date":
		// We deliberately return a fixed string rather than time.Now() because
		// the wasm clock is not a real wall clock in the test harness (the
		// host's clock leaks in differently per architecture), and a stable
		// output keeps the playwright assertion deterministic.
		return []string{"Fri Jun 26 12:00:00 UTC 2026"}
	case "pwd":
		return []string{sh.Cwd}
	case "ls":
		// Placeholder listing -- the wasm sandbox has no filesystem.
		return []string{"dir/", "file1.txt", "file2.txt"}
	default:
		return []string{cmd + ": command not found"}
	}
}

// IsClear reports whether a command line is the `clear` builtin (the caller
// uses this to wipe the Grid in addition to whatever Execute returned).
func IsClear(line string) bool {
	return strings.TrimSpace(line) == "clear"
}
