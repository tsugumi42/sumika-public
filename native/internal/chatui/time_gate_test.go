package chatui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// ADR-286：她的回复出字的时候不显示时间，字全部出完才显示；历史消息、被停下的立刻显示。
func TestTimeAfterReveal(t *testing.T) {
	unit := "窗边的纸灯亮起来了，我们慢慢聊。"
	for _, cs := range []struct {
		name  string
		n     int
		every time.Duration
	}{{"慢送 60 字", 60, 50 * time.Millisecond}, {"一次送完 20 字", 20, 0}, {"一次送完 200 字", 200, 0}, {"一次送完 2000 字", 2000, 0}} {
		full := string([]rune(strings.Repeat(unit, cs.n/len([]rune(unit))+1))[:cs.n])
		tk := Derive(DefaultSettings())
		h := Host{T: tk, Column: 720, NoEnter: true, MD: NewMD()}
		m := &Msg{ID: "m", Sender: "r1", Name: "晴枝", At: time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local), State: Streaming, Markdown: true}
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c.Key("list")).Width(h.Column).Children(func() { h.Message(c, m, nil, nil) })
		}, 900, 6000)
		tt.Frame()
		if cs.every > 0 {
			for _, r := range full {
				m.Text += string(r)
				tt.Frame()
				if tt.HasText("12:00") {
					t.Fatalf("%s：出字中显示了时间", cs.name)
				}
				tt.Advance(cs.every)
			}
		} else {
			m.Text = full
			tt.Frame()
		}
		m.State = Sent
		tt.Frame()
		shown := func() bool { return strings.Contains(strings.Join(tt.Texts(), ""), full) }
		var waited, fullAt time.Duration = 0, -1
		for ; !tt.HasText("12:00") && waited < 5*time.Second; waited += 10 * time.Millisecond {
			if fullAt < 0 && shown() {
				fullAt = waited
			}
			tt.Advance(10 * time.Millisecond)
		}
		if !tt.HasText("12:00") {
			t.Fatalf("%s：5 秒后还没有时间", cs.name)
		}
		if !shown() {
			t.Errorf("%s：时间出来时字还没出全（完成后 %v）", cs.name, waited)
		}
		if fullAt < 0 {
			fullAt = waited
		}
		if cs.every > 0 && waited > 250*time.Millisecond {
			t.Errorf("%s：慢慢送的完成时已经跟上，只该等淡入，却等了 %v", cs.name, waited)
		}
		if waited-fullAt > 250*time.Millisecond { // 只该等淡入（mdFade）：mygo 的 markdown.Revealed 报出完
			t.Errorf("%s：字出全以后又等了 %v 才显示时间（出全 %v，时间 %v）", cs.name, waited-fullAt, fullAt, waited)
		}
		t.Logf("%s：完成后 %v 出全，%v 显示时间", cs.name, fullAt, waited)
	}
}

func TestTimeImmediateForHistoryAndStopped(t *testing.T) {
	tk := Derive(DefaultSettings())
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local)
	for _, st := range []State{Sent, Interrupted} {
		h := Host{T: tk, Column: 720, NoEnter: true, MD: NewMD()}
		m := &Msg{ID: "m", Sender: "r1", At: at, Text: strings.Repeat("历史消息。", 200), State: Streaming, Markdown: true}
		if st == Sent {
			m.State = Sent // 一打开就是完成的：没在出字（NewTester 建好就画第一帧，所以要在它之前设）
		}
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c.Key("list")).Width(h.Column).Children(func() { h.Message(c, m, nil, nil) })
		}, 900, 3000)
		if st == Interrupted {
			tt.Frame() // 出过字，然后被停下
			m.State = Interrupted
		}
		tt.Frame()
		tt.Frame()
		if !tt.HasText("12:00") {
			t.Errorf("状态 %d：时间没有立刻显示", st)
		}
	}
}
