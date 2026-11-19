package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/runstats"
)

// 运行页测试的公共件：假界面线程队列、能触发每种状态的假读服务

func runApp(t *testing.T, src api.StatsReader, scan api.StorageScanner) (*app, *uiQueue) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := &app{rail: 3, ctx: ctx, cancel: cancel, demoMode: true}
	a.mainView = testMainView()
	q := &uiQueue{}
	a.postFn = q.post
	a.attachStats(src, scan)
	return a, q
}

func TestRunPageShellKeepsStatusTab(t *testing.T) {
	a, _ := runApp(t, nil, nil)
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	for _, want := range []string{"运行", "状态", "资源", "演示数据"} {
		if !tt.HasText(want) {
			t.Fatalf("运行页没有 %q：%v", want, tt.Texts())
		}
	}
	if a.run.cancel != nil {
		t.Fatal("状态标签在前台时不该有订阅")
	}
}

// ───────────── 假读服务和测试台 ─────────────

type fakeStats struct {
	mu                    sync.Mutex
	rep                   ctlproto.UsageReport
	repErr                error
	store                 ctlproto.StorageReport
	storeErr              error
	gate                  chan struct{} // 非 nil：读数等它关了才返回（模拟"加载中"）
	usageCalls, storeCall int
	qual                  ctlproto.QualityReport
	qualErr, spansErr     error
	qualityCalls          int
	turns                 ctlproto.ConvTurnsReport
	turnCalls             int
	details               map[string]ctlproto.RequestDetail
	detailCalls           int
	detailErr             error
	spans                 ctlproto.SpansReport
	rebuild               ctlproto.RebuildReport
	rebuildErr            error
	rescans, cancels      int
	rescanErr             error
	res                   ctlproto.ResourcesReport
	resErr                error
	resCalls, forgets     int
}

func (f *fakeStats) wait(ctx context.Context) error {
	f.mu.Lock()
	g := f.gate
	f.mu.Unlock()
	if g == nil {
		return nil
	}
	select {
	case <-g:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeStats) RangeOf(a ctlproto.StatsArgs) (ctlproto.UsageRange, error) {
	return runstats.New(nil, runstats.Options{}).RangeOf(a)
}

func (f *fakeStats) Quality(ctx context.Context, _ ctlproto.UsageRange, _ ctlproto.StatsArgs) (ctlproto.QualityReport, error) {
	if err := f.wait(ctx); err != nil {
		return ctlproto.QualityReport{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.qualityCalls++
	return f.qual, f.qualErr
}

func (f *fakeStats) ConvTurns(context.Context, string, int) (ctlproto.ConvTurnsReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.turnCalls++
	return f.turns, nil
}

// RequestDetail 记点开了几次；details 里没有的 id 回“没找到”。
func (f *fakeStats) RequestDetail(_ context.Context, id string) (ctlproto.RequestDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detailCalls++
	if f.detailErr != nil {
		return ctlproto.RequestDetail{}, f.detailErr
	}
	d, ok := f.details[id]
	if !ok {
		return ctlproto.RequestDetail{}, runstats.ErrNoRequest
	}
	return d, nil
}

func (f *fakeStats) Spans(context.Context, ctlproto.UsageRange, ctlproto.StatsArgs) (ctlproto.SpansReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spans, f.spansErr
}

func (f *fakeStats) Rebuild(context.Context, ctlproto.UsageRange) (ctlproto.RebuildReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rebuild, f.rebuildErr
}

func (f *fakeStats) Usage(ctx context.Context, r ctlproto.UsageRange) (ctlproto.UsageReport, error) {
	f.mu.Lock()
	f.usageCalls++
	f.mu.Unlock()
	if err := f.wait(ctx); err != nil {
		return ctlproto.UsageReport{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rep := f.rep
	rep.Range = r
	return rep, f.repErr
}

func (f *fakeStats) Storage(ctx context.Context) (ctlproto.StorageReport, error) {
	f.mu.Lock()
	f.storeCall++
	f.mu.Unlock()
	if err := f.wait(ctx); err != nil {
		return ctlproto.StorageReport{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.store, f.storeErr
}

func (f *fakeStats) Resources(ctx context.Context) (ctlproto.ResourcesReport, error) {
	f.mu.Lock()
	f.resCalls++
	f.mu.Unlock()
	if err := f.wait(ctx); err != nil {
		return ctlproto.ResourcesReport{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.res, f.resErr
}

func (f *fakeStats) ForgetResources() { f.mu.Lock(); f.forgets++; f.mu.Unlock() }

func (f *fakeStats) resCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.resCalls }

func (f *fakeStats) Rescan(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rescans++
	return f.rescanErr
}

func (f *fakeStats) CancelScan() { f.mu.Lock(); f.cancels++; f.mu.Unlock() }

func (f *fakeStats) set(fn func(*fakeStats)) { f.mu.Lock(); fn(f); f.mu.Unlock() }

func (f *fakeStats) calls() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.usageCalls, f.storeCall
}

type runHarness struct {
	t  *testing.T
	a  *app
	q  *uiQueue
	tt *ui.Tester
	w  int
}

func newRunHarness(t *testing.T, src api.StatsReader, scan api.StorageScanner, tab, w, h int) *runHarness {
	a, q := runApp(t, src, scan)
	a.run.tab = tab
	hn := &runHarness{t: t, a: a, q: q, tt: ui.NewTester(a.view, w, h), w: w}
	hn.frame()
	return hn
}

func (h *runHarness) frame() {
	h.t.Helper()
	for i := 0; i < 3; i++ {
		h.tt.Frame()
		settleMem(h.q)
	}
	h.tt.Frame()
}

func (h *runHarness) click(label string) {
	h.t.Helper()
	if err := h.tt.Click(label); err != nil {
		h.t.Fatalf("点 %q：%v\n窗口里的字：%v", label, err, h.tt.Texts())
	}
	h.frame()
}

// settleMotion 推进时钟直到画面不再有动画（下拉、弹层从入口展开，ADR-395）；点开下拉以后、点里面的项之前用。
func (h *runHarness) settleMotion() {
	for i := 0; i < 200 && h.tt.FramePending(); i++ {
		h.tt.Advance(16 * time.Millisecond)
		h.tt.Frame()
	}
}

// until 放行队列、画帧，直到条件成立（读数在后台协程里做，点完马上看计数会看到旧值）
func (h *runHarness) until(what string, cond func() bool) {
	h.t.Helper()
	remoteWait(h.t, what, func() bool { settleMem(h.q); h.tt.Frame(); return cond() })
}

// need 要求页面上有这些字。读数在后台协程里做，frame 只等“安静一会儿”，机器忙时结果可能还没回来：
// 缺字就接着放行队列、画帧再看，到期限才算失败（一直有才是对的，改坏了照样红，只是红得慢）。
func (h *runHarness) need(texts ...string) {
	h.t.Helper()
	missing := func() string {
		for _, s := range texts {
			if !h.tt.HasText(s) {
				return s
			}
		}
		return ""
	}
	for deadline := time.Now().Add(15 * time.Second); missing() != "" && time.Now().Before(deadline); {
		settleMem(h.q)
		h.tt.Frame()
		time.Sleep(2 * time.Millisecond)
	}
	if s := missing(); s != "" {
		h.t.Fatalf("页面上没有 %q：%v", s, h.tt.Texts())
	}
}

func (h *runHarness) absent(texts ...string) {
	h.t.Helper()
	for _, s := range texts {
		if h.tt.HasText(s) {
			h.t.Fatalf("页面上不该有 %q：%v", s, h.tt.Texts())
		}
	}
}

var runNow = time.Date(2026, 11, 8, 15, 4, 5, 0, time.Local)

func demoUsage(t *testing.T) ctlproto.UsageReport {
	t.Helper()
	d := buildDemoStats(10, 365, func() time.Time { return runNow })
	r, _ := d.RangeOf(ctlproto.StatsArgs{All: true})
	rep, err := d.Usage(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func newTesterFor(a *app) *ui.Tester { return ui.NewTester(a.view, 1100, 800) }

func newTesterForSize(a *app, w, h int) *ui.Tester { return ui.NewTester(a.view, w, h) }
