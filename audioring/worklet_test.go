// SPDX-License-Identifier: BSD-3-Clause

package audioring

import (
	"bytes"
	"os"
	"regexp"
	"strconv"
	"testing"
)

// The heartbeat is read from the word the layout names (6), and Heartbeat does
// not confuse it with a neighbour or with the producer's own counters.
func TestHeartbeatWord(t *testing.T) {
	if HBeat != 6 {
		t.Fatalf("HBeat = %d, the layout says word 6", HBeat)
	}
	m := newMem(2, 256)
	w, err := New(m, 48000, 2, 256)
	if err != nil {
		t.Fatal(err)
	}
	if got := w.Heartbeat(); got != 0 {
		t.Fatalf("fresh ring heartbeat = %d", got)
	}
	// Writing, overrunning, and the consumer's other counters leave it alone.
	w.Write(make([]float32, 600*2))
	m.hdr[HPlayed], m.hdr[HUnderrun] = 7, 3
	if got := w.Heartbeat(); got != 0 {
		t.Fatalf("heartbeat moved without the consumer: %d", got)
	}
	m.Add(HBeat, 5)
	if got := w.Heartbeat(); got != 5 {
		t.Fatalf("heartbeat = %d, want 5", got)
	}
	// Free-running: the Int32 word wraps negative, the reader sees a uint32.
	m.hdr[HBeat] = -1
	if got := w.Heartbeat(); got != 0xFFFFFFFF {
		t.Fatalf("wrapped heartbeat = %#x", got)
	}
}

// The embedded worklet is the file in git, byte for byte, and its header word
// indices are the Go constants: the JS cannot import them, so this test is
// what keeps the two in step.
func TestWorkletEmbeddedAndInStep(t *testing.T) {
	disk, err := os.ReadFile("audio-worklet.js")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(WorkletJS, disk) {
		t.Fatal("WorkletJS differs from audio-worklet.js")
	}
	if len(WorkletJS) == 0 {
		t.Fatal("WorkletJS is empty")
	}
	want := map[string]int{
		"H_WRITE": HWrite, "H_READ": HRead, "H_UNDERRUN": HUnderrun,
		"H_PLAYED": HPlayed, "H_STATE": HState, "H_HBEAT": HBeat,
	}
	re := regexp.MustCompile(`\b(H_[A-Z]+) = (\d+)`)
	seen := map[string]int{}
	for _, m := range re.FindAllSubmatch(WorkletJS, -1) {
		n, _ := strconv.Atoi(string(m[2]))
		seen[string(m[1])] = n
	}
	for k, v := range want {
		if got, ok := seen[k]; !ok || got != v {
			t.Errorf("worklet %s = %d (present %v), Go says %d", k, got, ok, v)
		}
	}
	if !bytes.Contains(WorkletJS, []byte(`registerProcessor("`+WorkletProcessor+`"`)) {
		t.Errorf("worklet does not register %q", WorkletProcessor)
	}
	if !bytes.Contains(WorkletJS, []byte("Atomics.add(this.hdr, H_HBEAT, 1)")) {
		t.Error("worklet never advances the heartbeat")
	}
}

// audio-proto.js carries the same indices for the page and the compositor.
func TestProtoInStep(t *testing.T) {
	js, err := os.ReadFile("../audio-proto.js")
	if err != nil {
		t.Skip("audio-proto.js not beside the module")
	}
	re := regexp.MustCompile(`\b(H_[A-Z]+): (\d+)`)
	seen := map[string]int{}
	for _, m := range re.FindAllSubmatch(js, -1) {
		n, _ := strconv.Atoi(string(m[2]))
		seen[string(m[1])] = n
	}
	want := map[string]int{
		"H_WRITE": HWrite, "H_READ": HRead, "H_UNDERRUN": HUnderrun, "H_PLAYED": HPlayed,
		"H_OVERRUN": HOverrun, "H_STATE": HState, "H_HBEAT": HBeat,
	}
	for k, v := range want {
		if got, ok := seen[k]; !ok || got != v {
			t.Errorf("audio-proto.js %s = %d (present %v), Go says %d", k, got, ok, v)
		}
	}
}
