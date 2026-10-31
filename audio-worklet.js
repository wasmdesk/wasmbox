// SPDX-License-Identifier: BSD-3-Clause
//
// wasmbox audio relay, audio-thread half: an AudioWorkletProcessor that reads
// one client's SharedArrayBuffer ring (layout in audio-proto.js and
// audioring/ring.go) straight from the audio thread. There is no postMessage
// per buffer and nothing on the main thread.
//
// Per the Web Audio spec, process() runs once per render quantum (128 frames)
// and must not block or allocate; this one only does Atomics loads/stores and
// typed-array reads. Cf. the Chrome "AudioWorklet design patterns" article
// (shared buffers + a state array, the consumer never waits for the producer).
//
// Underrun policy: until the ring first holds `prime` frames the processor
// outputs silence and counts nothing. After that, a quantum that cannot be
// filled is zero-padded and counted ONCE (an event), and playback re-primes so
// a starved stream is one clean gap rather than a stutter of one-frame bursts.
//
// Resampling: when the client rate differs from the context rate the ring is
// read with linear interpolation (step = clientRate / contextRate). Same-rate
// streams take the straight copy path.
//
// This file runs in AudioWorkletGlobalScope: it cannot importScripts, so the
// header word indices are repeated here (see audio-proto.js).

"use strict";

const H_WRITE = 0, H_READ = 1, H_UNDERRUN = 2, H_PLAYED = 3, H_STATE = 5;

class WasmboxRing extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const p = options.processorOptions;
    this.hdr = new Int32Array(p.sab, 0, 16);
    this.ch = p.channels;
    this.cap = p.capacityFrames;
    this.mask = this.cap - 1;
    this.data = new Float32Array(p.sab, 64, this.cap * this.ch);
    this.step = p.sampleRate / sampleRate;   // `sampleRate`: the context's, a worklet global
    this.phase = 0;
    this.r = Atomics.load(this.hdr, H_READ) >>> 0;
    this.started = false;
    // Frames to buffer before (re)starting: two quanta, never more than half the ring.
    this.prime = Math.min(this.cap >> 1, Math.ceil(2 * 128 * this.step));
  }

  process(_inputs, outputs) {
    const out = outputs[0];
    const n = out[0].length;
    if (Atomics.load(this.hdr, H_STATE) !== 0) return false;   // closed: let the node be collected
    const w = Atomics.load(this.hdr, H_WRITE) >>> 0;
    let r = this.r;
    let avail = (w - r) >>> 0;
    if (avail > this.cap) { r = w; avail = 0; this.started = false; }   // corrupt header: resync

    if (!this.started) {
      if (avail < this.prime) { Atomics.store(this.hdr, H_READ, r | 0); this.r = r; return true; }
      this.started = true;
    }

    const o0 = out[0], o1 = out.length > 1 ? out[1] : null;
    const ch = this.ch, data = this.data, mask = this.mask, step = this.step;
    let i = 0;
    let underrun = false;
    if (step === 1) {
      for (; i < n; i++) {
        if (avail < 1) { underrun = true; break; }
        const k = (r & mask) * ch;
        const l = data[k];
        o0[i] = l;
        if (o1) o1[i] = ch > 1 ? data[k + 1] : l;
        r = (r + 1) >>> 0; avail--;
      }
    } else {
      let phase = this.phase;
      const need = Math.ceil(step) + 1;
      for (; i < n; i++) {
        // Interpolation reads r and r+1, and the phase advance may move r by up
        // to ceil(step) frames: never let r overtake the write index.
        if (avail < need) { underrun = true; break; }
        const k = (r & mask) * ch, k2 = ((r + 1) & mask) * ch;
        const l = data[k] + (data[k2] - data[k]) * phase;
        o0[i] = l;
        if (o1) o1[i] = ch > 1 ? data[k + 1] + (data[k2 + 1] - data[k + 1]) * phase : l;
        phase += step;
        while (phase >= 1) { phase -= 1; r = (r + 1) >>> 0; avail--; }
      }
      this.phase = phase;
    }
    for (let j = i; j < n; j++) { o0[j] = 0; if (o1) o1[j] = 0; }
    this.r = r;
    Atomics.store(this.hdr, H_READ, r | 0);
    if (i > 0) Atomics.add(this.hdr, H_PLAYED, i);
    if (underrun) { Atomics.add(this.hdr, H_UNDERRUN, 1); this.started = false; }
    return true;
  }
}

registerProcessor("wasmbox-ring", WasmboxRing);
