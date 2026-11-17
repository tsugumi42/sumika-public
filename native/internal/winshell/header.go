package winshell

import (
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// HeaderSpec 是聊天标题栏的内容。副标题是会话状态（后台有就给，没有就空着），不编文案。
type HeaderSpec struct {
	Title, Subtitle string
	Notice          string // 出了错或在等什么：占副标题的位置，用危险色
	AvatarIndex     int
	Avatar          *ui.Bitmap
	Glow            chatui.OKLCH // 聊天背景图案里的角色色光晕
	SideLabel       string       // 右栏钮的读屏名和悬停提示
	SideActive      bool         // 右栏开着
	// 上下文用量（ADR-351）：画成头像外圈的环。UsageOK 为假（没请求过、窗口不知道）时不画环，不画假的 0%。
	Usage     float32 // 0～1
	UsageOK   bool
	UsageHint string // 悬停提示，比如“上下文用了 62%（8.1 万 / 13 万）”
	OnAvatar  func() // 点头像：打开右栏的“上下文”栏
}

// ChatLayout 是聊天区给里面的消息列和输入区的几何：版心居中、两边内距、顶上让出标题栏、底下让出输入区。
type ChatLayout struct {
	Column     float32 // 消息列宽（版心，最宽 752）
	Horizontal float32 // 版心左右到聊天区边的距离
	Inset      float32 // 输入区到聊天区底边和右边的内距
	Top        float32 // 标题栏高加一个内距：消息列顶上让出这么多（标题栏浮在上面，消息从下面透过去）
	Bottom     float32 // 输入区高加内距：消息列底边让出这么多
	Narrow     bool    // 窄窗口（三层），聊天层里没有导航
}

// header 画聊天标题栏：一条浮在消息列上面的玻璃圆角条（设计语言第二节第 2 层，照 Bun .su-chat-bar），四周离聊天窗 l.Inset，
// 消息滚到下面能透出来。头像、名字和状态两行靠左；窄窗口最左边多一个返回箭头。右栏没并排时，右边让给窗口按钮胶囊。
func (k *Kit) header(c *ui.Context, s *State, h *HeaderSpec, l ChatLayout, onBack, onSide func()) {
	g, p := k.G, k.P
	w, _ := c.Size()
	right := l.Inset
	if !s.sideShownRight(k, w) {
		right += k.windowRoom()
	}
	fill := ui.Box(c.Key("chat-header-fill")).Absolute().Left(l.Inset).Right(right).Top(l.Inset).Height(g.BarHeight).Radius(g.RadiusBar)
	k.Glass(fill, LevelHeader, p.Canvas.Alpha(OverlayAlpha))
	k.CardShadow(fill)
	e := ui.Row(c.Key("chat-header")).Absolute().Left(l.Inset).Right(right).Top(l.Inset).Height(g.BarHeight).Padding(g.BarInset).Gap(g.SpaceSmall).AlignItems(ui.Center).DragWindow()
	e.Children(func() {
		if l.Narrow {
			k.headerIconButton(c, "back-list", "返回会话列表", IconBack, onBack)
		}
		// 头像圆角和浮条同心
		k.headerAvatar(c, h, g.BarHeight-2*g.BarInset, g.RadiusBar-g.BarInset)
		status, ink := h.Subtitle, p.Muted
		if h.Notice != "" {
			status, ink = h.Notice, p.Danger
		}
		ui.Column(c.Key("chat-heading")).Grow(1).MinWidth(0).Gap(0).Children(func() {
			ui.Text(c.Key("chat-title"), h.Title).FontSize(g.LabelSize).FontWeight(g.BoldWeight).FixedLineHeight(g.BarLine).SingleLine()
			// 状态字换了就淡入（规格-过渡动画 C21）：字按内容起 key，新的一条进场、旧的一条淡出；外面这一格一直在，进场才播得出来
			ui.Column(c.Key("chat-status-slot")).MinWidth(0).Children(func() {
				if status != "" {
					ui.Text(c.Key(statusKey{status}), status).FontSize(g.MetaSize).FixedLineHeight(g.BarLine).TextColor(ink).MinWidth(0).SingleLine().
						Transition(StatusTransition)
				}
			})
		})
		label := h.SideLabel
		if label == "" {
			label = "资料与活动"
		}
		k.headerIconButton(c, "side-open", label, IconSide, onSide)
	})
	k.surface(c, e, "chat-header", "header", g.RadiusBar, g.BarInset)
}

// headerIconButton 是聊天标题浮条里的圆形图标钮（窄窗口的返回、右栏开关同一族）：浮条是半透明玻璃，
// 悬停、按下用凹块的两档底，比 IconButton 的叠色更看得出来；有读屏名和悬停提示。
func (k *Kit) headerIconButton(c *ui.Context, key, label string, icon *ui.SVG, fn func()) {
	g, p := k.G, k.P
	b := ui.ButtonBase(c.Key(key)).Label(label).Size(g.HitSmall, g.HitSmall).Center().Radius(g.HitSmall / 2).TextColor(p.Ink).Shrink(0).OnClick(fn)
	bg := ui.Transparent
	if b.Pressed() {
		bg = p.Selected
	} else if b.Hovered() {
		bg = p.Bubble
	}
	b.Background(b.AnimateColor("fill", bg, ControlFade)).Children(func() { ui.Icon(c, icon).Size(g.IconSize, g.IconSize) })
	k.Tip(c, b, label)
	k.pressScale(c, b, k.T.D.Num.ScalePressIcon, false)
	k.surface(c, b, key, "icon-button", g.HitSmall/2, 0)
}

// sideShownRight 说右栏现在是不是并排在聊天右边（三栏）。
func (s *State) sideShownRight(k *Kit, w float32) bool {
	return k.SideMode(w) == PanelPush && s.Panes.SideOpen
}

// sideOwnsButtons 说窗口三个键这一帧由右栏那边画：并排时在右栏顶行里；浮动面板在时在同一个浮层里（位置和聊天区那只胶囊重合，不跳）。
func (s *State) sideOwnsButtons(k *Kit, w float32) bool {
	return s.sideShownRight(k, w) || s.float.live
}

// headerAvatar 是标题栏的头像，外面套一圈上下文用量的环（ADR-351、规格-输入框胶囊和会话信息 第二节第 2 条）。
// 头像元素保持原大（标题栏的布局和几何检查不变），环直接画在它外面那一圈内距里；环和头像同心（环的圆角 = 头像圆角 + 缝）。
// 用量变了（比如压缩后掉下去）环用 motion.panel 平滑走过去，不回弹。
func (k *Kit) headerAvatar(c *ui.Context, h *HeaderSpec, size, radius float32) {
	if !h.UsageOK && h.OnAvatar == nil {
		k.avatarShaped(c, "header-avatar", h.Title, h.AvatarIndex, size, radius, h.Avatar)
		return
	}
	g, p := k.G, k.P
	line, gap := g.SpaceMicro, g.SpaceMicro // 环线宽和离头像的缝都是 s0-5（2），合起来不超过标题浮条的内距
	out := line + gap
	label := "这个对话的信息"
	if h.UsageHint != "" {
		label = h.UsageHint
	}
	b := ui.ButtonBase(c.Key("header-avatar-ring")).Label(label).Size(size, size).Shrink(0).Radius(radius)
	k.Tip(c, b, label)
	if h.OnAvatar != nil {
		b.OnClick(h.OnAvatar)
	}
	if h.UsageOK {
		v := b.AnimateTo("context-usage", max(0, min(h.Usage, 1)), ui.Animation{Duration: MotionRoute, Ease: RouteMotion.Ease})
		ink := p.Accent
		if h.Usage >= contextUsageWarn {
			ink = p.Danger
		}
		ink = b.AnimateColor("ring-ink", ink, ControlFade) // 过了 80% 变危险色也淡过去
		track, r := p.Line, radius+out
		b.DrawOver(func(pt *ui.Painter, rc ui.Rect) {
			x, y, w, hh, rr := rc.X-out+line/2, rc.Y-out+line/2, rc.W+2*out-line, rc.H+2*out-line, r-line/2
			pt.StrokePath(ringPath(x, y, w, hh, rr), line, track)
			if v > 0 {
				pt.StrokePath(ringPath(x, y, w, hh, rr).Trim(0, v), line, ink)
			}
		})
	}
	b.Children(func() { k.avatarShaped(c, "header-avatar", h.Title, h.AvatarIndex, size, radius, h.Avatar) })
}

// contextUsageWarn 是上下文环变成危险色的用量（80%：再长就要压缩了）。
const contextUsageWarn = 0.8

// ringPath 是一个圆角矩形的轮廓，从上边正中开始顺时针走一圈（Trim 从 12 点钟方向开始长）。
func ringPath(x, y, w, h, r float32) *ui.Path {
	const k = 0.5522847498 // 四分之一圆的三次贝塞尔近似
	r = max(0, min(r, w/2, h/2))
	p := new(ui.Path)
	p.MoveTo(x+w/2, y)
	p.LineTo(x+w-r, y)
	p.CubeTo(x+w-r+r*k, y, x+w, y+r-r*k, x+w, y+r)
	p.LineTo(x+w, y+h-r)
	p.CubeTo(x+w, y+h-r+r*k, x+w-r+r*k, y+h, x+w-r, y+h)
	p.LineTo(x+r, y+h)
	p.CubeTo(x+r-r*k, y+h, x, y+h-r+r*k, x, y+h-r)
	p.LineTo(x, y+r)
	p.CubeTo(x, y+r-r*k, x+r-r*k, y, x+r, y)
	p.LineTo(x+w/2, y)
	return p
}

// statusKey 是标题栏状态字按内容起的 key：内容换了就是新的一条，旧的走出场。
type statusKey struct{ s string }
