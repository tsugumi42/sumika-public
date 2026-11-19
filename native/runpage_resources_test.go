package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/resstat"
	"sumika-native/internal/runmetrics"
)

func fp(v float64) *float64 { return &v }
func ip(v int64) *int64     { return &v }

const mb = int64(1 << 20)

// demoResources 是测试用的资源报告：数字和名字都是编的
func demoResources() ctlproto.ResourcesReport {
	return ctlproto.ResourcesReport{
		SampledAtMS: runNow.UnixMilli(), IntervalMS: 2000, Cores: 8, MemoryKind: ctlproto.MemPrivate,
		GPU: ctlproto.GPUStatus{Available: true},
		Processes: []ctlproto.ProcessRow{
			{PID: 100, Name: "主程序测试.exe", Category: ctlproto.ProcMain, CPU: fp(1.5), Memory: ip(300 * mb), Commit: ip(400 * mb), GPU: fp(3), GPUMemory: ip(150 * mb)},
			{PID: 200, Parent: 100, Depth: 1, Name: "模型测试.exe", Category: ctlproto.ProcModels, CPU: fp(0), Memory: ip(500 * mb), Commit: ip(600 * mb)},
			{PID: 300, Parent: 100, Depth: 1, Name: "命令分析测试.exe", Category: ctlproto.ProcAnalyzer, Memory: ip(70 * mb)}, // CPU 没读到
			{PID: 400, Parent: 100, Depth: 1, Name: "服务测试.exe", Category: ctlproto.ProcMCP, Label: "编造服务", CPU: fp(0.2), Memory: ip(90 * mb)},
			{PID: 500, Parent: 400, Depth: 2, Name: "孙进程测试.exe", Category: ctlproto.ProcUnregistered, CPU: fp(0.1), Memory: ip(10 * mb)},
		},
		Runtime: ctlproto.RuntimeStats{Total: 180 * mb, HeapObj: 90 * mb, Stacks: 6 * mb, HeapLive: 80 * mb, Goroutines: 210, GCCycles: 1520, GCCPU: fp(0.012), PauseP99Ns: ip(420000), Other: ip(120 * mb)},
		Queues: []ctlproto.QueueRow{
			{Key: "model-slots", Label: "模型请求槽位", Wired: true, Backlog: ip(2), Running: ip(4), Capacity: ip(4), OldestWaitMs: ip(45000), State: ctlproto.QueueJammed},
			{Key: "backfill", Label: "补录"},
			{Key: "memory", Label: "记忆整理", Wired: true, Err: "读取失败：编造的原因"},
			{Key: "media", Label: "媒体下载", Wired: true, Running: ip(0), State: ctlproto.QueueIdle},
		},
	}
}

func TestRunResourcesShowsEverything(t *testing.T) {
	f := &fakeStats{res: demoResources()}
	h := newRunHarness(t, f, f, runTabResources, 2048, 1030)
	h.need("进程", "主程序测试.exe", "模型测试.exe", "服务测试.exe · 编造服务", "孙进程测试.exe", "主程序", "本地模型", "MCP 服务", "未登记的子进程",
		"1.5%", "300.0 MB", "主程序内部", "运行时总量", "协程数", "1,520", "420 微秒",
		"后台队列", "模型请求槽位", "补录", "未接入", "读取失败：编造的原因", "堵", "空闲", "45.0 秒",
		"各模块共用一个堆")
	// 没读到的 CPU 写横线，不写 0.0%
	h.need("—")
	// 采样条：间隔选项和刷新
	h.need("1 秒", "2 秒", "5 秒", "暂停", "刷新")
}

// 显存取不到：整列写"不可用"和原因，行里不写 0；原因写在表下面一行
func TestRunResourcesGPUUnavailable(t *testing.T) {
	rep := demoResources()
	rep.GPU = ctlproto.GPUStatus{Reason: "编造：计数器被禁用"}
	for i := range rep.Processes {
		rep.Processes[i].GPU, rep.Processes[i].GPUMemory = nil, nil
	}
	f := &fakeStats{res: rep}
	h := newRunHarness(t, f, f, runTabResources, 2048, 1030)
	h.need("不可用", "显存和 GPU 不可用：编造：计数器被禁用")
}

// 性能计数器还在准备：两列写"—"，没有"不可用"，也没有原因那一行
func TestRunResourcesGPUPending(t *testing.T) {
	rep := demoResources()
	rep.GPU = ctlproto.GPUStatus{Pending: true}
	for i := range rep.Processes {
		rep.Processes[i].GPU, rep.Processes[i].GPUMemory = nil, nil
	}
	f := &fakeStats{res: rep}
	h := newRunHarness(t, f, f, runTabResources, 2048, 1030)
	h.absent("不可用", "显存和 GPU 不可用")
}

// 内存口径退回工作集：列名跟着换，不静默换口径
func TestRunResourcesWorkingSetColumnTitle(t *testing.T) {
	rep := demoResources()
	f := &fakeStats{res: rep}
	h := newRunHarness(t, f, f, runTabResources, 2048, 1030)
	h.need("内存")
	h.absent("工作集")
	rep.MemoryKind = ctlproto.MemWorkingSet
	f.set(func(f *fakeStats) { f.res = rep })
	h.click("刷新")
	h.need("工作集")
}

func TestRunResourcesLoadingErrorStaleAndUnavailable(t *testing.T) {
	// 加载中：只有骨架
	gate := make(chan struct{})
	f := &fakeStats{res: demoResources(), gate: gate}
	a, q := runApp(t, f, f)
	a.run.tab = runTabResources
	tt := newTesterFor(a)
	tt.Frame()
	if a.run.res.has {
		t.Fatal("请求挂着不该有数据")
	}
	if tt.HasText("进程") {
		t.Fatalf("加载中不该有表：%v", tt.Texts())
	}
	close(gate)
	settleMem(q)
	tt.Frame()
	if !tt.HasText("主程序测试.exe") {
		t.Fatalf("放行后该出表：%v", tt.Texts())
	}
	// 读失败：保留旧数据，标已过期
	f.set(func(f *fakeStats) { f.resErr = errors.New("编造的采样错误") })
	if err := tt.Click("刷新"); err != nil {
		t.Fatal(err)
	}
	settleMem(q)
	tt.Frame()
	for _, want := range []string{"已过期", "编造的采样错误", "主程序测试.exe"} {
		if !tt.HasText(want) {
			t.Fatalf("没有 %q：%v", want, tt.Texts())
		}
	}
	// 没有旧数据时出错：给重试
	f2 := &fakeStats{resErr: errors.New("编造的采样错误")}
	h2 := newRunHarness(t, f2, f2, runTabResources, 1100, 800)
	h2.need("没取到资源", "编造的采样错误", "重试")
	f2.set(func(f *fakeStats) { f.resErr, f.res = nil, demoResources() })
	h2.click("重试")
	h2.need("主程序测试.exe")
	// 不可用：写原因，不当出错重试
	f3 := &fakeStats{resErr: &errRunUnavailable{"编造的不可用原因"}}
	h3 := newRunHarness(t, f3, f3, runTabResources, 1100, 800)
	h3.need("资源暂时看不了", "编造的不可用原因")
	h3.absent("重试")
	// 后台没起来
	h4 := newRunHarness(t, nil, nil, runTabResources, 1100, 800)
	h4.need("后台还没起来")
}

// 到点自己采；选"暂停"不起定时器；离开标签停采样并让读服务丢前值。
func TestRunResourcesIntervalPauseAndLeave(t *testing.T) {
	old := runResIntervals
	runResIntervals = [4]time.Duration{150 * time.Millisecond, 150 * time.Millisecond, 150 * time.Millisecond, 0}
	t.Cleanup(func() { runResIntervals = old })
	f := &fakeStats{res: demoResources()}
	h := newRunHarness(t, f, f, runTabResources, 1100, 800)
	deadline := time.Now().Add(3 * time.Second)
	for f.resCount() < 3 && time.Now().Before(deadline) {
		h.q.drain() // 定时器一直在发活，不能用 settleMem 等"静下来"
		h.tt.Frame()
		time.Sleep(20 * time.Millisecond)
	}
	if n := f.resCount(); n < 3 {
		t.Fatalf("资源标签该按间隔自己采：%d 次", n)
	}
	// 暂停：之后不再有采样
	h.click("暂停")
	settleMem(h.q)
	h.need("已暂停，最后一次是")
	n := f.resCount()
	time.Sleep(500 * time.Millisecond)
	h.q.drain()
	if got := f.resCount(); got != n {
		t.Fatalf("暂停以后不该再采：%d → %d", n, got)
	}
	// 点刷新还能手动采一次
	h.click("刷新")
	if got := f.resCount(); got != n+1 {
		t.Fatalf("暂停时手动刷新该采一次：%d → %d", n, got)
	}
	// 切回 2 秒：恢复自动
	h.click("2 秒")
	deadline = time.Now().Add(3 * time.Second)
	for f.resCount() < n+3 && time.Now().Before(deadline) {
		h.q.drain()
		h.tt.Frame()
		time.Sleep(20 * time.Millisecond)
	}
	if got := f.resCount(); got < n+3 {
		t.Fatalf("取消暂停该恢复自动采样：%d", got)
	}
	// 离开标签：订阅取消，读服务被告知丢前值，之后不再采
	h.a.run.tab = runTabUsage
	h.frame()
	if h.a.run.cancel == nil || h.a.run.subTab != runTabUsage {
		t.Fatal("切到用量标签后订阅该换成用量的")
	}
	f.mu.Lock()
	forgets := f.forgets
	f.mu.Unlock()
	deadline = time.Now().Add(2 * time.Second)
	for forgets == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		f.mu.Lock()
		forgets = f.forgets
		f.mu.Unlock()
	}
	if forgets == 0 {
		t.Fatal("离开资源标签要让读服务丢掉采样前值")
	}
	after := f.resCount()
	time.Sleep(500 * time.Millisecond)
	h.q.drain()
	if got := f.resCount(); got != after {
		t.Fatalf("离开资源标签后不该再采：%d → %d", after, got)
	}
}

// 验收⑤：没有订阅者时协程数不涨；资源标签在前台才有定时器，离开、切走页面、窗口隐藏都回到原值
func TestRunResourcesNoGoroutinesWhenAway(t *testing.T) {
	f := &fakeStats{res: demoResources()}
	a, q := runApp(t, f, f)
	a.run.tab = runTabStatus
	tt := newTesterFor(a)
	tt.Frame()
	settleMem(q)
	base := tickersOf(a)
	for i := 0; i < 5; i++ {
		tt.Frame()
		settleMem(q)
	}
	if f.resCount() != 0 || a.run.cancel != nil {
		t.Fatalf("状态标签不该订阅也不该采样：%d", f.resCount())
	}
	a.run.tab = runTabResources
	tt.Frame()
	settleMem(q)
	if a.run.cancel == nil || f.resCount() == 0 {
		t.Fatal("资源标签在前台要有订阅、要采一次")
	}
	if n := tickersOf(a); n <= base {
		t.Fatalf("订阅期间应该多出定时器协程：%d -> %d", base, n)
	}
	a.rail = 0
	tt.Frame()
	settleMem(q)
	if a.run.cancel != nil {
		t.Fatal("离开运行页要取消订阅")
	}
	waitTickers(t, a, base)
	a.rail = 3
	tt.Frame()
	settleMem(q)
	a.away.Store(true)
	a.runSync()
	if a.run.cancel != nil {
		t.Fatal("窗口隐藏要取消订阅")
	}
	waitTickers(t, a, base)
}

// 暂停时订阅在、定时器不在：协程数不涨。
func TestRunResourcesPausedHasNoTicker(t *testing.T) {
	f := &fakeStats{res: demoResources()}
	a, q := runApp(t, f, f)
	a.run.tab = runTabStatus
	tt := newTesterFor(a)
	tt.Frame()
	settleMem(q)
	base := tickersOf(a)
	a.run.resPick = len(runResIntervals) // 暂停（下标加一）
	a.run.tab = runTabResources
	tt.Frame()
	settleMem(q)
	if a.run.cancel == nil {
		t.Fatal("暂停时仍有订阅")
	}
	waitTickers(t, a, base)
}

// 三个宽度都放得下。
func TestRunResourcesThreeWidths(t *testing.T) {
	for _, w := range []int{480, 1100, 2048} {
		f := &fakeStats{res: demoResources()}
		h := newRunHarness(t, f, f, runTabResources, w, 900)
		h.need("进程", "后台队列", "主程序内部")
	}
}

// 用本进程真的采一遍再画：页面上能看到主程序，队列表带着真注册表里的行。
func TestRunResourcesWithRealSampler(t *testing.T) {
	reg := &runmetrics.Registry{}
	reg.Register(runmetrics.KeyModelSlots, 0, func() (runmetrics.Reading, error) {
		return runmetrics.Reading{Running: runmetrics.I(1), Capacity: runmetrics.I(4), Backlog: runmetrics.I(0)}, nil
	})
	src := &samplerStats{fakeStats: &fakeStats{}, s: resstat.New(resstat.Options{Queues: reg})}
	h := newRunHarness(t, src, src, runTabResources, 1400, 900)
	h.need("主程序", "模型请求槽位", "补录", "未接入", "协程数")
	rep := h.a.run.res.data
	if len(rep.Processes) == 0 || rep.Processes[0].Category != ctlproto.ProcMain || rep.Runtime.Goroutines <= 0 {
		t.Fatalf("真采样的报告不对：%+v", rep.Processes)
	}
}

type samplerStats struct {
	*fakeStats
	s *resstat.Sampler
}

func (x *samplerStats) Resources(ctx context.Context) (ctlproto.ResourcesReport, error) {
	return x.s.Sample(ctx, true)
}

func (x *samplerStats) ForgetResources() { x.s.Forget() }

// 格式化：没有的数写横线，有的数按单位写
func TestResourceFormatters(t *testing.T) {
	t.Parallel()
	if fmtCPU(nil) != "—" || fmtCPU(fp(0)) != "0.0%" || fmtCPU(fp(12.345)) != "12.3%" {
		t.Fatal("CPU 的写法不对")
	}
	if fmtBytesPtr(nil) != "—" || fmtBytesPtr(ip(0)) != "0 字节" || fmtBytesPtr(ip(5*mb)) != "5.0 MB" {
		t.Fatal("字节数的写法不对")
	}
	if fmtCountPtr(nil) != "—" || fmtCountPtr(ip(0)) != "0" || fmtCountPtr(ip(1234)) != "1,234" {
		t.Fatal("个数的写法不对")
	}
	if fmtNs(420000) != "420 微秒" || fmtNs(2_500_000) != "2.5 毫秒" {
		t.Fatalf("暂停的写法不对：%s %s", fmtNs(420000), fmtNs(2_500_000))
	}
	for k, want := range map[string]string{ctlproto.QueueIdle: "空闲", ctlproto.QueueBusy: "忙", ctlproto.QueueJammed: "堵", ctlproto.QueueUnknown: "", "": ""} {
		if got := queueStateLabel(k); got != want {
			t.Fatalf("状态 %q：%q，想要 %q", k, got, want)
		}
	}
	for _, c := range []string{ctlproto.ProcMain, ctlproto.ProcModels, ctlproto.ProcAnalyzer, ctlproto.ProcMCP, ctlproto.ProcShell, ctlproto.ProcMedia, ctlproto.ProcUnregistered} {
		if categoryLabel(c) == c {
			t.Fatalf("类别 %s 没有中文名", c)
		}
	}
}

// 回归：换采样间隔只重起定时器，不能让采样器丢前值（否则紧接着一个周期 CPU / GPU 列显示"—"）。
// 只有真正离开资源标签才丢，所以整段下来读服务只被告知一次。
func TestRunResourcesIntervalChangeKeepsSamplerState(t *testing.T) {
	f := &fakeStats{res: demoResources()}
	h := newRunHarness(t, f, f, runTabResources, 1100, 800)
	h.click("5 秒")
	settleMem(h.q)
	if h.a.run.cancel == nil || h.a.run.subTab != runTabResources {
		t.Fatal("换间隔后订阅该仍在资源标签上")
	}
	h.a.run.tab = runTabUsage
	h.frame()
	count := func() int { f.mu.Lock(); defer f.mu.Unlock(); return f.forgets }
	deadline := time.Now().Add(2 * time.Second)
	for count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// 留一小段让可能多出来的那次（旧写法换间隔也丢）赶上
	time.Sleep(200 * time.Millisecond)
	if got := count(); got != 1 {
		t.Fatalf("只有离开标签才该丢前值，换间隔不该：丢了 %d 次", got)
	}
}
