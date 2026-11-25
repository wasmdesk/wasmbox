// SPDX-License-Identifier: BSD-3-Clause

package audioring

import _ "embed"

// WorkletJS is the AudioWorkletProcessor that consumes a ring: the one
// implementation, served as is by every page (wasmbox's compositor, the
// wasmcontainers playground). Register it with
// audioWorklet.addModule(URL of these bytes); it registers the processor
// "wasmbox-ring", whose processorOptions are {sab, sampleRate, channels,
// capacityFrames}, and "wasmbox-voices" (the mixer described in voices.go),
// whose processorOptions are {ctl, bank, aheadFrames, mixRate}.
//
//go:embed audio-worklet.js
var WorkletJS []byte

// WorkletProcessor is the name the embedded worklet registers.
const WorkletProcessor = "wasmbox-ring"
