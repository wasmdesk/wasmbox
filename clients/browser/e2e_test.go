// Copyright (c) the wasmdesk/wasmbox authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !js

// Package-level end-to-end test for the browser client. It compiles the real
// js/wasm client, runs it under Node with a minimal wasmboxClient shim, and
// drives it against a fake browserpb.Browser server reached over the real
// grpc-transports/websocket transport. It proves the client's gRPC wiring
// actually runs in the browser target — not merely that it compiles — by
// asserting, in order: (0) the loading skeleton paints in the content area
// before the first frame (the server holds it back for a beat); (1) the client
// connects, sends its content-size resize, and paints the streamed RED frame,
// clearing the skeleton; (2) an injected address-bar navigation (click + type +
// Enter) makes the client send a Navigate, and the fresh GREEN frame the server
// streams back paints into the content area. It skips cleanly when Node or the
// toolchain wasm glue is absent.
package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-webengine/browserproxy/browserpb"
	wstransport "github.com/grpc-transports/websocket"
	"google.golang.org/grpc"
)

// fakeBrowser is a stub browserpb.Browser server: it sends the initial chrome
// state, then for every client message replies with a solid frame (sized to the
// last resize) plus a state. It holds back the FIRST frame for a beat so the
// client's loading skeleton is observable, streams RED until the client
// navigates, and switches to GREEN once it sees a Navigate — so the probe can
// assert an address-bar navigation streams a fresh, visibly different frame. It
// records whether it saw the client's resize.
type fakeBrowser struct {
	browserpb.UnimplementedBrowserServer
	mu        sync.Mutex
	sawResiz  bool
	sentFirst bool
	navigated bool
	w, h      int
}

func (f *fakeBrowser) sawResize() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sawResiz
}

// firstFrameHold is how long the server withholds its first frame after the
// stream opens, so the client paints (and the probe observes) the loading
// skeleton before the stream's first frame clears it.
const firstFrameHold = 350 * time.Millisecond

func (f *fakeBrowser) Session(stream browserpb.Browser_SessionServer) error {
	// Initial (empty) chrome state.
	if err := stream.Send(&browserpb.ServerMsg{Msg: &browserpb.ServerMsg_State{State: &browserpb.State{}}}); err != nil {
		return err
	}
	for {
		in, err := stream.Recv()
		if err != nil {
			return nil
		}
		if r := in.GetResize(); r != nil {
			f.mu.Lock()
			f.sawResiz, f.w, f.h = true, int(r.GetW()), int(r.GetH())
			f.mu.Unlock()
		}
		if in.GetNavigate() != nil {
			f.mu.Lock()
			f.navigated = true
			f.mu.Unlock()
		}

		f.mu.Lock()
		first := !f.sentFirst
		f.sentFirst = true
		green := f.navigated
		f.mu.Unlock()
		if first {
			// Hold the very first frame so the loading skeleton is on screen
			// long enough for the probe to catch it.
			time.Sleep(firstFrameHold)
		}

		w, h := f.size()
		png := redPNG(w, h)
		url, title := "http://red/", "red"
		if green {
			png = greenPNG(w, h)
			url, title = "http://green/", "green"
		}
		frame := &browserpb.ServerMsg{Msg: &browserpb.ServerMsg_Frame{Frame: &browserpb.Frame{
			Png: png, W: int32(w), H: int32(h),
		}}}
		st := &browserpb.ServerMsg{Msg: &browserpb.ServerMsg_State{State: &browserpb.State{Url: url, Title: title}}}
		if err := stream.Send(frame); err != nil {
			return err
		}
		if err := stream.Send(st); err != nil {
			return err
		}
	}
}

func (f *fakeBrowser) size() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.w > 0 && f.h > 0 {
		return f.w, f.h
	}
	return 64, 64
}

// redPNG returns a solid opaque-red w×h PNG.
func redPNG(w, h int) []byte { return solidPNG(w, h, color.RGBA{R: 0xff, A: 0xff}) }

// greenPNG returns a solid opaque-green w×h PNG — the post-navigation frame,
// visibly distinct from the initial red so the probe can prove the navigate
// streamed a fresh frame.
func greenPNG(w, h int) []byte { return solidPNG(w, h, color.RGBA{G: 0xff, A: 0xff}) }

// solidPNG returns a solid w×h PNG filled with c.
func solidPNG(w, h int, c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 0xff
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestBrowserWasmE2E(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found; skipping wasm e2e")
	}
	wasmExec := filepath.Join(runtime.GOROOT(), "lib", "wasm", "wasm_exec.js")
	if _, err := os.Stat(wasmExec); err != nil {
		t.Skipf("wasm_exec.js not found at %s; skipping", wasmExec)
	}

	tmp := t.TempDir()
	wasmPath := filepath.Join(tmp, "browser.wasm")
	build := exec.Command("go", "build", "-o", wasmPath, ".")
	build.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm", "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("wasm build failed: %v\n%s", err, out)
	}

	fake := &fakeBrowser{}
	lis, err := wstransport.ListenWebSocket("127.0.0.1:0", wstransport.ServerConfig{OriginPatterns: []string{"*"}})
	if err != nil {
		t.Fatalf("ListenWebSocket: %v", err)
	}
	gs := grpc.NewServer()
	browserpb.RegisterBrowserServer(gs, fake)
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()
	defer lis.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, filepath.Join(".", "e2e_shim.mjs"))
	cmd.Env = append(os.Environ(),
		"URL=ws://"+lis.Addr().String(),
		"WASM="+wasmPath,
		"WASM_EXEC="+wasmExec,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node host failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "WASM_OK") {
		t.Fatalf("browser wasm e2e did not report success:\n%s", out)
	}
	if !fake.sawResize() {
		t.Error("fake server never received the client's resize intent")
	}
	t.Logf("browser wasm e2e: %s", strings.TrimSpace(string(out)))
}
