package overlay

import (
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// TooltipTrigger selects how an anchor reveals its tooltip.
type TooltipTrigger uint8

const (
	TooltipHover TooltipTrigger = 1 << iota
	TooltipFocus
)

type TooltipOptions struct {
	// Nil uses the chat tooltip delay; a pointer to zero shows immediately.
	Delay *time.Duration
	// Trigger defaults to TooltipHover | TooltipFocus. Focus means visible
	// keyboard focus, appears immediately, and never transfers focus to the tip.
	Trigger TooltipTrigger
}
type tooltipLocal struct {
	since     time.Time
	point     ui.Point
	dismissed bool
}
type nativeTooltip struct {
	host          *nativeHost
	owner         *tooltipLocal
	window        *mygo.Window
	label         string
	visible       visibility
	closing       bool
	hidden        bool
	destroyQueued bool
}

// Tooltip shows prepared text in a nonactivating native flyout. Build inside
// Native; the pointer, work area and theme determine its placement and size.
func Tooltip(c *ui.Context, anchor ui.Element, label string, o TooltipOptions) ui.Element {
	root := ui.Box(c).Size(0, 0)
	state := ui.Local(root, "tooltip", func() tooltipLocal { return tooltipLocal{} })
	anchor.Description(label)
	// paint/tooltip.md#触发与定位: share input rules with the native content tests.
	if !state.show(c, anchor, label, o) {
		return root
	}
	t := theme.T(c)
	m := t.Metrics().Tooltip()
	h := native(c)
	if h.menu != nil || !h.window.IsFocused() {
		return root
	}
	h.tipSeen = true
	if h.tip != nil && h.tip.owner == state && h.tip.label == label {
		h.tip.hidden = false
		return root
	}
	if h.tip != nil {
		h.tip.close()
	}
	width, height := tooltipSize(c, label)
	at := anchor.Bounds()
	px := at.X + state.point.X - m.ShiftX()
	if width < 2*m.ShiftX() {
		px = at.X + state.point.X - width/2
	}
	if c.Direction() == ui.RightToLeft {
		px = at.X + state.point.X - width + m.ShiftX()
	}
	// The anchor spans the source's above/below offsets. Flyout flips above its
	// top (-skip) rather than below an already shifted pointer at the screen edge.
	rect := mygo.Rectangle{X: int(px), Y: int(at.Y + state.point.Y - m.EdgeSkip()), Height: extent(m.ShiftY() + m.EdgeSkip())}
	tip := &nativeTooltip{host: h, owner: state, label: label}
	h.tip = tip
	direction := c.Direction()
	h.window.Update(func() {
		if h.tip != tip || tip.closing {
			return
		}
		tip.window = mygo.NewFlyout(mygo.FlyoutOptions{
			Parent: h.window, Anchor: rect, Width: extent(width), Height: extent(height), Hidden: true,
			Content: ui.View(func(c *ui.Context) {
				theme.Apply(c, h.theme)
				c.Root().Direction(direction)
				c.Root().Background(ui.Transparent)
				tip.render(c)
			}),
		})
		tip.window.SetTitle("MewTypeUI tooltip")
		tip.window.SetIgnoreMouseEvents(true)
		tip.window.OnReadyToShow(func() {
			if !tip.closing {
				tip.window.ShowInactive()
			}
		})
		tip.window.OnClosed(func() {
			if h.tip == tip {
				h.tip = nil
			}
		})
	})
	return root
}

func (state *tooltipLocal) show(c *ui.Context, anchor ui.Element, label string, o TooltipOptions) bool {
	trigger := o.Trigger
	if trigger == 0 {
		trigger = TooltipHover | TooltipFocus
	}
	x, y, over := anchor.PointerPosition()
	hover := trigger&TooltipHover != 0 && anchor.Hovered() && over
	focus := trigger&TooltipFocus != 0 && anchor.Focused() && anchor.FocusVisible()
	if (!hover && !focus) || anchor.IsDisabled() || label == "" {
		state.since, state.dismissed = time.Time{}, false
		return false
	}
	if anchor.Pressed() || anchor.Clicked() || anchor.RightClicked() || c.Root().OverlayShortcut(0, ui.KeyEscape) {
		state.dismissed = true
	}
	if state.dismissed {
		return false
	}
	if focus {
		at := anchor.Bounds()
		state.point = ui.Point{X: at.W / 2, Y: at.H}
		return true
	}
	m := theme.T(c).Metrics().Tooltip()
	if state.since.IsZero() || abs(x-state.point.X)+abs(y-state.point.Y) > m.EdgeSkip() {
		state.since, state.point = c.Now(), ui.Point{X: x, Y: y}
	}
	delay := theme.T(c).Motion().TooltipDelay().Duration()
	if o.Delay != nil {
		delay = *o.Delay
	}
	if wait := delay - c.Now().Sub(state.since); wait > 0 {
		c.After(wait)
		return false
	}
	return true
}
func tooltipSize(c *ui.Context, label string) (float32, float32) {
	t := theme.T(c)
	m := t.Metrics().Tooltip()
	border := t.Metrics().Control().BorderWidth()
	width, height := measure(c, label, t.Typography().Body(), m.MaxWidth()-2*(m.PaddingX()+border))
	_, line := measure(c, "Mg", t.Typography().Body(), 0)
	return min(m.MaxWidth(), width+2*(m.PaddingX()+border)), min(height, line*m.MaxLines()) + 2*(m.PaddingY()+border)
}
func (tip *nativeTooltip) render(c *ui.Context) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Tooltip()
	// paint/tooltip.md#动效: retain the same content through exit and reversal.
	alpha := tip.visible.update(c, !tip.hidden, t.Motion().Fade(), t.Motion().Fade())
	if tip.hidden && alpha == 0 && tip.host != nil && !tip.destroyQueued {
		tip.destroyQueued = true
		tip.host.window.Update(func() {
			tip.destroyQueued = false
			if tip.hidden {
				tip.close()
			}
		})
	}
	root := ui.Box(c).Fill().Role(ui.RoleTooltip).Label(tip.label).IgnoreInput(true).
		Padding(m.PaddingY(), m.PaddingX()).Border(t.Metrics().Control().BorderWidth(), t.Colors().Tooltip().BorderForeground()).
		Radius(t.Metrics().Control().RadiusSmall()).Opacity(alpha)
	material(root, t.Materials().Tooltip())
	root.Children(func() {
		text(c, tip.label, t.Typography().Body(), t.Colors().Tooltip().Foreground()).MaxLines(int(m.MaxLines()))
	})
	return root
}
func (tip *nativeTooltip) hide() {
	if !tip.hidden {
		tip.hidden = true
		if tip.window != nil {
			tip.window.Invalidate()
		}
	}
}

func (tip *nativeTooltip) close() {
	if tip.closing {
		return
	}
	tip.closing = true
	if tip.host.tip == tip {
		tip.host.tip = nil
	}
	tip.host.window.Update(func() {
		if tip.window != nil {
			tip.window.Destroy()
		}
	})
}
func abs(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
