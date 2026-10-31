// SPDX-License-Identifier: BSD-3-Clause

package audioring

import (
	"errors"
	"testing"
)

// mem is an in-memory Backing: header words plus a float32 sample area.
type mem struct {
	hdr  [16]int32
	data []float32
}

func newMem(channels, capacity int) *mem {
	return &mem{data: make([]float32, channels*capacity)}
}
func (m *mem) Load(i int) int32     { return m.hdr[i] }
func (m *mem) Store(i int, v int32) { m.hdr[i] = v }
func (m *mem) Add(i int, d int32)   { m.hdr[i] += d }
func (m *mem) WriteSamples(at int, src []float32) {
	copy(m.data[at:], src)
}

// read plays the consumer: it takes up to n frames and advances the read index.
func (m *mem) read(channels, capacity, n int) []float32 {
	r := uint32(m.hdr[HRead])
	avail := int(Buffered(uint32(m.hdr[HWrite]), r))
	if n > avail {
		n = avail
	}
	out := make([]float32, 0, n*channels)
	for i := 0; i < n; i++ {
		base := int((r+uint32(i))&uint32(capacity-1)) * channels
		out = append(out, m.data[base:base+channels]...)
	}
	m.hdr[HRead] = int32(r + uint32(n))
	return out
}

func TestSABBytes(t *testing.T) {
	if got := SABBytes(2, 512); got != 64+512*2*4 {
		t.Fatalf("SABBytes = %d", got)
	}
}

func TestValidate(t *testing.T) {
	good := [][3]int{{48000, 2, 512}, {8000, 1, 256}, {192000, 2, 65536}}
	for _, g := range good {
		if err := Validate(g[0], g[1], g[2]); err != nil {
			t.Errorf("%v rejected: %v", g, err)
		}
	}
	bad := [][3]int{
		{7999, 2, 512}, {192001, 2, 512}, {-1, 2, 512},
		{48000, 0, 512}, {48000, 3, 512},
		{48000, 2, 128}, {48000, 2, 131072}, {48000, 2, 300}, {48000, 2, 0},
	}
	for _, b := range bad {
		err := Validate(b[0], b[1], b[2])
		if !errors.Is(err, ErrParams) {
			t.Errorf("%v accepted or wrong error: %v", b, err)
		}
	}
	if _, err := New(newMem(2, 512), 1, 2, 512); !errors.Is(err, ErrParams) {
		t.Errorf("New with bad rate: %v", err)
	}
}

func TestWriteReadBasic(t *testing.T) {
	m := newMem(2, 256)
	w, err := New(m, 48000, 2, 256)
	if err != nil {
		t.Fatal(err)
	}
	if w.Free() != 256 || w.Buffered() != 0 {
		t.Fatalf("fresh ring free=%d buffered=%d", w.Free(), w.Buffered())
	}
	if n := w.Write(nil); n != 0 {
		t.Fatalf("empty write = %d", n)
	}
	// 3 frames + a dangling half frame (ignored).
	if n := w.Write([]float32{1, 2, 3, 4, 5, 6, 7}); n != 3 {
		t.Fatalf("write = %d, want 3", n)
	}
	if w.Buffered() != 3 {
		t.Fatalf("buffered = %d", w.Buffered())
	}
	got := m.read(2, 256, 10)
	want := []float32{1, 2, 3, 4, 5, 6}
	if len(got) != len(want) {
		t.Fatalf("read %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestWraparoundSplitsWrite(t *testing.T) {
	const cap, ch = 256, 2
	m := newMem(ch, cap)
	w, _ := New(m, 44100, ch, cap)
	// Advance the ring so the next write straddles the end of the data area.
	pre := make([]float32, 250*ch)
	w.Write(pre)
	m.read(ch, cap, 250)
	in := make([]float32, 20*ch)
	for i := range in {
		in[i] = float32(i + 1)
	}
	if n := w.Write(in); n != 20 {
		t.Fatalf("write = %d", n)
	}
	out := m.read(ch, cap, 20)
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("sample %d = %v, want %v", i, out[i], in[i])
		}
	}
}

func TestCounterWrapAt2to32(t *testing.T) {
	const cap, ch = 256, 1
	m := newMem(ch, cap)
	start := int32(-100) // uint32 0xFFFFFF9C: 100 frames before the wrap
	m.hdr[HWrite], m.hdr[HRead] = start, start
	w, _ := New(m, 8000, ch, cap)
	for round := 0; round < 5; round++ {
		in := make([]float32, 60)
		for i := range in {
			in[i] = float32(round*100 + i)
		}
		if n := w.Write(in); n != 60 {
			t.Fatalf("round %d: write = %d", round, n)
		}
		out := m.read(ch, cap, 60)
		for i := range in {
			if out[i] != in[i] {
				t.Fatalf("round %d sample %d = %v want %v", round, i, out[i], in[i])
			}
		}
	}
	if uint32(m.hdr[HWrite]) >= 1000 { // wrapped past 2^32 back to small values
		t.Fatalf("write index did not wrap: %d", uint32(m.hdr[HWrite]))
	}
}

func TestOverflowDropsAndCounts(t *testing.T) {
	const cap, ch = 256, 2
	m := newMem(ch, cap)
	w, _ := New(m, 48000, ch, cap)
	if n := w.Write(make([]float32, 300*ch)); n != cap {
		t.Fatalf("first write accepted %d, want %d", n, cap)
	}
	if w.Free() != 0 {
		t.Fatalf("free = %d", w.Free())
	}
	if n := w.Write(make([]float32, 10*ch)); n != 0 {
		t.Fatalf("write to a full ring accepted %d", n)
	}
	if got := w.Stats().Overruns; got != 44+10 {
		t.Fatalf("overruns = %d, want 54", got)
	}
}

func TestFreeCorruptHeader(t *testing.T) {
	// A hostile consumer cannot make Free negative or huge.
	if got := Free(10, 20, 256); got != 0 { // read ahead of write: 2^32-10 buffered
		t.Fatalf("Free = %d", got)
	}
	if got := Free(500, 0, 256); got != 0 {
		t.Fatalf("Free = %d", got)
	}
	if got := Free(0, 0, 256); got != 256 {
		t.Fatalf("Free = %d", got)
	}
}

func TestCloseAndStats(t *testing.T) {
	m := newMem(1, 256)
	w, _ := New(m, 22050, 1, 256)
	m.hdr[HPlayed], m.hdr[HUnderrun] = 7, 2
	if s := w.Stats(); s.Played != 7 || s.Underruns != 2 || s.Overruns != 0 {
		t.Fatalf("stats %+v", s)
	}
	w.Close()
	if m.hdr[HState] != StateClosed {
		t.Fatal("state not closed")
	}
	if n := w.Write([]float32{1, 2, 3}); n != 0 {
		t.Fatalf("write after close = %d", n)
	}
}

// FuzzIndexArithmetic drives a writer against a model consumer with random
// write and read sizes and starting counters, and checks the invariants the
// worklet relies on: never more buffered than capacity, free + buffered ==
// capacity, and every sample comes out in order, exactly once.
func FuzzIndexArithmetic(f *testing.F) {
	f.Add(uint32(0), []byte{10, 200, 3, 255, 128})
	f.Add(uint32(0xFFFFFF00), []byte{250, 250, 1, 1, 77, 255})
	f.Add(uint32(0xFFFFFFFF), []byte{})
	f.Fuzz(func(t *testing.T, start uint32, ops []byte) {
		const cap, ch = 256, 2
		m := newMem(ch, cap)
		m.hdr[HWrite], m.hdr[HRead] = int32(start), int32(start)
		w, err := New(m, 48000, ch, cap)
		if err != nil {
			t.Fatal(err)
		}
		next, expect := float32(0), float32(0)
		for i, op := range ops {
			if i%2 == 0 {
				n := int(op) // 0..255 frames
				in := make([]float32, n*ch)
				for j := range in {
					in[j] = next + float32(j)
				}
				got := w.Write(in)
				if got < 0 || got > n {
					t.Fatalf("write %d returned %d", n, got)
				}
				next += float32(got * ch)
			} else {
				for _, s := range m.read(ch, cap, int(op)) {
					if s != expect {
						t.Fatalf("sample %v, want %v", s, expect)
					}
					expect++
				}
			}
			b := w.Buffered()
			if b < 0 || b > cap {
				t.Fatalf("buffered %d outside [0,%d]", b, cap)
			}
			if w.Free()+b != cap {
				t.Fatalf("free %d + buffered %d != %d", w.Free(), b, cap)
			}
		}
	})
}

// FuzzFree checks the pure arithmetic on arbitrary counters, including
// corrupted ones.
func FuzzFree(f *testing.F) {
	f.Add(uint32(0), uint32(0), 256)
	f.Add(uint32(1), uint32(0xFFFFFFFF), 65536)
	f.Fuzz(func(t *testing.T, write, read uint32, capacity int) {
		if capacity < 1 || capacity > MaxFrames {
			t.Skip()
		}
		fr := Free(write, read, capacity)
		if fr < 0 || fr > capacity {
			t.Fatalf("Free(%d,%d,%d) = %d", write, read, capacity, fr)
		}
		if b := Buffered(write, read); int64(b) < int64(capacity) && int64(fr)+int64(b) != int64(capacity) {
			t.Fatalf("free %d + buffered %d != %d", fr, b, capacity)
		}
	})
}
