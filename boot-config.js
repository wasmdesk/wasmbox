// SPDX-License-Identifier: BSD-3-Clause
//
// boot-config.js -- main-thread boot-time configuration for wasmbox.
//
// Loaded as a regular <script> ahead of the worker spawn in index.html. The
// purpose is to expose a small set of page-level toggles a Playwright probe
// (or a curious user) can flip via query string + localStorage without
// modifying the compositor bootstrap inline.
//
// Toggles supported:
//
//   ?ociapps=1     - after the compositor signals C2M_READY, auto-spawn each
//                    OCI-bundled client from the local registry (i.e. the
//                    five demo apps the Taskfile's `demo-wasmbox-ociapps`
//                    one-shot pushed to http://localhost:5000). The default
//                    registry list (DEFAULT_OCI_REGISTRIES) already points at
//                    http://127.0.0.1:5000 inside the compositor worker, so
//                    no extra wiring is needed -- the apps just spawn.
//
//                    The list of refs to spawn is curated here (the LAUNCHABLE
//                    table in compositor.rb is the trust boundary; this page
//                    just hands the compositor known-good refs).
//
//   ?ociapps_only=1 - like ociapps=1 but only spawn the named subset, useful
//                     when reproducing a single-app issue. Example:
//                       ?ociapps=1&ociapps_only=hello,terminal
//
//   localStorage["wasmbox.ociapps.enable"] = "1"  - sticky equivalent of
//                                                    ?ociapps=1; survives a
//                                                    reload.

"use strict";

// Saved-window-size relay for Quake (2026-06-29). The quake worker
// needs the persisted geometry from localStorage["wasmbox.layout"] so
// the Go side can allocate the SAB at the user's last window size
// instead of the hardcoded 320x240. But `localStorage` is window-only
// (`self.localStorage === undefined` inside a Web Worker), so the
// worker cannot read the key itself. We answer a small
// BroadcastChannel query from the main thread instead.
//
// Protocol: a single channel ("wasmbox.quake.fb"); the worker posts
// {type:"wasmbox.quake.fb/query"}, we reply with
// {type:"wasmbox.quake.fb/response", layout: <string|null>}. Same
// origin so the channel is delivered to every worker in the page.
// The relay must be registered before the quake worker spawns; this
// file is loaded as a regular <script> in index.html ahead of any
// worker construction, which satisfies that ordering.
(function () {
  // Snapshot the persisted layout AT SCRIPT-LOAD TIME, before the
  // compositor wasm boots + starts re-serializing its own state into
  // the same key. By the time the quake worker queries us (a few
  // hundred ms later, after `new Worker(...)` + module load), the
  // compositor's tick has already overwritten localStorage with its
  // current windows -- which does NOT yet include the quake entry
  // (quake hasn't sent its hello yet). The snapshot preserves the
  // pre-boot state -- exactly what the worker needs to size its SAB
  // at the user's last-known quake window dims.
  let layoutSnapshot = null;
  try {
    layoutSnapshot = globalThis.localStorage
      ? globalThis.localStorage.getItem("wasmbox.layout")
      : null;
  } catch (_) { layoutSnapshot = null; }

  try {
    const bc = new BroadcastChannel("wasmbox.quake.fb");
    bc.addEventListener("message", (ev) => {
      const m = ev.data;
      if (!m || m.type !== "wasmbox.quake.fb/query") return;
      try {
        bc.postMessage({ type: "wasmbox.quake.fb/response", layout: layoutSnapshot });
        console.log("boot-config: served wasmbox.quake.fb query (layout=" +
          (layoutSnapshot ? layoutSnapshot.length + " bytes" : "null") + ")");
      } catch (_) {}
    });
    console.log("boot-config: wasmbox.quake.fb relay armed (snapshot=" +
      (layoutSnapshot ? layoutSnapshot.length + " bytes" : "null") + ")");
  } catch (_) {
    // BroadcastChannel unsupported (legacy browsers); the worker will
    // fall back to its default 800x600 after the query timeout.
  }
})();

(function () {
  const params = new URLSearchParams(globalThis.location ? globalThis.location.search : "");
  let enable = params.get("ociapps") === "1";
  try {
    if (!enable && globalThis.localStorage && globalThis.localStorage.getItem("wasmbox.ociapps.enable") === "1") {
      enable = true;
    }
  } catch (_) { /* private mode: ignore */ }

  // Default demo set -- the OCI refs `task demo-wasmbox-ociapps` pushes to
  // the local registry. The page just hands them to wasmboxSpawnFromOCI;
  // the compositor's OCIAppsLoader pulls each one + spawns the worker.
  const DEFAULT_REFS = [
    "hello:latest",
    "dock:latest",
    "terminal:latest",
    "files:latest",
  ];

  let refs = DEFAULT_REFS;
  const only = params.get("ociapps_only");
  if (only) {
    const allow = new Set(only.split(",").map((s) => s.trim()).filter(Boolean));
    refs = DEFAULT_REFS.filter((r) => allow.has(r.split(":")[0]));
  }

  // Expose for the Playwright probe + for the inline bootstrap below to read.
  globalThis.WASMBOX_BOOT_CONFIG = Object.freeze({
    ociapps:      enable,
    ociapps_refs: refs,
  });

  // Wire the auto-spawn. The compositor sets globalThis.wasmboxReady = true
  // when it has finished bootstrapping; once it does we kick off one
  // wasmboxSpawnFromOCI per ref. The compositor relays the ref to the worker
  // (which owns the OCI loader), pulls the blobs, and spawns the client
  // worker. Failures are surfaced on the console so the Playwright probe
  // catches them.
  if (!enable) return;

  function pump() {
    if (!globalThis.wasmboxReady) { setTimeout(pump, 50); return; }
    if (typeof globalThis.wasmboxSpawnFromOCI !== "function") {
      console.error("boot-config: ociapps=1 but wasmboxSpawnFromOCI missing");
      return;
    }
    // Stagger the spawns slightly so the compositor's per-spawn fetch +
    // worker construction don't all race for the same MessageChannel slot.
    // The compositor handles concurrent spawns correctly; this just keeps
    // the network waterfall easier to read in devtools.
    refs.forEach((ref, i) => {
      setTimeout(() => {
        try { globalThis.wasmboxSpawnFromOCI(ref); }
        catch (e) { console.error("boot-config spawn(" + ref + "):", e); }
      }, i * 80);
    });
    console.log("boot-config: ociapps enabled; spawned refs: " + refs.join(", "));
  }
  pump();
})();
