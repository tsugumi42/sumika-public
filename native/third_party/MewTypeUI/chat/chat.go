// Package chat implements the header, message shells and history furniture.
// Hosts own message data, selection and new-message events. Source geometry and
// group-end/viewport avatar behavior are documented in docs/reference/chat.md.
package chat

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func text(c *ui.Context, label string, style theme.TextStyle, color ui.Color) ui.Element {
	return ui.Text(c, label).Font(theme.T(c).Typography().Font()).
		FontSize(style.Size() * c.Preferences().TextScale).FontWeight(style.Weight()).TextColor(color)
}

func span(c *ui.Context, label string, style theme.TextStyle, color ui.Color) ui.Span {
	return ui.Span{Text: label, Font: theme.T(c).Typography().Font(),
		Size: style.Size() * c.Preferences().TextScale, Weight: style.Weight(), Color: color}
}

func animation(t theme.Transition) ui.Animation {
	return ui.Animation{Duration: t.Duration(), Ease: t.Easing().Curve()}
}
