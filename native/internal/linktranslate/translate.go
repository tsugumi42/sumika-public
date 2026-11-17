package linktranslate

import (
	"context"
	"errors"
	"time"
)

// Caller 调模型：system 和 user 两条消息，返回原始文字。宿主用后台模型实现，无工具。
type Caller func(ctx context.Context, system, user string) (string, error)

// 结果状态（运行页记录用）。
const (
	StatusOK       = "ok"       // 翻译成功
	StatusCached   = "cached"   // 缓存里有译文
	StatusUnneeded = "unneeded" // 不用翻：缓存里记过，或模型明确回了哨兵 / 同原文
	StatusSkipped  = "skipped"  // 本地判断不翻
	StatusTimeout  = "timeout"
	StatusFailed   = "failed"
)

// ErrNoModel 是调用方没有可用的翻译模型（没配后台模型）：算“没法翻”，不是失败，运行页不记。
var ErrNoModel = errors.New("没有配置后台模型")

// DefaultTimeout 是单次翻译调用的默认时限（和 config.DefaultTranslateTimeout 一致）。
const DefaultTimeout = 15 * time.Second

// Outcome 是一次翻译的结果。Text 非空才有译文，其余状态一律只发原文。
type Outcome struct {
	Text   string
	Status string
	Reason string // 不翻或失败的原因类别，不含用户内容
	Took   time.Duration
}

// Translator 把一段正文翻成目标语言。零值不可用。
type Translator struct {
	Call    Caller
	Cache   *Cache
	Timeout time.Duration // 单次调用的时限；0 用 DefaultTimeout
	Retries int           // 超时后再试几次，每次重新计时；外层 ctx 到期或取消不重试
	Now     func() time.Time
}

// Translate 翻译 text。cleanURL 只用来做缓存键。单次调用超时按 Retries 再试；ctx 先到期或被取消算失败，不重试。
func (t *Translator) Translate(ctx context.Context, cleanURL, text, target string) Outcome {
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	start := now()
	done := func(o Outcome) Outcome { o.Took = now().Sub(start); return o }
	if t.Call == nil {
		return done(Outcome{Status: StatusSkipped, Reason: "no_model"})
	}
	if ok, why := Needs(text, target); !ok {
		return done(Outcome{Status: StatusSkipped, Reason: why})
	}
	key := Key(cleanURL, target, text)
	if t.Cache != nil {
		if got, hit := t.Cache.Get(key); hit {
			if got == "" {
				return done(Outcome{Status: StatusUnneeded, Reason: "cached"})
			}
			return done(Outcome{Text: got, Status: StatusCached})
		}
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	raw, timedOut, err := t.call(ctx, timeout, target, text)
	if err != nil {
		if errors.Is(err, ErrNoModel) {
			return done(Outcome{Status: StatusSkipped, Reason: "no_model"})
		}
		reason := "error"
		status := StatusFailed
		if timedOut {
			status, reason = StatusTimeout, "timeout"
		} else if errors.Is(err, context.Canceled) {
			reason = "canceled"
		}
		return done(Outcome{Status: status, Reason: reason})
	}
	out, why := CleanWhy(raw, text)
	switch why {
	case WhyNone:
	case WhyUnneeded:
		if t.Cache != nil {
			t.Cache.Put(key, "") // 模型明确判过不用翻，下次不再调
		}
		return done(Outcome{Status: StatusUnneeded, Reason: why})
	default: // 回空、超长是模型出了问题：不缓存，下次重试
		return done(Outcome{Status: StatusFailed, Reason: why})
	}
	if t.Cache != nil {
		t.Cache.Put(key, out)
	}
	return done(Outcome{Text: out, Status: StatusOK})
}

// call 调一次模型，单次超时就按 Retries 重新计时再试；外层 ctx 已经结束（外层时限到了、被取消）就不再试。
// timedOut 说最后一次是不是到期：单次时限和外层时限都算，运行记录照旧记 timeout，不记成 error。
func (t *Translator) call(ctx context.Context, timeout time.Duration, target, text string) (raw string, timedOut bool, err error) {
	for attempt := 0; ; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		raw, err = t.Call(cctx, System(target), User(text, target))
		timedOut = err != nil && (errors.Is(err, context.DeadlineExceeded) || cctx.Err() == context.DeadlineExceeded)
		cancel()
		if !timedOut || ctx.Err() != nil || attempt >= t.Retries {
			return raw, timedOut, err
		}
	}
}
