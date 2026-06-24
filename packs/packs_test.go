// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.
//
// Tests for the B3 OCI image packs. Two things are verified per app:
//
//  1. The pack manifest's [files] section enumerates the right VFS names for
//     the OCI spawn path (worker.js + wasm_exec.js + the app's .wasm, plus
//     sdk.js where applicable). This catches typos and missed renames before
//     a build run.
//
//  2. When the previously-built bin/ociapps-pack CLI is present + the
//     prep step has materialised the required files, running the packer
//     produces an OCI image-layout directory whose manifest carries an
//     `ociapps.path/<name>` annotation for every file the TOML listed. This
//     end-to-end check is what `task ociapps:pack:<app>` does -- the test
//     just makes it run-once-in-CI rather than rely on a human's eyeball.
//
//  3. The push surface is exercised against a httptest.Server that mimics
//     the OCI Distribution v2 registry. The test packs hello, runs
//     bin/ociapps-push at the fake, and asserts the manifest landed under
//     /v2/hello/manifests/latest with the same bytes the packer wrote.
//
// All three tests are skipped when the CLI binaries don't exist on disk
// (i.e. nobody has run `task build:ociapps-cli` yet). CI runs them after
// the build step, so they fire there unconditionally.

package packs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// expectedFiles is the source-of-truth map this test asserts the .toml +
// the packer output both agree on. Keeping it in one place makes adding a
// new client a single-line edit (here + a packs/<app>.toml + a Taskfile
// entry, exactly what B3's mandate calls for).
var expectedFiles = map[string][]string{
	"hello":    {"worker.js", "wasm_exec.js", "sdk.js", "hello.wasm"},
	"dock":     {"worker.js", "wasm_exec.js", "sdk.js", "dock.wasm"},
	"terminal": {"worker.js", "wasm_exec.js", "sdk.js", "terminal.wasm"},
	"files":    {"worker.js", "wasm_exec.js", "sdk.js", "files.wasm"},
	"quake":    {"worker.js", "wasm_exec.js", "quake.wasm"},
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Walk up until we find go.mod (the wasmbox module root). Bounded loop
	// so a misplaced test under a worktree doesn't spin.
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			break
		}
		wd = parent
	}
	t.Fatalf("could not find wasmbox repo root from %s", wd)
	return ""
}

// TestPackManifestsListExpectedFiles asserts every packs/<app>.toml has a
// [files] section whose VFS names exactly match expectedFiles. The minimal
// TOML grammar the packer accepts is what we re-parse here.
func TestPackManifestsListExpectedFiles(t *testing.T) {
	root := repoRoot(t)
	for app, want := range expectedFiles {
		t.Run(app, func(t *testing.T) {
			path := filepath.Join(root, "packs", app+".toml")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			got := parseFilesSection(t, body)
			wantSet := make(map[string]struct{}, len(want))
			for _, w := range want {
				wantSet[w] = struct{}{}
			}
			if len(got) != len(wantSet) {
				t.Fatalf("%s: file count: want %d got %d (got=%v)", app, len(wantSet), len(got), keys(got))
			}
			for _, w := range want {
				if _, ok := got[w]; !ok {
					t.Errorf("%s: manifest missing %q (got=%v)", app, w, keys(got))
				}
			}
		})
	}
}

// TestPackCLIProducesAnnotatedLayout shells out to bin/ociapps-pack against
// each client and asserts the produced layout's manifest carries
// `ociapps.path/<name>` annotations for every expectedFile.
func TestPackCLIProducesAnnotatedLayout(t *testing.T) {
	root := repoRoot(t)
	packCLI := filepath.Join(root, "bin", "ociapps-pack")
	if _, err := os.Stat(packCLI); err != nil {
		t.Skipf("bin/ociapps-pack not present; run `task build:ociapps-cli` first (%v)", err)
	}
	// Only the apps whose source dirs have everything materialised on disk;
	// quake's wasm is ~11 MB + needs the engine sibling, so skip it unless
	// the file is already there.
	apps := []string{"hello", "dock", "terminal", "files"}
	if _, err := os.Stat(filepath.Join(root, "clients", "quake", "quake.wasm")); err == nil {
		apps = append(apps, "quake")
	}
	for _, app := range apps {
		t.Run(app, func(t *testing.T) {
			in := filepath.Join(root, "clients", app)
			ensurePrep(t, root, app)
			manifest := filepath.Join(root, "packs", app+".toml")
			out := t.TempDir()
			cmd := exec.Command(packCLI,
				"-in", in,
				"-manifest", manifest,
				"-out", out,
				"-ref", app+":latest")
			cmd.Dir = root
			combined, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("ociapps-pack %s: %v\n%s", app, err, combined)
			}
			ann := readManifestAnnotations(t, out)
			for _, name := range expectedFiles[app] {
				if _, ok := ann["ociapps.path/"+name]; !ok {
					t.Errorf("%s: annotation ociapps.path/%s missing (got=%v)", app, name, keys(ann))
				}
			}
		})
	}
}

// TestPushCLIRoundTrip packs hello, pushes it to a httptest server speaking
// the four endpoints the OCI Distribution v2 spec requires, and asserts the
// pushed manifest bytes match what was on disk.
func TestPushCLIRoundTrip(t *testing.T) {
	root := repoRoot(t)
	packCLI := filepath.Join(root, "bin", "ociapps-pack")
	pushCLI := filepath.Join(root, "bin", "ociapps-push")
	for _, b := range []string{packCLI, pushCLI} {
		if _, err := os.Stat(b); err != nil {
			t.Skipf("%s not present; run `task build:ociapps-cli` first (%v)", b, err)
		}
	}
	ensurePrep(t, root, "hello")

	tmp := t.TempDir()
	cmd := exec.Command(packCLI,
		"-in", filepath.Join(root, "clients", "hello"),
		"-manifest", filepath.Join(root, "packs", "hello.toml"),
		"-out", tmp,
		"-ref", "hello:latest")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pack: %v\n%s", err, out)
	}

	// Local manifest bytes (verifying the round-trip).
	idxBody, err := os.ReadFile(filepath.Join(tmp, "index.json"))
	if err != nil {
		t.Fatalf("read index.json: %v", err)
	}
	var idx struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(idxBody, &idx); err != nil {
		t.Fatalf("decode index.json: %v", err)
	}
	if len(idx.Manifests) == 0 {
		t.Fatalf("index.json has no manifests")
	}
	manifestDigest := idx.Manifests[0].Digest
	wantManifestBody, err := os.ReadFile(filepath.Join(tmp, "blobs", "sha256",
		strings.TrimPrefix(manifestDigest, "sha256:")))
	if err != nil {
		t.Fatalf("read manifest blob: %v", err)
	}

	reg := newFakeRegistry()
	srv := httptest.NewServer(reg)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	ref := u.Host + "/hello:latest"

	cmd = exec.Command(pushCLI, "-in", tmp, "-ref", ref, "-scheme", "http")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}

	got, ok := reg.getManifest("hello", "latest")
	if !ok {
		t.Fatalf("registry has no /v2/hello/manifests/latest after push")
	}
	if !bytes.Equal(got, wantManifestBody) {
		t.Fatalf("manifest mismatch:\n want sha256(%s)=%s\n got  sha256=%s",
			manifestDigest, sha256Hex(wantManifestBody), sha256Hex(got))
	}
}

// ensurePrep guarantees the sdk.js + wasm_exec.js + .wasm files exist for
// the named client. The task graph normally handles this; tests run in
// isolation so do the same work inline.
func ensurePrep(t *testing.T, root, app string) {
	t.Helper()
	dir := filepath.Join(root, "clients", app)
	needSDK := false
	for _, f := range expectedFiles[app] {
		if f == "sdk.js" {
			needSDK = true
			break
		}
	}
	if needSDK {
		if _, err := os.Stat(filepath.Join(dir, "sdk.js")); err != nil {
			src := filepath.Join(root, "clients", "sdk", "sdk.js")
			if err := copyFile(src, filepath.Join(dir, "sdk.js")); err != nil {
				t.Fatalf("copy sdk: %v", err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "wasm_exec.js")); err != nil {
		root := goroot()
		src := filepath.Join(root, "lib", "wasm", "wasm_exec.js")
		if _, err := os.Stat(src); err != nil {
			src = filepath.Join(root, "misc", "wasm", "wasm_exec.js")
		}
		if err := copyFile(src, filepath.Join(dir, "wasm_exec.js")); err != nil {
			t.Fatalf("copy wasm_exec.js: %v", err)
		}
	}
	wasm := app + ".wasm"
	if _, err := os.Stat(filepath.Join(dir, wasm)); err != nil {
		t.Skipf("%s/%s missing; run `task build:%s` first", dir, wasm, app)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func goroot() string {
	if r := runtime.GOROOT(); r != "" {
		return r
	}
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// parseFilesSection extracts the [files] key/value pairs from a packs/*.toml.
// Only the [files] section is read -- the test does not need to validate
// the full grammar (the packer's own tests do that).
func parseFilesSection(t *testing.T, body []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	section := ""
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		if section != "files" {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		k = strings.Trim(k, `"`)
		v = strings.Trim(v, `"`)
		out[k] = v
	}
	return out
}

// stripComment trims a trailing "# ..." comment outside of double-quotes.
func stripComment(s string) string {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case '#':
			if !inQuote {
				return strings.TrimSpace(s[:i])
			}
		}
	}
	return s
}

// readManifestAnnotations reads the manifest blob the packer pointed
// index.json at + returns its annotations map.
func readManifestAnnotations(t *testing.T, layout string) map[string]string {
	t.Helper()
	idxBody, err := os.ReadFile(filepath.Join(layout, "index.json"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var idx struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(idxBody, &idx); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	if len(idx.Manifests) == 0 {
		t.Fatalf("index has no manifests")
	}
	mBlob, err := os.ReadFile(filepath.Join(layout, "blobs", "sha256",
		strings.TrimPrefix(idx.Manifests[0].Digest, "sha256:")))
	if err != nil {
		t.Fatalf("read manifest blob: %v", err)
	}
	var m struct {
		Annotations map[string]string `json:"annotations"`
	}
	if err := json.Unmarshal(mBlob, &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return m.Annotations
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- fake OCI Distribution v2 registry -----------------------------------

// fakeRegistry implements just enough of /v2/* for ociapps-push to succeed:
// HEAD blobs, POST uploads/, PUT <Location>?digest=..., PUT manifests/<tag>.
// Storage is in-memory + per-repo (the spec dedups across tags within a repo).
type fakeRegistry struct {
	mu        sync.Mutex
	blobs     map[string]map[string][]byte // repo -> digest -> bytes
	manifests map[string]map[string][]byte // repo -> tag -> bytes
	uploads   map[string][]byte            // upload id -> bytes (chunked)
	nextID    int
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		blobs:     map[string]map[string][]byte{},
		manifests: map[string]map[string][]byte{},
		uploads:   map[string][]byte{},
	}
}

func (f *fakeRegistry) getManifest(repo, tag string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.manifests[repo]
	if !ok {
		return nil, false
	}
	b, ok := r[tag]
	return b, ok
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// All routes live under /v2/<repo>/...; the repo segment can span any
	// number of '/'-separated parts in real life, but our tests push just
	// "hello" so a single segment is enough.
	if !strings.HasPrefix(r.URL.Path, "/v2/") {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v2/")
	// Special case: /v2/<repo>/blobs/uploads/<id?>
	if strings.Contains(rest, "/blobs/uploads/") || strings.HasSuffix(rest, "/blobs/uploads") {
		f.handleUpload(w, r)
		return
	}
	// /v2/<repo>/blobs/<digest>
	if i := strings.Index(rest, "/blobs/"); i >= 0 {
		repo := rest[:i]
		digest := rest[i+len("/blobs/"):]
		f.handleBlob(w, r, repo, digest)
		return
	}
	// /v2/<repo>/manifests/<tag>
	if i := strings.Index(rest, "/manifests/"); i >= 0 {
		repo := rest[:i]
		tag := rest[i+len("/manifests/"):]
		f.handleManifest(w, r, repo, tag)
		return
	}
	http.NotFound(w, r)
}

func (f *fakeRegistry) handleBlob(w http.ResponseWriter, r *http.Request, repo, digest string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bag := f.blobs[repo]
	if r.Method == http.MethodHead {
		if bag == nil {
			http.NotFound(w, r)
			return
		}
		if _, ok := bag[digest]; !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method == http.MethodGet {
		if bag == nil {
			http.NotFound(w, r)
			return
		}
		b, ok := bag[digest]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(b)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (f *fakeRegistry) handleUpload(w http.ResponseWriter, r *http.Request) {
	// Two forms:
	//   POST /v2/<repo>/blobs/uploads/        -> 202 + Location: /uploads/<id>
	//   PUT  /v2/<repo>/blobs/uploads/<id>?digest=... -> 201 (stored)
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v2/"), "/")
	if len(parts) < 3 || parts[len(parts)-2] != "uploads" && parts[len(parts)-3] != "uploads" {
		// Fall through; not our shape.
	}
	repo := parts[0]
	if r.Method == http.MethodPost {
		f.nextID++
		id := fmt.Sprintf("u%d", f.nextID)
		f.uploads[id] = nil
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, id))
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if r.Method == http.MethodPut {
		// id is the last path segment
		id := parts[len(parts)-1]
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		digest := r.URL.Query().Get("digest")
		if digest == "" {
			http.Error(w, "missing digest", http.StatusBadRequest)
			return
		}
		// Verify the digest matches the bytes (spec requires it).
		want := strings.TrimPrefix(digest, "sha256:")
		got := sha256Hex(body)
		if got != want {
			http.Error(w, "digest mismatch", http.StatusBadRequest)
			return
		}
		if f.blobs[repo] == nil {
			f.blobs[repo] = map[string][]byte{}
		}
		f.blobs[repo][digest] = body
		delete(f.uploads, id)
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusCreated)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (f *fakeRegistry) handleManifest(w http.ResponseWriter, r *http.Request, repo, tag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodPut {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if f.manifests[repo] == nil {
			f.manifests[repo] = map[string][]byte{}
		}
		f.manifests[repo][tag] = body
		w.Header().Set("Docker-Content-Digest", "sha256:"+sha256Hex(body))
		w.WriteHeader(http.StatusCreated)
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		bag := f.manifests[repo]
		if bag == nil {
			http.NotFound(w, r)
			return
		}
		b, ok := bag[tag]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(b)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}
