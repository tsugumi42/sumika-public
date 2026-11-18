package winshell

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/design"
)

// 动效量的唯一出处，和“慢放”开关。
//
// 慢放只给开发用的画廊（cmd/sumika-gallery）：动画只有 140～230 毫秒，肉眼和截图都看不清中间帧，慢放把时长乘上倍数、
// 弹簧按同样倍数放慢（刚度 ÷ 倍数²、阻尼 ÷ 倍数，路径形状不变，只是时间拉长）。mygo 没有全局的时间缩放，所以乘在我们自己取动效量的地方：
//   - 包级的这一批（ControlFade、SelectFade、DropdownGrow……）：SetMotionScale 用新倍数整批重算、原地赋值；
//   - Kit 上的弹簧：k.smooth()、k.bouncy() 每次取用时按当前倍数换算；
//   - 按下缩放的时长：slowDur。
//
// 正式界面从不调 SetMotionScale，倍数永远是 1，所有量和没有这一层时逐字节相同。
// 没被放慢的：mygo 自己内置的动画（滚动惯性、滚动条淡出、焦点环）、chatui 里自己取的消息进场和气泡弹簧、
// 其他窗口的包级量。倍数是进程级的，用完要还原成 1（画廊只在构建舞台的那一段里设，构建完立刻还原）。

var motionScale = 1.0

// MotionScale 是现在的慢放倍数；1 是正常速度。
func MotionScale() float64 { return motionScale }

// SetMotionScale 设慢放倍数（大于 1 是慢放，比如 4、10）；小于等于 0 或不是有限数当 1。返回设之前的倍数，方便还原。
// 包级的动效量会被整批重算；倍数没变什么也不做。
func SetMotionScale(f float64) (was float64) {
	was = motionScale
	if f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		f = 1
	}
	if f == motionScale {
		return was
	}
	motionScale = f
	m := newMotionSet(f)
	mo = m
	MotionFast, MotionRoute = m.fast, m.route
	ControlMotion, ControlFade, RouteMotion, SizeMotion = m.controlMotion, m.controlFade, m.routeMotion, m.sizeMotion
	PanelOutMotion, PanelFade, PanelFadeOut = m.panelOut, m.panelFade, m.panelFadeOut
	SelectFade = m.selectFade
	TipTransition, StatusTransition, ContentFadeAnim = m.tip, m.status, m.contentFade
	DropdownGrow, DropdownShrink = m.ddGrow, m.ddShrink
	return was
}

// slowDur 把一个时长乘上慢放倍数。
func slowDur(d time.Duration) time.Duration {
	if motionScale == 1 {
		return d
	}
	return time.Duration(math.Round(float64(d) * motionScale))
}

// slowSpring 把弹簧按倍数放慢：质量不变，刚度 ÷ 倍数²、阻尼 ÷ 倍数，等于把时间轴拉长（用 Duration/Bounce 描述的弹簧时长 × 倍数）。
func slowSpring(s ui.SpringOptions) ui.SpringOptions {
	f := motionScale
	if f == 1 {
		return s
	}
	s.Duration = time.Duration(math.Round(float64(s.Duration) * f))
	s.Stiffness /= f * f
	s.Damping /= f
	return s
}

// 弹簧预设（design.Springs），按慢放倍数换算。位移、尺寸、缩放只从这几根里挑：
// 宽高和任何高度变化只用 firm（不回弹，设计语言第九节）；位移、旋转、按下回弹用 snappy；大块的面板用 soft；冒出来的小东西用 bouncy。
func (k *Kit) firm() ui.SpringOptions    { return slowSpring(k.T.Spring.Firm) }
func (k *Kit) snappy() ui.SpringOptions  { return slowSpring(k.T.Spring.Snappy) }
func (k *Kit) soft() ui.SpringOptions    { return slowSpring(k.T.Spring.Soft) }
func (k *Kit) springy() ui.SpringOptions { return slowSpring(k.T.Spring.Bouncy) }

// motionSet 是一整套包级动效量。
type motionSet struct {
	fast, route                             time.Duration
	controlMotion, controlFade, routeMotion ui.Animation
	sizeMotion                              ui.Animation
	panelOut, panelFade, panelFadeOut       ui.Animation
	contentFade, ddGrow, ddShrink           ui.Animation
	selectFade, tip, status                 ui.ElementTransition
}

// springSet 是默认令牌的弹簧预设按慢放倍数换算后的一套（包级量用；Kit 上的用 k.firm() 这些）。
func springSet(f float64) design.Springs {
	s := base.Springs()
	slow := func(o ui.SpringOptions) ui.SpringOptions {
		o.Duration = time.Duration(math.Round(float64(o.Duration) * f))
		return o
	}
	s.Firm, s.Snappy, s.Soft, s.Bouncy = slow(s.Firm), slow(s.Snappy), slow(s.Soft), slow(s.Bouncy)
	s.StretchLead, s.StretchTrail = slow(s.StretchLead), slow(s.StretchTrail)
	return s
}

func newMotionSet(f float64) motionSet {
	sc := func(d time.Duration) time.Duration { return time.Duration(math.Round(float64(d) * f)) }
	var m motionSet
	m.fast, m.route = sc(base.Motion.MotionFast), sc(base.Motion.MotionPanel)
	std := base.Curve.EaseStandard
	rt := base.Curve.EaseRoute
	// ControlMotion 和 RouteMotion 是控件状态与换层的动画（原型 controlMotion、routeMotion）；ControlFade 是同样快慢的颜色、透明度淡变，
	// 带 NonMotion：系统要求减少动态效果时位移、旋转直接到位，AnimateTo 做的透明度淡变照常（ADR-124）。AnimateColor 在 mygo 里本来就
	// 不受减少动态影响，颜色也用 ControlFade 只是为了一眼看出“这是淡变”。
	// 位移、旋转、尺寸一律是弹簧预设（spring-base）；颜色、透明度是时长淡变。ControlMotion（箭头旋转这类小位移）用“灵敏”，
	// RouteMotion（换层、浮动右栏进场）用“柔和”。
	sp := springSet(f)
	stdEase := ui.CubicBezier(std[0], std[1], std[2], std[3])
	m.controlMotion = ui.Animation{Spring: sp.Snappy}
	m.controlFade = ui.Animation{Duration: m.fast, Ease: stdEase, NonMotion: true}
	m.routeMotion = ui.Animation{Spring: sp.Soft}
	m.sizeMotion = ui.Animation{Spring: sp.Firm} // 宽高、进度条这类尺寸：“利落”，不过冲
	routeEase := ui.CubicBezier(rt[0], rt[1], rt[2], rt[3])
	// PanelOutMotion 是抽屉、右栏退场：200 毫秒（令牌 motion.panel-out），进场仍是 RouteMotion 的 230；曲线暂同进场（退场曲线三选一等主人定）。
	// PanelFade 是抽屉的透明度：进出都淡，带 NonMotion，减少动态效果时位移直接到位、淡变还在。
	// 退场的位移用“利落”（不回弹）；淡变仍是时长。
	m.panelOut = ui.Animation{Spring: sp.Firm}
	m.panelFade = ui.Animation{Duration: m.route, Ease: routeEase, NonMotion: true}
	m.panelFadeOut = ui.Animation{Duration: sc(base.Motion.MotionPanelOut), Ease: routeEase, NonMotion: true}
	// SelectFade 是“选中”这一族（导航项、会话行、设置分类、筛选片）底色和阴影的淡变：选中那块浮起的白底加阴影、悬停和按下的浅底，都 140 毫秒淡进淡出
	// （规格 A9、A10、B2）。只开 Colors、Shadows：位置、大小不动画，拖分栏时行跟手。
	m.selectFade = ui.ElementTransition{Duration: m.fast, Ease: stdEase, Colors: true, Shadows: true}
	// 弹层的进出场（规格-过渡动画 3.1，时长和位移都取令牌 motion.*、travel.*、scale.*）。
	// 菜单、选择器不在这里：从按钮那条边展开收起，见 FloatMenu（chatui.Tokens.MenuMorph，2026-11-15 起）。
	// 对话框也不在这里：从打开它的按钮长出来、关掉缩回去，见 dialog.go 的 dialogMorph（2026-11-15 起）。
	// 提示：150 / 100，Y 2。进出场只淡加这点位移，高度不动。
	// 退场曲线沿用进场（ExitEase 留空）；规格里的 (.42,0,1,1) / animate-ui / 直接用 route 三选一等主人定，定了只改 popTransition。
	// 减少动态效果时 mygo 保留淡入淡出、位移缩放归零（transition.go:158-161）。
	mt := base.Motion
	m.tip = popTransition(sc(mt.MotionTipIn), sc(mt.MotionTipOut), ui.Motion{Y: float32(base.Num.TravelTip), Flip: true})
	m.status = popTransition(m.fast, m.fast, ui.Motion{})
	// ContentFadeAnim 是“换内容”（换页、换会话、换设置分类）：新内容 200 毫秒淡入，不位移，旧的不做出场（规格 3.1）。
	// 淡入带 NonMotion：系统要求减少动态效果时淡入照常（ADR-124）。状态驱动（fadeIn），因为换内容的元素和它的父元素常常是同一帧新出现的，Transition 的 Enter 不播。
	m.contentFade = ui.Animation{Duration: sc(mt.MotionMsg), Ease: ui.EaseOut, NonMotion: true}
	// 下拉自己长大：高度和箭头用不过冲的缓出（高度变化永远不过冲）；展开 180、收回 130，和菜单进出场同一档（令牌 motion.menu-in / menu-out）。
	// 不带 NonMotion：系统要求减少动态效果时直接到位。
	m.ddGrow = ui.Animation{Spring: sp.Firm}
	m.ddShrink = ui.Animation{Spring: sp.Firm}
	return m
}

var _ = chatui.PopTransition // popTransition 在 geometry.go 里包了它

// ChatTokens 是给 chatui 用的令牌：弹簧按慢放倍数换算。chatui 自己从令牌里取弹簧，不经过 Kit，所以画廊要看慢放就用它；倍数是 1 时和 k.T 逐字节一样。
func (k *Kit) ChatTokens() chatui.Tokens {
	t := k.T
	if motionScale != 1 {
		s := &t.Spring
		s.Firm, s.Snappy, s.Soft, s.Bouncy = slowSpring(s.Firm), slowSpring(s.Snappy), slowSpring(s.Soft), slowSpring(s.Bouncy)
		s.StretchLead, s.StretchTrail = slowSpring(s.StretchLead), slowSpring(s.StretchTrail)
		// 淡变的时长也放慢（chatui 的 Fade、FadeOut 取 t.Fast 和 t.D.Motion）：不然慢放下位移、高度慢慢走、淡变还是 140 毫秒，看不出它们是配着走的
		t.Fast, t.Msg, t.Panel = slowDur(t.Fast), slowDur(t.Msg), slowDur(t.Panel)
		m := &t.D.Motion
		m.MotionFast, m.MotionMsg, m.MotionPanel, m.MotionPanelOut = slowDur(m.MotionFast), slowDur(m.MotionMsg), slowDur(m.MotionPanel), slowDur(m.MotionPanelOut)
		m.MotionTipIn, m.MotionTipOut, m.MotionPress = slowDur(m.MotionTipIn), slowDur(m.MotionTipOut), slowDur(m.MotionPress)
	}
	return t
}
