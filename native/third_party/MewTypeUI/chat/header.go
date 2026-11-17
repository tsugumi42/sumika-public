package chat

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type Status uint8

const (
	Offline Status = iota
	Online
	Typing
	Connecting
)

type HeaderInfo struct {
	Title, Subtitle string
	Status          Status
}

type ActionKind uint8

const (
	CustomAction ActionKind = iota
	SearchAction
	CallAction
	MenuAction
	CancelAction
)

// Action.ID is stable and unique among a header's actions. Labels are supplied
// by the host for localization and accessibility, including icon-only controls.
type Action struct {
	ID, Label        string
	Kind             ActionKind
	Icon             *ui.SVG
	Active, Disabled bool
	OnActivate       func()
}

type HeaderOptions struct {
	Actions        []Action
	BackLabel      string
	OnBack, OnOpen func()
	// SelectionLabel replaces both title lines while selecting/reporting.
	SelectionLabel string
	Badge          func()
}

// Header draws ordinary, online, typing, connecting and selection headers.
// Connecting/typing strings and any animated status artwork belong to the host.
func Header(c *ui.Context, info HeaderInfo, o HeaderOptions) ui.Element {
	t := theme.T(c)
	m, colors := t.Metrics().Header(), t.Colors()
	_, titleHeight := c.MeasureText(0, span(c, info.Title, t.Typography().HeaderTitle(), colors.Conversation().NameForeground()))
	_, statusHeight := c.MeasureText(0, span(c, info.Subtitle, t.Typography().HeaderStatus(), colors.Window().MutedText()))
	height := max(m.Height(), titleHeight+statusHeight+m.TitleTop()+m.StatusBottom())
	return ui.Row(c).Height(height).Shrink(0).Background(t.Materials().Header().Background()).Children(func() {
		if o.OnBack != nil {
			c.Key("back")
			headerButton(c, Action{Label: o.BackLabel, Icon: backIcon, OnActivate: o.OnBack}, m.BackWidth(), height)
		} else {
			ui.Box(c).Width(m.TitleLeft()).Shrink(0)
		}
		c.Key("title")
		title := ui.ButtonBase(c).Grow(1).MinWidth(0).Height(height).OnClick(o.OnOpen)
		title.Children(func() {
			if o.SelectionLabel != "" {
				text(c, o.SelectionLabel, t.Typography().HeaderTitle(), colors.Conversation().NameForeground()).SingleLine()
				return
			}
			ui.Row(c).Absolute().Top(m.TitleTop()).Left(0).Right(0).MinWidth(0).Children(func() {
				text(c, info.Title, t.Typography().HeaderTitle(), colors.Conversation().NameForeground()).SingleLine().MinWidth(0)
				if o.Badge != nil {
					o.Badge()
				}
			})
			fg := colors.Window().MutedText()
			if info.Status == Online || info.Status == Typing {
				fg = colors.Window().ActiveTextForeground()
			}
			text(c, info.Subtitle, t.Typography().HeaderStatus(), fg).SingleLine().
				Absolute().Left(0).Right(0).Bottom(m.StatusBottom())
		})
		for _, a := range o.Actions {
			c.Key(a.ID)
			width := m.ActionWidth()
			if a.Kind == MenuAction {
				width = m.MenuWidth()
			}
			headerButton(c, a, width, height)
		}
	})
}

func headerButton(c *ui.Context, a Action, width, height float32) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Header()
	icon := a.Icon
	if icon == nil {
		switch a.Kind {
		case SearchAction:
			icon = searchIcon
		case CallAction:
			icon = callIcon
		case MenuAction:
			icon = menuIcon
		case CancelAction:
			icon = cancelIcon
		}
	}
	b := ui.ButtonBase(c).Size(width, height).Shrink(0).Label(a.Label).
		Disabled(a.Disabled).OnClick(a.OnActivate)
	fg := t.Colors().Menu().IconForeground()
	if b.Hovered() || b.Pressed() {
		fg = t.Colors().Menu().IconForegroundHover()
	}
	if a.Active {
		fg = t.Colors().Window().ActiveTextForeground()
	}
	if a.Disabled {
		fg = t.Colors().Menu().ForegroundDisabled()
	}
	ripple := buttonRipple(c, b, t.Colors().Window().BackgroundPressed())
	// paint/icon-glyph.md#图形限框: the button and ripple keep their canvas.
	iconSize := m.IconSize()
	if draw.IsHost(icon) {
		iconSize *= t.Metrics().Glyph().HostInkRatio()
	}
	glyph := draw.Resolve(c, icon, m.IconSize(), m.IconSize())
	b.Draw(func(p *ui.Painter, r ui.Rect) {
		size := m.RippleSize()
		v := ripple
		v.x -= (r.W - size) / 2
		v.y -= (r.H - size) / 2
		v.draw(p, ui.Rect{X: r.X + (r.W-size)/2, Y: r.Y + (r.H-size)/2, W: size, H: size})
		if icon != nil {
			s := iconSize
			glyph.Paint(p, ui.Rect{X: r.X + (r.W-s)/2, Y: r.Y + (r.H-s)/2, W: s, H: s}, fg)
		}
	})
	return b
}
