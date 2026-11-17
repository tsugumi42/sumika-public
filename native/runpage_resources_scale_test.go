package main

import (
	"io"
	"log"
	"os"
	"testing"
	"time"
)

// TestRunResourcesFrames 量资源标签的一帧耗时（规格-运行页 第十二节第 5 步验收④）：演示数据里十来个进程，
// 每次刷新换一份新结果。口径和 TestRunPageScaleFrames 一样：一帧的耗时从 mygo 的帧日志取，
// 要设 SUMIKA_RUNPAGE_FRAMES=1 和 MYGO_FRAME_STATS=all，日常短测不跑。
func TestRunResourcesFrames(t *testing.T) {
	if os.Getenv("SUMIKA_RUNPAGE_FRAMES") == "" {
		t.Skip("SUMIKA_RUNPAGE_FRAMES 没设")
	}
	if os.Getenv("MYGO_FRAME_STATS") == "" {
		t.Fatal("要设 MYGO_FRAME_STATS=all：一帧的耗时从 mygo 的帧日志里取")
	}
	d := buildDemoStats(10, 30, func() time.Time { return runNow })
	tap := &frameTap{}
	log.SetOutput(io.MultiWriter(os.Stderr, tap))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for _, w := range []int{480, 1100, 2048} {
		a, q := runApp(t, d, d)
		a.run.tab = runTabResources
		tt := newTesterForSize(a, w, 1030)
		tt.SetScale(1.25)
		tt.Frame()
		settleDrain(q)
		tt.Frame()
		tap.take()
		for i := 0; i < 60; i++ {
			tt.Frame()
		}
		p50, p95, max := pctF(tap.take())
		var refresh []time.Duration
		for i := 0; i < 8; i++ {
			a.runFetchResources()
			settleDrain(q)
			start := time.Now()
			tt.Frame()
			refresh = append(refresh, time.Since(start))
		}
		rp50, _, rmax := pct(refresh)
		log.Printf("RUNPAGE RESOURCES width=%d 一帧 p50=%.2fms p95=%.2fms max=%.2fms；换数据后的第一次 Tester.Frame（约 20 帧）p50=%.1fms max=%.1fms", w, p50, p95, max, rp50, rmax)
	}
}
