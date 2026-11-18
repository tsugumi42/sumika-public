package chat

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type avatarTextKey struct {
	text, font string
	size       float32
	weight     int
}

type avatarTextMetrics struct {
	key  avatarTextKey
	w, h float32
}

// paint/chat-identity.md#圆与首字: retain shaped line geometry across color changes.
func avatarPainter(c *ui.Context, root ui.Element, message *Message) func(*ui.Painter, ui.Rect) {
	t := theme.T(c)
	colors := t.Colors().Avatar()
	from, to := colors.Peer4UserpicBackground(), colors.Peer4UserpicBackground()
	bitmap := message.Avatar
	var label [1]ui.Span
	var w, h float32
	if bitmap == nil && message.AvatarInitials != "" {
		to = colors.Peer4UserpicBackground2()
		style := t.Typography().MessageAvatar()
		size := float32(int(t.Metrics().Message().AvatarSize()*style.Size()/theme.Light().Metrics().Message().AvatarSize())) * c.Preferences().TextScale
		label[0] = ui.Span{Text: message.AvatarInitials, Font: t.Typography().Font(), Size: size, Weight: style.Weight(), Color: colors.UserpicForeground()}
		key := avatarTextKey{message.AvatarInitials, t.Typography().Font(), size, style.Weight()}
		metrics := ui.Local(root, "chat.avatar.text", func() avatarTextMetrics { return avatarTextMetrics{} })
		if metrics.key != key {
			metrics.key = key
			metrics.w, metrics.h = c.MeasureText(0, label[:]...)
		}
		w, h = metrics.w, metrics.h
	}
	if message.AvatarColor != (ui.Color{}) {
		from, to = message.AvatarColor, message.AvatarColor
	}
	return func(p *ui.Painter, r ui.Rect) {
		// paint/chat-identity.md#资源与颜色优先级: bitmap readiness is host-owned.
		p.Clip(r, r.W/2, func() {
			if bitmap != nil {
				p.Fill(r, from, r.W/2)
				p.Image(bitmap, r, ui.Cover)
				return
			}
			if from == to {
				p.Fill(r, from, r.W/2)
			} else {
				p.FillGradient(r, ui.LinearGradient{From: from, To: to, Angle: 180}, r.W/2)
			}
			if label[0].Text != "" {
				p.RichText(r.X+(r.W-w)/2, r.Y+(r.H-h)/2, 0, label[:]...)
			}
		})
	}
}
