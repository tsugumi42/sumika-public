package turn

// 模型请求出错以后的重试（规格-Go后台 8.2 A7；Bun 不重试，规格第七节 A40）。照 OpenCode 的做法（每一步一份次数、退避带 ±20% 抖动、
// 服务商要求等的只拉长不缩短，model-retry.ts:22），数取 pi（3 次、2/4/8 秒）和 OpenCode（10 次、约 84 秒）之间：聊天里
// 等太久不如报错。哪些错误值得重试看 llm.Retryable；已经推给界面的字不重来（不重复），由调用方判断

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"sumika-native/internal/llm"
)

// RetryPolicy：零值的字段用默认（我定的，2026-10-26）
type RetryPolicy struct {
	Max      int                                  // 最多再请求几次（不算第一次）；默认 4
	Base     time.Duration                        // 第一次重试前等多久，之后每次翻倍；默认 1 秒
	Cap      time.Duration                        // 每次最多等多久（不算服务商要求的）；默认 8 秒
	MaxAfter time.Duration                        // 服务商要求等的（Retry-After）超过它就不等、直接报错；默认 60 秒
	Rand     func() float64                       // [0, 1) 的随机数，抖动用；nil 用 rand.Float64（测试里固定）
	Wait     func(context.Context, time.Duration) // nil 使用可取消定时器；测试注入事件同步
}

func (p RetryPolicy) orDefault() RetryPolicy {
	if p.Max == 0 {
		p.Max = 4
	}
	if p.Base == 0 {
		p.Base = time.Second
	}
	if p.Cap == 0 {
		p.Cap = 8 * time.Second
	}
	if p.MaxAfter == 0 {
		p.MaxAfter = time.Minute
	}
	if p.Rand == nil {
		p.Rand = rand.Float64
	}
	return p
}

// errorText 是模型请求出错记进 model_runs.error 的那句：结束原因的错误照 pi 的写法（PiMessage），别的原样
func errorText(err error) string {
	var se *llm.StopError
	if errors.As(err, &se) {
		return se.PiMessage()
	}
	return err.Error()
}

// wait：第 attempt 次请求（从 0 数）出错以后要不要再请求、先等多久。不值得重试的、次数用完的、服务商要等太久的都不再请求
func (p RetryPolicy) wait(err error, attempt int) (time.Duration, bool) {
	p = p.orDefault()
	ok, after := llm.Retryable(err)
	if !ok || attempt >= p.Max || after > p.MaxAfter {
		return 0, false
	}
	d := p.Cap
	if attempt < 30 {
		d = min(p.Base<<attempt, p.Cap)
	}
	d = time.Duration(float64(d) * (0.8 + 0.4*p.Rand()))
	return max(d, after), true
}

func (p RetryPolicy) pause(ctx context.Context, delay time.Duration) {
	if p.Wait != nil {
		p.Wait(ctx, delay)
		return
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
