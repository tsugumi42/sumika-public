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

// MenuMorph 给菜单、选择器这类从按钮弹出来的面板挂进出场（主人 2026-11-15：展开收缩做成 morph，照 Fluid 的 MenuMorph“动边不动内容”）：
// 面板从贴着按钮的那条边展开，高度用“利落”弹簧从 0 长到全高，同时淡入；里面的项按全尺寸排好，只是被外框逐渐露出来，不挤不变形。
// 关掉时 mygo 留一份拷贝反过来缩回那条边，不收点击和焦点。
// 朝下弹出时面板顶边贴着按钮，展开、收起都以顶边为准，正好；朝上弹出（窗口下面放不下，面板底边贴着按钮）时布局自己从底边长，
// 而拷贝的收起只会往顶边缩，所以那时让拷贝边缩边往下挪自己的全高，等于缩回底边。朝哪边看上一帧布局实际放的那一侧（AttachedAnchors）。
// 减少动态效果时不长不缩，只淡入淡出。阴影钉住不动画：面板跟着按钮、窗口挪的时候直接跟手。
//
// 展开不用 Motion 的 Collapse：那会改面板在布局里的高度，mygo 按长到一半的小面板判断“下面放得下”，长大以后就出了窗口。
// 所以面板一直按全尺寸排（朝上朝下按全尺寸定），只用 ClipInsetPercent 从远离按钮的那条边往外露；裁剪的圆角跟面板的圆角，露出来的边是圆的。
// 被裁掉的部分不收点击：点开以后要等它长开再点（测试用 settle）。收起的拷贝不再排版，用 Collapse 没有这个问题。
// radius 是面板自己的圆角（裁剪的圆角跟它）。shadow 画面板的阴影，a 是浓度倍数（0～1）；nil 就是没有阴影。
// 裁剪连阴影一起裁，展开的时候阴影本来就看不见，等裁剪撤掉那一帧会一下子冒出来（主人 2026-11-16 看出“阴影会突然弹出来”）；
// 所以展开期间不画阴影，长开以后阴影 140 毫秒淡到满。
func (t Tokens) MenuMorph(c *ui.Context, panel ui.Element, radius float32, shadow func(a float32)) ui.Element {
	_, self := panel.AttachedAnchors()
	up := self == ui.AnchorBottom || self == ui.AnchorBottomStart || self == ui.AnchorBottomEnd || self == ui.AnchorBottomLeft || self == ui.AnchorBottomRight
	out := ui.Motion{Collapse: true}
	if up {
		out.YPercent = 100
	}
	tr := ui.ElementTransition{Animation: &ui.Animation{Spring: t.Spring.Firm}, Shadows: true, Exit: &out}
	if c.Preferences().ReduceMotion {
		// 减少动态效果：mygo 对带 Collapse 的退场直接拿掉，连淡出也没有；这时只淡出（ADR-124：淡变照常）
		out = ui.Motion{}
		tr.Animation, tr.Duration = nil, t.D.Motion.MotionTipOut
	}
	panel.Transition(tr)
	// 展开：第一次画时从 0 起（mygo 的通道第一次就是目标值，所以先 Jump 到 0）；面板关掉后状态随元素一起丢，下次再开重新长
	seen := ui.Local(panel, "morph-seen", func() bool { return false })
	if !*seen {
		*seen = true
		panel.Jump("morph", 0)
		panel.Jump("morph-fade", 0)
		panel.Jump("morph-shadow", 0)
	}
	p := panel.AnimateTo("morph", 1, ui.Animation{Spring: t.Spring.Firm})
	f := panel.AnimateTo("morph-fade", 1, t.Fade()) // 淡入带 NonMotion：减少动态效果时不长，但照样淡入
	if !c.Preferences().ReduceMotion {
		// 平时透明度跟着展开走，长到四分之一就不透明（和下拉 Dropdown 的 veil 一样）：
		// 单独 140 毫秒淡入的话，面板还没看清高度就差不多长完了，眼里只剩“蹦出来”（主人 2026-11-16“右键菜单展开时有跳变”）
		f = min(1, max(0, p)*4)
	}
	if f < 1 {
		panel.Opacity(max(0, f))
	}
	hide := (1 - max(0, min(p, 1))) * 100
	if hide > .01 {
		top, bottom := float32(0), hide
		if up {
			top, bottom = hide, 0
		}
		panel.ClipInsetPercent(top, 0, bottom, 0, radius)
	}
	sTarget := float32(0)
	if hide <= .01 {
		sTarget = 1
	}
	s := max(0, min(1, panel.AnimateTo("morph-shadow", sTarget, t.Fade())))
	panel.NoShadow()
	if shadow != nil && s > 0 {
		shadow(s)
	}
	return panel
}

// MenuButton 是 mygo 自己画项的按钮菜单（PopoverMenuButton）挂 MenuMorph 用的选项。
// mygo 自画的右键菜单（PopoverMenu）没有这个口子，消息右键菜单还是直接出现（mygo 0.9.1 也没有）。
func (t Tokens) MenuButton(c *ui.Context) ui.PopoverMenuButtonOptions {
	// mygo 自画菜单的阴影是它主题里那一组（popovermenu.go 的 style：0, 6, 20, 0, 黑 18%），这里照样画、浓度跟着淡入
	return ui.PopoverMenuButtonOptions{Panel: func(p ui.Element) {
		t.MenuMorph(c, p, t.RCtl, func(a float32) { p.Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, 0.18*a)) })
	}}
}

// TipTransition 是选区条这类小浮层的进出场（规格 C14）：150 毫秒淡入、100 毫秒淡出（motion.tip-in/out），沿 dir 方向移 travel.tip：
// dir 为 1 从下方升起（条在选区上方），-1 从上方落下（条在选区下方）。
func (t Tokens) TipTransition(dir float32) ui.ElementTransition {
	return PopTransition(t.D.Motion.MotionTipIn, t.D.Motion.MotionTipOut, ui.Motion{Y: dir * float32(t.D.Num.TravelTip)})
}
