package dialogs

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func cornerBadge(c *ui.Context, host ui.Element, item Conversation, bg ui.Color, online float32, o RowOptions, covered bool) {
	t := theme.T(c)
	m := t.Metrics().Conversation()
	if covered {
		return
	}
	if item.Subscribed {
		ui.Box(c).Absolute().Right(-m.SubscriptionOutset()).Bottom(-m.SubscriptionOutset()).Size(m.SubscriptionSize(), m.SubscriptionSize()).Label("Subscription badge").Children(func() {
			// paint/icon-glyph.md#图形限框: nested filled stars, no Lucide pen.
			outer := m.SubscriptionSize() + 2*m.SubscriptionStroke()
			draw.Fit(c, subscriptionFill, outer, outer, outer, outer, 0).Element(c).Absolute().Left(-m.SubscriptionStroke()).Top(-m.SubscriptionStroke()).TextColor(bg)
			size := m.SubscriptionSize()
			draw.Fit(c, subscriptionFill, size, size, size, size, 0).Element(c).Absolute().Left(0).Top(0).TextColor(t.Colors().Credits().Background1())
		})
		return
	}
	if item.CommunityMember && !item.Call && !item.Online {
		b := ui.Box(c).Absolute().Right(-m.CommunityOutset()).Bottom(-m.CommunityOutset()).Size(m.CommunitySize(), m.CommunitySize()).Radius(m.CommunitySize()/2).Background(t.Colors().Window().Text()).Border(m.CommunityStroke(), bg).Label("Community badge")
		if o.OnCommunity != nil {
			b.OnClick(func() { o.OnCommunity(item.ID) }).Focusable().Role(ui.RoleButton)
		}
		b.DrawOver(func(p *ui.Painter, r ui.Rect) {
			arm := r.W / 4
			p.Line(r.X+r.W/2-arm, r.Y+r.H/2-arm/2, r.X+r.W/2, r.Y+r.H/2+arm/2, m.CommunityLine(), t.Colors().Window().Background())
			p.Line(r.X+r.W/2, r.Y+r.H/2+arm/2, r.X+r.W/2+arm, r.Y+r.H/2-arm/2, m.CommunityLine(), t.Colors().Window().Background())
		})
		return
	}
	color := t.Colors().Conversation().OnlineBadgeForeground()
	if o.Selected {
		color = t.Colors().Conversation().OnlineBadgeForegroundActive()
	}
	call := animate(host, "call-badge", unit(item.Call), t.Motion().Online())
	if call > 0 {
		size := m.CallSize() * call
		ui.Box(c).Absolute().Right(-m.CallOutset()-(m.OnlineStroke()*call)+(m.CallSize()-size)/2).Bottom(-m.CallOutset()-(m.OnlineStroke()*call)+(m.CallSize()-size)/2).Size(size+2*m.OnlineStroke()*call, size+2*m.OnlineStroke()*call).Radius(size/2 + m.OnlineStroke()*call).Background(bg).Center().Children(func() {
			inner := ui.Box(c).Size(size, size).Radius(size / 2).Background(color).Label("Call badge")
			callSpectrum(c, inner, item.Speaking, bg)
		})
		return
	}
	if online > 0 {
		size, stroke := m.OnlineSize()*online, m.OnlineStroke()*online
		bottom := m.OnlineSkipY() + (m.OnlineSize()-size)/2
		ui.Box(c).Absolute().Right((m.OnlineSize()-size)/2-stroke).Bottom(bottom-stroke).Size(size+2*stroke, size+2*stroke).Radius(size/2 + stroke).Background(bg).Center().Children(func() { ui.Box(c).Size(size, size).Radius(size / 2).Background(color).Label("Online badge") })
		return
	}
	if item.TTL != "" {
		size := m.TtlSize()
		backdrop := ttlBackdrop(c, host, item)
		b := ui.Box(c).Absolute().Right(-m.TtlOffset()).Bottom(-m.TtlOffset()).Size(size, size).Radius(size/2).Clip().Border(m.OnlineStroke(), bg).Label("TTL badge")
		b.Children(func() { ui.Image(c, backdrop).Absolute().Fill().Fit(ui.Cover); ttlTimer(c, b, item.TTL) })
	}
}
