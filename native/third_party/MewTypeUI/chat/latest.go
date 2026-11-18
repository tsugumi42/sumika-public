package chat

import (
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type LatestOptions struct {
	Visible    bool
	Unread     int
	Label      string
	Disabled   bool
	OnActivate func()
}

// LatestButton builds inside a stationary, clipped history viewport. The 52×62
// outer hit box includes badge/shadow space; the visible circle is 42 DIP.
// Visible is independent from Unread, so zero-unread navigation still works.
func LatestButton(c *ui.Context, o LatestOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Navigation()
	root := ui.ButtonBase(c).Absolute().Right(m.ToLatestRight()).
		Size(m.ToLatestWidth(), m.ToLatestHeight()).Label(o.Label).Disabled(o.Disabled).OnClick(o.OnActivate)
	target := float32(0)
	if o.Visible {
		target = 1
	}
	progress := root.AnimateTo("visibility", target, animation(t.Motion().ToLatest()))
	root.Bottom(progress*(m.ToLatestHeight()+m.ToLatestBottom()) - m.ToLatestHeight()).IgnoreInput(!o.Visible)
	if progress == 0 {
		root.Invisible()
	}
	bg, fg := t.Colors().Navigation().ToLatestBackground(), t.Colors().Navigation().ToLatestForeground()
	if root.Hovered() || root.Pressed() {
		bg, fg = t.Colors().Navigation().ToLatestBackgroundHover(), t.Colors().Navigation().ToLatestForegroundHover()
	}
	if o.Disabled {
		fg = t.Colors().Menu().ForegroundDisabled()
	}
	ripple := buttonRipple(c, root, t.Colors().Navigation().ToLatestBackgroundPressed())
	icon := draw.Resolve(c, downIcon, t.Metrics().Header().IconSize(), t.Metrics().Header().IconSize())
	return root.Children(func() {
		size := m.ToLatestRippleSize()
		ui.Box(c).Absolute().Left(m.ToLatestRippleLeft()).Top(m.ToLatestRippleTop()).Size(size, size).
			Radius(size/2).Shadow(0, t.Metrics().Message().ShadowHeight(), 0, 0, t.Colors().Navigation().ToLatestShadow()).
			Draw(func(p *ui.Painter, r ui.Rect) {
				draw.Circle(p, r, bg)
				draw.Icon(p, r, icon, t.Metrics().Header().IconSize(), fg)
				v := ripple
				v.x -= m.ToLatestRippleLeft()
				v.y -= m.ToLatestRippleTop()
				v.draw(p, r)
			})
		if o.Unread > 0 {
			label := strconv.Itoa(o.Unread)
			badge := span(c, label, t.Typography().LatestBadge(), t.Colors().Conversation().UnreadForeground())
			width, textHeight := c.MeasureText(0, badge)
			height := max(m.ToLatestBadgeHeight(), textHeight)
			width = max(height, width+2*m.ToLatestBadgePadding())
			ui.Row(c).Absolute().Top(0).Left((m.ToLatestWidth()-width)/2).Size(width, height).
				Radius(height / 2).Center().Background(t.Colors().Conversation().UnreadBackground()).Children(func() {
				text(c, label, t.Typography().LatestBadge(), t.Colors().Conversation().UnreadForeground()).SingleLine()
			})
		}
	})
}

// LatestVisible applies Telegram's ordinary threshold. Hosts OR in navigation
// return targets or unread-below-viewport conditions that their models own.
func LatestVisible(c *ui.Context, distanceFromBottom float32, loadedBottom bool) bool {
	return !loadedBottom || distanceFromBottom > theme.T(c).Metrics().Navigation().ToLatestShownAfter()
}
