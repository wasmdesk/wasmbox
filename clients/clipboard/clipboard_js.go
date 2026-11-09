// Copyright (c) 2026 The wasmbox authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

//go:build js && wasm

package clipboard

import (
	"syscall/js"

	"github.com/go-widgets/toolkit"
)

// Install makes the page's clipboard, through the SDK client, the toolkit's
// clipboard (toolkit.SetClipboard) and returns it. The caller routes the
// input events of kind "paste" to Deliver. A client without writeClipboard
// (an older sdk.js) keeps copies inside the window.
func Install(client js.Value) *Clipboard {
	var write func(string)
	if client.Get("writeClipboard").Type() == js.TypeFunction {
		write = func(s string) { client.Call("writeClipboard", s) }
	}
	c := New(write)
	toolkit.SetClipboard(c)
	return c
}
