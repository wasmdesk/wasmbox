// SPDX-License-Identifier: BSD-3-Clause

package audioring

import (
	"errors"
	"os"
	"regexp"
	"strconv"
	"testing"
)

func TestCommandRoundTrip(t *testing.T) {
	for _, c := range []Command{
		{Op: OpStart, Slot: 0, Gen: 0, Sample: 0, Left: 0, Right: 0},
		{Op: OpStart, Slot: 127, Gen: 0xffff, Sample: 1023, Left: 255, Right: 255, Loop: true},
		{Op: OpStop, Slot: 5, Gen: 9},
		{Op: OpStopAll},
		{Op: OpSetVol, Slot: 3, Gen: 1, Left: 10, Right: 200},
	} {
		w, err := c.Encode()
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		got, err := DecodeCommand(w)
		if err != nil || got != c {
			t.Fatalf("%+v -> %v -> %+v, %v", c, w, got, err)
		}
	}
}

func TestCommandRefusals(t *testing.T) {
	bad := []Command{
		{Op: 0}, {Op: OpSetVol + 1}, {Op: OpStart, Slot: VoiceSlots}, {Op: OpStart, Slot: -1},
		{Op: OpStart, Sample: MaxSamples}, {Op: OpStart, Sample: -1},
		{Op: OpStart, Left: MaxVolume + 1}, {Op: OpStart, Right: -1}, {Op: OpStart, Right: MaxVolume + 1},
	}
	for _, c := range bad {
		if _, err := c.Encode(); !errors.Is(err, ErrCommand) {
			t.Errorf("%+v accepted (%v)", c, err)
		}
	}
	for _, w := range [][4]int32{
		{OpStart, 2 << 16, 0, 0}, // loop word not 0/1
		{OpStart, 0, 0, 1},       // reserved word set
		{0, 0, 0, 0},             // op 0
		{OpStart, 0, 256, 0},     // left volume over the bound
	} {
		if _, err := DecodeCommand(w); !errors.Is(err, ErrCommand) {
			t.Errorf("%v accepted (%v)", w, err)
		}
	}
}

func TestVoicesSABBytes(t *testing.T) {
	if got, want := VoicesSABBytes(), (16+256+4096)*4; got != want {
		t.Fatalf("VoicesSABBytes = %d, want %d", got, want)
	}
	if VCmdWord != 272 || BankTableBytes != 32768 {
		t.Fatalf("layout moved: VCmdWord %d BankTableBytes %d", VCmdWord, BankTableBytes)
	}
}

// FuzzDecodeCommand: the ring is a decoder of guest-influenced words. Nothing
// panics, and whatever decodes re-encodes to the same words.
func FuzzDecodeCommand(f *testing.F) {
	f.Add(int32(1), int32(0), int32(0), int32(0))
	f.Add(int32(-1), int32(-1), int32(-1), int32(-1))
	f.Add(int32(1|127<<8)-65536, int32(1023|1<<16), int32(255|255<<16), int32(0))
	f.Fuzz(func(t *testing.T, a, b, c, d int32) {
		w := [4]int32{a, b, c, d}
		cmd, err := DecodeCommand(w)
		if err != nil {
			return
		}
		back, err := cmd.Encode()
		if err != nil || back != w {
			t.Fatalf("%v decoded to %+v re-encoded to %v, %v", w, cmd, back, err)
		}
	})
}

// The worklet cannot import the Go constants: this keeps the two in step.
func TestVoicesWorkletInStep(t *testing.T) {
	want := map[string]int{
		"V_CMDW": VCmdWrite, "V_CMDR": VCmdRead, "V_UNDERRUN": VUnderrun, "V_PLAYED": VPlayed,
		"V_STATE": VState, "V_HBEAT": VHBeat, "V_STARVED": VStarved, "V_PAINTED": VPainted,
		"V_BADCMD": VBadCmd, "V_SNAP": VSnapWord, "V_CMD": VCmdWord,
		"SLOTS": VoiceSlots, "MAXSFX": MaxSamples, "CMDS": CmdEntries, "ENTRY_WORDS": BankEntryWords,
		"MIX_RATE": MixRate, "PAINT_FRAMES": PaintFrames, "MAX_VOL": MaxVolume,
		"OP_START": OpStart, "OP_STOP": OpStop, "OP_STOPALL": OpStopAll, "OP_SETVOL": OpSetVol,
	}
	seen := map[string]int{}
	re := regexp.MustCompile(`\b([A-Z][A-Z_]+) = (\d+)\b`)
	for _, m := range re.FindAllSubmatch(WorkletJS, -1) {
		n, _ := strconv.Atoi(string(m[2]))
		seen[string(m[1])] = n
	}
	for k, v := range want {
		if got, ok := seen[k]; !ok || got != v {
			t.Errorf("worklet %s = %d (present %v), Go says %d", k, got, ok, v)
		}
	}
	if _, err := os.ReadFile("audio-worklet.js"); err != nil {
		t.Fatal(err)
	}
}

// refVoice is an independent, closed-form model of one Quake channel: the
// oracle the worklet's output is compared with. WinQuake mixes into an int
// paintbuffer and clamps at transfer; so does this.
type refVoice struct {
	pcm       []int32 // decoded samples
	bits      int
	loopStart int
	start     int // first mix frame
	stop      int // first frame after the voice stopped (-1: none)
	lv, rv    int32
	loop      bool
}

func (v *refVoice) at(k int) (l, r int32) {
	if k < v.start || (v.stop >= 0 && k >= v.stop) {
		return 0, 0
	}
	i := k - v.start
	n := len(v.pcm)
	if i >= n {
		if !v.loop || v.loopStart < 0 || v.loopStart >= n {
			return 0, 0
		}
		i = v.loopStart + (i-n)%(n-v.loopStart)
	}
	s := v.pcm[i]
	if v.bits == 8 {
		return s * v.lv / 256, s * v.rv / 256
	}
	return s * v.lv >> 8, s * v.rv >> 8
}

func clamp16(x int32) int32 {
	if x > 32767 {
		return 32767
	}
	if x < -32768 {
		return -32768
	}
	return x
}

func refFrame(vs []*refVoice, k int) (l, r int32) {
	for _, v := range vs {
		a, b := v.at(k)
		l += a
		r += b
	}
	return clamp16(l), clamp16(r)
}

// TestRefClampsNotWraps pins the oracle itself to WinQuake: 20 loud voices sum
// past int16 and saturate; an int16 accumulator would wrap to a negative value.
func TestRefClampsNotWraps(t *testing.T) {
	var vs []*refVoice
	for i := 0; i < 20; i++ {
		vs = append(vs, &refVoice{pcm: []int32{30000}, bits: 16, stop: -1, lv: 255, rv: 255})
	}
	l, r := refFrame(vs, 0)
	if l != 32767 || r != 32767 {
		t.Fatalf("got %d,%d want 32767,32767", l, r)
	}
}
