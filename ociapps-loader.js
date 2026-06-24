// SPDX-License-Identifier: BSD-3-Clause
//
// Browser-side OCI Distribution v2 client used by the compositor worker to
// load wasmbox client apps (worker.js + wasm_exec.js + <app>.wasm bundles)
// from an OCI registry instead of from a static path inside the compositor
// asset tree.
//
// This is the JS twin of the Go package github.com/wasmdesk/ociapps:
// same wire vocabulary, same annotation convention, same multi-registry
// fallback policy. We do NOT compile ociapps to wasm here because the only
// thing the compositor worker needs is HTTP GET against /v2/<repo>/manifests/
// and /v2/<repo>/blobs/ -- a few hundred lines of JS, no heavy lifting --
// and shipping a second wasm module just to run that would double the boot
// payload for marginal benefit.
//
// Convention (locked with the Go package + with the ociapps packer):
//
//   * Each app OCI image carries a manifest with annotations of the form
//
//       "ociapps.path/<filename>" -> "<digest>"
//
//     where <filename> is a VFS-relative name (e.g. "worker.js",
//     "wasm_exec.js", "hello.wasm") and <digest> is "sha256:<hex>". The
//     digest MUST match one of the layer descriptors in the manifest --
//     LoadApp does not trust files outside that annotation map.
//
//   * The manifest's schemaVersion must be 2 (the only OCI image-manifest
//     version supported). Any other value is rejected.
//
//   * Blob bytes are sha256-verified before being cached, so a registry
//     that returns the wrong bytes for a digest cannot silently feed them
//     to the compositor.
//
// Cache: blob bytes (which are content-addressed) are persisted in
// IndexedDB so an app load survives a page reload. The cache is keyed by
// digest, so a single store is correct across every registry and every
// app. The cache silently degrades to an in-memory Map when IndexedDB is
// unavailable (private mode, Safari workers in some configurations, ...);
// callers see no behaviour change.
//
// Public API:
//
//   class OCIAppsLoader {
//     constructor(registries: Array<{url:string}>)
//     loadApp(ref: string): Promise<{
//       manifest: object,
//       annotations: Record<string,string>,
//       files: Map<string, Uint8Array>,
//     }>
//   }
//
// The result shape mirrors ociapps.App in the Go package: a parsed
// manifest, a copy of the annotation map, and the union of every blob
// pulled from the registry keyed by its VFS-relative file name.

"use strict";

(function (g) {
  // Annotation prefix shared with the Go package's AnnotationPathPrefix.
  // Changing it would silently break interop with the ociapps packer.
  const ANNOTATION_PATH_PREFIX = "ociapps.path/";

  // Media type the compositor sends in Accept on the manifest GET. The
  // registry will fall back to the legacy docker manifest type if it does
  // not recognise this; we let the wire decide and just decode the body.
  const MEDIA_TYPE_MANIFEST = "application/vnd.oci.image.manifest.v1+json";

  // -------- digest helpers ------------------------------------------------

  // hexlify(buf) -> "<hex>" (lowercase, no separators).
  function hexlify(buf) {
    const u8 = new Uint8Array(buf);
    let s = "";
    for (let i = 0; i < u8.length; i++) {
      const v = u8[i];
      s += (v < 16 ? "0" : "") + v.toString(16);
    }
    return s;
  }

  // sha256Hex(bytes) -> "sha256:<hex>". Uses the WebCrypto SubtleCrypto
  // API, which is available in dedicated workers under cross-origin
  // isolation (the COOP/COEP setup the compositor already requires).
  async function sha256Hex(bytes) {
    const digestBuf = await g.crypto.subtle.digest("SHA-256", bytes);
    return "sha256:" + hexlify(digestBuf);
  }

  // VerifyDigest: assert sha256(data) === expected ("sha256:<hex>").
  async function verifyDigest(data, expected) {
    const got = await sha256Hex(data);
    if (got !== expected) {
      throw new Error(
        "ociapps: digest mismatch: want " + expected + " got " + got);
    }
  }

  // -------- IndexedDB cache ----------------------------------------------

  // blobCache is the public Cache surface. Implementations must provide
  // get(digest) -> Promise<Uint8Array|null> and put(digest, data) -> Promise.
  // Two implementations live below: idbCache (default) and memoryCache
  // (used when openDB rejects).

  const IDB_NAME = "wasmbox-ociapps";
  const IDB_STORE = "blobs";
  const IDB_VERSION = 1;

  function openDB() {
    return new Promise((resolve, reject) => {
      if (!g.indexedDB) {
        reject(new Error("no IndexedDB"));
        return;
      }
      let req;
      try {
        req = g.indexedDB.open(IDB_NAME, IDB_VERSION);
      } catch (e) {
        reject(e);
        return;
      }
      req.onupgradeneeded = () => {
        const db = req.result;
        if (!db.objectStoreNames.contains(IDB_STORE)) {
          db.createObjectStore(IDB_STORE);
        }
      };
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error || new Error("indexedDB.open failed"));
    });
  }

  function idbGet(db, key) {
    return new Promise((resolve, reject) => {
      const tx = db.transaction(IDB_STORE, "readonly");
      const store = tx.objectStore(IDB_STORE);
      const req = store.get(key);
      req.onsuccess = () => resolve(req.result ?? null);
      req.onerror = () => reject(req.error);
    });
  }

  function idbPut(db, key, value) {
    return new Promise((resolve, reject) => {
      const tx = db.transaction(IDB_STORE, "readwrite");
      const store = tx.objectStore(IDB_STORE);
      const req = store.put(value, key);
      req.onsuccess = () => resolve();
      req.onerror = () => reject(req.error);
    });
  }

  // idbCache lazily opens the DB on first use and falls back to a memory
  // map for the rest of the page lifetime if the open fails.
  class IDBCache {
    constructor() {
      this._dbPromise = null;
      this._fallback = null;
    }
    async _db() {
      if (this._fallback) return null;
      if (!this._dbPromise) this._dbPromise = openDB();
      try {
        return await this._dbPromise;
      } catch (_) {
        this._fallback = new Map();
        this._dbPromise = null;
        return null;
      }
    }
    async get(digest) {
      const db = await this._db();
      if (!db) return this._fallback.has(digest) ? this._fallback.get(digest) : null;
      try {
        const v = await idbGet(db, digest);
        return v ?? null;
      } catch (_) {
        return null;
      }
    }
    async put(digest, data) {
      const db = await this._db();
      if (!db) { this._fallback.set(digest, data); return; }
      try { await idbPut(db, digest, data); } catch (_) { /* ignore */ }
    }
  }

  // memoryCache is the explicit fallback / test seam: no persistence, just
  // a Map. The loader will swap to this if the caller passes one in.
  class MemoryCache {
    constructor() { this._m = new Map(); }
    async get(digest) { return this._m.has(digest) ? this._m.get(digest) : null; }
    async put(digest, data) { this._m.set(digest, data); }
  }

  // -------- manifest helpers ---------------------------------------------

  function parseRef(ref) {
    if (!ref) throw new Error("ociapps: empty reference");
    const colon = ref.lastIndexOf(":");
    let repo, tag;
    if (colon >= 0) {
      repo = ref.slice(0, colon);
      tag = ref.slice(colon + 1);
    } else {
      repo = ref;
      tag = "latest";
    }
    if (!repo) throw new Error("ociapps: invalid reference: empty repo in " + JSON.stringify(ref));
    if (!tag)  throw new Error("ociapps: invalid reference: empty tag in "  + JSON.stringify(ref));
    return { repo, tag };
  }

  function decodeManifest(text) {
    let m;
    try { m = JSON.parse(text); }
    catch (e) { throw new Error("ociapps: decode manifest: " + e.message); }
    if (m.schemaVersion !== 2) {
      throw new Error("ociapps: manifest schemaVersion must be 2, got " + m.schemaVersion);
    }
    return m;
  }

  // buildFileMap walks m.annotations and returns a name -> digest map for
  // every "ociapps.path/<name>" entry. Empty result rejects -- a manifest
  // without that namespace is from a different producer + we can't load it.
  function buildFileMap(m) {
    const out = new Map();
    const ann = m.annotations || {};
    for (const k of Object.keys(ann)) {
      if (!k.startsWith(ANNOTATION_PATH_PREFIX)) continue;
      const name = k.slice(ANNOTATION_PATH_PREFIX.length);
      const digest = ann[k];
      if (!name || !digest) continue;
      out.set(name, digest);
    }
    if (out.size === 0) {
      throw new Error("ociapps: manifest has no ociapps.path/* annotations");
    }
    return out;
  }

  // -------- registry HTTP -------------------------------------------------

  function trimRightSlash(s) { return s.replace(/\/+$/, ""); }

  async function fetchManifestBytes(reg, repo, reference) {
    const u = trimRightSlash(reg.url) + "/v2/" + repo + "/manifests/" + reference;
    const r = await fetch(u, { headers: { Accept: MEDIA_TYPE_MANIFEST } });
    if (!r.ok) {
      throw new Error("ociapps: GET " + u + ": status " + r.status);
    }
    return await r.text();
  }

  async function fetchBlobBytes(reg, repo, digest) {
    if (!digest.startsWith("sha256:")) {
      throw new Error("ociapps: digest " + JSON.stringify(digest) + " missing sha256: prefix");
    }
    const u = trimRightSlash(reg.url) + "/v2/" + repo + "/blobs/" + digest;
    const r = await fetch(u);
    if (!r.ok) {
      throw new Error("ociapps: GET " + u + ": status " + r.status);
    }
    const buf = await r.arrayBuffer();
    return new Uint8Array(buf);
  }

  // -------- OCIAppsLoader -------------------------------------------------

  class OCIAppsLoader {
    // registries: Array<{url: string}>
    // options.cache: optional Cache implementation (defaults to IDBCache);
    //               pass new OCIAppsLoader.MemoryCache() to disable IDB.
    constructor(registries, options) {
      if (!Array.isArray(registries) || registries.length === 0) {
        throw new Error("ociapps: OCIAppsLoader requires at least one registry");
      }
      this.registries = registries.map((r) => ({ url: String(r.url || r) }));
      this.cache = (options && options.cache) || new IDBCache();
    }

    // fetchManifest: try every registry in order, return the first one that
    // responds OK + decodes. Returns {registry, manifest}.
    async fetchManifest(repo, reference) {
      let lastErr = null;
      for (const reg of this.registries) {
        try {
          const body = await fetchManifestBytes(reg, repo, reference);
          const m = decodeManifest(body);
          return { registry: reg, manifest: m };
        } catch (e) {
          lastErr = e;
        }
      }
      throw lastErr || new Error("ociapps: no registry returned a manifest");
    }

    // fetchBlob: cache-first by digest, then try every registry in order.
    // Bytes are sha256-verified before being cached.
    async fetchBlob(repo, digest) {
      const cached = await this.cache.get(digest);
      if (cached) return cached;
      let lastErr = null;
      for (const reg of this.registries) {
        try {
          const bytes = await fetchBlobBytes(reg, repo, digest);
          await verifyDigest(bytes, digest);
          await this.cache.put(digest, bytes);
          return bytes;
        } catch (e) {
          lastErr = e;
        }
      }
      throw lastErr || new Error("ociapps: no registry returned blob " + digest);
    }

    // loadApp: pull a manifest + every annotated blob and return them as a
    // Map keyed by the VFS-relative file name. Mirrors ociapps.LoadApp in
    // the Go package.
    async loadApp(ref) {
      const { repo, tag } = parseRef(ref);
      const { manifest } = await this.fetchManifest(repo, tag);
      if (!manifest.layers || manifest.layers.length === 0) {
        throw new Error("ociapps: manifest has no layers");
      }
      const fileMap = buildFileMap(manifest);
      const files = new Map();
      // Fetch in parallel: each entry is independent + content-addressed.
      const entries = Array.from(fileMap.entries());
      const bodies = await Promise.all(
        entries.map(([_, digest]) => this.fetchBlob(repo, digest)),
      );
      for (let i = 0; i < entries.length; i++) {
        files.set(entries[i][0], bodies[i]);
      }
      return {
        manifest: manifest,
        annotations: manifest.annotations || {},
        files: files,
      };
    }
  }

  // Expose the helpers + the loader. The compositor worker reaches the
  // loader through `globalThis.OCIAppsLoader`; the memory cache + parse
  // helpers are exposed for tests + for callers that want to swap caches.
  OCIAppsLoader.MemoryCache = MemoryCache;
  OCIAppsLoader.IDBCache = IDBCache;
  OCIAppsLoader.parseRef = parseRef;
  OCIAppsLoader.decodeManifest = decodeManifest;
  OCIAppsLoader.buildFileMap = buildFileMap;
  OCIAppsLoader.verifyDigest = verifyDigest;
  OCIAppsLoader.sha256Hex = sha256Hex;
  OCIAppsLoader.ANNOTATION_PATH_PREFIX = ANNOTATION_PATH_PREFIX;

  g.OCIAppsLoader = OCIAppsLoader;
})(typeof self !== "undefined" ? self : globalThis);
