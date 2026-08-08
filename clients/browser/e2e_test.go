// Copyright (c) the wasmdesk/wasmbox authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !js

// Package-level end-to-end test for the browser client. It compiles the real
// js/wasm client, runs it under Node with a minimal wasmboxClient shim, and
// drives it against a fake browserpb.Browser server reached over the real
// grpc-transports/websocket transport. It proves the client's gRPC wiring
// actually runs in the browser target — it connects, sends its content-size
// resize, and paints the frame the server streams back — not merely that it
// compiles. It skips cleanly when Node or the toolchain wasm glue is absent.
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
// state, and for every client message replies with a solid-red frame (sized to
// the last resize) plus a state. It records whether it saw the client's resize.
type fakeBrowser struct {
	browserpb.UnimplementedBrowserServer
	mu       sync.Mutex
	sawResiz bool
	w, h     int
}

func (f *fakeBrowser) sawResize() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sawResiz
}

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
		w, h := f.size()
		frame := &browserpb.ServerMsg{Msg: &browserpb.ServerMsg_Frame{Frame: &browserpb.Frame{
			Png: redPNG(w, h), W: int32(w), H: int32(h),
		}}}
		st := &browserpb.ServerMsg{Msg: &browserpb.ServerMsg_State{State: &browserpb.State{Url: "http://red/", Title: "red"}}}
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
func redPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	red := color.RGBA{R: 0xff, A: 0xff}
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = red.R, 0, 0, 0xff
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
