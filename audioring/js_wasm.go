// SPDX-License-Identifier: BSD-3-Clause

//go:build js && wasm

package audioring

import (
	"syscall/js"
	"unsafe"
)

// sab is the Backing over a SharedArrayBuffer.
type sab struct {
	atomics js.Value // the Atomics global
	hdr     js.Value // Int32Array over the header
	bytes   js.Value // Uint8Array over the whole buffer
}

func (s *sab) Load(i int) int32     { return int32(s.atomics.Call("load", s.hdr, i).Int()) }
func (s *sab) Store(i int, v int32) { s.atomics.Call("store", s.hdr, i, v) }
func (s *sab) Add(i int, d int32)   { s.atomics.Call("add", s.hdr, i, d) }

func (s *sab) WriteSamples(at int, src []float32) {
	b := unsafe.Slice((*byte)(unsafe.Pointer(&src[0])), len(src)*4)
	off := HeaderBytes + at*4
	js.CopyBytesToJS(s.bytes.Call("subarray", off, off+len(b)), b)
}

// Options configure Open.
type Options struct {
	SampleRate, Channels, CapacityFrames int
	// WindowID labels the session (informative; the compositor binds the ring to
	// the client's port, never to a client-supplied window id).
	WindowID int
	// Post sends a message to the compositor. Nil means the worker's
	// self.postMessage, which clients/*/worker.js route to the compositor port.
	Post func(msg js.Value)
}

// Open allocates the SharedArrayBuffer, announces it with an audio_open
// message and returns the writer. Writes are accepted from the moment it
// returns; sound starts when the compositor has attached the ring and the page
// has had a user gesture.
func Open(o Options) (*Writer, js.Value, error) {
	if err := Validate(o.SampleRate, o.Channels, o.CapacityFrames); err != nil {
		return nil, js.Undefined(), err
	}
	g := js.Global()
	buf := g.Get("SharedArrayBuffer").New(SABBytes(o.Channels, o.CapacityFrames))
	m := &sab{
		atomics: g.Get("Atomics"),
		hdr:     g.Get("Int32Array").New(buf, 0, HeaderBytes/4),
		bytes:   g.Get("Uint8Array").New(buf),
	}
	w := newWriter(m, o.Channels, o.CapacityFrames) // parameters validated above
	msg := js.ValueOf(map[string]any{
		"type":           "audio_open",
		"window_id":      o.WindowID,
		"sampleRate":     o.SampleRate,
		"channels":       o.Channels,
		"capacityFrames": o.CapacityFrames,
	})
	msg.Set("sab", buf)
	if o.Post != nil {
		o.Post(msg)
	} else {
		g.Call("postMessage", msg)
	}
	return w, buf, nil
}
