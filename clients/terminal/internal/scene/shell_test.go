// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package scene

import "testing"

// NewShell stamps the default prompt + cwd.
func TestNewShellDefaults(t *testing.T) {
	sh := NewShell()
	if sh.Prompt != "$ " {
		t.Fatalf("default prompt = %q, want %q", sh.Prompt, "$ ")
	}
	if sh.Cwd != "/home/user" {
		t.Fatalf("default cwd = %q, want %q", sh.Cwd, "/home/user")
	}
	if sh.Line != nil || len(sh.History) != 0 {
		t.Fatalf("freshly built shell has non-empty edit state: %+v", sh)
	}
}

// Empty / whitespace-only lines produce no output and do NOT enter history.
func TestExecuteEmptyLine(t *testing.T) {
	sh := NewShell()
	for _, in := range []string{"", "   ", "\t  \t"} {
		if out := sh.Execute(in); out != nil {
			t.Fatalf("Execute(%q) = %v, want nil", in, out)
		}
	}
	if len(sh.History) != 0 {
		t.Fatalf("empty lines polluted history: %v", sh.History)
	}
}

// `echo` echoes its args back, joined by single spaces.
func TestExecuteEcho(t *testing.T) {
	sh := NewShell()
	out := sh.Execute("echo hello")
	if len(out) != 1 || out[0] != "hello" {
		t.Fatalf("echo hello = %v", out)
	}
	out = sh.Execute("echo  a  b  c")
	if len(out) != 1 || out[0] != "a b c" {
		t.Fatalf("echo a b c = %v", out)
	}
	out = sh.Execute("echo")
	if len(out) != 1 || out[0] != "" {
		t.Fatalf("bare echo = %v", out)
	}
}

// `help` lists the builtin set.
func TestExecuteHelp(t *testing.T) {
	sh := NewShell()
	out := sh.Execute("help")
	if len(out) != 2 {
		t.Fatalf("help = %v, want 2 lines", out)
	}
	if want := "builtins:"; out[0][:len(want)] != want {
		t.Fatalf("help first line = %q", out[0])
	}
}

// `clear` returns no output (the caller wipes the grid via IsClear()).
func TestExecuteClear(t *testing.T) {
	sh := NewShell()
	if out := sh.Execute("clear"); out != nil {
		t.Fatalf("clear = %v, want nil", out)
	}
}

// `date` is deterministic so the playwright probe can assert exact pixels.
func TestExecuteDate(t *testing.T) {
	sh := NewShell()
	out := sh.Execute("date")
	if len(out) != 1 || out[0] != "Fri Jun 26 12:00:00 UTC 2026" {
		t.Fatalf("date = %v", out)
	}
}

// `pwd` reflects sh.Cwd.
func TestExecutePwd(t *testing.T) {
	sh := NewShell()
	out := sh.Execute("pwd")
	if len(out) != 1 || out[0] != "/home/user" {
		t.Fatalf("pwd = %v", out)
	}
	sh.Cwd = "/tmp"
	out = sh.Execute("pwd")
	if out[0] != "/tmp" {
		t.Fatalf("pwd after cwd change = %v", out)
	}
}

// `ls` is a placeholder fixed listing.
func TestExecuteLs(t *testing.T) {
	sh := NewShell()
	out := sh.Execute("ls")
	if len(out) != 3 || out[0] != "dir/" || out[1] != "file1.txt" || out[2] != "file2.txt" {
		t.Fatalf("ls = %v", out)
	}
}

// Unknown command -> not-found line.
func TestExecuteUnknown(t *testing.T) {
	sh := NewShell()
	out := sh.Execute("frobnicate -x")
	if len(out) != 1 || out[0] != "frobnicate: command not found" {
		t.Fatalf("unknown = %v", out)
	}
}

// Execute populates history with the trimmed line.
func TestExecuteHistory(t *testing.T) {
	sh := NewShell()
	sh.Execute("  echo a  ")
	sh.Execute("ls")
	if len(sh.History) != 2 {
		t.Fatalf("history len = %d, want 2", len(sh.History))
	}
	if string(sh.History[0]) != "echo a" || string(sh.History[1]) != "ls" {
		t.Fatalf("history content = %q, %q", sh.History[0], sh.History[1])
	}
}

// IsClear is true for `clear` (any surrounding whitespace), false otherwise.
func TestIsClear(t *testing.T) {
	if !IsClear("clear") {
		t.Fatal("IsClear(\"clear\") = false")
	}
	if !IsClear("  clear  ") {
		t.Fatal("IsClear(\"  clear  \") = false")
	}
	if IsClear("clears") {
		t.Fatal("IsClear(\"clears\") = true")
	}
	if IsClear("") {
		t.Fatal("IsClear(\"\") = true")
	}
}
