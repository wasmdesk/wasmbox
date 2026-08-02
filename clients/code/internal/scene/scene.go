// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package scene is the VS Code Dark+-inspired editor scene used by the
// wasmbox "code" external client. It exposes a single SceneState handle
// plus a Render entry point + HandleKey / HandleMouse dispatchers; every
// other symbol is an implementation detail of these three.
//
// The editor is built from the go-widgets/toolkit widget + layout model
// rather than hand-drawn: a Dock shell with a Statusbar docked South over an
// HBox of a ListBox sidebar + a VBox of a tab strip over a TextView editor,
// with the "Connect to Live Server" popup as a Dialog. The TextView owns the
// editing model (lines + cursor + insert/split/backspace + undo/redo +
// selection), its ShowLineNumbers gutter replaces the old hand-drawn one, and
// its Highlighter hook is fed by the package's own Tokenize lexer.
//
// Files in the package:
//
//   - scene.go     : this file, package documentation only.
//   - state.go     : SceneState + widget tree + dispatchers (HandleKey /
//     HandleMouse) + OpenFile / SaveCurrent / Live Server popup.
//   - highlight.go : Tokenize(line) -> []Token (Dark+ palette) + Highlight,
//     the TextView.Highlighter adapter (Token -> TextSpan).
//   - render.go    : Render(state, buf) + the widget tree paint + tabStrip +
//     per-region themes + the Dark+ palette.
//
// Pure Go (no syscall/js, no cgo) -- the wasm entry point lives in
// clients/code/main.go and imports this package via the //go:build js &&
// wasm constraint. Native test targets build the package without the
// build tag so 100% coverage is reachable from any of the 6 architectures
// the repo CI exercises.
package scene
