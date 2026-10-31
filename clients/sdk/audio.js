// SPDX-License-Identifier: BSD-3-Clause
//
// wasmbox audio, JavaScript client side: the producer for a ring that the
// compositor page plays through an AudioWorklet (docs/protocol.md, "Audio").
// Go clients use audioring/ (github.com/wasmdesk/wasmbox/audioring), which is
// the same ring in Go; the two share one layout.
//
//   importScripts("../sdk/audio.js");
//   const a = new WasmboxAudio({ sampleRate: 48000, channels: 2, capacityFrames: 2048 });
//   a.open(client.channel);              // after the window's welcome
//   a.write(float32Interleaved);         // returns frames accepted; never blocks
//
// An AudioContext does not exist in a worker; that is the whole reason for the
// relay.

"use strict";

(function (g) {
  const HDR = 64;
  const H_WRITE = 0, H_READ = 1, H_UNDERRUN = 2, H_PLAYED = 3, H_OVERRUN = 4, H_STATE = 5;

  class WasmboxAudio {
    constructor(o) {
      this.sampleRate = o.sampleRate | 0;
      this.channels = o.channels | 0;
      this.capacityFrames = o.capacityFrames | 0;
      const f = this.capacityFrames;
      if (this.sampleRate < 8000 || this.sampleRate > 192000 || this.channels < 1 || this.channels > 2 ||
          f < 256 || f > 65536 || (f & (f - 1)) !== 0)
        throw new RangeError("WasmboxAudio: parameters outside the protocol bounds");
      this.sab = new SharedArrayBuffer(HDR + f * this.channels * 4);
      this.hdr = new Int32Array(this.sab, 0, 16);
      this.data = new Float32Array(this.sab, HDR, f * this.channels);
      this.write_ = 0;
      this.welcome = null;
    }

    // Sends audio_open on the compositor port (client.channel).
    open(port, windowId) {
      port.postMessage({
        type: "audio_open", window_id: windowId == null ? null : windowId | 0,
        sampleRate: this.sampleRate, channels: this.channels,
        capacityFrames: this.capacityFrames, sab: this.sab,
      });
      this._port = port;
    }

    buffered() { return (this.write_ - (Atomics.load(this.hdr, H_READ) >>> 0)) >>> 0; }
    free() { const b = this.buffered(); return b >= this.capacityFrames ? 0 : this.capacityFrames - b; }

    // pcm: interleaved Float32Array. Returns frames accepted; the surplus is
    // dropped and counted in the overrun word.
    write(pcm) {
      const ch = this.channels, cap = this.capacityFrames;
      if (Atomics.load(this.hdr, H_STATE) !== 0) return 0;
      let n = (pcm.length / ch) | 0;
      if (n === 0) return 0;
      const fr = this.free();
      if (n > fr) { Atomics.add(this.hdr, H_OVERRUN, n - fr); n = fr; }
      if (n === 0) return 0;
      const start = (this.write_ & (cap - 1)) * ch;
      const take = n * ch, first = cap * ch - start;
      if (take <= first) this.data.set(pcm.subarray(0, take), start);
      else { this.data.set(pcm.subarray(0, first), start); this.data.set(pcm.subarray(first, take), 0); }
      this.write_ = (this.write_ + n) >>> 0;
      Atomics.store(this.hdr, H_WRITE, this.write_ | 0);   // publish last
      return n;
    }

    stats() {
      return { played: Atomics.load(this.hdr, H_PLAYED) >>> 0, underruns: Atomics.load(this.hdr, H_UNDERRUN) >>> 0,
               overruns: Atomics.load(this.hdr, H_OVERRUN) >>> 0, buffered: this.buffered() };
    }

    close() {
      Atomics.store(this.hdr, H_STATE, 1);
      if (this._port) this._port.postMessage({ type: "audio_close" });
    }
  }
  g.WasmboxAudio = WasmboxAudio;
})(self);
