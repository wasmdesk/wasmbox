// SPDX-License-Identifier: BSD-3-Clause
//
// wasmbox audio relay, compositor-worker half. A client worker cannot make
// sound (no AudioContext in a DedicatedWorker, and the compositor is a worker
// too), so it sends `audio_open` with a ring SharedArrayBuffer; this file
// validates it and forwards it to the page, which owns the AudioContext
// (audio-host.js). Samples never pass through here.
//
// Loaded with importScripts after audio-proto.js. The compositor's per-client
// message listeners call wasmboxAudioIntercept(worker, data) first and skip
// the Ruby bus when it returns true, so the Ruby side never sees audio_*.
//
// Isolation: a session is bound to the client's own MessagePort (the
// compositor-side object, never a client-supplied id), the id the page sees is
// allocated here, and the ring SAB is only ever forwarded for the session that
// sent it. A client holds no reference to any other client's ring.

"use strict";

(function (g) {
  const A = g.WASMBOX_AUDIO;
  const byPort = new Map();   // client port (compositor side) -> session id
  let nextId = 1;

  function reply(worker, windowId, msg) {
    try { worker.postMessage(Object.assign({ window_id: windowId }, msg)); } catch (_) {}
  }

  function closeSession(key) {
    const id = byPort.get(key);
    if (id === undefined) return;
    byPort.delete(key);
    self.postMessage({ type: "audio_close", id });
  }

  // Returns true when the message was an audio_* message (consumed).
  g.wasmboxAudioIntercept = function (worker, m) {
    if (!m || typeof m.type !== "string" || m.type.lastIndexOf("audio_", 0) !== 0) return false;
    const key = (worker && worker._port) || worker;
    const windowId = Number.isInteger(m.window_id) ? m.window_id : null;
    if (m.type === "audio_open") {
      if (byPort.has(key)) { reply(worker, windowId, { type: "audio_welcome", ok: false, reason: "already open" }); return true; }
      if (byPort.size >= A.MAX_SESSIONS) { reply(worker, windowId, { type: "audio_welcome", ok: false, reason: "too many audio sessions" }); return true; }
      const v = A.validateOpen(m);
      if (!v.ok) { reply(worker, windowId, { type: "audio_welcome", ok: false, reason: v.reason }); return true; }
      const id = nextId++;
      byPort.set(key, id);
      self.postMessage({
        type: "audio_open", id, window_id: windowId,
        sampleRate: v.sampleRate, channels: v.channels, capacityFrames: v.capacityFrames, sab: v.sab,
      });
      reply(worker, windowId, { type: "audio_welcome", ok: true, id });
      return true;
    }
    if (m.type === "audio_close") closeSession(key);
    return true;   // unknown audio_* types are dropped, not forwarded to Ruby
  };

  // Called when a client worker is terminated.
  g.wasmboxAudioClose = function (worker) { closeSession((worker && worker._port) || worker); };
  g.__wasmboxAudioSessions = function () { return byPort.size; };
})(self);
