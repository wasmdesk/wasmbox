package scene

import (
	"testing"

	"github.com/go-widgets/toolkit"
)

// TestSidebarRowHeightCoversFont pins SidebarRowHeight to the editor face's
// height: toolkit's ListBox clamps every row UP to GlyphHeight() (toolkit
// v0.403.0), so a smaller constant would make the hit-test row pitch differ
// from the one this package (and its tests) compute.
func TestSidebarRowHeightCoversFont(t *testing.T) {
	newTestState(t)
	if gh := toolkit.GlyphHeight(); SidebarRowHeight < gh {
		t.Fatalf("SidebarRowHeight %d < GlyphHeight %d", SidebarRowHeight, gh)
	}
}
