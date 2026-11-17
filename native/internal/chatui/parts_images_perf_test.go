package chatui

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 设 CHATUI_PERF=1 时量带图分块消息的排版和一帧的耗时（AGENTS.md 验收要报实测数）；平时跳过。
func TestPerfPartsImages(t *testing.T) {
	if os.Getenv("CHATUI_PERF") == "" {
		t.Skip("设 CHATUI_PERF 才量")
	}
	tk := Derive(DefaultSettings())
	land := MediaResult{State: MediaReady, Image: photoBitmap(320, 240)}
	media := &fixedMedia{byPos: map[int]MediaResult{}}
	var atts []PartAtt
	for i := range 9 {
		media.byPos[i] = land
		atts = append(atts, picAtt(fmt.Sprintf("图%d.png", i), i, 800, 600))
	}
	host := Host{T: tk, SelfChar: CharColors[0], Column: 560, NoEnter: true, Media: media}
	for _, n := range []int{1, 20, 100} {
		msgs := make([]*Msg, n)
		for i := range msgs {
			msgs[i] = partsMsg(fmt.Sprintf("m%d", i), Part{Text: "看这几张", Atts: atts})
		}
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Scroll(c.Key("list")).Size(600, 900).Children(func() {
				ui.Column(c).Width(580).Gap(8).Children(func() {
					for i, m := range msgs {
						host.Message(c.Key(i), m, nil, nil)
					}
				})
			})
		}, 600, 900)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		start := time.Now()
		tt.Frame() // 第一帧：整列构建、排版、绘制（Tester 不做虚拟列表，是最坏情况）
		first := time.Since(start)
		t.Logf("%d 条（每条 9 张图 + 文字）：第一帧 %.1f ms，平均每条 %.2f ms", n, float64(first.Microseconds())/1000, float64(first.Microseconds())/1000/float64(n))
	}
	start := time.Now()
	const rounds = 2000
	m := partsMsg("m", Part{Text: "看这几张", Atts: atts})
	for range rounds {
		_ = host.partsMeasure(m, 400, 480)
	}
	t.Logf("partsMeasure（9 张图 + 文字）：%.1f µs / 次", float64(time.Since(start).Microseconds())/rounds)
}
