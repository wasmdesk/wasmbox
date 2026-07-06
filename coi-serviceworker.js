// SPDX-License-Identifier: BSD-3-Clause
//
// coi-serviceworker.js — make the page cross-origin isolated on a static host
// that cannot send response headers (GitHub Pages, S3, ...).
//
// wasmbox needs SharedArrayBuffer (clients render into a shared surface the
// compositor reads), and SharedArrayBuffer is only available when
// `self.crossOriginIsolated === true`, which in turn requires the top-level
// document to be served with:
//
//     Cross-Origin-Opener-Policy:   same-origin
//     Cross-Origin-Embedder-Policy: require-corp
//
// GitHub Pages serves only static files and cannot set those headers. The
// well-known fix (the "coi-serviceworker" technique) is to register a service
// worker that re-fetches every response and injects the two headers itself —
// the page then reloads once so the now-controlling worker can rewrite the
// navigation response, and from then on the document is cross-origin isolated.
// Every wasmbox subresource (wasm, the worker scripts, the same-origin /v2 OCI
// blobs) is same-origin, so COEP:require-corp lets them all load without any
// CORP/CORS dance — which is exactly why the OCI artifacts are mirrored
// same-origin rather than pulled from ghcr (ghcr sends no CORS headers at all).
//
// This file is BOTH the service worker and its own registrar: include it once
// with `<script src="coi-serviceworker.js"></script>` early in <head>. In a
// worker context `self.window` is undefined, so the SW half runs; in the page
// it registers itself.

"use strict";

if (typeof window === "undefined") {
  // ---- Service-worker half ------------------------------------------------
  self.addEventListener("install", () => self.skipWaiting());
  self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));

  self.addEventListener("message", (event) => {
    if (event.data && event.data.type === "coi-deregister") {
      self.registration.unregister().then(() => {
        return self.clients.matchAll();
      }).then((clients) => {
        clients.forEach((client) => client.navigate(client.url));
      });
    }
  });

  // withCOI rebuilds a response carrying the COOP/COEP(/CORP) headers a
  // SharedArrayBuffer page needs -- the original coi-serviceworker behaviour.
  const withCOI = (response) => {
    // Opaque/redirect responses (status 0) cannot be rebuilt; pass through.
    if (response.status === 0) return response;
    const headers = new Headers(response.headers);
    headers.set("Cross-Origin-Embedder-Policy", "require-corp");
    headers.set("Cross-Origin-Opener-Policy", "same-origin");
    // Same-origin resources are always allowed under COEP; tagging them
    // CORP:same-origin is belt-and-suspenders and harmless.
    if (!headers.has("Cross-Origin-Resource-Policy")) {
      headers.set("Cross-Origin-Resource-Policy", "same-origin");
    }
    return new Response(response.body, {
      status: response.status,
      statusText: response.statusText,
      headers,
    });
  };

  // Every lazily-loaded client wasm -- clients/<app>/<app>.wasm -- is
  // content-hash cached (quake, code, files, terminal, calculator, ...). The
  // compositor's own wasmbox.wasm loads eagerly at boot and is left to the
  // normal HTTP cache.
  const WASM_RE = /\/clients\/[^/]+\/[^/]+\.wasm(\?|$)/;
  const WASM_CACHE = "wasmbox-wasm-v1";

  // cachedWasm serves a client wasm (up to ~13 MB for quake) from CacheStorage
  // keyed by its CONTENT hash (from a tiny build-time manifest beside it,
  // <name>-wasm.json), independent of the origin's ETag/max-age. When the hash
  // is already cached the wasm is served with NO network body -- so a page
  // reload, and even a redeploy that only re-timestamps the file, does not
  // re-download it; only a real content change (new hash) fetches the body.
  // Each app is namespaced by its own path, so caching one never evicts
  // another. Any hiccup (missing manifest, cache error) rejects and the caller
  // falls back to the plain network path.
  const cachedWasm = async (req) => {
    // clients/<app>/<app>.wasm -> clients/<app>/<app>-wasm.json (same dir).
    const manifestURL = req.url.replace(/([^/]+)\.wasm(\?.*)?$/, "$1-wasm.json");
    const mres = await fetch(manifestURL, { cache: "no-store" });
    if (!mres.ok) throw new Error("no wasm manifest");
    const hash = (await mres.json()).sha256;
    if (!hash) throw new Error("wasm manifest missing sha256");
    // Namespace the cache key by this wasm's own path so apps don't collide.
    const keyPath = "/__wasmcache" + new URL(req.url).pathname;
    const keyURL = new Request(keyPath + "?h=" + hash).url;
    const prefix = new Request(keyPath + "?h=").url;
    const cache = await caches.open(WASM_CACHE);
    const hit = await cache.match(keyURL);
    if (hit) return hit;
    const resp = withCOI(await fetch(req));
    if (resp.ok) {
      await cache.put(keyURL, resp.clone());
      // Evict stale hashes for THIS app only (leave other apps' wasms cached).
      for (const k of await cache.keys()) {
        if (k.url.startsWith(prefix) && k.url !== keyURL) {
          await cache.delete(k);
        }
      }
    }
    return resp;
  };

  self.addEventListener("fetch", (event) => {
    const req = event.request;
    // Leave cache-only cross-origin probes alone (Chromium throws otherwise).
    if (req.cache === "only-if-cached" && req.mode !== "same-origin") return;

    // Content-hash cache path for the big Quake wasm (falls back to network).
    if (req.method === "GET" && WASM_RE.test(req.url)) {
      event.respondWith(cachedWasm(req).catch(() => fetch(req).then(withCOI)));
      return;
    }

    event.respondWith(
      fetch(req).then(withCOI).catch((e) => {
        // A network error here would otherwise blank the page; log + rethrow
        // so the failure is visible rather than silent.
        console.error("coi-serviceworker fetch:", e);
        throw e;
      }),
    );
  });
} else {
  // ---- Page-registration half ---------------------------------------------
  (() => {
    // Already isolated (headers present, or a previous load installed us), or
    // the browser doesn't expose the flag: nothing to do.
    if (window.crossOriginIsolated !== false) return;
    if (!window.isSecureContext) return; // SWs need https / localhost
    if (!("serviceWorker" in navigator)) return;

    const src = (document.currentScript && document.currentScript.src) || "coi-serviceworker.js";
    navigator.serviceWorker.register(src).then(
      (registration) => {
        // A brand-new worker means this load is still uncontrolled (no COOP/
        // COEP yet) — reload once so the controlling worker rewrites the
        // navigation response and the reloaded document becomes isolated.
        registration.addEventListener("updatefound", () => window.location.reload());
        if (registration.active && !navigator.serviceWorker.controller) {
          window.location.reload();
        }
      },
      (err) => console.error("coi-serviceworker registration failed:", err),
    );
  })();
}
