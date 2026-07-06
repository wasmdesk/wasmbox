#!/bin/sh
# wasm-manifest.sh writes a content-hash manifest <name>-wasm.json beside each
# built client wasm. coi-serviceworker.js reads it to cache the wasm in
# CacheStorage keyed by sha256, so a lazily-loaded app is re-downloaded only
# when its bytes actually change -- surviving reloads and GitHub Pages
# redeploys (whose mtime-based ETag otherwise busts the cache every deploy).
#
# Usage:
#   scripts/wasm-manifest.sh clients/hello/hello.wasm [clients/code/code.wasm ...]
#   scripts/wasm-manifest.sh clients/*/*.wasm
#
# sha256sum (Linux CI) with a shasum fallback (macOS dev).
set -eu

if [ "$#" -eq 0 ]; then
  echo "usage: wasm-manifest.sh <wasm> [<wasm> ...]" >&2
  exit 2
fi

for wasm in "$@"; do
  [ -f "$wasm" ] || { echo "wasm-manifest: no such file: $wasm" >&2; exit 1; }
  dir=$(dirname "$wasm")
  base=$(basename "$wasm" .wasm)
  h=$(sha256sum "$wasm" 2>/dev/null | cut -d' ' -f1 || shasum -a 256 "$wasm" | cut -d' ' -f1)
  printf '{"sha256":"%s"}\n' "$h" > "$dir/$base-wasm.json"
  echo "wasm-manifest: $dir/$base-wasm.json sha256 $h"
done
