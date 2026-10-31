// SPDX-License-Identifier: BSD-3-Clause
//
// audiotest: a wasmbox client that plays a 440 Hz tone through the audio relay.
// It exists for test/probe-audio.mjs (a pure-JS client, no wasm): it proves the
// whole path client ring -> compositor worker -> page AudioContext -> worklet.
//
//   clients/audiotest/worker.js            plays the tone
//   clients/audiotest/worker.js?nowrite=1  opens the ring but never writes (the
//                                          planted negative: played must stay 0)
//   &rate=N                                client sample rate (default 32000, so
//                                          the worklet resamples)
//   &cap=N                                 ring capacity in frames (default 1024)

"use strict";
importScripts("../sdk/sdk.js");
importScripts("../sdk/audio.js");

const q = new URLSearchParams(self.location.search);
const RATE = Number(q.get("rate") || 32000);
const CAP = Number(q.get("cap") || 1024);
const noWrite = q.get("nowrite") === "1";

const client = new WasmboxClient({ title: "audio tone", w: 200, h: 80 });
const audio = new WasmboxAudio({ sampleRate: RATE, channels: 2, capacityFrames: CAP });
self.audioTestStats = () => audio.stats();

client.onWelcome(() => {
  client.pixels.fill(40);
  client.commit();
  if (q.get("evil") === "1") {
    // Hostile client: every one of these must be refused by the relay.
    const good = { type: "audio_open", window_id: client.windowId, sampleRate: 48000, channels: 2, capacityFrames: 1024 };
    const tries = [
      { ...good, sab: new SharedArrayBuffer(100) },                              // size does not match
      { ...good, sampleRate: 1e9, sab: new SharedArrayBuffer(64 + 1024 * 8) },    // rate out of range
      { ...good, channels: 99, sab: new SharedArrayBuffer(64 + 1024 * 99 * 4) },  // too many channels
      { ...good, capacityFrames: 1000, sab: new SharedArrayBuffer(64 + 1000 * 8) }, // not a power of two
      { ...good, sab: new ArrayBuffer(64 + 1024 * 8) },                           // not shared
    ];
    for (const m of tries) { try { client.channel.postMessage(m); } catch (_) {} }
    // Control: after the five refusals one valid open must still be accepted
    // (so "0 sessions" cannot mean "the relay never looked"), and a second open
    // on the same port must be refused (one ring per client).
    client.channel.postMessage({ ...good, sab: new SharedArrayBuffer(64 + 1024 * 8) });
    client.channel.postMessage({ ...good, sab: new SharedArrayBuffer(64 + 1024 * 8) });
    return;
  }
  audio.open(client.channel, client.windowId);
  let phase = 0;
  const dphi = (2 * Math.PI * 440) / RATE;
  // Keep the ring about half full: that is the latency this client asks for.
  setInterval(() => {
    if (noWrite) return;
    const want = (CAP >> 1) - audio.buffered();
    if (want <= 0) return;
    const pcm = new Float32Array(want * 2);
    for (let i = 0; i < want; i++) {
      const s = 0.2 * Math.sin(phase); phase += dphi;
      pcm[2 * i] = s; pcm[2 * i + 1] = s;
    }
    audio.write(pcm);
  }, 5);
});
client.start();
