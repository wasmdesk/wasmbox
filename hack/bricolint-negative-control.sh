#!/usr/bin/env bash
# Copyright (c) 2026 the wasmdesk/wasmbox authors. All rights reserved.
# Use of this source code is governed by a BSD-3-Clause license that can be
# found in the LICENSE file at the root of this repository.
#
# bricolint-negative-control.sh — prove the hand-drawn-UI guard actually bites.
#
# A guard that never fails is worthless. This script asserts the full cycle on a
# REAL painter-using Draw method in a CLIENT (not a compositor framebuffer leaf):
#
#   1. clean tree                         -> go vet exits 0
#   2. inject a raw p.FillRect(...) call  -> go vet exits non-zero (guard bites)
#   3. remove the injection               -> go vet exits 0 again
#
# The injection is placed with awk into the body of a specific, real Draw method
# whose receiver `p` statically resolves to go-widgets/painter, so bricolint must
# flag it. The original file is restored by an EXIT trap even if any step fails.
#
# The vettool binary path comes from $BRICOLINT (the CI job exports it); it
# falls back to $(go env GOPATH)/bin/bricolint for local runs.
set -euo pipefail

export GOWORK=off

BRICOLINT="${BRICOLINT:-$(go env GOPATH)/bin/bricolint}"
if [[ ! -x "$BRICOLINT" ]]; then
	echo "FAIL: bricolint binary not found or not executable at: $BRICOLINT" >&2
	echo "      install it with: go install github.com/go-widgets/bricolint/cmd/bricolint@v0.1.0" >&2
	exit 2
fi

# Real anchor: the settings client's page.Draw, whose receiver p is a
# go-widgets/painter.Painter. Injecting a drawing primitive here is exactly the
# violation the guard exists to catch.
ANCHOR_FILE="clients/settings/internal/scene/scene.go"
ANCHOR_SIG='func (pg *page) Draw(p painter.Painter, th *toolkit.Theme) {'
INJECT='	p.FillRect(painter.Rect{}, painter.RGBA{}) // bricolint-negative-control INJECTED'

if [[ ! -f "$ANCHOR_FILE" ]]; then
	echo "FAIL: anchor file missing: $ANCHOR_FILE (run from the module root)" >&2
	exit 2
fi
if ! grep -qF "$ANCHOR_SIG" "$ANCHOR_FILE"; then
	echo "FAIL: anchor signature not found in $ANCHOR_FILE:" >&2
	echo "      $ANCHOR_SIG" >&2
	echo "      the Draw method may have been renamed — update ANCHOR_SIG." >&2
	exit 2
fi

# Restore the anchor file ONLY once a real backup has been taken (RESTORE=1) and
# it is non-empty, so a failure before the cp below cannot make the trap copy an
# empty temp over $ANCHOR_FILE and wipe it.
BACKUP="$(mktemp)"
RESTORE=0
restore() { [ "$RESTORE" = 1 ] && [ -s "$BACKUP" ] && cp "$BACKUP" "$ANCHOR_FILE"; rm -f "$BACKUP"; return 0; }
trap restore EXIT
cp "$ANCHOR_FILE" "$BACKUP"; RESTORE=1

vet() { go vet -vettool="$BRICOLINT" ./...; }

echo "== step 1: clean tree must pass =="
if ! vet; then
	echo "FAIL: bricolint flagged the CLEAN tree (expected exit 0)." >&2
	exit 1
fi
echo "OK: clean tree passes."

echo "== step 2: inject a raw p.FillRect into ${ANCHOR_FILE} — guard must bite =="
awk -v sig="$ANCHOR_SIG" -v inj="$INJECT" '
	{ print }
	index($0, sig) { print inj }
' "$BACKUP" > "$ANCHOR_FILE"

if vet; then
	echo "FAIL: bricolint did NOT flag an injected p.FillRect (guard is asleep)." >&2
	exit 1
fi
echo "OK: guard bit the injected drawing primitive (exit non-zero)."

echo "== step 3: remove the injection — must pass again =="
restore
trap - EXIT
if ! vet; then
	echo "FAIL: bricolint still flags after removing the injection (state not restored)." >&2
	exit 1
fi
echo "OK: clean again."

echo "PASS: bricolint negative control — the guard bites and the tree is clean."
