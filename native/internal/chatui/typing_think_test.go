package chatui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 开始想的时候，三个点不变，后面长出“正在想”：点的大小、横向位置、气泡高度都和“正在输入”一样。
func TestThinkingDotsContinueTypingDots(t *testing.T) {
	h := Host{T: Derive(DefaultSettings()), Column: 600}
	dotsOf := func(tt *ui.Tester) (ui.Rect, ui.Rect) {
		t.Helper()
		root := tree(tt)
		d := root.find("dots")
		if d == nil {
			t.Fatalf("没画点：%v", tt.Texts())
		}
		b := root.find("bubble")
		if b == nil {
			b = root.find("summary")
		}
		return d.Box, b.Box
	}
	typing := ui.NewTester(func(c *ui.Context) { h.Typing(c, "live-typing", CharColors[0]) }, 700, 300)
	typing.Frame()
	typing.Frame()
	m := Msg{ID: "m", Sender: "甲", Steps: []*Step{{ID: "t", Kind: StepThink, State: StepRunning}}, Char: CharColors[0], State: Sent}
	think := ui.NewTester(func(c *ui.Context) { h.Message(c, &m, nil, nil) }, 700, 300)
	think.Frame()
	think.Frame()
	if !think.HasText("正在想") {
		t.Fatalf("过程气泡该写正在想：%v", think.Texts())
	}
	a, ab := dotsOf(typing)
	b, bb := dotsOf(think)
	if !near(a.W, b.W) || !near(a.H, b.H) {
		t.Fatalf("两组点大小不同：%+v %+v", a, b)
	}
	if !near(a.X-ab.X, b.X-bb.X) {
		t.Fatalf("点离气泡左边的距离不同：%v %v", a.X-ab.X, b.X-bb.X)
	}
	if !near(ab.H, bb.H) {
		t.Fatalf("气泡高度不同：%v %v", ab.H, bb.H)
	}
}
