package chatui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 时间到气泡右边和下边的距离（按像素量时间数字本身，不是文字框）要相等（主人 11-06 截图）。
func TestTimeCornerGap(t *testing.T) {
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local)
	for _, dark := range []bool{false, true} {
		s := DefaultSettings()
		s.Dark = dark
		tk := Derive(s)
		for _, cs := range []struct {
			name string
			m    *Msg
		}{
			{"单行同一行", &Msg{ID: "x", Sender: "r1", Char: CharColors[0], At: at, Text: "我也在。"}},
			{"多行同一行", &Msg{ID: "x", Sender: "r1", Char: CharColors[0], At: at, Text: "今天想聊些什么？可以从早上发生的小事说起，也可以随便聊聊别的。"}},
			{"另起一行", &Msg{ID: "x", Sender: "r1", Char: CharColors[0], At: at, Text: strings.Repeat("字", 40)}},
			{"表情排", &Msg{ID: "x", Sender: "r1", Char: CharColors[0], At: at, Text: "比如这个。", Reactions: []Reaction{{Emoji: "👍", Count: 5}}}},
			{"自己的", &Msg{ID: "x", Sender: "self", Self: true, At: at, Text: "先把想到的慢慢说吧。"}},
		} {
			host := Host{T: tk, SelfChar: CharColors[0], NoEnter: true, Column: 400}
			view := func(c *ui.Context) {
				ui.Column(c).Size(800, 400).Padding(20).Background(tk.C.ChatBg).Children(func() { host.Message(c, cs.m, nil, nil) })
			}
			tt := ui.NewTester(view, 800, 400)
			tt.SetScale(4)
			tt.Frame()
			tt.Frame()
			b := tree(tt).path("x", "bubble")
			tm := b.find("time")
			ink := inkBox(tt, tm.Box) // 只扫时间文字框，别扫进气泡角上的弧形细边
			right := b.Box.X + b.Box.W - (ink.X + ink.W)
			bottom := b.Box.Y + b.Box.H - (ink.Y + ink.H)
			name := fmt.Sprintf("深%v %s", dark, cs.name)
			t.Logf("%s：时间数字到气泡 右 %.2f 下 %.2f", name, right, bottom)
			if d := right - bottom; d > 1 || d < -1 {
				t.Errorf("%s：时间到气泡右 %.2f、下 %.2f 不等", name, right, bottom)
			}
		}
	}
}
