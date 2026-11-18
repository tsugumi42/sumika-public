package layout

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type DividerOptions struct {
	Label    string
	Min, Max float32
	Reverse  bool // size the pane to the right of the divider
	OnCommit func()
}

type dividerDrag struct {
	active bool
	raw    float32
}

// Divider is a vertical, keyboard-focusable resize target. value is the shown
// pane width; onResize receives the requested width. A gesture accumulates its
// raw motion independently of snapping performed by the caller.
func Divider(c *ui.Context, value float32, onResize func(float32), o DividerOptions) ui.Element {
	t := theme.T(c)
	hit := t.Metrics().Layout().ResizeHitWidth()
	line := t.Metrics().Control().BorderWidth()
	step := t.Metrics().Conversation().PaddingLeft()
	e := ui.Box(c).Width(hit).FillHeight().Shrink(0).Focusable().Role(ui.RoleSplitter).
		Label(o.Label).Cursor(ui.CursorResizeEW).Range(float64(o.Min), float64(o.Max), float64(value)).Step(float64(step))
	drag := ui.Local(e, "drag", func() dividerDrag { return dividerDrag{} })
	dx, _, held := e.Dragged()
	if held {
		if !drag.active {
			drag.raw, drag.active = value, true
		}
		if dx != 0 {
			if o.Reverse {
				dx = -dx
			}
			drag.raw += dx
			onResize(clamp(drag.raw, o.Min, o.Max))
			e.SetChanged().Focus()
		}
	} else {
		drag.active = false
	}
	if _, _, ended := e.DragEnded(); ended && o.OnCommit != nil {
		o.OnCommit()
	}
	set := func(v float32) {
		onResize(v)
		if o.OnCommit != nil {
			o.OnCommit()
		}
	}
	move := func(delta float32) {
		if o.Reverse {
			delta = -delta
		}
		set(clamp(value+delta, o.Min, o.Max))
	}
	e.OnShortcut(0, ui.KeyLeft, func() { move(-step) }).
		OnShortcut(0, ui.KeyRight, func() { move(step) }).
		OnShortcut(0, ui.KeyHome, func() { set(o.Min) }).
		OnShortcut(0, ui.KeyEnd, func() { set(o.Max) })
	color := t.Colors().Control().ShadowForeground()
	if held || e.Hovered() || e.FocusVisible() {
		color = t.Colors().Control().Focus()
	}
	e.Draw(func(p *ui.Painter, r ui.Rect) {
		p.Fill(ui.Rect{X: r.X, Y: r.Y, W: line, H: r.H}, color, 0)
	})
	return e
}
