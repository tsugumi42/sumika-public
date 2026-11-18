package winshell

import (
	"github.com/egoist/mygo/ui"
)

// nav 画左栏导航：顶上一排和别的栏标题行同高、放 logo（主人 11-06：几栏顶行同高对齐），下面才是导航按钮，
// 只有图标，名字在悬停提示里（照 Bun .su-rail，主人 2026-11-09）；
// Bottom 项（设置）贴栏底。选中是浮起的一块（Raised），悬停和按下各一层，颜色 140ms 动画。
func (k *Kit) nav(c *ui.Context, s *State, sp *Spec) {
	g, p := k.G, k.P
	e := ui.Column(c.Key("navigation")).Width(g.NavWidth).Shrink(0).FillHeight().Background(p.Rail)
	e.Children(func() {
		// 顶行和聊天标题条、搜索框同一条线：logo 的中线和它们的中线重合
		ui.Row(c.Key("nav-header")).Height(g.BarHeight).Shrink(0).Margin(2*g.SpaceSmall, 0, g.SpaceSmall, 0).Justify(ui.Center).AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Image(c.Key("app-logo"), Logo).Size(g.HitSmall, g.HitSmall).Label("Sumika")
		})
		body := ui.Column(c.Key("nav-body")).FillWidth().Grow(1).MinHeight(0).Padding((g.NavWidth - g.NavItemSize) / 2).Gap(g.SpaceTight).AlignItems(ui.Center)
		k.navButtons(c, "nav", body, sp.Nav, sp.Page, func(id string) {
			s.ShowList()
			sp.OnPage(id)
		})
	})
	k.surface(c, e, "navigation", "pane", 0, 0)
}

// navButtons 在 body（一列）里排导航图标：Bottom 项贴底；选中的圆是独立的一块，换页时在图标之间上下滑（SlideIndicator）。
func (k *Kit) navButtons(c *ui.Context, key string, body ui.Element, items []NavItem, page string, onPage func(id string)) {
	g := k.G
	st := k.slides("nav/" + key)
	own := k.SlideIndicator(c, body, st, page, SlideOptions{Radius: g.NavItemSize / 2})
	body.Children(func() {
		spaced := false
		for _, it := range items {
			if it.Bottom && !spaced {
				ui.Spacer(c).FillWidth()
				spaced = true
			}
			k.navItem(c, st, own, it, page == it.ID, onPage)
		}
	})
}

// NavRail 是一小列导航（图标按钮，名字在悬停提示里），画廊里单独摆也用它；正式界面的导航栏是它加上 logo 行。
func (k *Kit) NavRail(c *ui.Context, key string, items []NavItem, page string, onPage func(id string)) {
	g, p := k.G, k.P
	body := ui.Column(c.Key(key)).Width(g.NavWidth).Padding(g.SpaceTight, 0).Gap(g.SpaceTight).AlignItems(ui.Center).Background(p.Rail).Radius(g.SpaceMedium)
	k.navButtons(c, key, body, items, page, onPage)
}

func (k *Kit) navItem(c *ui.Context, st *SlideState, own bool, it NavItem, selected bool, onPage func(id string)) {
	g, p := k.G, k.P
	b := ui.ButtonBase(c.Key("nav-"+it.ID)).Label(it.Label).Size(g.NavItemSize, g.NavItemSize).Center().Radius(g.NavItemSize / 2).Selected(selected).TextColor(p.Muted).OnClick(func() { onPage(it.ID) })
	b.Bind(st.Item(it.ID))
	k.Tip(c, b, it.Label)
	icon := it.Icon
	ink := p.Muted
	st.FillKey(it.ID, selected) // 选中的帧里记一笔，取消选中后换新的底色通道
	if selected {
		ink = p.Ink
		if own {
			k.selectBg(b, selMode) // 第一帧还没量到位置：先自己上和选中块同色同圆角的底
		} else {
			b.Background(ui.Transparent) // 圆由导航栏画；不写的话按钮基座会按“选中”自己上一层主题色
		}
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
		b.Background(b.AnimateColor(st.FillKey(it.ID, false), bg, ControlFade)) // 悬停、按下的底 140 毫秒淡变；不挂 Transition，免得刚取消选中的底又淡一遍
	}
	b.TextColor(b.AnimateColor("ink", ink, ControlFade))
	b.Children(func() { ui.Icon(c, icon).Size(g.IconSize, g.IconSize) })
	k.surface(c, b, "nav-"+it.ID, "navigation-item", g.NavItemSize/2, 0)
}
