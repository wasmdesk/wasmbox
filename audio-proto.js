// SPDX-License-Identifier: BSD-3-Clause
//
// wasmbox audio relay: shared constants and the validation of an untrusted
// `audio_open` message. Loaded by the compositor worker (importScripts) and by
// the page (<script>); the same code runs at both hops, so a client cannot
// slip past one by relying on the other. The Go twin is audioring/ring.go and
// the consumer is audio-worklet.js: keep the three in step.
//
// Ring layout (see docs/protocol.md, "Audio"):
//   bytes 0..63    Int32 header: [0] write [1] read [2] underruns [3] played
//                  [4] overruns [5] state(0 open, 1 closed)
//   bytes 64..     capacityFrames * channels float32, interleaved
//   capacityFrames is a power of two, so uint32 free-running counters are safe
//   across the 2^32 wrap.

"use strict";

(function (g) {
  const AUDIO = Object.freeze({
    HEADER_BYTES: 64,
    H_WRITE: 0, H_READ: 1, H_UNDERRUN: 2, H_PLAYED: 3, H_OVERRUN: 4, H_STATE: 5,
    MIN_RATE: 8000, MAX_RATE: 192000,
    MAX_CHANNELS: 2,
    MIN_FRAMES: 256, MAX_FRAMES: 65536,
    // Rings alive at once across all clients: bounds the memory and the audio
    // graph a handful of hostile workers can build.
    MAX_SESSIONS: 8,
    sabBytes(channels, frames) { return 64 + frames * channels * 4; },

    // validateOpen returns {ok:true, sampleRate, channels, capacityFrames} or
    // {ok:false, reason}. It reads only the fields it names, copies them out
    // (a getter-bearing object cannot change them between check and use) and
    // requires the SAB to be exactly the size the numbers imply.
    validateOpen(m) {
      if (!m || typeof m !== "object") return { ok: false, reason: "not an object" };
      const sampleRate = m.sampleRate, channels = m.channels, frames = m.capacityFrames;
      const isInt = (v) => typeof v === "number" && Number.isInteger(v);
      if (!isInt(sampleRate) || sampleRate < 8000 || sampleRate > 192000)
        return { ok: false, reason: "sampleRate out of range" };
      if (!isInt(channels) || channels < 1 || channels > 2)
        return { ok: false, reason: "channels out of range" };
      if (!isInt(frames) || frames < 256 || frames > 65536)
        return { ok: false, reason: "capacityFrames out of range" };
      if ((frames & (frames - 1)) !== 0)
        return { ok: false, reason: "capacityFrames is not a power of two" };
      const sab = m.sab;
      if (typeof SharedArrayBuffer === "undefined" || !(sab instanceof SharedArrayBuffer))
        return { ok: false, reason: "sab is not a SharedArrayBuffer" };
      if (sab.byteLength !== 64 + frames * channels * 4)
        return { ok: false, reason: "sab size does not match the parameters" };
      return { ok: true, sampleRate, channels, capacityFrames: frames, sab };
    },
  });
  g.WASMBOX_AUDIO = AUDIO;
  if (typeof module !== "undefined" && module.exports) module.exports = AUDIO;
})(typeof globalThis !== "undefined" ? globalThis : self);
