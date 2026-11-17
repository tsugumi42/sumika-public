package main

import (
	"io"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
)

// frameTap 截住 MYGO_FRAME_STATS=all 写出的每帧耗时。Tester.Frame 一次会连画约 20 帧（等动画和布局稳定），
// 所以 Tester.Frame 的墙钟时间是 20 帧的总和，不是一帧；一帧的耗时要从 mygo 自己的帧日志里取
type frameTap struct {
	mu  sync.Mutex
	got []float64
}

var frameLine = regexp.MustCompile(`mygo: frame \d+ took ([\d.]+) ms`)

func (f *frameTap) Write(b []byte) (int, error) {
	if m := frameLine.FindSubmatch(b); m != nil {
		v, _ := strconv.ParseFloat(string(m[1]), 64)
		f.mu.Lock()
		f.got = append(f.got, v)
		f.mu.Unlock()
	}
	return len(b), nil
}

// take 取走到现在为止收到的帧耗时
func (f *frameTap) take() []float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.got
	f.got = nil
	return g
}

func pctF(v []float64) (p50, p95, max float64) {
	if len(v) == 0 {
		return
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	return c[len(c)/2], c[len(c)*95/100], c[len(c)-1]
}

// TestRunPageScaleFrames 是运行页第 3 步验收②：十个角色加一年的数据，量元素数和帧耗时。
// 帧耗时是 Tester 里一帧（构建、布局、绘制）的墙钟时间；配合 MYGO_FRAME_STATS=all 时 mygo 另把每帧分阶段耗时写到标准错误。
// 没设 SUMIKA_RUNPAGE_FRAMES 就跳过，日常短测不跑。
func TestRunPageScaleFrames(t *testing.T) {
	if os.Getenv("SUMIKA_RUNPAGE_FRAMES") == "" {
		t.Skip("SUMIKA_RUNPAGE_FRAMES 没设")
	}
	d := buildDemoStats(10, 365, func() time.Time { return runNow })
	tap := &frameTap{}
	log.SetOutput(io.MultiWriter(os.Stderr, tap))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if os.Getenv("MYGO_FRAME_STATS") == "" {
		t.Fatal("要设 MYGO_FRAME_STATS=all：一帧的耗时从 mygo 的帧日志里取")
	}
	for _, w := range []int{480, 1100, 2048} {
		// 基线：同一个窗口、同样每帧强制重建，页面只有"状态"标签（没有表格）。它是这个窗口外壳（导航栏、标题、标签栏、
		// 整窗绘制）本身的帧耗时，用量、存储页的耗时扣掉它才是页面自己的部分
		{
			a, q := runApp(t, d, d)
			a.run.tab = runTabStatus
			tt := newTesterForSize(a, w, 1030)
			tt.SetScale(1.25)
			tt.Frame()
			settleDrain(q)
			var base []time.Duration
			for i := 0; i < 60; i++ {
				start := time.Now()
				tt.Frame()
				base = append(base, time.Since(start))
			}
			sort.Slice(base, func(i, j int) bool { return base[i] < base[j] })
			log.Printf("RUNPAGE BASELINE tab=状态 width=%d p50=%.2fms p95=%.2fms", w, ms(base[len(base)/2]), ms(base[len(base)*95/100]))
		}
		for _, tab := range []int{runTabUsage, runTabStorage} {
			a, q := runApp(t, d, d)
			a.run.tab = tab
			a.run.rangeKey = "all" // 一整年
			a.run.now = func() time.Time { return runNow }
			tt := newTesterForSize(a, w, 1030)
			tt.SetScale(1.25)
			var samples []time.Duration
			frame := func() {
				start := time.Now()
				tt.Frame()
				samples = append(samples, time.Since(start))
			}
			// 打开：第一帧是骨架，结果回来后第一帧才真正画出表
			frame()
			settleDrain(q)
			var open time.Duration
			for i := 0; i < 3; i++ {
				start := time.Now()
				tt.Frame()
				if dur := time.Since(start); dur > open {
					open = dur
				}
			}
			// 稳态：数据不变，连画 60 次
			samples = samples[:0]
			tap.take()
			for i := 0; i < 60; i++ {
				frame()
			}
			steady := append([]time.Duration(nil), samples...)
			oneP50, oneP95, oneMax := pctF(tap.take()) // 一帧（mygo 的帧）的耗时
			// 刷新：每次换一份新结果，量换数据后的第一帧
			var refresh []time.Duration
			for i := 0; i < 8; i++ {
				if tab == runTabUsage {
					a.runFetchUsage()
				} else {
					a.runFetchStorage()
				}
				settleDrain(q)
				start := time.Now()
				tt.Frame()
				refresh = append(refresh, time.Since(start))
			}
			// 次表滚动（用量）：按天 365 行
			var scroll []time.Duration
			if tab == runTabUsage {
				for i := 0; i < 40; i++ {
					a.run.dayList.ScrollTo(i*8, 0)
					start := time.Now()
					tt.Frame()
					scroll = append(scroll, time.Since(start))
				}
			}
			p50, p95, max := pct(steady)
			rp50, _, rmax := pct(refresh)
			sp50, _, smax := 0.0, 0.0, 0.0
			if len(scroll) > 0 {
				sp50, _, smax = pct(scroll)
			}
			samples = steady
			elements := countElements(t, tt)
			name := map[int]string{runTabUsage: "用量", runTabStorage: "存储"}[tab]
			rows := 0
			if tab == runTabUsage {
				rows = a.run.usageTbl.out.Rows()
			} else {
				rows = a.run.storeTbl.out.Rows()
			}
			days := 0
			if a.run.usage.has {
				days = len(a.run.usage.data.Days)
			}
			log.Printf("RUNPAGE RESULT tab=%s width=%d elements=%d tableRows=%d days=%d open=%.2fms oneFrame(p50/p95/max)=%.2f/%.2f/%.2fms perTesterFrame(p50/p95/max)=%.2f/%.2f/%.2fms refresh(p50/max)=%.2f/%.2fms scroll(p50/max)=%.2f/%.2fms",
				name, w, elements, rows, days, ms(open), oneP50, oneP95, oneMax, p50, p95, max, rp50, rmax, sp50, smax)
		}
	}
}

// pct 返回 p50、p95、最大（毫秒）
func pct(d []time.Duration) (p50, p95, max float64) {
	c := append([]time.Duration(nil), d...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return ms(c[len(c)/2]), ms(c[len(c)*95/100]), ms(c[len(c)-1])
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// settleDrain 倒界面线程的队列直到静下来（后台线程的结果会引出新的活）；订阅的定时器在这里不会插进来，因为间隔是 30 秒
func settleDrain(q *uiQueue) { settleMem(q) }
