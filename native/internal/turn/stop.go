package turn

import (
	"context"
	"sync"
	"time"
)

type stopContextKey struct{}

// stagedStop owns the execution signal until real drain. The soft context still
// cancels synchronously with its parent; only already dispatched tools use hard.
type stagedStop struct {
	mu       sync.Mutex
	soft     context.Context
	cancel   context.CancelFunc
	hard     context.Context
	force    context.CancelFunc
	grace    time.Duration
	logf     func(string, ...any)
	escalate func()
	watch    func() bool
	timer    *time.Timer
	started  time.Time
	forced   time.Time
	finished bool
}

func newStagedStop(parent context.Context, grace time.Duration, logf func(string, ...any), escalate func()) (context.Context, *stagedStop) {
	soft, cancel := context.WithCancel(parent)
	hard, force := context.WithCancel(context.WithoutCancel(parent))
	s := &stagedStop{soft: soft, cancel: cancel, hard: hard, force: force, grace: grace, logf: logf, escalate: escalate}
	s.watch = context.AfterFunc(soft, s.begin)
	return context.WithValue(soft, stopContextKey{}, s), s
}

func (s *stagedStop) log(stage int, elapsed time.Duration, result string) {
	if s.logf != nil {
		s.logf("停止阶段 stage=%d elapsed_ms=%d result=%s", stage, elapsed.Milliseconds(), result)
	}
}

func (s *stagedStop) stop() { s.cancel(); s.begin() }
func (s *stagedStop) begin() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || !s.started.IsZero() {
		return
	}
	s.started = time.Now()
	s.log(1, 0, "sealed")
	s.timer = time.AfterFunc(s.grace, s.immediate)
}
func (s *stagedStop) immediate() {
	s.mu.Lock()
	if s.finished || !s.forced.IsZero() {
		s.mu.Unlock()
		return
	}
	s.forced = time.Now()
	if s.started.IsZero() {
		s.started = s.forced
	}
	elapsed := s.forced.Sub(s.started)
	escalate := s.escalate
	s.mu.Unlock()
	s.log(2, elapsed, "expired")
	s.force()
	if escalate != nil {
		escalate()
	}
	s.log(3, 0, "cancelled")
}
func (s *stagedStop) finish() {
	s.watch()
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	s.finished = true
	if s.timer != nil {
		s.timer.Stop()
	}
	log := !s.started.IsZero()
	stage, elapsed, result := 2, time.Duration(0), "drained"
	if log && !s.forced.IsZero() {
		stage, elapsed = 3, time.Since(s.forced)
	} else if log {
		elapsed = time.Since(s.started)
	}
	s.mu.Unlock()
	if log {
		s.log(stage, elapsed, result)
	}
	s.cancel()
	s.force()
}

// stopsAtOnce：命令是主人喊停时最该马上停的（可能在删东西、可能跑很久），杀进程树也是它定义好的结束方式。
// 宽限留给写文件这类很快结束、打断反而留半截的工具。ask_owner 只是在等主人，没有半截可留，卡片要马上收回。
func stopsAtOnce(tool string) bool { return tool == "powershell" || tool == "ask_owner" }

func stopOf(ctx context.Context) *stagedStop {
	s, _ := ctx.Value(stopContextKey{}).(*stagedStop)
	return s
}

// executionContext preserves invocation values and budget deadlines. Cancellation
// after dispatch follows the stop escalation; preparation still uses the soft ctx.
func executionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	s := stopOf(ctx)
	if s == nil {
		return context.WithCancel(ctx)
	}
	execution, cancel := context.WithCancel(context.WithoutCancel(ctx))
	unwatch := context.AfterFunc(s.hard, cancel)
	var deadlineCancel context.CancelFunc
	if deadline, ok := ctx.Deadline(); ok {
		execution, deadlineCancel = context.WithDeadline(execution, deadline)
	}
	return execution, func() {
		unwatch()
		cancel()
		if deadlineCancel != nil {
			deadlineCancel()
		}
	}
}
