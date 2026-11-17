package chatui

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// Fade 是悬停、按下这类颜色和透明度的淡变（规格-过渡动画 3.1“反馈”）：140 毫秒、标准曲线，带 NonMotion。
// 和外壳的 winshell.ControlFade 同一条（时长取令牌 motion.fast、曲线取 ease.standard）。
// AnimateColor 在 mygo 里本来就不受“减少动态效果”影响；AnimateTo 做的透明度要带 NonMotion，否则减少动态时淡变也变成硬切（ADR-124）。
// 位移、缩放、旋转不要用它。
func (t Tokens) Fade() ui.Animation {
	e := t.D.Curve.EaseStandard
	return ui.Animation{Duration: t.Fast, Ease: ui.CubicBezier(e[0], e[1], e[2], e[3]), NonMotion: true}
}

// Fill 返回按钮 b 这一帧该画的底色，过渡到目标 bg 用 Fade：悬停、按下的底 140 毫秒淡进淡出，不再硬切。
// 用法：b.Background(t.Fill(b, bg))。同一个元素只用一次（通道名是 "fill"）。
func (t Tokens) Fill(b ui.Element, bg ui.Color) ui.Color { return b.AnimateColor("fill", bg, t.Fade()) }

// PopTransition 是浮层的进出场：in 进、out 出，起点（也是终点）是 m；进场淡入加 m 的位移缩放，出场反过来，退场期间 mygo 拷贝最后一帧、不收点击和焦点。
// Transition 不选 Position / Size / Colors / Shadows 时它们全都会动画，所以钉上 Shadows（这些浮层没有阴影变化）：
// 浮层跟着锚点、选区或窗口挪的时候直接跟手，不被缓动拖着走。
func PopTransition(in, out time.Duration, m ui.Motion) ui.ElementTransition {
	return ui.ElementTransition{Duration: in, Ease: ui.EaseOut, ExitDuration: out, Shadows: true, Enter: &m, Exit: &m}
}

// TipTransition 是选区条这类小浮层的进出场（规格 C14）：150 毫秒淡入、100 毫秒淡出（motion.tip-in/out），沿 dir 方向移 travel.tip：
// dir 为 1 从下方升起（条在选区上方），-1 从上方落下（条在选区下方）。
func (t Tokens) TipTransition(dir float32) ui.ElementTransition {
	return PopTransition(t.D.Motion.MotionTipIn, t.D.Motion.MotionTipOut, ui.Motion{Y: dir * float32(t.D.Num.TravelTip)})
}
