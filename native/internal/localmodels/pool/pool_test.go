package pool

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/localmodels/engine"
)

// ---- 规格 GM4 验收 1：槽位满要等，不是当场 503 ----

func TestSlotWait(t *testing.T) {
	specs := []engine.Spec{embedSpec("a"), embedSpec("b"), embedSpec("c")}

	t.Run("有人放开就装上", func(t *testing.T) {
		h := newHarness(t, specs, nil) // K=2
		la, lb := h.acquire("a"), h.acquire("b")
		start := h.clk.Now()
		ch := h.acquireAsync(context.Background(), "c")
		waitFor(t, "c 已经在等槽位", h.waiting(1))

		h.clk.Advance(3 * time.Second)
		h.notDone(ch) // 3 秒没到 30 秒的截止，不能报错
		la.Release()
		r := h.result(ch)
		if r.err != nil {
			t.Fatalf("放开一个以后应该装上：%v", r.err)
		}
		waited := h.clk.Now().Sub(start)
		t.Logf("GM4-1 槽位满，第三个模型等了 %v（假时钟）后装上", waited)
		if waited != 3*time.Second {
			t.Fatalf("等待时长记错了：%v", waited)
		}
		if h.state("a").Loaded || !h.state("b").Loaded || !h.state("c").Loaded {
			t.Fatalf("该卸掉空闲的 a、留下在用的 b、装上 c：%+v", h.p.Snapshot().Models)
		}
		if h.fl.closedCount("a") != 1 || h.fl.closedCount("b") != 0 {
			t.Fatal("被卸的该是 a，b 在用不能动")
		}
		lb.Release()
		r.l.Release()
	})

	t.Run("谁也不放，到点 503 说清是都在用", func(t *testing.T) {
		h := newHarness(t, specs, nil)
		la, lb := h.acquire("a"), h.acquire("b")
		defer la.Release()
		defer lb.Release()
		ch := h.acquireAsync(context.Background(), "c")
		waitFor(t, "c 已经在等槽位", h.waiting(1))
		h.clk.Advance(29 * time.Second)
		h.notDone(ch)
		h.clk.Advance(time.Second)
		r := h.result(ch)
		pe := asPoolError(t, r.err)
		if pe.Status != 503 || !strings.Contains(pe.Detail, "Every loaded model is in use") {
			t.Fatalf("该是 503 加'所有模型都在用'：%+v", pe)
		}
		if h.fl.callCount("c") != 0 {
			t.Fatal("没有位置时不该调加载函数")
		}
		if st := h.state("c"); st.Loading || st.Loaded {
			t.Fatalf("c 不该留下加载中的痕迹：%+v", st)
		}
	})

	t.Run("等的时候调用方走了，回 ctx 的错误不是 503", func(t *testing.T) {
		h := newHarness(t, specs, nil)
		la, lb := h.acquire("a"), h.acquire("b")
		defer la.Release()
		defer lb.Release()
		ctx, cancel := context.WithCancel(context.Background())
		ch := h.acquireAsync(ctx, "c")
		waitFor(t, "c 已经在等槽位", h.waiting(1))
		cancel()
		r := h.result(ch)
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("该回 context.Canceled：%v", r.err)
		}
		var pe *Error
		if errors.As(r.err, &pe) {
			t.Fatalf("调用方走了不是池的错误：%+v", pe)
		}
	})
}

// ---- 验收 2：并发首次请求只加载一次 ----

func TestSingleLoadForConcurrentFirstUse(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("a")}, nil)
	gate := h.fl.gateFor("a")
	var chs []<-chan acqResult
	for i := 0; i < 3; i++ {
		chs = append(chs, h.acquireAsync(context.Background(), "a"))
	}
	waitFor(t, "加载函数进来了，另两个在等同一次加载", func() bool { return h.fl.callCount("a") == 1 && h.p.waiting.Load() == 3 })
	if st := h.state("a"); !st.Loading || st.Loaded {
		t.Fatalf("应该是加载中：%+v", st)
	}
	close(gate)
	var leases []*Lease
	for _, ch := range chs {
		r := h.result(ch)
		if r.err != nil {
			t.Fatal(r.err)
		}
		leases = append(leases, r.l)
	}
	if n := h.fl.callCount("a"); n != 1 {
		t.Fatalf("GM4-2 3 路并发首次请求，加载函数被调了 %d 次，该是 1 次", n)
	}
	t.Logf("GM4-2 3 路并发首次请求同一个模型，加载函数被调 1 次")
	if h.fl.of(leases[0]) != h.fl.of(leases[1]) || h.fl.of(leases[1]) != h.fl.of(leases[2]) {
		t.Fatal("三个租约该指向同一份模型")
	}
	if st := h.state("a"); st.Refs != 3 {
		t.Fatalf("三个租约，引用数该是 3：%d", st.Refs)
	}
	for _, l := range leases {
		l.Release()
	}
	if st := h.state("a"); st.Refs != 0 || !st.Loaded {
		t.Fatalf("放开以后引用数该是 0 且仍加载着：%+v", st)
	}
}

// ---- 验收 3：单个模型自己就超内存上限，立刻报，不等，也不白卸别的 ----

func TestOverBudgetFailsFast(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("small"), embedSpec("huge")}, func(c *Config) {
		c.MaxBytes = 1000 * mib
	})
	h.fl.estimates["small"] = 400 * mib
	h.fl.estimates["huge"] = 1500 * mib
	h.acquire("small").Release() // 空闲、可卸，但卸它也救不了 huge

	r := h.result(h.acquireAsync(context.Background(), "huge")) // 假时钟没动：能返回就说明没等
	pe := asPoolError(t, r.err)
	if pe.Status != 503 || !strings.Contains(pe.Detail, "alone needs about 1500 MiB") || !strings.Contains(pe.Detail, "1000 MiB") {
		t.Fatalf("该是 503 加'自己就要 1500 MiB，超过 1000 MiB'：%+v", pe)
	}
	t.Logf("GM4-3 立刻 503：%s", pe.Detail)
	if !h.state("small").Loaded {
		t.Fatal("不该为一个永远装不下的模型白卸 small")
	}
	if h.fl.callCount("huge") != 0 {
		t.Fatal("不该调加载函数")
	}
}

// 字节不够但还有空闲的可卸：卸最久没用的，再装；都在用才等。
func TestBudgetEvictsIdleThenWaits(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("a"), embedSpec("b"), embedSpec("c")}, func(c *Config) {
		c.MaxLoaded = 3
		c.MaxBytes = 500 * mib
	})
	for id, e := range map[string]uint64{"a": 200 * mib, "b": 200 * mib, "c": 200 * mib} {
		h.fl.estimates[id] = e
	}
	la := h.acquire("a")
	h.clk.Advance(time.Second)
	lb := h.acquire("b")
	lb.Release()

	// a 在用，b 空闲：装 c 要 600 > 500，卸 b（唯一空闲的）
	lc := h.acquire("c")
	if h.state("b").Loaded || !h.state("a").Loaded || !h.state("c").Loaded {
		t.Fatalf("该卸 b：%+v", h.p.Snapshot().Models)
	}
	// a、c 都在用：b 再来要等，原因是内存上限
	ch := h.acquireAsync(context.Background(), "b")
	waitFor(t, "b 在等", h.waiting(1))
	h.clk.Advance(30 * time.Second)
	pe := asPoolError(t, h.result(ch).err)
	if pe.Status != 503 || !strings.Contains(pe.Detail, "would exceed the 500 MiB resident budget") {
		t.Fatalf("该是 503 加内存上限的原因：%+v", pe)
	}
	la.Release()
	lc.Release()
}

// ---- 验收 4：加载失败释放名额、下一次会重试；失败不影响别的模型 ----

func TestLoadFailureReleasesSlot(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("a"), embedSpec("b")}, func(c *Config) { c.MaxLoaded = 1 })
	boom := errors.New("boom")
	h.fl.failNext["a"] = boom

	_, err := h.p.Acquire(context.Background(), "a")
	pe := asPoolError(t, err)
	if pe.Status != 500 || !strings.Contains(pe.Detail, "Model 'a' failed to load: boom") {
		t.Fatalf("该是 500 加原因：%+v", pe)
	}
	if !errors.Is(err, boom) {
		t.Fatal("底层原因要能用 errors.Is 认出来（Unwrap）")
	}
	st := h.state("a")
	if st.Loading || st.Loaded || st.Ready || st.Problem == "" {
		t.Fatalf("失败以后该是：没在加载、没装上、ready=false、带原因：%+v", st)
	}
	if snap := h.p.Snapshot(); snap.ResidentBytes != 0 {
		t.Fatalf("失败的预留该释放，现在还记着 %d 字节", snap.ResidentBytes)
	}
	// K=1：槽位也释放了，别的模型马上能装，不受 a 的影响
	lb := h.acquire("b")
	lb.Release()
	if !h.state("b").Ready || !h.state("b").Loaded {
		t.Fatal("a 坏了不该影响 b")
	}
	// 下一次请求重试，成功以后问题清掉
	la := h.acquire("a")
	la.Release()
	if st := h.state("a"); !st.Ready || st.Problem != "" || !st.Loaded {
		t.Fatalf("重试成功以后 ready 该恢复：%+v", st)
	}
	if h.fl.callCount("a") != 2 {
		t.Fatalf("a 该被加载了 2 次（失败一次、重试一次）：%d", h.fl.callCount("a"))
	}
	t.Logf("GM4-4 加载失败 → 500，名额释放，下一次重试成功（a 的加载函数共调 2 次）")
}

func TestFollowersShareLoadFailure(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("a")}, nil)
	gate := h.fl.gateFor("a")
	h.fl.failNext["a"] = errors.New("boom")
	var chs []<-chan acqResult
	for i := 0; i < 3; i++ {
		chs = append(chs, h.acquireAsync(context.Background(), "a"))
	}
	waitFor(t, "三个都在等同一次加载", func() bool { return h.fl.callCount("a") == 1 && h.p.waiting.Load() == 3 })
	close(gate)
	for _, ch := range chs {
		pe := asPoolError(t, h.result(ch).err)
		if pe.Status != 500 || !strings.Contains(pe.Detail, "boom") {
			t.Fatalf("三个都该拿到同一个 500：%+v", pe)
		}
	}
	if n := h.fl.callCount("a"); n != 1 {
		t.Fatalf("同一次失败不该各自再试：加载函数被调了 %d 次", n)
	}
}

func TestPanickingLoaderIsAnError(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("a")}, nil)
	h.fl.panicNext["a"] = true
	_, err := h.p.Acquire(context.Background(), "a")
	pe := asPoolError(t, err)
	if pe.Status != 500 || strings.Contains(pe.Detail, "故意的") {
		t.Fatalf("panic 该变成 500，且正文不带 panic 的值：%+v", pe)
	}
	if st := h.state("a"); st.Loading {
		t.Fatal("panic 以后不该一直是加载中")
	}
	h.acquire("a").Release() // 之后还能加载
}

func TestWrongKindFromLoaderIsAnError(t *testing.T) {
	closed := 0
	h := newHarness(t, []engine.Spec{embedSpec("a")}, func(c *Config) {
		c.Load = func(ctx context.Context, spec engine.Spec) (Loaded, error) {
			return Loaded{Reranker: new(engine.Reranker), Close: func() error { closed++; return nil }}, nil // 嵌入模型却给了重排器
		}
	})
	_, err := h.p.Acquire(context.Background(), "a")
	pe := asPoolError(t, err)
	if pe.Status != 500 || !strings.Contains(pe.Detail, "no embed model") || closed != 1 {
		t.Fatalf("种类对不上该是 500 且把半成品关掉：%+v closed=%d", pe, closed)
	}
}

// ---- 验收 5：空闲卸载 ----

func TestIdleUnload(t *testing.T) {
	t.Run("到点卸载，下一次请求重新加载", func(t *testing.T) {
		h := newHarness(t, []engine.Spec{embedSpec("a")}, func(c *Config) {
			c.Idle = 10 * time.Minute
			c.ReapInterval = 15 * time.Second
		})
		h.fl.loadDur["a"] = 1200 * time.Millisecond
		h.acquire("a").Release()
		if h.fl.callCount("a") != 1 || !h.state("a").Loaded {
			t.Fatal("先装上了")
		}
		waitFor(t, "reaper 建好了定时器", func() bool { return h.clk.Pending() >= 1 })
		h.clk.Advance(9*time.Minute + 59*time.Second) // 还没到 10 分钟：reaper 虽然被叫醒，什么都不卸
		waitFor(t, "reaper 查完了、建了下一个定时器", func() bool { return h.clk.Pending() >= 1 })
		if !h.state("a").Loaded {
			t.Fatal("空闲不到 10 分钟，不该卸")
		}
		h.clk.Advance(16 * time.Second) // 下一次检查在 15 秒以后
		waitFor(t, "空闲超过 10 分钟，该被卸", func() bool { return !h.state("a").Loaded })
		if len(h.log.lines("UNLOADED model=a")) != 1 || h.fl.closedCount("a") != 1 {
			t.Fatal("该有一行 UNLOADED，且 Close 了一次")
		}
		h.acquire("a").Release()
		if h.fl.callCount("a") != 2 || !h.state("a").Loaded {
			t.Fatal("下一次请求该重新加载")
		}
		t.Logf("GM4-5 假时钟走过 idle：卸载（loaded=false），下一次请求重新加载（加载函数共 2 次，load_ms=%d）", h.state("a").LoadMs)
	})

	t.Run("idle=0 不自动卸，也不起 reaper", func(t *testing.T) {
		h := newHarness(t, []engine.Spec{embedSpec("a")}, func(c *Config) { c.Idle = 0 })
		h.acquire("a").Release()
		if h.p.reaperDone != nil {
			t.Fatal("idle=0 不需要 reaper")
		}
		h.clk.Advance(24 * time.Hour)
		h.p.reapOnce()
		if !h.state("a").Loaded {
			t.Fatal("idle=0 不该卸")
		}
	})

	t.Run("在用的不卸，放开以后才卸", func(t *testing.T) {
		h := newHarness(t, []engine.Spec{embedSpec("a")}, func(c *Config) { c.Idle = time.Minute })
		l := h.acquire("a")
		h.clk.Advance(time.Hour)
		h.p.reapOnce()
		if !h.state("a").Loaded {
			t.Fatal("有租约在用，不该卸")
		}
		l.Release() // 放开时 lastUsed 刷新为现在
		h.p.reapOnce()
		if !h.state("a").Loaded {
			t.Fatal("刚放开，空闲时间从放开算起")
		}
		h.clk.Advance(61 * time.Second)
		h.p.reapOnce()
		if h.state("a").Loaded {
			t.Fatal("放开后空闲超过 idle，该卸")
		}
	})
}

// "空闲超过 idle"是严格大于：刚好等于不卸，多一点就卸。
func TestIdleBoundaryIsStrict(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("a")}, func(c *Config) { c.Idle = time.Minute })
	h.acquire("a").Release()
	h.clk.Advance(time.Minute)
	h.p.reapOnce()
	if !h.state("a").Loaded {
		t.Fatal("空闲刚好等于 idle，不算超过，不该卸")
	}
	h.clk.Advance(time.Nanosecond)
	h.p.reapOnce()
	if h.state("a").Loaded {
		t.Fatal("多过一纳秒就该卸")
	}
}

// ---- 验收 6：有租约不卸 ----

func TestUnloadInUse(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("a")}, nil)
	l := h.acquire("a")
	if ok, why := h.p.Unload("a"); ok || why != "in_use" {
		t.Fatalf("有租约在用该回 in_use：%v %q", ok, why)
	}
	if !h.state("a").Loaded || h.fl.closedCount("a") != 0 {
		t.Fatal("在用的不能被关")
	}
	l.Release()
	if ok, why := h.p.Unload("a"); !ok || why != "unloaded" {
		t.Fatalf("放开以后该能卸：%v %q", ok, why)
	}
	if h.state("a").Loaded || h.fl.closedCount("a") != 1 {
		t.Fatal("卸掉以后 loaded=false，Close 一次")
	}
	if ok, why := h.p.Unload("a"); ok || why != "not_loaded" {
		t.Fatalf("没加载的该回 not_loaded：%v %q", ok, why)
	}
	if ok, why := h.p.Unload("zzz"); ok || why != "unknown" {
		t.Fatalf("不认识的 id：%v %q", ok, why)
	}
	t.Logf("GM4-6 有租约 → in_use 不卸；放开 → unloaded")
}

func TestLeaseReleaseOnce(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("a")}, nil)
	l1, l2 := h.acquire("a"), h.acquire("a")
	l1.Release()
	l1.Release()
	l1.Release()
	if st := h.state("a"); st.Refs != 1 {
		t.Fatalf("重复 Release 只算一次，引用数该是 1：%d", st.Refs)
	}
	if ok, why := h.p.Unload("a"); ok || why != "in_use" {
		t.Fatal("l2 还在用，不能卸")
	}
	l2.Release()
}

// ---- 验收 7：Close 以后 reaper 不残留 ----

func TestCloseStopsReaper(t *testing.T) {
	settle := func(want int) int { // 等 goroutine 数降到 want 以内，最多 2 秒
		var n int
		for i := 0; i < 400; i++ {
			if n = runtime.NumGoroutine(); n <= want {
				return n
			}
			time.Sleep(5 * time.Millisecond)
		}
		return n
	}
	before := runtime.NumGoroutine() // 前面的测试留下的收尾 goroutine 只会减少，所以起点只会偏高一点，不会漏报泄漏
	for i := 0; i < 20; i++ {        // 起点先稳住：连续两次相同
		time.Sleep(10 * time.Millisecond)
		if n := runtime.NumGoroutine(); n == before {
			break
		} else {
			before = n
		}
	}
	clk := newFakeClock()
	fl := newFakeLoader(clk)
	p, err := New(Config{
		Models: entries(embedSpec("a")), Load: fl.load, Estimate: fl.estimate, Idle: time.Minute, ReapInterval: time.Second,
		Clock: clk, LogOut: &syncBuffer{}, RSS: func() (uint64, error) { return 0, errors.New("x") },
	})
	if err != nil {
		t.Fatal(err)
	}
	l, _ := p.Acquire(context.Background(), "a")
	l.Release()
	waitFor(t, "reaper 起来了", func() bool { return runtime.NumGoroutine() > before && clk.Pending() >= 1 })
	p.Close()
	if after := settle(before); after > before {
		t.Fatalf("GM4-7 Close 以后还剩 %d 个 goroutine（起点 %d）：reaper 没停", after, before)
	}
	if fl.closedCount("a") != 1 {
		t.Fatal("Close 该把装着的模型关掉")
	}
	clk.Advance(time.Hour) // 停了的 reaper 不会再被叫醒
	if _, err := p.Acquire(context.Background(), "a"); err == nil || asPoolError(t, err).Status != 503 {
		t.Fatalf("Close 以后 Acquire 该回 503：%v", err)
	}
	p.Close() // 重复调用不出事
	t.Logf("GM4-7 Close 后 goroutine 数 %d，起点 %d", runtime.NumGoroutine(), before)
}

// ---- 验收 8：日志 ----

func TestLogLines(t *testing.T) {
	rss := uint64(1000 * mib)
	h := newHarness(t, []engine.Spec{embedSpec("a")}, func(c *Config) {
		c.RSS = func() (uint64, error) { return rss, nil }
	})
	h.fl.hashMs = 300
	h.fl.loadDur["a"] = 1200 * time.Millisecond
	h.fl.estimates["a"] = 1144 * mib
	// 假加载函数在"加载中"把进程常驻内存抬高 372 MiB（和真 gte 加载后的量一样）
	orig := h.p.load
	h.p.load = func(ctx context.Context, spec engine.Spec) (Loaded, error) {
		rss += 372 * mib
		return orig(ctx, spec)
	}
	h.acquire("a").Release()
	want := "LOADED model=a kind=embed dimension=64 load_ms=1200 hash_ms=300 estimated_mb=1144 rss_delta_mb=372"
	got := h.log.lines("LOADED")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("LOADED 行不对：\n got %q\nwant %q", got, want)
	}
	if lines := h.log.lines("WARN"); len(lines) != 0 {
		t.Fatalf("实测 372 没超过估计 1144 的 1.5 倍，不该告警：%q", lines)
	}
	h.p.Unload("a")
	if got := h.log.lines("UNLOADED"); len(got) != 1 || got[0] != "UNLOADED model=a" {
		t.Fatalf("UNLOADED 行不对：%q", got)
	}
	t.Logf("GM4-8 日志：%s", want)
	if strings.Contains(h.log.String(), "model.onnx") {
		t.Fatalf("日志里不该有模型文件路径：%s", h.log.String())
	}
}

// 实测超过估计的 1.5 倍：写一行告警，记账改按实测算（估计偏低时不能让预算继续当摆设）。
func TestMeasuredRSSRaisesTheCharge(t *testing.T) {
	rss := uint64(0)
	h := newHarness(t, []engine.Spec{embedSpec("a"), embedSpec("b")}, func(c *Config) {
		c.MaxLoaded = 3
		c.MaxBytes = 450 * mib
		c.RSS = func() (uint64, error) { return rss, nil }
	})
	h.fl.estimates["a"] = 100 * mib
	h.fl.estimates["b"] = 100 * mib
	orig := h.p.load
	h.p.load = func(ctx context.Context, spec engine.Spec) (Loaded, error) {
		if spec.ID == "a" {
			rss += 400 * mib // a 实际涨了 400，估计只有 100
		}
		return orig(ctx, spec)
	}
	la := h.acquire("a")
	st := h.state("a")
	if st.EstimatedBytes != 100*mib || st.MeasuredBytes != 400*mib || st.ChargedBytes != 400*mib {
		t.Fatalf("记账该取 max(估计, 实测)：%+v", st)
	}
	if warn := h.log.lines("WARN model=a"); len(warn) != 1 || !strings.Contains(warn[0], "1.5 倍") {
		t.Fatalf("该有一行告警：%q", warn)
	}
	if got := h.p.Snapshot().ResidentBytes; got != 400*mib {
		t.Fatalf("ResidentBytes 该是 400 MiB：%d", got/mib)
	}
	// 按估计 100 + 100 装得下 450；按实测 400 + 100 = 500 > 450，a 在用，b 要等
	ch := h.acquireAsync(context.Background(), "b")
	waitFor(t, "b 在等预算", h.waiting(1))
	la.Release() // a 放开 → 空闲 → 被卸 → b 装上
	r := h.result(ch)
	if r.err != nil {
		t.Fatal(r.err)
	}
	r.l.Release()
	if h.state("a").Loaded {
		t.Fatal("为了给 b 腾预算，a 该被卸")
	}
	t.Logf("GM4-8 实测 400 MiB 超过估计 100 MiB 的 1.5 倍：告警一行，记账改成 400，b 因此要等 a 放开")
}

// ---- 验收 9：Snapshot 不被任何长操作挡住 ----

func TestSnapshotNotBlocked(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("a"), embedSpec("b")}, nil)
	// b 已装好，租约被一个"卡在 Run 里"的调用方拿着
	stuck := h.acquire("b") // 失败时故意不 defer 放开：池锁被挡住的话，放开也会卡在锁上

	gate := h.fl.gateFor("a") // a 的加载卡住
	ch := h.acquireAsync(context.Background(), "a")
	waitFor(t, "a 的加载进行中", func() bool { return h.fl.callCount("a") == 1 })

	snapped := make(chan Snapshot, 1)
	start := time.Now()
	go func() { snapped <- h.p.Snapshot() }()
	select {
	case s := <-snapped:
		took := time.Since(start)
		t.Logf("GM4-9 加载卡住、另一路租约卡在 Run 时 Snapshot 用了 %v", took)
		if took > 100*time.Millisecond {
			t.Fatalf("Snapshot 用了 %v，超过 100 毫秒", took)
		}
		var loading, loaded bool
		for _, m := range s.Models {
			loading = loading || (m.ID == "a" && m.Loading)
			loaded = loaded || (m.ID == "b" && m.Loaded && m.Refs == 1)
		}
		if !loading || !loaded {
			t.Fatalf("快照该显示 a 加载中、b 已装好且被租用：%+v", s.Models)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Snapshot 被加载或 Run 挡住了")
	}
	close(gate)
	h.result(ch).l.Release()
	stuck.Release()
}

// ---- 卸载谁、卸载时机 ----

func TestEvictsLeastRecentlyUsedIdle(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("a"), embedSpec("b"), embedSpec("c")}, nil) // K=2
	h.acquire("a").Release()
	h.clk.Advance(time.Second)
	h.acquire("b").Release()
	h.clk.Advance(time.Second)
	h.acquire("a").Release() // a 比 b 新了
	h.clk.Advance(time.Second)
	h.acquire("c").Release()
	if !h.state("a").Loaded || h.state("b").Loaded || !h.state("c").Loaded {
		t.Fatalf("该卸最久没用的 b：%+v", h.p.Snapshot().Models)
	}
}

// 同一个模型正在卸载时的新请求：等它关完再加载，不同时装两份（卸载期间名额和字节还记着）。
func TestSameModelWaitsForItsOwnClose(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("a")}, nil)
	closeGate := make(chan struct{})
	h.fl.mu.Lock()
	h.fl.closeGate["a"] = closeGate
	h.fl.mu.Unlock()
	h.acquire("a").Release()

	unloaded := make(chan struct{})
	go func() { h.p.Unload("a"); close(unloaded) }()
	waitFor(t, "卸载已开始（Close 卡在门上）", func() bool { return h.state("a").Unloading })
	if snap := h.p.Snapshot(); snap.ResidentBytes != testEstimate {
		t.Fatalf("卸载还没完，它占的字节该照记：%d", snap.ResidentBytes)
	}

	ch := h.acquireAsync(context.Background(), "a")
	waitFor(t, "新请求在等", h.waiting(1))
	h.notDone(ch)
	if h.fl.callCount("a") != 1 {
		t.Fatal("旧的还没关完，不该开始加载新的")
	}
	close(closeGate)
	<-unloaded
	r := h.result(ch)
	if r.err != nil {
		t.Fatal(r.err)
	}
	r.l.Release()
	ev := strings.Join(h.fl.eventsSnapshot(), ",")
	if ev != "load:a,close:a,load:a" {
		t.Fatalf("顺序该是 加载、关闭、再加载：%s", ev)
	}
}

// ---- 加载：串行、不绑请求、leader 优先 ----

func TestLoadsAreSerialized(t *testing.T) {
	ids := []string{"a", "b", "c"}
	h := newHarness(t, []engine.Spec{embedSpec("a"), embedSpec("b"), embedSpec("c")}, func(c *Config) { c.MaxLoaded = 3 })
	gates := map[string]chan struct{}{}
	for _, id := range ids {
		gates[id] = h.fl.gateFor(id)
	}
	var chs []<-chan acqResult
	for _, id := range ids {
		chs = append(chs, h.acquireAsync(context.Background(), id))
	}
	waitFor(t, "第一个加载进来了", func() bool { return h.fl.total() == 1 })
	time.Sleep(30 * time.Millisecond)
	if h.fl.total() != 1 {
		t.Fatalf("一次只加载一个模型，现在同时有 %d 个进了加载函数", h.fl.total())
	}
	for _, id := range ids { // 逐个放行
		close(gates[id])
	}
	for _, ch := range chs {
		h.result(ch).l.Release()
	}
	h.fl.mu.Lock()
	maxIn := h.fl.maxIn
	h.fl.mu.Unlock()
	if maxIn != 1 {
		t.Fatalf("加载函数同时最多 %d 个在跑，该是 1", maxIn)
	}
}

func TestLeaderCancelDoesNotFailFollowers(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("a")}, nil)
	gate := h.fl.gateFor("a")
	leaderCtx, cancel := context.WithCancel(context.Background())
	leader := h.acquireAsync(leaderCtx, "a")
	waitFor(t, "加载开始", func() bool { return h.fl.callCount("a") == 1 })
	follower := h.acquireAsync(context.Background(), "a")
	waitFor(t, "两个都在等", h.waiting(2))

	cancel()
	if r := h.result(leader); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("发起加载的请求走了，它自己回 ctx 的错误：%v", r.err)
	}
	h.notDone(follower) // 加载不受它影响，还在继续
	close(gate)
	r := h.result(follower)
	if r.err != nil {
		t.Fatalf("发起者走了，等同一次加载的不该跟着失败：%v", r.err)
	}
	if st := h.state("a"); !st.Loaded || st.Refs != 1 {
		t.Fatalf("加载完留在池里，只有 follower 一个引用（没有替走掉的 leader 钉着）：%+v", st)
	}
	if h.fl.callCount("a") != 1 {
		t.Fatal("加载函数该只调一次")
	}
	r.l.Release()
}

// 加载完的模型替 leader 钉着一个引用，别的请求不能在 leader 拿到之前把它当空闲的卸掉。
func TestLeaderGetsTheModelItLoaded(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("a"), embedSpec("b")}, func(c *Config) { c.MaxLoaded = 1 })
	gateA := h.fl.gateFor("a")
	leader := h.acquireAsync(context.Background(), "a")
	waitFor(t, "a 加载开始", func() bool { return h.fl.callCount("a") == 1 })
	other := h.acquireAsync(context.Background(), "b") // K=1：a 在加载，b 只能等
	waitFor(t, "两个都在等", h.waiting(2))
	close(gateA)

	r := h.result(leader)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if h.fl.closedCount("a") != 0 {
		t.Fatal("a 刚装好就被 b 卸掉了：leader 拿不到自己加载的模型")
	}
	h.notDone(other) // a 被 leader 用着，b 继续等
	r.l.Release()
	rb := h.result(other)
	if rb.err != nil {
		t.Fatal(rb.err)
	}
	rb.l.Release()
	if h.state("a").Loaded || !h.state("b").Loaded {
		t.Fatal("leader 放开以后 b 才卸 a、装上自己")
	}
}

// 白盒：leader 走了的两种时序（装好之前、装好但还没认领），都不该让模型被无人认领地钉住。
func TestAbandonedFlight(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("a")}, nil)
	p := h.p
	sl := p.slots["a"]

	t.Run("装好之前走了：装好时不占引用", func(t *testing.T) {
		fl := &flight{done: make(chan struct{}), reserve: testEstimate}
		p.mu.Lock()
		sl.flight = fl
		p.mu.Unlock()
		p.abandon(fl)
		p.finishLoad(sl, fl, Loaded{Embedder: new(engine.Embedder)}, nil, 5, 0)
		if st := h.state("a"); !st.Loaded || st.Refs != 0 || fl.handed {
			t.Fatalf("leader 已走，装好后引用数该是 0：%+v handed=%v", st, fl.handed)
		}
		p.Unload("a")
	})
	t.Run("装好了但还没认领就走了：把占着的引用还回去", func(t *testing.T) {
		fl := &flight{done: make(chan struct{}), reserve: testEstimate}
		p.mu.Lock()
		sl.flight = fl
		p.mu.Unlock()
		p.finishLoad(sl, fl, Loaded{Embedder: new(engine.Embedder)}, nil, 5, 0)
		if st := h.state("a"); st.Refs != 1 || !fl.handed {
			t.Fatalf("leader 还在时装好应替它占 1 个引用：%+v", st)
		}
		p.abandon(fl)
		if st := h.state("a"); st.Refs != 0 || fl.handed {
			t.Fatalf("leader 走了该还回引用：%+v handed=%v", st, fl.handed)
		}
		p.Unload("a")
	})
}

// ---- Close ----

func TestCloseWaitsForLeasesThenForces(t *testing.T) {
	t.Run("等租约放开", func(t *testing.T) {
		h := newHarness(t, []engine.Spec{embedSpec("a")}, nil)
		l := h.acquire("a")
		done := make(chan struct{})
		go func() { h.p.Close(); close(done) }()
		select {
		case <-done:
			t.Fatal("还有租约在用，Close 不该返回")
		case <-time.After(50 * time.Millisecond):
		}
		if h.fl.closedCount("a") != 0 {
			t.Fatal("租约还在用，不该先关")
		}
		l.Release()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("放开以后 Close 该返回")
		}
		if h.fl.closedCount("a") != 1 {
			t.Fatal("放开以后该关一次")
		}
	})
	t.Run("等不到就强制关", func(t *testing.T) {
		h := newHarness(t, []engine.Spec{embedSpec("a")}, func(c *Config) { c.CloseWait = 5 * time.Second })
		h.acquire("a") // 故意不放
		done := make(chan struct{})
		go func() { h.p.Close(); close(done) }()
		waitFor(t, "Close 在等", func() bool { return h.clk.Pending() >= 1 })
		h.clk.Advance(5 * time.Second)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("到点该强制关并返回")
		}
		if h.fl.closedCount("a") != 1 || len(h.log.lines("强制关闭")) != 1 {
			t.Fatal("该强制关一次并写一行告警")
		}
	})
}

// Close 时还有加载在进行：做完的丢掉不登记，等它的人得到 503。
func TestCloseDuringLoad(t *testing.T) {
	gate, entered := make(chan struct{}), make(chan struct{})
	h := newHarness(t, []engine.Spec{embedSpec("a")}, func(c *Config) {
		orig := c.Load
		c.Load = func(ctx context.Context, spec engine.Spec) (Loaded, error) {
			close(entered)
			<-gate // 故意不看 ctx：覆盖"加载其实做完了"这条时序
			return orig(ctx, spec)
		}
	})
	ch := h.acquireAsync(context.Background(), "a")
	<-entered

	done := make(chan struct{})
	go func() { h.p.Close(); close(done) }()
	waitFor(t, "池已标记关闭", func() bool {
		h.p.mu.Lock()
		defer h.p.mu.Unlock()
		return h.p.closed
	})
	close(gate)
	r := h.result(ch)
	pe := asPoolError(t, r.err)
	if pe.Status != 503 || !strings.Contains(pe.Detail, "shutting down") {
		t.Fatalf("关池时进行中的加载该以 503 收场：%+v", pe)
	}
	<-done
	if st := h.state("a"); st.Loaded || st.Loading {
		t.Fatalf("关池以后不该留下加载的东西：%+v", st)
	}
	if len(h.fl.models) != 1 || h.fl.models[0].closed.Load() != 1 {
		t.Fatal("加载完的那一份该被关掉，不登记")
	}
}

// ---- 压力：reaper 和 Acquire 抢（go test -race 下跑）----

func TestReaperVersusAcquireStress(t *testing.T) {
	fl := newFakeLoader(nil)
	log := &syncBuffer{}
	p, err := New(Config{
		Models: entries(embedSpec("a"), embedSpec("b"), embedSpec("c")), Load: fl.load, Estimate: fl.estimate,
		MaxLoaded: 2, Idle: time.Millisecond, ReapInterval: time.Millisecond, Wait: 20 * time.Second,
		LogOut: log, RSS: func() (uint64, error) { return 0, errors.New("x") },
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"a", "b", "c"}
	var wg sync.WaitGroup
	var leased, rejected int
	var mu sync.Mutex
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				id := ids[(g+i)%3]
				l, err := p.Acquire(context.Background(), id)
				if err != nil {
					mu.Lock()
					rejected++
					mu.Unlock()
					continue
				}
				m := fl.of(l)
				m.users.Add(1)
				if m.closed.Load() != 0 {
					fl.violate(id + "：拿到租约时模型已经被关了")
				}
				if i%7 == 0 {
					time.Sleep(2 * time.Millisecond) // 让 reaper 有机会在"刚空闲"的当口来
				}
				m.users.Add(-1)
				l.Release()
				mu.Lock()
				leased++
				mu.Unlock()
			}
		}(g)
	}
	wg.Wait()
	p.Close()
	fl.mu.Lock()
	defer fl.mu.Unlock()
	for _, v := range fl.violations {
		t.Errorf("违规：%s", v)
	}
	loads, closes := len(fl.models), 0
	for _, m := range fl.models {
		closes += int(m.closed.Load())
	}
	if loads != closes {
		t.Fatalf("加载了 %d 份，关了 %d 份：有漏关或多关", loads, closes)
	}
	if leased+rejected != 8*60 {
		t.Fatalf("账对不上：%d + %d", leased, rejected)
	}
	t.Logf("压力：480 次请求，成功 %d，被拒 %d；加载 %d 份、关 %d 份，没有卸载时还在用", leased, rejected, loads, closes)
}

// ---- 构造、查询 ----

func TestRequire(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("e"), rerankSpec("r")}, nil)
	if s, err := h.p.Require("e", engine.KindEmbed); err != nil || s.ID != "e" {
		t.Fatalf("%+v %v", s, err)
	}
	pe := asPoolError(t, func() error { _, err := h.p.Require("nope", engine.KindEmbed); return err }())
	if pe.Status != 404 || !strings.Contains(pe.Detail, "Unknown model 'nope'") || !strings.Contains(pe.Detail, "available: e, r") {
		t.Fatalf("不认识的 id：%+v", pe)
	}
	pe = asPoolError(t, func() error { _, err := h.p.Require("e", engine.KindRerank); return err }())
	if pe.Status != 404 || !strings.Contains(pe.Detail, "is a embed model, not rerank") {
		t.Fatalf("种类不对：%+v", pe)
	}
	_, err := h.p.Acquire(context.Background(), "nope")
	if asPoolError(t, err).Status != 404 {
		t.Fatal("Acquire 不认识的 id 也是 404")
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	fl := newFakeLoader(nil)
	ok := Config{Models: entries(embedSpec("a")), Load: fl.load, Estimate: fl.estimate}
	cases := []struct {
		name string
		mod  func(*Config)
	}{
		{"没有加载函数", func(c *Config) { c.Load = nil }},
		{"没有估计函数", func(c *Config) { c.Estimate = nil }},
		{"没有模型", func(c *Config) { c.Models = nil }},
		{"重复的 id", func(c *Config) { c.Models = entries(embedSpec("a"), embedSpec("a")) }},
		{"登记项不合格", func(c *Config) { s := embedSpec("a"); s.Pooling = "mean"; c.Models = entries(s) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := ok
			c.mod(&cfg)
			if p, err := New(cfg); err == nil {
				p.Close()
				t.Fatal("应该被拒绝")
			}
		})
	}
	t.Run("零值配置用默认", func(t *testing.T) {
		p, err := New(ok)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		if p.maxLoaded != DefaultMaxLoaded || p.maxBytes != DefaultMaxBytes || p.wait != DefaultWait ||
			p.reapInterval != DefaultReapInterval || p.closeWait != DefaultCloseWait || p.idle != 0 || p.reaperDone != nil {
			t.Fatalf("默认值不对：%+v", p)
		}
		pp, err := New(Config{Models: ok.Models, Load: ok.Load, Estimate: ok.Estimate, MaxLoaded: -3})
		if err != nil {
			t.Fatal(err)
		}
		defer pp.Close()
		if pp.maxLoaded != 1 {
			t.Fatalf("同时上限是负数该夹成 1（server.py 的 max(1, n)）：%d", pp.maxLoaded)
		}
	})
}

// 登记项的切片是拷贝：外面改了不影响池。
func TestSpecIsCopied(t *testing.T) {
	s := embedSpec("a")
	s.Matryoshka, s.AllowedDimensions = true, []int{32, 64}
	h := newHarness(t, []engine.Spec{s}, nil)
	s.AllowedDimensions[0] = 999
	got, err := h.p.Require("a", engine.KindEmbed)
	if err != nil || got.AllowedDimensions[0] != 32 {
		t.Fatalf("池里的登记项被外面改了：%+v %v", got, err)
	}
	got.AllowedDimensions[1] = 999
	again, _ := h.p.Require("a", engine.KindEmbed)
	if again.AllowedDimensions[1] != 64 {
		t.Fatal("Require 返回的该是拷贝")
	}
}

// 启动时就知道有问题的模型（--lazy-models 里文件缺了）：只它自己 ready=false，别的照常，Snapshot 带原因。
func TestEntryProblemOnlyAffectsItself(t *testing.T) {
	fl := newFakeLoader(nil)
	p, err := New(Config{
		Models: []Entry{{Spec: embedSpec("gte")}, {Spec: rerankSpec("bge"), Problem: "找不到文件 bge/model.onnx"}},
		Load:   fl.load, Estimate: fl.estimate, LogOut: &syncBuffer{}, RSS: func() (uint64, error) { return 0, errors.New("x") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var gte, bge ModelState
	for _, m := range p.Snapshot().Models {
		if m.ID == "gte" {
			gte = m
		} else {
			bge = m
		}
	}
	if !gte.Ready || bge.Ready || bge.Problem != "找不到文件 bge/model.onnx" {
		t.Fatalf("gte 该 ready，bge 不 ready 且带原因：%+v %+v", gte, bge)
	}
	l, err := p.Acquire(context.Background(), "gte")
	if err != nil {
		t.Fatalf("bge 坏了不该影响 gte：%v", err)
	}
	l.Release()
	// bge 的文件后来补上了（这里的假加载函数本来就成功）：请求一次就恢复，不用重启
	l, err = p.Acquire(context.Background(), "bge")
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	for _, m := range p.Snapshot().Models {
		if !m.Ready {
			t.Fatalf("加载成功以后问题该清掉：%+v", m)
		}
	}
}

// 估计函数失败（文件不在）：500，原因记在该模型上，不调加载函数，别的模型不受影响。
func TestEstimateFailureIsPerModel(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("a"), embedSpec("b")}, func(c *Config) {
		est := c.Estimate
		c.Estimate = func(spec engine.Spec) (uint64, error) {
			if spec.ID == "b" {
				return 0, fmt.Errorf("找不到文件 b/model.onnx")
			}
			return est(spec)
		}
	})
	_, err := h.p.Acquire(context.Background(), "b")
	pe := asPoolError(t, err)
	if pe.Status != 500 || !strings.Contains(pe.Detail, "找不到文件 b/model.onnx") {
		t.Fatalf("%+v", pe)
	}
	if st := h.state("b"); st.Ready || !strings.Contains(st.Problem, "找不到文件") {
		t.Fatalf("%+v", st)
	}
	if h.fl.callCount("b") != 0 {
		t.Fatal("估计都失败了，不该去加载")
	}
	h.acquire("a").Release()
}

// 卸载以后空闲的堆立刻还给系统（不等 Go 的后台回收，那要两分钟）：真模型上两个模型装着 1061 MiB，卸掉以后
// 162 MiB 停了两分钟，加了这一步立刻 43 MiB（models-parity 的内存 ③）。这里用一块 192 MiB 的垃圾代替模型：
// 丢掉引用、不触发别的 GC，Unload 以后系统回收的堆（HeapReleased）该立刻涨上一大截。
func TestUnloadReturnsFreeHeapToSystem(t *testing.T) {
	h := newHarness(t, []engine.Spec{embedSpec("a")}, nil)
	h.acquire("a").Release()
	big := make([]byte, 192<<20)
	for i := 0; i < len(big); i += 4096 {
		big[i] = 1 // 真的用上这些页
	}
	runtime.KeepAlive(big)
	big = nil
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if ok, why := h.p.Unload("a"); !ok {
		t.Fatalf("卸不掉：%s", why)
	}
	waitFor(t, "卸载后堆还给系统", func() bool {
		runtime.ReadMemStats(&after)
		return after.HeapReleased >= before.HeapReleased+128<<20
	})
	t.Logf("HeapReleased %.0f → %.0f MiB", float64(before.HeapReleased)/(1<<20), float64(after.HeapReleased)/(1<<20))
}

// 加载过程留下的垃圾也立刻还给系统，并且在量"加载涨了多少常驻内存"之前：真模型上 gte 加载后进程常驻 479 MiB，
// 加了这一步 401 MiB（models-parity 内存 ①）。这里在加载函数里造一块 192 MiB 的垃圾，加载完 HeapReleased 该立刻涨上一大截。
func TestLoadReturnsGarbageToSystem(t *testing.T) {
	var atAlloc atomic.Uint64 // 垃圾刚造出来、页都在用的时候，系统回收的堆是多少（别的测试留下的不算）
	h := newHarness(t, []engine.Spec{embedSpec("a")}, func(c *Config) {
		inner := c.Load
		c.Load = func(ctx context.Context, spec engine.Spec) (Loaded, error) {
			garbage := make([]byte, 192<<20)
			for i := 0; i < len(garbage); i += 4096 {
				garbage[i] = 1
			}
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			atAlloc.Store(ms.HeapReleased)
			runtime.KeepAlive(garbage)
			garbage = nil
			return inner(ctx, spec)
		}
	})
	h.acquire("a").Release()
	var after runtime.MemStats
	waitFor(t, "加载后堆还给系统", func() bool {
		runtime.ReadMemStats(&after)
		return after.HeapReleased >= atAlloc.Load()+128<<20
	})
	t.Logf("HeapReleased %.0f → %.0f MiB", float64(atAlloc.Load())/(1<<20), float64(after.HeapReleased)/(1<<20))
}
