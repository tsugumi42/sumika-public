package turn

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/modelcall"
	"sumika-native/internal/turnbudget"
)

type RootBudget interface {
	kernel.Budget
	OpenRoot(context.Context, string, kernel.Scope, kernel.Limits) error
	Stop(context.Context, string) error
}

// initializeBudget 不开第二套客户端；模型热发布只替换路由字段，保留此宿主端口。
func (h *Hub) initializeBudget() {
	if h.Cfg.BudgetConfig.MaxRequests == 0 {
		h.Cfg.BudgetConfig = config.DefaultBudget()
	}
	if h.Cfg.Scheduler == nil {
		h.Cfg.Scheduler = NewToolScheduler(4, 8)
	}
	if h.trace() == nil {
		return
	}
	if h.Cfg.Budget == nil {
		h.Cfg.Budget = &turnbudget.Ledger{Store: h.trace()}
	}
	if h.Cfg.ModelCaller == nil {
		caller := modelcall.New(h.Client, h.Cfg.Budget, h.trace(), 4, h.Cfg.BudgetConfig.RequestOutputTokens)
		if h.Cfg.Now != nil {
			caller.Now = h.Cfg.Now
			if caller.Failover != nil {
				caller.Failover.Now = h.Cfg.Now
			}
		}
		h.Cfg.ModelCaller = caller
	}
	if h.Cfg.Failover == nil {
		if caller, ok := h.Cfg.ModelCaller.(*modelcall.Client); ok && caller.Failover != nil {
			h.Cfg.Failover = caller.Failover
		} else {
			h.Cfg.Failover = &llm.Failover{}
		}
	}
	if h.Cfg.Failover != nil && h.Cfg.Failover.Now == nil {
		h.Cfg.Failover.Now = h.Cfg.Now
	}
}

// deadlineRestarter 由持久账本实现：本地输入真正开始运行时，把时限从这一刻起算。
type deadlineRestarter interface {
	RestartDeadline(ctx context.Context, root string, deadline time.Time) error
}

// AdmitBudgetRoot 供 K1 在持久受理后、排队之前调用；重复调用只读取既有余额，不延长时限。
func AdmitBudgetRoot(ctx context.Context, budget kernel.Budget, in kernel.Invocation, limits kernel.Limits) error {
	_, err := budget.Snapshot(ctx, in.RootID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	root, ok := budget.(RootBudget)
	if !ok {
		return errors.New("budget root must be admitted by host")
	}
	return root.OpenRoot(ctx, in.RootID, in.Scope, limits)
}

func (h *Hub) beginBudget(ctx context.Context, cfg Config, in Input, runID string) (context.Context, kernel.Invocation, context.CancelFunc, error) {
	identity := in.Identity
	if carried := kernel.InvocationFromContext(ctx); carried.RootID != "" {
		identity = carried
	}
	if identity.RootID == "" {
		identity = kernel.Invocation{RootID: runID, RunID: runID, Kind: "foreground", Scope: kernel.Scope{CharacterID: in.CharacterID, Conversation: in.Conversation, ScopeID: in.Scope, Channel: in.Audience.Channel, Scene: in.Audience.Scene}}
	}
	if err := AdmitBudgetRoot(ctx, cfg.Budget, identity, cfg.BudgetConfig.LimitsForInput(identity.Scope, in.Auto, time.Now())); err != nil {
		return ctx, identity, func() {}, err
	}
	snap, err := cfg.Budget.Snapshot(ctx, identity.RootID)
	if err != nil {
		return ctx, identity, func() {}, err
	}
	tracked, untrack := turnbudget.TrackRoot(ctx, cfg.Budget, identity.RootID)
	run, cancel := context.WithDeadline(kernel.WithInvocation(tracked, identity), snap.Limits.Deadline)
	return run, identity, func() { cancel(); untrack() }, nil
}

func modelStream(ctx context.Context, cfg Config, b *Built, req chat.Request, onEvent func(llm.Event)) (llm.Result, error) {
	b.Invocation.AttemptID = "attempt:" + storeID(cfg)
	if cfg.Lifetime != nil {
		ctx = modelcall.WithLifetime(ctx, cfg.Lifetime)
	}
	if cfg.internal != nil {
		return cfg.internal.stream(ctx, cfg, b.Invocation, req, onEvent)
	}
	return cfg.ModelCaller.StreamChat(ctx, b.Invocation, req, onEvent)
}
