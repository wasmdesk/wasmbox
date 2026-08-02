# mvvm-counter

A self-contained wasmbox demo of the MVVM data-binding stack: a
[`require "mvvm"`](https://github.com/go-ruby-widgets/mvvm) `Observable` is the
single source of truth, and a
[`require "widgets"`](https://github.com/go-ruby-widgets/widgets) UI is bound to
it — the first consumer wiring the two adapters together end to end.

Unlike the pixel-painting clients (`hello`, `calculator`, …), which are Go
`js/wasm` programs, this demo's UI **and** state live entirely in Ruby. The Go
side only hosts the [rbgo](https://github.com/go-embedded-ruby/ruby) interpreter;
[`counter.rb`](counter.rb) is the whole program.

## What it does

```
Observable(count) ──subscribe/drain──▶ Label "count: N"
        ▲                                    (rebinds on change)
        │ set(count+1)
   "+1" Button click ──dispatch──▶ fires "on_inc"
```

Clicking `+1` fires the button's callback; the Ruby handler bumps the
`Observable`; the `Observable`'s change event rebinds the `Label` text; and
re-rendering produces a different pixel buffer — proving the bound render tracks
the observed state.

## Run it

Directly through rbgo:

```sh
rbgo clients/mvvm-counter/counter.rb
# => OK count=3 label=count: 3
```

Or from Go (the CI acceptance test in [`counter_test.go`](counter_test.go) runs
the embedded script on a fresh rbgo VM and asserts the final bound state):

```sh
go test ./clients/mvvm-counter/
```
