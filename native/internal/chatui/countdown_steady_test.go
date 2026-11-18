package chatui

import (
	"fmt"
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 审批倒计时一秒一秒往下数时，左边的时钟图标和数字的起点不许动（主人 2026-11-15：“倒计时的时候，时间 icon 会跳来跳去”）。
// 根子是正式字体文渊圆体的数字不等宽、Tabular 不起作用，所以用仓库里的文渊圆体子集量（字形和 tnum 取自完整字体；默认字体的数字本来等宽，测不出）。
func TestApprovalCountdownIconSteady(t *testing.T) {
	font := registerTinyTestFont(t) // 小字体子集也有等宽数字（tnum）；完整字体会让后面每个 Tester 都复制一份
	tk := Derive(DefaultSettings())
	h := Host{T: tk, Column: 600, SelfChar: CharColors[0], Font: font}
	st := &Step{ID: "c", Kind: StepCommand, State: StepRunning, Target: "rm -rf build",
		Approval: &Approval{State: ApprovalPending, Reason: "清掉旧的构建", Remaining: 272 * time.Second}}
	m := &Msg{ID: "p1", Sender: "r1", Name: "晴枝", Char: CharColors[1], At: time.Date(2026, 11, 6, 23, 0, 0, 0, time.Local), Steps: []*Step{st}}
	tt := ui.NewTester(func(c *ui.Context) {
		c.Root().Background(h.T.C.ChatBg)
		ui.Column(c.Key("list")).Width(h.Column).Padding(20).Children(func() { h.Message(c, m, nil, nil) })
	}, 700, 300)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true}) // 只看停稳后的位置，不看滚动中途
	var x0 float32
	var icon0 *image.RGBA
	var area image.Rectangle
	for sec := 272; sec >= 260; sec-- {
		st.Approval.Remaining = time.Duration(sec) * time.Second
		for i := 0; i < 3 || (tt.FramePending() && i < 100); i++ {
			tt.Advance(50 * time.Millisecond)
			tt.Frame()
		}
		label := fmt.Sprintf("%d:%02d", sec/60, sec%60)
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("没找到倒计时 %s：%v", label, tt.Texts())
		}
		img := tt.Image()
		if icon0 == nil {
			x0 = r.X
			area = image.Rect(int(r.X)-int(tk.FS[1])-12, int(r.Y)-2, int(r.X)-1, int(r.Y+r.H)+2) // 数字左边：时钟图标那一块
			icon0 = image.NewRGBA(area)
			for y := area.Min.Y; y < area.Max.Y; y++ {
				for x := area.Min.X; x < area.Max.X; x++ {
					icon0.Set(x, y, img.At(x, y))
				}
			}
			continue
		}
		if r.X != x0 {
			t.Errorf("%s 的起点 %.2f，第一秒是 %.2f：数字在左右挪", label, r.X, x0)
		}
		n := 0
		for y := area.Min.Y; y < area.Max.Y; y++ {
			for x := area.Min.X; x < area.Max.X; x++ {
				if img.RGBAAt(x, y) != icon0.RGBAAt(x, y) {
					n++
				}
			}
		}
		if n != 0 {
			t.Errorf("%s 时时钟图标那一块有 %d 个像素和第一秒不同：图标在跳", label, n)
		}
	}
}
