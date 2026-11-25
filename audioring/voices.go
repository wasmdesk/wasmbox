// SPDX-License-Identifier: BSD-3-Clause

package audioring

import (
	"errors"
	"fmt"
)

// Voice mixer: the second processor in audio-worklet.js, "wasmbox-voices".
//
// A producer that cannot be trusted to run on time (a game's worker thread that
// the OS deschedules for 100+ ms) must not be the thread that keeps the output
// fed. The voices processor moves the mixer itself onto the audio rendering
// thread: the producer loads PCM samples once into a shared bank, then sends
// small commands (start, stop, set volume) over a single-producer ring. The
// worklet paints the voices ahead of the play cursor, so sounds that are already
// playing, loops and ambients included, continue through a producer stall.
//
// # Control buffer (one SharedArrayBuffer, Int32 words)
//
//	words 0..15   header
//	  [0] cmdWrite   commands published, free-running (producer owns)
//	  [1] cmdRead    commands consumed, free-running (worklet owns)
//	  [2] underruns  quanta whose output the worklet could not render (always 0)
//	  [3] played     output frames rendered
//	  [5] state      0 open, 1 closed
//	  [6] heartbeat  +1 per process() call
//	  [7] starved    quanta in which the paint buffer could not be topped up
//	  [8] painted    mix-rate frames painted, free-running (worklet owns)
//	  [9] badcmd     commands the worklet refused
//	words 16..271  snapshot: VoiceSlots x 2 words (worklet owns)
//	  word 0: generation (16 bits) | active << 16
//	  word 1: frames remaining
//	words 272..   command ring: CmdEntries x 4 words (see Command)
//
// # Bank (a second SharedArrayBuffer)
//
// A table of MaxSamples entries of BankEntryWords Int32 words, then PCM data.
// The producer writes the PCM first and the entry's ready word last. Entry:
// [ready, bits, rate, nsamples, loopstart, byteOffset, byteLength, 0]. Samples
// are 8-bit signed or 16-bit little-endian signed, byteOffset 4-aligned.
//
// # Mixing
//
// Integer arithmetic exactly as Quake's mixer, into a 32-bit accumulator that
// is clamped to 16 bits only at transfer (WinQuake S_TransferPaintBuffer), so
// many loud voices saturate instead of wrapping:
//
//	8-bit : acc += int32(s) * vol / 256   (truncating division)
//	16-bit: acc += int32(s) * vol >> 8
//
// The paint buffer runs at MixRate and is read with linear interpolation, as
// WasmboxRing does.
const (
	VoiceSlots   = 128
	MaxSamples   = 1024
	CmdEntries   = 1024
	CmdWords     = 4
	MaxVolume    = 255
	MixRate      = 11025
	PaintFrames  = 2048 // paint buffer, frames
	MaxBankBytes = 16 << 20

	VCmdWrite = 0
	VCmdRead  = 1
	VUnderrun = 2
	VPlayed   = 3
	VState    = 5
	VHBeat    = 6
	VStarved  = 7
	VPainted  = 8
	VBadCmd   = 9

	VSnapWord = 16
	VCmdWord  = VSnapWord + 2*VoiceSlots

	BankEntryWords = 8
	BankTableBytes = MaxSamples * BankEntryWords * 4
)

// Command opcodes.
const (
	OpStart   = 1
	OpStop    = 2
	OpStopAll = 3
	OpSetVol  = 4
)

// ErrCommand is wrapped by every refusal from Encode and DecodeCommand.
var ErrCommand = errors.New("audioring: bad voice command")

// Command is one entry of the command ring. A command takes effect only when
// Gen is the slot's current generation or the next one, which resolves the race
// between a stale snapshot and a new command.
type Command struct {
	Op          int
	Slot        int
	Gen         uint16
	Sample      int
	Left, Right int
	Loop        bool
}

// VoicesSABBytes is the exact size of the control buffer.
func VoicesSABBytes() int { return (VCmdWord + CmdEntries*CmdWords) * 4 }

// Encode packs c into the four words that go into the ring.
//
//	w0: op | slot << 8 | gen << 16
//	w1: sample | loop << 16
//	w2: left | right << 16
//	w3: 0
func (c Command) Encode() ([4]int32, error) {
	var w [4]int32
	switch {
	case c.Op < OpStart || c.Op > OpSetVol:
		return w, fmt.Errorf("%w: op %d", ErrCommand, c.Op)
	case c.Slot < 0 || c.Slot >= VoiceSlots:
		return w, fmt.Errorf("%w: slot %d outside [0,%d)", ErrCommand, c.Slot, VoiceSlots)
	case c.Sample < 0 || c.Sample >= MaxSamples:
		return w, fmt.Errorf("%w: sample %d outside [0,%d)", ErrCommand, c.Sample, MaxSamples)
	case c.Left < 0 || c.Left > MaxVolume || c.Right < 0 || c.Right > MaxVolume:
		return w, fmt.Errorf("%w: volume (%d,%d) outside [0,%d]", ErrCommand, c.Left, c.Right, MaxVolume)
	}
	loop := int32(0)
	if c.Loop {
		loop = 1
	}
	w[0] = int32(uint32(c.Op) | uint32(c.Slot)<<8 | uint32(c.Gen)<<16)
	w[1] = int32(uint32(c.Sample) | uint32(loop)<<16)
	w[2] = int32(uint32(c.Left) | uint32(c.Right)<<16)
	return w, nil
}

// DecodeCommand is the validation the worklet applies to every word it reads
// from the ring: the reference for it, and the fuzz target's oracle. It accepts
// exactly what Encode produces.
func DecodeCommand(w [4]int32) (Command, error) {
	u0, u1, u2 := uint32(w[0]), uint32(w[1]), uint32(w[2])
	c := Command{
		Op:     int(u0 & 0xff),
		Slot:   int(u0 >> 8 & 0xff),
		Gen:    uint16(u0 >> 16),
		Sample: int(u1 & 0xffff),
		Loop:   u1>>16 == 1,
		Left:   int(u2 & 0xffff),
		Right:  int(u2 >> 16),
	}
	if u1>>16 > 1 || w[3] != 0 {
		return Command{}, fmt.Errorf("%w: reserved bits set", ErrCommand)
	}
	if _, err := c.Encode(); err != nil {
		return Command{}, err
	}
	return c, nil
}
