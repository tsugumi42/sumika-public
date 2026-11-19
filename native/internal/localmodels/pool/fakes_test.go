package pool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/localmodels/engine"
)

// 这个包的测试默认不需要真 dll、真模型：假加载函数加假时钟。真的在 real_test.go，用环境变量门着。

// testSpec 是一条能通过 Validate 的登记项，id 随意；嵌入 64 维，重排没有维度。
func testSpec(id string, kind engine.Kind) engine.Spec {
	s := engine.Spec{
		ID: id, Kind: kind, Dir: id, Onnx: "model.onnx", MaxTokens: 512, MinDimensions: engine.DefaultMinDimensions,
		TokenizerSHA256: strings.Repeat("a", 64), OnnxSHA256: strings.Repeat("b", 64),
	}
	if kind == engine.KindEmbed {
		s.Dimension, s.Pooling, s.Normalize, s.Truncation = 64, engine.PoolingCLS, true, engine.TruncationLongestFirst
	} else {
		s.Pooling, s.Truncation = engine.PoolingLogit, engine.TruncationOnlySecond
	}
	return s
}

func embedSpec(id string) engine.Spec  { return testSpec(id, engine.KindEmbed) }
func rerankSpec(id string) engine.Spec { return testSpec(id, engine.KindRerank) }

func entries(specs ...engine.Spec) []Entry {
	out := make([]Entry, len(specs))
	for i, s := range specs {
		out[i] = Entry{Spec: s}
	}
	return out
}

const testEstimate = 100 * mib // 假 Estimate 给每个模型的缺省估计

// waitFor 等条件成立；成功的路径一两毫秒就过，只有失败才会等满 5 秒。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等了 5 秒还没有：%s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// syncBuffer 是可以多个 goroutine 同时写的日志缓冲。
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// lines 是日志里含 sub 的各行（去掉前面的时间戳）。
func (s *syncBuffer) lines(sub string) []string {
	var out []string
	for _, ln := range strings.Split(s.String(), "\n") {
		if strings.Contains(ln, sub) {
			if i := strings.Index(ln, sub); i >= 0 {
				out = append(out, ln[i:])
			}
		}
	}
	return out
}

// ---- 假加载函数 ----

type fakeModel struct {
	id      string
	owner   *fakeLoader
	emb     *engine.Embedder
	rer     *engine.Reranker
	users   atomic.Int32 // 测试里正在用它的人数；Close 时不为 0 就是"卸载时租约还在用"
	closed  atomic.Int32 // Close 被调用的次数
	closeCh chan struct{}
}

func (m *fakeModel) close() error {
	if m.closeCh != nil {
		<-m.closeCh
	}
	if m.users.Load() != 0 {
		m.owner.violate(fmt.Sprintf("%s：卸载时还有 %d 个人在用", m.id, m.users.Load()))
	}
	if m.closed.Add(1) > 1 {
		m.owner.violate(m.id + "：Close 了不止一次")
	}
	m.owner.event("close:" + m.id)
	return nil
}

type fakeLoader struct {
	clk *fakeClock // 非 nil 时 loadDur 会拨它，让 load_ms 有数

	mu         sync.Mutex
	calls      map[string]int
	loadDur    map[string]time.Duration
	gate       map[string]chan struct{} // Load 在这里等，close 放行；ctx 取消也会放它走
	closeGate  map[string]chan struct{} // 下一个加载出来的模型，Close 要等这个
	failNext   map[string]error
	panicNext  map[string]bool
	estimates  map[string]uint64
	hashMs     int64
	cur, maxIn int
	models     []*fakeModel
	byEmb      map[*engine.Embedder]*fakeModel
	byRer      map[*engine.Reranker]*fakeModel
	events     []string
	violations []string
}

func newFakeLoader(clk *fakeClock) *fakeLoader {
	return &fakeLoader{
		clk: clk, calls: map[string]int{}, loadDur: map[string]time.Duration{}, gate: map[string]chan struct{}{},
		closeGate: map[string]chan struct{}{}, failNext: map[string]error{}, panicNext: map[string]bool{},
		estimates: map[string]uint64{}, byEmb: map[*engine.Embedder]*fakeModel{}, byRer: map[*engine.Reranker]*fakeModel{},
	}
}

func (f *fakeLoader) violate(msg string) {
	f.mu.Lock()
	f.violations = append(f.violations, msg)
	f.mu.Unlock()
}

func (f *fakeLoader) event(e string) {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
}

func (f *fakeLoader) estimate(spec engine.Spec) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.estimates[spec.ID]; ok {
		return e, nil
	}
	return testEstimate, nil
}

func (f *fakeLoader) load(ctx context.Context, spec engine.Spec) (Loaded, error) {
	f.mu.Lock()
	f.calls[spec.ID]++
	f.cur++
	f.maxIn = max(f.maxIn, f.cur)
	f.events = append(f.events, "load:"+spec.ID)
	gate := f.gate[spec.ID]
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.cur--
		f.mu.Unlock()
	}()

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return Loaded{}, ctx.Err()
		}
	}
	f.mu.Lock()
	dur := f.loadDur[spec.ID]
	failure := f.failNext[spec.ID]
	delete(f.failNext, spec.ID)
	doPanic := f.panicNext[spec.ID]
	delete(f.panicNext, spec.ID)
	closeCh := f.closeGate[spec.ID]
	delete(f.closeGate, spec.ID)
	f.mu.Unlock()
	if dur > 0 && f.clk != nil {
		f.clk.Advance(dur)
	}
	if doPanic {
		panic("故意的")
	}
	if failure != nil {
		return Loaded{}, failure
	}
	m := &fakeModel{id: spec.ID, owner: f, closeCh: closeCh}
	ld := Loaded{HashMs: f.hashMs, Close: m.close}
	f.mu.Lock()
	if spec.Kind == engine.KindEmbed {
		m.emb = new(engine.Embedder)
		f.byEmb[m.emb] = m
		ld.Embedder = m.emb
	} else {
		m.rer = new(engine.Reranker)
		f.byRer[m.rer] = m
		ld.Reranker = m.rer
	}
	f.models = append(f.models, m)
	f.mu.Unlock()
	return ld, nil
}

func (f *fakeLoader) callCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[id]
}

func (f *fakeLoader) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

// of 是租约背后的假模型。
func (f *fakeLoader) of(l *Lease) *fakeModel {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l.Embedder() != nil {
		return f.byEmb[l.Embedder()]
	}
	return f.byRer[l.Reranker()]
}

func (f *fakeLoader) closedCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.models {
		if m.id == id && m.closed.Load() > 0 {
			n++
		}
	}
	return n
}

func (f *fakeLoader) eventsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func (f *fakeLoader) gateFor(id string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := make(chan struct{})
	f.gate[id] = g
	return g
}

// ---- 测试用的池 ----

type harness struct {
	t   *testing.T
	p   *Pool
	clk *fakeClock
	fl  *fakeLoader
	log *syncBuffer
}

// newHarness 用假时钟、假加载函数建一个池；mod 可以改配置。池在测试结束时关掉，并检查没有"卸载时还在用"这类违规。
func newHarness(t *testing.T, specs []engine.Spec, mod func(*Config)) *harness {
	t.Helper()
	clk := newFakeClock()
	fl := newFakeLoader(clk)
	h := &harness{t: t, clk: clk, fl: fl, log: &syncBuffer{}}
	cfg := Config{
		Models: entries(specs...), Load: fl.load, Estimate: fl.estimate, MaxLoaded: 2, MaxBytes: 4 << 30,
		Clock: clk, LogOut: h.log, RSS: func() (uint64, error) { return 0, fmt.Errorf("测试里不量进程内存") },
	}
	if mod != nil {
		mod(&cfg)
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.p = p
	t.Cleanup(func() {
		// 失败的测试可能还留着没放开的租约，Close 会等它们；假时钟不会自己走，所以隔一会儿把 Close 的等待时间拨过去
		done := make(chan struct{})
		go func() { p.Close(); close(done) }()
		giveUp := time.After(5 * time.Second)
		for waiting := true; waiting; {
			select {
			case <-done:
				waiting = false
			case <-time.After(10 * time.Millisecond):
				clk.Advance(p.closeWait)
			case <-giveUp:
				t.Error("测试结束时池关不掉")
				return
			}
		}
		fl.mu.Lock()
		defer fl.mu.Unlock()
		for _, v := range fl.violations {
			t.Errorf("违规：%s", v)
		}
	})
	return h
}

type acqResult struct {
	l   *Lease
	err error
}

// acquireAsync 在另一个 goroutine 里取租约。
func (h *harness) acquireAsync(ctx context.Context, id string) <-chan acqResult {
	ch := make(chan acqResult, 1)
	go func() {
		l, err := h.p.Acquire(ctx, id)
		ch <- acqResult{l, err}
	}()
	return ch
}

func (h *harness) acquire(id string) *Lease {
	h.t.Helper()
	l, err := h.p.Acquire(context.Background(), id)
	if err != nil {
		h.t.Fatalf("Acquire(%s)：%v", id, err)
	}
	return l
}

// result 取 acquireAsync 的结果，最多等 5 秒。
func (h *harness) result(ch <-chan acqResult) acqResult {
	h.t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		h.t.Fatal("等了 5 秒 Acquire 还没返回")
		return acqResult{}
	}
}

// notDone 断言 Acquire 在一小会儿里确实还没返回（只是"没发生"的检查，不会因为慢而误报）。
func (h *harness) notDone(ch <-chan acqResult) {
	h.t.Helper()
	select {
	case r := <-ch:
		h.t.Fatalf("不该这么早返回：%+v", r)
	case <-time.After(50 * time.Millisecond):
	}
}

func (h *harness) waiting(n int32) func() bool { return func() bool { return h.p.waiting.Load() == n } }

func (h *harness) state(id string) ModelState {
	h.t.Helper()
	for _, m := range h.p.Snapshot().Models {
		if m.ID == id {
			return m
		}
	}
	h.t.Fatalf("Snapshot 里没有 %s", id)
	return ModelState{}
}

func asPoolError(t *testing.T, err error) *Error {
	t.Helper()
	var pe *Error
	if err == nil {
		t.Fatal("应该有错误")
	}
	if !errors.As(err, &pe) {
		t.Fatalf("不是 *Error：%T %v", err, err)
	}
	return pe
}
