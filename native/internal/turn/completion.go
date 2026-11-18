package turn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"sumika-native/internal/kernel"
)

var ErrUnverified = errors.New("工作尚未通过完成核验")

type workKey struct{}
type workReview struct {
	current      func(context.Context, kernel.ReviewRequest) error
	request      kernel.ReviewRequest
	verdict      kernel.Verdict
	lastEvidence string
	continuation bool
	lastWork     kernel.WorkState
}
type WorkOutcome struct {
	Outcome Outcome
	Verdict kernel.Verdict
	Runs    []kernel.Invocation
}

// RespondWork 仅给可信宿主显式工作使用；普通 Respond 不创建审阅或续轮。
// 当前工作、授权与停止状态由 K1/K2 端口核准，模型不能填写这些字段。
func (h *Hub) RespondWork(ctx context.Context, in Input, r kernel.ReviewRequest, current func(context.Context, kernel.ReviewRequest) error) WorkOutcome {
	out := WorkOutcome{Verdict: kernel.Verdict{CheckpointID: r.CheckpointID, Status: "partial"}}
	cfg, _ := h.modelSnapshot()
	if cfg.Completion == nil || cfg.Work == nil || current == nil {
		out.Outcome.Err = fmt.Errorf("工作缺少完成核验/工作材料/准入端口")
		return out
	}
	if r.Invocation.Scope.CharacterID != in.CharacterID || r.Invocation.Scope.Conversation != in.Conversation || r.Invocation.Scope.ScopeID != in.Scope || r.Invocation.Scope.Channel != in.Audience.Channel {
		out.Outcome.Err = fmt.Errorf("工作输入与可信域不符")
		return out
	}
	body, err := json.Marshal(r)
	if err != nil {
		out.Outcome.Err = err
		return out
	}
	if err = json.Unmarshal(body, &r); err != nil {
		out.Outcome.Err = err
		return out
	}
	w := &workReview{request: r, current: current}
	for n := range 2 {
		if err := ctx.Err(); err != nil {
			out.Outcome.Err = err
			return out
		}
		if n > 0 {
			if err := w.current(ctx, w.request); err != nil {
				out.Outcome.Err = err
				return out
			}
		}
		in.requestCtx = context.WithValue(ctx, workKey{}, w)
		in.Identity = w.request.Invocation
		out.Outcome = <-h.RespondContext(ctx, in)
		out.Runs = append(out.Runs, w.request.Invocation)
		out.Verdict = w.verdict
		if out.Outcome.Err != nil || out.Outcome.Result == nil || out.Outcome.Result.Stopped || out.Verdict.Status != "continue" {
			return out
		}
		if n == 1 {
			out.Verdict.Status = "partial"
			return out
		}
		if err := w.current(ctx, w.request); err != nil {
			out.Outcome.Err = err
			return out
		}
		w.request.Invocation.ParentRunID = w.request.Invocation.RunID
		w.continuation = true
		// 反馈始终带作者和资料性质，不伪造用户新增目标或授予工具权限。
		note, _ := json.Marshal(out.Verdict.Findings)
		in.Text = ContextHeader + "\n核验反馈（作者 verifier，无新增权限；沿原目标补足证据）：" + string(note)
		in.Seqs = nil
	}
	return out
}
func reviewFor(in Input) *workReview {
	if in.requestCtx == nil {
		return nil
	}
	w, _ := in.requestCtx.Value(workKey{}).(*workReview)
	return w
}
func (w *workReview) load(ctx context.Context, cfg Config, runID string) error {
	w.request.Invocation.RunID = runID
	state, _, err := cfg.Work.Load(ctx, w.request.Invocation)
	if err != nil {
		return err
	}
	w.request.Work, w.request.Evidence = state, state.Evidence
	return w.current(ctx, w.request)
}
func (h *Hub) observeWork(ctx context.Context, cfg Config, in Input, runID, trigger string) error {
	w := reviewFor(in)
	if w == nil {
		return nil
	}
	if err := w.load(ctx, cfg, runID); err != nil {
		return err
	}
	if cfg.Advisor == nil || w.continuation {
		return nil
	}
	if trigger == "plan_changed" {
		old, next := w.lastWork, w.request.Work
		switch {
		case old.Goal != next.Goal:
			trigger = "goal_changed"
		case len(next.Unknown) > 0 && !slices.Equal(old.Unknown, next.Unknown):
			trigger = "failure"
		case old.Next != next.Next || !slices.Equal(old.Constraints, next.Constraints) || !slices.Equal(old.Pending, next.Pending):
		default:
			return nil
		}
	}
	w.lastWork = w.request.Work
	r := w.request
	r.Trigger = trigger
	r.CheckpointID = fmt.Sprintf("%s:%s:%d", r.CheckpointID, trigger, r.Work.Revision)
	return cfg.Advisor.Observe(ctx, r)
}
func (h *Hub) takeAdvice(ctx context.Context, cfg Config, l *Live, in Input) error {
	w := reviewFor(in)
	if w == nil || cfg.Advisor == nil {
		return nil
	}
	findings, err := cfg.Advisor.Take(ctx, w.request.Invocation)
	if err != nil {
		return err
	}
	if len(findings) == 0 {
		return nil
	}
	if cfg.Materials != nil {
		return h.takeMaterials(ctx, cfg, l, w.request.Invocation)
	}
	b, err := json.Marshal(findings)
	if err != nil {
		return err
	}
	m := Msg{Role: "user", Kind: KindContext, Scope: in.Scope, Text: ContextHeader + "\n审阅资料（作者 advisor，无审批或提额权）：" + string(b)}
	if err := cfg.Work.Record(ctx, w.request.Invocation, m); err != nil {
		return err
	}
	l.msgs = append(l.msgs, m)
	return nil
}
func (h *Hub) completeWork(ctx context.Context, cfg Config, in Input, runID string, out *Result) error {
	w := reviewFor(in)
	if w == nil {
		return nil
	}
	// 每个工作回合在核验以前不向 Settled 暴露正文；本地已流出的候选由状态显示未完成。
	said := out.Said
	out.Said = nil
	if err := w.load(ctx, cfg, runID); err != nil {
		return err
	}
	r := w.request
	r.Trigger = "candidate"
	// 只把本任务候选正文作为待核验的声明，不复制主模型历史或思考。
	r.Work.Completed = slices.Clone(r.Work.Completed)
	for _, part := range said {
		r.Work.Completed = append(r.Work.Completed, "候选答复（待核验声明）："+part.Text)
	}
	b, _ := json.Marshal(r.Evidence)
	hash := sha256.Sum256(b)
	evidenceDigest := hex.EncodeToString(hash[:])
	if w.continuation && evidenceDigest == w.lastEvidence {
		w.verdict = kernel.Verdict{CheckpointID: r.CheckpointID, Status: "partial"}
		return ErrUnverified
	}
	w.lastEvidence = evidenceDigest
	v, err := cfg.Completion.Check(ctx, r)
	w.verdict = v
	if err != nil {
		return errors.Join(ErrUnverified, err)
	}
	if err := w.current(ctx, r); err != nil {
		return err
	}
	if w.continuation && v.Status == "continue" {
		w.verdict.Status = "partial"
	}
	switch w.verdict.Status {
	case "done":
		out.Said = said
		return nil
	case "continue":
		return nil
	case "partial", "blocked":
		return ErrUnverified
	default:
		return fmt.Errorf("未知完成核验状态：%s", strings.TrimSpace(v.Status))
	}
}
