// Package winshell 是新界面的窗口外壳：导航、会话列表、标题栏、右侧栏、窄窗三层和抽屉。
// 组件、令牌和几何都来自原型 cmd/sumika-proto（ADR-283～285），数据与动作全从参数进，不碰后台类型。
// 依赖方向写死：只依赖 mygo 和 internal/chatui，不许依赖 MewTypeUI（archcheck 卡着）。
package winshell

import (
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/chatui"
	"sumika-native/internal/design"
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
var (
	base        = design.Derive(design.DefaultSettings())
	MotionFast  = base.Motion.MotionFast
	MotionRoute = base.Motion.MotionPanel
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
	BodySize, BodyLine, LabelSize, LabelLine, MetaSize, MetaLine                          float32
	ConversationLine, ConversationSlot, TitleSize, TitleLine                              float32
	NormalWeight, MediumWeight, StrongWeight, BoldWeight                                  int
	NavWidth, BreakpointNarrow, BreakpointWide                                            float32
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
	g.ConversationAvatar, g.ConversationInset = 46*kt, g.SpaceSmall
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
	Canvas, Rail, Pane, Bubble, Composer, Ink, Muted, Meta, Accent, OnAccent, Self, OnSelf, GroupUser, Selected, Danger, Line ui.Color
	AccentHover, AccentPress                                                                                                  ui.Color    // 主按钮悬停、按下：角色色阶梯的 hover、press
	Hover, Press                                                                                                              ui.Color    // 悬停、按下：正文色 6%、11% 的叠色
	Strong, OnStrong, OnStrongMuted                                                                                           ui.Color    // 选中行：强调色解出的深档 + 白字
	Avatar, AvatarInk                                                                                                         [8]ui.Color // 首字头像：角色色实底和它上面的字色（照 Bun .su-avatar-face）
}

// NewPalette 从 chatui 令牌推出外壳的颜色。等 fix/proto-palette 的主题预设合进来，只在这里换来源。
func NewPalette(t chatui.Tokens) Palette {
	c := t.C
	acc := t.AccentOf(chatui.DefaultChar)
	p := Palette{Canvas: c.Bg, Rail: c.Rail, Pane: c.Bg, Bubble: c.Sunken, Composer: c.Surface, Ink: c.Text, Muted: c.Text2, Meta: c.Text3,
		Accent: acc.Base, OnAccent: acc.On, Self: acc.Soft, OnSelf: c.Text, GroupUser: c.Text, Selected: c.SunkenHover, Danger: c.Danger, Line: c.Line, Hover: c.Hover, Press: c.Press,
		Strong: acc.Strong, OnStrong: acc.OnStrong, OnStrongMuted: acc.OnStrongMuted, AccentHover: acc.Hover, AccentPress: acc.Press}
	for i := range p.Avatar {
		a := t.AccentOf(chatui.CharColors[i%len(chatui.CharColors)])
		p.Avatar[i], p.AvatarInk[i] = a.Face, a.On
	}
	return p
}

// NewTheme 是 mygo 的主题（给没有自己画的控件、焦点环和读屏用）；由 design.Theme 构造（规格 7.5）。
func NewTheme(t chatui.Tokens, p Palette, font string) ui.Theme {
	_ = p // 颜色和外壳调色板同源（都来自 t.D），参数留着不改调用方
	return design.Theme(t.D, t.D.AccentOf(chatui.DefaultChar.Design()), font)
}

// ControlMotion 和 RouteMotion 是控件状态与换层的动画（原型 controlMotion、routeMotion）；ControlFade 是同样快慢的颜色、透明度淡变，
// 带 NonMotion：系统要求减少动态效果时位移、旋转直接到位，AnimateTo 做的透明度淡变照常（ADR-124）。AnimateColor 在 mygo 里本来就
// 不受减少动态影响，颜色也用 ControlFade 只是为了一眼看出“这是淡变”。
var (
	ControlMotion = ui.Animation{Duration: MotionFast, Ease: ui.CubicBezier(base.Curve.EaseStandard[0], base.Curve.EaseStandard[1], base.Curve.EaseStandard[2], base.Curve.EaseStandard[3])}
	ControlFade   = ui.Animation{Duration: MotionFast, Ease: ControlMotion.Ease, NonMotion: true}
	RouteMotion   = ui.Animation{Duration: MotionRoute, Ease: ui.CubicBezier(base.Curve.EaseRoute[0], base.Curve.EaseRoute[1], base.Curve.EaseRoute[2], base.Curve.EaseRoute[3])}
)

// PanelOutMotion 是抽屉、右栏退场：200 毫秒（令牌 motion.panel-out），进场仍是 RouteMotion 的 230；曲线暂同进场（退场曲线三选一等主人定）。
// PanelFade 是抽屉的透明度：进出都淡，带 NonMotion，减少动态效果时位移直接到位、淡变还在。
var (
	PanelOutMotion = ui.Animation{Duration: base.Motion.MotionPanelOut, Ease: RouteMotion.Ease}
	PanelFade      = ui.Animation{Duration: MotionRoute, Ease: RouteMotion.Ease, NonMotion: true}
	PanelFadeOut   = ui.Animation{Duration: base.Motion.MotionPanelOut, Ease: RouteMotion.Ease, NonMotion: true}
)

// SelectFade 是“选中”这一族（导航项、会话行、设置分类、筛选片）底色和阴影的淡变：选中那块浮起的白底加阴影、悬停和按下的浅底，都 140 毫秒淡进淡出
// （规格 A9、A10、B2）。只开 Colors、Shadows：位置、大小不动画，拖分栏时行跟手。
var SelectFade = ui.ElementTransition{Duration: MotionFast, Ease: ControlMotion.Ease, Colors: true, Shadows: true}

// 弹层的进出场（规格-过渡动画 3.1，时长和位移都取令牌 motion.*、travel.*、scale.*）。
// 菜单、下拉：180 进 / 130 出，从触发的那一边出来（Flip：菜单翻到触发钮上方时镜像）6、缩放 .98；
// 对话框：220 / 160，进场 Y 10、缩放 .985；提示：150 / 100，Y 2。进出场都只淡加这点位移缩放，高度不动。
// 退场曲线沿用进场（ExitEase 留空）；规格里的 (.42,0,1,1) / animate-ui / 直接用 route 三选一等主人定，定了只改 popTransition。
// 减少动态效果时 mygo 保留淡入淡出、位移缩放归零（transition.go:158-161）。
var (
	MenuTransition = popTransition(base.Motion.MotionMenuIn, base.Motion.MotionMenuOut,
		ui.Motion{Y: -float32(base.Num.TravelMenu), Scale: float32(base.Num.ScaleMenu), Flip: true})
	// 对话框：遮罩和面板一起淡（Transition 挂在整窗遮罩上，它是浮层的直接孩子，进出场才播），面板的上移和缩放另用 DialogPop 状态驱动
	// （面板和遮罩同一帧出现，它自己的 Enter 不播）；退场是遮罩拷贝整个淡出，面板不再位移。
	DialogFade    = popTransition(base.Motion.MotionDialogIn, base.Motion.MotionDialogOut, ui.Motion{})
	DialogPop     = ui.Animation{Duration: base.Motion.MotionDialogIn, Ease: ui.EaseOut}
	TipTransition = popTransition(base.Motion.MotionTipIn, base.Motion.MotionTipOut, ui.Motion{Y: float32(base.Num.TravelTip), Flip: true})
	// StatusTransition 是标题栏状态字换内容：140 毫秒淡入、淡出，不位移（规格-过渡动画 C21）。
	StatusTransition = popTransition(base.Motion.MotionFast, base.Motion.MotionFast, ui.Motion{})
	// ContentFadeAnim 是“换内容”（换页、换会话、换设置分类）：新内容 200 毫秒淡入，不位移，旧的不做出场（规格 3.1）。
	// 淡入带 NonMotion：系统要求减少动态效果时淡入照常（ADR-124）。状态驱动（fadeIn），因为换内容的元素和它的父元素常常是同一帧新出现的，Transition 的 Enter 不播。
	ContentFadeAnim = ui.Animation{Duration: base.Motion.MotionMsg, Ease: ui.EaseOut, NonMotion: true}
)

// popTransition 是进出场用同一个起点的 Transition：in 进、out 出。
// Transition 不选 Position / Size / Colors / Shadows 时它们全都会动画，所以钉上 Shadows：这些元素本身没有阴影变化，
// 等于只留进出场，面板跟着触发钮、窗口或分隔条挪的时候不被缓动拖着走（拖分栏要跟手，规格 铁律 1）。
func popTransition(in, out time.Duration, m ui.Motion) ui.ElementTransition {
	return chatui.PopTransition(in, out, m)
}
