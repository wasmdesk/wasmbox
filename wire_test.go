// SPDX-License-Identifier: BSD-3-Clause
//
// Cross-file consistency test for the step-C.1 worker<->worker MessageChannel
// wiring. The compositor worker spawns each external client with a dedicated
// MessagePort handoff; the client SDK swaps from `self` to that port. Both
// ends of the swap are coupled by the `__wasmbox_port` magic message type +
// the `transfer` list -- a regression in either half would silently fall back
// to the step-C nested-worker direct channel and lose the per-client isolation
// the architecture relies on.
//
// We can't actually run a browser from `go test`, so this is a textual pin:
// it asserts that the producer (compositor.worker.js) and the consumer
// (clients/sdk/sdk.js + clients/dock/sdk.js) both reference the contract.
//
//go:build !js
// +build !js

package main

import (
	"strings"
	"testing"
)

// TestCompositorSpawnsMessageChannel pins the spawn path: the compositor MUST
// allocate a MessageChannel per client, transfer port2, and retain port1 for
// outbound traffic.
func TestCompositorSpawnsMessageChannel(t *testing.T) {
	src := readSource(t, "compositor.worker.js")

	// 1. The spawner allocates a fresh MessageChannel.
	if !strings.Contains(src, "new MessageChannel(") {
		t.Errorf("compositor.worker.js does not allocate a MessageChannel per spawn")
	}

	// 2. The spawner transfers port2 to the new worker (transfer list MUST
	//    contain the port -- otherwise port2 is structured-cloned and the
	//    other side gets an orphan stub).
	if !strings.Contains(src, "[channel.port2]") {
		t.Errorf("compositor.worker.js does not transfer port2 to the spawned worker")
	}

	// 3. The handoff message uses the agreed-upon type.
	if !strings.Contains(src, "B.COMP_TO_CLIENT_PORT") {
		t.Errorf("compositor.worker.js does not use B.COMP_TO_CLIENT_PORT for the port handoff")
	}

	// 4. The compositor retains port1 as the outbound channel (the returned
	//    wrapper's postMessage MUST route to channel.port1, not the raw
	//    worker -- otherwise Ruby's wasmboxPostMessage would dead-end).
	if !strings.Contains(src, "channel.port1") {
		t.Errorf("compositor.worker.js does not retain channel.port1 for outbound traffic")
	}
	// port1.start() must be called AFTER the listener is attached (otherwise
	// any messages buffered before attach get silently dropped). The spawn
	// path leaves the port unstarted on purpose; wasmboxAttachWorker drains
	// it via worker._port.start() once it has wired the message listener.
	if !strings.Contains(src, "worker._port.start()") {
		t.Errorf("compositor.worker.js does not start() the port from wasmboxAttachWorker (buffered hello would be dropped)")
	}

	// 5. The wrapper preserves the worker-shaped API Ruby's compositor.rb
	//    calls into (postMessage + addEventListener). Without these, the
	//    existing wasmboxAttachWorker / wasmboxPostMessage calls would throw.
	for _, want := range []string{
		"postMessage(msg",
		"addEventListener(name",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("compositor.worker.js spawn wrapper missing %q", want)
		}
	}
}

// TestSDKSwapsToMessagePort pins the client end of the swap: both SDK copies
// (shared + dock) must register a port handler at module load and swap their
// active channel when the port arrives.
func TestSDKSwapsToMessagePort(t *testing.T) {
	for _, path := range []string{
		"clients/sdk/sdk.js",
		"clients/dock/sdk.js",
	} {
		src := readSource(t, path)

		// 1. Listens on `self` for the magic port-handoff type.
		if !strings.Contains(src, `"__wasmbox_port"`) {
			t.Errorf("%s does not handle the __wasmbox_port handoff", path)
		}

		// 2. Defines a swapChannel helper that re-attaches the active
		//    client's listener and start()s the port.
		if !strings.Contains(src, "swapChannel(") {
			t.Errorf("%s does not define swapChannel", path)
		}
		if !strings.Contains(src, "port.addEventListener(\"message\"") {
			t.Errorf("%s does not re-attach the message listener to the new port", path)
		}
		if !strings.Contains(src, "port.start()") {
			t.Errorf("%s does not call port.start() after swapping", path)
		}

		// 3. Exposes the test seam.
		if !strings.Contains(src, "useMessagePort") {
			t.Errorf("%s does not expose WasmboxClient.useMessagePort for tests", path)
		}

		// 4. All outbound posts go through activeChannel, NOT the raw `g`
		//    binding -- otherwise the swap is a no-op for application
		//    traffic. We assert there are no remaining `g.postMessage(`
		//    call sites for the protocol messages.
		if strings.Contains(src, "g.postMessage(") {
			t.Errorf("%s still posts via g.postMessage (should route through activeChannel)", path)
		}
	}
}

// TestSDKBuffersSendsUntilPortArrives pins the race-fix: the SDK MUST queue
// application sends (hello/commit/...) until activeChannel is a real
// MessagePort. Otherwise the client's hello, sent synchronously during the
// `client.start()` call at module load, lands on `self` BEFORE the port
// handoff has a chance to run (the bootPortHandler is a queued event), gets
// delivered to the compositor's own self.onmessage which only handles main-
// thread bridge traffic, and is silently dropped -- a bug we hit + caught
// during step-C.1 verification.
func TestSDKBuffersSendsUntilPortArrives(t *testing.T) {
	for _, path := range []string{
		"clients/sdk/sdk.js",
		"clients/dock/sdk.js",
	} {
		src := readSource(t, path)

		// Channel starts null/unset, not `= g`. The `null` initial state is
		// what triggers the send() path to enqueue instead of post.
		if !strings.Contains(src, "activeChannel = null") {
			t.Errorf("%s: activeChannel must start as null so send() buffers (got initial activeChannel=g or similar)", path)
		}

		// A pendingSends queue exists, and flushPending drains it after the
		// port swap.
		for _, want := range []string{
			"pendingSends",
			"flushPending",
		} {
			if !strings.Contains(src, want) {
				t.Errorf("%s: missing %q (no buffer means a racy hello)", path, want)
			}
		}

		// swapChannel calls flushPending after assigning the port, so the
		// queued hello/commit go out as soon as the channel is alive.
		if !strings.Contains(src, "flushPending()") {
			t.Errorf("%s: swapChannel must call flushPending() to drain buffered sends", path)
		}
	}
}

// TestBridgeExportsPortHandoffConstant pins that the magic type lives in
// bridge.js (one source of truth) rather than scattered string literals on
// each side of the channel.
func TestBridgeExportsPortHandoffConstant(t *testing.T) {
	bridge := readSource(t, "bridge.js")
	if !strings.Contains(bridge, "COMP_TO_CLIENT_PORT") {
		t.Errorf("bridge.js does not export COMP_TO_CLIENT_PORT (per-client MessagePort handoff type)")
	}
	if !strings.Contains(bridge, `"__wasmbox_port"`) {
		t.Errorf("bridge.js does not define COMP_TO_CLIENT_PORT = \"__wasmbox_port\"")
	}
}

// TestBridgeExportsAssetsHandoffConstant pins the OCI-launch assets envelope
// type next to the port handoff: both are transport-setup messages flowing
// over the implicit `self` channel before application traffic starts on the
// MessageChannel. A regression that splits the constant between files would
// silently break the OCI client boot path.
func TestBridgeExportsAssetsHandoffConstant(t *testing.T) {
	bridge := readSource(t, "bridge.js")
	if !strings.Contains(bridge, "COMP_TO_CLIENT_ASSETS") {
		t.Errorf("bridge.js does not export COMP_TO_CLIENT_ASSETS (OCI assets handoff type)")
	}
	if !strings.Contains(bridge, `"__wasmbox_assets"`) {
		t.Errorf("bridge.js does not define COMP_TO_CLIENT_ASSETS = \"__wasmbox_assets\"")
	}
}

// TestCompositorSpawnsFromOCI pins the OCI launch path in the compositor
// worker: it must import the loader, expose wasmboxSpawnFromOCI, send the
// assets envelope BEFORE the port handoff, and dispatch the assets envelope
// over `self` (not the per-client MessageChannel that does not exist yet at
// assets-handoff time).
func TestCompositorSpawnsFromOCI(t *testing.T) {
	src := readSource(t, "compositor.worker.js")

	// 1. The OCI loader is imported into the worker scope.
	if !strings.Contains(src, `importScripts("./ociapps-loader.js")`) {
		t.Errorf("compositor.worker.js does not importScripts ociapps-loader.js")
	}

	// 2. The public hook exists.
	if !strings.Contains(src, "wasmboxSpawnFromOCI") {
		t.Errorf("compositor.worker.js does not expose wasmboxSpawnFromOCI")
	}

	// 3. The spawn helper resolves an app through OCIAppsLoader.loadApp.
	if !strings.Contains(src, ".loadApp(") {
		t.Errorf("compositor.worker.js does not call loader.loadApp from the OCI spawn path")
	}

	// 4. Each pulled file is wrapped in a Blob URL.
	if !strings.Contains(src, "URL.createObjectURL(new Blob(") {
		t.Errorf("compositor.worker.js does not wrap OCI blobs in createObjectURL")
	}

	// 5. The assets envelope uses the bridge constant.
	if !strings.Contains(src, "B.COMP_TO_CLIENT_ASSETS") {
		t.Errorf("compositor.worker.js does not use B.COMP_TO_CLIENT_ASSETS for the assets handoff")
	}

	// 6. The Ruby-facing bus dispatcher exists (lets compositor.rb spawn an
	//    OCI app via a CustomEvent on the same bus as static spawns).
	if !strings.Contains(src, "wasmboxSpawnExternalOCI") {
		t.Errorf("compositor.worker.js does not expose wasmboxSpawnExternalOCI")
	}
	if !strings.Contains(src, "wasmboxSpawnFromOCIAndAttach") {
		t.Errorf("compositor.worker.js does not expose wasmboxSpawnFromOCIAndAttach (async spawn+attach helper)")
	}

	// 7. The OCI registry override hook is documented (we read it lazily so
	//    a late assignment is honoured).
	if !strings.Contains(src, "WASMBOX_OCI_REGISTRIES") {
		t.Errorf("compositor.worker.js does not honour globalThis.WASMBOX_OCI_REGISTRIES")
	}
}

// TestSDKExposesOCIAssetsBoot pins the SDK end of the OCI launch path: a
// module-load assets listener stashes the envelope, and the public helper
// returns a Promise so worker.js can await it before importScripts.
func TestSDKExposesOCIAssetsBoot(t *testing.T) {
	src := readSource(t, "clients/sdk/sdk.js")

	// 1. The SDK listens on `self` for the magic assets type at module load.
	if !strings.Contains(src, `"__wasmbox_assets"`) {
		t.Errorf("clients/sdk/sdk.js does not handle the __wasmbox_assets envelope")
	}

	// 2. The helper exists.
	if !strings.Contains(src, "bootFromOCIAssets") {
		t.Errorf("clients/sdk/sdk.js does not expose WasmboxClient.bootFromOCIAssets")
	}

	// 3. Test seam exposed.
	if !strings.Contains(src, "_setOCIAssets") {
		t.Errorf("clients/sdk/sdk.js does not expose WasmboxClient._setOCIAssets (test seam)")
	}
}

// TestOCIAppsLoaderShape pins the public API of the browser-side loader so
// the compositor worker can rely on it without poking at internals. The Go
// twin (github.com/wasmdesk/ociapps) carries the same surface; we re-pin
// here because the JS loader is shipped as a separate file.
func TestOCIAppsLoaderShape(t *testing.T) {
	src := readSource(t, "ociapps-loader.js")
	for _, want := range []string{
		"class OCIAppsLoader",
		"loadApp(",
		"fetchManifest(",
		"fetchBlob(",
		"verifyDigest",
		"ociapps.path/",
		"sha256:",
		"MemoryCache",
		"IDBCache",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("ociapps-loader.js is missing required token %q", want)
		}
	}
}
