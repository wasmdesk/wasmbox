// SPDX-License-Identifier: BSD-3-Clause
//
// themes.go embeds a handful of GTK theme @define-color palettes (real
// upstream Adwaita + Solarized variants — see themes/*.css for the
// trademark/licence notes) and exposes them to scene.go via
// LoadGTKTheme. The showcase's View menu walks Themes() to build its
// theme-picker submenu, so adding a new theme = drop a .css under
// themes/ and rebuild.

package scene

import (
	"embed"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/wasmdesk/toolkit"
)

//go:embed themes/*.css
var themeFS embed.FS

// ThemeEntry pairs a display label with the parsed Theme. Built once at
// scene.New() time so the View menu can iterate without re-parsing on
// every render.
type ThemeEntry struct {
	Name  string
	Theme *toolkit.Theme
}

// Themes returns every embedded GTK theme parsed into a toolkit Theme,
// in alphabetic file-name order. Always includes the toolkit's built-in
// Default Light + Dark as the first two entries so the user can flip
// back to the un-themed baseline.
func Themes() []ThemeEntry { return themesFromFS(themeFS, "themes") }

// themesFromFS does the real work: walks dir in fsys, parses every
// .css file via LoadGTKTheme. Split out so a test can pass an
// in-memory fs.FS to exercise the malformed / missing-dir / non-css
// branches without polluting the embedded themes/ dir.
func themesFromFS(fsys fs.FS, dir string) []ThemeEntry {
	out := []ThemeEntry{
		{Name: "Default Light", Theme: toolkit.DefaultLight()},
		{Name: "Default Dark", Theme: toolkit.DefaultDark()},
	}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return out
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".css") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		// ReadFile after a successful ReadDir on a stable FS (embed.FS
		// / fstest.MapFS) cannot fail — skip the defensive check.
		data, _ := fs.ReadFile(fsys, path.Join(dir, n))
		th, err := toolkit.LoadGTKTheme(string(data))
		if err != nil {
			continue
		}
		out = append(out, ThemeEntry{Name: prettify(n), Theme: th})
	}
	return out
}

// prettify turns "adwaita-light.css" into "Adwaita Light" — strips the
// .css extension, replaces hyphens with spaces, title-cases each word.
func prettify(filename string) string {
	base := strings.TrimSuffix(filename, ".css")
	parts := strings.Split(base, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
