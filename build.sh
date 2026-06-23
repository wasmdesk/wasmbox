#!/bin/sh
# build.sh — build the rbgo WebAssembly interpreter for the wasmbox compositor.
#
# Clones (once) and builds the pure-Go go-embedded-ruby interpreter for
# GOOS=js GOARCH=wasm into ./rbgo.wasm, and copies the matching Go wasm_exec.js
# next to it. With `serve`, also starts a static http server on :8080.
#
#   ./build.sh          # build rbgo.wasm + wasm_exec.js
#   ./build.sh serve    # build, then serve http://localhost:8080/
set -eu

SRC=${RBGO_SRC:-${TMPDIR:-/tmp}/go-embedded-ruby}
OUT="$PWD/rbgo.wasm"

if [ ! -d "$SRC/.git" ]; then
	echo "cloning go-embedded-ruby into $SRC ..."
	git clone --depth 1 https://github.com/go-embedded-ruby/ruby "$SRC"
fi

echo "building rbgo.wasm (GOOS=js GOARCH=wasm) ..."
(cd "$SRC" && GOWORK=off GOOS=js GOARCH=wasm go build -o "$OUT" ./cmd/wasm)

GOROOT=$(go env GOROOT)
for p in "$GOROOT/lib/wasm/wasm_exec.js" "$GOROOT/misc/wasm/wasm_exec.js"; do
	if [ -f "$p" ]; then cp "$p" ./wasm_exec.js && break; fi
done
echo "done: rbgo.wasm + wasm_exec.js"

if [ "${1:-}" = serve ]; then
	echo "serving http://localhost:8080/ — open /index.html"
	python3 -m http.server 8080
fi
