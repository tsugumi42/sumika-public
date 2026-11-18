package selfcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 测试里的检查：返回值由测试控制
type fakeCheck struct {
	id  string
	res func(e *Env) Result
}

func (f *fakeCheck) ID() string   { return f.id }
func (f *fakeCheck) Name() string { return "假检查 " + f.id }
func (f *fakeCheck) Eval(_ context.Context, e *Env) Result {
	return f.res(e)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type logs struct {
	mu          sync.Mutex
	warn, infos []string
}

func (l *logs) Warnf(f string, a ...any) {
	l.mu.Lock()
	l.warn = append(l.warn, fmt.Sprintf(f, a...))
	l.mu.Unlock()
}
func (l *logs) Infof(f string, a ...any) {
	l.mu.Lock()
	l.infos = append(l.infos, fmt.Sprintf(f, a...))
	l.mu.Unlock()
}

func newTestRunner(t *testing.T, dir string, cs *Counters, now *clock, lg *logs, checks ...Check) *Runner {
	t.Helper()
	cfg := Config{Dir: dir, Counters: cs, Checks: checks, Now: now.Now, StartDelay: -1}
	if lg != nil {
		cfg.Warnf, cfg.Infof = lg.Warnf, lg.Infof
	}
	return New(cfg)
}

func readFile(t *testing.T, dir, day string) DayFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, day+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var f DayFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("当天文件不是合法 JSON：%v\n%s", err, b)
	}
	return f
}

var day1 = time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)

// B7：刷新写出的 JSON 能解析，字段齐全，状态行读得出来
func TestFlushWritesParsableDayFile(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{t: day1}
	cs := NewCounters()
	cs.Add(IntakeStored)
	ok := &fakeCheck{id: "a.ok", res: func(*Env) Result { return Result{State: Ok, Value: 0, Threshold: 1} }}
	bad := &fakeCheck{id: "b.bad", res: func(*Env) Result {
		return Result{State: Bad, Value: 2, Threshold: 1, Example: map[string]string{"turn": "t-1"}}
	}}
	r := newTestRunner(t, dir, cs, clk, nil, ok, bad)
	r.Evaluate(context.Background())
	f := readFile(t, dir, "2026-11-13")
	if f.Day != "2026-11-13" || !f.Running || len(f.Checks) != 2 {
		t.Fatalf("文件内容不对：%+v", f)
	}
	if f.Checks[0].ID != "a.ok" || f.Checks[1].ID != "b.bad" || f.Checks[1].State != Bad || f.Checks[1].Since == "" || f.Checks[1].Example["turn"] != "t-1" {
		t.Fatalf("检查行不对（顺序按配置，bad 带 since 和例子）：%+v", f.Checks)
	}
	if f.Counters["stored"] != 1 {
		t.Fatalf("counters 里 stored 应是 1：%v", f.Counters)
	}
	if got := Summary(dir, clk.Now()); !strings.Contains(got, "1 项异常（b.bad）") {
		t.Fatalf("状态行：%q", got)
	}
}

// B7：原子写——写一半被打断（留下 .tmp）时旧文件仍完整，下一次写把 .tmp 清掉
func TestAtomicWriteKeepsOldFileWhenInterrupted(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{t: day1}
	r := newTestRunner(t, dir, NewCounters(), clk, nil, &fakeCheck{id: "a", res: func(*Env) Result { return Result{State: Ok} }})
	r.Evaluate(context.Background())
	good, _ := os.ReadFile(filepath.Join(dir, "2026-11-13.json"))
	// 打断：只留下写了一半的临时文件
	if err := os.WriteFile(filepath.Join(dir, "2026-11-13.json.tmp"), []byte(`{"day":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if f := readFile(t, dir, "2026-11-13"); f.Day != "2026-11-13" {
		t.Fatalf("旧文件被动了：%+v", f)
	}
	clk.Add(time.Minute)
	r.Evaluate(context.Background())
	if _, err := os.Stat(filepath.Join(dir, "2026-11-13.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("下一次写以后不该留 .tmp：%v", err)
	}
	now, _ := os.ReadFile(filepath.Join(dir, "2026-11-13.json"))
	if string(now) == string(good) {
		t.Fatal("时间变了，文件应该刷新")
	}
}

// B7：重启后当天计数续上；日期翻转后归零，不带昨天的
func TestCountersResumeAfterRestartAndResetAtMidnight(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{t: day1}
	cs1 := NewCounters()
	cs1.Add(IntakeStoreFailed)
	cs1.Add(IntakeStoreFailed)
	r1 := newTestRunner(t, dir, cs1, clk, nil)
	r1.Evaluate(context.Background())
	r1.Close() // 正常关：running 记成 false
	if f := readFile(t, dir, "2026-11-13"); f.Running || f.Counters["storeFailed"] != 2 {
		t.Fatalf("关机后的文件：%+v", f)
	}

	// 新进程：计数器从零开始，当天的数从文件续上
	cs2 := NewCounters()
	cs2.Add(IntakeStoreFailed)
	r2 := newTestRunner(t, dir, cs2, clk, nil)
	cs2.Add(IntakeStoreFailed)
	r2.Evaluate(context.Background())
	if f := readFile(t, dir, "2026-11-13"); f.Counters["storeFailed"] != 4 {
		// 文件里 2，加新进程里数到的 2 次（含 New 之前数的那 1 次）
		t.Fatalf("重启后应续上：%v", f.Counters)
	}

	// 过零点：零点到这次评估之间数到的归给昨天（不丢），之后新文件从零开始、不带昨天的
	clk.t = time.Date(2026, 11, 14, 0, 5, 0, 0, time.Local)
	cs2.Add(IntakeStoreFailed)
	r2.Evaluate(context.Background())
	if f := readFile(t, dir, "2026-11-13"); f.Counters["storeFailed"] != 5 {
		t.Fatalf("零点以后第一次评估前数到的归昨天：%v", f.Counters)
	}
	if f := readFile(t, dir, "2026-11-14"); f.Counters["storeFailed"] != 0 {
		t.Fatalf("新文件从零开始、不带昨天的：%v", f.Counters)
	}
	cs2.Add(IntakeStoreFailed)
	clk.Add(5 * time.Minute)
	r2.Evaluate(context.Background())
	if f := readFile(t, dir, "2026-11-14"); f.Counters["storeFailed"] != 1 {
		t.Fatalf("新一天只数新的：%v", f.Counters)
	}
}

// B8：ok 变 bad 只写一条 WARN，不重复；bad 变回 ok 写一条恢复；unknown 不记成 ok
func TestTransitionsLogOnceAndUnknownIsNeverOk(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{t: day1}
	lg := &logs{}
	state := Ok
	c := &fakeCheck{id: "x.y", res: func(*Env) Result {
		return Result{State: state, Value: 3, Threshold: 1, Example: map[string]string{"conv": "qq:group:123"}}
	}}
	r := newTestRunner(t, dir, NewCounters(), clk, lg, c)
	step := func(s State) {
		state = s
		clk.Add(time.Minute)
		r.Evaluate(context.Background())
	}
	step(Ok)
	step(Bad)
	step(Bad)
	step(Bad)
	if len(lg.warn) != 1 || !strings.Contains(lg.warn[0], "自检异常：x.y") || !strings.Contains(lg.warn[0], "例子 conv=qq:group:123") {
		t.Fatalf("同一个 bad 只该写一条 WARN：%v", lg.warn)
	}
	step(Unknown)
	if f := readFile(t, dir, "2026-11-13"); f.Checks[0].State != Unknown {
		t.Fatalf("unknown 要原样记，不许变成 ok：%+v", f.Checks[0])
	}
	if len(lg.infos) != 0 {
		t.Fatalf("unknown 不是恢复：%v", lg.infos)
	}
	step(Bad)
	if len(lg.warn) != 1 {
		t.Fatalf("bad→unknown→bad 不该重复报：%v", lg.warn)
	}
	step(Ok)
	step(Ok)
	if len(lg.infos) != 1 || !strings.Contains(lg.infos[0], "自检恢复：x.y") {
		t.Fatalf("恢复只写一条：%v", lg.infos)
	}
	step(Bad)
	if len(lg.warn) != 2 {
		t.Fatalf("恢复以后再坏应再报一次：%v", lg.warn)
	}
}

// 检查 panic、超时都记成 unknown，不拖垮别的检查，也不记成 ok
func TestPanicAndTimeoutBecomeUnknown(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{t: day1}
	boom := &fakeCheck{id: "boom", res: func(*Env) Result { panic("炸了") }}
	slow := &slowCheck{}
	fine := &fakeCheck{id: "fine", res: func(*Env) Result { return Result{State: Ok} }}
	r := newTestRunner(t, dir, NewCounters(), clk, nil, boom, slow, fine)
	r.Evaluate(context.Background())
	f := readFile(t, dir, "2026-11-13")
	if f.Checks[0].State != Unknown || !strings.Contains(f.Checks[0].Note, "意外") {
		t.Fatalf("panic：%+v", f.Checks[0])
	}
	if f.Checks[1].State != Unknown {
		t.Fatalf("超时：%+v", f.Checks[1])
	}
	if f.Checks[2].State != Ok {
		t.Fatalf("别的检查不受影响：%+v", f.Checks[2])
	}
}

type slowCheck struct{}

func (*slowCheck) ID() string   { return "slow" }
func (*slowCheck) Name() string { return "慢" }
func (*slowCheck) Eval(ctx context.Context, _ *Env) Result {
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
	}
	return Result{State: Ok}
}

// Skip：保持上一次的状态，不算 ok；从没判过的占一行 unknown
func TestSkipKeepsPreviousState(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{t: day1}
	state := Skip
	c := &fakeCheck{id: "s", res: func(*Env) Result { return Result{State: state, Value: 1, Threshold: 1} }}
	r := newTestRunner(t, dir, NewCounters(), clk, nil, c)
	r.Evaluate(context.Background())
	if f := readFile(t, dir, "2026-11-13"); f.Checks[0].State != Unknown {
		t.Fatalf("从没判过的不能看起来像没这条检查或正常：%+v", f.Checks[0])
	}
	state = Bad
	r.Evaluate(context.Background())
	state = Skip
	r.Evaluate(context.Background())
	if f := readFile(t, dir, "2026-11-13"); f.Checks[0].State != Bad {
		t.Fatalf("Skip 应保持上一次的 bad：%+v", f.Checks[0])
	}
}

// 重启后 bad 状态从文件续上，不重复写 WARN
func TestRestartDoesNotRepeatWarn(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{t: day1}
	bad := &fakeCheck{id: "k", res: func(*Env) Result { return Result{State: Bad, Value: 1, Threshold: 1} }}
	lg1 := &logs{}
	r1 := newTestRunner(t, dir, NewCounters(), clk, lg1, bad)
	r1.Evaluate(context.Background())
	r1.Close()
	lg2 := &logs{}
	r2 := newTestRunner(t, dir, NewCounters(), clk, lg2, bad)
	clk.Add(time.Minute)
	r2.Evaluate(context.Background())
	if len(lg1.warn) != 1 || len(lg2.warn) != 0 {
		t.Fatalf("重启后同一个 bad 不该再报：第一次 %v，第二次 %v", lg1.warn, lg2.warn)
	}
}

// 状态文件读坏了当没有（Fresh），不 panic
func TestCorruptStateFileIsFresh(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{坏"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newTestRunner(t, dir, NewCounters(), &clock{t: day1}, nil)
	if !r.kv.Fresh {
		t.Fatal("读坏的状态文件应当作没有")
	}
	r.kv.Set("baseline.x", 4)
	r.Evaluate(context.Background())
	if f := readFile(t, dir, "2026-11-13"); f.Baseline["x"] != 4 {
		t.Fatalf("baseline 进文件：%v", f.Baseline)
	}
	r2 := newTestRunner(t, dir, NewCounters(), &clock{t: day1}, nil)
	if v, ok := r2.kv.Get("baseline.x"); !ok || v != 4 || r2.kv.Fresh {
		t.Fatalf("状态文件重写以后下次能读回：%v %v fresh=%v", v, ok, r2.kv.Fresh)
	}
}

// 状态行（Summary）：正常 / 异常 / 未更新 / 读取失败 / 刚过零点读昨天的
func TestSummary(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{t: day1}
	if got := Summary(dir, clk.Now()); !strings.HasPrefix(got, "未更新") {
		t.Fatalf("没有文件：%q", got)
	}
	state := Ok
	c := &fakeCheck{id: "q", res: func(*Env) Result { return Result{State: state} }}
	r := newTestRunner(t, dir, NewCounters(), clk, nil, c)
	r.Start(context.Background()) // 启动就写一份，状态行马上能读
	defer r.Close()
	if got := Summary(dir, clk.Now()); got != "启动中，还没有评估" {
		t.Fatalf("刚启动：%q", got)
	}
	r.Evaluate(context.Background())
	if got := Summary(dir, clk.Now()); got != "正常" {
		t.Fatalf("正常：%q", got)
	}
	state = Unknown
	r.Evaluate(context.Background())
	if got := Summary(dir, clk.Now()); strings.HasPrefix(got, "正常") || !strings.Contains(got, "无法判定") {
		t.Fatalf("unknown 不能说成正常：%q", got)
	}
	if got := Summary(dir, clk.Now().Add(16*time.Minute)); !strings.HasPrefix(got, "未更新") {
		t.Fatalf("超过 15 分钟没更新：%q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-11-13.json"), []byte("{坏"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Summary(dir, clk.Now()); !strings.HasPrefix(got, "读取失败") {
		t.Fatalf("文件读坏了不能当正常：%q", got)
	}
}

// 计数器：nil 安全；断开窗口只数窗口内的
func TestCountersNilSafeAndDisconnectWindow(t *testing.T) {
	var nilc *Counters
	nilc.Add(IntakeStored)
	nilc.Frame()
	nilc.Disconnect(day1)
	nilc.StoreFailed("x")
	if nilc.Get(IntakeStored) != 0 || nilc.DisconnectsSince(day1) != 0 || !nilc.LastSubmit().IsZero() || nilc.LastStoreError() != "" {
		t.Fatal("nil 计数器应当什么也不做")
	}
	c := NewCounters()
	for i := 0; i < 10; i++ {
		c.Disconnect(day1.Add(time.Duration(i) * time.Minute))
	}
	if n := c.DisconnectsSince(day1.Add(5 * time.Minute)); n != 5 {
		t.Fatalf("窗口内应有 5 次：%d", n)
	}
	if c.Get(Reconnects) != 10 {
		t.Fatalf("累计 10 次：%d", c.Get(Reconnects))
	}
	for i := 0; i < 200; i++ { // 环形窗口写满以后仍正确
		c.Disconnect(day1.Add(time.Hour))
	}
	if n := c.DisconnectsSince(day1.Add(time.Hour)); n != disconnectRing {
		t.Fatalf("环形窗口只留最近 %d 次：%d", disconnectRing, n)
	}
}

// B10：计数路径基准（规格要求 ≤ 30 ns/次）
func BenchmarkAdd(b *testing.B) {
	c := NewCounters()
	for i := 0; i < b.N; i++ {
		c.Add(IntakeStored)
	}
}

// 状态行：刚过零点、新文件还没写时，读昨天还新鲜的那份
func TestSummaryReadsYesterdayJustAfterMidnight(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{t: time.Date(2026, 11, 13, 23, 58, 0, 0, time.Local)}
	r := newTestRunner(t, dir, NewCounters(), clk, nil, &fakeCheck{id: "q", res: func(*Env) Result { return Result{State: Ok} }})
	r.Evaluate(context.Background())
	if got := Summary(dir, time.Date(2026, 11, 14, 0, 1, 0, 0, time.Local)); got != "正常" {
		t.Fatalf("应读昨天的：%q", got)
	}
	if got := Summary(dir, time.Date(2026, 11, 14, 0, 30, 0, 0, time.Local)); !strings.HasPrefix(got, "未更新") {
		t.Fatalf("昨天的也过期了：%q", got)
	}
}

// 一直等到被取消才返回的检查：模拟查库卡住的评估
type blockCheck struct {
	once    sync.Once
	started chan struct{}
}

func (*blockCheck) ID() string   { return "block" }
func (*blockCheck) Name() string { return "卡住" }
func (b *blockCheck) Eval(ctx context.Context, _ *Env) Result {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return Result{State: Unknown}
}

// Close 先取消、再等评估收尾：评估正卡着（持着评估锁）时，关机不用等它的单条时限（2 秒）耗完
func TestCloseCancelsRunningEvaluationBeforeWaiting(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{t: day1}
	b := &blockCheck{started: make(chan struct{})}
	r := newTestRunner(t, dir, NewCounters(), clk, nil, b)
	r.Start(context.Background())
	select {
	case <-b.started:
	case <-time.After(time.Minute):
		t.Fatal("评估一直没开始")
	}
	start := time.Now()
	r.Close()
	if d := time.Since(start); d >= CheckTimeout {
		t.Fatalf("Close 等了 %v，要在取消后马上收手（单条时限 %v）", d, CheckTimeout)
	}
	if f := readFile(t, dir, "2026-11-13"); f.Running {
		t.Fatalf("关了以后 running 应是 false：%+v", f)
	}
	r.Close() // 可重复调
}

// Next 和 Note 分开落到当天文件：命令行和界面读同一行就能看到下一步。
func TestRunnerWritesNext(t *testing.T) {
	dir := t.TempDir()
	chk := &fakeCheck{id: "x", res: func(*Env) Result {
		return Result{State: Bad, Note: "原因", Next: "运行 sumika-ctl backup now"}
	}}
	r := newTestRunner(t, dir, NewCounters(), &clock{t: day1}, nil, chk)
	r.Evaluate(context.Background())
	f := readFile(t, dir, "2026-11-13")
	if len(f.Checks) != 1 || f.Checks[0].Note != "原因" || f.Checks[0].Next != "运行 sumika-ctl backup now" {
		t.Fatalf("Next 没写进当天文件：%+v", f.Checks)
	}
}

// 整轮被打断（关机）时，被打断的那条不能把上一次的判定覆盖成 unknown：它是因为被打断才失败的
func TestInterruptedEvaluationDoesNotOverwriteRows(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{t: day1}
	state, note := Bad, "第一次的结果"
	var cancel context.CancelFunc
	chk := &fakeCheck{id: "x", res: func(*Env) Result {
		if cancel != nil {
			cancel() // 评估到一半被打断
		}
		return Result{State: state, Note: note}
	}}
	r := newTestRunner(t, dir, NewCounters(), clk, nil, chk)
	r.Evaluate(context.Background())
	if f := readFile(t, dir, "2026-11-13"); f.Checks[0].State != Bad {
		t.Fatalf("前提：第一次应是 bad：%+v", f.Checks[0])
	}
	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()
	cancel = cancelFn
	state, note = Unknown, "本次结果"
	r.Evaluate(ctx)
	if f := readFile(t, dir, "2026-11-13"); f.Checks[0].State != Bad || f.Checks[0].Note == "本次结果" {
		t.Fatalf("被打断的这一条不该覆盖上一次的判定：%+v", f.Checks[0])
	}
}
