package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"sumika-native/internal/api"
	"sumika-native/internal/checkpoint"
	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
)

var _ api.CheckpointActions = (*DesktopReader)(nil)

func (r *DesktopReader) enterCheckpoints(ctx context.Context, key api.ConversationKey) (context.Context, kernel.Scope, func(), error) {
	base, done, err := r.desk.enterChat()
	if err != nil {
		return nil, kernel.Scope{}, nil, err
	}
	scope, err := r.localInputScope(ctx, key)
	if err != nil {
		done()
		return nil, kernel.Scope{}, nil, err
	}
	if r.desk.app.checkpoints == nil {
		done()
		return nil, kernel.Scope{}, nil, fmt.Errorf("检查点未启用")
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(base, cancel)
	return child, scope, func() { stop(); cancel(); done() }, nil
}
func (r *DesktopReader) ListCheckpoints(ctx context.Context, key api.ConversationKey) ([]api.CheckpointSummary, error) {
	ctx, scope, done, err := r.enterCheckpoints(ctx, key)
	if err != nil {
		return nil, err
	}
	defer done()
	records, err := r.desk.app.checkpoints.List(ctx, scope)
	if err != nil {
		return nil, err
	}
	result := make([]api.CheckpointSummary, 0, len(records))
	for _, rec := range records {
		var size int64
		for _, state := range rec.Before {
			size += state.Bytes
		}
		for _, state := range rec.After {
			size += state.Bytes
		}
		result = append(result, api.CheckpointSummary{ID: rec.ID, RunID: rec.Invocation.RunID, ToolID: rec.Invocation.ToolID, Tool: rec.Tool, State: rec.Status, CreatedMS: rec.CreatedMS, Files: len(rec.Before), Bytes: size, Child: rec.Invocation.Kind == kernel.KindChild, Note: rec.SkipReason})
	}
	return result, nil
}
func checkpointView(plan checkpoint.Plan) api.CheckpointPlan {
	out := api.CheckpointPlan{ID: plan.ID, Mode: plan.Mode, Version: plan.Version, Workspace: plan.CWD, Files: make([]api.CheckpointFile, 0, len(plan.Files))}
	for _, file := range plan.Files {
		out.Files = append(out.Files, api.CheckpointFile{Path: file.Path, TargetExists: file.Before.Exists, CurrentExists: file.After.Exists, TargetHash: file.Before.Hash, CurrentHash: file.After.Hash, TargetBytes: file.Before.Bytes, CurrentBytes: file.After.Bytes})
	}
	return out
}
func (r *DesktopReader) PreviewCheckpoint(ctx context.Context, key api.ConversationKey, id, mode string) (api.CheckpointPlan, error) {
	ctx, scope, done, err := r.enterCheckpoints(ctx, key)
	if err != nil {
		return api.CheckpointPlan{}, err
	}
	defer done()
	release, err := r.desk.app.hub.AcquireLocalMaintenance(ctx, scope)
	if err != nil {
		return api.CheckpointPlan{}, err
	}
	defer release()
	plan, err := r.desk.app.checkpoints.Preview(ctx, scope, id, mode)
	return checkpointView(plan), err
}
func checkpointApproval(plan checkpoint.Plan) (string, bool) {
	var b strings.Builder
	fmt.Fprintf(&b, "检查点 %s mode=%s version=%s\n文件 %d 个；恢复不撤销聊天、命令外部副作用或预算。\n", plan.ID, plan.Mode, plan.Version, len(plan.Files))
	for i, file := range plan.Files {
		row := fmt.Sprintf("%s\n  当前 exists=%t bytes=%d sha=%s\n  目标 exists=%t bytes=%d sha=%s\n", file.Path, file.After.Exists, file.After.Bytes, file.After.Hash, file.Before.Exists, file.Before.Bytes, file.Before.Hash)
		if i >= 50 || b.Len()+len(row) > 12<<10 {
			return b.String(), true
		}
		b.WriteString(row)
	}
	return b.String(), false
}
func (r *DesktopReader) RestoreCheckpoint(ctx context.Context, key api.ConversationKey, id, mode, version string) (out api.CheckpointRestore, err error) {
	ctx, scope, done, err := r.enterCheckpoints(ctx, key)
	if err != nil {
		return out, err
	}
	defer done()
	a := r.desk.app
	var maintenance func()
	defer func() {
		if maintenance != nil {
			maintenance()
		}
	}()
	// 预览时占同一个屏障；等审批时释放，批准后重新取得并重新计算 CAS。
	release, err := a.hub.AcquireLocalMaintenance(ctx, scope)
	if err != nil {
		return out, err
	}
	plan, err := a.checkpoints.Preview(ctx, scope, id, mode)
	release()
	if err != nil {
		return out, err
	}
	if plan.Version != version {
		return out, checkpoint.ErrConflict
	}
	hostID := store.NewID(a.now())
	in := kernel.Invocation{Scope: scope, RootID: plan.Invocation.RootID, RunID: "checkpoint:" + hostID, StepID: "checkpoint-step:" + hostID, ToolID: "checkpoint-tool:" + hostID, Kind: kernel.KindForeground}
	ctx, activity, err := a.hub.Cfg.Lifetime.Begin(kernel.WithInvocation(ctx, in), in)
	if err != nil {
		return out, err
	}
	defer func() { activity.End(err) }()
	trace := a.set.TraceStore()
	if err = trace.TurnStart(in.RunID, scope.CharacterID, scope.Conversation, "console", a.now().UnixMilli()); err != nil {
		return out, err
	}
	defer func() {
		state := "done"
		if err != nil {
			state = "error"
		}
		err = errors.Join(err, trace.TurnEnd(in.RunID, state, a.now().UnixMilli()))
	}()
	args, _ := json.Marshal(map[string]string{"id": id, "mode": mode, "version": version})
	argText := string(args)
	if err = trace.StepStart(store.Step{ID: in.ToolID, Identity: in, CharacterID: scope.CharacterID, Conversation: scope.Conversation, TurnID: in.RunID, Kind: "tool", Name: "checkpoint_restore", Args: &argText, StartedAt: a.now().UnixMilli()}); err != nil {
		return out, err
	}
	defer func() {
		raw, marshalErr := json.Marshal(out)
		text := string(raw)
		if err != nil {
			text += "\n" + err.Error()
		}
		err = errors.Join(err, marshalErr, trace.StepEnd(in.ToolID, text, err != nil, a.now().UnixMilli(), nil))
		ref, saveErr := a.hub.Cfg.Results.Put(context.WithoutCancel(ctx), in, "text/plain; charset=utf-8", strings.NewReader(text), true)
		err = errors.Join(err, saveErr)
		if saveErr == nil {
			err = errors.Join(err, trace.StepResult(in.ToolID, ref))
		}
	}()
	preview, truncated := checkpointApproval(plan)
	if err = a.approvalGate.CheckCheckpoint(ctx, in, plan.CWD, preview, truncated); err != nil {
		return out, err
	}
	if err = a.authorizeTool(ctx, scope, "write"); err != nil {
		return out, err
	}
	maintenance, err = a.hub.AcquireLocalMaintenance(ctx, scope)
	if err != nil {
		return out, err
	}
	rec, restoreErr := a.checkpoints.Restore(ctx, scope, in, id, mode, version)
	out = api.CheckpointRestore{TargetID: id, InverseID: rec.ID, State: rec.Status, Completed: rec.Done, Remaining: checkpoint.Remaining(rec)}
	// 有持久逆检查点就可能发生过修改；即使结局保存失败也不能沿用旧 Live。
	if rec.ID != "" {
		a.hub.RestartIn(scope.CharacterID, scope.Conversation)
	}
	return out, restoreErr
}
