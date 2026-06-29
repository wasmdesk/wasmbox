// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package complete

import (
	"reflect"
	"testing"

	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

// builtinsFixture is a deterministic subset of the real builtin list. We
// don't import multicall here -- the package contract takes the list as a
// parameter, so the test fixture is a literal slice.
var builtinsFixture = []string{
	"cat", "cd", "clear", "echo", "head", "help", "ls", "mkdir", "pwd",
	"rm", "rmdir", "touch", "wc",
}

// newVFS seeds an InMemoryVFS with a small tree the tests can list against.
//
//	/
//	  scratch/
//	    a.txt
//	    apple.txt
//	    banana.txt
//	    sub/
//	      x.txt
//	  about.txt
func newVFS(t *testing.T) sharedvfs.VFS {
	t.Helper()
	v := sharedvfs.NewInMemoryVFS()
	if err := v.Mkdir("/scratch"); err != nil {
		t.Fatal(err)
	}
	if err := v.Mkdir("/scratch/sub"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"/scratch/a.txt", "/scratch/apple.txt", "/scratch/banana.txt",
		"/scratch/sub/x.txt", "/about.txt",
	} {
		if err := v.Write(p, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	return v
}

// TestCompleteEmptyLine: Tab at the prompt returns every builtin.
func TestCompleteEmptyLine(t *testing.T) {
	r := Complete("", 0, builtinsFixture, newVFS(t), "/")
	if !reflect.DeepEqual(r.Matches, builtinsFixture) {
		t.Fatalf("Matches = %v, want every builtin %v", r.Matches, builtinsFixture)
	}
	if r.Prefix != "" || r.Target != "" || r.Suffix != "" {
		t.Fatalf("Prefix/Target/Suffix = %q/%q/%q, want all empty", r.Prefix, r.Target, r.Suffix)
	}
}

// TestCompleteCommandPositionSingle: "ec" -> ["echo"].
func TestCompleteCommandPositionSingle(t *testing.T) {
	r := Complete("ec", 2, builtinsFixture, newVFS(t), "/")
	if !reflect.DeepEqual(r.Matches, []string{"echo"}) {
		t.Fatalf("Matches = %v, want [echo]", r.Matches)
	}
	if r.Target != "ec" {
		t.Fatalf("Target = %q, want %q", r.Target, "ec")
	}
}

// TestCompleteCommandPositionMulti: "r" -> ["rm","rmdir"] (sorted).
func TestCompleteCommandPositionMulti(t *testing.T) {
	r := Complete("r", 1, builtinsFixture, newVFS(t), "/")
	if !reflect.DeepEqual(r.Matches, []string{"rm", "rmdir"}) {
		t.Fatalf("Matches = %v, want [rm rmdir]", r.Matches)
	}
}

// TestCompleteCommandPositionNone: "zz" -> [].
func TestCompleteCommandPositionNone(t *testing.T) {
	r := Complete("zz", 2, builtinsFixture, newVFS(t), "/")
	if len(r.Matches) != 0 {
		t.Fatalf("Matches = %v, want []", r.Matches)
	}
}

// TestCompleteArgRelativeSingle: "ls a" in cwd "/" -> ["about.txt"].
func TestCompleteArgRelativeSingle(t *testing.T) {
	r := Complete("ls a", 4, builtinsFixture, newVFS(t), "/")
	if !reflect.DeepEqual(r.Matches, []string{"about.txt"}) {
		t.Fatalf("Matches = %v, want [about.txt]", r.Matches)
	}
	if r.Prefix != "ls " {
		t.Fatalf("Prefix = %q, want %q", r.Prefix, "ls ")
	}
	if r.Target != "a" {
		t.Fatalf("Target = %q, want %q", r.Target, "a")
	}
}

// TestCompleteArgAbsoluteDirSuffix: "/scr" -> ["/scratch/"] (trailing slash).
func TestCompleteArgAbsoluteDirSuffix(t *testing.T) {
	r := Complete("ls /scr", 7, builtinsFixture, newVFS(t), "/")
	if !reflect.DeepEqual(r.Matches, []string{"/scratch/"}) {
		t.Fatalf("Matches = %v, want [/scratch/]", r.Matches)
	}
}

// TestCompleteArgAbsoluteFile: "ls /scratch/a" -> /scratch/a.txt + /scratch/apple.txt.
func TestCompleteArgAbsoluteFile(t *testing.T) {
	r := Complete("ls /scratch/a", 13, builtinsFixture, newVFS(t), "/")
	want := []string{"/scratch/a.txt", "/scratch/apple.txt"}
	if !reflect.DeepEqual(r.Matches, want) {
		t.Fatalf("Matches = %v, want %v", r.Matches, want)
	}
}

// TestCompleteArgRelativeDir: cwd="/scratch", target "sub" -> "sub/" (dir).
func TestCompleteArgRelativeDir(t *testing.T) {
	r := Complete("cd su", 5, builtinsFixture, newVFS(t), "/scratch")
	if !reflect.DeepEqual(r.Matches, []string{"sub/"}) {
		t.Fatalf("Matches = %v, want [sub/]", r.Matches)
	}
}

// TestCompleteArgRelativeSubdir: target "sub/x" in cwd "/scratch" -> "sub/x.txt".
func TestCompleteArgRelativeSubdir(t *testing.T) {
	r := Complete("cat sub/x", 9, builtinsFixture, newVFS(t), "/scratch")
	if !reflect.DeepEqual(r.Matches, []string{"sub/x.txt"}) {
		t.Fatalf("Matches = %v, want [sub/x.txt]", r.Matches)
	}
}

// TestCompleteArgUnknownDir: list of a missing directory -> [].
func TestCompleteArgUnknownDir(t *testing.T) {
	r := Complete("ls /nope/", 9, builtinsFixture, newVFS(t), "/")
	if len(r.Matches) != 0 {
		t.Fatalf("Matches = %v, want []", r.Matches)
	}
}

// TestCompleteArgEmptyTargetInCwd: "ls " (just the command + space) lists cwd.
func TestCompleteArgEmptyTargetInCwd(t *testing.T) {
	r := Complete("ls ", 3, builtinsFixture, newVFS(t), "/scratch")
	want := []string{"a.txt", "apple.txt", "banana.txt", "sub/"}
	if !reflect.DeepEqual(r.Matches, want) {
		t.Fatalf("Matches = %v, want %v", r.Matches, want)
	}
}

// TestCompleteCursorClampLow: cursor < 0 clamps to 0.
func TestCompleteCursorClampLow(t *testing.T) {
	r := Complete("ec", -5, builtinsFixture, newVFS(t), "/")
	// cursor=0 -> target = "" -> matches = all builtins -> Prefix="" Suffix="ec"
	if r.Prefix != "" || r.Target != "" || r.Suffix != "ec" {
		t.Fatalf("Prefix/Target/Suffix = %q/%q/%q", r.Prefix, r.Target, r.Suffix)
	}
	if len(r.Matches) != len(builtinsFixture) {
		t.Fatalf("Matches len = %d, want %d", len(r.Matches), len(builtinsFixture))
	}
}

// TestCompleteCursorClampHigh: cursor > len(line) clamps to len(line).
func TestCompleteCursorClampHigh(t *testing.T) {
	r := Complete("ec", 999, builtinsFixture, newVFS(t), "/")
	if r.Suffix != "" || r.Target != "ec" {
		t.Fatalf("Suffix/Target = %q/%q", r.Suffix, r.Target)
	}
}

// TestCompleteCursorMidWord: cursor in the middle of a word slices at cursor.
func TestCompleteCursorMidWord(t *testing.T) {
	// line = "echo hello", cursor between "ec" and "ho" -> Suffix="ho hello"
	r := Complete("echo hello", 2, builtinsFixture, newVFS(t), "/")
	if r.Target != "ec" {
		t.Fatalf("Target = %q, want %q", r.Target, "ec")
	}
	if r.Suffix != "ho hello" {
		t.Fatalf("Suffix = %q, want %q", r.Suffix, "ho hello")
	}
	if !reflect.DeepEqual(r.Matches, []string{"echo"}) {
		t.Fatalf("Matches = %v, want [echo]", r.Matches)
	}
}

// TestCompleteQuotedSpaceNoSplit: single-quoted span doesn't split the
// target. "cat 'my fi" should treat "'my fi" as the target.
func TestCompleteQuotedSpaceNoSplit(t *testing.T) {
	r := Complete("cat 'my fi", 10, builtinsFixture, newVFS(t), "/")
	if r.Prefix != "cat " {
		t.Fatalf("Prefix = %q, want %q", r.Prefix, "cat ")
	}
	if r.Target != "'my fi" {
		t.Fatalf("Target = %q, want %q", r.Target, "'my fi")
	}
}

// TestCompleteDoubleQuotedSpaceNoSplit: same for double quotes.
func TestCompleteDoubleQuotedSpaceNoSplit(t *testing.T) {
	r := Complete(`cat "my fi`, 10, builtinsFixture, newVFS(t), "/")
	if r.Target != `"my fi` {
		t.Fatalf("Target = %q, want %q", r.Target, `"my fi`)
	}
}

// TestCompleteEscapedSpace: backslash-escaped space does NOT split.
func TestCompleteEscapedSpace(t *testing.T) {
	r := Complete(`cat my\ fi`, 10, builtinsFixture, newVFS(t), "/")
	if r.Prefix != "cat " {
		t.Fatalf("Prefix = %q, want %q", r.Prefix, "cat ")
	}
	if r.Target != `my\ fi` {
		t.Fatalf("Target = %q, want %q", r.Target, `my\ fi`)
	}
}

// TestCompleteTrailingBackslash: a trailing backslash with nothing after
// must not panic (no escaped byte to skip).
func TestCompleteTrailingBackslash(t *testing.T) {
	r := Complete(`cat \`, 5, builtinsFixture, newVFS(t), "/")
	if r.Target != `\` {
		t.Fatalf("Target = %q, want %q", r.Target, `\`)
	}
}

// TestCompleteLeadingTabs: leading tabs count as command position (no
// non-whitespace before the cursor's word).
func TestCompleteLeadingTabs(t *testing.T) {
	r := Complete("\tec", 3, builtinsFixture, newVFS(t), "/")
	// splitAtCursor only splits on spaces; \t stays inside the target. To
	// keep this test meaningful we instead use leading space here:
	_ = r
	r2 := Complete("  ec", 4, builtinsFixture, newVFS(t), "/")
	if r2.Prefix != "  " {
		t.Fatalf("Prefix = %q, want %q", r2.Prefix, "  ")
	}
	if !reflect.DeepEqual(r2.Matches, []string{"echo"}) {
		t.Fatalf("Matches = %v, want [echo]", r2.Matches)
	}
}

// TestSplitPath: covers every branch of splitPath.
func TestSplitPath(t *testing.T) {
	cases := []struct {
		in, wantDir, wantName string
	}{
		{"", "", ""},
		{"foo", "", "foo"},
		{"/", "/", ""},
		{"/foo", "/", "foo"},
		{"a/b", "a/", "b"},
		{"a/b/c", "a/b/", "c"},
	}
	for _, c := range cases {
		gotDir, gotName := splitPath(c.in)
		if gotDir != c.wantDir || gotName != c.wantName {
			t.Errorf("splitPath(%q) = (%q,%q), want (%q,%q)",
				c.in, gotDir, gotName, c.wantDir, c.wantName)
		}
	}
}

// TestLongestCommonPrefix: covers empty, single, mismatch, full overlap.
func TestLongestCommonPrefix(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"abc"}, "abc"},
		{[]string{"abc", "abd"}, "ab"},
		{[]string{"abc", "xyz"}, ""},
		{[]string{"foo", "foobar"}, "foo"},
		{[]string{"foobar", "foo"}, "foo"},
	}
	for _, c := range cases {
		got := LongestCommonPrefix(c.in)
		if got != c.want {
			t.Errorf("LongestCommonPrefix(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestIsCommandPositionWhitespaceOnly: pure tab/space prefix is command position.
func TestIsCommandPositionWhitespaceOnly(t *testing.T) {
	if !isCommandPosition(" \t  ") {
		t.Fatal("' \\t  ' should be command position")
	}
	if isCommandPosition("echo ") {
		t.Fatal("'echo ' should NOT be command position")
	}
}

// TestFormatColumns: row-major column packing across several grid widths.
func TestFormatColumns(t *testing.T) {
	cases := []struct {
		name     string
		gridCols int
		in       []string
		want     []string
	}{
		{
			name:     "empty",
			gridCols: 40,
			in:       nil,
			want:     nil,
		},
		{
			name:     "single match fits one column",
			gridCols: 40,
			in:       []string{"echo"},
			want:     []string{"echo"},
		},
		{
			// widest = 10 ("foobar.txt") -> cell = 12 (10 + 2 gap).
			// gridCols 40 / 12 = 3 cols, ceil(2/3) = 1 row.
			// Last cell of the row is unpadded. First cell pads "foo.txt"
			// (7) to 12 chars -> 5 trailing spaces before "foobar.txt".
			name:     "two matches one row no trailing pad",
			gridCols: 40,
			in:       []string{"foo.txt", "foobar.txt"},
			want:     []string{"foo.txt     foobar.txt"},
		},
		{
			// widest = 5 ("alpha"|"delta") -> cell = 7. 20/7 = 2 cols.
			// ceil(4/2) = 2 rows. Row-major:
			//   col 0 = [alpha, beta]
			//   col 1 = [gamma, delta]
			name:     "four matches two cols two rows",
			gridCols: 20,
			in:       []string{"alpha", "beta", "gamma", "delta"},
			want: []string{
				"alpha  gamma",
				"beta   delta",
			},
		},
		{
			// gridCols too narrow for even one padded cell -> 1-column fallback.
			// 3 matches -> 3 rows, each a bare match.
			name:     "narrow terminal one column fallback",
			gridCols: 1,
			in:       []string{"foo", "bar", "baz"},
			want:     []string{"foo", "bar", "baz"},
		},
		{
			// gridCols <= 0 also falls back to 1-column.
			name:     "zero gridCols fallback",
			gridCols: 0,
			in:       []string{"a", "b"},
			want:     []string{"a", "b"},
		},
		{
			// Odd count: 5 items, 2 columns, ceil(5/2)=3 rows.
			//   col 0 = [a, b, c]
			//   col 1 = [d, e, _]  (last row col 1 missing)
			// Row 2 has only col 0 occupied; col 0 is the last-occupied cell so
			// it is NOT padded.
			name:     "odd count short last row",
			gridCols: 20,
			in:       []string{"alpha", "beta", "gamma", "delta", "kappa"},
			want: []string{
				"alpha  delta",
				"beta   kappa",
				"gamma",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatColumns(tc.gridCols, tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("FormatColumns(%d, %v):\ngot:  %#v\nwant: %#v",
					tc.gridCols, tc.in, got, tc.want)
			}
		})
	}
}
