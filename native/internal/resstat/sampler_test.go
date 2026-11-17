package resstat

import (
	"context"
	"errors"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/procreg"
	"sumika-native/internal/runmetrics"
)

// 用假的系统和假的显存来源测逻辑：CPU 怎么算、内存口径、pid 复用、登记和未登记、显存不可用

type fakeSys struct {
	procs     []procEntry
	infos     map[int]procInfo
	created   map[int]int64
	listFail  bool
	gone      map[int]bool
	listCalls int
}

func (f *fakeSys) list() ([]procEntry, error) {
	f.listCalls++
	if f.listFail {
		return nil, errors.New("列不出来")
	}
	return f.procs, nil
}

func (f *fakeSys) info(pid int) (procInfo, error) {
	if f.gone[pid] {
		return procInfo{}, errGone
	}
	if in, ok := f.infos[pid]; ok {
		return in, nil
	}
	return procInfo{}, errors.New("没权限")
}

func (f *fakeSys) creation(pid int) int64 { return f.created[pid] }

type fakeGPU struct {
	r      GPUReading
	err    error
	closed int
	reads  int
}

func (g *fakeGPU) Read() (GPUReading, error) { g.reads++; return g.r, g.err }
func (g *fakeGPU) Close()                    { g.closed++ }

func pinfo(created int64, cpu time.Duration, private int64) procInfo {
	return procInfo{created: created, cpu: cpu, hasCPU: true, privateWS: private, hasPrivate: true, workingSet: private + 1, hasWS: true, commit: private * 2, hasCommit: true}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newFake(t *testing.T) (*Sampler, *fakeSys, *procreg.Registry, *clock, *fakeGPU) {
	t.Helper()
	sys := &fakeSys{
		procs: []procEntry{
			{pid: 100, ppid: 1, name: "sumika.exe"},
			{pid: 200, ppid: 100, name: "sumika-models.exe"},
			{pid: 300, ppid: 100, name: "pwsh.exe"},
			{pid: 400, ppid: 300, name: "git.exe"},
			{pid: 500, ppid: 1, name: "别人的程序.exe"},
		},
		infos: map[int]procInfo{
			100: pinfo(1, 10*time.Second, 300<<20),
			200: pinfo(2, 20*time.Second, 200<<20),
			300: pinfo(3, 1*time.Second, 80<<20),
			400: pinfo(4, 0, 5<<20),
			500: pinfo(5, 1*time.Second, 1<<30),
		},
		created: map[int]int64{100: 1, 200: 2, 300: 3, 400: 4, 500: 5},
	}
	reg := &procreg.Registry{CreatedOf: func(pid int) int64 { return sys.created[pid] }}
	clk := &clock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	g := &fakeGPU{r: GPUReading{Memory: map[int]int64{100: 123 << 20}, Util: map[int]float64{100: 12.5}}}
	s := New(Options{Registry: reg, Queues: &runmetrics.Registry{}, Now: clk.now, Self: 100, Cores: 4, sys: sys, GPU: g})
	return s, sys, reg, clk, g
}

func rowOf(rep ctlproto.ResourcesReport, pid int) *ctlproto.ProcessRow {
	for i := range rep.Processes {
		if rep.Processes[i].PID == pid {
			return &rep.Processes[i]
		}
	}
	return nil
}

func TestFakeTreeCategoriesAndUnregistered(t *testing.T) {
	s, _, reg, _, _ := newFake(t)
	reg.Register(procreg.CatModels, "本地模型", 200)
	reg.Register(procreg.CatAnalyzer, "", 300)
	rep, err := s.Sample(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if rowOf(rep, 500) != nil {
		t.Fatal("别人的程序不是主程序的后代也没登记，不该出现")
	}
	want := map[int]struct {
		cat   string
		depth int
		par   int
	}{100: {ctlproto.ProcMain, 0, 0}, 200: {ctlproto.ProcModels, 1, 100}, 300: {ctlproto.ProcAnalyzer, 1, 100}, 400: {ctlproto.ProcUnregistered, 2, 300}}
	if len(rep.Processes) != len(want) {
		t.Fatalf("行数不对：%+v", rep.Processes)
	}
	for pid, w := range want {
		r := rowOf(rep, pid)
		if r == nil || r.Category != w.cat || r.Depth != w.depth || (pid != 100 && r.Parent != w.par) {
			t.Fatalf("pid %d：%+v，想要 %+v", pid, r, w)
		}
	}
	idx := map[int]int{}
	for i, r := range rep.Processes {
		idx[r.PID] = i
	}
	if idx[400] != idx[300]+1 {
		t.Fatalf("子进程要紧跟在父进程后面：%+v", rep.Processes)
	}
	if r := rowOf(rep, 400); r.Name != "git.exe" {
		t.Fatalf("未登记的要带 exe 名：%+v", r)
	}
}

func TestFakeCPUNeedsTwoSamples(t *testing.T) {
	s, sys, _, clk, _ := newFake(t)
	first, _ := s.Sample(context.Background(), false)
	for _, r := range first.Processes {
		if r.CPU != nil {
			t.Fatalf("第一次采样 CPU 必须是 nil，不是 0：%+v", r)
		}
	}
	if first.IntervalMS != 0 {
		t.Fatalf("没有前值时间隔应该是 0：%d", first.IntervalMS)
	}
	// 2 秒后：pid 100 多用了 1 秒 CPU，4 核 → 1 / 2 / 4 = 12.5%
	clk.t = clk.t.Add(2 * time.Second)
	in := sys.infos[100]
	in.cpu += time.Second
	sys.infos[100] = in
	second, _ := s.Sample(context.Background(), false)
	r := rowOf(second, 100)
	if r.CPU == nil || *r.CPU < 12.49 || *r.CPU > 12.51 {
		t.Fatalf("CPU 应该是 12.5：%v", r.CPU)
	}
	if second.IntervalMS != 2000 {
		t.Fatalf("间隔：%d", second.IntervalMS)
	}
	if r := rowOf(second, 200); r.CPU == nil || *r.CPU != 0 {
		t.Fatalf("没动的进程 CPU 是真的 0：%+v", r)
	}
}

func TestFakeStalePrevIsDiscarded(t *testing.T) {
	s, _, _, clk, _ := newFake(t)
	s.Sample(context.Background(), false)
	clk.t = clk.t.Add(MaxAge + time.Second) // 页面关了又开：前值太老，算出来是平均值不是即时值
	rep, _ := s.Sample(context.Background(), false)
	if rep.IntervalMS != 0 || rowOf(rep, 100).CPU != nil {
		t.Fatalf("前值太老应当当作没有：%+v", rep.Processes[0])
	}
}

func TestFakeForgetClearsPrevAndClosesGPU(t *testing.T) {
	s, _, _, clk, g := newFake(t)
	s.Sample(context.Background(), true)
	clk.t = clk.t.Add(time.Second)
	s.Forget()
	if g.closed != 1 {
		t.Fatalf("离开标签要放掉计数器句柄：%d", g.closed)
	}
	rep, _ := s.Sample(context.Background(), false)
	if rowOf(rep, 100).CPU != nil {
		t.Fatal("Forget 以后应该从没有前值开始")
	}
}

func TestFakeMinGapReusesLast(t *testing.T) {
	s, sys, _, clk, _ := newFake(t)
	a, _ := s.Sample(context.Background(), false)
	clk.t = clk.t.Add(50 * time.Millisecond)
	in := sys.infos[100]
	in.cpu += time.Hour
	sys.infos[100] = in
	b, _ := s.Sample(context.Background(), false)
	if b.SampledAtMS != a.SampledAtMS {
		t.Fatal("200 毫秒内的第二次调用该直接给上一份")
	}
	b.Processes[0].Name = "被改了" // 调用方改不到缓存
	c, _ := s.Sample(context.Background(), false)
	if c.Processes[0].Name == "被改了" {
		t.Fatal("缓存被调用方改到了")
	}
}

// pid 被复用：登记时的创建时间和现在对不上，登记作废，这一行退回"未登记"，不张冠李戴。
func TestFakePIDReuseDropsRegistration(t *testing.T) {
	s, sys, reg, _, _ := newFake(t)
	reg.Register(procreg.CatMCP, "旧服务", 200)
	if r := rowOf(sample(t, s), 200); r.Category != ctlproto.ProcMCP || r.Label != "旧服务" {
		t.Fatalf("创建时间对得上时该显示登记的类别：%+v", r)
	}
	s.Forget()
	sys.created[200] = 99 // 这个 pid 现在是另一个进程了
	r := rowOf(sample(t, s), 200)
	if r.Category != ctlproto.ProcUnregistered || r.Label != "" {
		t.Fatalf("创建时间对不上，登记要作废：%+v", r)
	}
}

func sample(t *testing.T, s *Sampler) ctlproto.ResourcesReport {
	t.Helper()
	s.mu.Lock()
	s.haveLast = false // 绕开 MinGap 缓存
	s.mu.Unlock()
	rep, err := s.Sample(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestFakeRegisteredButDeadIsNotShown(t *testing.T) {
	s, sys, reg, _, _ := newFake(t)
	sys.procs = append(sys.procs[:1], sys.procs[2:]...) // 200 退出了
	reg.Register(procreg.CatModels, "本地模型", 200)
	if rowOf(sample(t, s), 200) != nil {
		t.Fatal("已经不在进程列表里的登记项不该显示")
	}
}

func TestFakeUnreadableProcessKeepsNil(t *testing.T) {
	s, sys, _, _, _ := newFake(t)
	delete(sys.infos, 400) // 没权限读
	r := rowOf(sample(t, s), 400)
	if r == nil || r.Memory != nil || r.CPU != nil || r.Commit != nil {
		t.Fatalf("读不到的进程数值必须是 nil，不能是 0：%+v", r)
	}
}

func TestFakeMemoryKindFallsBackToWorkingSetForWholeColumn(t *testing.T) {
	s, sys, reg, _, _ := newFake(t)
	reg.Register(procreg.CatModels, "本地模型", 200)
	in := sys.infos[100]
	in.hasPrivate = false // 老系统：主程序拿不到专用工作集
	sys.infos[100] = in
	rep := sample(t, s)
	if rep.MemoryKind != ctlproto.MemWorkingSet {
		t.Fatalf("口径应该退回工作集：%s", rep.MemoryKind)
	}
	if r := rowOf(rep, 200); r.Memory == nil || *r.Memory != (200<<20)+1 {
		t.Fatalf("整列都按工作集，不能混用：%+v", r.Memory)
	}
	if rep.Runtime.Other != nil {
		t.Fatal("口径不是专用工作集时，“其他”不能算")
	}
}

// 页面的第一次采样先不开性能计数器（要 400 多毫秒）：GPU 列写"还在准备"，不是"不可用"，也不是 0；下一次才读。
func TestFakeGPUFirstSampleIsPending(t *testing.T) {
	s, _, _, clk, g := newFake(t)
	first, _ := s.Sample(context.Background(), true)
	if !first.GPU.Pending || first.GPU.Available || g.reads != 0 {
		t.Fatalf("第一次该是“还在准备”，而且不读计数器：%+v reads=%d", first.GPU, g.reads)
	}
	clk.t = clk.t.Add(2 * time.Second)
	second, _ := s.Sample(context.Background(), true)
	if second.GPU.Pending || !second.GPU.Available || g.reads != 1 {
		t.Fatalf("第二次该读到：%+v reads=%d", second.GPU, g.reads)
	}
	// 命令行（SampleWarm）当场开
	s2, _, _, _, g2 := newFake(t)
	rep, err := s2.SampleWarm(context.Background(), true)
	if err != nil || !rep.GPU.Available || g2.reads == 0 {
		t.Fatalf("命令行该当场读：%+v %v", rep.GPU, err)
	}
}

func TestFakeGPUUnavailableAndAvailable(t *testing.T) {
	s, _, _, clk, g := newFake(t)
	g.err = errors.New("编造的原因")
	s.Sample(context.Background(), true) // 第一次只是准备
	clk.t = clk.t.Add(2 * time.Second)
	rep, _ := s.Sample(context.Background(), true)
	if rep.GPU.Available || rep.GPU.Pending || rep.GPU.Reason != "编造的原因" {
		t.Fatalf("取不到要写不可用和原因：%+v", rep.GPU)
	}
	for _, r := range rep.Processes {
		if r.GPU != nil || r.GPUMemory != nil {
			t.Fatalf("不可用时行里必须是 nil，不能是 0：%+v", r)
		}
	}
	g.err = nil
	clk.t = clk.t.Add(2 * time.Second)
	rep, _ = s.Sample(context.Background(), true)
	r := rowOf(rep, 100)
	if !rep.GPU.Available || r.GPUMemory == nil || *r.GPUMemory != 123<<20 || r.GPU == nil || *r.GPU != 12.5 {
		t.Fatalf("显存读数没并进去：%+v %+v", rep.GPU, r)
	}
	if r := rowOf(rep, 200); r.GPUMemory != nil || r.GPU != nil {
		t.Fatalf("计数器里没有的进程是 nil：%+v", r)
	}
}

func TestFakeGPUNotSampledUnlessWanted(t *testing.T) {
	s, _, _, _, g := newFake(t)
	g.err = errors.New("不该被调用")
	rep, _ := s.Sample(context.Background(), false)
	if rep.GPU.Available || rep.GPU.Reason == "不该被调用" {
		t.Fatalf("GPU 列不可见时不该去读计数器：%+v", rep.GPU)
	}
}

func TestFakeListFailureStillHasMain(t *testing.T) {
	s, sys, _, _, _ := newFake(t)
	sys.listFail = true
	rep, err := s.Sample(context.Background(), false)
	if err != nil || len(rep.Processes) != 1 || rep.Processes[0].Category != ctlproto.ProcMain {
		t.Fatalf("进程列表读不出来时至少有主程序一行：%+v %v", rep.Processes, err)
	}
}

func TestSampleCanceledContext(t *testing.T) {
	s, _, _, _, _ := newFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Sample(ctx, false); err == nil {
		t.Fatal("ctx 已取消应该返回错误")
	}
}

// 队列表跟着报告出去：没注册的行 Wired 为假，注册了的有读数
func TestSampleCarriesQueues(t *testing.T) {
	s, _, _, _, _ := newFake(t)
	q := &runmetrics.Registry{}
	s.queues = q
	q.Register(runmetrics.KeyModelSlots, 0, func() (runmetrics.Reading, error) {
		return runmetrics.Reading{Running: runmetrics.I(2), Capacity: runmetrics.I(4)}, nil
	})
	rep := sample(t, s)
	if len(rep.Queues) == 0 || rep.Queues[0].Key != runmetrics.KeyModelSlots || !rep.Queues[0].Wired || *rep.Queues[0].Running != 2 {
		t.Fatalf("队列没带出来：%+v", rep.Queues)
	}
	if rep.Queues[1].Wired {
		t.Fatalf("没注册的行应是未接入：%+v", rep.Queues[1])
	}
}

// 类别键：procreg 和 ctlproto 各写一份，这里钉住两边一致
func TestCategoryKeysMatch(t *testing.T) {
	pairs := [][2]string{
		{procreg.CatModels, ctlproto.ProcModels}, {procreg.CatAnalyzer, ctlproto.ProcAnalyzer}, {procreg.CatMCP, ctlproto.ProcMCP},
		{procreg.CatShell, ctlproto.ProcShell}, {procreg.CatMedia, ctlproto.ProcMedia},
	}
	for _, p := range pairs {
		if p[0] != p[1] || mapCategory(p[0]) != p[1] {
			t.Fatalf("类别键对不上：%v", p)
		}
	}
	if mapCategory("没见过的") != ctlproto.ProcUnregistered {
		t.Fatal("不认识的类别当未登记")
	}
}

func TestParsePIDAndEngine(t *testing.T) {
	cases := []struct {
		name string
		pid  int
		ok   bool
	}{
		{"pid_1234_luid_0x00000000_0x0000F3A2_phys_0", 1234, true},
		{"pid_7", 7, true},
		{"luid_0x0_phys_0", 0, false},
		{"pid_abc_luid", 0, false},
		{"pid_0_luid", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		if pid, ok := parsePID(c.name); pid != c.pid || ok != c.ok {
			t.Fatalf("%q → %d %v，想要 %d %v", c.name, pid, ok, c.pid, c.ok)
		}
	}
	if engineType("pid_1_luid_0_phys_0_eng_0_engtype_3D") != "3D" || engineType("pid_1_luid_0") != "" {
		t.Fatal("引擎类型解析不对")
	}
}

// 进程列表最多每 listEvery 重列一次；有新登记的进程立刻重列；已退出的进程（info 回 errGone）从表里消失。
func TestFakeListIsThrottled(t *testing.T) {
	s, sys, reg, clk, _ := newFake(t)
	ctx := context.Background()
	s.Sample(ctx, false)
	clk.t = clk.t.Add(2 * time.Second)
	s.Sample(ctx, false)
	clk.t = clk.t.Add(2 * time.Second)
	s.Sample(ctx, false)
	if sys.listCalls != 1 {
		t.Fatalf("listEvery 之内只该列一次：%d", sys.listCalls)
	}
	// 新登记的进程（上次列表里没有）立刻重列
	sys.procs = append(sys.procs, procEntry{pid: 600, ppid: 100, name: "新服务.exe"})
	sys.infos[600] = pinfo(6, 0, 1<<20)
	sys.created[600] = 6
	reg.Register(procreg.CatMCP, "新服务", 600)
	clk.t = clk.t.Add(2 * time.Second)
	rep, _ := s.Sample(ctx, false)
	if sys.listCalls != 2 || rowOf(rep, 600) == nil || rowOf(rep, 600).Category != ctlproto.ProcMCP {
		t.Fatalf("新登记要立刻重列并显示：calls=%d %+v", sys.listCalls, rowOf(rep, 600))
	}
	// 超过 listEvery 再列
	clk.t = clk.t.Add(listEvery + time.Second)
	s.Sample(ctx, false)
	if sys.listCalls != 3 {
		t.Fatalf("到时间该重列：%d", sys.listCalls)
	}
	// 复用旧列表期间进程退出了：不显示
	sys.gone = map[int]bool{300: true}
	clk.t = clk.t.Add(2 * time.Second)
	rep, _ = s.Sample(ctx, false)
	if sys.listCalls != 3 || rowOf(rep, 300) != nil || rowOf(rep, 400) == nil {
		t.Fatalf("已退出的进程该消失（它的孩子仍在旧列表里）：calls=%d %+v", sys.listCalls, rep.Processes)
	}
	// 主程序自己永远在
	sys.gone[100] = true
	clk.t = clk.t.Add(2 * time.Second)
	rep, _ = s.Sample(ctx, false)
	if rowOf(rep, 100) == nil {
		t.Fatal("主程序那一行不能被丢掉")
	}
}
