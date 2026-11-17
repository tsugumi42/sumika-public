package chat

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type ServiceOptions struct {
	Selected   bool
	OnActivate func()
}

// ServiceMessage centers a system notice. Text wraps using the platform font;
// height, padding and corner radius grow with text scale.
func ServiceMessage(c *ui.Context, label string, o ServiceOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Service()
	bg := t.Materials().Service().Background()
	if o.Selected {
		bg = t.Colors().Service().BackgroundSelected()
	}
	_, line := c.MeasureText(0, span(c, "M", t.Typography().Service(), t.Colors().Service().Text()))
	radius := float32(int(line+m.PaddingTop()+m.PaddingBottom()) / 2)
	return ui.Column(c).Padding(m.MarginTop(), m.MarginRight(), m.MarginBottom(), m.MarginLeft()).
		AlignItems(ui.Center).Children(func() {
		labelElement := text(c, label, t.Typography().Service(), t.Colors().Service().Text()).TextAlign(ui.Center).
			Padding(m.PaddingTop(), m.PaddingRight(), m.PaddingBottom(), m.PaddingLeft()).Radius(radius).
			Selected(o.Selected).Background(ui.Color{}).TextColor(t.Colors().Service().Text()).OnClick(o.OnActivate)
		serviceBackground(c, labelElement, label, bg)
	})
}

type DateOptions struct {
	OnActivate func()
}

func DateMarker(c *ui.Context, label string, o DateOptions) ui.Element {
	return ServiceMessage(c, label, ServiceOptions{OnActivate: o.OnActivate})
}

func UnreadMarker(c *ui.Context, label string) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Service()
	_, line := c.MeasureText(0, span(c, label, t.Typography().Service(), t.Colors().Service().UnreadText()))
	height := max(m.UnreadHeight(), line+2*t.Metrics().Control().BorderWidth())
	return ui.Column(c).Padding(m.UnreadMargin(), 0, 0, 0).Children(func() {
		ui.Row(c).Height(height).Center().Background(t.Colors().Service().UnreadBackground()).
			BorderWidth(0, 0, t.Metrics().Control().BorderWidth(), 0).BorderColor(t.Colors().Service().UnreadBorder()).
			Children(func() { text(c, label, t.Typography().Service(), t.Colors().Service().UnreadText()).SingleLine() })
	})
}

// DateState belongs to a history viewport. Offset is supplied by its scroll
// state; a newly mounted history does not pretend that historical rows arrived.
type DateState struct {
	offset      float32
	label       string
	moved       time.Time
	initialized bool
	crossed     bool
}

type FloatingDateOptions struct {
	ScrollOffset float32
	// DayBottom is the current day's bottom relative to the viewport. A day
	// ending near the top pushes the floating date away. Zero means unbounded.
	DayBottom  float32
	OnActivate func()
}

// FloatingDate builds in a stationary viewport above its scroll container.
// Scroll movement reveals it; it waits DateHold, then fades over DateFade.
func FloatingDate(c *ui.Context, label string, state *DateState, o FloatingDateOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Service()
	now := c.Now()
	changed := state.initialized && (state.offset != o.ScrollOffset || state.label != label)
	if changed {
		if label != state.label && o.ScrollOffset > state.offset {
			state.crossed = true
		}
		if o.ScrollOffset < state.offset {
			state.crossed = false
		}
		state.moved = now
	}
	state.offset, state.label, state.initialized = o.ScrollOffset, label, true
	root := ui.Column(c).Absolute().Left(0).Right(0).Top(0)
	elapsed := now.Sub(state.moved)
	hold, fade := t.Motion().DateHold(), t.Motion().DateFade()
	if state.crossed {
		hold = t.Motion().DateCrossingHold()
	}
	visible := !state.moved.IsZero() && label != "" && elapsed < hold.Duration()
	if visible {
		c.After(hold.Duration() - elapsed)
	}
	width, line := c.MeasureText(0, span(c, label, t.Typography().Service(), t.Colors().Service().Text()))
	height := line + m.PaddingTop() + m.PaddingBottom()
	width += m.PaddingLeft() + m.PaddingRight()
	x, y, _ := root.PointerPosition()
	left := (root.Bounds().W - width) / 2
	hovered := root.Hovered() && x >= left && x < left+width && y >= m.MarginTop() && y < m.MarginTop()+height
	target := float32(0)
	if visible || hovered {
		target = 1
	}
	opacity := root.AnimateTo("date", target, animation(fade))
	if c.Instant() || c.Preferences().ReduceMotion {
		opacity = target
	}
	root.Opacity(opacity)
	if opacity == 0 {
		root.Invisible()
	}
	if o.DayBottom != 0 {
		root.Top(min(0, o.DayBottom-height-m.MarginTop()))
	}
	return root.Children(func() { DateMarker(c, label, DateOptions{OnActivate: o.OnActivate}) })
}
