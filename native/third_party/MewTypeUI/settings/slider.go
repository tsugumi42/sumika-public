package settings

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type SliderOptions struct {
	Disabled           bool
	OnChange, OnCommit func(int)
}

// LimitSlider accepts a host-owned index into an explicitly supplied stop count.
// Business stop values and formatting stay outside theme and the component.
func LimitSlider(c *ui.Context, label string, index, count int, o SliderOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Slider()
	sm := t.Metrics().Settings()
	root := ui.Column(c).FillWidth().Height(sm.SliderPaddingTop()+m.MarkerHeight()+sm.SliderPaddingBottom()).Shrink(0).Padding(sm.SliderPaddingTop(), sm.SliderPaddingRight(), sm.SliderPaddingBottom(), sm.SliderPaddingLeft())
	root.Children(func() {
		holder := ui.Box(c).FillWidth().Height(m.MarkerHeight()).Shrink(0)
		local := ui.Local(holder, "slider-value", func() float64 { return float64(index) })
		*local = float64(index)
		// paint/numeric-input.md#自动下载大小右值行加离散滑块: integer
		// left half is 7, right remainder is 8, while artwork splits at 7.5.
		half := float32(int(m.MarkerWidth() / 2))
		holder.Children(func() {
			slider := ui.SliderBase(c, local, 0, float64(count-1)).Absolute().Left(0).Top(0).Right(0).Bottom(0).Padding(0, m.MarkerWidth()-half, 0, half).Step(1).Label(label).Disabled(o.Disabled)
			pressed := slider.Pressed()
			slider.OnChange(func() {
				if o.OnChange != nil {
					o.OnChange(int(*local))
				}
				if !pressed && o.OnCommit != nil {
					o.OnCommit(int(*local))
				}
			})
			slider.HandleInput(func(ev ui.InputEvent) bool {
				if ev.Kind == ui.InputPointerUp && o.OnCommit != nil {
					o.OnCommit(int(*local))
				}
				return false
			})
		})
		u := float32(index) / float32(count-1)
		rtl := c.Direction() == ui.RightToLeft
		holder.DrawOver(func(p *ui.Painter, r ui.Rect) {
			colorActive, colorInactive := t.Colors().Player().ActiveForeground(), t.Colors().Player().InactiveForeground()
			if o.Disabled {
				colorActive, colorInactive = colorInactive, t.Colors().Window().Background()
			}
			marker := round(half+u*(r.W-m.MarkerWidth())) - half
			split := round(m.MarkerWidth()/2 + u*(r.W-m.MarkerWidth()))
			y := float32(int((m.MarkerHeight() - m.TrackHeight()) / 2))
			radius := float32(int(m.TrackHeight() / 2))
			if rtl {
				split = r.W - split
				marker = r.W - marker - m.MarkerWidth()
				colorActive, colorInactive = colorInactive, colorActive
			}
			p.Clip(ui.Rect{X: r.X, Y: r.Y, W: split, H: r.H}, 0, func() { p.Fill(ui.Rect{X: r.X, Y: r.Y + y, W: r.W, H: m.TrackHeight()}, colorActive, radius) })
			p.Clip(ui.Rect{X: r.X + split, Y: r.Y, W: r.W - split, H: r.H}, 0, func() { p.Fill(ui.Rect{X: r.X, Y: r.Y + y, W: r.W, H: m.TrackHeight()}, colorInactive, radius) })
			if !o.Disabled {
				draw.Circle(p, ui.Rect{X: r.X + marker, Y: r.Y, W: m.MarkerWidth(), H: m.MarkerHeight()}, t.Colors().Player().ActiveForeground())
			}
		})
	})
	return root
}

// LimitValue is the noninteractive 30 DIP value row above a discrete slider.
// paint/numeric-input.md#自动下载大小右值行加离散滑块.
func LimitValue(c *ui.Context, title, value string) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Settings()
	return ui.Box(c).FillWidth().Height(m.LimitPaddingTop() + m.ButtonContentHeight() + m.LimitPaddingBottom()).Shrink(0).Children(func() {
		text(c, title, t.Typography().DialogBody(), t.Colors().Window().BoldForeground()).Absolute().Left(m.LimitPaddingLeft()).Top(m.LimitPaddingTop()).SingleLine()
		text(c, value, t.Typography().DialogBody(), t.Colors().Window().ActiveTextForeground()).Absolute().Right(m.ButtonValueRight()).Top(m.LimitPaddingTop()).SingleLine()
	})
}
