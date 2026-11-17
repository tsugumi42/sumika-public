package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sumika-native/internal/advisor"
	"sumika-native/internal/api"
	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
	"sumika-native/internal/turnbudget"
	"time"
)

// ReviewHost 由 K1 在 app 装配时提供，不扩大冻结的 turn.Config/内核端口。
type ReviewHost struct {
	Current func(context.Context, kernel.ReviewRequest) error
	Accept  func(context.Context, kernel.Invocation, string, []kernel.Finding) (bool, error)
	Models  func(string) ([]string, []llm.Model)
}

// ConfigureKernelReviews 复用 K1/K2/K3 的端口；不开第二个寿命或模型账本。
// 缺前置时显式报错，不能以裸 llm.Client 或全权 Scope 冒充已接通。
func ConfigureKernelReviews(c config.ReviewConfig, models []llm.Model, tc *turn.Config, st *store.Store, host ReviewHost) error {
	if err := c.Check(); err != nil {
		return err
	}
	if !c.Advisor.Enabled && !c.Verify.Enabled {
		return nil
	}
	for purpose, id := range reviewRoutes(c) {
		found := false
		for _, m := range models {
			if m.ID == id {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s 审阅模型不在当前列表：%s", purpose, id)
		}
	}
	svc, err := advisor.New(c, advisor.Dependencies{Caller: tc.ModelCaller, Failover: tc.Failover, Models: host.Models, Results: tc.Results, ReadPageBytes: int64(tc.WorkSettings.ReadPageBytes), Lifetime: tc.Lifetime, Store: st, Current: host.Current, Accept: host.Accept, Logf: tc.Logf})
	if err != nil {
		return err
	}
	if c.Advisor.Enabled {
		tc.Advisor = svc
	}
	if c.Verify.Enabled {
		tc.Completion = svc
	}
	return nil
}
func reviewRoutes(c config.ReviewConfig) map[string]string {
	if !c.Advisor.Enabled && !c.Verify.Enabled {
		return nil
	}
	routes := make(map[string]string, 2)
	if c.Advisor.Enabled {
		routes["advisor"] = c.Advisor.Model
	}
	if c.Verify.Enabled {
		routes["verify"] = c.Verify.Model
	}
	return routes
}
func (a *App) configureReviews(tc *turn.Config) error {
	return ConfigureKernelReviews(a.cfg.Agent.ReviewConfig, a.currentModels().Models, tc, a.set.TraceStore(), ReviewHost{Current: a.currentReview, Accept: a.hub.AcceptReview, Models: func(id string) ([]string, []llm.Model) {
		s := a.currentModels()
		return slices.Clone(s.Values.Fallback(id)), s.Models
	}})
}

func (a *App) currentReview(ctx context.Context, r kernel.ReviewRequest) error {
	if err := a.hub.CheckInvocation(ctx, r.Invocation); err != nil {
		return err
	}
	if _, err := a.authority.Resolve(r.Invocation.Scope); err != nil {
		return err
	}
	state, _, err := a.hub.Cfg.Work.Load(ctx, r.Invocation)
	if err != nil {
		return err
	}
	if state.Revision != r.Work.Revision || state.Goal != r.Work.Goal || state.Next != r.Work.Next || !slices.Equal(state.Constraints, r.Work.Constraints) || !slices.Equal(state.Pending, r.Work.Pending) || !slices.Equal(state.Unknown, r.Work.Unknown) || !slices.Equal(state.Evidence, r.Evidence) || len(r.Work.Completed) < len(state.Completed) || !slices.Equal(state.Completed, r.Work.Completed[:len(state.Completed)]) {
		return store.ErrWorkConflict
	}
	snapshot, err := a.hub.Cfg.Budget.Snapshot(ctx, r.Invocation.RootID)
	if err != nil {
		return err
	}
	if !time.Now().Before(snapshot.Limits.Deadline) {
		return turnbudget.ErrExhausted
	}
	return nil
}

// RunWork 供本地可信宿主显式提交验收；普通聊天仍走 LocalChat，不自动触发审阅。
func (a *App) RunWork(ctx context.Context, key api.ConversationKey, goal string, criteria []string) turn.WorkOutcome {
	d := a.Desktop()
	_, done, err := d.enterChat()
	if err != nil {
		return turn.WorkOutcome{Outcome: turn.Outcome{Err: err}}
	}
	defer done()
	if strings.TrimSpace(goal) == "" {
		return turn.WorkOutcome{Outcome: turn.Outcome{Err: fmt.Errorf("工作目标不能为空")}}
	}
	scope, err := d.Reader.localInputScope(ctx, key)
	if err != nil {
		return turn.WorkOutcome{Outcome: turn.Outcome{Err: err}}
	}
	in := turn.Input{Identity: kernel.Invocation{Scope: scope, RootID: store.NewID(time.Now()), Kind: kernel.KindForeground}, CharacterID: scope.CharacterID, Conversation: scope.Conversation, Scope: scope.ScopeID, Text: goal, Audience: audience.Console(scope.Conversation), Addressed: true}
	r := kernel.ReviewRequest{Invocation: in.Identity, CheckpointID: "work:" + in.Identity.RootID, Criteria: criteria}
	return a.hub.RespondWork(ctx, in, r, a.currentReview)
}
