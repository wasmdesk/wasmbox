// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package shellparse is the POSIX-ish shell front-end the terminal client
// uses to turn a typed command line into a runnable AST. v1 covers the
// features a user expects from "a real shell":
//
//   - Word splitting on whitespace, with double-quoted ("...") and single-
//     quoted ('...') strings kept as literal words (no $VAR interpolation
//     inside double quotes yet; that's a v2 nicety).
//
//   - Backslash escapes (\<char> -> <char>) outside single-quoted strings.
//
//   - Pipelines via '|' (stage N's stdout feeds stage N+1's stdin via an
//     in-memory bytes.Buffer; no goroutines, every stage runs synchronously).
//
//   - I/O redirection: '< path' reads stdin from a VFS file, '> path' writes
//     stdout to a VFS file (replacing), '>> path' appends to a VFS file.
//
//   - Statement sequencing with ';' (always run RHS), '&&' (run RHS only on
//     LHS exit 0), '||' (run RHS only on LHS exit != 0). The exit code of
//     a pipeline is its last stage's exit code; the exit code of an and-or
//     chain is the last evaluated stage's exit code.
//
//   - '$?' expansion to the literal digits of the last command's exit code,
//     performed at lex time. ($VAR / $0 / $1 are NOT done in v1.)
//
//   - '# comment' to end of line (only when '#' starts a word).
//
// The package is pure (no sharedvfs/coreutils imports): the executor calls
// out through two small interfaces (VFS for redirects, DispatchFunc for
// command invocation) supplied by the caller. That keeps the parser/lexer
// test-only with stdlib-only fakes and lets the scene package wire the real
// sharedvfs + coreutils on top.
package shellparse
