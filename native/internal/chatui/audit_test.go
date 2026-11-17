package chatui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 11-06 多代理审查挑出、主人抓过的间距问题：字形四边等距、表情排按块摆、失败角标贴近气泡。
func TestAuditSpacing(t *testing.T) {
	tk := Derive(DefaultSettings())
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local)
	// 字形四边量的是墨迹，系统兜底字体每台机器不一样（CI 上就量歪过）：用仓库里的测试字体，单行气泡只写它有的字
	host := Host{T: tk, Font: registerTinyTestFont(t), SelfChar: CharColors[0], NoEnter: true, Column: 600}
	msgs := []*Msg{
		{ID: "a", Sender: "r1", Char: CharColors[0], Text: "你好 hello"},
		{ID: "b", Sender: "r2", Char: CharColors[1], At: at, Text: "比如这个。", Quote: &Quote{Name: "雾栞", Text: "昨天说好的纸灯"}, Reactions: []Reaction{{Emoji: "👍", Count: 5}}},
		{ID: "c", Sender: "self", Self: true, Text: "这是一条发送失败的消息。", State: Failed},
	}
	view := func(c *ui.Context) {
		ui.Column(c).Size(800, 500).Padding(20).Gap(30).Background(tk.C.ChatBg).Children(func() {
			for _, m := range msgs {
				host.Message(c, m, nil, nil)
			}
		})
	}
	tt := ui.NewTester(view, 800, 500)
	tt.SetScale(4)
	tt.Frame()
	tt.Frame()
	root := tree(tt)

	// 1. 单行气泡：字形到气泡上、下、左差不超过 1
	b := root.path("a", "bubble")
	ink := inkBox(tt, b.find("text").Box)
	gaps := [3]float32{ink.Y - b.Box.Y, b.Box.Y + b.Box.H - ink.Y - ink.H, ink.X - b.Box.X}
	for _, g := range gaps {
		if d := g - gaps[0]; d > 0.6 || d < -0.6 {
			t.Errorf("单行气泡字形到 上 %.2f 下 %.2f 左 %.2f 不等", gaps[0], gaps[1], gaps[2])
			break
		}
	}

	// 2. 表情排是块：离气泡左、下都是竖内边距；引用到正文、正文到表情排都隔竖内边距
	b = root.path("b", "bubble")
	r, q, txt := b.find("reactions"), b.find("quote"), b.find("text")
	d := edges(b.Box, r.Box)
	if !near(d[3], tk.BubblePadV) || !near(d[2], tk.BubblePadV) {
		t.Errorf("表情排到气泡 左 %.2f 下 %.2f，应都是竖内边距 %.2f", d[3], d[2], tk.BubblePadV)
	}
	g1 := txt.Box.Y - (q.Box.Y + q.Box.H)
	g2 := r.Box.Y - (txt.Box.Y + txt.Box.H)
	if !near(g1, tk.BubblePadV) || !near(g2, tk.BubblePadV) {
		t.Errorf("引用到正文 %.2f、正文到表情排 %.2f，应都是竖内边距 %.2f", g1, g2, tk.BubblePadV)
	}

	// 3. 失败角标：圆点右边离气泡左边正好 s1
	b = root.path("c", "bubble")
	badge := root.path("c", "badge")
	if gap := b.Box.X - (badge.Box.X + badge.Box.W); !near(gap, tk.S1) {
		t.Errorf("失败角标离气泡 %.2f，应是 s1 %.2f", gap, tk.S1)
	}
}

// 一个个加表情，时间挂在表情排时不能压到最后一个表情（时间右边距在表情排时另算，预留要跟着算）。
func TestChipsNeverHitTime(t *testing.T) {
	tk := Derive(DefaultSettings())
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local)
	host := Host{T: tk, SelfChar: CharColors[0], NoEnter: true, Column: 420}
	m := &Msg{ID: "x", Sender: "r1", Char: CharColors[0], At: at, Text: "比如这个。"}
	var lay layout
	view := func(c *ui.Context) {
		lay = host.measure(c, m, nil, nil)
		host.Message(c, m, nil, nil)
	}
	tt := ui.NewTester(view, 800, 600)
	emojis := []string{"👍", "😂", "🎉", "🔥", "👀", "❤", "😢", "⭐"}
	for i := 0; i < 24; i++ {
		m.Reactions = append(m.Reactions, Reaction{Emoji: emojis[i%len(emojis)], Count: 4 + i%5})
		tt.Frame()
		tt.Frame()
		if !lay.InfoInline {
			continue
		}
		b := tree(tt).path("x", "bubble")
		tm, r := b.find("time"), b.find("reactions")
		last := lay.Chips[len(lay.Chips)-1]
		if gap := tm.Box.X - (r.Box.X + last.X + last.W); gap < tk.S2-0.75 {
			t.Fatalf("%d 个表情时时间离最后一个表情只有 %.2f", len(m.Reactions), gap)
		}
	}
}

// 表情排第一个按钮（胶囊）在气泡左下角：气泡左下角 = 按钮半径 + 它离气泡的距离（同心）。
func TestChipCornerConcentric(t *testing.T) {
	tk := Derive(DefaultSettings())
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local)
	for _, self := range []bool{false, true} {
		host := Host{T: tk, SelfChar: CharColors[0], NoEnter: true, Column: 600}
		m := &Msg{ID: "x", Sender: "r1", Self: self, Char: CharColors[0], At: at, Text: "好呀。", Reactions: []Reaction{{Emoji: "❤", Count: 1, By: []Reactor{{Name: "晴枝", Char: CharColors[0]}}}}}
		view := func(c *ui.Context) { host.Message(c, m, nil, nil) }
		tt := ui.NewTester(view, 800, 300)
		tt.Frame()
		tt.Frame()
		b := tree(tt).path("x", "bubble")
		chip := b.find("reactions").Children[0]
		d := chip.Box.X - b.Box.X
		if want := chip.Radius[3] + d; !near(b.Radius[3], want) {
			t.Errorf("自己=%v：气泡左下角 %.2f，应 = 按钮半径 %.2f + 距离 %.2f", self, b.Radius[3], chip.Radius[3], d)
		}
	}
}
