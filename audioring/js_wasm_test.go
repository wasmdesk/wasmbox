// SPDX-License-Identifier: BSD-3-Clause

//go:build js && wasm

package audioring

import (
	"errors"
	"syscall/js"
	"testing"
)

// Run with: GOOS=js GOARCH=wasm go test -exec "$(go env GOROOT)/lib/wasm/go_js_wasm_exec" ./...
func TestOpenJS(t *testing.T) {
	var posted js.Value
	w, buf, err := Open(Options{SampleRate: 48000, Channels: 2, CapacityFrames: 512, WindowID: 7,
		Post: func(m js.Value) { posted = m }})
	if err != nil {
		t.Fatal(err)
	}
	if got := buf.Get("byteLength").Int(); got != SABBytes(2, 512) {
		t.Fatalf("byteLength = %d", got)
	}
	if posted.Get("type").String() != "audio_open" || posted.Get("window_id").Int() != 7 ||
		posted.Get("sampleRate").Int() != 48000 || posted.Get("channels").Int() != 2 ||
		posted.Get("capacityFrames").Int() != 512 || !posted.Get("sab").Equal(buf) {
		t.Fatalf("bad message: %v", posted)
	}

	// Write across the end of the data area and read it back through JS.
	pre := make([]float32, 500*2)
	w.Write(pre)
	atomics := js.Global().Get("Atomics")
	hdr := js.Global().Get("Int32Array").New(buf, 0, HeaderBytes/4)
	atomics.Call("store", hdr, HRead, 500) // consumer catches up
	in := make([]float32, 40*2)
	for i := range in {
		in[i] = float32(i) + 0.5
	}
	if n := w.Write(in); n != 40 {
		t.Fatalf("write = %d", n)
	}
	f32 := js.Global().Get("Float32Array").New(buf, HeaderBytes)
	for i := range in {
		at := (500*2 + i) % (512 * 2)
		if got := float32(f32.Index(at).Float()); got != in[i] {
			t.Fatalf("float %d = %v want %v", at, got, in[i])
		}
	}
	if got := atomics.Call("load", hdr, HWrite).Int(); got != 540 {
		t.Fatalf("write index = %d", got)
	}
	w.Write(make([]float32, 600*2)) // more than fits: the surplus is counted
	if w.Stats().Overruns == 0 {
		t.Fatal("overrun not counted through Atomics.add")
	}
	atomics.Call("store", hdr, HPlayed, 9)
	if w.Stats().Played != 9 {
		t.Fatal("stats do not see the consumer's counter")
	}
	w.Close()
	if atomics.Call("load", hdr, HState).Int() != StateClosed {
		t.Fatal("close not visible")
	}
}

func TestOpenJSDefaultPostAndErrors(t *testing.T) {
	g := js.Global()
	var got js.Value
	cb := js.FuncOf(func(this js.Value, args []js.Value) any { got = args[0]; return nil })
	defer cb.Release()
	g.Set("postMessage", cb)
	if _, _, err := Open(Options{SampleRate: 44100, Channels: 1, CapacityFrames: 256}); err != nil {
		t.Fatal(err)
	}
	if got.IsUndefined() || got.Get("type").String() != "audio_open" {
		t.Fatalf("default post not used: %v", got)
	}
	if _, _, err := Open(Options{SampleRate: 44100, Channels: 5, CapacityFrames: 256}); !errors.Is(err, ErrParams) {
		t.Fatalf("err = %v", err)
	}
}
