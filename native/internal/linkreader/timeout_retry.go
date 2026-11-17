package linkreader

// 链接取数全部线路都超时：隔 5 秒整条再试一次（规格-链接发送改版 C3，2026-11-16）。
// 只要有一条线路是明确失败（403、要登录、找不到、HTTP 错、解析错）就不重试：不为风控或权限重试。

import (
	"context"
	"errors"
	"slices"
	"time"
)

const (
	timeoutRetryDelay = 5 * time.Second
	retryMinRound     = 8 * time.Second  // 外层至少还要留出这么久跑第二轮
	retryMaxRound     = 15 * time.Second // 第二轮最多跑这么久
)

// retryTiming 是超时重试的时限：生产值等待 5 秒、第二轮最短 8 秒、封顶 15 秒；只供测试按比例缩小。
type retryTiming struct {
	delay    time.Duration
	minRound time.Duration
	maxRound time.Duration
}

// retryTrailMarker 写在两轮 Trail 中间，日志能看出试了两轮。
const retryTrailMarker = "5 秒后重试"

// allTimedOut 说这一轮是不是每条线路都超时了：X 看逐条线路的记录，单线路站点只有一条线路、看失败的 kind。
func allTimedOut(err error) bool {
	var fe *FetchError
	if !errors.As(err, &fe) {
		return false
	}
	if len(fe.Attempts) == 0 {
		return fe.Kind == "timeout"
	}
	for _, a := range fe.Attempts {
		if a.Kind != "timeout" {
			return false
		}
	}
	return true
}

// retryInBudget 看调用方的时限还剩多少：不到等待 5 秒 + 第二轮 8 秒就不试；没有截止时间（直接调 Fetch 的入口）就试。
// 不看第一轮实际耗时：X 的三条线路依次超时就用掉 20 秒左右，按“够再跑一整轮”估永远不重试（2026-11-16 核对）。
func (f *Fetcher) retryInBudget(ctx context.Context) bool {
	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return time.Until(deadline) >= f.retry.delay+f.retry.minRound
}

// retryContext 给第二轮开时限：按调用方剩余时间（等完 5 秒之后）封顶 15 秒；没有截止时间按 15 秒。
func (f *Fetcher) retryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	limit := f.retry.maxRound
	if deadline, ok := ctx.Deadline(); ok {
		limit = min(time.Until(deadline), limit)
	}
	return context.WithTimeout(ctx, limit)
}

func (f *Fetcher) waitRetry(ctx context.Context) error {
	if f.retrySleep != nil {
		return f.retrySleep(ctx, f.retry.delay)
	}
	timer := time.NewTimer(f.retry.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// dropIdleConnections 丢掉客户端池里的空闲连接。全超时多半是代理换了节点或隧道断了：池里的连接已经死了，
// 但 Transport 还没发现（HTTP/2 在响应头超时后只重置流、不断连接，新请求还会被塞进这条死连接），
// 第二轮若复用它们就得再等满一次 ResponseHeaderTimeout；先丢干净，第二轮每条线路都从新拨的连接开始。
// 媒体客户端和主客户端共用同一个 Transport，关一次就都覆盖到。
func (f *Fetcher) dropIdleConnections() {
	f.client.CloseIdleConnections()
	if f.biliClient != f.client {
		f.biliClient.CloseIdleConnections()
	}
}

// retryAfterTimeout 整条再跑一轮（只再一次）。fetch 已经等过 5 秒，ctx 是第二轮的时限。
// 第二轮的线路记录接在第一轮后面，失败时 Trail 中间写“5 秒后重试”；成功、失败都标 RetriedAfterTimeout，插件日志据此写 note。
func (f *Fetcher) retryAfterTimeout(ctx context.Context, l Link, metadata bool, first error) (Result, error) {
	var ffe *FetchError
	if !errors.As(first, &ffe) {
		return Result{}, first
	}
	r, err := f.fetchRound(ctx, l, metadata)
	if err == nil {
		r.Attempts = append(roundAttempts(ffe.Attempts, 1), roundAttempts(r.Attempts, 2)...)
		r.RetriedAfterTimeout = true
		return r, nil
	}
	var fe *FetchError
	if !errors.As(err, &fe) {
		return Result{}, err
	}
	cp := *fe
	cp.Trail = retryTrail(ffe.Trail, cp.Trail)
	cp.Attempts = append(roundAttempts(ffe.Attempts, 1), roundAttempts(cp.Attempts, 2)...)
	cp.RetriedAfterTimeout = true
	return Result{}, &cp
}

// roundAttempts 标出这些线路记录属于第几轮；不改原来的切片。
func roundAttempts(in []RouteAttempt, round int) []RouteAttempt {
	out := slices.Clone(in)
	for i := range out {
		out[i].Round = round
	}
	return out
}

func retryTrail(first, second string) string {
	switch {
	case first == "" && second == "":
		return retryTrailMarker
	case first == "":
		return retryTrailMarker + " → " + second
	case second == "":
		return first + " → " + retryTrailMarker
	}
	return first + " → " + retryTrailMarker + " → " + second
}
