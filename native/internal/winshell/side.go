package winshell

import (
	"strconv"

	"github.com/egoist/mygo/ui"
)

// Member 是右栏“成员”里的一行。
type Member struct {
	Name        string
	AvatarIndex int
	Avatar      *ui.Bitmap
	Square      bool // 角色的圆角方头像
}

// SideSpec 是右侧栏“资料与活动”的内容。活动现在没有后台接口，不造数：
// Activity 有行才画行，没有就只写 ActivityNote（接口缺口的说明），不假装有记录。
type SideSpec struct {
	Title    string // 栏标题，默认“资料与活动”
	Content  func() // 非空时画它，不画默认的活动和成员
	Body     func() // 非空时整块内容区归它（不带滚动和内距）：放虚拟列表用
	Recessed bool   // 右栏底退一档（里面放气泡时）
	// Escape 是浮动面板里按 Esc 时先问一声：返回真就是内容自己处理了（比如退一层），面板不关；空或返回假才关面板。
	Escape func() bool
	// KeepOpen 为真时点面板外面不收起：内容自己开的大弹层（看大图）盖在上面，点的是弹层
	KeepOpen     func() bool
	Tabs         func()     // 顶行小胶囊里的内容（切换下面内容的选项卡）；空就写标题
	TabKey       string     // 现在选的是哪个选项卡：换了下面的内容淡入（规格-过渡动画 D3）
	Facts        []KeyValue // 资料：这个会话是什么（名称、场合、模型）
	Members      []Member
	MembersEmpty string // 没有成员时写什么
	Activity     []string
	ActivityNote string
}

// sideStyle 是右栏这一次怎么摆：并排的栏、窄窗口的整层、浮动面板。
type sideStyle int

const (
	sideColumn sideStyle = iota // 并排：一张铺到窗口边的卡，三个键在顶行里
	sideLayer                   // 窄窗口第三层：整层，有返回钮，三个键在顶行里
	sideFloat                   // 浮动面板：容器由 floatPanel 给，顶行有关闭钮，三个键由浮层里的胶囊负责
)

// side 画右栏：顶行（左边小长胶囊放标题，以后放切换内容的选项卡；右边窗口按钮）+ 滚动区里“活动”“成员”两节。back 为真（窄窗口第三层、抽屉）时标题行有返回钮、铺满；
// 否则是一张浮起的圆角卡，上下右留一个 SpaceSmall 的缝（照 Bun .su-app-side）。
func (k *Kit) side(c *ui.Context, s *State, sp *SideSpec, style sideStyle, onBack func()) {
	g, p := k.G, k.P
	back, float := style == sideLayer, style == sideFloat
	title := sp.Title
	if title == "" {
		title = "资料与活动"
	}
	fill := p.Composer
	if sp.Recessed { // 里面是白气泡：底退一档，气泡才分得出来
		fill = p.Pane
	}
	jump := s.fade.tabSeen && s.fade.tab != sp.TabKey // 换了选项卡：内容区淡入
	s.fade.tab, s.fade.tabSeen = sp.TabKey, true
	body := func() {
		// 顶行和聊天标题条同高同位；上边、右边都和右栏关着时的三个键同一条线
		top := g.SpaceSmall
		if style == sideColumn {
			top = k.windowInset(false) // 铺满时顶行自己离窗口上边这么远，和聊天标题条同一条线（ADR-326）
		}
		right := top + k.windowCapsulePad()
		if float { // 面板离窗口 floatGap，顶行补足到和聊天标题条同一条线；右边让出浮层里那只胶囊
			top = k.windowInset(false) - k.floatGap()
			right = top + k.windowRoom()
		}
		if k.Win == nil {
			right = g.PaneInset
		}
		pillH := g.BarHeight - 2*g.BarInset
		ui.Row(c.Key("side-header")).Height(g.BarHeight).Shrink(0).Margin(top, right, 0, g.PaneInset).Gap(g.SpaceSmall).AlignItems(ui.Center).DragWindow().Children(func() {
			if back {
				k.IconButton(c, "back-chat", "返回聊天", IconBack, onBack)
			}
			inset := float32(0) // 标题自己带左右内距；选项卡是圆按钮，和胶囊同心
			if sp.Tabs != nil {
				inset = (pillH - g.HitSmall) / 2
			}
			pill := ui.Row(c.Key("side-tabs")).Grow(1).MinWidth(0).Height(pillH).Padding(inset).AlignItems(ui.Center).Radius(pillH / 2).Background(p.Track)
			k.surface(c, pill, "side-tabs", "tabs", pillH/2, inset).Children(func() {
				if sp.Tabs != nil {
					sp.Tabs()
					return
				}
				ui.Text(c.Key("side-title"), title).Grow(1).MinWidth(0).Padding(0, g.SpaceMedium).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).FontWeight(g.StrongWeight).SingleLine()
			})
			if !float {
				k.WindowButtons(c)
			}
		})
		if sp.Body != nil {
			k.fadeIn(ui.Column(c.Key("side-body")).Grow(1).MinHeight(0).FillWidth(), jump).Children(sp.Body)
			return
		}
		k.fadeIn(ui.Scroll(c.Key("side-content")).Grow(1).MinHeight(0).Padding(g.PaneInset).Gap(g.PaneInset), jump).Children(func() {
			if sp.Content != nil {
				sp.Content()
				return
			}
			if len(sp.Facts) > 0 {
				k.KeyValues(c, "side-facts", sp.Facts)
			}
			if len(sp.Activity) > 0 || sp.ActivityNote != "" {
				ui.Column(c.Key("activity-section")).FillWidth().Gap(g.SpaceSmall).Children(func() {
					ui.Text(c, "活动").FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).FontWeight(g.MediumWeight).TextColor(p.Muted)
					if len(sp.Activity) == 0 {
						ui.Text(c.Key("activity-empty"), sp.ActivityNote).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Muted)
						return
					}
					for i, line := range sp.Activity {
						k.sideRow(c, "activity-row", i, func() {
							ui.Text(c.Key("activity-summary"), line).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine)
						})
					}
				})
			}
			ui.Column(c.Key("members-section")).FillWidth().Gap(g.SpaceSmall).Children(func() {
				ui.Text(c, "成员").FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).FontWeight(g.MediumWeight).TextColor(p.Muted)
				if len(sp.Members) == 0 && sp.MembersEmpty != "" {
					ui.Text(c.Key("members-empty"), sp.MembersEmpty).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Muted)
				}
				for i, m := range sp.Members {
					k.sideRow(c, "member-row", i, func() {
						if m.Square {
							k.SquareAvatar(c, "member-"+m.Name, m.Name, m.AvatarIndex, g.AvatarSize, m.Avatar)
						} else {
							k.Avatar(c, "member-"+m.Name, m.Name, m.AvatarIndex, g.AvatarSize, m.Avatar)
						}
						ui.Text(c, m.Name).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).SingleLine().Grow(1).MinWidth(0)
					})
				}
			})
		})
	}
	switch {
	case back:
		k.surface(c, ui.Column(c.Key("side")).Fill().Background(p.Pane), "side", "pane", 0, g.SpacePanel).Children(body)
		return
	case float: // 底和玻璃由 floatPanel 给
		ui.Column(c.Key("side")).Fill().Children(body)
		return
	}
	// 铺满，不留缝（ADR-326）
	ui.Box(c.Key("side-slot")).Fill().Children(func() {
		card := ui.Column(c.Key("side")).Fill().Clip().Background(fill)
		k.surface(c, card, "side", "card", 0, 0).Children(body)
	})
}

// SideTabs 是右栏顶行小胶囊里的文字选项卡：选中的一块底，和胶囊同心。返回这一帧换了没有。
func (k *Kit) SideTabs(c *ui.Context, key string, value *int, names []string) (changed bool) {
	g, p := k.G, k.P
	parts := ui.SegmentedBase(c.Key(key), value, len(names))
	before := *value
	parts.Track.Grow(1).MinWidth(0).Gap(g.SpaceMicro).Children(func() {
		for i, name := range names {
			b := parts.Segment(i).Label(name).Grow(1).Height(g.HitSmall).Center().Radius(g.HitSmall / 2).TextColor(p.Muted)
			b.Transition(SelectFade) // 选中块、悬停、按下的底和字色 140 毫秒淡变，和导航、会话行同一族（规格 B8）
			switch {
			case i == *value:
				k.selectBg(b, selMode).TextColor(p.Ink)
			case b.Pressed():
				b.Background(p.Press)
			case b.Hovered():
				b.Background(p.Hover)
			}
			b.Children(func() {
				ui.Text(c, name).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).Padding(0, g.ControlInset).SingleLine()
			})
			k.surface(c, b, key+strconv.Itoa(i), "segment", g.HitSmall/2, 0)
		}
	})
	return *value != before
}

func (k *Kit) sideRow(c *ui.Context, key string, i int, children func()) {
	g := k.G
	e := ui.Row(c.Key(key + "-" + strconv.Itoa(i))).FillWidth().MinHeight(g.AvatarSize).Gap(g.SpaceSmall).AlignItems(ui.Center).Children(children)
	k.surface(c, e, key, "side-row", 0, 0)
}
