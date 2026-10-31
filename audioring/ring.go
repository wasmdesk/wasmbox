// SPDX-License-Identifier: BSD-3-Clause

// Package audioring is the producer half of the wasmbox audio relay: a
// single-producer / single-consumer ring of interleaved float32 PCM that lives
// in a SharedArrayBuffer. The consumer is the AudioWorkletProcessor in
// audio-worklet.js, which reads the ring on the audio thread; nothing is
// posted per buffer.
//
// AudioContext does not exist in a DedicatedWorker, so a wasmbox client cannot
// play sound itself. It writes into this ring and the compositor page plays it.
//
// # Layout (shared with audio-proto.js and audio-worklet.js)
//
//	bytes 0..63   header, 16 Int32 words (little endian)
//	  [0] write   frames written, free-running uint32 (producer owns)
//	  [1] read    frames consumed, free-running uint32 (consumer owns)
//	  [2] underruns   consumer: starvation events after playback started
//	  [3] played      consumer: frames output
//	  [4] overruns    producer: frames dropped because the ring was full
//	  [5] state       0 open, 1 closed (set by either side; the consumer then stops)
//	bytes 64..    capacityFrames*channels float32, interleaved
//
// capacityFrames is a power of two, so the free-running counters stay correct
// across the 2^32 wrap. The producer stores the sample data first and the new
// write index last (an atomic store, which is a release); the consumer loads
// the write index first (an acquire) and then the samples, so it never sees an
// index ahead of its data.
//
// This package is platform neutral: the shared memory is reached through the
// Backing interface, so the index arithmetic is tested natively and fuzzed. The
// GOOS=js adapter is in js_wasm.go.
package audioring

import (
	"errors"
	"fmt"
)

// Header word indices and sizes. They mirror audio-proto.js.
const (
	HeaderBytes = 64

	HWrite    = 0
	HRead     = 1
	HUnderrun = 2
	HPlayed   = 3
	HOverrun  = 4
	HState    = 5

	StateOpen   = 0
	StateClosed = 1
)

// Bounds the compositor enforces (audio-proto.js). Values outside are refused
// there, so the client validates the same limits and fails early.
const (
	MinSampleRate = 8000
	MaxSampleRate = 192000
	MaxChannels   = 2
	MinFrames     = 256
	MaxFrames     = 65536
)

// Backing is the shared memory seen by the writer.
type Backing interface {
	// Load, Store and Add are atomic operations on header word i.
	Load(i int) int32
	Store(i int, v int32)
	Add(i int, d int32)
	// WriteSamples copies src into the sample area starting at float32
	// index at (not bytes).
	WriteSamples(at int, src []float32)
}

// ErrParams is wrapped by every parameter error from Validate and New.
var ErrParams = errors.New("audioring: bad parameters")

// SABBytes is the exact size the SharedArrayBuffer must have.
func SABBytes(channels, capacityFrames int) int {
	return HeaderBytes + capacityFrames*channels*4
}

// Validate applies the same bounds as the compositor.
func Validate(sampleRate, channels, capacityFrames int) error {
	switch {
	case sampleRate < MinSampleRate || sampleRate > MaxSampleRate:
		return fmt.Errorf("%w: sampleRate %d outside [%d,%d]", ErrParams, sampleRate, MinSampleRate, MaxSampleRate)
	case channels < 1 || channels > MaxChannels:
		return fmt.Errorf("%w: channels %d outside [1,%d]", ErrParams, channels, MaxChannels)
	case capacityFrames < MinFrames || capacityFrames > MaxFrames:
		return fmt.Errorf("%w: capacityFrames %d outside [%d,%d]", ErrParams, capacityFrames, MinFrames, MaxFrames)
	case capacityFrames&(capacityFrames-1) != 0:
		return fmt.Errorf("%w: capacityFrames %d is not a power of two", ErrParams, capacityFrames)
	}
	return nil
}

// Buffered is the number of frames written and not yet read, for free-running
// uint32 counters. It is the one piece of index arithmetic both sides share.
func Buffered(write, read uint32) uint32 { return write - read }

// Free is the room left in a ring of capacity frames. A reader index that is
// inconsistent (more buffered than capacity: a corrupted header) yields 0, never
// a negative or a wrapped-around huge value.
func Free(write, read uint32, capacity int) int {
	b := Buffered(write, read)
	if int64(b) >= int64(capacity) {
		return 0
	}
	return capacity - int(b)
}

// Writer is the producer. It is not safe for concurrent use: the ring is
// single-producer, so give it one goroutine.
type Writer struct {
	m        Backing
	channels int
	capacity int
	write    uint32
}

// New returns a writer for a ring already allocated and zeroed (a fresh
// SharedArrayBuffer is zero).
func New(m Backing, sampleRate, channels, capacityFrames int) (*Writer, error) {
	if err := Validate(sampleRate, channels, capacityFrames); err != nil {
		return nil, err
	}
	return newWriter(m, channels, capacityFrames), nil
}

// newWriter builds a writer from parameters Validate has already accepted.
func newWriter(m Backing, channels, capacityFrames int) *Writer {
	return &Writer{m: m, channels: channels, capacity: capacityFrames, write: uint32(m.Load(HWrite))}
}

// Free is the number of frames Write would accept now.
func (w *Writer) Free() int {
	return Free(w.write, uint32(w.m.Load(HRead)), w.capacity)
}

// Buffered is the number of frames queued for playback.
func (w *Writer) Buffered() int {
	return int(Buffered(w.write, uint32(w.m.Load(HRead))))
}

// Write queues interleaved samples (len(pcm) / channels frames; a trailing
// partial frame is ignored) and returns the number of frames accepted. It
// never blocks: frames that do not fit are dropped and counted in the overrun
// word, so a producer that outruns the consumer cannot stall the game loop.
// Once the ring is closed it accepts nothing.
func (w *Writer) Write(pcm []float32) int {
	frames := len(pcm) / w.channels
	if frames == 0 || w.m.Load(HState) != StateOpen {
		return 0
	}
	n := frames
	if f := w.Free(); n > f {
		w.m.Add(HOverrun, int32(n-f))
		n = f
	}
	if n == 0 {
		return 0
	}
	start := int(w.write&uint32(w.capacity-1)) * w.channels
	first := w.capacity*w.channels - start // floats until the end of the data area
	if take := n * w.channels; take <= first {
		w.m.WriteSamples(start, pcm[:take])
	} else {
		w.m.WriteSamples(start, pcm[:first])
		w.m.WriteSamples(0, pcm[first:take])
	}
	w.write += uint32(n)
	w.m.Store(HWrite, int32(w.write)) // publish last
	return n
}

// Stats is a snapshot of the consumer's counters.
type Stats struct {
	Played, Underruns, Overruns uint32
}

// Stats reads the counters.
func (w *Writer) Stats() Stats {
	return Stats{
		Played:    uint32(w.m.Load(HPlayed)),
		Underruns: uint32(w.m.Load(HUnderrun)),
		Overruns:  uint32(w.m.Load(HOverrun)),
	}
}

// Close tells the consumer to stop. Further writes are refused.
func (w *Writer) Close() { w.m.Store(HState, StateClosed) }
