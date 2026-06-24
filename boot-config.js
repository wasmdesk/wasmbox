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
