// Copyright (c) 2026 the wasmdesk/wasmbox authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !js

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFileETag(t *testing.T) {
	dir := t.TempDir()
	name := "quake.wasm"
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte("AAAA"), 0o644); err != nil {
		t.Fatal(err)
	}

	e1, ok := fileETag(dir, "/"+name)
	if !ok || e1 == "" {
		t.Fatalf("fileETag = (%q, %v), want a non-empty etag", e1, ok)
	}
	// A second call returns the same etag (cache hit, unchanged file).
	if e2, ok := fileETag(dir, "/"+name); !ok || e2 != e1 {
		t.Errorf("second fileETag = (%q, %v), want stable %q", e2, ok, e1)
	}
	// Changing the content (different length -> size differs) changes the etag.
	if err := os.WriteFile(full, []byte("BBBBBB"), 0o644); err != nil {
		t.Fatal(err)
	}
	if e3, ok := fileETag(dir, "/"+name); !ok || e3 == e1 {
		t.Errorf("after edit fileETag = (%q, %v), want a NEW etag != %q", e3, ok, e1)
	}
	// Non-regular / missing paths yield ok=false (fall through to FileServer).
	if _, ok := fileETag(dir, "/does-not-exist"); ok {
		t.Error("missing file: want ok=false")
	}
	if _, ok := fileETag(dir, "/"); ok {
		t.Error("directory: want ok=false")
	}
	// Traversal is contained (../../etc/passwd resolves under dir, absent).
	if _, ok := fileETag(dir, "/../../../../etc/passwd"); ok {
		t.Error("traversal: want ok=false")
	}
}

// serveWithETag mirrors the dev handler's static path: set the content ETag,
// no-cache, then hand off to http.FileServer (whose ServeContent honours
// If-None-Match).
func serveWithETag(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		if etag, ok := fileETag(dir, r.URL.Path); ok {
			w.Header().Set("Etag", etag)
		}
		fs.ServeHTTP(w, r)
	})
}

func TestETagRevalidation(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "quake.wasm")
	if err := os.WriteFile(full, []byte("hello wasm"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := serveWithETag(dir)

	// First fetch: 200 with an ETag + body.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/quake.wasm", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first GET = %d, want 200", rec.Code)
	}
	etag := rec.Header().Get("Etag")
	if etag == "" || rec.Body.Len() == 0 {
		t.Fatalf("first GET: etag=%q bodyLen=%d, want both non-empty", etag, rec.Body.Len())
	}

	// Reload with the ETag: 304, NO body re-sent.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/quake.wasm", nil)
	req.Header.Set("If-None-Match", etag)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("revalidate unchanged = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 re-sent %d body bytes, want 0", rec.Body.Len())
	}

	// Change the content: same request now re-downloads (200 + new etag).
	if err := os.WriteFile(full, []byte("hello wasm v2 changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/quake.wasm", nil)
	req.Header.Set("If-None-Match", etag)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revalidate after change = %d, want 200 (re-download)", rec.Code)
	}
	if rec.Header().Get("Etag") == etag {
		t.Error("etag did not change after the file changed")
	}
}
