// SPDX-License-Identifier: BSD-3-Clause
//
// wasmbox audio relay, page half. The page (main thread) is the only place an
// AudioContext can live. It owns ONE context; every client ring becomes an
// AudioWorkletNode (audio-worklet.js, reading the ring on the audio thread)
// -> a per-window GainNode -> the destination, so the browser mixes them.
//
// Mixing policy: every open window is mixed at gain 1. Unfocused windows are
// NOT ducked: focus lives in the Ruby window manager and exposing it here would
// mean editing the compositor; the per-session GainNode is in place for it
// (setGain) so ducking is a later one-line hook, not a redesign.
//
// Autoplay: the context is created on the first audio_open and starts
// suspended. Browsers allow resume() only after a user gesture on the page, so
// the first pointer/key/touch input resumes it (and so does an earlier gesture,
// via navigator.userActivation).
//
// Test hooks: globalThis.__wasmboxAudio.{state,stats}() (stats are read from
// the ring headers, so reading them costs the audio thread nothing).
//
// Usage (index.html): WASMBOX_AUDIO_HOST.attach(worker)  — the compositor worker.

"use strict";

(function (g) {
  const A = g.WASMBOX_AUDIO;
  const sessions = new Map();   // id -> {sab, hdr, node, gain, meta}
  let ctx = null;
  let moduleReady = null;
  let gestureHooked = false;

  function tryResume() {
    if (ctx && ctx.state !== "running") ctx.resume().catch(() => {});
  }

  function hookGestures() {
    if (gestureHooked) return;
    gestureHooked = true;
    const evs = ["pointerdown", "mousedown", "keydown", "touchend"];
    const on = () => {
      tryResume();
      if (ctx && ctx.state === "running") evs.forEach((e) => g.removeEventListener(e, on, true));
    };
    evs.forEach((e) => g.addEventListener(e, on, true));
    if (ctx) ctx.addEventListener("statechange", () => {
      if (ctx.state === "running") evs.forEach((e) => g.removeEventListener(e, on, true));
    });
  }

  function ensureContext() {
    if (ctx) return ctx;
    const Ctor = g.AudioContext || g.webkitAudioContext;
    if (!Ctor) return null;
    ctx = new Ctor({ latencyHint: "interactive" });
    moduleReady = ctx.audioWorklet.addModule("audio-worklet.js");
    hookGestures();
    if (g.navigator && g.navigator.userActivation && g.navigator.userActivation.hasBeenActive) tryResume();
    return ctx;
  }

  async function open(m) {
    // Re-validate: this hop trusts nothing the worker relayed, either.
    const v = A.validateOpen(m);
    if (!v.ok || !Number.isInteger(m.id) || sessions.has(m.id) || sessions.size >= A.MAX_SESSIONS) return;
    const c = ensureContext();
    if (!c) return;
    const entry = { sab: v.sab, hdr: new Int32Array(v.sab, 0, 16), node: null, gain: null, closed: false,
      meta: { id: m.id, windowId: m.window_id, sampleRate: v.sampleRate, channels: v.channels, capacityFrames: v.capacityFrames } };
    sessions.set(m.id, entry);
    try {
      await moduleReady;
      if (entry.closed) return;
      entry.node = new AudioWorkletNode(c, "wasmbox-ring", {
        numberOfInputs: 0, numberOfOutputs: 1, outputChannelCount: [2],
        processorOptions: { sab: v.sab, sampleRate: v.sampleRate, channels: v.channels, capacityFrames: v.capacityFrames },
      });
      entry.gain = c.createGain();
      entry.node.connect(entry.gain);
      entry.gain.connect(c.destination);
    } catch (e) {
      sessions.delete(m.id);
      console.error("wasmbox audio: attach failed: " + e);
    }
  }

  function close(id) {
    const e = sessions.get(id);
    if (!e) return;
    e.closed = true;
    sessions.delete(id);
    Atomics.store(e.hdr, A.H_STATE, 1);   // tells the processor to stop and the client's writer to refuse writes
    try { if (e.node) e.node.disconnect(); } catch (_) {}
    try { if (e.gain) e.gain.disconnect(); } catch (_) {}
  }

  g.WASMBOX_AUDIO_HOST = {
    attach(worker) {
      worker.addEventListener("message", (ev) => {
        const m = ev.data;
        if (!m) return;
        if (m.type === "audio_open") open(m);
        else if (m.type === "audio_close") close(m.id);
      });
    },
  };

  g.__wasmboxAudio = {
    state() { return ctx ? ctx.state : "none"; },
    contextRate() { return ctx ? ctx.sampleRate : 0; },
    sessions() { return sessions.size; },
    stats() {
      return Array.from(sessions.values()).map((e) => ({
        id: e.meta.id, windowId: e.meta.windowId, attached: !!e.node,
        sampleRate: e.meta.sampleRate, channels: e.meta.channels, capacityFrames: e.meta.capacityFrames,
        played: Atomics.load(e.hdr, A.H_PLAYED) >>> 0,
        underruns: Atomics.load(e.hdr, A.H_UNDERRUN) >>> 0,
        overruns: Atomics.load(e.hdr, A.H_OVERRUN) >>> 0,
        buffered: (Atomics.load(e.hdr, A.H_WRITE) - Atomics.load(e.hdr, A.H_READ)) >>> 0,
      }));
    },
    setGain(id, v) {
      const e = sessions.get(id);
      if (e && e.gain && Number.isFinite(v)) e.gain.gain.value = Math.min(1, Math.max(0, v));
    },
  };
})(globalThis);
