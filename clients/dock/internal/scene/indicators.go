// SPDX-License-Identifier: BSD-3-Clause

package scene

import "strings"

// Running / active indicators + attention badges.
//
// A launcher whose app has at least one open window carries a small "running"
// dot centred under its glyph (the macOS/Unity dock convention) — wired to the
// AppDockItem.Running flag; the launcher of the currently-focused window is
// additionally flagged Active (BevelDockStyle draws it pressed-in). An app can
// also request an attention badge — a count drawn in the launcher's top-right
// corner via the AppDock's Badge overlay — through SetBadge; this is the
// placeholder API a client uses until a first-class "badge" dock wire message
// lands (the dock would set it from that message's payload exactly the way
// SetBadge does). The dot / badge pixels themselves are painted by the AppDock;
// this file only computes which launchers carry them.

// appIndexForWindow maps an open window to the launcher index it belongs to, or
// -1 if none matches. The match is by the window's explicit App id when the
// compositor supplies one (Window.App, forward-compatible — the field rides the
// windows_changed payload the moment the compositor starts sending it), else by
// a case-insensitive match of the window Title against a launcher's Label or Id
// (the dom-app launch descriptors title their windows after the launcher, e.g.
// "VS Code", "Terminal", so the fallback lights the right dot today).
func (s *State) appIndexForWindow(w Window) int {
	if app := strings.TrimSpace(w.App); app != "" {
		for i := range s.Apps {
			if strings.EqualFold(s.Apps[i].Id, app) {
				return i
			}
		}
		return -1
	}
	title := strings.TrimSpace(w.Title)
	if title == "" {
		return -1
	}
	for i := range s.Apps {
		if strings.EqualFold(s.Apps[i].Label, title) || strings.EqualFold(s.Apps[i].Id, title) {
			return i
		}
	}
	return -1
}

// launcherRunning reports, per launcher index, whether at least one open (or
// folded) window maps to it — i.e. whether it should carry a running dot.
func (s *State) launcherRunning() []bool {
	out := make([]bool, len(s.Apps))
	for _, w := range s.Windows {
		if i := s.appIndexForWindow(w); i >= 0 {
			out[i] = true
		}
	}
	return out
}

// focusedLauncher returns the launcher index of the currently-focused window,
// or -1 if no focused window maps to a launcher. Its launcher gets the brighter
// active underline on top of the running dot.
func (s *State) focusedLauncher() int {
	for _, w := range s.Windows {
		if w.Focused {
			return s.appIndexForWindow(w)
		}
	}
	return -1
}

// SetBadge sets (count > 0) or clears (count <= 0) the attention badge on the
// launcher whose Id is app. The badge shows the count, capped at "99+" so the
// pill stays compact. Unknown app ids are ignored. Placeholder attention API:
// a client requests a badge through the dock, which calls this and repaints.
func (s *State) SetBadge(app string, count int) {
	if s.badges == nil {
		s.badges = map[string]int{}
	}
	if count <= 0 {
		delete(s.badges, app)
		return
	}
	s.badges[app] = count
}

// BadgeCount returns the attention-badge count for the launcher app id (0 when
// none). Exposed so the wasm shell + tests can read back what SetBadge stored.
func (s *State) BadgeCount(app string) int {
	if s.badges == nil {
		return 0
	}
	return s.badges[app]
}
