// SPDX-License-Identifier: BSD-3-Clause

//go:build js && wasm

package audioring

import (
	"math"
	"syscall/js"
	"testing"
)

// These tests run the real worklet under node (V8, real SharedArrayBuffer and
// Atomics) and compare its output with the closed-form oracle in voices_test.go.

type voiceRig struct {
	t      *testing.T
	proc   js.Value
	ctl    js.Value // Int32Array
	bank32 js.Value
	bank8  js.Value
	out    js.Value // [[Float32Array, Float32Array]]
	outU8  js.Value
	atom   js.Value
	nextW  int
	bankAt int
	pcmOf  map[int]*refVoice
	heard  [][2]float32 // every output frame so far
	ctxHz  int
}

func newVoiceRig(t *testing.T, ctxHz int) *voiceRig {
	t.Helper()
	g := js.Global()
	var cls js.Value
	reg := js.FuncOf(func(_ js.Value, a []js.Value) any {
		if a[0].String() == "wasmbox-voices" {
			cls = a[1]
		}
		return nil
	})
	g.Set("registerProcessor", reg)
	g.Set("sampleRate", ctxHz)
	g.Set("AudioWorkletProcessor", g.Call("eval", "(class AudioWorkletProcessor { constructor() { this.port = {}; } })"))
	g.Get("Function").New(string(WorkletJS)).Invoke()
	if cls.IsUndefined() {
		t.Fatal(`the worklet registers no "wasmbox-voices" processor`)
	}
	ctlSAB := g.Get("SharedArrayBuffer").New(VoicesSABBytes())
	bankSAB := g.Get("SharedArrayBuffer").New(MaxBankBytes / 64)
	opts := map[string]any{"processorOptions": map[string]any{
		"ctl": ctlSAB, "bank": bankSAB, "aheadFrames": 1103, "mixRate": MixRate,
	}}
	r := &voiceRig{t: t, ctxHz: ctxHz, atom: g.Get("Atomics"), pcmOf: map[int]*refVoice{}, bankAt: BankTableBytes}
	r.proc = cls.New(opts)
	r.ctl = g.Get("Int32Array").New(ctlSAB)
	r.bank32 = g.Get("Int32Array").New(bankSAB)
	r.bank8 = g.Get("Uint8Array").New(bankSAB)
	r.out = g.Get("Function").New("n", "return [[new Float32Array(n), new Float32Array(n)]]").Invoke(128)
	r.outU8 = g.Get("Uint8Array").New(r.out.Index(0).Index(0).Get("buffer"))
	return r
}

func (r *voiceRig) word(i int) int32 { return int32(r.atom.Call("load", r.ctl, i).Int()) }

// load puts a sample in the bank under id, as the producer does: data first,
// the ready word last.
func (r *voiceRig) load(id int, pcm []int32, bits, loopStart int) {
	r.t.Helper()
	var raw []byte
	for _, s := range pcm {
		if bits == 8 {
			raw = append(raw, byte(int8(s)))
		} else {
			raw = append(raw, byte(s), byte(s>>8))
		}
	}
	js.CopyBytesToJS(r.bank8.Call("subarray", r.bankAt, r.bankAt+len(raw)), raw)
	e := id * BankEntryWords
	for i, v := range []int{bits, MixRate, len(pcm), loopStart, r.bankAt, len(raw), 0} {
		r.atom.Call("store", r.bank32, e+1+i, v)
	}
	r.atom.Call("store", r.bank32, e, 1)
	r.bankAt += (len(raw) + 3) &^ 3
	r.pcmOf[id] = &refVoice{pcm: pcm, bits: bits, loopStart: loopStart}
}

func (r *voiceRig) send(c Command) {
	r.t.Helper()
	w, err := c.Encode()
	if err != nil {
		r.t.Fatal(err)
	}
	r.sendWords(w)
}

func (r *voiceRig) sendWords(w [4]int32) {
	at := VCmdWord + (r.nextW%CmdEntries)*CmdWords
	for i, v := range w {
		r.atom.Call("store", r.ctl, at+i, v)
	}
	r.nextW++
	r.atom.Call("store", r.ctl, VCmdWrite, r.nextW)
}

// quantum renders one 128-frame block and records it.
func (r *voiceRig) quantum() {
	r.t.Helper()
	if !r.proc.Call("process", js.Null(), r.out).Bool() {
		r.t.Fatal("process returned false")
	}
	raw := make([]byte, 128*4)
	for ch := 0; ch < 2; ch++ {
		buf := js.Global().Get("Uint8Array").New(r.out.Index(0).Index(ch).Get("buffer"))
		js.CopyBytesToGo(raw, buf)
		for i := 0; i < 128; i++ {
			v := math.Float32frombits(uint32(raw[4*i]) | uint32(raw[4*i+1])<<8 | uint32(raw[4*i+2])<<16 | uint32(raw[4*i+3])<<24)
			if ch == 0 {
				r.heard = append(r.heard, [2]float32{v, 0})
			} else {
				r.heard[len(r.heard)-128+i][1] = v
			}
		}
	}
}

// start sends a START and returns the oracle voice, which begins at the frame
// the worklet will paint next (the painted word before the command is applied).
func (r *voiceRig) start(slot int, gen uint16, id int, lv, rv int, loop bool) *refVoice {
	r.t.Helper()
	p := r.pcmOf[id]
	v := &refVoice{pcm: p.pcm, bits: p.bits, loopStart: p.loopStart, start: int(uint32(r.word(VPainted))),
		stop: -1, lv: int32(lv), rv: int32(rv), loop: loop}
	r.send(Command{Op: OpStart, Slot: slot, Gen: gen, Sample: id, Left: lv, Right: rv, Loop: loop})
	return v
}

func (r *voiceRig) expectExact(vs []*refVoice, from int) {
	r.t.Helper()
	for k := from; k < len(r.heard); k++ {
		l, rr := refFrame(vs, k)
		wl, wr := float32(l)/32768, float32(rr)/32768
		if r.heard[k] != [2]float32{wl, wr} {
			r.t.Fatalf("frame %d: heard %v, oracle %v", k, r.heard[k], [2]float32{wl, wr})
		}
	}
}

// loud reports whether any of the last n output frames is non-zero.
func (r *voiceRig) loud(n int) bool {
	for _, f := range r.heard[len(r.heard)-n:] {
		if f[0] != 0 || f[1] != 0 {
			return true
		}
	}
	return false
}

func ramp(n int, amp int32) []int32 {
	s := make([]int32, n)
	for i := range s {
		s[i] = (int32(i*37%255) - 127) * amp / 127
	}
	return s
}

func TestVoicesMixExactly(t *testing.T) {
	r := newVoiceRig(t, MixRate) // step 1: output frame k is paint frame k
	r.load(1, ramp(300, 100), 8, -1)
	r.load(2, ramp(400, 30000), 16, 50)
	r.quantum()
	var vs []*refVoice
	vs = append(vs, r.start(0, 1, 1, 200, 100, false))
	vs = append(vs, r.start(1, 1, 2, 255, 255, true))
	for i := 0; i < 8; i++ {
		r.quantum()
	}
	if got := r.word(VBadCmd); got != 0 {
		t.Fatalf("badcmd = %d", got)
	}
	// Twenty loud 16-bit voices: the sum passes int16 and must saturate.
	for s := 2; s < 22; s++ {
		vs = append(vs, r.start(s, 1, 2, 255, 255, true))
	}
	for i := 0; i < 30; i++ {
		r.quantum()
	}
	r.expectExact(vs, 0)
	if u, s := r.word(VUnderrun), r.word(VStarved); u != 0 || s != 0 {
		t.Fatalf("underruns %d starved %d", u, s)
	}
	if r.word(VPlayed) != int32(len(r.heard)) {
		t.Fatalf("played %d frames %d", r.word(VPlayed), len(r.heard))
	}
	// The saturating window is non-trivially loud (the test exercises the clamp).
	var peak float32
	for _, f := range r.heard {
		peak = max(peak, f[0])
	}
	if peak != 32767.0/32768 {
		t.Fatalf("peak = %v: the clamp was not reached", peak)
	}
}

func TestVoicesKeepPlayingWhenTheProducerStalls(t *testing.T) {
	r := newVoiceRig(t, MixRate)
	r.load(3, ramp(200, 20000), 16, 20)
	r.quantum()
	v := r.start(0, 1, 3, 255, 255, true)
	// 0.5 s of output with no command and no producer: the loop never ends.
	for i := 0; i < 50; i++ {
		r.quantum()
	}
	r.expectExact([]*refVoice{v}, 0)
	if r.word(VUnderrun) != 0 || r.word(VStarved) != 0 {
		t.Fatal("the worklet starved while the producer was away")
	}
	tail := r.heard[len(r.heard)-1000:]
	for _, f := range tail {
		if f[0] != 0 {
			return
		}
	}
	t.Fatal("the loop went silent")
}

func TestVoicesResample(t *testing.T) {
	r := newVoiceRig(t, 48000)
	r.load(4, ramp(500, 20000), 16, -1)
	r.quantum()
	v := r.start(0, 1, 4, 255, 255, false)
	for i := 0; i < 60; i++ {
		r.quantum()
	}
	step := float64(MixRate) / 48000
	for j, f := range r.heard {
		pos := float64(j) * step
		k := int(pos)
		fr := pos - float64(k)
		a, _ := refFrame([]*refVoice{v}, k)
		b, _ := refFrame([]*refVoice{v}, k+1)
		want := (float64(a) + (float64(b)-float64(a))*fr) / 32768
		if math.Abs(float64(f[0])-want) > 1e-4 {
			t.Fatalf("frame %d: heard %v, want %v", j, f[0], want)
		}
	}
}

func TestVoicesCommandsAndSnapshot(t *testing.T) {
	r := newVoiceRig(t, MixRate)
	r.load(5, ramp(2000, 50), 8, 0)
	r.quantum()
	v := r.start(7, 1, 5, 255, 255, false)
	r.quantum()
	if got := r.word(VSnapWord + 14); got != 1|1<<16 {
		t.Fatalf("snapshot word0 = %#x", got)
	}
	if rem := r.word(VSnapWord + 15); rem <= 0 || rem > 2000 {
		t.Fatalf("remaining = %d", rem)
	}
	// A STOP for another generation is ignored; the right one ends the voice.
	r.send(Command{Op: OpStop, Slot: 7, Gen: 9})
	r.quantum()
	if got := r.word(VSnapWord + 14); got != 1|1<<16 {
		t.Fatalf("a stale STOP stopped the voice: %#x", got)
	}
	v.stop = int(uint32(r.word(VPainted)))
	r.send(Command{Op: OpStop, Slot: 7, Gen: 1})
	r.quantum()
	if got := r.word(VSnapWord + 14); got != 1 {
		t.Fatalf("STOP did not free the slot: %#x", got)
	}
	if r.word(VSnapWord+15) != 0 {
		t.Fatal("a free slot reports frames remaining")
	}
	for i := 0; i < 20; i++ {
		r.quantum()
	}
	r.expectExact([]*refVoice{v}, 0)

	// Refusals: unloaded sample, bad generation jump on START, out of range words.
	bad := r.word(VBadCmd)
	r.send(Command{Op: OpStart, Slot: 1, Gen: 1, Sample: 900, Left: 1, Right: 1}) // never loaded
	r.sendWords([4]int32{OpStart | 200<<8, 5, 255 | 255<<16, 0})                  // slot 200
	r.sendWords([4]int32{OpStart, 5, 999, 0})                                     // volume 999
	r.sendWords([4]int32{99, 0, 0, 0})                                            // unknown op
	r.sendWords([4]int32{OpStart | 1<<8 | 5<<16, 5, 255 | 255<<16, 0})            // generation 5, current 0
	r.quantum()
	if got := r.word(VBadCmd) - bad; got != 5 {
		t.Fatalf("badcmd grew by %d, want 5", got)
	}
	if r.word(VSnapWord+2)>>16 != 0 {
		t.Fatal("a refused START made a voice")
	}

	// SETVOL then STOPALL.
	r.start(2, 1, 5, 255, 255, true)
	for i := 0; i < 12; i++ { // the voice starts one paint-ahead later
		r.quantum()
	}
	if !r.loud(128) {
		t.Fatal("the voice is silent before SETVOL")
	}
	r.send(Command{Op: OpSetVol, Slot: 2, Gen: 1, Left: 0, Right: 0})
	for i := 0; i < 12; i++ { // SETVOL lands one paint-ahead later
		r.quantum()
	}
	if r.loud(128) {
		t.Fatal("SETVOL 0 left the voice audible")
	}
	r.send(Command{Op: OpStopAll})
	r.quantum()
	for s := 0; s < VoiceSlots; s++ {
		if r.word(VSnapWord+2*s)>>16 != 0 {
			t.Fatalf("slot %d active after STOPALL", s)
		}
	}
}

func TestVoicesClose(t *testing.T) {
	r := newVoiceRig(t, MixRate)
	r.atom.Call("store", r.ctl, VState, StateClosed)
	if r.proc.Call("process", js.Null(), r.out).Bool() {
		t.Fatal("a closed ring keeps the node alive")
	}
	if r.word(VHBeat) != 1 {
		t.Fatalf("heartbeat = %d", r.word(VHBeat))
	}
}
