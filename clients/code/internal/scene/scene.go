// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene is the VS Code Dark+-inspired editor scene used by the
// wasmbox "code" external client. It exposes a single SceneState handle
// plus a Render entry point + HandleKey / HandleMouse dispatchers; every
// other symbol is an implementation detail of these three.
//
// Files in the package:
//
//   - scene.go     : this file, package documentation only.
//   - state.go     : SceneState + dispatchers (HandleKey / HandleMouse) +
//                    OpenFile / SaveCurrent / Flash / Live Server popup.
//   - buffer.go    : TextBuffer with Insert / Delete / Split / MoveCursor.
//   - highlight.go : Tokenize(line) -> []Token (Dark+ syntactic palette).
//   - render.go    : Render(state, buf) + per-stage paint helpers + palette.
//   - font.go      : 8x8 ASCII bitmap font + Glyph(c) accessor.
//
// Pure Go (no syscall/js, no cgo) -- the wasm entry point lives in
// clients/code/main.go and imports this package via the //go:build js &&
// wasm constraint. Native test targets build the package without the
// build tag so 100% coverage is reachable from any of the 6 architectures
// the repo CI exercises.
package scene
