package main

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/ctlproto"
)

// 用量标签（规格-运行页 第十二节第 3 步验收①③⑤）：每个状态一条测试，三个宽度，没有订阅者时没有定时器。

func usageWith(tasks ...ctlproto.UsageTask) ctlproto.UsageReport {
	rep := ctlproto.UsageReport{Tasks: tasks}
	for _, t := range tasks {
		addStats(&rep.Totals, t.UsageStats)
	}
	finishStats(&rep.Totals)
	return rep
}

func liveTask(key, label string, runs, input int64) ctlproto.UsageTask {
	st := ctlproto.UsageStats{Runs: runs, OKRuns: runs, OKMs: runs * 1200}
	st.Input, st.CacheRead, st.Output, st.Reasoning = input, input/2, input/10, input/20
	finishStats(&st)
	return ctlproto.UsageTask{Key: key, Label: label, Wired: true, UsageStats: st}
}

func TestRunUsageEmpty(t *testing.T) {
	f := &fakeStats{rep: usageWith(ctlproto.UsageTask{Key: ctlproto.TaskForeground, Label: "前台回合", Wired: true})}
	h := newRunHarness(t, f, f, runTabUsage, 1100, 800)
	h.need("这段时间没有记录", "改成 30 天", "时间范围")
	h.absent("总输入")
	h.click("改成 30 天")
	if h.a.run.rangeKey != "30d" {
		t.Fatalf("点了要换成 30 天：%q", h.a.run.rangeKey)
	}
	h.until("换范围重新读", func() bool { u, _ := f.calls(); return u >= 2 })
	h.absent("改成 30 天") // 已经是 30 天，不再给这个按钮
}

func TestRunUsageLoadingThenReady(t *testing.T) {
	f := &fakeStats{rep: usageWith(liveTask(ctlproto.TaskForeground, "前台回合", 12, 90000)), gate: make(chan struct{})}
	a, q := runApp(t, f, f)
	a.run.tab = runTabUsage
	tt := newTesterFor(a)
	tt.Frame()
	if !a.run.usage.loading || a.run.usage.has {
		t.Fatalf("请求挂着时该是加载中：%+v", a.run.usage)
	}
	if tt.HasText("总输入") || tt.HasText("这段时间没有记录") || tt.HasText("没取到用量") {
		t.Fatalf("加载中只显示骨架：%v", tt.Texts())
	}
	f.set(func(f *fakeStats) { close(f.gate); f.gate = nil })
	settleMem(q)
	tt.Frame()
	if !tt.HasText("总输入") || !tt.HasText("前台回合") {
		t.Fatalf("读到以后要显示数据：%v", tt.Texts())
	}
}

func TestRunUsageErrorKeepsOldDataAsStale(t *testing.T) {
	f := &fakeStats{rep: usageWith(liveTask(ctlproto.TaskForeground, "前台回合", 12, 90000))}
	h := newRunHarness(t, f, f, runTabUsage, 1100, 800)
	h.need("前台回合", "9.0 万")
	h.absent("已过期")
	f.set(func(f *fakeStats) { f.repErr = errors.New("统计超时") })
	h.click("刷新")
	h.need("已过期", "前台回合", "9.0 万") // 旧数据还在
	f.set(func(f *fakeStats) { f.repErr = nil })
	h.click("刷新")
	h.absent("已过期")
}

func TestRunUsageErrorWithoutDataAndUnavailable(t *testing.T) {
	f := &fakeStats{repErr: errors.New("读运行记录库出错")}
	h := newRunHarness(t, f, f, runTabUsage, 1100, 800)
	h.need("没取到用量", "读运行记录库出错", "重试")
	f.set(func(f *fakeStats) { f.repErr = nil; f.rep = usageWith(liveTask(ctlproto.TaskGuard, "守卫", 3, 3000)) })
	h.click("重试")
	h.need("守卫")

	// 不可用：库还没建，不当出错重试
	g := &fakeStats{repErr: &errRunUnavailable{"还没有运行记录库，没有可统计的"}}
	h2 := newRunHarness(t, g, g, runTabUsage, 1100, 800)
	h2.need("用量暂时看不了", "还没有运行记录库，没有可统计的")
	h2.absent("重试")

	// 后台没起来
	h3 := newRunHarness(t, nil, nil, runTabUsage, 1100, 800)
	h3.need("后台还没起来")
}

func TestRunUsageReservedRowsSayNotWired(t *testing.T) {
	f := &fakeStats{rep: usageWith(
		liveTask(ctlproto.TaskForeground, "前台回合", 12, 90000),
		ctlproto.UsageTask{Key: ctlproto.TaskGoal, Label: "/goal 评判"},
		ctlproto.UsageTask{Key: ctlproto.TaskLoop, Label: "/loop"},
	)}
	h := newRunHarness(t, f, f, runTabUsage, 1100, 800)
	h.need("/goal 评判", "/loop", "未接入")
	n := 0
	for _, s := range h.tt.Texts() {
		if s == "未接入" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("两个预留行各写一次\"未接入\"，不写 0：%d 次 %v", n, h.tt.Texts())
	}
	// 预留行排在最后，不参与按输入降序
	_, roots := h.a.run.usageRows(f.rep)
	if len(roots) != 3 || roots[0] != ctlproto.TaskForeground {
		t.Fatalf("预留行要沉底：%v", roots)
	}
}

func TestRunUsageUnwiredWithHistoryKeepsNumbers(t *testing.T) {
	vision := liveTask(ctlproto.TaskVision, "识图", 6202, 5000000)
	vision.Wired = false
	f := &fakeStats{rep: usageWith(liveTask(ctlproto.TaskForeground, "前台回合", 12, 90000), vision)}
	h := newRunHarness(t, f, f, runTabUsage, 1100, 800)
	h.need("识图", "Go 侧未接入", "6,202", "500.0 万")
}

func TestRunUsageNoUsageFootnote(t *testing.T) {
	task := liveTask(ctlproto.TaskForeground, "前台回合", 12, 90000)
	task.NoUsage = 3
	f := &fakeStats{rep: usageWith(task)}
	h := newRunHarness(t, f, f, runTabUsage, 1100, 800)
	found := false
	for _, s := range h.tt.Texts() {
		if strings.Contains(s, "3 次没拿到用量，未计入") {
			found = true
		}
	}
	if !found {
		t.Fatalf("表脚要写没拿到用量的次数：%v", h.tt.Texts())
	}
}

func TestRunUsageThreeWidths(t *testing.T) {
	rep := demoUsage(t)
	for _, w := range []int{380, 1100, 2048} {
		f := &fakeStats{rep: rep}
		h := newRunHarness(t, f, f, runTabUsage, w, 900)
		h.need("总输入", "前台回合", "时间范围")
		for _, label := range []string{"总输入", "时间范围"} {
			r, ok := h.tt.Find(label)
			if !ok || r.X < 0 || r.X+r.W > float32(w) {
				t.Fatalf("宽 %d：%q 越界 %+v", w, label, r)
			}
		}
	}
}

func TestRunUsageFiltersAndTabClick(t *testing.T) {
	f := &fakeStats{rep: demoUsage(t)}
	a, q := runApp(t, f, f)
	tt := newTesterFor(a)
	tt.Frame()
	if a.run.cancel != nil {
		t.Fatal("状态标签在前台，不该有订阅")
	}
	if err := tt.Click("用量"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		settleMem(q)
		tt.Frame()
	}
	if a.run.tab != runTabUsage || !tt.HasText("总输入") {
		t.Fatalf("点标签要切到用量：%v", tt.Texts())
	}
	a.run.taskPick = ctlproto.TaskGuard
	rows, roots := a.run.usageRows(a.run.usage.data)
	if len(roots) != 1 || rows[roots[0]].label != "守卫" {
		t.Fatalf("类别筛选只留一行：%v", roots)
	}
	a.run.taskPick, a.run.modelPick = "", "agnes-2.5-flash"
	_, roots = a.run.usageRows(a.run.usage.data)
	if len(roots) == 0 {
		t.Fatal("模型筛选后应该还有行")
	}
	for _, k := range roots {
		if strings.Contains(k, "/") {
			t.Fatalf("筛了模型就不再展开按模型的子行：%v", roots)
		}
	}
}

// 验收⑤：没有订阅者时没有定时器、没有常驻协程；进了用量标签才有，离开就没
func TestRunPageNoSubscriberNoGoroutines(t *testing.T) {
	f := &fakeStats{rep: demoUsage(t), store: demoStorage(3, runNow)}
	a, q := runApp(t, f, f)
	a.run.tab = runTabStatus
	tt := newTesterFor(a)
	tt.Frame()
	settleMem(q)
	base := tickersOf(a) // 只数这个 App 自己的定时器协程：数全进程协程会被别的测试留下的协程进出带偏
	for i := 0; i < 5; i++ {
		tt.Frame()
		settleMem(q)
	}
	if u, s := f.calls(); a.run.cancel != nil || u+s != 0 {
		t.Fatalf("状态标签不该订阅也不该读数：%d %d", u, s)
	}
	if n := tickersOf(a); n > base {
		t.Fatalf("没有订阅者时定时器协程数不该涨：%d -> %d", base, n)
	}
	// 进用量：有订阅，协程数多出定时器那一个
	a.run.tab = runTabUsage
	tt.Frame()
	settleMem(q)
	if a.run.cancel == nil {
		t.Fatal("用量标签在前台要有订阅")
	}
	if n := tickersOf(a); n <= base {
		t.Fatalf("订阅期间应该多出定时器协程：%d -> %d", base, n)
	}
	// 离开运行页：订阅、定时器取消，协程数回到原来
	a.rail = 0
	tt.Frame()
	settleMem(q)
	if a.run.cancel != nil {
		t.Fatal("离开运行页要取消订阅")
	}
	waitTickers(t, a, base)
	// 窗口隐藏同理
	a.rail = 3
	tt.Frame()
	settleMem(q)
	if a.run.cancel == nil {
		t.Fatal("回到运行页要重新订阅")
	}
	a.away.Store(true)
	a.runSync()
	if a.run.cancel != nil {
		t.Fatal("窗口隐藏要取消订阅")
	}
	waitTickers(t, a, base)
}

// tickersOf 数这个 App 的运行页定时器协程（runTicker）有几个：从全进程的协程栈里按 App 指针认。
// 不数 runtime.NumGoroutine：它连别的测试、别的 App 留下的协程一起数，进出一个就判错（偶发红）。
func tickersOf(a *app) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	want := fmt.Sprintf("(*app).runTicker(%p", a)
	return strings.Count(string(buf), want)
}

func waitTickers(t *testing.T, a *app, base int) {
	t.Helper()
	var n int
	for i := 0; i < 150; i++ {
		if n = tickersOf(a); n <= base {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("定时器协程数没回到 %d：%d", base, n)
}

// 自动刷新：范围含今天时到点自己读
func TestRunUsageAutoRefresh(t *testing.T) {
	old := runRefresh
	runRefresh = 150 * time.Millisecond // 比 settleMem 判“静下来”的窗口长，否则它每次都等满 5 秒
	t.Cleanup(func() { runRefresh = old })
	f := &fakeStats{rep: demoUsage(t)}
	h := newRunHarness(t, f, f, runTabUsage, 1100, 800)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.q.drain() // 定时器一直在发活，不能用 settleMem 等"静下来"
		h.tt.Frame()
		if u, _ := f.calls(); u >= 3 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	u, _ := f.calls()
	t.Fatalf("近 7 天含今天，应该自动刷新：%d 次", u)
}

// build 里不重算聚合：数据没变、筛选没变、排序没变，画再多帧也只算一次；变了才算（回归：聚合曾经每帧重做）
func TestRunUsageAggregateNotRebuiltPerFrame(t *testing.T) {
	f := &fakeStats{rep: demoUsage(t), store: demoStorage(10, runNow)}
	h := newRunHarness(t, f, f, runTabUsage, 1100, 800)
	cache := &h.a.run.usageTbl.cache
	for i := 0; i < 30; i++ {
		h.tt.Frame()
	}
	if cache.builds != 1 {
		t.Fatalf("30 帧只该算 1 次：%d", cache.builds)
	}
	h.click("刷新") // 新数据到了：重算
	for i := 0; i < 10; i++ {
		h.tt.Frame()
	}
	if cache.builds != 2 {
		t.Fatalf("刷新后该再算 1 次：%d", cache.builds)
	}
	h.a.run.taskPick = ctlproto.TaskGuard // 筛选变了：重算
	h.tt.Frame()
	h.tt.Frame()
	if cache.builds != 3 {
		t.Fatalf("筛选变了该再算 1 次：%d", cache.builds)
	}
	h.a.run.usageTbl.sort = ui.SortOrder{Column: "runs"} // 排序变了：重算
	h.tt.Frame()
	h.tt.Frame()
	if cache.builds != 4 {
		t.Fatalf("排序变了该再算 1 次：%d", cache.builds)
	}
	// 存储同理
	h.a.run.tab = runTabStorage
	for i := 0; i < 10; i++ {
		h.frame()
	}
	sc := &h.a.run.storeTbl.cache
	for i := 0; i < 30; i++ {
		h.tt.Frame()
	}
	if sc.builds != 1 {
		t.Fatalf("存储 30 帧只该算 1 次：%d", sc.builds)
	}
}
