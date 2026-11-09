// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/wasmdesk/wasmbox/clients/sharedvfs"
)

// newCompleteVFS seeds an InMemoryVFS with a small tree to list against.
//
//	/
//	  scratch/
//	    a.txt  apple.txt  banana.txt
//	    sub/
//	      x.txt
//	  about.txt
func newCompleteVFS(t *testing.T) sharedvfs.VFS {
	t.Helper()
	v := sharedvfs.NewInMemoryVFS()
	for _, d := range []string{"/scratch", "/scratch/sub"} {
		if err := v.Mkdir(d); err != nil {
			t.Fatal(err)
		}
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

// complete runs the shell's Completer the way lineedit calls it.
func complete(t *testing.T, cwd string, words []string, current string) ([]string, bool) {
	t.Helper()
	sh := NewShellWithVFS(newCompleteVFS(t))
	sh.Cwd = cwd
	cands, noSpace := sh.Completer()(context.Background(), words, current)
	var vals []string
	for _, c := range cands {
		vals = append(vals, c.Value)
	}
	return vals, noSpace
}

func TestCompleterEmptyLineListsEveryBuiltin(t *testing.T) {
	got, _ := complete(t, "/", nil, "")
	if !reflect.DeepEqual(got, completionBuiltins()) {
		t.Fatalf("got %v, want every builtin %v", got, completionBuiltins())
	}
	for _, want := range []string{"cd", "clear", "help", "echo", "printf", "ls"} {
		found := false
		for _, g := range got {
			found = found || g == want
		}
		if !found {
			t.Errorf("builtin %q missing from %v", want, got)
		}
	}
}

func TestCompleterCommandPosition(t *testing.T) {
	got, noSpace := complete(t, "/", nil, "ech")
	if !reflect.DeepEqual(got, []string{"echo"}) || noSpace {
		t.Fatalf("ech -> %v noSpace=%v, want [echo] false", got, noSpace)
	}
	got, _ = complete(t, "/", nil, "rm")
	if !reflect.DeepEqual(got, []string{"rm", "rmdir"}) {
		t.Fatalf("rm -> %v, want [rm rmdir]", got)
	}
	if got, _ = complete(t, "/", nil, "zz"); len(got) != 0 {
		t.Fatalf("zz -> %v, want none", got)
	}
}

func TestCompleterArguments(t *testing.T) {
	cases := []struct {
		name    string
		cwd     string
		current string
		want    []string
		noSpace bool
	}{
		{"relative file", "/", "ab", []string{"about.txt"}, false},
		{"absolute dir has slash and no space", "/", "/scr", []string{"/scratch/"}, true},
		{"absolute files sorted", "/", "/scratch/a", []string{"/scratch/a.txt", "/scratch/apple.txt"}, false},
		{"relative dir", "/scratch", "su", []string{"sub/"}, true},
		{"relative subdir keeps the typed directory", "/scratch", "sub/x", []string{"sub/x.txt"}, false},
		{"unknown directory", "/", "/nope/", nil, false},
		{"empty word lists the cwd", "/scratch", "", []string{"a.txt", "apple.txt", "banana.txt", "sub/"}, false},
	}
	for _, c := range cases {
		got, noSpace := complete(t, c.cwd, []string{"ls"}, c.current)
		if !reflect.DeepEqual(got, c.want) || noSpace != c.noSpace {
			t.Errorf("%s: %q in %s -> %v noSpace=%v, want %v %v", c.name, c.current, c.cwd, got, noSpace, c.want, c.noSpace)
		}
	}
}

func TestSplitPath(t *testing.T) {
	cases := []struct{ in, dir, name string }{
		{"", "", ""}, {"foo", "", "foo"}, {"src/foo", "src/", "foo"},
		{"/abs/foo", "/abs/", "foo"}, {"/", "/", ""}, {"a/", "a/", ""},
	}
	for _, c := range cases {
		if d, n := splitPath(c.in); d != c.dir || n != c.name {
			t.Errorf("splitPath(%q) = (%q, %q), want (%q, %q)", c.in, d, n, c.dir, c.name)
		}
	}
}

func TestCompletionBuiltinsMergedAndSorted(t *testing.T) {
	got := completionBuiltins()
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("not sorted at %d: %q > %q", i, got[i-1], got[i])
		}
	}
	if !strings.Contains(" "+strings.Join(got, " ")+" ", " cd ") {
		t.Fatalf("local builtin cd missing: %v", got)
	}
}
