package chatui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
	"sumika-native/internal/uitest"
)

// 元素框审计（docs/archive/notes/排查-元素框超出可见内容-2026-11-09.md）两处回归：
//  1. FillWidth 是 100% 宽、不扣外边距：正文块带左右外边距又 FillWidth，框比父元素宽出一个外边距，整行字比气泡左边多缩进 5 像素、右边只剩 3 像素；
//  2. 气泡最宽按列宽的 0.8 算，没扣头像格和操作条：列窄（右栏转发详情）时行放不下，气泡被挤窄，正文已按更宽的排好，字越出气泡。
func boxHost(column float32, group bool) Host {
	h := Host{T: Derive(DefaultSettings()), Column: column, NoEnter: true, MD: NewMD(), Group: group}
	h.OnReply = func(Selected, bool) {}
	return h
}

func boxMsg(self, md bool, text string) *Msg {
	return &Msg{ID: "m", Name: "编的甲", Self: self, Markdown: md, Text: text, At: time.Date(2026, 11, 9, 8, 0, 0, 0, time.Local), Blocks: segview.TextBlocks(text)}
}

func TestBubbleBoxesStayInsideParents(t *testing.T) {
	long := strings.Repeat("这是一句很长很长的话，要折成好几行。", 14) + "\n\n" + strings.Repeat("abcdefghij", 30)
	for _, c := range []struct {
		name        string
		column      float32
		group, self bool
		markdown    bool
	}{
		{"宽列·她的话", 800, false, false, false},
		{"宽列·她的 markdown", 800, false, false, true},
		{"宽列·自己的话", 800, false, true, false},
		{"窄列·群里别人的话（右栏转发详情）", 296, true, false, false},
		{"窄列·群里别人的 markdown", 296, true, false, true},
		{"中等列·群里别人的话", 420, true, false, false},
	} {
		h := boxHost(c.column, c.group)
		tt := ui.NewTester(func(ctx *ui.Context) { h.Message(ctx, boxMsg(c.self, c.markdown, long), nil, nil) }, int(c.column), 1400) // 窗口宽就是列宽：行放不下时才会挤气泡
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		for range 4 {
			tt.Advance(time.Second)
			tt.Frame()
		}
		root := uitest.Tree(tt)
		fs, _ := uitest.Audit(root, ui.Rect{W: c.column, H: 1400})
		for _, f := range fs {
			if f.Class == uitest.ClassOverParent {
				t.Errorf("%s：%v", c.name, f)
			}
		}
		// 每一行字都在气泡里：字的右缘不超过气泡右缘减内边距（左边是多少，右边也该是多少）
		bubble := root.Find("bubble")
		if bubble == nil {
			t.Fatalf("%s：没找到气泡", c.name)
		}
		bubble.Walk(func(n *uitest.Node) {
			if n.Kind == 1 && n.TextW > 0 && n.Box.X+n.TextW > bubble.Box.X+bubble.Box.W-bubble.Pad[1]+0.5 {
				t.Errorf("%s：字的右缘 %.1f 越过气泡内容区右缘 %.1f", c.name, n.Box.X+n.TextW, bubble.Box.X+bubble.Box.W-bubble.Pad[1])
			}
		})
	}
}
