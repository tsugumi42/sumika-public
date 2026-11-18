package chatui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 过渡动画第一批在聊天区这一半（规格-过渡动画 第七节）：按钮族悬停、按下的底色淡变，纯透明度通道在“减少动态效果”下仍淡变，小浮层进出场。

func darkness(tt *ui.Tester) int {
	img := tt.Image()
	n := 0
	for i := 0; i+3 < len(img.Pix); i += 4 {
		n += 765 - (int(img.Pix[i]) + int(img.Pix[i+1]) + int(img.Pix[i+2]))
	}
	return n
}

func stepBy(tt *ui.Tester, d time.Duration) {
	tt.Advance(d)
	tt.Frame()
}

// Fill：悬停的底 140 毫秒淡进，离开淡出；减少动态效果时颜色照样淡变（AnimateColor 本来就不受它影响）。
func TestFillFadesHover(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		tk := Derive(DefaultSettings())
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Box(c.Key("bg")).Fill().Background(ui.RGB(255, 255, 255)).Center().Children(func() {
				b := ui.ButtonBase(c.Key("b")).Label("按钮").Size(80, 40)
				bg := ui.Transparent
				if b.Hovered() {
					bg = tk.C.Hover
				}
				b.Background(tk.Fill(b, bg))
			})
		}, 200, 120)
		tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
		tt.Frame()
		tt.Frame()
		d0 := darkness(tt)
		tt.Move(100, 60)
		tt.Frame()
		d1 := darkness(tt)
		stepBy(tt, 40*time.Millisecond)
		dm := darkness(tt)
		stepBy(tt, time.Second)
		dEnd := darkness(tt)
		if !(d0 <= d1 && d1 < dm && dm < dEnd) {
			t.Errorf("reduce=%v 悬停底色淡入四帧深色量 %d → %d → %d → %d，应逐步变深", reduce, d0, d1, dm, dEnd)
		}
		tt.Move(5, 5)
		tt.Frame()
		stepBy(tt, 40*time.Millisecond)
		if out := darkness(tt); !(d0 < out && out < dEnd) {
			t.Errorf("reduce=%v 离开后 40 毫秒深色量 %d，应在 %d 和 %d 之间", reduce, out, d0, dEnd)
		}
	}
}

// 悬停操作条的透明度通道带 NonMotion：减少动态效果时仍然淡入，不是硬切。
func TestHoverBarFadesUnderReduceMotion(t *testing.T) {
	r := newBarRig(t, plainMsg(false), false, true)
	r.away()
	stepBy(r.tt, time.Second)
	d0 := darkness(r.tt)
	r.over(t, "第一句")
	d1 := darkness(r.tt)
	stepBy(r.tt, 30*time.Millisecond)
	dm := darkness(r.tt)
	stepBy(r.tt, time.Second)
	dEnd := darkness(r.tt)
	if !(d0 <= d1 && d1 < dm && dm < dEnd) {
		t.Errorf("减少动态效果时操作条深色量 %d → %d → %d → %d，应逐步淡入而不是一步到位", d0, d1, dm, dEnd)
	}
}

// 新消息进场（行内状态驱动那一套）：减少动态效果时淡入还在、位移为 0。
func TestAppearFadesUnderReduceMotion(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		tk := Derive(DefaultSettings())
		seen := map[string]bool{}
		host := Host{T: tk, SelfChar: CharColors[0], Column: 600, Appear: func(id string) bool { // 和正式的一样：只有第一次问是新的
			first := !seen[id]
			seen[id] = true
			return first
		}}
		m := &Msg{ID: "e", Sender: "self", Self: true, Text: "进场"}
		show := false
		tt := ui.NewTester(func(c *ui.Context) {
			c.Root().Background(ui.RGB(255, 255, 255))
			if show {
				host.Message(c, m, nil, nil)
			}
		}, 400, 120)
		tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
		tt.Frame()
		show = true
		tt.Frame()
		tt.Frame() // 第二帧才问到“不是新的了”，目标从起点换成原位，淡入从这里算起
		first := textTop(tt)
		d1 := darkness(tt)
		stepBy(tt, 30*time.Millisecond)
		dm := darkness(tt)
		stepBy(tt, 2*time.Second)
		dEnd := darkness(tt)
		settled := textTop(tt)
		if !(d1 < dm && dm < dEnd) {
			t.Errorf("reduce=%v 新消息淡入三帧深色量 %d → %d → %d，应逐步变深", reduce, d1, dm, dEnd)
		}
		if reduce && first >= 0 && first != settled && abs32(first-settled) > 0.5 {
			t.Errorf("减少动态效果时新消息还在位移：第一帧 %v 停稳 %v", first, settled)
		}
	}
}

// 选区条这类小浮层：第一次出现淡入加 2 像素（150），消失淡出（100）；减少动态时只淡不位移；选区条位置变动时跟手。
func TestTipTransitionPopover(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		tk := Derive(DefaultSettings())
		open := false
		x := float32(60)
		tt := ui.NewTester(func(c *ui.Context) {
			c.Root().Background(ui.RGB(255, 255, 255))
			anchor := ui.Box(c.Key("anchor")).Absolute().Left(x).Top(100).Size(1, 10)
			ui.PopoverBase(c, anchor, &open, func(panel ui.Element) {
				panel.AttachTo(anchor, ui.AnchorTop, ui.AnchorBottom).Margin(0, 0, 4, 0)
				panel.Label("条").Size(60, 30).Background(ui.RGB(0, 0, 0))
				panel.Transition(tk.TipTransition(1))
			})
		}, 300, 200)
		tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
		tt.Frame()
		open = true
		tt.Frame()
		r0, _ := tt.Find("条")
		d0 := darkness(tt)
		stepBy(tt, 30*time.Millisecond)
		r1, _ := tt.Find("条")
		dm := darkness(tt)
		stepBy(tt, time.Second)
		rEnd, _ := tt.Find("条")
		dEnd := darkness(tt)
		if !(d0 < dm && dm < dEnd) {
			t.Errorf("reduce=%v 小浮层淡入三帧深色量 %d → %d → %d", reduce, d0, dm, dEnd)
		}
		if reduce && (abs32(r0.Y-rEnd.Y) > 0.01 || abs32(r1.Y-rEnd.Y) > 0.01) {
			t.Errorf("减少动态效果时小浮层还在位移：%.2f %.2f", r0.Y-rEnd.Y, r1.Y-rEnd.Y)
		}
		if !reduce && (r0.Y-rEnd.Y < 1 || r0.Y-rEnd.Y > 3) {
			t.Errorf("小浮层进场起点离停稳位置 %.2f，应约 2", r0.Y-rEnd.Y)
		}
		// 位置变动跟手：锚点挪了，下一帧浮层就在新位置，不被缓动拖着走
		x = 160
		tt.Frame()
		if moved, _ := tt.Find("条"); abs32(moved.X-rEnd.X-100) > 0.01 {
			t.Errorf("reduce=%v 锚点挪了 100，浮层只跟了 %.2f（被缓动拖着走）", reduce, moved.X-rEnd.X)
		}
		open = false
		tt.Frame()
		stepBy(tt, 30*time.Millisecond)
		if out := darkness(tt); !(0 < out && out < dEnd) {
			t.Errorf("reduce=%v 小浮层退场中深色量 %d，应在 0 和 %d 之间", reduce, out, dEnd)
		}
		stepBy(tt, time.Second)
		if gone := darkness(tt); gone != 0 {
			t.Errorf("reduce=%v 小浮层退场放完还在：%d", reduce, gone)
		}
	}
}
