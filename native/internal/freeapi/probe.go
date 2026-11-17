package freeapi

import (
	"context"
	"math/rand/v2"
	"net/http"
	"time"
)

// 探测（规格-免费接口 第五节）：只带注册表里的固定参数，不带任何用户数据；要解析出关键字段才算通；
// 启动后随机等一会儿才开始，每家之间随机隔几秒；最近一半间隔内已有真实成功的不再另测；人关了的不测；需处理的照样按间隔测，通过就解除。

// ProbeFn 是某个提供方的探测做法：用 env.Provider.Probe 里的固定参数发请求并校验内容，不对就返回错误
type ProbeFn func(ctx context.Context, env Env) error

// RegisterProbe 登记一个提供方的探测做法
func (e *Engine) RegisterProbe(id string, fn ProbeFn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.probes[id] = fn
}

// 探测间隔的范围：用户说“半天到一天”，放宽到 1 到 48 小时
const (
	minProbeInterval = time.Hour
	maxProbeInterval = 48 * time.Hour
	manualProbeGap   = time.Minute
)

// ProbeResult 是一家的检测结果；Skipped 非空 = 没测，原因写在里面
type ProbeResult struct {
	ID      string
	Kind    Kind
	Skipped string
}

func clampInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return 12 * time.Hour
	}
	return min(max(d, minProbeInterval), maxProbeInterval)
}

// probeOne 测一家。manual 为真是人点的：不看“最近已有成功”，成功了能解除需处理
func (e *Engine) probeOne(ctx context.Context, p Provider, s Settings, manual bool) ProbeResult {
	e.mu.Lock()
	fn := e.probes[p.ID]
	route := routeOf(p, s)
	now := e.now()
	switch {
	case fn == nil:
		e.mu.Unlock()
		return ProbeResult{ID: p.ID, Skipped: "没有探测做法"}
	case manual:
		if last, ok := e.lastMan[p.ID]; ok && now.Sub(last) < manualProbeGap {
			e.mu.Unlock()
			return ProbeResult{ID: p.ID, Skipped: "刚检测过，一分钟后再试"}
		}
		e.lastMan[p.ID] = now
	default:
		if e.st.Manual[p.ID].Off {
			e.mu.Unlock()
			return ProbeResult{ID: p.ID, Skipped: "已手动关闭"}
		}
		ps := e.st.Providers[stKey(p.ID, route)]
		// 需处理的照常按间隔检测：探测通过就自动解除（真实调用只是个别地点的内容不对时，不该永远停用）
		if ps != nil && ps.Attention == "" && ps.LastOK > 0 && now.Sub(time.UnixMilli(ps.LastOK)) < clampInterval(s.ProbeInterval)/2 {
			e.mu.Unlock()
			return ProbeResult{ID: p.ID, Skipped: "最近已有成功的调用"}
		}
	}
	e.mu.Unlock()
	if !e.takeProvider(p, s) {
		return ProbeResult{ID: p.ID, Skipped: "今天的上限用完了"}
	}
	cl, err := e.o.HTTP(p.Network == NetDirect)
	if err != nil {
		e.record(p, route, KNet, 0, true, false, manual, 0)
		return ProbeResult{ID: p.ID, Kind: KNet}
	}
	actx, cancel := context.WithTimeout(ctx, e.o.AttemptMax*2)
	defer cancel()
	start := time.Now()
	err = fn(actx, Env{Client: cl, Provider: p, URL: e.urlOf(p)})
	took := time.Since(start)
	k, retry := Classify(err)
	if k != KOK && k != KNotFound && ctx.Err() != nil {
		k = KCanceled
	}
	if k == KNotFound { // 探测的固定参数不该查无结果：当内容不对
		k = KSchema
	}
	solo := false // 探测不受“唯一候选”保护：它是在问接口还活着没有，退避本来就只影响真实调用的排序
	e.record(p, route, k, took, true, solo, manual, retry)
	return ProbeResult{ID: p.ID, Kind: k}
}

// ProbeNow 是页面上的“立即检测”：id 为空测全部。每家一分钟内只能点一次
func (e *Engine) ProbeNow(ctx context.Context, id string) []ProbeResult {
	s := e.o.Settings()
	var out []ProbeResult
	for _, p := range e.cat.Providers {
		if id == "" || p.ID == id {
			out = append(out, e.probeOne(ctx, p, s, true))
		}
	}
	return out
}

// 测试能换的几个时间点
type probeClock struct {
	StartDelay func() time.Duration
	Gap        func() time.Duration
	After      func(time.Duration) <-chan time.Time
}

func defaultClock() probeClock {
	return probeClock{
		StartDelay: func() time.Duration { return time.Minute + time.Duration(rand.Int64N(int64(9*time.Minute))) },
		Gap:        func() time.Duration { return 5*time.Second + time.Duration(rand.Int64N(int64(25*time.Second))) },
		After:      time.After,
	}
}

// StartProbing 开始后台探测。Close 停。重复调用只有第一次有用
func (e *Engine) StartProbing() { e.startProbing(defaultClock()) }

func (e *Engine) startProbing(clk probeClock) {
	e.probeMu.Lock()
	defer e.probeMu.Unlock()
	if e.probeStop != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.probeStop = cancel
	e.probeWG.Add(1)
	go func() {
		defer e.probeWG.Done()
		wait := func(d time.Duration) bool {
			select {
			case <-ctx.Done():
				return false
			case <-clk.After(d):
				return true
			}
		}
		if !wait(clk.StartDelay()) {
			return
		}
		for {
			s := e.o.Settings()
			next := clampInterval(s.ProbeInterval)
			if s.ProbeEnabled {
				e.probeRound(ctx, s, wait, clk.Gap)
			} else {
				next = min(next, 10*time.Minute) // 关着也隔一会儿看看有没有被打开
			}
			if !wait(next) {
				return
			}
		}
	}()
}

func (e *Engine) probeRound(ctx context.Context, s Settings, wait func(time.Duration) bool, gap func() time.Duration) {
	first := true
	for _, p := range e.cat.Providers {
		if ctx.Err() != nil {
			return
		}
		if !first && !wait(gap()) {
			return
		}
		r := e.probeOne(ctx, p, s, false)
		if r.Skipped == "" {
			first = false
		}
	}
}

// Close 停后台探测，等它退出
func (e *Engine) Close() {
	e.probeMu.Lock()
	stop := e.probeStop
	e.probeStop = nil
	e.probeMu.Unlock()
	if stop != nil {
		stop()
	}
	e.probeWG.Wait()
}

// DefaultHTTP 是没给 HTTP 时用的客户端：不跟随跳转，不管代理
func defaultHTTP(bool) (*http.Client, error) {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 30 * time.Second}, nil
}
