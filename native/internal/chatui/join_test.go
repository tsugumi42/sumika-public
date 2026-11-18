package chatui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// renderMsgs 用 Message 依次画一串消息（和工作台同一条路）。
func renderMsgs(h Host, msgs []*Msg) *ui.Tester {
	view := func(c *ui.Context) {
		ui.Column(c.Key("list")).Width(h.Column).Children(func() {
			for i, m := range msgs {
				var prev, next *Msg
				if i > 0 {
					prev = msgs[i-1]
				}
				if i+1 < len(msgs) {
					next = msgs[i+1]
				}
				h.Message(c, m, prev, next)
			}
		})
	}
	tt := ui.NewTester(view, 900, 1400)
	tt.Frame()
	tt.Frame()
	return tt
}

// diff 的加减行：连续同类算一段，段的上下两角是圆角，段内相接处是直角；上下文行没有底色、没有圆角。字左右留 s1。
func TestDiffRunCorners(t *testing.T) {
	for _, set := range []Settings{DefaultSettings(), {Text: TextMin, Radius: RadiusMax, Density: DensityMin}, {Text: TextMax, Radius: RadiusMax, Density: DensityMax}} {
		diffRunCorners(t, set)
	}
}

func diffRunCorners(t *testing.T, set Settings) {
	tk := Derive(set)
	h := Host{T: tk, Column: 720}
	st := &Step{ID: "e", Kind: StepEdit, State: StepRunning, Target: "a.md", Diff: "-a\n+b\n+c\n d\n", Approval: &Approval{State: ApprovalPending}}
	lines := tree(processTester(h, []*Step{st}, map[string]*bool{})).find("lines")
	if lines == nil || len(lines.Children) != 4 {
		t.Fatal("找不到 diff 的四行")
	}
	r := min(max(tk.Inner(max(tk.Inner(tk.R1, tk.BubblePadV), tk.RInnerMin), tk.S2), tk.RInnerMin), round1(tk.FS[1]*1.6)/2)
	want := [][4]float32{{r, r, r, r}, {r, r, 0, 0}, {0, 0, r, r}, {0, 0, 0, 0}}
	for i, w := range want {
		n := lines.Children[i]
		if r > n.Box.H/2+0.01 {
			t.Errorf("外观 %+v 第 %d 行圆角 %.2f 超过半行高 %.2f", set, i, r, n.Box.H/2)
		}
		for k := range w {
			if !near(n.Radius[k], w[k]) {
				t.Errorf("第 %d 行 %q 圆角 %v，期望 %v", i, n.Text, n.Radius, w)
				break
			}
		}
		if !near(n.Pad[1], tk.S1) || !near(n.Pad[3], tk.S1) {
			t.Errorf("第 %d 行左右内距 %v，期望 %.1f", i, n.Pad, tk.S1)
		}
	}
}
