package turn

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

// prepareCall 是内置、插件与外部来源共同的冻结/审批路径；外部来源只能收紧中央声明。
func prepareCall(ctx context.Context, cfg Config, workspace Workspace, contextID string, plugs *plugins, c Call, stop, characterID, conversation, turnID string, full audience.Audience, decls []tools.Declaration, regs map[string]*toolReg, in kernel.Invocation, inputSeqs []int64) (PreparedCall, *tools.Result, error) {
	pc := PreparedCall{Invocation: in, Name: c.Name, Args: c.Args, Spec: policy.Undeclared}
	if in.Kind == kernel.KindChild {
		if err := cfg.InternalAuthorize(ctx, in, c.Name); err != nil {
			return pc, &tools.Result{Text: err.Error(), IsError: true}, nil
		}
	}
	fail := func(text string) (PreparedCall, *tools.Result, error) {
		return pc, &tools.Result{Text: text, IsError: true}, nil
	}
	if stop == "length" {
		return fail(fmt.Sprintf(`Tool call "%s" was not executed: the response hit the output token limit, so its arguments may be truncated. Re-issue the tool call with complete arguments.`, c.Name))
	}
	if c.ArgsErr != "" {
		return fail(fmt.Sprintf(`Invalid arguments for tool "%s": %s`, c.Name, c.ArgsErr))
	}
	if !known(decls, c.Name) {
		return fail(fmt.Sprintf("Tool %s not found", c.Name))
	}
	pc.Args = tools.Prepare(c.Name, c.Args)
	if spec, ok := policy.Specs[c.Name]; ok {
		pc.Spec = spec
	}
	call := ToolCall{InputSeqs: slices.Clone(inputSeqs), Name: c.Name, Identity: in, CharacterID: characterID, Conversation: conversation, TurnID: turnID, Audience: full, Args: pc.Args, Workspace: workspace, ContextID: contextID}
	reg := regs[c.Name]
	if reg != nil {
		pc.Spec = reg.spec
		pc.Binding = fmt.Sprintf("%s:%p", c.Name, reg)
		// 没走到 runTool 就拒掉的（插件停了或换了、听众不能用）也在插件日志里记一行 host.tool（规格-插件两层 第二点五节第 3 条：每次工具调用）
		refuse := func(kind, text string) (PreparedCall, *tools.Result, error) {
			res := tools.Result{Text: text, IsError: true}
			plugs.traceTool(ctx, reg, time.Now(), kind, res)
			return pc, &res, nil
		}
		if current := plugs.tool(c.Name); current != reg {
			why := "停掉了"
			if current != nil {
				why = "变了"
			}
			return refuse("gone", fmt.Sprintf("工具 %s 没有执行：提供它的插件%s。", c.Name, why))
		}
		allowed := func() bool {
			return (reg.spec.Audience != "owner-console" || ownerConsoleAudience(full)) && (reg.t.For == nil || reg.t.For(full))
		}
		if !allowed() {
			return refuse("denied", fmt.Sprintf("工具 %s 没有执行：当前听众不能用。", c.Name))
		}
		// Valid 由调度器在真正执行前调一次；失效时在这里记 host.tool（有记录的副作用，只该在那一处调）
		pc.Valid = func() bool {
			kind := ""
			switch {
			case plugs.tool(c.Name) != reg || reg.ctx.Err() != nil:
				kind = "gone"
			case !allowed():
				kind = "denied"
			}
			if kind != "" {
				plugs.traceTool(ctx, reg, time.Now(), kind, tools.Result{Text: invalidText, IsError: true})
			}
			return kind == ""
		}
		pc.Execute = func(ctx context.Context) (tools.Result, error) {
			call.Args = pc.Args
			return plugs.runTool(ctx, reg, call), nil
		}
	} else {
		exec := cfg.ExecTool
		if exec == nil {
			exec = tools.Execute
		}
		pc.Execute = func(ctx context.Context) (tools.Result, error) {
			if cfg.Instructions != nil {
				return cfg.Instructions.Execute(ctx, ContextIn{CharacterID: characterID, Conversation: conversation, Audience: full, Workspace: workspace, ContextID: contextID}, c.Name, pc.Args, func(ctx context.Context) (tools.Result, error) { return exec(ctx, workspace.CWD, c.Name, pc.Args) })
			}
			return exec(ctx, workspace.CWD, c.Name, pc.Args)
		}
	}
	targetApproved := false
	// 外部解析器只接管自己的来源；空 Name 表示不接管，错误不能退到未冻结的执行路径。
	if cfg.Resolver != nil && !tools.Builtin(c.Name) {
		resolverCall := call
		resolverCall.InputSeqs = slices.Clone(call.InputSeqs)
		resolved, err := cfg.Resolver.Prepare(kernel.WithInvocation(ctx, in), resolverCall)
		if err != nil {
			var refused Refusal
			if errors.As(err, &refused) {
				return fail(refused.Text) // 拒绝、过期、目标变了是这一步的结局，不是这一轮出错
			}
			return pc, nil, err
		}
		if resolved.Name != "" {
			if resolved.Name != c.Name {
				return pc, nil, fmt.Errorf("resolver changed tool identity %s to %s", c.Name, resolved.Name)
			}
			originalValid := pc.Valid
			pc.Args, pc.Binding, pc.Execute = resolved.Args, resolved.Binding, resolved.Execute
			pc.Note, pc.Recheck, pc.Rule = resolved.Note, resolved.Recheck, resolved.Rule
			targetApproved = c.Name == "mcp_call" && resolved.Binding != "" && resolved.Valid != nil
			if originalValid != nil && resolved.Valid != nil {
				pc.Valid = func() bool { return originalValid() && resolved.Valid() }
			} else if resolved.Valid != nil {
				pc.Valid = resolved.Valid
			}
		}
	}
	if cfg.Gate == nil {
		return fail("工具没有接上审批，没有执行。")
	}
	// MCP resolver 已批准同一冻结目标；普通工具仍经过中央 Gate。
	if targetApproved {
		if pc.Execute == nil {
			return pc, nil, fmt.Errorf("冻结目标缺执行体")
		}
		return pc, nil, nil
	}
	cl := cfg.Gate.Clear(kernel.WithInvocation(ctx, in), workspace.CWD, characterID, policyAudience(full), conversation, approval.Call{Identity: in, ID: in.ToolID, Name: c.Name, Args: pc.Args})
	if ctx.Err() != nil {
		return fail("Operation aborted")
	}
	if cl.Blocked {
		return fail(cl.Reason)
	}
	pc.Note, pc.Recheck = cl.Note(), cl.Recheck
	if cl.Traced {
		pc.Rule = approval.RuleText(cl.By, cl.Label, cl.Reason)
	}
	if pc.Execute == nil {
		return pc, nil, fmt.Errorf("prepared tool %s has no execution body", c.Name)
	}
	return pc, nil, nil
}

func toolInvocation(ctx context.Context, c Call, turnID string, cfg Config) kernel.Invocation {
	in := kernel.InvocationFromContext(ctx)
	in.RunID = turnID
	if in.RootID == "" {
		in.RootID = turnID
		in.Kind = kernel.KindForeground
	}
	in.ToolID = c.ToolID
	if in.ToolID == "" {
		in.ToolID = "tool:" + storeID(cfg)
	}
	in.ProviderCallID = c.ID
	return in
}

func storeID(cfg Config) string {
	now := time.Now
	if cfg.Now != nil {
		now = cfg.Now
	}
	return store.NewID(now())
}

// shared 只认中央显式 Parallel；动态 classify 不将 powershell 或写工具升为 shared。
func sharedCall(pc PreparedCall) bool {
	if !pc.Spec.Parallel {
		return false
	}
	for _, e := range pc.Spec.Effects {
		if e == "write" || e == "exec" || e == "net" {
			return false
		}
	}
	return true
}
