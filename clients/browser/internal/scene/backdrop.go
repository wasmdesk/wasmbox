package scene

import (
	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
)

// fillBox paints a solid rectangle as a toolkit.Backdrop instead of a raw
// painter.FillRect, so the scene chrome composes from the widget toolkit rather
// than hand-drawn shape-ops. With an explicit Fill and the zero Radius, the
// Backdrop's Draw is byte-identical to p.FillRect(r, fill); the theme argument
// is unused for an explicit fill, so any *toolkit.Theme suffices.
func fillBox(p painter.Painter, r toolkit.Rect, fill toolkit.RGBA) {
	b := &toolkit.Backdrop{Fill: fill}
	b.SetBounds(r)
	b.Draw(p, toolkit.DefaultLight())
}
