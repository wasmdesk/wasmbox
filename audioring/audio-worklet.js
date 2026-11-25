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

const H_WRITE = 0, H_READ = 1, H_UNDERRUN = 2, H_PLAYED = 3, H_STATE = 5, H_HBEAT = 6;

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
    // Heartbeat: one tick per render quantum, whatever the ring holds. It
    // measures the CONSUMER (the context runs and this node is pulled), so a
    // producer can tell "the page plays" from "nobody reads" (a context still
    // suspended before a gesture, a page that has gone).
    Atomics.add(this.hdr, H_HBEAT, 1);
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

// ---------------------------------------------------------------------------
// wasmbox-voices: a game's channel mixer on the audio thread. The producer loads
// PCM into a shared bank once, then sends start/stop/volume commands over a
// single-producer ring (layout and rationale in audioring/voices.go). Voices are
// painted `ahead` frames in front of the play cursor into an int32 paint buffer
// at the mix rate (11025 Hz, Quake's), clamped to 16 bits only at transfer as
// WinQuake does, then read with linear interpolation. Nothing here allocates,
// waits or touches the main thread, so a producer that the OS deschedules for
// 200 ms cannot dry the output: sounds already playing run on.
// ---------------------------------------------------------------------------
const V_CMDW = 0, V_CMDR = 1, V_UNDERRUN = 2, V_PLAYED = 3, V_STATE = 5, V_HBEAT = 6,
  V_STARVED = 7, V_PAINTED = 8, V_BADCMD = 9, V_SNAP = 16, V_CMD = 272;
const SLOTS = 128, MAXSFX = 1024, CMDS = 1024, ENTRY_WORDS = 8, MIX_RATE = 11025,
  PAINT_FRAMES = 2048, MAX_VOL = 255, BANK_TABLE = 32768;
const OP_START = 1, OP_STOP = 2, OP_STOPALL = 3, OP_SETVOL = 4;

function clamp16(x) { return x > 32767 ? 32767 : x < -32768 ? -32768 : x; }

class WasmboxVoices extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const p = options.processorOptions;
    this.ctl = new Int32Array(p.ctl, 0, V_CMD + CMDS * 4);
    this.bank = new Int32Array(p.bank);
    this.bank8 = new Int8Array(p.bank);
    this.bank16 = new Int16Array(p.bank);
    this.bankBytes = p.bank.byteLength;
    // Frames kept painted in front of the play cursor (MixAhead x MIX_RATE).
    this.ahead = Math.min(p.aheadFrames | 0 || 1103, PAINT_FRAMES >> 1);
    this.step = MIX_RATE / sampleRate;
    this.phase = 0;
    this.p = 0;          // play cursor, whole paint frames
    this.painted = 0;    // frames painted so far
    this.pl = new Int32Array(PAINT_FRAMES);
    this.pr = new Int32Array(PAINT_FRAMES);
    this.cmdRead = Atomics.load(this.ctl, V_CMDR) >>> 0;
    this.active = new Uint8Array(SLOTS);
    this.dirty = new Uint8Array(SLOTS);
    this.gen = new Uint16Array(SLOTS);
    this.bits = new Uint8Array(SLOTS);
    this.looped = new Uint8Array(SLOTS);
    this.base = new Int32Array(SLOTS);    // sample index of the PCM in bank8 / bank16
    this.pos = new Int32Array(SLOTS);
    this.end = new Int32Array(SLOTS);
    this.lstart = new Int32Array(SLOTS);
    this.lv = new Int32Array(SLOTS);
    this.rv = new Int32Array(SLOTS);
  }

  bad() { Atomics.add(this.ctl, V_BADCMD, 1); }

  drain() {
    const ctl = this.ctl;
    const w = Atomics.load(ctl, V_CMDW) >>> 0;
    let r = this.cmdRead;
    if (((w - r) >>> 0) > CMDS) { r = w; this.bad(); }   // corrupt header: resync
    while (r !== w) {
      const at = V_CMD + (r & (CMDS - 1)) * 4;
      this.apply(Atomics.load(ctl, at), Atomics.load(ctl, at + 1), Atomics.load(ctl, at + 2), Atomics.load(ctl, at + 3));
      r = (r + 1) >>> 0;
    }
    this.cmdRead = r;
    Atomics.store(ctl, V_CMDR, r | 0);
  }

  // apply validates every word as audioring.DecodeCommand does, then acts. A
  // command takes effect only for the slot's current generation (START: or the
  // next one), so a stale command cannot touch a slot that has been reused.
  apply(w0, w1, w2, w3) {
    const op = w0 & 0xff, slot = (w0 >>> 8) & 0xff, gen = w0 >>> 16;
    const id = w1 & 0xffff, loop = w1 >>> 16, lv = w2 & 0xffff, rv = w2 >>> 16;
    if (op < OP_START || op > OP_SETVOL || slot >= SLOTS || id >= MAXSFX || loop > 1 ||
        lv > MAX_VOL || rv > MAX_VOL || w3 !== 0) { this.bad(); return; }
    if (op === OP_STOPALL) {
      for (let s = 0; s < SLOTS; s++) if (this.active[s]) { this.active[s] = 0; this.dirty[s] = 1; }
      return;
    }
    const cur = this.gen[slot];
    if (op === OP_START) {
      if (gen !== cur && gen !== ((cur + 1) & 0xffff)) { this.bad(); return; }
      const e = id * ENTRY_WORDS, b = this.bank;
      if (Atomics.load(b, e) !== 1) { this.bad(); return; }
      const bits = b[e + 1], n = b[e + 3], off = b[e + 5], len = b[e + 6];
      if ((bits !== 8 && bits !== 16) || n <= 0 || off < BANK_TABLE || (off & 3) !== 0 ||
          len !== n * (bits >> 3) || off + len > this.bankBytes) { this.bad(); return; }
      this.gen[slot] = gen;
      this.bits[slot] = bits;
      this.base[slot] = bits === 8 ? off : off >> 1;
      this.pos[slot] = 0;
      this.end[slot] = n;
      this.lstart[slot] = b[e + 4];
      this.looped[slot] = loop;
      this.lv[slot] = lv;
      this.rv[slot] = rv;
      this.active[slot] = 1;
      this.dirty[slot] = 1;
      return;
    }
    if (gen !== cur || !this.active[slot]) return;   // stale: the slot moved on
    if (op === OP_STOP) { this.active[slot] = 0; this.dirty[slot] = 1; return; }
    this.lv[slot] = lv;                              // OP_SETVOL
    this.rv[slot] = rv;
  }

  // paint mixes `count` frames of every active voice into the paint buffer,
  // as Quake's S_PaintChannels does: a voice that reaches its end loops back to
  // its loop start or retires.
  paint(count) {
    const pl = this.pl, pr = this.pr, mask = PAINT_FRAMES - 1, t = this.painted;
    for (let k = 0; k < count; k++) { const j = (t + k) & mask; pl[j] = 0; pr[j] = 0; }
    for (let s = 0; s < SLOTS; s++) {
      if (!this.active[s]) continue;
      let done = 0;
      while (done < count) {
        let c = count - done;
        const remaining = this.end[s] - this.pos[s];
        if (remaining < c) c = remaining;
        if (c > 0) { this.mix(s, t + done, c); this.pos[s] += c; done += c; }
        if (this.pos[s] >= this.end[s]) {
          const ls = this.lstart[s];
          if (this.looped[s] && ls >= 0 && ls < this.end[s]) { this.pos[s] = ls; continue; }
          this.active[s] = 0; this.dirty[s] = 1;
          break;
        }
      }
    }
    this.painted = t + count;
    Atomics.store(this.ctl, V_PAINTED, this.painted | 0);
  }

  mix(s, f0, c) {
    const pl = this.pl, pr = this.pr, mask = PAINT_FRAMES - 1;
    const lv = this.lv[s], rv = this.rv[s], at = this.base[s] + this.pos[s];
    if (this.bits[s] === 8) {
      const d = this.bank8;
      for (let j = 0; j < c; j++) {
        const x = d[at + j], i = (f0 + j) & mask;
        pl[i] += (x * lv / 256) | 0;     // truncating division, as Go's s*lv/256
        pr[i] += (x * rv / 256) | 0;
      }
    } else {
      const d = this.bank16;
      for (let j = 0; j < c; j++) {
        const x = d[at + j], i = (f0 + j) & mask;
        pl[i] += (x * lv) >> 8;
        pr[i] += (x * rv) >> 8;
      }
    }
  }

  process(_inputs, outputs) {
    const out = outputs[0];
    const n = out[0].length;
    const ctl = this.ctl;
    Atomics.add(ctl, V_HBEAT, 1);
    if (Atomics.load(ctl, V_STATE) !== 0) return false;
    this.drain();

    // Keep `ahead` frames painted beyond what this quantum reads.
    const need = Math.ceil(this.phase + this.step * n) + 2;
    let todo = this.p + this.ahead + need - this.painted;
    const room = PAINT_FRAMES - (this.painted - this.p);
    if (todo > room) { todo = room; Atomics.add(ctl, V_STARVED, 1); }
    if (todo > 0) this.paint(todo);

    const o0 = out[0], o1 = out.length > 1 ? out[1] : null;
    const pl = this.pl, pr = this.pr, mask = PAINT_FRAMES - 1, step = this.step;
    let p = this.p, phase = this.phase;
    for (let i = 0; i < n; i++) {
      const a = p & mask, b = (p + 1) & mask;
      const l0 = clamp16(pl[a]), r0 = clamp16(pr[a]);
      o0[i] = (l0 + (clamp16(pl[b]) - l0) * phase) / 32768;
      if (o1) o1[i] = (r0 + (clamp16(pr[b]) - r0) * phase) / 32768;
      phase += step;
      while (phase >= 1) { phase -= 1; p++; }
    }
    this.p = p; this.phase = phase;
    Atomics.add(ctl, V_PLAYED, n);

    // Publish the voice table the producer allocates from.
    for (let s = 0; s < SLOTS; s++) {
      if (!this.active[s] && !this.dirty[s]) continue;
      this.dirty[s] = 0;
      const k = V_SNAP + 2 * s;
      Atomics.store(ctl, k, this.gen[s] | (this.active[s] << 16));
      Atomics.store(ctl, k + 1, this.active[s] ? this.end[s] - this.pos[s] : 0);
    }
    return true;
  }
}

registerProcessor("wasmbox-voices", WasmboxVoices);
