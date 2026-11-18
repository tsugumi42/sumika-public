package loadui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/loadstate"
)

// 骨架条的高度要和同字号的真实文字行一样：换内容时不跳
func TestBarHeightEqualsTextLine(t *testing.T) {
	// 前提：占位字和真实文字落在同一种中文字体上。有的机器（CI 的 Windows Server）中文分散在几种兜底字体里，
	// 两段汉字各取各的，行高本来就差零点几：这时测不出骨架条的问题，跳过并写明
	var one, line float32
	ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Children(func() {
			one = ui.Text(c, blank).SingleLine().LayoutBounds().H
			line = ui.Text(c, "真实的一行").SingleLine().LayoutBounds().H
		})
	}, 400, 200).Frame()
	if one != line {
		t.Skipf("这台机器上中文字落在不同的兜底字体里（%q 行高 %v，“真实的一行” %v），量不出骨架条", blank, one, line)
	}
	for _, fs := range []float32{0, 12, 14, 16, 20} {
		var bh, th float32
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).FillWidth().Children(func() {
				bh = Bar(c, 40, fs, true).LayoutBounds().H
				e := ui.Text(c, "真实的一行").SingleLine()
				if fs > 0 {
					e.FontSize(fs)
				}
				th = e.LayoutBounds().H
			})
		}, 400, 200)
		tt.Frame()
		if bh <= 0 || bh != th {
			t.Errorf("字号 %v：骨架条高 %v，文字行高 %v", fs, bh, th)
		}
	}
}

// 提示行出现前后高度一样，没出现时不含那几个字
func TestLineKeepsHeight(t *testing.T) {
	show := false
	var lineH float32
	tt := ui.NewTester(func(c *ui.Context) {
		v := loadstate.View{}
		if show {
			v.Refreshing = true
		}
		ui.Column(c).Children(func() {
			lineH = Line(v, "取数据中…", func(s string) ui.Element { return ui.Text(c, s).SingleLine() }).LayoutBounds().H
		})
	}, 400, 200)
	tt.Frame()
	h0 := lineH
	if tt.HasText("取数据中…") {
		t.Fatal("没到时间不该有提示字")
	}
	show = true
	tt.Frame()
	if !tt.HasText("取数据中…") || lineH != h0 {
		t.Fatalf("出现后高度 %v → %v，有字=%v", h0, lineH, tt.HasText("取数据中…"))
	}
}

// Wake 在状态机说还要等时安排一帧：Tester 推进到点后重新画，状态机才会变
func TestWakeSchedulesFrameAtThreshold(t *testing.T) {
	var l loadstate.Loader
	l.Begin()
	skeleton := 0
	tt := ui.NewTester(func(c *ui.Context) {
		v := l.View(c.Now())
		Wake(c, v)
		if v.Skeleton {
			skeleton++
		}
	}, 200, 100)
	tt.Frame()
	tt.Advance(299 * time.Millisecond)
	if skeleton != 0 {
		t.Fatalf("299ms 就出骨架了")
	}
	tt.Advance(3 * time.Millisecond)
	if skeleton == 0 {
		t.Fatal("过了阈值没有自己再画一帧")
	}
}

// 只有 draw 为真才画；不画时位置照样占着
func TestBarDrawsOnlyWhenAsked(t *testing.T) {
	draw := false
	var h float32
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).FillWidth().Children(func() { h = Bar(c, 50, 0, draw).LayoutBounds().H })
	}, 300, 100)
	tt.Frame()
	h0, n0 := h, Drawn()
	if h0 <= 0 || n0 != Drawn() {
		t.Fatalf("不画时也该占着高度：%v", h0)
	}
	draw = true
	tt.Frame()
	if Drawn() == n0 {
		t.Fatal("draw 为真却没画")
	}
	if h != h0 {
		t.Fatalf("画出来前后高度变了：%v → %v", h0, h)
	}
}
