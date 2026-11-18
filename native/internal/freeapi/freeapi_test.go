package freeapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 测试里的提供方都编：a、b、c 三家同类别 t，d 是只服务国内的，e 单独一类。
func testCatalog() Catalog {
	mk := func(id, cat string, tier map[string]int, regions []string) Provider {
		return Provider{ID: id, Category: cat, Name: id, URL: "https://example.test/" + id, Regions: regions, Tier: tier, Network: NetProxy, Auth: "none", Probe: map[string]string{"k": "v"}}
	}
	return Catalog{
		Categories: []Category{{ID: "t", Name: "测试"}, {ID: "solo", Name: "单家"}},
		Providers: []Provider{
			mk("a", "t", map[string]int{"*": 1}, nil),
			mk("b", "t", map[string]int{"*": 2}, nil),
			mk("c", "t", map[string]int{"*": 3}, nil),
			mk("d", "t", map[string]int{"*": 1}, []string{RegionCN}),
			mk("e", "solo", map[string]int{"*": 1}, nil),
		},
	}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type harness struct {
	e    *Engine
	clk  *clock
	set  *Settings
	logs []string
	mu   sync.Mutex
}

func newHarness(t *testing.T, mutate func(*Options)) *harness {
	t.Helper()
	h := &harness{clk: &clock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}}
	s := DefaultSettings()
	h.set = &s
	o := Options{
		Catalog:  testCatalog(),
		Settings: func() Settings { return *h.set },
		Now:      h.clk.Now,
		Zone:     func() *time.Location { return time.UTC },
		Logf: func(f string, a ...any) {
			h.mu.Lock()
			h.logs = append(h.logs, f)
			h.mu.Unlock()
		},
		Rand:    func(int) int { return 0 },
		Backoff: func(n int) time.Duration { return time.Minute << (n - 1) },
		HTTP:    func(bool) (*http.Client, error) { return &http.Client{}, nil },
	}
	if mutate != nil {
		mutate(&o)
	}
	h.e = New(o)
	t.Cleanup(h.e.Close)
	return h
}

func ok(v any) Fetcher { return func(context.Context, Env) (any, error) { return v, nil } }
func bad(err error) Fetcher {
	return func(context.Context, Env) (any, error) { return nil, err }
}

func fetchers(m map[string]Fetcher) Request {
	return Request{Category: "t", Region: RegionGlobal, Fetch: m}
}

func TestCatalogDefaultLoads(t *testing.T) {
	c := DefaultCatalog()
	if len(c.Providers) < 11 {
		t.Fatalf("内嵌注册表提供方太少：%d", len(c.Providers))
	}
	for _, p := range c.Providers {
		if len(p.Probe) == 0 {
			t.Errorf("%s 没有探测参数", p.ID)
		}
		if d, m := p.DefaultCaps(); d <= 0 || m <= 0 {
			t.Errorf("%s 上限不对", p.ID)
		}
	}
	tx, _ := c.Provider("tencent-weather")
	if !tx.Grey || tx.Network != NetDirect || tx.Serves(RegionGlobal) || !tx.Serves(RegionCN) {
		t.Errorf("腾讯天气的登记不对：%+v", tx)
	}
	om, _ := c.Provider("open-meteo")
	if d, m := om.DefaultCaps(); d != 5000 || m != 300 {
		t.Errorf("Open-Meteo 默认上限应是公布值的一半：%d %d", d, m)
	}
	if tm, _ := c.Provider("holiday-timor"); tm.Network != NetDirect {
		t.Errorf("timor 是国内站应直连")
	}
}

func TestCatalogRejects(t *testing.T) {
	base := `{"categories":[{"id":"x","name":"x"}],"providers":[{"id":"p","category":"x","name":"p","url":"https://e.test/a","tier":{"*":1},"network":"proxy","auth":"none"%s}]}`
	if _, err := ParseCatalog([]byte(strings.Replace(base, "%s", "", 1))); err != nil {
		t.Fatalf("合格的被拒：%v", err)
	}
	cases := map[string]string{
		"未知字段":   strings.Replace(base, "%s", `,"surprise":1`, 1),
		"要密钥":    strings.Replace(strings.Replace(base, "%s", "", 1), `"auth":"none"`, `"auth":"key"`, 1),
		"类别不存在":  strings.Replace(strings.Replace(base, "%s", "", 1), `"category":"x"`, `"category":"y"`, 1),
		"路线乱写":   strings.Replace(strings.Replace(base, "%s", "", 1), `"network":"proxy"`, `"network":"maybe"`, 1),
		"负上限":    strings.Replace(base, "%s", `,"dailyCap":-1`, 1),
		"http地址": strings.Replace(strings.Replace(base, "%s", "", 1), "https://", "http://", 1),
		"区域乱写":   strings.Replace(base, "%s", `,"regions":["mars"]`, 1),
	}
	for name, js := range cases {
		if _, err := ParseCatalog([]byte(js)); err == nil {
			t.Errorf("%s：应当被拒", name)
		}
	}
	dup := `{"categories":[{"id":"x","name":"x"}],"providers":[` +
		`{"id":"p","category":"x","name":"p","url":"https://e.test/a","tier":{"*":1},"network":"proxy","auth":"none"},` +
		`{"id":"p","category":"x","name":"p","url":"https://e.test/a","tier":{"*":1},"network":"proxy","auth":"none"}]}`
	if _, err := ParseCatalog([]byte(dup)); err == nil {
		t.Error("重复 id 应当被拒")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want Kind
	}{
		{nil, KOK},
		{ErrNotFound, KNotFound},
		{&StatusError{Code: 429, RetryAfter: 5 * time.Second}, KRate},
		{&StatusError{Code: 403, Challenge: true}, KBlocked},
		{&StatusError{Code: 503, Challenge: true}, KBlocked},
		{&StatusError{Code: 403}, KAuth},
		{&StatusError{Code: 401}, KAuth},
		{&StatusError{Code: 502}, K5xx},
		{&StatusError{Code: 404}, KSchema},
		{Schema("缺字段"), KSchema},
		{context.DeadlineExceeded, KTimeout},
		{context.Canceled, KCanceled},
		{ErrTooLarge, KTooLarge},
		{errors.New("connection refused"), KNet},
	}
	for _, c := range cases {
		if got, _ := Classify(c.err); got != c.want {
			t.Errorf("%v 应归 %s，得 %s", c.err, c.want, got)
		}
	}
	if _, d := Classify(&StatusError{Code: 429, RetryAfter: 7 * time.Second}); d != 7*time.Second {
		t.Errorf("429 要带回等待时间")
	}
}

func TestGetClassifiesCloudflareAndRetryAfter(t *testing.T) {
	var mode atomic.Int32
	srv := newServer(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 0:
			w.Header().Set("Server", "cloudflare")
			w.WriteHeader(403)
			w.Write([]byte("<html>Just a moment...</html>"))
		case 1:
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(429)
		case 2:
			w.WriteHeader(403)
			w.Write([]byte(`{"error":"key"}`))
		case 3:
			w.Write([]byte(strings.Repeat("x", 100)))
		}
	})
	defer srv.Close()
	get := func(max int64) error {
		_, err := Get(context.Background(), srv.Client(), srv.URL, nil, max)
		return err
	}
	if k, _ := Classify(get(1000)); k != KBlocked {
		t.Errorf("挑战页应归被拦，得 %s", k)
	}
	mode.Store(1)
	if k, d := Classify(get(1000)); k != KRate || d != 30*time.Second {
		t.Errorf("429 带 Retry-After：%s %v", k, d)
	}
	mode.Store(2)
	if k, _ := Classify(get(1000)); k != KAuth {
		t.Errorf("普通 403 应归需处理，得 %s", k)
	}
	mode.Store(3)
	if err := get(10); !errors.Is(err, ErrTooLarge) {
		t.Errorf("超限应报 ErrTooLarge：%v", err)
	}
}

func TestFallbackToNextProvider(t *testing.T) {
	h := newHarness(t, nil)
	res, err := h.e.Do(context.Background(), fetchers(map[string]Fetcher{
		"a": bad(&StatusError{Code: 502}), "b": ok("来自b"), "c": ok("来自c"),
	}))
	if err != nil || res.Value != "来自b" || res.Provider.ID != "b" || res.Attempts != 2 {
		t.Fatalf("主提供方挂了应回退到 b：%+v %v", res, err)
	}
	snap := h.e.Snapshot()
	var a ProviderView
	for _, p := range snap[0].Providers {
		if p.ID == "a" {
			a = p
		}
	}
	if a.LastKind != K5xx {
		t.Errorf("a 的失败应记下来：%+v", a)
	}
}

func TestAllFailedAndNoProvider(t *testing.T) {
	h := newHarness(t, nil)
	_, err := h.e.Do(context.Background(), fetchers(map[string]Fetcher{
		"a": bad(&StatusError{Code: 500}), "b": bad(context.DeadlineExceeded), "c": bad(errors.New("refused")),
	}))
	var ce *CallError
	if !errors.Is(err, ErrAllFailed) || !errors.As(err, &ce) || ce.Attempts != 3 {
		t.Fatalf("都挂了应报 ErrAllFailed 且试了 3 家：%v", err)
	}
	// 没有任何实现的类别：一个能用的都没有
	_, err = h.e.Do(context.Background(), Request{Category: "t", Region: RegionGlobal, Fetch: map[string]Fetcher{}})
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("没有可用提供方应报 ErrNoProvider：%v", err)
	}
}

func TestMaxThreeAttempts(t *testing.T) {
	c := testCatalog()
	c.Providers = append(c.Providers, Provider{ID: "f", Category: "t", Name: "f", URL: "https://example.test/f", Tier: map[string]int{"*": 4}, Network: NetProxy, Auth: "none"})
	h := newHarness(t, func(o *Options) { o.Catalog = c })
	var n atomic.Int32
	f := func(context.Context, Env) (any, error) { n.Add(1); return nil, &StatusError{Code: 500} }
	h.e.Do(context.Background(), fetchers(map[string]Fetcher{"a": f, "b": f, "c": f, "f": f}))
	if n.Load() != 3 {
		t.Errorf("一次逻辑调用最多试 3 家，试了 %d", n.Load())
	}
}

func TestNotFoundTriesOneMoreProvider(t *testing.T) {
	h := newHarness(t, nil)
	var calls []string
	nf := func(id string) Fetcher {
		return func(context.Context, Env) (any, error) { calls = append(calls, id); return nil, ErrNotFound }
	}
	_, err := h.e.Do(context.Background(), fetchers(map[string]Fetcher{"a": nf("a"), "b": nf("b"), "c": nf("c")}))
	if !errors.Is(err, ErrNotFound) || len(calls) != 2 {
		t.Fatalf("查无此地只换一家再问一次，之后回查无：%v %v", err, calls)
	}
	// 第二家有结果就用第二家
	res, err := h.e.Do(context.Background(), fetchers(map[string]Fetcher{"a": nf("a"), "b": ok("有"), "c": ok("c")}))
	if err != nil || res.Value != "有" {
		t.Fatalf("第二家有结果应成功：%v %v", res, err)
	}
	// 查无结果不算提供方的错：不进退避
	for i := 0; i < 5; i++ {
		h.clk.Add(time.Second)
		h.e.Do(context.Background(), fetchers(map[string]Fetcher{"a": nf("a"), "b": nf("b")}))
	}
	for _, p := range h.e.Snapshot()[0].Providers {
		if p.State != StateOK {
			t.Errorf("查无结果不该让 %s 变成 %s", p.ID, p.State)
		}
	}
}

func TestBackoffAfterThreeFailuresAndRecovery(t *testing.T) {
	h := newHarness(t, nil)
	fail := bad(&StatusError{Code: 503})
	req := fetchers(map[string]Fetcher{"a": fail, "b": ok("b")})
	for i := 0; i < 3; i++ {
		h.clk.Add(time.Second)
		h.e.Do(context.Background(), req)
	}
	st := stateOfView(h.e, "a")
	if st.State != StateBackoff || !st.Until.After(h.clk.Now()) {
		t.Fatalf("连续失败 3 次应进退避：%+v", st)
	}
	// 退避期间不再试 a
	var aCalls atomic.Int32
	req.Fetch["a"] = func(context.Context, Env) (any, error) { aCalls.Add(1); return nil, &StatusError{Code: 503} }
	h.e.Do(context.Background(), req)
	if aCalls.Load() != 0 {
		t.Error("退避中的提供方不该被试")
	}
	// 第一次退避 1 分钟；到点后再失败翻倍
	h.clk.Add(61 * time.Second)
	h.e.Do(context.Background(), req)
	if aCalls.Load() != 1 {
		t.Fatalf("到点应放行试一次：%d", aCalls.Load())
	}
	st = stateOfView(h.e, "a")
	if st.State != StateBackoff || st.Until.Sub(h.clk.Now()) < 90*time.Second {
		t.Errorf("再失败应翻倍到 2 分钟：%+v", st)
	}
	// 恢复
	h.clk.Add(3 * time.Minute)
	req.Fetch["a"] = ok("a")
	if res, _ := h.e.Do(context.Background(), req); res.Provider.ID != "a" {
		t.Errorf("退避结束后成功应回到 a：%+v", res)
	}
	if st = stateOfView(h.e, "a"); st.State != StateOK {
		t.Errorf("成功后应清零：%+v", st)
	}
}

func TestBackoffCapsAtOneHour(t *testing.T) {
	d := defaultBackoff(func(int) int { return 200 }) // 抖动为 0
	if d(1) != time.Minute || d(2) != 2*time.Minute || d(20) != time.Hour {
		t.Errorf("退避应 1 分钟起翻倍、封顶 1 小时：%v %v %v", d(1), d(2), d(20))
	}
	lo, hi := defaultBackoff(func(int) int { return 0 }), defaultBackoff(func(int) int { return 400 })
	if lo(3) >= 4*time.Minute || hi(3) <= 4*time.Minute {
		t.Errorf("抖动应在 ±20%%：%v %v", lo(3), hi(3))
	}
}

func TestSoloCandidateNeverBacksOff(t *testing.T) {
	h := newHarness(t, nil)
	for i := 0; i < 8; i++ {
		h.clk.Add(time.Second)
		_, err := h.e.Do(context.Background(), Request{Category: "solo", Region: RegionGlobal, Fetch: map[string]Fetcher{"e": bad(&StatusError{Code: 500})}})
		if !errors.Is(err, ErrAllFailed) {
			t.Fatalf("唯一候选失败照常回报，不该变成没有可用：%v", err)
		}
	}
	if v := stateOfView(h.e, "e"); v.State != StateOK {
		t.Errorf("唯一候选不进退避：%+v", v)
	}
}

func TestRateLimitDoesNotCountAsUnhealthy(t *testing.T) {
	h := newHarness(t, nil)
	req := fetchers(map[string]Fetcher{"a": bad(&StatusError{Code: 429, RetryAfter: 90 * time.Second}), "b": ok("b")})
	for i := 0; i < 5; i++ {
		h.e.Do(context.Background(), req)
		h.clk.Add(100 * time.Second)
	}
	v := stateOfView(h.e, "a")
	if v.State == StateAttention || h.e.st.Providers[stKey("a", NetDirect)].Streak != 0 || h.e.st.Providers[stKey("a", NetDirect)].BackoffN != 0 {
		t.Errorf("429 不算不健康：%+v", v)
	}
	// 刚收到 429 的时候是暂停
	req.Fetch["a"] = bad(&StatusError{Code: 429, RetryAfter: 90 * time.Second})
	h.e.Do(context.Background(), req)
	if v = stateOfView(h.e, "a"); v.State != StatePaused || v.Until.Sub(h.clk.Now()) < 80*time.Second {
		t.Errorf("429 应按 Retry-After 暂停：%+v", v)
	}
}

func TestAuthAndSchemaNeedAttention(t *testing.T) {
	h := newHarness(t, nil)
	req := fetchers(map[string]Fetcher{"a": bad(&StatusError{Code: 401}), "b": ok("b")})
	h.e.Do(context.Background(), req)
	if v := stateOfView(h.e, "a"); v.State != StateAttention || v.Reason != KAuth {
		t.Fatalf("401 应转需处理：%+v", v)
	}
	// 需处理的不再被试
	var n atomic.Int32
	req.Fetch["a"] = func(context.Context, Env) (any, error) { n.Add(1); return "x", nil }
	h.e.Do(context.Background(), req)
	if n.Load() != 0 {
		t.Error("需处理的提供方不该被自动调用")
	}
	// 格式变了：连续 2 次才转需处理，1 次只记失败
	sreq := fetchers(map[string]Fetcher{"b": bad(Schema("缺 temp")), "c": ok("c")})
	h.e.Do(context.Background(), sreq)
	if v := stateOfView(h.e, "b"); v.State != StateOK {
		t.Fatalf("格式变了第一次不该转需处理：%+v", v)
	}
	h.e.Do(context.Background(), sreq)
	if v := stateOfView(h.e, "b"); v.State != StateAttention || v.Reason != KSchema {
		t.Fatalf("连续两次格式不对应转需处理：%+v", v)
	}
}

func TestManualOffNeverResurrected(t *testing.T) {
	h := newHarness(t, nil)
	var probed atomic.Int32
	h.e.RegisterProbe("a", func(context.Context, Env) error { probed.Add(1); return nil })
	if err := h.e.SetOff("a", true); err != nil {
		t.Fatal(err)
	}
	// 探测一轮不碰它
	h.e.probeRound(context.Background(), *h.set, func(time.Duration) bool { return true }, func() time.Duration { return 0 })
	if probed.Load() != 0 {
		t.Error("手动关的提供方不该被探测")
	}
	var called atomic.Int32
	res, _ := h.e.Do(context.Background(), fetchers(map[string]Fetcher{"a": func(context.Context, Env) (any, error) { called.Add(1); return "a", nil }, "b": ok("b")}))
	if called.Load() != 0 || res.Provider.ID != "b" {
		t.Errorf("手动关的不参与调用：%+v", res)
	}
	if v := stateOfView(h.e, "a"); v.State != StateOff || !v.Off {
		t.Errorf("页面应显示已关：%+v", v)
	}
	if err := h.e.SetOff("nope", true); err == nil {
		t.Error("不存在的接口应报错")
	}
}

func TestPinnedFirstEvenIfLowerTier(t *testing.T) {
	h := newHarness(t, nil)
	h.e.SetPinned("c", true)
	res, _ := h.e.Do(context.Background(), fetchers(map[string]Fetcher{"a": ok("a"), "b": ok("b"), "c": ok("c")}))
	if res.Provider.ID != "c" {
		t.Errorf("置顶的排最前：%s", res.Provider.ID)
	}
	// 置顶只是排前面：它挂了照样回退
	res, _ = h.e.Do(context.Background(), fetchers(map[string]Fetcher{"a": ok("a"), "c": bad(&StatusError{Code: 500})}))
	if res.Provider.ID != "a" {
		t.Errorf("置顶的挂了应回退：%s", res.Provider.ID)
	}
	// 同类别只留一个置顶
	h.e.SetPinned("a", true)
	for _, p := range h.e.Snapshot()[0].Providers {
		if p.Pinned != (p.ID == "a") {
			t.Errorf("同类别只该有一个置顶：%s %v", p.ID, p.Pinned)
		}
	}
}

func TestRegionFilterAndTier(t *testing.T) {
	h := newHarness(t, nil)
	all := map[string]Fetcher{"a": ok("a"), "d": ok("d")}
	// 国内：d 是只服务国内的，档位和 a 同为 1；分数里区域匹配 d 更高
	req := fetchers(all)
	req.Region = RegionCN
	if res, _ := h.e.Do(context.Background(), req); res.Provider.ID != "d" {
		t.Errorf("国内应优先只服务国内的：%s", res.Provider.ID)
	}
	// 国外：d 不服务
	req.Region = RegionGlobal
	if res, _ := h.e.Do(context.Background(), req); res.Provider.ID != "a" {
		t.Errorf("国外不该用只服务国内的：%s", res.Provider.ID)
	}
	// 只有 d 且在国外：没有可用的
	req = fetchers(map[string]Fetcher{"d": ok("d")})
	if _, err := h.e.Do(context.Background(), req); !errors.Is(err, ErrNoProvider) {
		t.Errorf("区域不符的不该被调用：%v", err)
	}
}

func TestSameTierBufferSpreadsTraffic(t *testing.T) {
	c := testCatalog()
	c.Providers[1].Tier = map[string]int{"*": 1} // a、b 同档
	pick := 1
	h := newHarness(t, func(o *Options) { o.Catalog = c; o.Rand = func(n int) int { return min(pick, n-1) } })
	res, _ := h.e.Do(context.Background(), fetchers(map[string]Fetcher{"a": ok("a"), "b": ok("b")}))
	if res.Provider.ID != "b" {
		t.Errorf("同档分数相近时随机取一个排前面：%s", res.Provider.ID)
	}
	pick = 0
	h = newHarness(t, func(o *Options) { o.Catalog = c; o.Rand = func(n int) int { return min(pick, n-1) } })
	if res, _ = h.e.Do(context.Background(), fetchers(map[string]Fetcher{"a": ok("a"), "b": ok("b")})); res.Provider.ID != "a" {
		t.Errorf("随机数为 0 取最高分：%s", res.Provider.ID)
	}
}

func TestSuccessRateShrinksTowardPrior(t *testing.T) {
	now := time.Now()
	one := []sample{{T: now.UnixMilli(), K: KOK}}
	fail := []sample{{T: now.UnixMilli(), K: K5xx}}
	r1, n1 := successRate(one, now)
	r2, _ := successRate(fail, now)
	if n1 != 1 || r1 >= 0.95 || r2 <= 0.5 {
		t.Errorf("一次成功不该满分、一次失败不该垫底：%.2f %.2f", r1, r2)
	}
	if r0, n0 := successRate(nil, now); n0 != 0 || r0 != 0.8 {
		t.Errorf("没样本就是先验 0.8：%.2f", r0)
	}
	many := make([]sample, 40)
	for i := range many {
		many[i] = sample{T: now.UnixMilli(), K: KOK}
	}
	if r, _ := successRate(many, now); r < 0.95 {
		t.Errorf("样本多了应接近真实成功率：%.2f", r)
	}
	// 429 不计，24 小时前的不计
	old := []sample{{T: now.Add(-30 * time.Hour).UnixMilli(), K: K5xx}, {T: now.UnixMilli(), K: KRate}}
	if _, n := successRate(old, now); n != 0 {
		t.Errorf("429 和过期样本不该计入：%d", n)
	}
}

func TestStrongTryWhenAllBackedOff(t *testing.T) {
	h := newHarness(t, nil)
	fail := &StatusError{Code: 500}
	req := fetchers(map[string]Fetcher{"a": bad(fail), "b": bad(fail), "c": bad(fail)})
	for i := 0; i < 3; i++ {
		h.clk.Add(time.Second)
		h.e.Do(context.Background(), req)
	}
	for _, id := range []string{"a", "b", "c"} {
		if v := stateOfView(h.e, id); v.State != StateBackoff {
			t.Fatalf("%s 应在退避：%+v", id, v)
		}
	}
	var n atomic.Int32
	count := func(context.Context, Env) (any, error) { n.Add(1); return nil, fail }
	req.Fetch = map[string]Fetcher{"a": count, "b": count, "c": count}
	h.clk.Add(time.Second)
	h.e.Do(context.Background(), req) // 强试一个
	if n.Load() != 1 {
		t.Fatalf("全在退避时强试 1 个：%d", n.Load())
	}
	h.clk.Add(5 * time.Second)
	if _, err := h.e.Do(context.Background(), req); !errors.Is(err, ErrNoProvider) || n.Load() != 1 {
		t.Fatalf("30 秒内不再强试：%v %d", err, n.Load())
	}
	h.clk.Add(31 * time.Second)
	h.e.Do(context.Background(), req)
	if n.Load() != 2 {
		t.Errorf("30 秒后可以再强试：%d", n.Load())
	}
}

func TestProviderCapsSkipAndFallThrough(t *testing.T) {
	h := newHarness(t, func(o *Options) {})
	h.set.Providers = map[string]ProviderCap{"a": {DailyCap: 2, PerMinute: 100}}
	req := fetchers(map[string]Fetcher{"a": ok("a"), "b": ok("b")})
	for i := 0; i < 2; i++ {
		if res, _ := h.e.Do(context.Background(), req); res.Provider.ID != "a" {
			t.Fatalf("第 %d 次应还是 a", i+1)
		}
	}
	if res, _ := h.e.Do(context.Background(), req); res.Provider.ID != "b" {
		t.Errorf("a 的每天上限到了应换 b：%s", res.Provider.ID)
	}
	if v := stateOfView(h.e, "a"); v.State != StateQuota || v.UsedToday != 2 || v.DailyCap != 2 {
		t.Errorf("页面应显示额度用完：%+v", v)
	}
	h.clk.Add(24 * time.Hour)
	if res, _ := h.e.Do(context.Background(), req); res.Provider.ID != "a" {
		t.Errorf("过了零点应恢复：%s", res.Provider.ID)
	}
	// 每分钟
	h.set.Providers = map[string]ProviderCap{"a": {DailyCap: 1000, PerMinute: 1}}
	h.clk.Add(48 * time.Hour)
	h.e.Do(context.Background(), req)
	if res, _ := h.e.Do(context.Background(), req); res.Provider.ID != "b" {
		t.Errorf("a 的每分钟上限到了应换 b：%s", res.Provider.ID)
	}
	h.clk.Add(61 * time.Second)
	if res, _ := h.e.Do(context.Background(), req); res.Provider.ID != "a" {
		t.Errorf("过了一分钟应恢复：%s", res.Provider.ID)
	}
}

func TestCacheAndSingleFlight(t *testing.T) {
	h := newHarness(t, nil)
	var n atomic.Int32
	release := make(chan struct{})
	slow := func(context.Context, Env) (any, error) { n.Add(1); <-release; return "v", nil }
	req := fetchers(map[string]Fetcher{"a": slow})
	req.CacheKey, req.CacheTTL = "k1", 10*time.Minute
	var wg sync.WaitGroup
	results := make([]Result, 5)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = h.e.Do(context.Background(), req)
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n.Load() != 1 {
		t.Fatalf("相同请求同时进来应合并成一次：%d", n.Load())
	}
	// 命中缓存不扣提供方额度
	before := h.e.ProviderUsedToday("a")
	res, _ := h.e.Do(context.Background(), req)
	if !res.Cached || n.Load() != 1 || h.e.ProviderUsedToday("a") != before {
		t.Errorf("缓存命中不该联网也不该扣提供方额度：%+v", res)
	}
	h.clk.Add(11 * time.Minute)
	req.Fetch["a"] = ok("v2")
	if res, _ = h.e.Do(context.Background(), req); res.Cached || res.Value != "v2" {
		t.Errorf("过期后应重新取：%+v", res)
	}
	// 失败不缓存
	req.CacheKey = "k2"
	req.Fetch["a"] = bad(&StatusError{Code: 500})
	h.e.Do(context.Background(), req)
	req.Fetch["a"] = ok("好了")
	if res, _ = h.e.Do(context.Background(), req); res.Value != "好了" {
		t.Errorf("失败的结果不该进缓存：%+v", res)
	}
}

// waitWaiters 等到有 n 个调用挂在这个请求的合并上（用调度让出轮询，不靠固定睡眠）。
func waitWaiters(t *testing.T, e *Engine, key string, n int) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		e.mu.Lock()
		got := 0
		if f := e.flights[key]; f != nil {
			got = f.waiters
		}
		e.mu.Unlock()
		if got >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("等了 10 秒，挂在合并上的调用只有 %d 个", got)
		default:
			runtime.Gosched()
		}
	}
}

// 回归：领头的调用被取消，等着的调用不能跟着拿“接口都没成功”；它们回头重来，其中一个接着发，大家共享这次的结果。
// （旧代码把领头的取消错误原样发给所有等待者，等待者以为全部失败。）
func TestFlightLeaderCanceledWaitersRetry(t *testing.T) {
	h := newHarness(t, nil)
	var n atomic.Int32
	started := make(chan struct{})
	fetch := func(ctx context.Context, _ Env) (any, error) {
		if n.Add(1) == 1 { // 领头的这一次：等到被取消
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return "v", nil
	}
	req := fetchers(map[string]Fetcher{"a": fetch})
	req.CacheKey, req.CacheTTL = "k3", 10*time.Minute
	key := "t" + string(rune(0)) + "k3"

	leaderCtx, cancel := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() { _, err := h.e.Do(leaderCtx, req); leaderDone <- err }()
	<-started

	type out struct {
		res Result
		err error
	}
	waiters := make([]chan out, 3)
	for i := range waiters {
		waiters[i] = make(chan out, 1)
		go func() { r, err := h.e.Do(context.Background(), req); waiters[i] <- out{r, err} }()
	}
	waitWaiters(t, h.e, key, len(waiters))
	cancel()

	if err := <-leaderDone; err == nil {
		t.Error("领头的自己被取消，应该拿到错误")
	}
	for i, ch := range waiters {
		o := <-ch
		if o.err != nil || o.res.Value != "v" {
			t.Errorf("等待者 %d 不该因为领头被取消而失败：%+v %v", i, o.res, o.err)
		}
	}
	if got := n.Load(); got != 2 {
		t.Errorf("领头取消后应只有一个等待者接着发，共 2 次，实际 %d", got)
	}
	// 领头的取消不记成提供方的错
	if res, err := h.e.Do(context.Background(), req); err != nil || !res.Cached || res.Provider.ID != "a" {
		t.Errorf("接着发成功后应进缓存：%+v %v", res, err)
	}
}

// 真失败（领头的 ctx 好好的）照旧发给所有等待者，不重试：等待者各自重发会把一次失败放大成 N 次。
func TestFlightRealFailureStillShared(t *testing.T) {
	h := newHarness(t, nil)
	var n atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	fetch := func(context.Context, Env) (any, error) {
		n.Add(1)
		close(started)
		<-release
		return nil, &StatusError{Code: 500}
	}
	req := fetchers(map[string]Fetcher{"a": fetch})
	req.CacheKey = "k4"
	key := "t" + string(rune(0)) + "k4"
	leader := make(chan error, 1)
	go func() { _, err := h.e.Do(context.Background(), req); leader <- err }()
	<-started
	waiter := make(chan error, 1)
	go func() { _, err := h.e.Do(context.Background(), req); waiter <- err }()
	waitWaiters(t, h.e, key, 1)
	close(release)
	if err := <-leader; !errors.Is(err, ErrAllFailed) {
		t.Errorf("领头的应拿到真失败：%v", err)
	}
	if err := <-waiter; !errors.Is(err, ErrAllFailed) {
		t.Errorf("等待者应共享真失败：%v", err)
	}
	if n.Load() != 1 {
		t.Errorf("真失败不该让等待者重发：%d 次", n.Load())
	}
}

// 等待者自己被取消就只返回自己的取消，不影响领头的和别的等待者。
func TestFlightWaiterCanceledReturnsOwnError(t *testing.T) {
	h := newHarness(t, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	fetch := func(context.Context, Env) (any, error) { close(started); <-release; return "v", nil }
	req := fetchers(map[string]Fetcher{"a": fetch})
	req.CacheKey = "k5"
	key := "t" + string(rune(0)) + "k5"
	leader := make(chan error, 1)
	go func() { _, err := h.e.Do(context.Background(), req); leader <- err }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	waiter := make(chan error, 1)
	go func() { _, err := h.e.Do(ctx, req); waiter <- err }()
	waitWaiters(t, h.e, key, 1)
	cancel()
	if err := <-waiter; !errors.Is(err, context.Canceled) {
		t.Errorf("等待者自己被取消应拿到取消：%v", err)
	}
	close(release)
	if err := <-leader; err != nil {
		t.Errorf("等待者取消不该影响领头的：%v", err)
	}
}

type expiring struct{ at time.Time }

func (e expiring) ExpiresAt() time.Time { return e.at }

func TestCacheHonorsExpiring(t *testing.T) {
	h := newHarness(t, nil)
	var n atomic.Int32
	req := fetchers(map[string]Fetcher{"a": func(context.Context, Env) (any, error) {
		n.Add(1)
		return expiring{at: h.clk.Now().Add(time.Minute)}, nil
	}})
	req.CacheKey, req.CacheTTL = "fx", 6*time.Hour
	h.e.Do(context.Background(), req)
	h.clk.Add(2 * time.Minute)
	h.e.Do(context.Background(), req)
	if n.Load() != 2 {
		t.Errorf("结果自己说的过期时间早于 TTL，应按它：%d", n.Load())
	}
}

func TestAttemptTimeoutIsPerAttempt(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.AttemptMax = 30 * time.Millisecond })
	hang := func(ctx context.Context, _ Env) (any, error) { <-ctx.Done(); return nil, ctx.Err() }
	res, err := h.e.Do(context.Background(), fetchers(map[string]Fetcher{"a": hang, "b": ok("b")}))
	if err != nil || res.Provider.ID != "b" {
		t.Fatalf("a 卡住超过单次时限应换 b：%+v %v", res, err)
	}
	if v := stateOfView(h.e, "a"); v.LastKind != KTimeout {
		t.Errorf("单次超时应记成 timeout：%+v", v)
	}
}

func TestCallerCancelIsNotProviderFailure(t *testing.T) {
	h := newHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	f := func(c context.Context, _ Env) (any, error) { cancel(); <-c.Done(); return nil, c.Err() }
	h.e.Do(ctx, fetchers(map[string]Fetcher{"a": f, "b": ok("b")}))
	if v := stateOfView(h.e, "a"); v.LastKind == KTimeout || v.LastKind == KCanceled && v.LastCheck != (time.Time{}) {
		t.Errorf("调用方自己取消不该记成提供方的错：%+v", v)
	}
}

func stateOfView(e *Engine, id string) ProviderView {
	for _, c := range e.Snapshot() {
		for _, p := range c.Providers {
			if p.ID == id {
				return p
			}
		}
	}
	return ProviderView{}
}

// ---- 次数 ----

func TestAllowPerActorDaily(t *testing.T) {
	h := newHarness(t, nil)
	who := Who{ActorKind: "human", ActorID: "u1", Conversation: "c1"}
	for i := 0; i < 20; i++ {
		if d := h.e.Allow("weather", who); d.Denied {
			t.Fatalf("第 %d 次不该被拒", i+1)
		}
	}
	d := h.e.Allow("weather", who)
	if !d.Denied || d.Reason != "actor" || d.Limit != 20 {
		t.Fatalf("第 21 次应被拒：%+v", d)
	}
	if h.e.Allow("weather", Who{ActorKind: "human", ActorID: "u2", Conversation: "c1"}).Denied {
		t.Error("别人不受影响")
	}
	if h.e.Allow("exchange_rate", who).Denied {
		t.Error("每个工具各算各的")
	}
	h.clk.Add(24 * time.Hour)
	if h.e.Allow("weather", who).Denied {
		t.Error("过了零点应恢复")
	}
}

func TestAllowPerGroupHourlyAndNoPartialCharge(t *testing.T) {
	h := newHarness(t, nil)
	grp := func(id string) Who { return Who{ActorKind: "human", ActorID: id, Group: true, Conversation: "g1"} }
	for i := 0; i < 15; i++ {
		if h.e.Allow("weather", grp(string(rune('a'+i)))).Denied {
			t.Fatalf("第 %d 次不该被拒", i+1)
		}
	}
	d := h.e.Allow("weather", grp("zz"))
	if !d.Denied || d.Reason != "group" || d.Limit != 15 {
		t.Fatalf("第 16 次应被群上限拒：%+v", d)
	}
	// 被拒不扣“人”的次数
	h.e.mu.Lock()
	n := h.e.used(actorKey("weather", grp("zz")), h.e.dayKey(h.clk.Now()))
	h.e.mu.Unlock()
	if n != 0 {
		t.Errorf("被群上限拒了，这个人不该被扣：%d", n)
	}
	// 另一个群不受影响；私聊（非群）不看群上限
	if h.e.Allow("weather", Who{ActorKind: "human", ActorID: "p1", Group: true, Conversation: "g2"}).Denied {
		t.Error("别的群不受影响")
	}
	h.clk.Add(61 * time.Minute)
	if h.e.Allow("weather", grp("zz")).Denied {
		t.Error("过了这个小时应恢复")
	}
	for i := 0; i < 30; i++ {
		if h.e.Allow("weather", Who{ActorKind: "human", ActorID: "same", Conversation: "dm"}).Denied && i < 20 {
			t.Fatalf("私聊不看群上限，第 %d 次被拒", i+1)
		}
	}
}

func TestAllowOwnerUnlimitedAndZeroMeansUnlimited(t *testing.T) {
	h := newHarness(t, nil)
	owner := Who{Owner: true, ActorKind: "owner", ActorID: "owner", Group: true, Conversation: "g1"}
	for i := 0; i < 100; i++ {
		if h.e.Allow("weather", owner).Denied {
			t.Fatal("主人不限")
		}
	}
	if h.e.ToolUsedToday("weather") != 100 {
		t.Errorf("主人的总用量照记：%d", h.e.ToolUsedToday("weather"))
	}
	// 主人用了不占别人的群额度
	if h.e.Allow("weather", Who{ActorKind: "human", ActorID: "x", Group: true, Conversation: "g1"}).Denied {
		t.Error("主人的调用不该占群的小时额度")
	}
	// 0 不限
	for i := 0; i < 50; i++ {
		if h.e.Allow("calendar", Who{ActorKind: "human", ActorID: "x", Group: true, Conversation: "g1"}).Denied {
			t.Fatal("0 表示不限")
		}
	}
}

func TestAllowUnknownIdentitySharesOneBucket(t *testing.T) {
	h := newHarness(t, nil)
	for i := 0; i < 20; i++ {
		h.e.Allow("weather", Who{ActorKind: "", ActorID: ""})
	}
	if d := h.e.Allow("weather", Who{ActorKind: "human", ActorID: ""}); !d.Denied {
		t.Error("身份缺失归同一个桶按普通人限，不是不限")
	}
	if d := h.e.Allow("weather", Who{ActorKind: "", ActorID: "x"}); !d.Denied {
		t.Error("种类缺失同样归未知桶")
	}
}

func TestAllowConcurrentDoesNotOversell(t *testing.T) {
	h := newHarness(t, nil)
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !h.e.Allow("weather", Who{ActorKind: "human", ActorID: "same"}).Denied {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 20 {
		t.Errorf("并发不超卖：放行了 %d", allowed.Load())
	}
}

func TestProviderFailuresChargePersonOnlyOnce(t *testing.T) {
	h := newHarness(t, nil)
	who := Who{ActorKind: "human", ActorID: "u1"}
	h.e.Allow("weather", who)
	h.e.Do(context.Background(), fetchers(map[string]Fetcher{
		"a": bad(&StatusError{Code: 500}), "b": bad(&StatusError{Code: 500}), "c": bad(&StatusError{Code: 500}),
	}))
	h.e.mu.Lock()
	n := h.e.used(actorKey("weather", who), h.e.dayKey(h.clk.Now()))
	h.e.mu.Unlock()
	if n != 1 {
		t.Errorf("全部提供方挂掉，这个人最多被扣一次：%d", n)
	}
	for _, id := range []string{"a", "b", "c"} {
		if h.e.ProviderUsedToday(id) != 1 {
			t.Errorf("回退里每次尝试各扣自己的提供方额度：%s=%d", id, h.e.ProviderUsedToday(id))
		}
	}
}

// ---- 探测 ----

func TestManualProbeRateLimitAndClearsAttention(t *testing.T) {
	h := newHarness(t, nil)
	var fail atomic.Bool
	fail.Store(true)
	h.e.RegisterProbe("a", func(context.Context, Env) error {
		if fail.Load() {
			return &StatusError{Code: 401}
		}
		return nil
	})
	r := h.e.ProbeNow(context.Background(), "a")
	if len(r) != 1 || r[0].Kind != KAuth {
		t.Fatalf("手动检测应得 auth：%+v", r)
	}
	if v := stateOfView(h.e, "a"); v.State != StateAttention {
		t.Fatalf("检测被拒应转需处理：%+v", v)
	}
	fail.Store(false)
	if r = h.e.ProbeNow(context.Background(), "a"); r[0].Skipped == "" {
		t.Error("一分钟内不能连点")
	}
	h.clk.Add(61 * time.Second)
	if r = h.e.ProbeNow(context.Background(), "a"); r[0].Kind != KOK {
		t.Fatalf("检测通过：%+v", r)
	}
	if v := stateOfView(h.e, "a"); v.State != StateOK {
		t.Errorf("手动检测成功应解除需处理：%+v", v)
	}
}

func TestScheduledProbeDoesNotResurrectAttentionOrTouchRecentSuccess(t *testing.T) {
	h := newHarness(t, nil)
	var calls sync.Map
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		h.e.RegisterProbe(id, func(context.Context, Env) error {
			v, _ := calls.LoadOrStore(id, new(atomic.Int32))
			v.(*atomic.Int32).Add(1)
			return nil
		})
	}
	// a 需处理；b 刚有真实成功
	h.e.Do(context.Background(), fetchers(map[string]Fetcher{"a": bad(&StatusError{Code: 401}), "b": ok("b")}))
	h.e.probeRound(context.Background(), *h.set, func(time.Duration) bool { return true }, func() time.Duration { return 0 })
	count := func(id string) int32 {
		if v, ok := calls.Load(id); ok {
			return v.(*atomic.Int32).Load()
		}
		return 0
	}
	if count("a") != 1 {
		t.Error("需处理的也要按间隔定时探测，好自己恢复")
	}
	if count("b") != 0 {
		t.Error("最近一半间隔内已有真实成功的不再另测")
	}
	if count("c") != 1 || count("d") != 1 || count("e") != 1 {
		t.Errorf("其余应各测一次：c=%d d=%d e=%d", count("c"), count("d"), count("e"))
	}
	// 探测失败要记，且不是用户的次数
	if v := stateOfView(h.e, "c"); v.LastKind != KOK {
		t.Errorf("探测成功要记下检测时间：%+v", v)
	}
}

func TestScheduledProbeClearsSchemaAttention(t *testing.T) {
	h := newHarness(t, nil)
	var okNow atomic.Bool
	h.e.RegisterProbe("b", func(context.Context, Env) error {
		if okNow.Load() {
			return nil
		}
		return Schema("缺字段")
	})
	// 真实调用里两个怪地点让 b 连续格式不对，转需处理
	sreq := fetchers(map[string]Fetcher{"b": bad(Schema("degree 不是数字")), "c": ok("c")})
	h.e.Do(context.Background(), sreq)
	h.e.Do(context.Background(), sreq)
	if v := stateOfView(h.e, "b"); v.State != StateAttention {
		t.Fatalf("前置：应转需处理：%+v", v)
	}
	round := func() {
		h.e.probeRound(context.Background(), *h.set, func(time.Duration) bool { return true }, func() time.Duration { return 0 })
	}
	round() // 探测仍不对：保持需处理
	if v := stateOfView(h.e, "b"); v.State != StateAttention {
		t.Fatalf("探测仍不对应保持需处理：%+v", v)
	}
	okNow.Store(true)
	round()
	if v := stateOfView(h.e, "b"); v.State != StateOK {
		t.Fatalf("定期探测通过应自动解除需处理：%+v", v)
	}
}

func TestNotFoundThenFailureIsNotNotFound(t *testing.T) {
	h := newHarness(t, nil)
	// a 查无、b 失败、c 正常：要试到 c，不能回“查无此地”
	res, err := h.e.Do(context.Background(), fetchers(map[string]Fetcher{
		"a": bad(ErrNotFound), "b": bad(&StatusError{Code: 503}), "c": ok("来自c"),
	}))
	if err != nil || res.Value != "来自c" {
		t.Fatalf("查无后换的一家失败，应继续试第三家：%+v %v", res, err)
	}
	// a 查无、b 失败、c 也失败：是失败，不是查无
	h2 := newHarness(t, nil)
	_, err = h2.e.Do(context.Background(), fetchers(map[string]Fetcher{
		"a": bad(ErrNotFound), "b": bad(&StatusError{Code: 503}), "c": bad(&StatusError{Code: 500}),
	}))
	if errors.Is(err, ErrNotFound) || !errors.Is(err, ErrAllFailed) {
		t.Fatalf("夹着失败不能回查无：%v", err)
	}
}

func TestProbeSchedulerStartDelayIntervalAndDisable(t *testing.T) {
	h := newHarness(t, nil)
	var rounds atomic.Int32
	h.e.RegisterProbe("e", func(context.Context, Env) error { rounds.Add(1); return nil })
	var afters []time.Duration
	var mu sync.Mutex
	gate := make(chan struct{}, 8)
	clk := probeClock{
		StartDelay: func() time.Duration { return 5 * time.Minute },
		Gap:        func() time.Duration { return time.Second },
		After: func(d time.Duration) <-chan time.Time {
			mu.Lock()
			afters = append(afters, d)
			mu.Unlock()
			ch := make(chan time.Time, 1)
			go func() { <-gate; ch <- time.Time{} }()
			return ch
		},
	}
	h.e.startProbing(clk)
	h.e.startProbing(clk) // 重复调用无效
	// 启动延迟还没到：一次探测都不该有
	time.Sleep(30 * time.Millisecond)
	if rounds.Load() != 0 {
		t.Fatal("启动后要先等一阵，不能立刻探")
	}
	gate <- struct{}{} // 放行启动延迟
	waitFor(t, func() bool { return rounds.Load() == 1 })
	// 第一轮之后等一个间隔（默认 12 小时）
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(afters) >= 2 })
	mu.Lock()
	if afters[0] != 5*time.Minute || afters[len(afters)-1] != 12*time.Hour {
		t.Errorf("等待时长不对：%v", afters)
	}
	mu.Unlock()
	// 关掉探测：下一轮不探，且只隔 10 分钟再看
	h.set.ProbeEnabled = false
	gate <- struct{}{}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(afters) >= 3 })
	time.Sleep(30 * time.Millisecond)
	if rounds.Load() != 1 {
		t.Errorf("关掉探测后不该再探：%d", rounds.Load())
	}
	mu.Lock()
	if afters[len(afters)-1] != 10*time.Minute {
		t.Errorf("关着时应隔一会儿再看：%v", afters[len(afters)-1])
	}
	mu.Unlock()
	for i := 0; i < 8; i++ {
		gate <- struct{}{}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("等不到条件")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestClampInterval(t *testing.T) {
	cases := map[time.Duration]time.Duration{0: 12 * time.Hour, time.Minute: time.Hour, 100 * time.Hour: 48 * time.Hour, 24 * time.Hour: 24 * time.Hour}
	for in, want := range cases {
		if got := clampInterval(in); got != want {
			t.Errorf("clampInterval(%v)=%v 应为 %v", in, got, want)
		}
	}
}

func TestProbeNotFoundCountsAsSchema(t *testing.T) {
	h := newHarness(t, nil)
	h.e.RegisterProbe("a", func(context.Context, Env) error { return ErrNotFound })
	if r := h.e.ProbeNow(context.Background(), "a"); r[0].Kind != KSchema {
		t.Errorf("探测用固定参数，查无结果说明内容不对：%+v", r)
	}
}

// ---- 持久化 ----

func TestStatePersistsAndHidesIdentity(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "free", "state.json")
	h := newHarness(t, func(o *Options) { o.StateFile = file })
	who := Who{ActorKind: "human", ActorID: "alpha-user", Group: true, Conversation: "qq:group:beta-room"}
	h.e.Allow("weather", who)
	h.e.SetOff("c", true)
	h.e.SetPinned("b", true)
	h.e.Do(context.Background(), fetchers(map[string]Fetcher{"a": ok("a")}))
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "alpha-user") || strings.Contains(string(raw), "beta-room") {
		t.Errorf("状态文件里不该有明文的用户号和会话号：%s", raw)
	}
	var probe stateFile
	if json.Unmarshal(raw, &probe) != nil {
		t.Fatal("状态文件不是合法 JSON")
	}
	h2 := newHarness(t, func(o *Options) { o.StateFile = file })
	h2.clk.t = h.clk.t
	if v := stateOfView(h2.e, "c"); !v.Off {
		t.Error("重启后人工关闭应保留")
	}
	if v := stateOfView(h2.e, "b"); !v.Pinned {
		t.Error("重启后置顶应保留")
	}
	if h2.e.ProviderUsedToday("a") != 1 {
		t.Errorf("重启后提供方用量应保留：%d", h2.e.ProviderUsedToday("a"))
	}
	for i := 0; i < 19; i++ {
		h2.e.Allow("weather", who)
	}
	if !h2.e.Allow("weather", who).Denied {
		t.Error("重启后人的次数应接着算")
	}
}

func TestStateCorruptOrUnwritableDoesNotStopFeature(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "state.json")
	os.WriteFile(file, []byte("{坏了"), 0o600)
	h := newHarness(t, func(o *Options) { o.StateFile = file })
	if _, err := h.e.Do(context.Background(), fetchers(map[string]Fetcher{"a": ok("a")})); err != nil {
		t.Fatalf("状态文件坏了功能照常：%v", err)
	}
	// 写不进：状态路径的父级是个文件
	blocker := filepath.Join(dir, "blocker")
	os.WriteFile(blocker, []byte("x"), 0o600)
	h2 := newHarness(t, func(o *Options) { o.StateFile = filepath.Join(blocker, "state.json") })
	if _, err := h2.e.Do(context.Background(), fetchers(map[string]Fetcher{"a": ok("a")})); err != nil {
		t.Fatalf("写不进状态文件功能照常：%v", err)
	}
	if h2.e.Allow("weather", Who{ActorKind: "human", ActorID: "x"}).Denied {
		t.Error("写不进状态文件不该拒绝用户")
	}
}

func TestCountersPrunedAfterWindow(t *testing.T) {
	h := newHarness(t, nil)
	h.e.Allow("weather", Who{ActorKind: "human", ActorID: "x", Group: true, Conversation: "g"})
	h.clk.Add(48 * time.Hour)
	h.e.Allow("weather", Who{ActorKind: "human", ActorID: "y"})
	h.e.mu.Lock()
	n := len(h.e.st.Counters)
	h.e.mu.Unlock()
	if n > 2 { // 只该剩今天的：y 的人次数和总用量
		t.Errorf("过了窗口的计数应清掉：%d", n)
	}
}

func TestLogsNeverContainPlaceOrIdentity(t *testing.T) {
	h := newHarness(t, nil)
	h.e.Do(context.Background(), fetchers(map[string]Fetcher{"a": bad(&StatusError{Code: 500}), "b": ok("b")}))
	h.e.Allow("weather", Who{ActorKind: "human", ActorID: "u-secret"})
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range h.logs {
		if strings.Contains(l, "secret") || strings.Contains(l, "http") {
			t.Errorf("日志里不该有网址和身份：%s", l)
		}
	}
}
