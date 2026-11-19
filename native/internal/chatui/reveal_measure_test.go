package chatui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// 量 mygo markdown.Stream 从"消息完成"到全文都显示出来要多久（规格-过程组件 3.7：当初 mygo 没有"出完了"的信号，按这里量的数估；0.8.0 有了 markdown.Revealed 以后只留着对照，
// 也用来核对 Revealed 报得准不准）。设 CHATUI_MEASURE_REVEAL=1 才跑，结果打在日志里。
func TestMeasureRevealCatchUp(t *testing.T) {
	if os.Getenv("CHATUI_MEASURE_REVEAL") == "" {
		t.Skip("设 CHATUI_MEASURE_REVEAL=1 才量")
	}
	unit := "窗边的纸灯亮起来了，我们慢慢聊。"
	for _, cs := range []struct {
		name  string
		n     int           // 字数
		every time.Duration // 送字间隔；0 表示一次送完
	}{{"慢送 200 字（50ms 一个）", 200, 50 * time.Millisecond}, {"一次送完 200 字", 200, 0}, {"一次送完 2000 字", 2000, 0}, {"一次送完 20 字", 20, 0}} {
		full := []rune(strings.Repeat(unit, cs.n/len([]rune(unit))+1))[:cs.n]
		src, done := "", false
		opts := &markdown.Options{Reveal: &markdown.Reveal{Fade: 130 * time.Millisecond}}
		view := func(c *ui.Context) {
			ui.Column(c.Key("col")).Width(600).Children(func() {
				if done {
					markdown.View(c, src, opts)
				} else {
					markdown.Stream(c, src, opts)
				}
			})
		}
		tt := ui.NewTester(view, 700, 4000)
		tt.Frame()
		if cs.every > 0 {
			for i := range full {
				src = string(full[:i+1])
				tt.Frame() // 新字到达时宿主让界面重画（真窗口里是假后端的事件触发的）
				tt.Advance(cs.every)
			}
		} else {
			src = string(full)
			tt.Frame()
		}
		done = true
		tt.Frame()
		shown := func() bool {
			return strings.Contains(strings.ReplaceAll(strings.Join(tt.Texts(), ""), "\n", ""), string(full))
		}
		var waited time.Duration
		for step := 10 * time.Millisecond; !shown() && waited < 10*time.Second; waited += step {
			tt.Advance(step)
		}
		if !shown() {
			got := []rune(strings.ReplaceAll(strings.Join(tt.Texts(), ""), "\n", ""))
			t.Logf("没出全：画出 %d 字，期望 %d 字；末尾 %q", len(got), len(full), string(got[max(0, len(got)-30):]))
		}
		t.Logf("%s：完成后 %v 全文出来（再加淡入 130ms）", cs.name, waited)
	}
}
