package turn

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
)

// InputPort 是所有宿主受理、停止和排空的共同入口。
type InputPort interface {
	Submit(context.Context, Submission) (kernel.Receipt, error)
	StopRun(context.Context, kernel.StopRequest) (kernel.StopReceipt, error)
	Seal()
	Drain(context.Context) error
}
type InternalRunPort interface {
	StartInternal(context.Context, InternalRun) (RunHandle, error)
}

type RunSnapshot struct {
	Identity kernel.Invocation
	Revision uint64
	State    kernel.RunState
	Outcome  kernel.Outcome
	Text     string
	// Process 是这一轮的思考和工具进度；每次变化换一份新的，nil 表示还没有
	Process *ProcessLog
	Step    int
	// SealedStep 是已经存库的步数（步号从 0 起）：比它小的步，草稿可以让真消息接替了。
	SealedStep int
	Drained    bool
	// Reason 是结局的具体原因（出错、预算到顶、被限流……），正常结束为空；界面照它写日常说法。
	Reason RunReason
	// Requeued 是收尾时才到的本地插话：内核把它们改排成了新的几轮。界面要跟上这些轮，否则显示空闲却停不了、再发被拒。
	// 用指针是为了 RunSnapshot 仍可直接比较（Process 同理）；nil 表示没有。
	Requeued *RequeuedInputs
}

// RequeuedInputs 是收尾时被改排成新一轮的那批插话。
type RequeuedInputs struct{ Inputs []RequeuedInput }

// RequeuedInput 是一句被改排成新一轮的本地插话。
type RequeuedInput struct {
	InputID, RunID, Text string
	Seq                  uint64
}

// 完成运行仅保留最近 128 条（前台和子运行共用），给短期重试和终态订阅留窗口，
// 不让历史输入、快照、结果通道随常驻进程轮数增长；活动运行不占这 128 个名额。
// 淘汰后 Submit 仍从 inbox 返回原回执；结果通道、StopRun、WatchRun、插话目标和
// Invocation 核验沿用内存未命中的行为。已持有的订阅/通道不受删除索引影响。
const completedRunLimit = 128

// retainCompletedRunLocked 只在执行和收尾完成后调用，调用方持有 lc.mu。
func (lc *hubLifecycle) retainCompletedRunLocked(r *inputRun) {
	if old := lc.completed[lc.nextCompleted]; old != "" {
		delete(lc.runs, old)
	}
	lc.completed[lc.nextCompleted] = r.input.Identity.RunID
	lc.nextCompleted = (lc.nextCompleted + 1) % completedRunLimit
}

// RunSubscription 只发布容量 1 的 dirty；慢消费者每次读取累计快照，不排逐 token 事件。
type RunSubscription struct {
	hub  *Hub
	run  *inputRun
	wake chan struct{}
}

func (s *RunSubscription) Wake() <-chan struct{} { return s.wake }
func (s *RunSubscription) Snapshot() RunSnapshot {
	s.hub.lifecycle.mu.Lock()
	defer s.hub.lifecycle.mu.Unlock()
	return s.run.snapshot
}
func (s *RunSubscription) Close() {
	s.hub.lifecycle.mu.Lock()
	delete(s.run.wakes, s)
	s.hub.lifecycle.mu.Unlock()
}
func (h *Hub) subscribeRunLocked(r *inputRun) *RunSubscription {
	s := &RunSubscription{hub: h, run: r, wake: make(chan struct{}, 1)}
	r.wakes[s] = struct{}{}
	s.wake <- struct{}{}
	return s
}
func (h *Hub) WatchRun(scope kernel.Scope, run string) (*RunSubscription, error) {
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	r := lc.runs[run]
	if r == nil || r.input.Identity.Scope != scope {
		return nil, store.ErrInputConflict
	}
	return h.subscribeRunLocked(r), nil
}
func (h *Hub) wakeRunLocked(r *inputRun, force bool) {
	r.snapshot.Revision++
	now := time.Now()
	remaining := h.Cfg.Lifecycle.defaults().SnapshotInterval - now.Sub(r.lastWake)
	if !force && remaining > 0 {
		if r.wakeTimer == nil {
			// 窗口内最后一段正文也必须唤醒；模型停在下一段时不能等另一次 delta。
			var timer *time.Timer
			timer = time.AfterFunc(remaining, func() {
				h.lifecycle.mu.Lock()
				defer h.lifecycle.mu.Unlock()
				if r.wakeTimer == timer {
					r.wakeTimer = nil
					h.publishRunLocked(r)
				}
			})
			r.wakeTimer = timer
		}
		return
	}
	if r.wakeTimer != nil {
		r.wakeTimer.Stop()
		r.wakeTimer = nil
	}
	h.publishRunLocked(r)
}
func (h *Hub) publishRunLocked(r *inputRun) {
	r.lastWake = time.Now()
	for s := range r.wakes {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}
func (h *Hub) updateProcess(r *inputRun, v ProcessStep) *ProcessLog {
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if r.snapshot.Drained {
		return nil
	}
	r.snapshot.Process = r.snapshot.Process.merge(v)
	h.wakeRunLocked(r, false)
	return r.snapshot.Process
}

func thinkID(turnID string, step int) string { return "think:" + turnID + ":" + strconv.Itoa(step) }

func (h *Hub) updateDraft(r *inputRun, v LocalDraft) {
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if r.snapshot.Drained || v.TurnID != r.input.Identity.RunID {
		return
	}
	if v.Reset {
		r.snapshot.Text = ""
	} else if v.Text != "" {
		r.snapshot.Text = v.Text
	} else {
		r.snapshot.SealedStep = max(r.snapshot.SealedStep, v.Step+1)
	}
	r.snapshot.Step = v.Step
	h.wakeRunLocked(r, v.Reset)
}

// invocationLocked 只认原 Scope、Root 和取消代次；同名后继运行不能继承旧工作的回流。
func (h *Hub) invocationLocked(in kernel.Invocation) (*inputRun, error) {
	lc := h.lifecycle
	r := lc.runs[in.RunID]
	if lc.sealed.Load() || r == nil || r.input.Identity.Scope != in.Scope || r.input.Identity.RootID != in.RootID || r.input.Identity.Generation != in.Generation {
		return nil, store.ErrInputConflict
	}
	if r.snapshot.Outcome != "" && r.snapshot.Outcome != kernel.OutcomeDone || !r.snapshot.Drained && r.ctx.Err() != nil {
		return nil, store.ErrInputConflict
	}
	if active := lc.active[r.record.Domain]; active != nil && active != r && active.input.Identity.RootID != in.RootID {
		return nil, store.ErrInputConflict
	}
	return r, nil
}

func (h *Hub) CheckInvocation(ctx context.Context, in kernel.Invocation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.lifecycle.mu.Lock()
	defer h.lifecycle.mu.Unlock()
	_, err := h.invocationLocked(in)
	return err
}

// InvocationContext 返回父运行寿命，不能用一次工具调用的短上下文替代。
func (h *Hub) InvocationContext(ctx context.Context, in kernel.Invocation) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.lifecycle.mu.Lock()
	defer h.lifecycle.mu.Unlock()
	r, err := h.invocationLocked(in)
	if err != nil {
		return nil, err
	}
	if r.snapshot.Drained || r.ctx.Err() != nil {
		return nil, store.ErrInputConflict
	}
	return r.ctx, nil
}

func (h *Hub) InvocationWorkspace(ctx context.Context, in kernel.Invocation) (Workspace, error) {
	if err := h.CheckInvocation(ctx, in); err != nil {
		return Workspace{}, err
	}
	l := h.activeOf(in.Scope.CharacterID, in.Scope.Conversation)
	if l == nil {
		return Workspace{}, store.ErrInputConflict
	}
	return l.in.Workspace, nil
}

func (h *Hub) AcceptReview(ctx context.Context, in kernel.Invocation, id string, findings []kernel.Finding) (bool, error) {
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if _, err := h.invocationLocked(in); err != nil {
		return false, err
	}
	return h.trace().AcceptReview(ctx, id, func(tx *sql.Tx) error {
		for _, finding := range findings {
			raw, err := json.Marshal(finding)
			if err != nil {
				return err
			}
			if err := store.InsertKernelMaterial(ctx, tx, in, store.KernelMaterial{ID: "advisor:" + finding.ID, Kind: "advisor", Text: string(raw)}); err != nil {
				return err
			}
		}
		return nil
	})
}

// NotifyMaterials 只唤醒原域快照；不创建回合或向渠道发送。
func (h *Hub) NotifyMaterials(scope kernel.Scope) {
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for _, r := range lc.runs {
		if r.input.Identity.Scope == scope {
			h.wakeRunLocked(r, true)
		}
	}
}

var _ InputPort = (*Hub)(nil)
