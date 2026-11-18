package chatui

import (
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 审批倒计时每秒变一次，逐位滚：4:32 → 4:31 只有最后一位动；变小所以从上面落下来；终点就是 4:31；减少动态效果只淡变。
func TestApprovalCountdownRolls(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		tk := Derive(DefaultSettings())
		h := Host{T: tk, Column: 600, SelfChar: CharColors[0]}
		st := &Step{ID: "c", Kind: StepCommand, State: StepRunning, Target: "rm -rf build",
			Approval: &Approval{State: ApprovalPending, Reason: "清掉旧的构建", Remaining: 272 * time.Second}}
		m := &Msg{ID: "p1", Sender: "r1", Name: "晴枝", Char: CharColors[1], At: time.Date(2026, 11, 6, 23, 0, 0, 0, time.Local), Steps: []*Step{st}}
		view := func(c *ui.Context) {
			c.Root().Background(h.T.C.ChatBg)
			ui.Column(c.Key("list")).Width(h.Column).Padding(20).Children(func() { h.Message(c, m, nil, nil) })
		}
		tt := ui.NewTester(view, 700, 300)
		tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
		tt.Frame()
		tt.Frame()
		r, ok := tt.Find("4:32")
		if !ok {
			t.Fatalf("没找到倒计时 4:32：%v", tt.Texts())
		}
		area := image.Rect(int(r.X)-2, int(r.Y)-2, int(r.X+r.W)+2, int(r.Y+r.H)+2)
		start := image.NewRGBA(tt.Image().Bounds())
		copy(start.Pix, tt.Image().Pix)
		st.Approval.Remaining = 271 * time.Second
		tt.Frame()
		tt.Advance(60 * time.Millisecond)
		tt.Frame()
		mid := tt.Image()
		diff := func(a, b *image.RGBA, r image.Rectangle) int {
			n := 0
			for y := r.Min.Y; y < r.Max.Y; y++ {
				for x := r.Min.X; x < r.Max.X; x++ {
					pa, pb := a.RGBAAt(x, y), b.RGBAAt(x, y)
					if int(pa.R)-int(pb.R) > 24 || int(pb.R)-int(pa.R) > 24 {
						n++
					}
				}
			}
			return n
		}
		// 等宽数字，四个字符一样宽：前三个（4:3）在左，最后一位在右
		cw := (area.Dx() - 4) / 4
		left := image.Rect(area.Min.X, area.Min.Y, area.Min.X+3*cw-1, area.Max.Y)
		right := image.Rect(area.Min.X+3*cw+2, area.Min.Y, area.Max.X, area.Max.Y)
		if !reduce {
			if n := diff(start, mid, left); n != 0 {
				t.Errorf("没变的 4:3 不该动，却有 %d 个像素不同", n)
			}
			if n := diff(start, mid, right); n < 5 {
				t.Errorf("最后一位应在滚，右边只有 %d 个像素不同", n)
			}
		}
		for i := 0; tt.FramePending() && i < 100; i++ {
			tt.Advance(50 * time.Millisecond)
			tt.Frame()
		}
		if _, ok := tt.Find("4:31"); !ok {
			t.Errorf("reduce=%v 终点没有 4:31：%v", reduce, tt.Texts())
		}
		if tt.FramePending() {
			t.Errorf("reduce=%v 滚完还在要帧", reduce)
		}
	}
}
