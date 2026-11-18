package overlay

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
	"time"
)

type ToastState struct {
	Open   bool
	serial uint64
}

func (s *ToastState) Show()  { s.Open = true; s.serial++ }
func (s *ToastState) Close() { s.Open = false }

type ToastOptions struct {
	Center, Multiline, Infinite bool
	Duration                    time.Duration // zero uses theme.Motion.ToastHold
	Icon                        *ui.SVG
	Action                      string
	OnAction, OnClose           func()
}
type toastLocal struct {
	visible visibility
	serial  uint64
	started time.Time
}
type toastKey struct{ owner uint64 }

// Toast slides in at the bottom, or fades at the center. Calling Show again
// restarts its lifetime. Ordinary notifications let pointer input through.
func Toast(c *ui.Context, state *ToastState, message string, o ToastOptions) ui.Element {
	root := ui.Box(c).Size(0, 0)
	local := ui.Local(root, "toast", func() toastLocal { return toastLocal{} })
	t := theme.T(c)
	m := t.Metrics().Toast()
	vw, _ := c.Size()
	if state.Open && (local.started.IsZero() || local.serial != state.serial) {
		local.started = c.Now()
		local.serial = state.serial
	}
	hold := o.Duration
	if hold == 0 {
		hold = t.Motion().ToastHold().Duration()
	}
	if state.Open && !o.Infinite {
		if remaining := hold - c.Now().Sub(local.started); remaining > 0 {
			c.After(remaining)
		} else {
			state.Open = false
			if o.OnClose != nil {
				o.OnClose()
			}
		}
	}
	enter, exit := t.Motion().ToastSlide(), t.Motion().ToastSlide()
	if o.Center {
		enter, exit = t.Motion().ToastIn(), t.Motion().ToastOut()
	}
	shown := local.visible.update(c, state.Open, enter, exit)
	if !state.Open && shown == 0 {
		local.started = time.Time{}
		return root
	}
	maxWidth := m.MaxWidth()
	if o.Multiline {
		maxWidth = m.MultilineMaxWidth()
	}
	maxWidth = min(maxWidth, max(0, vw-m.Margin()*2))
	iconWidth := float32(0)
	if o.Icon != nil {
		iconWidth = m.IconSize() + m.IconPaddingX()*2
	}
	actionWidth := float32(0)
	if o.Action != "" {
		actionWidth, _ = measure(c, o.Action, t.Typography().Menu(), 0)
		actionWidth += m.IconPaddingX() * 2
	}
	available := max(0, maxWidth-m.PaddingLeft()-m.PaddingRight()-iconWidth-actionWidth)
	tw, th := measure(c, message, t.Typography().Body(), available)
	width := min(maxWidth, tw+m.PaddingLeft()+m.PaddingRight()+iconWidth+actionWidth)
	if o.Multiline {
		width = min(maxWidth, max(width, m.MultilineMinWidth()))
	}
	height := th + m.PaddingTop() + m.PaddingBottom()
	if o.Icon != nil {
		height = max(height, m.IconSize()+m.IconPaddingY()*2+m.PaddingTop()+m.PaddingBottom())
	}
	ui.Overlay(c, func() {
		panel := ui.Row(c.Key(toastKey{root.ID()})).Size(width, height).Radius(m.Radius()).Background(t.Colors().Toast().Background()).Role(ui.RoleStatus).Label(message).AlignItems(ui.Center).
			Padding(m.PaddingTop(), m.PaddingRight(), m.PaddingBottom(), m.PaddingLeft())
		if o.Center {
			panel.Attach(ui.AnchorCenter, ui.AnchorCenter).Opacity(shown)
		} else {
			panel.Attach(ui.AnchorBottom, ui.AnchorBottom).Bottom(m.Margin()).Translate(0, (1-shown)*(height+m.Margin()))
		}
		panel.IgnoreInput(o.Action == "" || !state.Open)
		panel.Children(func() {
			if o.Icon != nil {
				ui.Icon(c, o.Icon).Size(m.IconSize(), m.IconSize()).Margin(0, m.IconPaddingX()).TextColor(t.Colors().Toast().Foreground())
			}
			text(c, message, t.Typography().Body(), t.Colors().Toast().Foreground()).Grow(1).MinWidth(0)
			if o.Action != "" {
				ui.ButtonBase(c).Label(o.Action).Padding(0, m.IconPaddingX()).Children(func() {
					text(c, o.Action, t.Typography().Menu(), t.Colors().Viewer().TextLinkForeground()).SingleLine()
				}).OnClick(func() {
					state.Open = false
					if o.OnAction != nil {
						o.OnAction()
					}
				})
			}
		})
	})
	return root
}
