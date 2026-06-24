// SPDX-License-Identifier: BSD-3-Clause
//
// Cross-file consistency test for the step-C bridge protocol. The two ends of
// the main <-> compositor-worker channel live in separate files
// (index.html, compositor.worker.js) so a typo in a message type would
// silently break the page. This test parses the canonical list out of
// bridge.js and asserts that each constant:
//
//   1. appears in index.html  (the main-thread end)
//   2. appears in compositor.worker.js  (the worker end)
//
// It also asserts that the page no longer instantiates wasmbox.wasm directly:
// in step C, the wasm runtime must live in the worker only -- main MUST NOT
// load wasm_exec.js nor reference WebAssembly.instantiateStreaming.
//
//go:build !js
// +build !js

package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// readSource returns the contents of one of the project's source files,
// failing the test rather than the test harness if a file is missing.
func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// TestBridgeConstantsCoverBothEnds ensures every WASMBOX_BRIDGE.* constant
// declared in bridge.js is referenced from both index.html and
// compositor.worker.js, so neither end can drift unilaterally.
func TestBridgeConstantsCoverBothEnds(t *testing.T) {
	bridge := readSource(t, "bridge.js")
	indexHTML := readSource(t, "index.html")
	worker := readSource(t, "compositor.worker.js")

	// Each entry looks like `  M2C_BOOT: "boot",` -- pull the "boot" string.
	re := regexp.MustCompile(`(?m)^\s+([MC]2[MC]_[A-Z_]+):\s*"([^"]+)",`)
	matches := re.FindAllStringSubmatch(bridge, -1)
	if len(matches) == 0 {
		t.Fatalf("no WASMBOX_BRIDGE constants parsed out of bridge.js")
	}

	for _, m := range matches {
		name := m[1]
		// Skip TARGET_* entries -- they are values for dom_event.target, not
		// top-level message types, so the dom_event listener only references
		// them by name in one end (the relay).
		if strings.HasPrefix(name, "TARGET_") {
			continue
		}
		// The constant is referenced via B.<NAME> from the JS sources.
		ref := "B." + name
		if !strings.Contains(indexHTML, ref) && !strings.Contains(indexHTML, "B."+name) {
			// Main is the receiver for C2M_* and sender for M2C_*; either way
			// it must reference the constant somewhere if it is part of the
			// public contract. The exceptions are the *_RESIZE and
			// STORAGE_SNAPSHOT which main only sends -- both still appear via
			// B.<NAME>.
			t.Errorf("index.html does not reference bridge constant %s", name)
		}
		if !strings.Contains(worker, ref) {
			t.Errorf("compositor.worker.js does not reference bridge constant %s", name)
		}
	}
}

// TestMainThreadIsWasmFree asserts the page no longer loads the Go wasm shim
// nor calls WebAssembly.instantiateStreaming on the main thread. Step C moved
// all wasm work into compositor.worker.js; a regression that re-introduces it
// would defeat the isolation goal silently.
func TestMainThreadIsWasmFree(t *testing.T) {
	indexHTML := readSource(t, "index.html")
	if strings.Contains(indexHTML, "wasm_exec.js") {
		t.Errorf("index.html references wasm_exec.js on the main thread (step-C regression)")
	}
	if strings.Contains(indexHTML, "WebAssembly.instantiateStreaming") {
		t.Errorf("index.html instantiates wasm on the main thread (step-C regression)")
	}
	if !strings.Contains(indexHTML, "compositor.worker.js") {
		t.Errorf("index.html does not start the compositor worker")
	}
	if !strings.Contains(indexHTML, "transferControlToOffscreen") {
		t.Errorf("index.html does not transfer the canvas to the worker")
	}
}

// TestWorkerLoadsCompositorWasm pins the inverse: the worker MUST load
// wasm_exec.js + fetch wasmbox.wasm. Otherwise nothing renders.
func TestWorkerLoadsCompositorWasm(t *testing.T) {
	worker := readSource(t, "compositor.worker.js")
	for _, want := range []string{
		"wasm_exec.js",
		"wasmbox.wasm",
		"WebAssembly.instantiateStreaming",
		"new Go(",
	} {
		if !strings.Contains(worker, want) {
			t.Errorf("compositor.worker.js is missing required token %q", want)
		}
	}
}
