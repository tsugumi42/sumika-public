package turn

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
	"sumika-native/internal/turnbudget"
)

// ToolScheduler 的全局槽由同一宿主所有 Run/child 共用；审批线程不取槽。
type ToolScheduler struct {
	PerRun int
	global chan struct{}
}

func NewToolScheduler(perRun, global int) *ToolScheduler {
	return &ToolScheduler{PerRun: perRun, global: make(chan struct{}, global)}
}

type scheduledResult struct {
	i        int
	pc       PreparedCall
	r        tools.Result
	msg      Msg
	err      error
	executed bool
}

// runTools 只有协调器等待容量；每次取得真实槽后才开执行 goroutine。
// invalidText 是执行前发现工具绑定或授权失效时交回模型的话
const invalidText = "工具绑定或授权已失效，没有执行。"

func runTools(ctx context.Context, cfg Config, workspace Workspace, contextID string, st *store.Store, plugs *plugins, a Msg, stop, characterID, conversation string, full audience.Audience, decls []tools.Declaration, regs map[string]*toolReg, turnID string, guard *repeatGuard, inputSeqs []int64) ([]Msg, error) {
	scheduler := cfg.Scheduler
	batch, stopBatch := context.WithCancel(ctx)
	execution, stopExecution := executionContext(ctx)
	cancel := func() {
		stopBatch()
		// 停止导致的审批/准备错误不能提前取消已经派发的同行。
		if ctx.Err() == nil {
			stopExecution()
		}
	}
	defer func() { stopBatch(); stopExecution() }()
	completed := make(chan scheduledResult, len(a.Calls))
	outs := make([]scheduledResult, len(a.Calls))
	active := 0
	var firstErr error
	record := func(x scheduledResult) {
		if x.err != nil && firstErr == nil {
			firstErr = x.err
		}
		outs[x.i] = x
	}
	drainOne := func() { record(<-completed); active-- }
	toolStarted := map[string]time.Time{}
	finish := func(i int, pc PreparedCall, r tools.Result) (Msg, error) {
		resultCfg := cfg
		resultCfg.invocation = pc.Invocation
		call := a.Calls[i]
		call.ToolID, call.Args = pc.Invocation.ToolID, pc.Args
		m, err := finalizeTool(context.WithoutCancel(ctx), resultCfg, st, call, r)
		if err == nil && cfg.OnToolFinalized != nil {
			err = cfg.OnToolFinalized(context.WithoutCancel(ctx), pc.Invocation, r)
		}
		if cfg.OnProcess != nil {
			cfg.OnProcess(toolProcessDone(call.ID, call.Name, toolStarted[call.ID], cfg.Now(), r))
		}
		return m, err
	}
	for i, c := range a.Calls {
		for active > 0 {
			select {
			case x := <-completed:
				record(x)
				active--
			default:
				goto drained
			}
		}
	drained:
		in := toolInvocation(ctx, c, turnID, cfg)
		in.Scope.CharacterID, in.Scope.Conversation = characterID, conversation
		args := clip(jsjson.Stringify(c.Args), stepArgsMax)
		if err := st.StepStart(store.Step{ID: in.ToolID, CharacterID: characterID, Conversation: conversation, TurnID: turnID, Kind: "tool", Name: c.Name, Args: &args, Status: "running", StartedAt: cfg.Now().UnixMilli(), Identity: in, Ordinal: &i}); err != nil {
			firstErr = errors.Join(firstErr, err)
			cancel()
		}
		pc := PreparedCall{Invocation: in, Name: c.Name, Args: c.Args}
		immediate := func(r tools.Result) {
			m, err := finish(i, pc, r)
			record(scheduledResult{i: i, pc: pc, r: r, msg: m, err: err})
			if err != nil {
				cancel()
			}
		}
		if batch.Err() != nil || firstErr != nil {
			immediate(tools.Result{Text: "Operation aborted: skipped before dispatch", IsError: true})
			continue
		}
		if cfg.OnTool != nil {
			cfg.OnTool(c.ID, c.Name)
		}
		if cfg.OnProcess != nil {
			now := cfg.Now()
			toolStarted[c.ID] = now
			cfg.OnProcess(ProcessStep{ID: c.ID, Kind: "tool", Tool: c.Name, Target: tools.CallTarget(c.Name, c.Args), State: "running", Started: now.UnixMilli()})
		}
		if cfg.internal != nil {
			if cfg.internal.tools >= cfg.internal.limits.MaxTools {
				firstErr = turnbudget.ErrExhausted
				cancel()
				immediate(tools.Result{Text: firstErr.Error(), IsError: true})
				continue
			}
			cfg.internal.tools++
		}
		if cfg.Budget != nil {
			if err := cfg.Budget.Reserve(batch, kernel.Reservation{ID: in.ToolID, Invocation: in, Tools: 1}); err != nil {
				firstErr = err
				cancel()
				if errors.Is(err, turnbudget.ErrExhausted) {
					firstErr = errors.Join(err, turnbudget.ExhaustRoot(ctx, cfg.Budget, in.RootID))
				}
				immediate(tools.Result{Text: err.Error(), IsError: true})
				continue
			}
		}
		endPrepare := cfg.timeline.begin("tool.prepare", c.Name) // 等审批算在这里，不算进执行
		prepared, imm, err := prepareCall(batch, cfg, workspace, contextID, plugs, c, stop, characterID, conversation, turnID, full, decls, regs, in, inputSeqs)
		endPrepare(err != nil)
		pc = prepared
		if err == nil && imm == nil && pc.Rule != "" && cfg.OnProcess != nil {
			cfg.OnProcess(ProcessStep{ID: c.ID, Kind: "tool", Tool: c.Name, State: "running", Rule: pc.Rule})
		}
		if err != nil {
			firstErr = err
			cancel()
			immediate(tools.Result{Text: err.Error(), IsError: true})
			continue
		}
		if imm != nil {
			immediate(*imm)
			continue
		}
		shared := sharedCall(pc)
		endQueue := cfg.timeline.begin("tool.queue", c.Name)
		if !shared {
			for active > 0 {
				drainOne()
			}
		} else {
			for active >= scheduler.PerRun {
				drainOne()
			}
		}
		select {
		case scheduler.global <- struct{}{}:
			endQueue(false)
		case <-batch.Done():
			endQueue(true)
			immediate(tools.Result{Text: "Operation aborted: skipped before dispatch", IsError: true})
			continue
		}
		// 等槽/审批期间可能撤权或停用，不能把旧批准送到新注册。
		if batch.Err() != nil {
			<-scheduler.global
			immediate(tools.Result{Text: "已停止，没有执行。", IsError: true})
			continue
		}
		if pc.Valid != nil && !pc.Valid() {
			<-scheduler.global
			immediate(tools.Result{Text: invalidText, IsError: true})
			continue
		}
		// 批准和真正执行之间还隔着等槽：批准后目标变了、规则被撤销了，这里作废，下一次调用会重新问
		if pc.Recheck != nil {
			if why := pc.Recheck(); why != "" {
				<-scheduler.global
				immediate(tools.Result{Text: why, IsError: true})
				continue
			}
		}
		if in.Kind == kernel.KindChild && cfg.InternalAuthorize(batch, in, c.Name) != nil {
			<-scheduler.global
			immediate(tools.Result{Text: "子任务授权已失效，没有执行。", IsError: true})
			continue
		}
		workerCtx := kernel.WithInvocation(execution, in)
		if stopsAtOnce(pc.Name) {
			workerCtx = kernel.WithInvocation(batch, in) // 点了停止，正在跑的命令马上杀，不给宽限
		}
		var activity kernel.Activity
		if cfg.Lifetime != nil {
			var err error
			workerCtx, activity, err = cfg.Lifetime.Begin(workerCtx, in)
			if err != nil {
				<-scheduler.global
				firstErr = err
				cancel()
				immediate(tools.Result{Text: err.Error(), IsError: true})
				continue
			}
		}
		if cfg.Budget != nil {
			if err := cfg.Budget.Dispatch(batch, in.ToolID); err != nil {
				<-scheduler.global
				firstErr = err
				cancel()
				if errors.Is(err, turnbudget.ErrExhausted) {
					firstErr = errors.Join(err, turnbudget.ExhaustRoot(ctx, cfg.Budget, in.RootID))
				}
				immediate(tools.Result{Text: err.Error(), IsError: true})
				if activity != nil {
					activity.End(err)
				}
				continue
			}
		}
		active++
		go func(i int, pc PreparedCall, workerCtx context.Context, activity kernel.Activity) {
			var finalErr error
			if activity != nil {
				defer func() { activity.End(finalErr) }()
			}
			endExec := cfg.timeline.begin("tool.exec", pc.Name)
			r, err, aborted, exited := executeTracked(withEditHintsIfAllowed(workerCtx, cfg, workspace, pc), cfg, pc)
			endExec(err != nil || aborted || r.IsError)
			// 事实立即通知；repeatGuard 和模型投影在下面按 ordinal 提交。
			endSave := cfg.timeline.begin("tool.save", pc.Name)
			m, saveErr := finish(i, pc, r)
			endSave(saveErr != nil)
			if err != nil || saveErr != nil || aborted {
				cancel()
			}
			<-exited
			if cfg.Budget != nil {
				saveErr = errors.Join(saveErr, cfg.Budget.Settle(context.WithoutCancel(ctx), pc.Invocation.ToolID, kernel.Charge{Dispatched: true, UsageSeen: true}))
			}
			<-scheduler.global
			finalErr = errors.Join(err, saveErr)
			completed <- scheduledResult{i: i, pc: pc, r: r, msg: m, err: finalErr, executed: true}
		}(i, pc, workerCtx, activity)
		if !shared {
			drainOne()
		}
	}
	for active > 0 {
		drainOne()
	}
	results := make([]Msg, len(a.Calls))
	for i, x := range outs {
		m := x.msg
		if x.executed {
			if note := guard.observe(x.pc.Name, x.pc.Args, x.r.Text); note != "" {
				m.Text += strings.Repeat("\n", 3) + note
			}
			if x.pc.Note != "" { // 只给模型看的状态：步骤记录和界面里的输出不带它
				m.Text += "\n\n" + x.pc.Note
			}
		}
		results[i] = m
	}
	// 即使有基础设施错误，也把已成功及 skipped 配对交给主循环保存。
	return results, firstErr
}

// executeTracked 分开模型可见结局和真实退出：timeout 结果先落事实，槽只在 exited 后释放。
// aborted 只标记等待被取消或超时；业务 IsError 仍交给模型处理，不取消同批工具。
func executeTracked(ctx context.Context, cfg Config, pc PreparedCall) (tools.Result, error, bool, <-chan struct{}) {
	exited := make(chan struct{})
	tctx, cancel := context.WithCancel(ctx)
	var committed *tools.EditCommit
	if pc.Name == "edit" {
		committed = &tools.EditCommit{}
		tctx = tools.WithEditCommit(tctx, committed)
	}
	out := make(chan toolOutcome)
	go func() {
		executionCfg := cfg
		executionCfg.invocation = pc.Invocation
		var r tools.Result
		var err error
		if tools.Builtin(pc.Name) {
			r, err = captureTool(tctx, executionCfg, Call{ID: pc.Invocation.ProviderCallID, ToolID: pc.Invocation.ToolID, Name: pc.Name, Args: pc.Args}, pc.Execute)
		} else {
			r, err = pc.Execute(tctx)
		}
		select {
		case out <- toolOutcome{r, err}:
		case <-tctx.Done():
			if r.Disposition != nil {
				r.Disposition.Discard()
			}
		}
		cancel()
		close(exited)
	}()
	limit := time.Duration(pc.Spec.TimeoutMs) * time.Millisecond
	if cfg.ToolTimeout != nil {
		limit = cfg.ToolTimeout(pc.Name, limit)
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case x := <-out:
		return x.r, x.err, false, exited
	case <-ctx.Done():
		if x, ok := toolResultAtTimeout(out, committed, cancel); ok {
			return x.r, x.err, false, exited
		}
		cancel()
		r := interruptedTool(pc, "Operation aborted", false)
		r.Details = map[string]any{"interrupted": store.InterruptedByStop} // 记在 steps 上：程序退出时被切断的，退出收尾再改成“程序退出时中断”
		return r, nil, true, exited
	case <-timer.C:
		if x, ok := toolResultAtTimeout(out, committed, cancel); ok {
			return x.r, x.err, false, exited
		}
	}
	cancel()
	return interruptedTool(pc, fmt.Sprintf("超时了（%d 秒），没做完。", int(math.Round(limit.Seconds()))), true), nil, true, exited
}

func interruptedTool(pc PreparedCall, text string, retryHint bool) tools.Result {
	effects := pc.Spec.Effects
	if pc.Spec.Classify != nil {
		effects = pc.Spec.Classify(pc.Args)
	}
	if slices.ContainsFunc(effects, func(e string) bool { return e == "write" || e == "exec" || e == "net" }) {
		text += "这一步可能已经执行了一部分：先确认现在的状态（读一下文件、看一下结果），再决定要不要重做，别直接再来一遍。"
	} else if retryHint {
		text += "可以缩小范围再试（少读一点、换更具体的条件），或者分几步做。"
	}
	return tools.Result{Text: text, IsError: true}
}

// within 供单次执行使用；调度器必须同时等待 executeTracked 的真实退出信号。
func within(ctx context.Context, cfg Config, name string, args jsjson.Value, run func(context.Context) (tools.Result, error)) (tools.Result, error) {
	spec, ok := policy.Specs[name]
	if !ok {
		spec = policy.Undeclared
	}
	r, err, _, _ := executeTracked(ctx, cfg, PreparedCall{Name: name, Args: args, Spec: spec, Execute: run})
	return r, err
}
