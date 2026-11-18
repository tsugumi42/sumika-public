package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Failover keeps cooldowns for one model source. Its zero value is ready to use.
type Failover struct {
	Now      func() time.Time
	Cooldown time.Duration
	mu       sync.Mutex
	until    map[string]time.Time
}

type failoverContextKey struct{}

// WithFailover 把故障切换的 trace 元数据带进中立请求（chat.Request 不放传输层的 trace 字段）。
func WithFailover(ctx context.Context, info *FailoverInfo) context.Context {
	if info == nil {
		return ctx
	}
	return context.WithValue(ctx, failoverContextKey{}, info)
}

// FailoverFrom 取出 WithFailover 带的切换记录；没有是 nil。
func FailoverFrom(ctx context.Context) *FailoverInfo {
	info, _ := ctx.Value(failoverContextKey{}).(*FailoverInfo)
	return info
}

// FailoverInfo is trace-only metadata; it is never sent to the model.
type FailoverInfo struct {
	OriginalModel string   `json:"originalModel"`
	PreviousModel string   `json:"previousModel"`
	Reason        string   `json:"reason"`
	SkippedModels []string `json:"skippedModels,omitempty"`
}

// FailoverPlan selects each distinct candidate at most once per request.
type FailoverPlan struct {
	Model    string
	Info     *FailoverInfo
	owner    *Failover
	models   []string
	index    int
	selected []string
	skipped  []string
	enabled  bool
}

func (f *Failover) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Failover) cooling(model string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	until, ok := f.until[model]
	if ok && !f.now().Before(until) {
		delete(f.until, model)
		return false
	}
	return ok
}

func (f *Failover) cool(model string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.until == nil {
		f.until = make(map[string]time.Time)
	}
	d := f.Cooldown
	if d <= 0 {
		d = time.Minute
	}
	f.until[model] = f.now().Add(d)
}

// Plan respects cooldown even for the primary when a backup chain is enabled.
func (f *Failover) Plan(primary string, backups []string) *FailoverPlan {
	p := &FailoverPlan{owner: f, enabled: len(backups) > 0}
	seen := make(map[string]bool)
	for _, model := range append([]string{primary}, backups...) {
		if model != "" && !seen[model] {
			seen[model] = true
			p.models = append(p.models, model)
		}
	}
	if !p.enabled {
		p.Model = primary
		return p
	}
	p.selectNext("", "")
	return p
}

func (p *FailoverPlan) selectNext(previous, reason string) bool {
	for p.index < len(p.models) {
		model := p.models[p.index]
		p.index++
		if p.owner.cooling(model) {
			p.skipped = append(p.skipped, model)
			continue
		}
		p.Model = model
		p.selected = append(p.selected, model)
		if model != p.models[0] || previous != "" || len(p.skipped) > 0 {
			p.Info = &FailoverInfo{OriginalModel: p.models[0], PreviousModel: previous, Reason: reason, SkippedModels: append([]string(nil), p.skipped...)}
		} else {
			p.Info = nil
		}
		return true
	}
	p.Model = ""
	return false
}

// Next is called after same-model retries have been exhausted.
func (p *FailoverPlan) Next(ctx context.Context, err error) bool {
	if !p.enabled || p.Model == "" || !FailoverEligible(ctx, err) {
		return false
	}
	previous := p.Model
	p.owner.cool(previous)
	return p.selectNext(previous, ClassifyError(err))
}

// Err 在链启用且没有可继续的候选时，用中文说明试过谁、冷却跳过谁，并保留底层错误。
func (p *FailoverPlan) Err(err error) error {
	if p == nil || !p.enabled {
		return err
	}
	if err == nil && p.Model != "" {
		return nil
	}
	tried := strings.Join(p.selected, "、")
	if tried == "" {
		tried = "无"
	}
	cooled := strings.Join(p.skipped, "、")
	if cooled == "" {
		cooled = "无"
	}
	message := fmt.Sprintf("模型故障切换：试过 %s；冷却跳过 %s", tried, cooled)
	if err != nil {
		return fmt.Errorf("%s：%w", message, err)
	}
	return errors.New(message + "：都在冷却，没有可试的")
}

// Exhausted 只在已经换过、链上没人可试、或候选都失败时包装错误。
// 请求本身的错、用户停止、还停在原模型上的失败保持原错误，避免空链以外的普通失败被说成切换。
func (p *FailoverPlan) Exhausted(err error) error {
	if p == nil || !p.enabled {
		return err
	}
	if err != nil && (p.Model != "" && p.Info == nil || !FailoverEligible(context.Background(), err)) {
		return err
	}
	return p.Err(err)
}

type localError struct{ err error }

func (e *localError) Error() string { return e.err.Error() }
func (e *localError) Unwrap() error { return e.err }

// LocalError marks failures in local request preparation, accounting or recording.
func LocalError(err error) error {
	if err == nil {
		return nil
	}
	return &localError{err}
}

// FailoverEligible only accepts known model or transport failures.
func FailoverEligible(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var local *localError
	if errors.As(err, &local) {
		return false
	}
	var stop *StopError
	if errors.As(err, &stop) && stop.Reason != "" {
		return false
	}
	var stream *StreamError
	if errors.As(err, &stream) && ClassifyError(err) == ClassStreamError {
		text := strings.ToLower(string(stream.Data))
		if !strings.Contains(text, "api_error") && !strings.Contains(text, "upstream_stream_interrupted") {
			return false
		}
	}
	switch ClassifyError(err) {
	case ClassNetwork, ClassTimeout, ClassStreamError, ClassRateLimited, ClassServerError, ClassModelUnknown, ClassAuth:
		return true
	}
	return false
}
