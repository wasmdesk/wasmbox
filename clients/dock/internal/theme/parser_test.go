// SPDX-License-Identifier: BSD-3-Clause

package theme

import (
	"strings"
	"testing"
)

// TestParseRCFullCoverage runs a single themerc that touches every recognised
// key so the per-key apply branch + every setter (color/int/gradient/font) all
// fire. It is the strongest "round-trip" anchor.
func TestParseRCFullCoverage(t *testing.T) {
	src := `! full coverage
# alternate comment marker
border.color:                   #112233
border.width:                   2
padding.width:                  3
padding.height:                 4

window.active.title.bg:           vertical
window.active.title.bg.color:     #aabbcc
window.active.title.bg.colorTo:   #ddeeff
window.active.label.text.color:   #102030
window.active.label.text.font:    sans 11

window.inactive.title.bg:         horizontal
window.inactive.title.bg.color:   #112244
window.inactive.title.bg.colorTo: #334455
window.inactive.label.text.color: #506070
window.inactive.label.text.font:  serif 8

menu.title.bg:                    flat
menu.title.bg.color:              #804020
menu.title.bg.colorTo:            #905030
menu.title.text.color:            #fafafa
menu.title.text.font:             mono 10

menu.items.bg:                    diagonal
menu.items.bg.color:              #010203
menu.items.bg.colorTo:            #040506
menu.items.text.color:            #ffeedd
menu.items.text.font:             sans 9

osd.bg:                           crossdiagonal
osd.bg.color:                     #070809
osd.bg.colorTo:                   #0a0b0c
osd.label.text.color:             #fefefe
osd.label.text.font:              sans 12
`
	th, ws := ParseRC(strings.NewReader(src))
	if len(ws) != 0 {
		t.Fatalf("unexpected warnings on clean source: %v", ws)
	}
	// border + padding
	if th.Border.Color != (Color{0x11, 0x22, 0x33}) || th.Border.Width != 2 {
		t.Fatalf("border = %+v", th.Border)
	}
	if th.Padding.Width != 3 || th.Padding.Height != 4 {
		t.Fatalf("padding = %+v", th.Padding)
	}
	// window.active
	if th.Window.Active.Title.Bg.Gradient != GradientVertical {
		t.Fatalf("active.title.bg = %v", th.Window.Active.Title.Bg.Gradient)
	}
	if th.Window.Active.Title.Bg.Color != (Color{0xAA, 0xBB, 0xCC}) {
		t.Fatalf("active.title.bg.color = %v", th.Window.Active.Title.Bg.Color)
	}
	if th.Window.Active.Title.Bg.ColorTo != (Color{0xDD, 0xEE, 0xFF}) {
		t.Fatalf("active.title.bg.colorTo = %v", th.Window.Active.Title.Bg.ColorTo)
	}
	if th.Window.Active.Title.Label.Color != (Color{0x10, 0x20, 0x30}) {
		t.Fatalf("active.label.color = %v", th.Window.Active.Title.Label.Color)
	}
	if th.Window.Active.Title.Label.Font.Face != "sans" || th.Window.Active.Title.Label.Font.Size != 11 {
		t.Fatalf("active.label.font = %+v", th.Window.Active.Title.Label.Font)
	}
	// window.inactive
	if th.Window.Inactive.Title.Bg.Gradient != GradientHorizontal {
		t.Fatalf("inactive.title.bg = %v", th.Window.Inactive.Title.Bg.Gradient)
	}
	if th.Window.Inactive.Title.Label.Font.Face != "serif" || th.Window.Inactive.Title.Label.Font.Size != 8 {
		t.Fatalf("inactive.label.font = %+v", th.Window.Inactive.Title.Label.Font)
	}
	// menu.title
	if th.Menu.Title.Bg.Gradient != GradientFlat {
		t.Fatalf("menu.title.bg gradient = %v", th.Menu.Title.Bg.Gradient)
	}
	if th.Menu.Title.Bg.ColorTo != (Color{0x90, 0x50, 0x30}) {
		t.Fatalf("menu.title.bg.colorTo = %v", th.Menu.Title.Bg.ColorTo)
	}
	if th.Menu.Title.Label.Font.Face != "mono" {
		t.Fatalf("menu.title.font.face = %q", th.Menu.Title.Label.Font.Face)
	}
	// menu.items
	if th.Menu.Items.Bg.Gradient != GradientDiagonal {
		t.Fatalf("menu.items.bg = %v", th.Menu.Items.Bg.Gradient)
	}
	if th.Menu.Items.Bg.Color != (Color{0x01, 0x02, 0x03}) {
		t.Fatalf("menu.items.bg.color = %v", th.Menu.Items.Bg.Color)
	}
	if th.Menu.Items.Bg.ColorTo != (Color{0x04, 0x05, 0x06}) {
		t.Fatalf("menu.items.bg.colorTo = %v", th.Menu.Items.Bg.ColorTo)
	}
	if th.Menu.Items.Text.Color != (Color{0xFF, 0xEE, 0xDD}) {
		t.Fatalf("menu.items.text.color = %v", th.Menu.Items.Text.Color)
	}
	if th.Menu.Items.Text.Font.Face != "sans" || th.Menu.Items.Text.Font.Size != 9 {
		t.Fatalf("menu.items.font = %+v", th.Menu.Items.Text.Font)
	}
	// osd
	if th.Osd.Bg.Gradient != GradientCrossDiagonal {
		t.Fatalf("osd.bg = %v", th.Osd.Bg.Gradient)
	}
	if th.Osd.Bg.Color != (Color{0x07, 0x08, 0x09}) || th.Osd.Bg.ColorTo != (Color{0x0A, 0x0B, 0x0C}) {
		t.Fatalf("osd.bg colours = %+v", th.Osd.Bg)
	}
	if th.Osd.Label.Color != (Color{0xFE, 0xFE, 0xFE}) {
		t.Fatalf("osd.label.color = %v", th.Osd.Label.Color)
	}
	if th.Osd.Label.Font.Face != "sans" || th.Osd.Label.Font.Size != 12 {
		t.Fatalf("osd.label.font = %+v", th.Osd.Label.Font)
	}
}

// TestParseRCEveryGradientKeyword exercises the gradient setter's full
// keyword table.
func TestParseRCEveryGradientKeyword(t *testing.T) {
	cases := []struct {
		val  string
		want GradientType
	}{
		{"flat", GradientFlat},
		{"vertical", GradientVertical},
		{"horizontal", GradientHorizontal},
		{"diagonal", GradientDiagonal},
		{"crossdiagonal", GradientCrossDiagonal},
		{"pipecross", GradientPipeCross},
		{"rectangle", GradientRectangle},
		{"pyramid", GradientPyramid},
		{"raisedbevel", GradientRaisedBevel},
		{"sunkenbevel", GradientSunkenBevel},
		{"parentrelative", GradientParentRelative},
		// Compound value: first recognised token wins.
		{"some-noise vertical flat", GradientVertical},
		// Case-insensitive.
		{"VERTICAL", GradientVertical},
	}
	for _, c := range cases {
		src := "window.active.title.bg: " + c.val + "\n"
		th, ws := ParseRC(strings.NewReader(src))
		if len(ws) != 0 {
			t.Fatalf("warnings for %q: %v", c.val, ws)
		}
		if th.Window.Active.Title.Bg.Gradient != c.want {
			t.Fatalf("gradient %q -> %v, want %v", c.val, th.Window.Active.Title.Bg.Gradient, c.want)
		}
	}
}

// TestParseRCNilReader returns an empty theme with no warnings.
func TestParseRCNilReader(t *testing.T) {
	th, ws := ParseRC(nil)
	if len(ws) != 0 {
		t.Fatalf("warnings on nil reader: %v", ws)
	}
	if th != (Theme{}) {
		t.Fatalf("nil reader produced non-zero theme: %+v", th)
	}
}

// TestParseRCCommentsAndBlanks ignore blank lines, !-comments and #-comments.
func TestParseRCCommentsAndBlanks(t *testing.T) {
	src := `
! a bang comment
   ! indented bang comment
# a hash comment

border.color: #abcdef

! trailing blank lines below

`
	th, ws := ParseRC(strings.NewReader(src))
	if len(ws) != 0 {
		t.Fatalf("unexpected warnings: %v", ws)
	}
	if th.Border.Color != (Color{0xAB, 0xCD, 0xEF}) {
		t.Fatalf("border.color = %v", th.Border.Color)
	}
}

// TestParseRCInlineComment strips a trailing ` !comment`.
func TestParseRCInlineComment(t *testing.T) {
	src := "border.width: 5 ! pixels\n"
	th, ws := ParseRC(strings.NewReader(src))
	if len(ws) != 0 {
		t.Fatalf("unexpected warnings: %v", ws)
	}
	if th.Border.Width != 5 {
		t.Fatalf("border.width = %d", th.Border.Width)
	}
}

// TestParseRCUnknownKey produces a warning + leaves the rest intact.
func TestParseRCUnknownKey(t *testing.T) {
	src := "rootCommand: xsetroot\nborder.color: #010203\n"
	th, ws := ParseRC(strings.NewReader(src))
	if len(ws) != 1 || ws[0].Kind != WarnUnknownKey {
		t.Fatalf("warnings = %v", ws)
	}
	if ws[0].Key != "rootCommand" {
		t.Fatalf("warn key = %q", ws[0].Key)
	}
	if th.Border.Color != (Color{0x01, 0x02, 0x03}) {
		t.Fatalf("border.color = %v", th.Border.Color)
	}
}

// TestParseRCBadValueColor produces a bad-value warning but does not mutate
// dst (it stays at the zero value).
func TestParseRCBadValueColor(t *testing.T) {
	src := "border.color: not-a-colour\n"
	th, ws := ParseRC(strings.NewReader(src))
	if len(ws) != 1 || ws[0].Kind != WarnBadValue {
		t.Fatalf("warnings = %v", ws)
	}
	if th.Border.Color != (Color{}) {
		t.Fatalf("border.color = %v, expected zero", th.Border.Color)
	}
}

// TestParseRCBadValueInt rejects a non-integer width.
func TestParseRCBadValueInt(t *testing.T) {
	src := "border.width: nope\npadding.width: also-bad\npadding.height: 1.5\n"
	_, ws := ParseRC(strings.NewReader(src))
	if len(ws) != 3 {
		t.Fatalf("warnings = %v", ws)
	}
	for _, w := range ws {
		if w.Kind != WarnBadValue {
			t.Fatalf("warn kind %q, want bad-value", w.Kind)
		}
	}
}

// TestParseRCBadGradient appends a bad-value warning.
func TestParseRCBadGradient(t *testing.T) {
	src := "window.active.title.bg: nonsense gibberish\n"
	_, ws := ParseRC(strings.NewReader(src))
	if len(ws) != 1 || ws[0].Kind != WarnBadValue {
		t.Fatalf("warnings = %v", ws)
	}
}

// TestParseRCMalformedLine: a line with no colon is reported.
func TestParseRCMalformedLine(t *testing.T) {
	src := "border.color #112233\n"
	_, ws := ParseRC(strings.NewReader(src))
	if len(ws) != 1 || ws[0].Kind != WarnMalformedLine {
		t.Fatalf("warnings = %v", ws)
	}
}

// TestParseRCEmptyFont: an all-whitespace font value warns.
func TestParseRCEmptyFont(t *testing.T) {
	src := "window.active.label.text.font:    \n"
	_, ws := ParseRC(strings.NewReader(src))
	if len(ws) != 1 || ws[0].Kind != WarnBadValue {
		t.Fatalf("warnings = %v", ws)
	}
}

// TestParseRCFontFamilyOnly: a single token is treated as the family.
func TestParseRCFontFamilyOnly(t *testing.T) {
	src := "window.active.label.text.font: monospace\n"
	th, ws := ParseRC(strings.NewReader(src))
	if len(ws) != 0 {
		t.Fatalf("warnings = %v", ws)
	}
	if th.Window.Active.Title.Label.Font.Face != "monospace" {
		t.Fatalf("font face = %q", th.Window.Active.Title.Label.Font.Face)
	}
	if th.Window.Active.Title.Label.Font.Size != 0 {
		t.Fatalf("font size = %d, want 0", th.Window.Active.Title.Label.Font.Size)
	}
}

// TestParseRCFontMultiwordFamily: family with a space + size at end parses.
func TestParseRCFontMultiwordFamily(t *testing.T) {
	src := "window.active.label.text.font: DejaVu Sans 10\n"
	th, _ := ParseRC(strings.NewReader(src))
	if th.Window.Active.Title.Label.Font.Face != "DejaVu Sans" {
		t.Fatalf("font face = %q", th.Window.Active.Title.Label.Font.Face)
	}
	if th.Window.Active.Title.Label.Font.Size != 10 {
		t.Fatalf("font size = %d", th.Window.Active.Title.Label.Font.Size)
	}
}

// TestParseRCFontTrailingNonInt: last token not an int -> whole string is
// the family.
func TestParseRCFontTrailingNonInt(t *testing.T) {
	src := "window.active.label.text.font: DejaVu Sans Bold\n"
	th, _ := ParseRC(strings.NewReader(src))
	if th.Window.Active.Title.Label.Font.Face != "DejaVu Sans Bold" {
		t.Fatalf("font face = %q", th.Window.Active.Title.Label.Font.Face)
	}
	if th.Window.Active.Title.Label.Font.Size != 0 {
		t.Fatalf("font size = %d", th.Window.Active.Title.Label.Font.Size)
	}
}

// TestParseHexColorRejects rejects every malformed shape.
func TestParseHexColorRejects(t *testing.T) {
	bad := []string{"", "#abc", "abc123", "#GGGGGG", "#12345Z", "#12-456", "abcdef0"}
	for _, b := range bad {
		if _, ok := parseHexColor(b); ok {
			t.Fatalf("parseHexColor(%q) accepted", b)
		}
	}
}

// TestMustParseRCPanics on a malformed source.
func TestMustParseRCPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("MustParseRC did not panic on bad source")
		}
	}()
	MustParseRC("border.color: bogus\n")
}

// TestMustParseRCRoundTripsFluxboxLight: MustParseRC on the bundled fluxbox-
// light theme returns the same struct as DefaultFluxboxLight().
func TestMustParseRCRoundTripsFluxboxLight(t *testing.T) {
	src, ok := BuiltinSource("Fluxbox Light")
	if !ok {
		t.Fatalf("Fluxbox Light not bundled")
	}
	th := MustParseRC(src)
	def := DefaultFluxboxLight()
	if th != def {
		t.Fatalf("fluxbox-light themerc != DefaultFluxboxLight():\n  parsed = %+v\n  hand   = %+v", th, def)
	}
}

// TestBuiltinReturnsAllNames: every name in BuiltinNames must be present in
// Builtin().
func TestBuiltinReturnsAllNames(t *testing.T) {
	all := Builtin()
	if len(all) != 3 {
		t.Fatalf("Builtin has %d entries, want 3", len(all))
	}
	for _, n := range BuiltinNames() {
		if _, ok := all[n]; !ok {
			t.Fatalf("Builtin missing %q", n)
		}
	}
	// Names slice is a copy — mutation does not poison subsequent calls.
	names := BuiltinNames()
	names[0] = "MUTATED"
	if BuiltinNames()[0] == "MUTATED" {
		t.Fatalf("BuiltinNames returned a live slice")
	}
}

// TestBuiltinCacheStable: Builtin() returns the same map across calls.
func TestBuiltinCacheStable(t *testing.T) {
	a := Builtin()
	b := Builtin()
	if len(a) != len(b) {
		t.Fatalf("builtin maps differ in size")
	}
	for k, v := range a {
		if b[k] != v {
			t.Fatalf("builtin[%q] differs across calls", k)
		}
	}
}

// TestBuiltinSourceUnknown returns false on an unknown name.
func TestBuiltinSourceUnknown(t *testing.T) {
	if _, ok := BuiltinSource("nope"); ok {
		t.Fatalf("BuiltinSource(\"nope\") returned ok=true")
	}
}

// TestThemesDarkVsLightDiffer: the dark theme's window background is
// noticeably different from light.
func TestThemesDarkVsLightDiffer(t *testing.T) {
	all := Builtin()
	light := all["Fluxbox Light"]
	dark := all["Fluxbox Dark"]
	adw := all["GNOME Adwaita"]
	if light.Window.Active.Title.Bg.Color == dark.Window.Active.Title.Bg.Color {
		t.Fatalf("light + dark share an active title bg.color")
	}
	if light.Window.Active.Title.Bg.Color == adw.Window.Active.Title.Bg.Color {
		t.Fatalf("light + adwaita share an active title bg.color")
	}
	if dark.Window.Active.Title.Bg.Color == adw.Window.Active.Title.Bg.Color {
		t.Fatalf("dark + adwaita share an active title bg.color")
	}
}

// TestWarningErrorIncludesContext: Warning.Error() carries line + key + value.
func TestWarningErrorIncludesContext(t *testing.T) {
	w := Warning{Line: 7, Kind: WarnBadValue, Key: "border.width", Value: "bogus", Detail: "expected integer"}
	s := w.Error()
	if !strings.Contains(s, "line 7") {
		t.Fatalf("Warning.Error missing line number: %q", s)
	}
	if !strings.Contains(s, "border.width") || !strings.Contains(s, "bogus") {
		t.Fatalf("Warning.Error missing key/value: %q", s)
	}
	// Empty-key Warning prints the malformed-line shape.
	w2 := Warning{Line: 3, Kind: WarnMalformedLine, Detail: "no separator"}
	s2 := w2.Error()
	if !strings.Contains(s2, "line 3") || !strings.Contains(s2, "no separator") {
		t.Fatalf("Warning.Error (no key) = %q", s2)
	}
}

// TestParseRCColorTo handles every *.bg.colorTo Setter.
func TestParseRCColorTo(t *testing.T) {
	src := `
window.inactive.title.bg.colorTo: #112233
menu.title.bg.colorTo:            #445566
menu.items.bg.colorTo:            #778899
osd.bg.colorTo:                   #aabbcc
`
	th, ws := ParseRC(strings.NewReader(src))
	if len(ws) != 0 {
		t.Fatalf("warnings = %v", ws)
	}
	if th.Window.Inactive.Title.Bg.ColorTo != (Color{0x11, 0x22, 0x33}) {
		t.Fatalf("inactive title colorTo = %v", th.Window.Inactive.Title.Bg.ColorTo)
	}
	if th.Menu.Title.Bg.ColorTo != (Color{0x44, 0x55, 0x66}) {
		t.Fatalf("menu.title colorTo = %v", th.Menu.Title.Bg.ColorTo)
	}
	if th.Menu.Items.Bg.ColorTo != (Color{0x77, 0x88, 0x99}) {
		t.Fatalf("menu.items colorTo = %v", th.Menu.Items.Bg.ColorTo)
	}
	if th.Osd.Bg.ColorTo != (Color{0xAA, 0xBB, 0xCC}) {
		t.Fatalf("osd colorTo = %v", th.Osd.Bg.ColorTo)
	}
}
