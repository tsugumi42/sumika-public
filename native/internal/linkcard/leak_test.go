package linkcard

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// 连续画很多张卡，量进程私有内存。mygo 的每个 Tester 都带一整套 DirectWrite 字体且没有释放接口，
// 每张卡新建一个就一张卡漏一套（本机量过：画一遍 linkexpand 的测试峰值 1.6 GB）。
// 规模测试，-short 不跑，放每日整套；日志里每隔 10 张记一行。
func TestRenderMemoryStaysFlat(t *testing.T) {
	if testing.Short() {
		t.Skip("规模测试，放每日整套")
	}
	if _, ok := privateBytes(); !ok {
		t.Skip("只在 Windows 上量私有内存")
	}
	r := testRenderer(t, Options{Theme: func() string { return ThemeLight }})
	const total, warm, step = 60, 10, 10
	var base, last uint64
	for i := 1; i <= total; i++ {
		var err error
		if i%3 == 0 {
			_, err = r.Render(context.Background(), Data{Title: fmt.Sprintf("第 %d 张通用卡片", i), Author: "纸灯实验室", DateDuration: "2026-11-01 18:30", Stats: []string{"播放 1｜点赞 2"}, Cover: swatch(320, 180, sky, leaf)})
		} else {
			tw := sampleTweet()
			tw.Text = strings.Repeat(fmt.Sprintf("第%d张。", i), 20)
			tw.Author.Avatar = swatch(96, 96, sky, plum)
			if i%3 == 1 {
				tw.Media = []TweetMedia{{Image: swatch(400, 300, sky, leaf)}}
			}
			_, err = r.RenderTweet(context.Background(), tw)
		}
		if err != nil {
			t.Fatal(err)
		}
		if i%step == 0 {
			runtime.GC()
			p, _ := privateBytes()
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			t.Logf("张数=%d 私有内存=%d MB Go 堆=%d MB", i, p>>20, ms.HeapAlloc>>20)
			if i == warm {
				base = p
			}
			last = p
		}
	}
	// 热身 10 张之后再画 50 张，私有内存涨幅不超过 200 MB（泄漏时是每张几十 MB）
	if grow := int64(last) - int64(base); grow > 200<<20 {
		t.Fatalf("热身后再画 %d 张，私有内存涨了 %d MB：每张卡都在漏原生内存", total-warm, grow>>20)
	}
}
