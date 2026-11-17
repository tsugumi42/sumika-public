package api

import (
	"context"
	"errors"
)

// ErrNotFound 是要找的对象（检查点、会话里的某一项）不存在。线上码 1018；Go 后台现在多用具体的错误，Bun 后台用这个。
var ErrNotFound = errors.New("没有找到")

// CheckpointActions 只由可写本地宿主提供；会话身份沿用 LocalChat 的所有权检查。
type CheckpointActions interface {
	ListCheckpoints(context.Context, ConversationKey) ([]CheckpointSummary, error)
	PreviewCheckpoint(context.Context, ConversationKey, string, string) (CheckpointPlan, error)
	RestoreCheckpoint(context.Context, ConversationKey, string, string, string) (CheckpointRestore, error)
	ReadCheckpointProgress(context.Context, ConversationKey, string) (CheckpointProgress, error)
	WatchCheckpointProgress(context.Context, ConversationKey, string) (Subscription[CheckpointProgress], error)
	CancelCheckpointRestore(context.Context, ConversationKey, string) (CheckpointProgress, error)
}
type CheckpointSummary struct {
	ID, RunID, ToolID, Tool, State string
	CreatedMS                      int64
	Files                          int
	Bytes                          int64
	Child                          bool
	Note                           string // State 为 skipped（没拍快照）时的原因
}
type CheckpointFile struct {
	Path                        string
	TargetExists, CurrentExists bool
	TargetHash, CurrentHash     string
	TargetBytes, CurrentBytes   int64
}
type CheckpointPlan struct {
	ID, Mode, Version, Workspace string
	Files                        []CheckpointFile
}
type CheckpointRestore struct {
	TargetID, InverseID, State string
	Completed                  []string
	Remaining                  []string
}

type CheckpointProgress struct {
	TargetID, InverseID, State string
	Done, Total                int
	Completed, Remaining       []string
	OutcomeSaved               bool
	Error                      string
}
