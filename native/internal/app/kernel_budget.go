package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/modelcall"
	"sumika-native/internal/runmetrics"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
	"sumika-native/internal/turnbudget"
)

// WireKernelBudget 供 Open 在 NewHub 前装配；三种用途共用同一账本和模型 transport 槽。
func WireKernelBudget(tc *turn.Config, st *store.Store, transport *llm.Client, scheduler config.Scheduler, budget config.Budget) {
	ledger := &turnbudget.Ledger{Store: st}
	tc.Budget = ledger
	tc.BudgetConfig = budget
	caller := modelcall.New(transport, ledger, st, scheduler.MaxModelsGlobal, budget.RequestOutputTokens)
	if tc.Now != nil {
		caller.Now = tc.Now
	}
	tc.Failover = caller.Failover
	registerModelSlots(caller)
	tc.ModelCaller = caller
	tc.Scheduler = turn.NewToolScheduler(scheduler.MaxToolsPerRun, scheduler.MaxToolsGlobal)
}

// modelSlotWaitLimit 是模型请求槽位"正常等待"的上限：有人排队而且最久的等了比这更久，运行页标"堵"。
// 30 秒是起步值，没有实测依据；等运行页用上一阵子，看真实的排队时长再改
const modelSlotWaitLimit = 30 * time.Second

// registerModelSlots 把模型请求槽位登记到运行页的队列表（规格-运行页 第四节第 4 条）。
// 后台随接收模式重开时再调一次，新实例接替旧的
func registerModelSlots(c *modelcall.Client) {
	runmetrics.Register(runmetrics.KeyModelSlots, modelSlotWaitLimit, func() (runmetrics.Reading, error) {
		running, capacity, waiting, oldest := c.Load()
		return runmetrics.Reading{Backlog: runmetrics.I(waiting), Running: runmetrics.I(running), Capacity: runmetrics.I(capacity), OldestWait: &oldest}, nil
	})
}

func (g memoryGenerator) callIdentity(ctx context.Context, cacheKey string) (context.Context, kernel.Invocation, turn.ModelCaller, error) {
	caller, budget := g.caller, g.budget
	if caller == nil {
		if g.app.hub == nil {
			return ctx, kernel.Invocation{}, nil, errors.New("memory model caller is not wired")
		}
		caller, budget = g.app.hub.Cfg.ModelCaller, g.app.hub.Cfg.Budget
	}
	in := kernel.InvocationFromContext(ctx)
	if in.RootID == "" {
		// 渠道守卫必须传原回合，缺身份直接拒绝，不能悄悄另建 memory 根绕限额。
		if strings.HasPrefix(cacheKey, "guard:") {
			return ctx, in, nil, errors.New("guard requires owning invocation")
		}
		id := "memory:" + store.NewID(time.Now())
		kind := kernel.KindMemory
		if g.kind != "" {
			kind = g.kind
		}
		in = kernel.Invocation{RootID: id, RunID: id, Kind: kind, Scope: kernel.Scope{CharacterID: g.character, Conversation: g.conversation}}
		limits := config.DefaultBudget().Limits(time.Now())
		if g.app != nil && g.app.hub != nil {
			limits = g.app.hub.Cfg.BudgetConfig.Limits(time.Now())
		}
		if err := turn.AdmitBudgetRoot(ctx, budget, in, limits); err != nil {
			return ctx, in, nil, err
		}
	} else {
		switch in.Kind {
		case "guard", "compact", "memory", "goal_judge", "link_translate", "advisor", "verify", "child", "foreground":
		default:
			return ctx, in, nil, errors.New("invalid auxiliary purpose")
		}
	}
	in.StepID = "step:" + store.NewID(time.Now())
	in.AttemptID = "attempt:" + store.NewID(time.Now())
	return kernel.WithInvocation(ctx, in), in, caller, nil
}
