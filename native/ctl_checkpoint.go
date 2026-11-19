package main

import (
	"context"
	"errors"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
)

var _ ctlserver.CheckpointBackend = (*ctlBackend)(nil)

func (b *ctlBackend) checkpointReader() (*backend.DesktopReader, error) {
	h, err := b.host()
	if err != nil {
		return nil, err
	}
	h.op.Lock()
	defer h.op.Unlock()
	if h.desk == nil {
		return nil, ctlproto.Errorf(ctlproto.CodeReadOnly, "请先打开可写后台")
	}
	// 只在取得桌面引用时持 op；入口生命周期负责与关闭衔接，审批等待不能卡住宿主停止。
	return &h.desk.Reader, nil
}
func checkpointKey(conversation string) api.ConversationKey {
	return api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: conversation}
}
func checkpointError(err error, data any) error {
	if err == nil {
		return nil
	}
	code := ctlproto.CodeFailed
	if errors.Is(err, api.ErrLocalChatUnsupported) {
		code = ctlproto.CodeReadOnly
	}
	return &ctlproto.Error{Code: code, Msg: err.Error(), Data: data}
}
func (b *ctlBackend) CheckpointList(ctx context.Context, conversation string) (ctlproto.CheckpointList, error) {
	r, err := b.checkpointReader()
	if err != nil {
		return ctlproto.CheckpointList{}, err
	}
	rows, err := r.ListCheckpoints(ctx, checkpointKey(conversation))
	if err != nil {
		return ctlproto.CheckpointList{}, checkpointError(err, nil)
	}
	out := ctlproto.CheckpointList{Items: make([]ctlproto.CheckpointSummary, len(rows))}
	for i, v := range rows {
		out.Items[i] = ctlproto.CheckpointSummary{ID: v.ID, RunID: v.RunID, ToolID: v.ToolID, Tool: v.Tool, State: v.State, CreatedMS: v.CreatedMS, Files: v.Files, Bytes: v.Bytes, Child: v.Child, Note: v.Note}
	}
	return out, nil
}
func (b *ctlBackend) CheckpointPreview(ctx context.Context, conversation, id, mode string) (ctlproto.CheckpointPlan, error) {
	r, err := b.checkpointReader()
	if err != nil {
		return ctlproto.CheckpointPlan{}, err
	}
	v, err := r.PreviewCheckpoint(ctx, checkpointKey(conversation), id, mode)
	if err != nil {
		return ctlproto.CheckpointPlan{}, checkpointError(err, nil)
	}
	out := ctlproto.CheckpointPlan{ID: v.ID, Mode: v.Mode, Version: v.Version, Workspace: v.Workspace, Files: make([]ctlproto.CheckpointFile, len(v.Files))}
	for i, f := range v.Files {
		out.Files[i] = ctlproto.CheckpointFile{Path: f.Path, TargetExists: f.TargetExists, CurrentExists: f.CurrentExists, TargetHash: f.TargetHash, CurrentHash: f.CurrentHash, TargetBytes: f.TargetBytes, CurrentBytes: f.CurrentBytes}
	}
	return out, nil
}
func (b *ctlBackend) CheckpointRestore(ctx context.Context, conversation, id, mode, version string) (ctlproto.CheckpointRestore, error) {
	r, err := b.checkpointReader()
	if err != nil {
		return ctlproto.CheckpointRestore{}, err
	}
	v, err := r.RestoreCheckpoint(ctx, checkpointKey(conversation), id, mode, version)
	out := ctlproto.CheckpointRestore{TargetID: v.TargetID, InverseID: v.InverseID, State: v.State, Completed: v.Completed, Remaining: v.Remaining}
	return out, checkpointError(err, out)
}

func ctlCheckpointProgress(p api.CheckpointProgress) ctlproto.CheckpointProgress {
	return ctlproto.CheckpointProgress{TargetID: p.TargetID, InverseID: p.InverseID, State: p.State, Done: p.Done, Total: p.Total, Completed: p.Completed, Remaining: p.Remaining, OutcomeSaved: p.OutcomeSaved, Error: p.Error}
}

func (b *ctlBackend) CheckpointProgress(ctx context.Context, conversation, id string) (ctlproto.CheckpointProgress, error) {
	r, err := b.checkpointReader()
	if err != nil {
		return ctlproto.CheckpointProgress{}, err
	}
	p, err := r.ReadCheckpointProgress(ctx, checkpointKey(conversation), id)
	return ctlCheckpointProgress(p), checkpointError(err, nil)
}

func (b *ctlBackend) CheckpointCancel(ctx context.Context, conversation, id string) (ctlproto.CheckpointProgress, error) {
	r, err := b.checkpointReader()
	if err != nil {
		return ctlproto.CheckpointProgress{}, err
	}
	p, err := r.CancelCheckpointRestore(ctx, checkpointKey(conversation), id)
	return ctlCheckpointProgress(p), checkpointError(err, nil)
}
