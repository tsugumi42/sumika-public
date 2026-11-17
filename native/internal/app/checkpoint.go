package app

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"sumika-native/internal/checkpoint"
	"sumika-native/internal/ecosystem"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

func (a *App) openCheckpoints() error {
	if !a.cfg.Tools.Checkpoints.Enabled {
		return nil
	}
	service, err := checkpoint.New(a.paths.CheckpointsDir(), checkpoint.Options{Now: a.now, Authorize: a.authorizeTool, Exclude: func(path string) bool {
		return policy.TouchesProtected(path, a.workdir(), a.approvalGate.ProtectedDirs, false)
	}})
	if err != nil {
		return err
	}
	a.checkpoints = service
	a.hub.Cfg.ExecTool = a.executeCheckpointed
	a.hub.Cfg.Materials = a.kernelMaterials
	return nil
}
func (a *App) checkpointOwner(ctx context.Context, in kernel.Invocation) (kernel.Scope, error) {
	if in.Kind != kernel.KindChild {
		return in.Scope, nil
	}
	record, err := (store.EcosystemStore{DB: a.set.TraceStore().Trace}).Task(ctx, in.RunID)
	if err != nil {
		return kernel.Scope{}, err
	}
	if record.RootID != in.RootID || record.ParentRun != in.ParentRunID || in.Scope.Conversation != "internal:"+record.ID {
		return kernel.Scope{}, ecosystem.ErrDenied
	}
	return record.Owner, nil
}

// 检查点的范围（参照 Claude Code：只给自己的写、改文件工具留快照，跑命令不追踪；这里再加一档）：
//   - write、edit：拍目标文件；
//   - powershell：只拍审批门分析出的、工作目录内的写入目标；只读命令不拍；看不清会动哪些文件的不拍；
//   - 任何一种拍不成（容量、预算、链接……），工具照常执行，结果里告诉模型"没拍、不能回退"。
//
// 身份和授权（Admit）对所有路径一视同仁：不拍快照不等于不查权限。
func (a *App) executeCheckpointed(ctx context.Context, cwd, name string, args jsjson.Value) (tools.Result, error) {
	if name != "write" && name != "edit" && name != "powershell" {
		return tools.Execute(ctx, cwd, name, args)
	}
	in := kernel.InvocationFromContext(ctx)
	owner, err := a.checkpointOwner(ctx, in)
	if err != nil {
		return tools.Result{}, err
	}
	capture := checkpoint.Capture{Invocation: in, Owner: owner, CWD: cwd, Tool: name}
	// 后台命令调用一返回就结束，真正的文件变化发生在以后、在这个窗口外：不拍检查点，免得给出"已捕获"的错觉；
	// 但身份和授权关照过（不能借"不拍"绕开权限）
	if v := args.Obj["background"]; name == "powershell" && v.Kind == jsjson.Bool && v.Bool {
		if err := a.checkpoints.Admit(ctx, capture); err != nil {
			return tools.Result{}, err
		}
		return tools.Execute(ctx, cwd, name, args)
	}
	var skip string // 非空：这次不拍快照，原因
	if name == "powershell" {
		paths, why := a.shellCheckpointScope(ctx, cwd, args)
		switch {
		case why != "":
			skip = why
		case len(paths) == 0:
			// 只读，或只动工作目录外、不在检查点范围内的东西：没有要拍的。
			if err := a.checkpoints.Admit(ctx, capture); err != nil {
				return tools.Result{}, err
			}
			return tools.Execute(ctx, cwd, name, args)
		default:
			capture.Paths = paths
		}
	} else {
		if err := a.checkpoints.Admit(ctx, capture); err != nil {
			return tools.Result{}, err
		}
		path := policy.ToolPath(args.Obj["path"].Str, cwd)
		if policy.IsInside(path, []string{a.paths.Root}, cwd) {
			return tools.Result{Text: "检查点不捕获 Sumika 数据目录；本次文件变更没有执行。", IsError: true}, nil
		}
		capture.Paths = []string{filepath.Clean(path)}
	}
	var result tools.Result
	executed := false
	var toolErr error
	execute := func() error {
		executed = true
		result, toolErr = tools.Execute(ctx, cwd, name, args)
		return toolErr
	}
	var record checkpoint.Record
	if skip != "" {
		record, err = a.checkpoints.Skip(ctx, capture, skip, execute)
	} else {
		record, err = a.checkpoints.Run(ctx, capture, execute)
	}
	if !executed {
		return result, err // 身份、授权或存储出错，工具没有执行
	}
	if err != nil && toolErr == nil {
		// 工具已经执行成功，只是检查点没存好后态：交回工具结果并说明，不让整轮失败，也别让模型重跑。
		slog.Warn("检查点后态没存好，工具结果照常交回", "tool", name, "err", err)
		result.Text += fmt.Sprintf("\n[命令已执行完，但检查点没能保存这次改动（%v）；不要重跑，改动不能用检查点回退。]", err)
		return result, nil
	}
	err = toolErr
	if record.Status == "skipped" {
		result.Text += fmt.Sprintf("\n[这次没拍快照（%s），改动不能用检查点回退。]", record.SkipReason)
		return result, err
	}
	if record.ID != "" {
		result.Text += fmt.Sprintf("\n[checkpoint %s state=%s changed=%d]", record.ID, record.Status, len(record.Before))
		if name == "powershell" {
			result.Text += "\n恢复只覆盖这条命令写入的、工作目录内的普通文件；.git、node_modules、.venv、vendor、受保护文件、工作目录外和外部副作用不在范围内。"
		}
	}
	return result, err
}

// 这些命令的改动没法从分析结果里列全（git 改一片文件、下载落在当前目录、建目录、解压成一堆文件……），按看不清处理。
var unmappedCommands = map[string]bool{"curl": true, "wget": true, "robocopy": true, "xcopy": true, "icacls": true, "attrib": true,
	"mkdir": true, "md": true, "new-item": true, "ni": true, "expand-archive": true}

// shellCheckpointScope 用审批门同一个分析器，再分析一次命令，得出要拍快照的路径。
// 不改 kernel 和审批接口：分析器常驻、一条命令几毫秒，再分析一次比把判定结果穿过 turn 和 kernel 干净；
// 分析不出来（超时、没装 pwsh）就是"看不清"，不拍。
// paths 为空且 why 为空：没有要拍的；why 非空：看不清，这次不拍。
func (a *App) shellCheckpointScope(ctx context.Context, cwd string, args jsjson.Value) (paths []string, why string) {
	const unclear = "看不清这条命令会动哪些文件"
	command := args.Obj["command"]
	if command.Kind != jsjson.String {
		return nil, unclear
	}
	analysis := a.approvalGate.AnalyzeShell(ctx, command.Str)
	if analysis == nil {
		return nil, unclear
	}
	v := policy.Judge(*analysis)
	if v.ReadOnly && !v.Opaque && len(v.MethodRisk) == 0 {
		return nil, ""
	}
	if v.Opaque || len(v.Unclear) > 0 || len(v.Runs) > 0 || len(v.MethodRisk) > 0 || len(v.MustAsk) > 0 || len(v.FileTargets) == 0 {
		return nil, unclear
	}
	for _, n := range v.Names {
		if unmappedCommands[n] || n == "git" || strings.HasPrefix(n, "git ") {
			return nil, unclear
		}
	}
	abs := make([]string, 0, len(v.FileTargets))
	for _, t := range v.FileTargets {
		abs = append(abs, policy.ToolPath(t, cwd))
	}
	paths, clear := a.checkpoints.Targets(cwd, abs)
	if !clear {
		return nil, unclear
	}
	return paths, ""
}
func (a *App) checkpointMaterials(ctx context.Context, in kernel.Invocation) error {
	if a.checkpoints == nil || in.Scope.Platform != "local" || in.Scope.Channel != "console" || in.Scope.Scene != "private" || !strings.HasPrefix(in.Scope.Conversation, "console:") || strings.Contains(in.Scope.Conversation, ":group:") {
		return nil
	}
	// 撤权后普通聊天仍可受理，但旧本地恢复资料不能再进入模型。
	if err := a.authorizeTool(ctx, in.Scope, "read"); err != nil {
		return nil
	}
	notices, err := a.checkpoints.Notices(ctx, in.Scope)
	if err != nil {
		return err
	}
	for _, notice := range notices {
		id := "checkpoint:" + ecosystem.Digest([]string{notice, in.RootID})
		if err := a.set.TraceStore().PutKernelMaterial(ctx, in, store.KernelMaterial{ID: id, Kind: "checkpoint", Text: notice}); err != nil {
			return err
		}
	}
	return nil
}
