// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

//go:build js && wasm

package sharedvfs

import (
	"syscall/js"
)

// IDBVFS is the IndexedDB-backed VFS used by the wasmbox terminal + files
// clients in the browser. It wraps an InMemoryVFS as a "hot" cache:
//
//   - Reads (List / Stat / IsDir / Read) hit the cache directly, so the
//     wasmbox render loop stays synchronous.
//   - Writes (Write / Mkdir / Remove) update the cache first (sync, returns)
//     then fire-and-forget the corresponding IDB op so the change survives
//     a page reload.
//
// On construction the caller hands us a single IDBDatabase handle (already
// opened by JS-side bootstrap) and a callback that loads the persisted tree
// into the cache. When the load completes (or finds an empty store) the
// onReady callback fires so the client can re-render with real data.
//
// The store schema is intentionally trivial: one object store keyed by full
// path; the value is a JS object {dir: bool, data: Uint8Array, modTime: str}.
// A flat key space means a "load all" boots into the cache in a single
// IDBObjectStore.getAll() call -- O(N) network traffic but N is tiny here.
type IDBVFS struct {
	// mem is the in-memory hot state. Reads are served from here; writes
	// land here first, then flush to IDB.
	mem *InMemoryVFS
	// db is the JS IDBDatabase handle the JS bootstrap opened for us.
	db js.Value
	// storeName is the object-store name the writes target. Default "vfs".
	storeName string
}

// NewIDBVFS builds an IDBVFS over an already-open IDBDatabase. The caller
// (the JS-side bootstrap or a Go helper that uses idb_open below) is
// responsible for creating the object store before this constructor runs.
//
// The returned VFS starts with an empty cache; call LoadAll() to populate
// it from the persisted store before serving the first List() to the UI.
func NewIDBVFS(db js.Value, storeName string) *IDBVFS {
	if storeName == "" {
		storeName = "vfs"
	}
	return &IDBVFS{
		mem:       NewInMemoryVFS(),
		db:        db,
		storeName: storeName,
	}
}

// LoadAll synchronously (from the Go caller's POV) repopulates the cache
// from IDB. Internally it kicks off an IDBObjectStore.getAll() and blocks
// on a channel until the request fires its `success` event.
//
// Returns false if the store turned out to be empty (so the caller knows
// to call SeedDemoTree to bootstrap). Returns true otherwise.
func (v *IDBVFS) LoadAll() bool {
	tx := v.db.Call("transaction", v.storeName, "readonly")
	store := tx.Call("objectStore", v.storeName)
	req := store.Call("getAll")
	// keysReq fetches the parallel keys so we can rebuild paths.
	keysReq := store.Call("getAllKeys")

	done := make(chan struct{}, 2)
	finish := func() { done <- struct{}{} }

	var values, keys js.Value
	successVals := js.FuncOf(func(this js.Value, _ []js.Value) any {
		values = req.Get("result")
		finish()
		return nil
	})
	defer successVals.Release()
	req.Set("onsuccess", successVals)
	errVals := js.FuncOf(func(_ js.Value, _ []js.Value) any { finish(); return nil })
	defer errVals.Release()
	req.Set("onerror", errVals)

	successKeys := js.FuncOf(func(this js.Value, _ []js.Value) any {
		keys = keysReq.Get("result")
		finish()
		return nil
	})
	defer successKeys.Release()
	keysReq.Set("onsuccess", successKeys)
	errKeys := js.FuncOf(func(_ js.Value, _ []js.Value) any { finish(); return nil })
	defer errKeys.Release()
	keysReq.Set("onerror", errKeys)

	<-done
	<-done

	if !values.Truthy() || !keys.Truthy() {
		return false
	}
	n := keys.Get("length").Int()
	if n == 0 {
		return false
	}
	for i := 0; i < n; i++ {
		key := keys.Index(i).String()
		val := values.Index(i)
		if !val.Truthy() {
			continue
		}
		isDir := val.Get("dir").Bool()
		modTime := DemoModTime
		if mt := val.Get("modTime"); mt.Type() == js.TypeString {
			modTime = mt.String()
		}
		if isDir {
			if Clean(key) == "/" {
				continue // root is created by NewInMemoryVFS
			}
			// Ensure the node exists even if its parent has not been
			// processed yet -- we walk parents below.
			ensureDirTo(v.mem, key, modTime)
		} else {
			ensureDirTo(v.mem, Parent(key), modTime)
			// Pull bytes via js.CopyBytesToGo.
			data := val.Get("data")
			length := data.Get("length").Int()
			buf := make([]byte, length)
			if length > 0 {
				js.CopyBytesToGo(buf, data)
			}
			_ = v.mem.Write(key, buf)
		}
	}
	return true
}

// ensureDirTo creates every ancestor of p as a directory in mem if it does
// not already exist. Used by LoadAll so out-of-order key arrival never leaves
// the cache with an orphan file whose parent is missing.
func ensureDirTo(mem *InMemoryVFS, p, modTime string) {
	c := Clean(p)
	if c == "/" {
		return
	}
	ensureDirTo(mem, Parent(c), modTime)
	if !mem.IsDir(c) {
		_ = mem.Mkdir(c)
	}
}

// List reads from the in-memory cache.
func (v *IDBVFS) List(dir string) ([]Entry, error) { return v.mem.List(dir) }

// Stat reads from the in-memory cache.
func (v *IDBVFS) Stat(p string) (Entry, error) { return v.mem.Stat(p) }

// IsDir reads from the in-memory cache.
func (v *IDBVFS) IsDir(p string) bool { return v.mem.IsDir(p) }

// Read reads from the in-memory cache.
func (v *IDBVFS) Read(p string) ([]byte, error) { return v.mem.Read(p) }

// Write updates the cache then fires a put into IDB.
func (v *IDBVFS) Write(p string, data []byte) error {
	if err := v.mem.Write(p, data); err != nil {
		return err
	}
	v.idbPut(Clean(p), false, data)
	return nil
}

// Mkdir updates the cache then fires a put into IDB for the directory
// marker.
func (v *IDBVFS) Mkdir(p string) error {
	if err := v.mem.Mkdir(p); err != nil {
		return err
	}
	v.idbPut(Clean(p), true, nil)
	return nil
}

// Remove updates the cache then fires a recursive delete into IDB.
func (v *IDBVFS) Remove(p string) error {
	// Collect descendants BEFORE the in-memory delete so we know what to
	// remove from IDB (the in-memory remove is recursive too).
	c := Clean(p)
	victims := []string{c}
	prefix := c + "/"
	for k := range v.mem.nodes {
		if k != c && len(k) > len(prefix) && k[:len(prefix)] == prefix {
			victims = append(victims, k)
		}
	}
	if err := v.mem.Remove(p); err != nil {
		return err
	}
	for _, k := range victims {
		v.idbDelete(k)
	}
	return nil
}

// idbPut writes a single node into the IDB store. Fire-and-forget: a write
// failure logs to console but does not propagate to the Go caller (the
// in-memory write already succeeded; persistence is best-effort).
func (v *IDBVFS) idbPut(path string, isDir bool, data []byte) {
	tx := v.db.Call("transaction", v.storeName, "readwrite")
	store := tx.Call("objectStore", v.storeName)
	val := js.Global().Get("Object").New()
	val.Set("dir", isDir)
	val.Set("modTime", DemoModTime)
	if !isDir {
		u8 := js.Global().Get("Uint8Array").New(len(data))
		if len(data) > 0 {
			js.CopyBytesToJS(u8, data)
		}
		val.Set("data", u8)
	}
	store.Call("put", val, path)
}

// idbDelete removes a single node from the IDB store. Fire-and-forget.
func (v *IDBVFS) idbDelete(path string) {
	tx := v.db.Call("transaction", v.storeName, "readwrite")
	store := tx.Call("objectStore", v.storeName)
	store.Call("delete", path)
}

// OpenIDB opens (or upgrades) the named IndexedDB database and ensures the
// "vfs" object store exists. Returns the IDBDatabase handle synchronously
// from the Go caller's POV (it blocks on the open's success event).
//
// dbName    -- IndexedDB database name (e.g. "wasmbox").
// storeName -- object-store name (e.g. "vfs").
//
// Returns a zero js.Value if `indexedDB` is unavailable (non-browser host).
// Callers should fall back to InMemoryVFS in that case.
func OpenIDB(dbName, storeName string) js.Value {
	idb := js.Global().Get("indexedDB")
	if !idb.Truthy() {
		return js.Value{}
	}
	req := idb.Call("open", dbName, 1)
	done := make(chan struct{}, 1)
	var db js.Value

	upgrade := js.FuncOf(func(this js.Value, _ []js.Value) any {
		odb := req.Get("result")
		names := odb.Get("objectStoreNames")
		if !names.Call("contains", storeName).Bool() {
			odb.Call("createObjectStore", storeName)
		}
		return nil
	})
	defer upgrade.Release()
	req.Set("onupgradeneeded", upgrade)

	success := js.FuncOf(func(this js.Value, _ []js.Value) any {
		db = req.Get("result")
		done <- struct{}{}
		return nil
	})
	defer success.Release()
	req.Set("onsuccess", success)

	errf := js.FuncOf(func(_ js.Value, _ []js.Value) any { done <- struct{}{}; return nil })
	defer errf.Release()
	req.Set("onerror", errf)

	<-done
	return db
}
