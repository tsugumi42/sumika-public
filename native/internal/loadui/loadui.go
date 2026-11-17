// Package loadui 画 loadstate 状态机要的占位：骨架条、占住位置的提示行、安排下一帧。
// 只依赖 mygo/ui，不依赖任何组件库（新界面和旧页面共用，ADR-322）；颜色全取自当前主题，跟着明暗走。
//
// 骨架条的高度不是写死的数，而是真放一个同字号、看不见的文字行撑出来的：
// 真实内容用同样字号的文字，所以骨架和内容的行高永远一致，换的时候不跳（LESSONS：晚到的高度会把用户正在看的东西推走）。
// 阈值内没有数据时（loadstate.View.Waiting），页面照样排同一套骨架，只是不画（Bar 的 draw 传 false），
// 到阈值那一帧只是"画出来"，布局不动。
package loadui

import (
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/loadstate"
)

// blank 是占位用的一个汉字：一律画成全透明，只借它撑出和真实中文文字行一样的行高
// （换成不换行空格，字体回退不同，行高会差零点几像素）。它不进无障碍树，测试里 HasText 也不会把它当成提示。
const blank = "占"

// drawn 数骨架条被真正画了多少次（Draw 可能一帧调好几次，只看增量有没有）。只给测试确认"该画才画"，生产代码不读它
var drawn atomic.Int64

// Drawn 到现在骨架条被画过的次数
func Drawn() int64 { return drawn.Load() }

// Wake 安排阈值到点的那一帧。状态机说"还要等 Next"，就在到点后再画一次；没有要等的什么都不做。
// 多留 1 毫秒，免得计时器比帧时钟早一点点醒来、又判成"还没到"。
func Wake(c *ui.Context, v loadstate.View) {
	if v.Next > 0 {
		c.After(v.Next + time.Millisecond)
	}
}

// Visible 占位（骨架或刷新标记）这一帧该不该画出来。
func Visible(v loadstate.View) bool { return v.Skeleton || v.Refreshing }

// Line 一行"取数据中…"这样的提示：该出现时是 text，没到时间是同样高的透明占位，所以出现前后整块高度不变。
// mk 用页面自己的字号和颜色画这一行（传进来的是要画的字），必须返回文字元素本身。
func Line(v loadstate.View, text string, mk func(text string) ui.Element) ui.Element {
	if !Visible(v) {
		return mk(blank).TextColor(ui.Transparent).Role(ui.RoleNone)
	}
	return mk(text)
}

// Bar 一条骨架条：宽度占父容器的 widthPercent，高度等于字号 fontSize 的一行文字（0 = 默认字号），圆角取主题圆角的一半。
// draw 为 false 时只占位置、什么都不画。系统要求减少动效时只画底色，不流动、不要帧。
func Bar(c *ui.Context, widthPercent, fontSize float32, draw bool) ui.Element {
	t := c.Theme()
	base, shine := t.Text.Alpha(0.07), t.Text.Alpha(0.13)
	radius := t.Radius / 2
	still := c.Preferences().ReduceMotion
	bar := ui.Column(c).WidthPercent(widthPercent).Role(ui.RoleNone).Draw(func(p *ui.Painter, r ui.Rect) {
		if !draw {
			return
		}
		drawn.Add(1)
		p.Fill(r, base, radius)
		if still {
			return
		}
		p.AnimationFrame()
		w, view := t.Space(60), p.Visible()
		phase := float32(p.Now().UnixMilli()%1600) / 1600
		x := view.X - w + (view.W+2*w)*phase
		p.Clip(r, radius, func() {
			p.FillGradient(ui.Rect{X: x, Y: r.Y, W: w, H: r.H},
				ui.LinearGradient{Angle: 90, Stops: []ui.GradientStop{{Color: base, At: 0}, {Color: shine, At: 0.5}, {Color: base, At: 1}}}, 0)
		})
	})
	return bar.Children(func() {
		txt := ui.Text(c, blank).SingleLine().TextColor(ui.Transparent).Role(ui.RoleNone)
		if fontSize > 0 {
			txt.FontSize(fontSize)
		}
	})
}
