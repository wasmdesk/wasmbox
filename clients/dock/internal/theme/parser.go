// SPDX-License-Identifier: BSD-3-Clause
//
// Openbox `.themerc` parser. A .themerc is a plain-text file of lines of the
// form `key: value`, with `!` comments and blank lines ignored. Keys are
// dotted paths in the Openbox theme attribute tree (see
// https://openbox.org/help/Themes_fr); values are colour literals (`#RRGGBB`),
// integers (border / padding widths), gradient-type keywords (`flat` /
// `vertical` / `horizontal` / `diagonal` / `crossdiagonal` / `pipecross` /
// `rectangle` / `pyramid` / `raisedbevel` / `sunkenbevel` / `parentrelative`),
// or `family size` font specifiers.
//
// ParseRC reads one .themerc file, returns a fully-populated Theme and a slice
// of non-fatal warnings (unknown keys, malformed values). A malformed colour
// or unknown gradient becomes a warning, not a fatal error — Openbox-style
// themercs in the wild are forgiving, and the toolbar still has a sane fall-
// back (the field stays at its zero value, which the renderer treats as solid
// black). The caller decides whether to surface the warnings.
//
// Mapping table (Openbox key -> Theme field):
//
//   border.color                          Theme.Border.Color
//   border.width                          Theme.Border.Width
//   padding.width                         Theme.Padding.Width
//   padding.height                        Theme.Padding.Height
//
//   window.active.title.bg                Theme.Window.Active.Title.Bg.Gradient
//   window.active.title.bg.color          Theme.Window.Active.Title.Bg.Color
//   window.active.title.bg.colorTo        Theme.Window.Active.Title.Bg.ColorTo
//   window.active.label.text.color        Theme.Window.Active.Title.Label.Color
//   window.active.label.text.font         Theme.Window.Active.Title.Label.Font
//
//   window.inactive.title.bg              Theme.Window.Inactive.Title.Bg.Gradient
//   window.inactive.title.bg.color        Theme.Window.Inactive.Title.Bg.Color
//   window.inactive.title.bg.colorTo      Theme.Window.Inactive.Title.Bg.ColorTo
//   window.inactive.label.text.color      Theme.Window.Inactive.Title.Label.Color
//   window.inactive.label.text.font       Theme.Window.Inactive.Title.Label.Font
//
//   menu.title.bg                         Theme.Menu.Title.Bg.Gradient
//   menu.title.bg.color                   Theme.Menu.Title.Bg.Color
//   menu.title.bg.colorTo                 Theme.Menu.Title.Bg.ColorTo
//   menu.title.text.color                 Theme.Menu.Title.Label.Color
//   menu.title.text.font                  Theme.Menu.Title.Label.Font
//
//   menu.items.bg                         Theme.Menu.Items.Bg.Gradient
//   menu.items.bg.color                   Theme.Menu.Items.Bg.Color
//   menu.items.bg.colorTo                 Theme.Menu.Items.Bg.ColorTo
//   menu.items.text.color                 Theme.Menu.Items.Text.Color
//   menu.items.text.font                  Theme.Menu.Items.Text.Font
//
//   osd.bg                                Theme.Osd.Bg.Gradient
//   osd.bg.color                          Theme.Osd.Bg.Color
//   osd.bg.colorTo                        Theme.Osd.Bg.ColorTo
//   osd.label.text.color                  Theme.Osd.Label.Color
//   osd.label.text.font                   Theme.Osd.Label.Font
//
// Any key outside this set produces a Warning of kind WarnUnknownKey. A
// recognised key with a malformed value produces a Warning of kind
// WarnBadValue.

package theme

import (
	"bufio"
	"embed"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Warning carries a non-fatal parse problem (unknown key, bad colour, bad
// integer, ...). Fmt(w) is human-readable; callers can also inspect Kind +
// Line to decide whether to surface it.
type Warning struct {
	// Line is the 1-indexed line number in the source file.
	Line int
	// Kind is one of the Warn* sentinels below.
	Kind string
	// Key is the dotted Openbox key the warning is about (empty if the line
	// could not even be split on `:`).
	Key string
	// Value is the raw RHS value (for context in messages).
	Value string
	// Detail is a free-form human description.
	Detail string
}

const (
	// WarnUnknownKey marks a dotted-path key we do not map to a Theme field.
	WarnUnknownKey = "unknown-key"
	// WarnBadValue marks a recognised key with a malformed RHS (e.g. a
	// colour literal that is not `#RRGGBB`).
	WarnBadValue = "bad-value"
	// WarnMalformedLine marks a line that has no `:` separator.
	WarnMalformedLine = "malformed-line"
)

// Error makes Warning implement error so callers can join warnings into an
// errors.Join() bundle if they want.
func (w Warning) Error() string {
	if w.Key == "" {
		return fmt.Sprintf("line %d: %s: %s", w.Line, w.Kind, w.Detail)
	}
	return fmt.Sprintf("line %d: %s: %s = %q (%s)", w.Line, w.Kind, w.Key, w.Value, w.Detail)
}

// ParseRC reads an Openbox-style .themerc from r and returns the populated
// Theme + any non-fatal warnings encountered. A nil reader yields an empty
// Theme and no warnings. ParseRC never returns an error — every problem
// is recorded as a Warning so the caller can choose between strict and
// lenient handling.
func ParseRC(r io.Reader) (Theme, []Warning) {
	var t Theme
	var warns []Warning
	if r == nil {
		return t, warns
	}
	sc := bufio.NewScanner(r)
	// Allow long lines (font specifiers + comments) without surprises.
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Text()
		trim := strings.TrimSpace(raw)
		// Skip blanks + comments.
		if trim == "" || strings.HasPrefix(trim, "!") || strings.HasPrefix(trim, "#") {
			continue
		}
		idx := strings.Index(trim, ":")
		if idx < 0 {
			warns = append(warns, Warning{Line: line, Kind: WarnMalformedLine, Detail: "no ':' separator"})
			continue
		}
		key := strings.TrimSpace(trim[:idx])
		val := strings.TrimSpace(trim[idx+1:])
		// Strip an inline `! comment` tail (Openbox accepts both whole-line
		// and trailing comments).
		if c := strings.Index(val, " !"); c >= 0 {
			val = strings.TrimSpace(val[:c])
		}
		applyKey(&t, key, val, line, &warns)
	}
	return t, warns
}

// MustParseRC is a convenience for tests + embedded literals: parses the
// given source and panics on the first warning (so a typo in a baked-in
// theme file is a programming error, not a silent fallback).
func MustParseRC(src string) Theme {
	t, ws := ParseRC(strings.NewReader(src))
	if len(ws) > 0 {
		panic("theme: MustParseRC: " + ws[0].Error())
	}
	return t
}

// applyKey routes a single recognised key into the right Theme field. An
// unknown key appends a WarnUnknownKey; a malformed value appends a
// WarnBadValue. Recognised keys always set the field — even on bad value the
// other keys parse fine.
func applyKey(t *Theme, key, val string, line int, warns *[]Warning) {
	switch key {
	case "border.color":
		setColor(&t.Border.Color, key, val, line, warns)
	case "border.width":
		setInt(&t.Border.Width, key, val, line, warns)
	case "padding.width":
		setInt(&t.Padding.Width, key, val, line, warns)
	case "padding.height":
		setInt(&t.Padding.Height, key, val, line, warns)

	// window.active.*
	case "window.active.title.bg":
		setGradient(&t.Window.Active.Title.Bg.Gradient, key, val, line, warns)
	case "window.active.title.bg.color":
		setColor(&t.Window.Active.Title.Bg.Color, key, val, line, warns)
	case "window.active.title.bg.colorTo":
		setColor(&t.Window.Active.Title.Bg.ColorTo, key, val, line, warns)
	case "window.active.label.text.color":
		setColor(&t.Window.Active.Title.Label.Color, key, val, line, warns)
	case "window.active.label.text.font":
		setFont(&t.Window.Active.Title.Label.Font, key, val, line, warns)

	// window.inactive.*
	case "window.inactive.title.bg":
		setGradient(&t.Window.Inactive.Title.Bg.Gradient, key, val, line, warns)
	case "window.inactive.title.bg.color":
		setColor(&t.Window.Inactive.Title.Bg.Color, key, val, line, warns)
	case "window.inactive.title.bg.colorTo":
		setColor(&t.Window.Inactive.Title.Bg.ColorTo, key, val, line, warns)
	case "window.inactive.label.text.color":
		setColor(&t.Window.Inactive.Title.Label.Color, key, val, line, warns)
	case "window.inactive.label.text.font":
		setFont(&t.Window.Inactive.Title.Label.Font, key, val, line, warns)

	// menu.title.*
	case "menu.title.bg":
		setGradient(&t.Menu.Title.Bg.Gradient, key, val, line, warns)
	case "menu.title.bg.color":
		setColor(&t.Menu.Title.Bg.Color, key, val, line, warns)
	case "menu.title.bg.colorTo":
		setColor(&t.Menu.Title.Bg.ColorTo, key, val, line, warns)
	case "menu.title.text.color":
		setColor(&t.Menu.Title.Label.Color, key, val, line, warns)
	case "menu.title.text.font":
		setFont(&t.Menu.Title.Label.Font, key, val, line, warns)

	// menu.items.*
	case "menu.items.bg":
		setGradient(&t.Menu.Items.Bg.Gradient, key, val, line, warns)
	case "menu.items.bg.color":
		setColor(&t.Menu.Items.Bg.Color, key, val, line, warns)
	case "menu.items.bg.colorTo":
		setColor(&t.Menu.Items.Bg.ColorTo, key, val, line, warns)
	case "menu.items.text.color":
		setColor(&t.Menu.Items.Text.Color, key, val, line, warns)
	case "menu.items.text.font":
		setFont(&t.Menu.Items.Text.Font, key, val, line, warns)

	// osd.*
	case "osd.bg":
		setGradient(&t.Osd.Bg.Gradient, key, val, line, warns)
	case "osd.bg.color":
		setColor(&t.Osd.Bg.Color, key, val, line, warns)
	case "osd.bg.colorTo":
		setColor(&t.Osd.Bg.ColorTo, key, val, line, warns)
	case "osd.label.text.color":
		setColor(&t.Osd.Label.Color, key, val, line, warns)
	case "osd.label.text.font":
		setFont(&t.Osd.Label.Font, key, val, line, warns)

	default:
		*warns = append(*warns, Warning{
			Line: line, Kind: WarnUnknownKey, Key: key, Value: val,
			Detail: "no mapping into Theme",
		})
	}
}

// setColor parses `#RRGGBB` and writes into *dst on success. A malformed
// literal appends a WarnBadValue; *dst is left at its previous value (which
// may be the Theme zero value).
func setColor(dst *Color, key, val string, line int, warns *[]Warning) {
	c, ok := parseHexColor(val)
	if !ok {
		*warns = append(*warns, Warning{
			Line: line, Kind: WarnBadValue, Key: key, Value: val,
			Detail: "expected #RRGGBB colour literal",
		})
		return
	}
	*dst = c
}

// parseHexColor validates + decodes a `#RRGGBB` literal. Returns the colour
// and true on success.
func parseHexColor(s string) (Color, bool) {
	if len(s) != 7 || s[0] != '#' {
		return Color{}, false
	}
	var c Color
	for i := 0; i < 3; i++ {
		hi, ok1 := hexNibble(s[1+2*i])
		lo, ok2 := hexNibble(s[2+2*i])
		if !ok1 || !ok2 {
			return Color{}, false
		}
		c[i] = hi<<4 | lo
	}
	return c, true
}

// setInt parses a base-10 integer and writes into *dst on success. A
// malformed value appends a WarnBadValue.
func setInt(dst *int, key, val string, line int, warns *[]Warning) {
	n, err := strconv.Atoi(val)
	if err != nil {
		*warns = append(*warns, Warning{
			Line: line, Kind: WarnBadValue, Key: key, Value: val,
			Detail: "expected integer",
		})
		return
	}
	*dst = n
}

// setGradient parses the bg gradient strategy and writes into *dst. Openbox
// `*.bg` lines often carry multiple tokens (e.g. `vertical flat`); we honour
// the FIRST gradient-type token we recognise, in left-to-right order. An
// unrecognised value appends a WarnBadValue and leaves *dst at GradientFlat.
func setGradient(dst *GradientType, key, val string, line int, warns *[]Warning) {
	for _, tok := range strings.Fields(val) {
		switch strings.ToLower(tok) {
		case "flat":
			*dst = GradientFlat
			return
		case "vertical":
			*dst = GradientVertical
			return
		case "horizontal":
			*dst = GradientHorizontal
			return
		case "diagonal":
			*dst = GradientDiagonal
			return
		case "crossdiagonal":
			*dst = GradientCrossDiagonal
			return
		case "pipecross":
			*dst = GradientPipeCross
			return
		case "rectangle":
			*dst = GradientRectangle
			return
		case "pyramid":
			*dst = GradientPyramid
			return
		case "raisedbevel":
			*dst = GradientRaisedBevel
			return
		case "sunkenbevel":
			*dst = GradientSunkenBevel
			return
		case "parentrelative":
			*dst = GradientParentRelative
			return
		}
	}
	*warns = append(*warns, Warning{
		Line: line, Kind: WarnBadValue, Key: key, Value: val,
		Detail: "no recognised gradient keyword",
	})
}

// setFont parses an Openbox `*.text.font` line. The Openbox grammar is
// `<family> <size>` (space-separated), with the size as an optional integer
// at the end; we accept either `family` alone or `family size`. A malformed
// size appends a WarnBadValue but the family still applies.
func setFont(dst *Font, key, val string, line int, warns *[]Warning) {
	toks := strings.Fields(val)
	if len(toks) == 0 {
		*warns = append(*warns, Warning{
			Line: line, Kind: WarnBadValue, Key: key, Value: val,
			Detail: "empty font specifier",
		})
		return
	}
	// Last token is the size if it parses as an integer; otherwise the whole
	// string is the family.
	if len(toks) >= 2 {
		if sz, err := strconv.Atoi(toks[len(toks)-1]); err == nil {
			dst.Face = strings.Join(toks[:len(toks)-1], " ")
			dst.Size = sz
			return
		}
	}
	dst.Face = strings.Join(toks, " ")
}

// ---------------------------------------------------------------------------
// Built-in themes (//go:embed)
// ---------------------------------------------------------------------------

//go:embed themes/*.themerc
var builtinFS embed.FS

// builtinNames is the canonical user-visible order of the bundled themes
// (matches the root-menu order). Keep these in sync with the files in
// themes/.
var builtinNames = []string{
	"Fluxbox Light",
	"Fluxbox Dark",
	"GNOME Adwaita",
}

// builtinFiles maps each user-visible name to the embedded .themerc filename.
var builtinFiles = map[string]string{
	"Fluxbox Light": "themes/fluxbox-light.themerc",
	"Fluxbox Dark":  "themes/fluxbox-dark.themerc",
	"GNOME Adwaita": "themes/gnome-adwaita.themerc",
}

// Builtin returns the bundled themes keyed by user-visible name. Parsed once
// at init; subsequent calls return the same map. Any warning in a bundled
// file panics — those files ship in the binary and a malformed bake is a
// programming error.
func Builtin() map[string]Theme {
	return builtinMap()
}

// BuiltinNames returns the user-visible names in the canonical menu order.
func BuiltinNames() []string {
	out := make([]string, len(builtinNames))
	copy(out, builtinNames)
	return out
}

// BuiltinSource returns the raw .themerc source of a bundled theme by name,
// or ("", false) if the name is not bundled. Useful for round-trip tests
// and for the wire format (the compositor ships the SOURCE, not the parsed
// struct, so a future client can carry its own parser version).
func BuiltinSource(name string) (string, bool) {
	rel, ok := builtinFiles[name]
	if !ok {
		return "", false
	}
	// builtinFS is a //go:embed; a read failure here is impossible at runtime,
	// so a missing file is a programming error caught at first use by Builtin().
	b, _ := builtinFS.ReadFile(rel)
	return string(b), true
}

var cachedBuiltin map[string]Theme

func builtinMap() map[string]Theme {
	if cachedBuiltin != nil {
		return cachedBuiltin
	}
	out := make(map[string]Theme, len(builtinFiles))
	for name := range builtinFiles {
		// BuiltinSource cannot fail for a name in builtinFiles — the embedded
		// fs and the map are baked from the same file list at compile time.
		src, _ := BuiltinSource(name)
		out[name] = MustParseRC(src)
	}
	cachedBuiltin = out
	return out
}
