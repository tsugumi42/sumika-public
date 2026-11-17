package linkreader

// X 取数的线路（规格-链接发送改版 C2）：fx_v2、vx 两条按近期成功率排前两名同时取，先到的成功结果为准、另一条取消；
// 两条都失败才轮到排在后面的（syndication 备用路线）。结果不合并：哪条先成功就整份用哪条的。

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// 线路名，和 Result.Source、FetchError.Source、插件日志的 route 一致
const (
	RouteFX          = "fx_v2"
	RouteVX          = "vx"
	RouteSyndication = "syndication"
)

const (
	defaultFXTimeout = 4 * time.Second
	// routeWindow 是每条线路记多少次最近结果来算成功率
	routeWindow = 50
)

// RouteAttempt 是一次线路尝试：线路、结果（ok 或失败的 kind，被取消是 canceled）、耗时。只给日志用。
type RouteAttempt struct {
	Route string        `json:"route"`
	Kind  string        `json:"kind"`
	Took  time.Duration `json:"took"`
	// Round 是第几轮：全部超时后整条重试时两轮的记录都在，1 是第一轮、2 是重试那一轮；只给日志用
	Round int `json:"round,omitempty"`
}

// routeStats 在内存里按线路记最近 routeWindow 次的成败；重启清零。被取消的不记（不是那条线路的错）。
type routeStats struct {
	mu   sync.Mutex
	hist map[string][]bool
}

func (s *routeStats) record(route string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hist == nil {
		s.hist = map[string][]bool{}
	}
	h := append(s.hist[route], ok)
	if len(h) > routeWindow {
		h = h[len(h)-routeWindow:]
	}
	s.hist[route] = h
}

// rate 是成功率，用 (成功+1)/(次数+2)：没记录的线路算 0.5，不会因为一两次就排到最前或最后。
func (s *routeStats) rate(route string) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	ok := 0
	for _, v := range s.hist[route] {
		if v {
			ok++
		}
	}
	return float64(ok+1) / float64(len(s.hist[route])+2)
}

type xRoute struct {
	name  string
	fetch func(context.Context, Link) (Result, error)
}

// xRoutes 把 fx_v2、vx 按近期成功率从高到低排（一样时 fx_v2 在前），syndication 固定垫底：
// 它走 CDN 最快，排进前两名就总是先到，而它缺引用、长文和长推文的后半段，只在前两条都失败时用（规格 C2）。
func (f *Fetcher) xRoutes() []xRoute {
	routes := []xRoute{{RouteFX, f.fetchFX}, {RouteVX, f.fetchVX}}
	rates := map[string]float64{}
	for _, r := range routes {
		rates[r.name] = f.xStats.rate(r.name)
	}
	slices.SortStableFunc(routes, func(a, b xRoute) int {
		switch {
		case rates[a.name] > rates[b.name]:
			return -1
		case rates[a.name] < rates[b.name]:
			return 1
		}
		return 0
	})
	if f.syndication {
		routes = append(routes, xRoute{RouteSyndication, f.fetchSyndication})
	}
	return routes
}

func (f *Fetcher) xFXTimeout() time.Duration {
	if f.fxTimeout > 0 {
		return f.fxTimeout
	}
	return defaultFXTimeout
}

type routeOutcome struct {
	route string
	r     Result
	err   error
	took  time.Duration
}

func outcomeKind(err error) string {
	if err == nil {
		return "ok"
	}
	var fe *FetchError
	if errors.As(err, &fe) {
		return fe.Kind
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "error"
}

// syndEmptyReason 标记备用路线“回了空内容”这种不存在：帖子不存在时会这样，接口抽风时也会，只算弱证据。
const syndEmptyReason = "empty_body"

func weakNotFound(err error) bool {
	var fe *FetchError
	return errors.As(err, &fe) && fe.Kind == "not_found" && fe.Reason == syndEmptyReason
}

// localFailure 是本机自己挡下的失败：预算用完（不是接口回的 429）、等请求间隔时被取消。不是线路的错，不记进成功率。
func localFailure(err error) bool {
	var fe *FetchError
	if !errors.As(err, &fe) {
		return false
	}
	return (fe.Kind == "rate_limited" && fe.Status == 0) || fe.Source == "request_interval"
}

// transientKind 是“这条线路没答上来”的失败：网络、超时、接口 5xx、被取消。这类失败不说明帖子本身怎样。
func transientKind(err error) bool {
	var fe *FetchError
	if !errors.As(err, &fe) {
		return true
	}
	return fe.Kind == "network" || fe.Kind == "timeout" || fe.Kind == "canceled" || fe.Status >= 500
}

// fetchX 取一条推文：前两名同时取，先到的成功结果为准；都失败再按顺序试后面的。
// 全失败时报“最说明问题”的那条：排名靠前、且不是网络/超时/5xx 这类没答上来的失败；都没答上来就报最后一条；备用路线回的空内容（弱证据）排在线路故障之后。
// Trail 按完成顺序写每条线路的结果和耗时。
func (f *Fetcher) fetchX(ctx context.Context, l Link) (Result, error) {
	routes := f.xRoutes()
	n := min(2, len(routes))
	var attempts []RouteAttempt
	failed := map[string]error{}
	note := func(o routeOutcome) {
		kind := outcomeKind(o.err)
		attempts = append(attempts, RouteAttempt{Route: o.route, Kind: kind, Took: o.took})
		if kind != "canceled" && !localFailure(o.err) {
			f.xStats.record(o.route, o.err == nil)
		}
		if o.err != nil {
			failed[o.route] = o.err
		}
	}
	race, cancel := context.WithCancel(ctx)
	ch := make(chan routeOutcome, n)
	for _, rt := range routes[:n] {
		go func() {
			began := time.Now()
			// 先放一个失败结果再取：取数中途 Goexit 也照样送回一条，fetchX 不会干等
			o := routeOutcome{route: rt.name, err: failure("aborted", rt.name)}
			defer func() { o.took = time.Since(began); ch <- o }()
			o.r, o.err = rt.fetch(race, l)
		}()
	}
	var won *routeOutcome
	for range n {
		o := <-ch
		if o.err == nil && won == nil {
			won = &o
			cancel() // 另一条取消；它回来的结果记成 canceled，不算它失败
			note(o)
			continue
		}
		if won != nil && o.err != nil && race.Err() != nil && ctx.Err() == nil {
			o.err = failure("canceled", o.route)
		}
		note(o)
	}
	cancel()
	if won == nil {
		for _, rt := range routes[n:] {
			began := time.Now()
			r, err := rt.fetch(ctx, l)
			o := routeOutcome{route: rt.name, r: r, err: err, took: time.Since(began)}
			note(o)
			if err == nil {
				won = &o
				break
			}
		}
	}
	if won != nil {
		won.r.Attempts = attempts
		return won.r, nil
	}
	// 备用路线回空内容只是弱证据：前两条都没答上来时，不拿它报“帖子不存在”，报线路故障
	var pick error
	for _, rt := range routes {
		if err, ok := failed[rt.name]; ok && !transientKind(err) && !weakNotFound(err) {
			pick = err
			break
		}
	}
	for i := len(attempts) - 1; pick == nil && i >= 0; i-- {
		if err := failed[attempts[i].Route]; !weakNotFound(err) {
			pick = err
		}
	}
	if pick == nil {
		pick = failed[attempts[len(attempts)-1].Route]
	}
	trail := ""
	for _, a := range attempts {
		trail = routeTrail(trail, a.Route, a.Kind, a.Took)
	}
	var fe *FetchError
	if errors.As(pick, &fe) {
		cp := *fe
		cp.Trail, cp.Attempts = trail, attempts
		return Result{}, &cp
	}
	return Result{}, &FetchError{Kind: outcomeKind(pick), Source: attempts[len(attempts)-1].Route, Trail: trail, Attempts: attempts}
}
