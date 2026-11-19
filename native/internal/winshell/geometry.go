// Package winshell 是新界面的窗口外壳：导航、会话列表、标题栏、右侧栏、窄窗三层和抽屉。
// 组件、令牌和几何都来自原型 cmd/sumika-proto（ADR-283～285），数据与动作全从参数进，不碰后台类型。
// 依赖方向写死：只依赖 mygo 和 internal/chatui，不许依赖 MewTypeUI（archcheck 卡着）。
package winshell

import (
	"math"
	"os"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/chatui"
	"sumika-native/internal/design"
	"sumika-native/internal/design/colorx"
)

// 分栏常量照 Telegram，不随外观缩放（令牌 layout.*、opacity.*；design 的 TestLayoutConstants 钉着两边一致）。
const (
	SplitSize          float32 = 1
	ListMin            float32 = 260 // 会话列表最小
	ListDefault        float32 = 300 // 没拖过时的列表宽（照 Bun 版的窄列表，主人 2026-11-09 同意照 Bun 补）
	ListMax            float32 = 540 // 拖动上限
	ListCompactDrag    float32 = 130 // 拖到比这窄就收成头像模式
	ListGrab           float32 = 10  // 列表分隔条的抓取宽度，以分隔线为中心，不占布局宽度（mygo 自带的只有 6）
	ChatMin            float32 = 380 // 聊天区最小
	SideDefault        float32 = 320
	SideMin            float32 = 292
	SideMax            float32 = 392
	NarrowContentWidth float32 = 520
	ScrimAlpha         float32 = .2
	ContentWidth       float32 = 752
	SettingsWidth      float32 = 756
	SettingsListWidth  float32 = 275
	OverlayAlpha       float32 = .92
	DisabledAlpha      float32 = .5
	// 玻璃的叠色浓度：标题栏 0.2、输入框 0.35（原型 glassOr 的说明）
	GlassHeader, GlassComposer float32 = .2, .35
	// 浮层（菜单、下拉、对话框）的玻璃叠色：上面全是字，比标题栏、输入框实得多（HIG 的常规玻璃）
	GlassFloat float32 = .7
)

// 动效时长和曲线不随外观变，取令牌的 motion.fast、motion.panel 和 ease.*（换层 250 → 230 是主人 2026-11-10 看过并排图后定的）。
// 下面所有动效量都由 motionscale.go 的 newMotionSet 一处算出（慢放倍数只在那里乘），这里只是按名字取出来用。
var (
	base        = design.Derive(design.DefaultSettings())
	mo          = newMotionSet(1)
	MotionFast  = mo.fast
	MotionRoute = mo.route
)

// Geometry 是随外观（字号、圆角、疏密）缩放的尺寸、字号和字重。
// 有令牌的量全部从 internal/design 取（规格-设计令牌 第十节第 3 步，薄壳）；还没进令牌的组件量照旧按比例算，
// 比例以默认设置为 1：× 字号/16，间距类 × 疏密/8 × 字号/16，圆角类 × 圆角/12 × 字号/16。
type Geometry struct {
	SpaceMicro, SpaceTight, SpaceSmall, SpaceMedium, SpacePanel, SpaceSection, SpaceLarge float32
	NavInset, PaneInset, ControlInset, FieldInset, BadgeInset                             float32
	RadiusPanel, RadiusNavItem, RadiusControl, RadiusSegment                              float32
	RadiusComposer, RadiusSend                                                            float32
	BarInset, BarHeight, RadiusBar, RadiusCard, RadiusRow, RadiusAvatar, BarLine          float32
	NavItemSize                                                                           float32
	RetryInk                                                                              float32
	HeaderHeight, HitSmall, HitRegular, RowHeight, AvatarSize                             float32
	ConversationAvatar, ConversationInset, ListInset, ListCompactWidth                    float32
	SearchHeight, SearchInset, CompactBadge                                               float32
	SwitchThumbSize, IconSize, IconSmall, NavItemHeight, SendSize, ComposerMin            float32
	ControlWide, ValueWidth                                                               float32
	// 设置页的分组面（SettingGroup）：面最宽 640（让名字和右边的控件一眼连上，窗口宽了也靠左不拉满）；
	// 一行左右留 SettingsInsetX；下拉入口定宽 SettingsSelectWidth（不随选中的字变宽）；“改过”的小圆点直径 SettingsDot。
	SettingsGroupWidth, SettingsInsetX, SettingsSelectWidth, SettingsDot float32
	BodySize, BodyLine, LabelSize, LabelLine, MetaSize, MetaLine         float32
	ConversationLine, ConversationSlot, TitleSize, TitleLine             float32
	NormalWeight, MediumWeight, StrongWeight, BoldWeight                 int
	NavWidth, BreakpointNarrow, BreakpointWide                           float32
}

// NewGeometry 按外观令牌重算整套几何。
func NewGeometry(t chatui.Tokens) Geometry {
	var g Geometry
	m := t.D.Dim
	kt := t.S.Text / chatui.TextDefault
	kd := t.S.Density / chatui.DensityDefault * kt
	kr := t.S.Radius / chatui.RadiusDefault * kt
	g.SpaceMicro, g.SpaceTight, g.SpaceSmall, g.SpaceMedium = m.SpaceS0Half, m.SpaceS1, m.SpaceS2, m.SpaceS3
	g.SpacePanel, g.SpaceSection, g.SpaceLarge = m.SpaceS4, m.SpaceS5, m.SpaceS6
	g.NavInset, g.PaneInset = g.SpaceMedium, g.SpaceMedium
	g.BodySize, g.BodyLine, g.LabelSize, g.LabelLine = m.FontSize4, m.FontLineBody, m.FontSize3, m.FontLineUi
	g.MetaSize, g.MetaLine, g.TitleSize, g.TitleLine = m.FontSize2, m.FontLineMeta, m.FontSize6, m.FontLineTitle
	// 控件高三档是 32 / 40 / 48（主人 2026-11-10）：小控件 = sm，常规 = md。
	// 临时：输入框、下拉这类单行字段还是 36（不在三档里，主人没看过它变 40 的样子），第 6 步迁到组件层时定。
	g.HitSmall, g.HitRegular, g.RowHeight = m.SizeCtlSm, m.SizeCtlMd, 36*kt
	g.HeaderHeight = g.HitSmall + 2*g.PaneInset // 56：一个控件高加上下内距
	// 导航栏宽是 64（只有图标，照 Bun --w-rail，主人 2026-11-09），不随字号缩放：缩放会让整页横向挪动，
	// 拖字号滑块时滑块从手底下跑掉（主人 2026-11-08）。只在导航项加两边留缝装不下时才撑宽（临时：令牌的断点还不含这种撑宽，第 6 步定）。
	g.NavItemSize = 44 * kt
	g.NavWidth = max(m.LayoutNav, g.NavItemSize+2*g.SpaceTight)
	g.BreakpointNarrow = g.NavWidth + m.LayoutListMin + m.LayoutSplit + m.LayoutChatMin
	g.BreakpointWide = g.BreakpointNarrow + m.LayoutSplit + m.LayoutSideMin
	g.AvatarSize = 32 * kt
	g.SwitchThumbSize, g.IconSize, g.NavItemHeight, g.RetryInk = 24*kt, m.SizeIconMd, 56*kt, 16*kt
	g.ComposerMin = chatui.ComposerMinHeight(t) // 胶囊输入框的高（ADR-351），外壳按它预留消息列底下的位置
	g.IconSmall = m.SizeIconSm
	g.ControlWide, g.ValueWidth = 280*kt, 40*kt
	g.SettingsGroupWidth, g.SettingsSelectWidth, g.SettingsDot = 640*kt, 160*kt, 6*kt
	g.SettingsInsetX = g.SpaceMedium
	g.ConversationAvatar, g.ConversationInset = 46*kt, g.SpaceSmall
	if os.Getenv("DL_AVATAR") == "40" { // 临时：设计专场并排图
		g.ConversationAvatar = 40 * kt
	}
	if os.Getenv("DL_FIELD") == "40" { // 临时：设计专场并排图
		g.RowHeight = m.SizeCtlMd
	}
	g.ListInset = g.ConversationInset
	g.ListCompactWidth = g.ConversationAvatar + 2*g.ConversationInset
	g.SearchHeight = g.HeaderHeight - g.SpaceSmall // 搜索框和聊天标题条同高、同一条线
	g.SearchInset = (g.SearchHeight - g.MetaLine) / 2
	g.CompactBadge = g.MetaLine
	g.ConversationLine, g.ConversationSlot = g.MetaLine, 2*g.MetaLine
	g.ControlInset, g.FieldInset = (g.HitSmall-g.MetaLine)/2, (g.RowHeight-g.MetaLine)/2
	g.BadgeInset = g.ControlInset
	g.SendSize = g.HitSmall
	g.RadiusSend = m.SendRadius
	g.RadiusNavItem = min(16*kr, (max(72, g.HitSmall+2*g.PaneInset)-2*g.NavInset)/2, g.NavItemHeight/2) // 只有原型的带字导航还用它
	// 控件圆角不另封顶（主人 2026-11-10）：单行的东西按高度一半画成胶囊，已经兜住圆角滑块拉满的情形。
	g.RadiusControl = m.RadiusCtl
	g.RadiusComposer = min(28*kr, g.ComposerMin/2)
	g.RadiusSegment = m.SegmentIndicatorRadius
	g.RadiusPanel = m.PanelRadius
	// 浮动层次（照 Bun 版，主人 2026-11-09 定）：每包一层圆角加一层内距，同心。从发送键往外推：
	// 键半径 + 输入框内距（含 1 的描边）= 输入框圆角，再加离聊天窗的缝 = 聊天窗圆角；标题栏浮条同样离窗一个缝，高度装不下时是整颗胶囊。
	g.BarInset = 6 * kd
	g.BarHeight = g.HeaderHeight - g.SpaceSmall // 标题栏浮条：上边留一个 SpaceSmall，下边缘和别栏顶行对齐
	g.RadiusAvatar = g.RadiusControl
	g.RadiusRow = g.RadiusControl + g.BarInset
	g.RadiusCard = g.SpaceSmall + 1 + chatui.ComposerSend(t)/2 + g.SpaceSmall
	g.RadiusBar = min(g.RadiusCard-g.SpaceSmall, g.BarHeight/2)
	g.BarLine = (g.BarHeight - 2*g.BarInset) / 2
	g.NormalWeight, g.MediumWeight, g.StrongWeight, g.BoldWeight = t.Weight[0], t.Weight[1], t.Weight[2], t.Weight[3]
	return g
}

// Palette 是窗口外壳用的颜色（原型 palette）；全部从 chatui 令牌推出。
type Palette struct {
	Content, Group                                                                                                            ui.Color // 设置页内容区的底（最亮一档）和分组面的底（比内容区深一档，和分类栏同色）
	Canvas, Rail, Pane, Bubble, Composer, Ink, Muted, Meta, Accent, OnAccent, Self, OnSelf, GroupUser, Selected, Danger, Line ui.Color
	AccentHover, AccentPress                                                                                                  ui.Color // 主按钮悬停、按下：角色色阶梯的 hover、press
	Hover, Press                                                                                                              ui.Color // 悬停、按下：正文色 6%、11% 的叠色
	// Track 是“平的”那档的底：分段轨道、没选中的筛选片、右栏页签的胶囊、徽标、标题栏图标钮的悬停。
	// 现在和凹块底（Bubble，令牌 color.sunken）同色；分开放是为了两档能各自调（2026-11-17 体检：凹和平分不出来，一半原因是共用一个色）。
	Track ui.Color
	// Edge 是认出控件要靠的那条边、那段轨道：关着的开关的轨道、滑块没填满的那段和滑钮的描边。
	// 从深一档的细线色出发只调明度，取对页面底、内容区都刚好过 3:1 的一档（WCAG 2 1.4.11；设计语言第 117 行“控件边框 3”）。
	Edge ui.Color
	// Well 是“凹下去”那档的底：一层半透明的暗色（浅色用正文色、深色用黑），不透明度取“盖在页面底上明度正好降 WellDepth”的一档。
	// 放到白卡片上也只比卡片深差不多同样多；以前用实色的凹块底，在页面底上几乎看不出、在白卡片上又显得特别深
	// （2026-11-17 体检 A，主人在画廊对比小样里挑定 A5“相对凹”）。
	Well                            ui.Color
	Strong, OnStrong, OnStrongMuted ui.Color    // 选中行：强调色解出的深档 + 白字
	Avatar, AvatarInk               [8]ui.Color // 首字头像：角色色实底和它上面的字色（照 Bun .su-avatar-face）
}

// NewPalette 从 chatui 令牌推出外壳的颜色。等 fix/proto-palette 的主题预设合进来，只在这里换来源。
func NewPalette(t chatui.Tokens) Palette {
	c := t.C
	acc := t.AccentOf(chatui.DefaultChar)
	p := Palette{Content: c.Surface, Group: c.Bg, Canvas: c.Bg, Rail: c.Rail, Pane: c.Bg, Bubble: c.Sunken, Composer: c.Surface, Ink: c.Text, Muted: c.Text2, Meta: c.Text3,
		Accent: acc.Base, OnAccent: acc.On, Self: acc.Soft, OnSelf: c.Text, GroupUser: c.Text, Selected: c.SunkenHover, Danger: c.Danger, Line: c.Line, Hover: c.Hover, Press: c.Press,
		Strong: acc.Strong, OnStrong: acc.OnStrong, OnStrongMuted: acc.OnStrongMuted, AccentHover: acc.Hover, AccentPress: acc.Press, Track: c.Sunken, Edge: controlEdge(t),
		Well: wellOf(t)}
	for i := range p.Avatar {
		a := t.AccentOf(chatui.CharColors[i%len(chatui.CharColors)])
		p.Avatar[i], p.AvatarInk[i] = a.Face, a.On
	}
	return p
}

// WellDepth 是凹槽比它所在的面暗多少（OKLab 明度 × 100 的 4，亮暗一样）。主人 2026-11-17 在对比小样里拖滑块看过，默认 4。
const WellDepth = .04

// wellOf 算 Palette.Well：ink 盖在页面底上明度正好降 WellDepth 的那个不透明度（二分）。
func wellOf(t chatui.Tokens) ui.Color {
	c := t.C
	ink := c.Text
	if t.S.Dark {
		ink = ui.RGB(0, 0, 0)
	}
	target := colorx.FromColor(c.Bg).L - WellDepth
	lo, hi := 0.0, 1.0
	for range 30 {
		mid := (lo + hi) / 2
		if colorx.FromColor(colorx.Over(ink, mid, c.Bg)).L > target {
			lo = mid
		} else {
			hi = mid
		}
	}
	ink.A = uint8(math.Round(hi * 255))
	return ink
}

// controlEdge 算 Palette.Edge：浅色往深调、深色往浅调。
func controlEdge(t chatui.Tokens) ui.Color {
	c, dir := t.C, -1.0
	if t.S.Dark {
		dir = 1
	}
	o, _ := colorx.Reach(colorx.FromColor(c.LineStrong), dir, chatui.MinIconContrast, c.Bg, c.Surface)
	return o.Color()
}

// NewTheme 是 mygo 的主题（给没有自己画的控件、焦点环和读屏用）；由 design.Theme 构造（规格 7.5）。
func NewTheme(t chatui.Tokens, p Palette, font string) ui.Theme {
	_ = p // 颜色和外壳调色板同源（都来自 t.D），参数留着不改调用方
	return design.Theme(t.D, t.D.AccentOf(chatui.DefaultChar.Design()), font)
}

// 控件、换层、抽屉、弹层、下拉的动效（说明写在 motionscale.go 的 newMotionSet 里，和原来一字不差的取值）。
var (
	ControlMotion  = mo.controlMotion
	ControlFade    = mo.controlFade
	RouteMotion    = mo.routeMotion
	SizeMotion     = mo.sizeMotion // 宽高、进度条：“利落”弹簧，不过冲
	PanelOutMotion = mo.panelOut
	PanelFade      = mo.panelFade
	PanelFadeOut   = mo.panelFadeOut
	SelectFade     = mo.selectFade
	TipTransition  = mo.tip
	// StatusTransition 是标题栏状态字换内容：140 毫秒淡入、淡出，不位移（规格-过渡动画 C21）。
	StatusTransition = mo.status
	ContentFadeAnim  = mo.contentFade
	DropdownGrow     = mo.ddGrow
	DropdownShrink   = mo.ddShrink
)

// popTransition 是进出场用同一个起点的 Transition：in 进、out 出。
// Transition 不选 Position / Size / Colors / Shadows 时它们全都会动画，所以钉上 Shadows：这些元素本身没有阴影变化，
// 等于只留进出场，面板跟着触发钮、窗口或分隔条挪的时候不被缓动拖着走（拖分栏要跟手，规格 铁律 1）。
func popTransition(in, out time.Duration, m ui.Motion) ui.ElementTransition {
	return chatui.PopTransition(in, out, m)
}
