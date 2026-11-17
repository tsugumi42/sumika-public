package control

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type BadgeTone uint8

const (
	Neutral BadgeTone = iota
	Active
	Success
	Warning
	Error
	Pending
)

type BadgeOptions struct {
	Tone     BadgeTone
	MaxWidth float32
}

// StatusBadge never acquires focus or adds an action inside its parent.
func StatusBadge(c *ui.Context, label string, o BadgeOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Badge()
	fg, bg := t.Colors().Window().MutedText(), t.Colors().Window().BackgroundHover()
	switch o.Tone {
	case Active:
		fg = t.Colors().Window().ForegroundActive()
		bg = t.Colors().Window().BackgroundActive()
	case Success:
		fg = t.Colors().Semantic().Success()
	case Warning:
		fg = t.Colors().Semantic().Warning()
	case Error:
		fg = t.Colors().Semantic().Danger()
	}
	// paint/badge.md#紧凑文字标签 and #状态语义我定的.
	e := ui.Row(c).Label(label).Role(ui.RoleStatus).AlignSelf(ui.Start).Shrink(0).Padding(m.TextPaddingTop(), m.TextPaddingRight(), m.TextPaddingBottom(), m.TextPaddingLeft()).Radius(m.TextRadius()).Background(bg)
	if o.MaxWidth > 0 {
		e.MaxWidth(o.MaxWidth)
	}
	return e.Children(func() { text(c, label, t.Typography().StatusBadge(), fg).SingleLine().MinWidth(0).Shrink(1) })
}

type CountBadgeOptions struct{ Muted, Active, Hovered bool }

func CountBadge(c *ui.Context, label string, o CountBadgeOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Conversation()
	colors := t.Colors().Conversation()
	bg, fg := colors.UnreadBackground(), colors.UnreadForeground()
	if o.Hovered {
		bg = colors.UnreadBackgroundHover()
		fg = colors.UnreadForegroundHover()
	}
	if o.Active {
		bg = colors.UnreadBackgroundActive()
		fg = colors.UnreadForegroundActive()
	}
	if o.Muted {
		bg = colors.UnreadBackgroundMuted()
		if o.Hovered {
			bg = colors.UnreadBackgroundMutedHover()
		}
		if o.Active {
			bg = colors.UnreadBackgroundMutedActive()
		}
	}
	// paint/badge.md#数字badge: the font's line box is centered in the 19 DIP pill.
	return ui.Row(c).Label(label).Role(ui.RoleStatus).Height(m.BadgeHeight()).MinWidth(m.BadgeHeight()).Padding(0, m.BadgePadding()).Radius(m.BadgeHeight() / 2).Center().AlignSelf(ui.Start).Background(bg).Children(func() { text(c, label, t.Typography().Badge(), fg).SingleLine() })
}

type CollapseOptions struct {
	Expanded, Up, Instant bool
	MinimumHeight         float32
	Padding               [4]float32 // left, top, right, bottom
	Trigger               *ui.Handle
	OnComplete            func(bool)
}
type collapseLocal struct {
	motion        timeline
	full          float32
	content, root ui.Handle
	transitioning bool
}

// Collapse measures live children at their final width, changing only layout
// height and clipping. Hidden children keep their identity and host state.
func Collapse(c *ui.Context, o CollapseOptions, content func()) ui.Element {
	root := ui.Column(c).FillWidth().Shrink(0).Clip()
	s := ui.Local(root, "collapse", func() collapseLocal { return collapseLocal{} })
	root.Bind(&s.root)
	if !o.Expanded && s.content.FocusWithin(c) {
		s.content.CancelFocus(c)
		if o.Trigger != nil {
			o.Trigger.Focus(c)
		} else {
			root.Focusable()
			s.root.Focus(c)
		}
	}
	wasMoving := s.motion.ready && s.motion.value != s.motion.target
	a := s.motion.update(c, unit(o.Expanded), theme.T(c).Motion().Collapse(), o.Instant)
	moving := a != unit(o.Expanded)
	if wasMoving && !moving && s.transitioning {
		s.transitioning = false
		if o.OnComplete != nil {
			done := o.Expanded
			root.OnChange(func() { o.OnComplete(done) })
			root.SetChanged()
		}
	}
	if moving {
		s.transitioning = true
	}
	height := func() float32 { return round(min(o.MinimumHeight, s.full) + (s.full-min(o.MinimumHeight, s.full))*a) }
	root.Height(height())
	services := c.Services()
	// paint/collapse.md#几何与动画; #显隐交互和状态保留.
	root.Children(func() {
		top := float32(0)
		if o.Up {
			top = height() - s.full
		}
		child := ui.Column(c.Key("content")).Absolute().Left(0).Right(0).Top(top).Shrink(0).Bind(&s.content).Padding(o.Padding[1], o.Padding[2], o.Padding[3], o.Padding[0]).IgnoreInput(a < 1).Hidden(!o.Expanded && a == 0 && o.MinimumHeight == 0)
		child.Children(content)
		if h := child.LayoutBounds().H; h > 0 && h != s.full {
			s.full = h
			root.Height(height())
			c.AnimationFrame()
		}
		child.DrawOver(func(_ *ui.Painter, r ui.Rect) {
			if r.H != s.full {
				s.full = r.H
				services.Invalidate()
			}
		})
	})
	return root
}
