// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package complete implements Tab autocompletion for the terminal client.
//
// The completion model is intentionally bash-lite:
//
//   - The "completion target" is the partial word the cursor sits on (or
//     immediately follows). It is delimited by unquoted whitespace.
//   - The first non-empty token on the line is a COMMAND position: matches
//     come from the supplied builtins list (sorted, prefix-matched).
//   - Any later token is an ARGUMENT position: matches are filenames in the
//     VFS, resolved against cwd if relative. The target is split on the last
//     '/' into a directory + a name prefix; the directory is listed and
//     entries whose Name starts with the prefix are kept. Directory matches
//     are returned with a trailing '/' so the user can immediately chain
//     `cd src/<TAB>` -> `cd src/foo/`.
//
// The function returns a Result the caller (the scene's HandleKey) uses to
// either splice a single match back into the line or to print a multi-match
// menu. Result keeps the surrounding text (Prefix + Suffix) untouched so the
// caller never has to re-parse the line.
package complete

import (
	"sort"
	"strings"

	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

// Result is the value Complete returns.
//
//   - Prefix is the unchanged portion of line BEFORE the completion target.
//     Always ends right where the partial word started; never has a trailing
//     space (the space is part of Prefix).
//   - Suffix is the unchanged portion of line AFTER the cursor. Usually
//     empty (Tab at end of line); kept so cursor-in-the-middle completion
//     works too.
//   - Matches is the sorted list of candidate completions. For a single-match
//     completion the caller splices Matches[0] between Prefix and Suffix.
//     For a multi-match completion the caller displays the list and leaves
//     the line as-is.
//   - Target is the partial word that was being completed -- handy for the
//     caller when it needs to compute a "longest common prefix" extension
//     itself (v0 does not auto-extend; v1 may).
type Result struct {
	Prefix  string
	Suffix  string
	Target  string
	Matches []string
}

// Complete inspects line at cursor, decides whether the cursor is at a
// command or argument position, and returns a Result describing the matches.
//
// builtins is the command-position candidate set (already sorted by the
// caller, but Complete sorts the result regardless so test assertions are
// stable). vfs is the filesystem to list for argument-position completion;
// cwd is the directory relative paths resolve against.
//
// cursor is clamped into [0, len(line)] so a caller that passed a stale
// cursor never panics. line may be empty -- in that case Complete returns
// every builtin as a multi-match (this is how "press Tab at an empty prompt
// to see all commands" works in bash).
func Complete(line string, cursor int, builtins []string, vfs sharedvfs.VFS, cwd string) Result {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(line) {
		cursor = len(line)
	}

	prefix, target, suffix := splitAtCursor(line, cursor)
	cmdPos := isCommandPosition(prefix)

	var matches []string
	if cmdPos {
		matches = matchBuiltins(target, builtins)
	} else {
		matches = matchPaths(target, vfs, cwd)
	}

	sort.Strings(matches)
	return Result{
		Prefix:  prefix,
		Suffix:  suffix,
		Target:  target,
		Matches: matches,
	}
}

// splitAtCursor finds the partial word the cursor sits on/after and
// returns (prefix, target, suffix). The split point is the most recent
// unquoted space at or before cursor; the end of the target is cursor
// itself. The returned strings concatenate exactly back to line.
//
// We keep this dependency-light (no shellparse import) because the lexer
// rejects unterminated quotes -- and a Tab on an in-progress word is by
// construction in the middle of typing, where quoting may not yet balance.
func splitAtCursor(line string, cursor int) (prefix, target, suffix string) {
	suffix = line[cursor:]
	left := line[:cursor]
	// Walk backwards to the last unescaped space. We treat single/double
	// quotes as "inside a quoted span" toggles so a space inside quotes
	// does NOT split the target -- a user typing `cat "my fi<TAB>` keeps
	// the whole quoted run as one target.
	inSingle, inDouble := false, false
	split := 0
	for i := 0; i < len(left); i++ {
		c := left[i]
		if c == '\\' && i+1 < len(left) {
			// Skip the escaped byte so it cannot toggle quoting or split.
			i++
			continue
		}
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == ' ' && !inSingle && !inDouble:
			split = i + 1
		}
	}
	prefix = left[:split]
	target = left[split:]
	return prefix, target, suffix
}

// isCommandPosition reports whether the next word starts a command. The
// rule is "every non-space rune in prefix is whitespace" -- if anything
// non-space sits in prefix, the user has already typed the command and we
// are completing an argument.
//
// A future iteration could honour pipe/&&/||/; -- the word right after one
// of those is also a command position. v0 keeps it simple: nothing other
// than spaces means argument.
func isCommandPosition(prefix string) bool {
	for i := 0; i < len(prefix); i++ {
		if prefix[i] != ' ' && prefix[i] != '\t' {
			return false
		}
	}
	return true
}

// matchBuiltins returns the sublist of builtins whose name starts with the
// (lowercased) target. Returns a fresh slice so callers can mutate freely.
func matchBuiltins(target string, builtins []string) []string {
	out := make([]string, 0, len(builtins))
	for _, b := range builtins {
		if strings.HasPrefix(b, target) {
			out = append(out, b)
		}
	}
	return out
}

// matchPaths returns the filenames in cwd (or in the dir embedded in target)
// whose basename starts with the name prefix embedded in target. Directories
// are suffixed with '/' so `cd src/<TAB>` flows into a chainable path.
//
// target shape examples:
//
//	"foo"          -> List(cwd), keep entries starting with "foo"
//	"sub/foo"      -> List(cwd+"/sub"), keep entries starting with "foo", prefix output with "sub/"
//	"/abs/foo"     -> List("/abs"),    keep entries starting with "foo", prefix output with "/abs/"
//	""             -> List(cwd), keep all entries
//
// Entries whose listing fails (missing directory, permission, etc.) yield
// no matches -- the user sees the original line restored, which is the
// graceful UX.
func matchPaths(target string, vfs sharedvfs.VFS, cwd string) []string {
	dirPart, namePrefix := splitPath(target)
	// Resolve dirPart against cwd. Empty dirPart means "the cwd itself".
	var listDir, displayDir string
	if dirPart == "" {
		listDir = cwd
		displayDir = ""
	} else {
		listDir = sharedvfs.Resolve(cwd, dirPart)
		displayDir = dirPart
	}
	entries, err := vfs.List(listDir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !strings.HasPrefix(e.Name, namePrefix) {
			continue
		}
		name := e.Name
		if e.IsDir {
			name += "/"
		}
		out = append(out, displayDir+name)
	}
	return out
}

// splitPath splits a path-shaped target into ("dir-with-trailing-slash",
// "basename-prefix"). "src/foo" -> ("src/", "foo"); "foo" -> ("", "foo");
// "/abs/foo" -> ("/abs/", "foo"); "/" -> ("/", ""); "" -> ("", "").
func splitPath(target string) (dirPart, namePrefix string) {
	i := strings.LastIndexByte(target, '/')
	if i < 0 {
		return "", target
	}
	return target[:i+1], target[i+1:]
}

// LongestCommonPrefix returns the longest leading run of bytes shared by
// every string in ss. Returns "" for an empty slice. Used by callers that
// want to extend the input by a partial-match common prefix before showing
// the multi-match menu (`fo` -> `foo` when matches are foo.txt, foobar.txt).
func LongestCommonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := ss[0]
	for _, s := range ss[1:] {
		n := len(p)
		if len(s) < n {
			n = len(s)
		}
		j := 0
		for j < n && p[j] == s[j] {
			j++
		}
		p = p[:j]
		if p == "" {
			break
		}
	}
	return p
}

// FormatColumns lays out matches into a column-packed multi-line string
// fitting into gridCols character columns. Bash's `complete -A` uses the
// same shape: a 2-space gap between cells, each cell padded to the widest
// item, candidates emitted row-major across the column grid (column 0 holds
// matches[0..rows-1], column 1 holds matches[rows..2*rows-1], ...).
//
// The return value is one "row" per element (each ending in '\n'-style row
// boundary -- handled by the caller; here we return a []string for direct
// row-by-row painting into the grid). An empty matches slice yields nil.
//
// Algorithm:
//
//	widest = max(len(m)) for m in matches
//	cell   = widest + 2 (the trailing gap is the column separator)
//	cols   = max(1, gridCols / cell)
//	rows   = ceil(len(matches) / cols)
//	row[r] = strings.Join(matches[r], matches[r+rows], ...), each padded to cell
//
// gridCols <= 0 is treated as a 1-column fallback (a degenerate terminal
// where nothing fits) -- still readable, never panics.
func FormatColumns(gridCols int, matches []string) []string {
	if len(matches) == 0 {
		return nil
	}
	widest := 0
	for _, m := range matches {
		if len(m) > widest {
			widest = len(m)
		}
	}
	cell := widest + 2
	// cell is always >= 2 (empty match -> widest=0, cell=2) so gridCols/cell
	// can be 0 only when gridCols < cell; in that case (and the gridCols<=0
	// degenerate case) we fall back to a single column.
	cols := 1
	if gridCols >= cell {
		cols = gridCols / cell
	}
	rows := (len(matches) + cols - 1) / cols
	out := make([]string, rows)
	var b strings.Builder
	for r := 0; r < rows; r++ {
		b.Reset()
		// Collect the indices of the cells that actually exist on this row.
		// The LAST one of those is rendered un-padded so we don't paint
		// trailing spaces over empty grid cells.
		lastCol := -1
		for c := 0; c < cols; c++ {
			if c*rows+r < len(matches) {
				lastCol = c
			}
		}
		for c := 0; c <= lastCol; c++ {
			idx := c*rows + r
			m := matches[idx]
			b.WriteString(m)
			if c < lastCol {
				for i := len(m); i < cell; i++ {
					b.WriteByte(' ')
				}
			}
		}
		out[r] = b.String()
	}
	return out
}
