// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// completer.go is the terminal's Tab completion, as a go-widgets/lineedit
// Completer (it replaces the hand-written clients/terminal/internal/complete
// package). lineedit splits the line, unquotes the word under the cursor,
// takes the longest common prefix of several matches, lists the rest, and
// quotes what it inserts; this file only says WHICH words match.
//
// The model is bash-lite:
//
//   - The first word is a COMMAND position: matches are the builtins (the
//     local cd / clear / help plus every coreutils tool), prefix-matched.
//   - Any later word is an ARGUMENT position: matches are VFS entries,
//     resolved against the shell's working directory. The word is split on
//     its last '/' into a directory and a name prefix; the directory is
//     listed and the entries starting with the prefix are kept. A directory
//     match carries a trailing '/', and a unique one gets no trailing space,
//     so `cd src/<TAB>` chains into `cd src/foo/`.

package scene

import (
	"context"
	"sort"
	"strings"

	"github.com/go-widgets/lineedit"
	"github.com/wasmdesk/coreutils/multicall"
	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

// localBuiltinsForCompletion is the set of pure-shell builtins (cd / clear /
// help) the shell handles locally. The command-position candidates are this
// list UNIONed with multicall.Names(), so Tab at a fresh prompt sees every
// command the user can actually run.
var localBuiltinsForCompletion = []string{"cd", "clear", "help"}

// completionBuiltins returns the merged, sorted command names.
func completionBuiltins() []string {
	tools := multicall.Names()
	out := make([]string, 0, len(localBuiltinsForCompletion)+len(tools))
	out = append(out, localBuiltinsForCompletion...)
	out = append(out, tools...)
	sort.Strings(out)
	return out
}

// Completer returns the lineedit.Completer for this shell. It reads the
// shell's working directory when called, so a `cd` is followed at once.
func (sh *Shell) Completer() lineedit.Completer {
	return func(_ context.Context, words []string, current string) ([]lineedit.Candidate, bool) {
		var matches []string
		if len(words) == 0 {
			matches = matchBuiltins(current, completionBuiltins())
		} else {
			matches = matchPaths(current, sh.VFS, sh.Cwd)
		}
		sort.Strings(matches)
		cands := make([]lineedit.Candidate, len(matches))
		for i, m := range matches {
			cands[i] = lineedit.Candidate{Value: m}
		}
		// A unique directory ends in '/', and no space follows it.
		noSpace := len(matches) == 1 && strings.HasSuffix(matches[0], "/")
		return cands, noSpace
	}
}

// matchBuiltins returns the builtins whose name starts with target, in a
// fresh slice.
func matchBuiltins(target string, builtins []string) []string {
	out := make([]string, 0, len(builtins))
	for _, b := range builtins {
		if strings.HasPrefix(b, target) {
			out = append(out, b)
		}
	}
	return out
}

// matchPaths returns the entries of the directory embedded in target (the
// working directory when there is none) whose name starts with the name
// prefix embedded in target, each prefixed with that directory as typed and
// suffixed with '/' for a directory.
//
//	"foo"      -> List(cwd), entries starting with "foo"
//	"sub/foo"  -> List(cwd/sub), entries starting with "foo", shown as "sub/..."
//	"/abs/foo" -> List("/abs"), entries starting with "foo", shown as "/abs/..."
//	""         -> List(cwd), every entry
//
// A directory that cannot be listed yields no match.
func matchPaths(target string, vfs sharedvfs.VFS, cwd string) []string {
	dirPart, namePrefix := splitPath(target)
	listDir, displayDir := cwd, ""
	if dirPart != "" {
		listDir, displayDir = sharedvfs.Resolve(cwd, dirPart), dirPart
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
// "basename-prefix"): "src/foo" -> ("src/", "foo"); "foo" -> ("", "foo");
// "/" -> ("/", "").
func splitPath(target string) (dirPart, namePrefix string) {
	i := strings.LastIndexByte(target, '/')
	if i < 0 {
		return "", target
	}
	return target[:i+1], target[i+1:]
}
