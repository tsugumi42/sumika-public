package chat

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type Side uint8

const (
	Incoming Side = iota
	Outgoing
)

type Group struct{ Previous, Next bool }

type Delivery uint8

const (
	NoReceipt Delivery = iota
	Sending
	Sent
	Read
	Failed
)

// Info receives localized date/prefix strings and already formatted counts.
// Signature is elided independently; Label always remains intact.
type Info struct {
	Label, Prefix, Signature  string
	Views, Replies            string
	Delivery                  Delivery
	Pinned, Ephemeral, Silent bool
	RepliesContext, Shortcut  bool
}

type Message struct {
	Text, SenderName string
	// SenderNameColor overrides only this message's name; zero uses the theme.
	SenderNameColor ui.Color
	Avatar          *ui.Bitmap
	// AvatarInitials is prepared display text; nil Avatar uses it for missing,
	// loading and failed images. AvatarLabel is the full accessible name.
	AvatarInitials, AvatarLabel string
	// AvatarColor is a solid initials background; zero uses the theme gradient.
	AvatarColor ui.Color
	Info        Info
}

type BubbleOptions struct {
	Side     Side
	Group    Group
	Selected bool
	// SuppressTail covers visual media, inline keyboards, about/comment roots.
	SuppressTail bool
	// Body renders custom trailing text with the same invisible reservation as Body.
	Body func(ui.Span)
	// Footer transfers the reservation to the final reaction row.
	Footer func(ui.Span)
	// MediaBottom leaves information to the bottom media and reactions outside.
	MediaBottom bool
	HideInfo    bool
}

// Bubble is a shell with a content insertion point before its text (quotes,
// forwarding and media). Footer inserts content after the text. Use an empty
// Message.Text for entirely custom content. Row adds alignment and avatars.
func Bubble(c *ui.Context, message Message, o BubbleOptions, content func()) ui.Element {
	// paint/message-bottom.md#图片与状态矩阵: reactions alone never create a shell.
	if message.Text == "" && content == nil && o.Body == nil {
		return ui.Box(c).Size(0, 0)
	}
	if o.MediaBottom && o.Footer != nil {
		footer := o.Footer
		o.Footer = nil
		return ui.Column(c).MinWidth(0).MaxWidth(theme.T(c).Metrics().Message().MaxWidth()).AlignSelf(ui.Start).Children(func() {
			c.Key("media-shell")
			Bubble(c, message, o, content)
			c.Key("external-reactions")
			ui.Column(c).MinWidth(0).Margin(theme.T(c).Metrics().Preview().MediaGap(), 0, 0).Children(func() { footer(ui.Span{}) })
		})
	}
	t := theme.T(c)
	m := t.Metrics().Message()
	bg, fg, shadow, name, _ := messageColors(t, o.Side, o.Selected)
	// paint/chat-identity.md#发送人名字: this override never changes body colors.
	if message.SenderNameColor != (ui.Color{}) {
		name = message.SenderNameColor
	}
	corners := rounding(m, o)
	root := ui.Column(c).MinWidth(0).MaxWidth(m.MaxWidth()).AlignSelf(ui.Start).
		Padding(m.PaddingTop(), m.PaddingRight(), m.PaddingBottom(), m.PaddingLeft()).
		Radius(corners[:]...).Selected(o.Selected).Background(bg).TextColor(fg).
		Font(t.Typography().Font()).FontSize(t.Typography().Message().Size()*c.Preferences().TextScale).
		Shadow(0, m.ShadowHeight(), 0, 0, shadow)
	if !o.SuppressTail && !o.Group.Next {
		// Original vector geometry; no upstream raster mask is embedded.
		root.Draw(tailPainter(root, o.Side, m, bg, shadow))
	}
	root.Children(func() {
		if !o.Group.Previous && message.SenderName != "" {
			c.Key("sender")
			text(c, message.SenderName, t.Typography().MessageName(), name).SingleLine()
		}
		if content != nil {
			c.Key("content")
			ui.Column(c).MinWidth(0).Children(content)
		}
		info := message.Info
		if o.HideInfo || o.MediaBottom {
			info = Info{}
		}
		reservation := InfoReservation(c, info, BodyOptions{Side: o.Side, Selected: o.Selected})
		bodyTail := reservation
		if o.Footer != nil {
			bodyTail = ui.Span{}
		}
		if o.Body != nil {
			c.Key("body")
			o.Body(bodyTail)
		} else if message.Text != "" {
			c.Key("body")
			bodyText(c, message.Text, bodyTail, BodyOptions{Side: o.Side, Selected: o.Selected})
		}
		if o.Footer != nil && !o.MediaBottom {
			// paint/message-bottom.md#有回应时最后一行回应让时间
			c.Key("footer")
			ui.Column(c).MinWidth(0).Margin(t.Metrics().Preview().MediaGap(), 0, 0).Children(func() { o.Footer(reservation) })
		}
		if info != (Info{}) {
			c.Key("info")
			BottomInfo(c, info, BodyOptions{Side: o.Side, Selected: o.Selected}).Absolute().Right(m.PaddingRight() - m.TimestampDeltaX()).Bottom(m.PaddingBottom() - m.TimestampDeltaY())
		}
	})
	return root
}

type RowOptions struct {
	BubbleOptions
	// ReserveAvatar keeps the sender column even on subsequent group messages.
	ReserveAvatar bool
	// AvatarGroupTop is the group's top in window coordinates for a virtual
	// history whose earlier rows are not mounted. Nil clamps to this row's top.
	AvatarGroupTop *float32
}

// Row uses the host's group boundaries, preserving its rules for identity,
// service breaks and time windows. The host sets c.Key(messageID) before Row.
func Row(c *ui.Context, message Message, o RowOptions, content func()) ui.Element {
	m := theme.T(c).Metrics().Message()
	top := m.MarginTop()
	if o.Group.Previous {
		top = m.AttachedTop()
	}
	left, right := m.MarginLeft(), m.MarginRight()
	if o.Side == Outgoing {
		left, right = right, left
	}
	if o.ReserveAvatar && o.Side == Incoming {
		left = m.AvatarLeft()
		right -= m.AvatarSkip()
	}
	root := ui.Row(c).AlignItems(ui.Start).Padding(top, right, m.MarginBottom(), left).Children(func() {
		if o.Side == Outgoing {
			ui.Spacer(c)
		}
		if o.ReserveAvatar && o.Side == Incoming {
			c.Key("avatar")
			avatar := ui.Box(c).Width(m.AvatarSkip() + m.MarginLeft() - m.AvatarLeft()).Shrink(0).AlignSelf(ui.Stretch)
			if message.AvatarLabel != "" {
				// paint/chat-identity.md#资源与颜色优先级: initials are not the full name.
				avatar.Label(message.AvatarLabel).Role(ui.RoleImage)
			}
		}
		c.Key("bubble")
		Bubble(c, message, o.BubbleOptions, content)
		if o.Side == Incoming {
			ui.Spacer(c)
		}
	})
	if o.ReserveAvatar && o.Side == Incoming {
		var anchor *ui.Handle
		if o.AvatarGroupTop != nil {
			anchor = ui.Local(root, "avatar-coordinates", func() ui.Handle { return ui.Handle{} })
			root.Bind(anchor)
		}
		paint := avatarPainter(c, root, &message)
		root.DrawOver(func(p *ui.Painter, r ui.Rect) {
			visible := p.Visible()
			bottom := r.Y + r.H - m.MarginBottom()
			if o.Group.Next && r.Y+r.H < visible.Y+visible.H {
				return
			}
			groupTop := r.Y + top
			if o.AvatarGroupTop != nil {
				groupTop = *o.AvatarGroupTop + r.Y - anchor.Bounds(c).Y
			}
			bottom = max(groupTop+m.AvatarSize(), min(bottom, visible.Y+visible.H-m.HistoryBottom()-m.MarginBottom()))
			avatar := ui.Rect{X: r.X + m.AvatarLeft(), Y: bottom - m.AvatarSize(), W: m.AvatarSize(), H: m.AvatarSize()}
			paint(p, avatar)
		})
	}
	return root
}

func rounding(m theme.MetricsMessage, o BubbleOptions) [4]float32 {
	r := [4]float32{m.RadiusLarge(), m.RadiusLarge(), m.RadiusLarge(), m.RadiusLarge()}
	top, bottom := 0, 3
	if o.Side == Outgoing {
		top, bottom = 1, 2
	}
	if o.Group.Previous {
		r[top] = m.RadiusSmall()
	}
	if o.Group.Next {
		r[bottom] = m.RadiusSmall()
	} else if !o.SuppressTail {
		r[bottom] = 0
	}
	return r
}

func messageColors(t *theme.Theme, side Side, selected bool) (bg, fg, shadow, name, date ui.Color) {
	if side == Outgoing {
		v := t.Colors().Message().Out()
		if selected {
			return v.BackgroundSelected(), v.TextSelected(), v.ShadowSelected(), v.ServiceForegroundSelected(), v.TimestampForegroundSelected()
		}
		return t.Materials().MessageOut().Background(), v.Text(), v.Shadow(), v.ServiceForeground(), v.TimestampForeground()
	}
	v := t.Colors().Message().In()
	if selected {
		return v.BackgroundSelected(), v.TextSelected(), v.ShadowSelected(), v.ServiceForegroundSelected(), v.TimestampForegroundSelected()
	}
	return t.Materials().MessageIn().Background(), v.Text(), v.Shadow(), v.ServiceForeground(), v.TimestampForeground()
}

// Paths use their final DIP extent because mygo rasterizes before applying the
// painter transform. A normalized 1x1 path loses its curved silhouette.
type tailShape struct {
	width, height float32
	path          ui.Path
}

func tailPainter(root ui.Element, side Side, m theme.MetricsMessage, bg, shadow ui.Color) func(*ui.Painter, ui.Rect) {
	w, h, drop := m.TailWidth(), m.TailHeight(), m.ShadowHeight()
	shape := ui.Local(root, "chat.tail", func() tailShape { return tailShape{} })
	if shape.width != w || shape.height != h {
		shape.width, shape.height = w, h
		shape.path.Reset().MoveTo(0, 0).QuadTo(0, h/2, w, h).LineTo(0, h).Close()
	}
	return func(p *ui.Painter, r ui.Rect) {
		x, sx := r.X, float32(-1)
		if side == Outgoing {
			x, sx = r.X+r.W, 1
		}
		transform := ui.Scaling(sx, 1)
		p.Fill(ui.Rect{X: min(x, x+sx*w), Y: r.Y + r.H, W: w, H: drop}, shadow, 0)
		p.Transform(transform.Then(ui.Translation(x, r.Y+r.H-h)), 0, 0, func() { p.FillPath(&shape.path, bg) })
	}
}
