package winshell

import (
	"github.com/egoist/mygo/ui"
)

// nav 画左栏导航：顶上一排和别的栏标题行同高、放 logo（主人 11-06：几栏顶行同高对齐），下面才是导航按钮，
// 只有图标，名字在悬停提示里（照 Bun .su-rail，主人 2026-11-09）；
// Bottom 项（设置）贴栏底。选中是浮起的一块（Lift），悬停和按下各一层，颜色 140ms 动画。
func (k *Kit) nav(c *ui.Context, s *State, sp *Spec) {
	g, p := k.G, k.P
	e := ui.Column(c.Key("navigation")).Width(g.NavWidth).Shrink(0).FillHeight().Background(p.Rail)
	e.Children(func() {
		// 顶行和聊天标题条、搜索框同一条线：logo 的中线和它们的中线重合
		ui.Row(c.Key("nav-header")).Height(g.BarHeight).Shrink(0).Margin(2*g.SpaceSmall, 0, g.SpaceSmall, 0).Justify(ui.Center).AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Image(c.Key("app-logo"), Logo).Size(g.HitSmall, g.HitSmall).Label("Sumika")
		})
		ui.Column(c.Key("nav-body")).FillWidth().Grow(1).MinHeight(0).Padding(g.SpaceTight).Gap(g.SpaceTight).AlignItems(ui.Center).Children(func() {
			spaced := false
			for _, it := range sp.Nav {
				if it.Bottom && !spaced {
					ui.Spacer(c).FillWidth()
					spaced = true
				}
				k.navItem(c, s, sp, it)
			}
		})
	})
	k.surface(c, e, "navigation", "pane", 0, 0)
}

func (k *Kit) navItem(c *ui.Context, s *State, sp *Spec, it NavItem) {
	g, p := k.G, k.P
	selected := sp.Page == it.ID
	b := ui.ButtonBase(c.Key("nav-"+it.ID)).Label(it.Label).Size(g.NavItemSize, g.NavItemSize).Center().Radius(g.NavItemSize / 2).Selected(selected).TextColor(p.Muted).OnClick(func() {
		s.ShowList()
		sp.OnPage(it.ID)
	})
	k.Tip(c, b, it.Label)
	icon := it.Icon
	if selected {
		k.Lift(b).TextColor(p.Ink)
		if it.IconOn != nil {
			icon = it.IconOn
		}
	} else {
		bg := ui.Transparent
		if b.Pressed() {
			bg = p.Press
		} else if b.Hovered() {
			bg = p.Hover
		}
		b.Background(bg)
	}
	b.Transition(SelectFade) // 选中块（底色加阴影）和悬停、按下的底一起 140 毫秒淡变
	b.Children(func() { ui.Icon(c, icon).Size(g.IconSize, g.IconSize) })
	k.surface(c, b, "nav-"+it.ID, "navigation-item", g.NavItemSize/2, 0)
}
