package rpcserve_test

import (
	"context"
	"fmt"
	"sync"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
)

// fakeFull 实现第 2 批的 16 个可选接口（加一个空的 Reader）：每个方法记一笔、回固定的编造值；err 非空时全部报这个错。
// 用来对"经协议调到的"和"直接调的"是不是一样，不碰任何后台。
type fakeFull struct {
	api.Reader
	api.LocalStatusReader
	api.LoopControl
	api.ReminderControl
	mu    sync.Mutex
	calls []string
	err   error

	approvals *fakeSub[[]api.LocalApproval]
}

func (f *fakeFull) rec(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	return f.err
}

func (f *fakeFull) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type fakeSub[T any] struct {
	ch     chan api.Snapshot[T]
	mu     sync.Mutex
	closed bool
}

func newFakeSub[T any](first T) *fakeSub[T] {
	s := &fakeSub[T]{ch: make(chan api.Snapshot[T], 8)}
	s.ch <- api.Snapshot[T]{Version: 1, Result: api.Result[T]{State: api.Ready, Value: first}}
	return s
}

func (s *fakeSub[T]) Updates() <-chan api.Snapshot[T] { return s.ch }
func (s *fakeSub[T]) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}
func (s *fakeSub[T]) isClosed() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.closed }
func (s *fakeSub[T]) push(v T, version uint64) {
	s.ch <- api.Snapshot[T]{Version: version, Result: api.Result[T]{State: api.Ready, Value: v}}
}

var testKey = api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:a"}

func fakeApprovals() []api.LocalApproval {
	return []api.LocalApproval{{ID: "ap1", CharacterID: "a", Key: testKey, Title: "跑命令", Tool: "run_command", Subject: "echo 编造", Options: []api.ApprovalChoice{api.ApproveConversation, api.ApproveAlways}, ExpiresAt: 99}}
}

func (f *fakeFull) ReadAvatar(_ context.Context, key string) (api.Result[api.MediaView], error) {
	if err := f.rec("ReadAvatar"); err != nil {
		return api.Result[api.MediaView]{State: api.Failed, Err: err}, err
	}
	return api.Result[api.MediaView]{State: api.Ready, Value: api.MediaView{Bytes: []byte(key), MIME: "image/png"}}, nil
}

func (f *fakeFull) ReadForward(_ context.Context, req api.ForwardRequest) (api.Result[api.ForwardView], error) {
	if err := f.rec("ReadForward"); err != nil {
		return api.Result[api.ForwardView]{State: api.Failed, Value: api.ForwardView{State: "too_large"}, Err: err}, err
	}
	return api.Result[api.ForwardView]{State: api.Ready, Value: api.ForwardView{ID: req.ID, Nodes: []api.ForwardNode{{UID: "1", Name: "甲", At: 5}}, State: "ready"}}, nil
}

func (f *fakeFull) MarkRead(_ context.Context, k api.ConversationKey, upTo int64) error {
	return f.rec("MarkRead")
}

func (f *fakeFull) PrepareVideo(_ context.Context, ref api.MediaRef) (string, error) {
	if err := f.rec("PrepareVideo"); err != nil {
		return "", err
	}
	return "C:\\编造\\" + ref.Hash + ".mp4", nil
}

func (f *fakeFull) SendLocal(_ context.Context, k api.ConversationKey, text string, draft func(api.LocalDraft)) error {
	if draft != nil {
		draft(api.LocalDraft{TurnID: "t1", Step: 0, Text: "前半句"})
		draft(api.LocalDraft{TurnID: "t1", Step: 0, Text: "前半句后半句"})
		draft(api.LocalDraft{TurnID: "t1", Step: 0})
	}
	return f.rec("SendLocal:" + text)
}

func (f *fakeFull) StopLocal(_ context.Context, k api.ConversationKey) error {
	return f.rec("StopLocal")
}

func (f *fakeFull) WatchLocalApprovals(context.Context) (api.Subscription[[]api.LocalApproval], error) {
	if err := f.rec("WatchLocalApprovals"); err != nil {
		return nil, err
	}
	f.approvals = newFakeSub(fakeApprovals())
	return f.approvals, nil
}

func (f *fakeFull) DecideLocal(_ context.Context, k api.ConversationKey, id string, c api.ApprovalChoice) error {
	return f.rec("DecideLocal:" + id + ":" + string(c))
}

func (f *fakeFull) SendLocalImages(_ context.Context, k api.ConversationKey, text string, images [][]byte, draft func(api.LocalDraft)) error {
	if draft != nil {
		draft(api.LocalDraft{TurnID: "t2", Text: "看图"})
	}
	n := 0
	for _, im := range images {
		n += len(im)
	}
	return f.rec("SendLocalImages:" + text + ":" + string(rune('0'+len(images))) + ":" + string(rune('0'+n%10)))
}

func (f *fakeFull) SaveLocalImage(_ context.Context, k api.ConversationKey, png []byte) (api.LocalAttachment, error) {
	if err := f.rec("SaveLocalImage"); err != nil {
		return api.LocalAttachment{}, err
	}
	return api.LocalAttachment{Kind: "image", Path: "C:\\编造\\a.png", Width: len(png), Height: 2}, nil
}

func (f *fakeFull) SendLocalParts(_ context.Context, k api.ConversationKey, parts []api.LocalPart, draft func(api.LocalDraft)) error {
	if draft != nil {
		draft(api.LocalDraft{TurnID: "t3", Text: "分块"})
	}
	if len(parts) > 0 && parts[0].Quote != nil {
		return f.rec("SendLocalParts:quote:" + parts[0].Text)
	}
	return f.rec("SendLocalParts")
}

var groupKey = api.ConversationKey{Platform: "local", Channel: "console", Scene: "group", Conversation: "console:group:g1"}

func (f *fakeFull) CreateLocalGroup(_ context.Context, req api.LocalGroupCreate) (api.ConversationView, error) {
	if err := f.rec("CreateLocalGroup:" + req.Name); err != nil {
		return api.ConversationView{}, err
	}
	return api.ConversationView{Key: groupKey, Title: req.Name, Observers: req.Characters, CreatedAt: 77}, nil
}

func (f *fakeFull) SendLocalGroup(_ context.Context, req api.LocalGroupSend) error {
	if req.Draft != nil {
		req.Draft(api.LocalDraft{TurnID: "0", Step: -1, CharacterID: "a"})
		req.Draft(api.LocalDraft{TurnID: "0", Step: 0, Text: "群里的前半句", CharacterID: "a"})
	}
	if req.Process != nil {
		req.Process("0", []api.LocalProcessStep{{ID: "s1", Kind: "tool", Tool: "read", Target: "a.txt", State: "done", Step: 0}})
	}
	if req.Draft != nil {
		req.Draft(api.LocalDraft{TurnID: "1", Step: -1, CharacterID: "b"})
	}
	return f.rec("SendLocalGroup:" + req.Text + ":" + fmt.Sprint(req.Mentions))
}

func (f *fakeFull) StopLocalGroup(_ context.Context, k api.ConversationKey) error {
	return f.rec("StopLocalGroup:" + k.Conversation)
}

func (f *fakeFull) SubmitLocal(_ context.Context, k api.ConversationKey, in api.LocalInputSubmission) (api.InputReceipt, error) {
	if err := f.rec("SubmitLocal:" + in.Mode); err != nil {
		return api.InputReceipt{}, err
	}
	return api.InputReceipt{InputID: "i1", RunID: "r1", State: "accepted", Sequence: 3, Revision: 4}, nil
}

func (f *fakeFull) StopLocalRun(_ context.Context, k api.ConversationKey, runID string, through uint64) (api.StopReceipt, error) {
	if err := f.rec("StopLocalRun"); err != nil {
		return api.StopReceipt{}, err
	}
	return api.StopReceipt{RunID: runID, Revision: through + 1, Drained: true}, nil
}

func (f *fakeFull) WatchLocalRun(_ context.Context, k api.ConversationKey, runID string) (api.Subscription[api.LocalRunSnapshot], error) {
	if err := f.rec("WatchLocalRun"); err != nil {
		return nil, err
	}
	return newFakeSub(api.LocalRunSnapshot{RunID: runID, State: "running", Text: "写到一半", Revision: 2, Step: 1,
		Process: []api.LocalProcessStep{{ID: "s1", Kind: "tool", Tool: "run_command", State: "running"}}}), nil
}

func (f *fakeFull) HeldLocal(context.Context) ([]api.HeldInput, error) {
	if err := f.rec("HeldLocal"); err != nil {
		return nil, err
	}
	return []api.HeldInput{{Key: testKey, InputID: "h1", Text: "没处理", AcceptedAt: 7}}, nil
}

func (f *fakeFull) ReleaseHeldLocal(_ context.Context, k api.ConversationKey, id string) (api.InputReceipt, error) {
	if err := f.rec("ReleaseHeldLocal"); err != nil {
		return api.InputReceipt{}, err
	}
	return api.InputReceipt{InputID: id, RunID: "r2", State: "queued", Sequence: 1, Revision: 1}, nil
}

func (f *fakeFull) DiscardHeldLocal(_ context.Context, k api.ConversationKey, id string) error {
	return f.rec("DiscardHeldLocal")
}

func (f *fakeFull) WithdrawQueuedLocal(_ context.Context, k api.ConversationKey, id string) error {
	return f.rec("WithdrawQueuedLocal")
}

func (f *fakeFull) SendWhisper(_ context.Context, k api.ConversationKey, parts []api.LocalPart) (api.WhisperReceipt, error) {
	if err := f.rec("SendWhisper:" + k.Conversation); err != nil {
		return api.WhisperReceipt{}, err
	}
	var who []string
	for _, p := range parts {
		who = append(who, p.Mentions...)
	}
	return api.WhisperReceipt{Batch: "b1", Characters: who}, nil
}

func fakeLogPage() ctlproto.LogPage {
	return ctlproto.LogPage{Entries: []ctlproto.LogEntry{
		{ID: "say:b1", Batch: "b1", Kind: ctlproto.LogWhisper, At: 5, Summary: "悄悄话", Targets: []string{"a"}, Quotes: []ctlproto.LogQuote{{Name: "甲", At: 4, Text: "引一句"}}},
		{ID: "arr:b1:a", Batch: "b1", Kind: ctlproto.LogArrange, Character: "a", State: "running", Busy: true, Egress: "e1", EgressN: 2, EgressDanger: 1},
	}, Next: "c2", Running: true}
}

func (f *fakeFull) ReadWhisperLog(_ context.Context, k api.ConversationKey, cursor string, limit int) (ctlproto.LogPage, error) {
	if err := f.rec(fmt.Sprintf("ReadWhisperLog:%s:%s:%d", k.Conversation, cursor, limit)); err != nil {
		return ctlproto.LogPage{}, err
	}
	return fakeLogPage(), nil
}

func (f *fakeFull) ReadWhisperLogEntry(_ context.Context, k api.ConversationKey, id string, withThinking bool) (ctlproto.LogDetail, error) {
	if err := f.rec(fmt.Sprintf("ReadWhisperLogEntry:%s:%v", id, withThinking)); err != nil {
		return ctlproto.LogDetail{}, err
	}
	return ctlproto.LogDetail{ID: id, Kind: ctlproto.LogThink, Title: "思考", Text: "全文", Args: "{}", Hidden: !withThinking}, nil
}

func (f *fakeFull) WatchWhisperLog(_ context.Context, k api.ConversationKey) (api.Subscription[ctlproto.LogPage], error) {
	if err := f.rec("WatchWhisperLog:" + k.Conversation); err != nil {
		return nil, err
	}
	return newFakeSub(fakeLogPage()), nil
}

func (f *fakeFull) CancelWhisper(_ context.Context, k api.ConversationKey, batch, character string) (int, error) {
	if err := f.rec("CancelWhisper:" + batch + ":" + character); err != nil {
		return 0, err
	}
	return 2, nil
}

func (f *fakeFull) WatchLocalQuestions(context.Context) (api.Subscription[[]api.LocalQuestion], error) {
	if err := f.rec("WatchLocalQuestions"); err != nil {
		return nil, err
	}
	return newFakeSub([]api.LocalQuestion{{ID: "q1", Key: testKey, Text: "选哪个", Options: []api.LocalQuestionOption{{Label: "甲", Detail: "好"}}}}), nil
}

func (f *fakeFull) AnswerLocal(_ context.Context, k api.ConversationKey, id string, option int) error {
	return f.rec("AnswerLocal")
}

func (f *fakeFull) ListApprovalRules(context.Context) ([]api.ApprovalRule, error) {
	if err := f.rec("ListApprovalRules"); err != nil {
		return nil, err
	}
	return []api.ApprovalRule{{Scope: "always", Key: "k", Label: "git status", Tool: "run_command", Effect: "allow", Pattern: "Bash(git status:*)", Dirs: []string{"C:\\编造"}, At: 5}}, nil
}

func (f *fakeFull) RevokeApprovalRule(_ context.Context, r api.ApprovalRule) error {
	return f.rec("RevokeApprovalRule:" + r.Key)
}

func (f *fakeFull) AddApprovalRule(_ context.Context, raw, effect, project, session string) error {
	return f.rec("AddApprovalRule:" + raw + ":" + effect)
}

func (f *fakeFull) ConversationApprovalMode(_ context.Context, k api.ConversationKey) (api.ConversationApprovalMode, error) {
	if err := f.rec("ConversationApprovalMode"); err != nil {
		return api.ConversationApprovalMode{}, err
	}
	return api.ConversationApprovalMode{Mode: "auto", Default: "manual", Override: true}, nil
}

func (f *fakeFull) SetConversationApprovalMode(_ context.Context, k api.ConversationKey, mode string) error {
	return f.rec("SetConversationApprovalMode:" + mode)
}

func (f *fakeFull) ReadLocalWorkspace(_ context.Context, k api.ConversationKey) (api.WorkspaceInfo, error) {
	if err := f.rec("ReadLocalWorkspace"); err != nil {
		return api.WorkspaceInfo{}, err
	}
	return api.WorkspaceInfo{CWD: "C:\\编造\\项目", Root: "C:\\编造\\项目", Name: "项目", Bound: true}, nil
}

func (f *fakeFull) SetLocalWorkspace(_ context.Context, k api.ConversationKey, dir string) (api.WorkspaceInfo, error) {
	if err := f.rec("SetLocalWorkspace"); err != nil {
		return api.WorkspaceInfo{}, err
	}
	return api.WorkspaceInfo{CWD: dir, Root: dir, Name: "新", Bound: true}, nil
}

func (f *fakeFull) ClearLocalWorkspace(_ context.Context, k api.ConversationKey) (api.WorkspaceInfo, error) {
	if err := f.rec("ClearLocalWorkspace"); err != nil {
		return api.WorkspaceInfo{}, err
	}
	return api.WorkspaceInfo{CWD: "C:\\默认", Problem: "目录不存在"}, nil
}

func (f *fakeFull) RecentWorkspaces(context.Context) ([]string, error) {
	if err := f.rec("RecentWorkspaces"); err != nil {
		return nil, err
	}
	return []string{"C:\\编造\\一", "C:\\编造\\二"}, nil
}

func (f *fakeFull) ListCheckpoints(_ context.Context, k api.ConversationKey) ([]api.CheckpointSummary, error) {
	if err := f.rec("ListCheckpoints"); err != nil {
		return nil, err
	}
	return []api.CheckpointSummary{{ID: "c1", RunID: "r1", Tool: "write_file", State: "ready", CreatedMS: 9, Files: 2, Bytes: 30, Note: "n"}}, nil
}

func (f *fakeFull) PreviewCheckpoint(_ context.Context, k api.ConversationKey, id, mode string) (api.CheckpointPlan, error) {
	if err := f.rec("PreviewCheckpoint"); err != nil {
		return api.CheckpointPlan{}, err
	}
	return api.CheckpointPlan{ID: id, Mode: mode, Version: "v1", Workspace: "C:\\编造", Files: []api.CheckpointFile{{Path: "a.txt", TargetExists: true, TargetHash: "h", TargetBytes: 3}}}, nil
}

func (f *fakeFull) RestoreCheckpoint(_ context.Context, k api.ConversationKey, id, mode, version string) (api.CheckpointRestore, error) {
	if err := f.rec("RestoreCheckpoint"); err != nil {
		return api.CheckpointRestore{}, err
	}
	return api.CheckpointRestore{TargetID: id, InverseID: "inv", State: "done", Completed: []string{"a.txt"}}, nil
}

func (f *fakeFull) ReadCheckpointProgress(_ context.Context, k api.ConversationKey, id string) (api.CheckpointProgress, error) {
	if err := f.rec("ReadCheckpointProgress"); err != nil {
		return api.CheckpointProgress{}, err
	}
	return api.CheckpointProgress{TargetID: id, State: "running", Done: 1, Total: 2, Remaining: []string{"b.txt"}}, nil
}

func (f *fakeFull) WatchCheckpointProgress(_ context.Context, k api.ConversationKey, id string) (api.Subscription[api.CheckpointProgress], error) {
	if err := f.rec("WatchCheckpointProgress"); err != nil {
		return nil, err
	}
	return newFakeSub(api.CheckpointProgress{TargetID: id, State: "running", Done: 1, Total: 2}), nil
}

func (f *fakeFull) CancelCheckpointRestore(_ context.Context, k api.ConversationKey, id string) (api.CheckpointProgress, error) {
	if err := f.rec("CancelCheckpointRestore"); err != nil {
		return api.CheckpointProgress{}, err
	}
	return api.CheckpointProgress{TargetID: id, State: "cancelled"}, nil
}

func fakeStatus() api.LocalStatus {
	return api.LocalStatus{
		Key: testKey, GoalLine: "目标：编的 · 第 3 轮 · 进行中", GoalState: "active", AutoRunning: true, Loops: []string{"#1 每 30 分钟 · 看看 · 已跑 2 次"},
		Goal: &api.GoalInfo{Text: "编的目标", State: "active", Rounds: 2, MaxRounds: 30, TokensUsed: 900, MaxTokens: 5000, StartedAt: 11, LastReason: "还差一点", LastAchieved: false, LastFailed: true, LastAt: 12},
		LoopTasks: []api.LoopInfo{{ID: 1, Prompt: "看看", Status: "running", IntervalSec: 1800, NextRun: 13, NextReason: "盯紧", Runs: 2, LastRun: 14, LastResult: "content", LastNote: "错过了 1 次", Expires: 15,
			Kind: "weekly", At: "09:00", Days: []int{1, 3}, Notify: "errors", NewChat: true, History: []api.LoopRun{{At: 16, Result: "quiet", Note: "回答为空", Missed: 2}}}},
		Reminders: []api.ReminderInfo{{ID: 2, Character: "a", Text: "开会", At: 17, NextAt: 18, Status: "active", Source: "owner", Repeat: api.ReminderRepeat{Unit: "week", Every: 2, Weekdays: []int{1, 5}}, RepeatText: "每 2 周一五",
			Targets: []string{"local"}, EffectiveTargets: []string{"local"}, Rings: 3, LastRing: 19, LastNote: "到本地", EndNote: "", EndedAt: 0, Created: 20, History: []api.ReminderEvent{{At: 21, Kind: "ring", By: "chat", Note: "晚了"}}}},
	}
}

func (f *fakeFull) ReadLocalStatus(_ context.Context, k api.ConversationKey) (api.LocalStatus, error) {
	if err := f.rec("ReadLocalStatus:" + k.Conversation); err != nil {
		return api.LocalStatus{}, err
	}
	return fakeStatus(), nil
}

func (f *fakeFull) WatchLocalStatus(context.Context) (api.Subscription[[]api.LocalStatus], error) {
	if err := f.rec("WatchLocalStatus"); err != nil {
		return nil, err
	}
	return newFakeSub([]api.LocalStatus{fakeStatus()}), nil
}

func (f *fakeFull) AddLoop(_ context.Context, k api.ConversationKey, sp api.LoopSpec) (int, error) {
	if err := f.rec(fmt.Sprintf("AddLoop:%s:%s:%d:%s:%v:%s:%v", sp.Prompt, sp.Kind, sp.IntervalSec, sp.At, sp.Days, sp.Notify, sp.NewChat)); err != nil {
		return 0, err
	}
	return 5, nil
}

func (f *fakeFull) EditLoop(_ context.Context, k api.ConversationKey, id int, sp api.LoopSpec) error {
	return f.rec(fmt.Sprintf("EditLoop:%d:%s:%s", id, sp.Prompt, sp.Kind))
}

func (f *fakeFull) PauseLoop(_ context.Context, k api.ConversationKey, id int) error {
	return f.rec(fmt.Sprintf("PauseLoop:%d", id))
}

func (f *fakeFull) ResumeLoop(_ context.Context, k api.ConversationKey, id int) error {
	return f.rec(fmt.Sprintf("ResumeLoop:%d", id))
}

func (f *fakeFull) RemoveLoop(_ context.Context, k api.ConversationKey, id int) error {
	return f.rec(fmt.Sprintf("RemoveLoop:%d", id))
}

func (f *fakeFull) ReminderOptions() api.ReminderOptions {
	_ = f.rec("ReminderOptions")
	return api.ReminderOptions{Enabled: true, AllowQQ: false, DefaultTargets: []string{"local"}}
}

func (f *fakeFull) CreateReminder(_ context.Context, k api.ConversationKey, d api.ReminderDraft) error {
	return f.rec(fmt.Sprintf("CreateReminder:%s:%s:%v:%v", d.Text, d.At, d.Repeat, d.Targets))
}

// 改哪几项靠 Set 带过去：指针非空才算改（包括改成空数组、改成 false）
func (f *fakeFull) UpdateReminder(_ context.Context, k api.ConversationKey, id int, p api.ReminderPatch) error {
	var set []string
	if p.Text != nil {
		set = append(set, "text="+*p.Text)
	}
	if p.At != nil {
		set = append(set, "at="+*p.At)
	}
	if p.Repeat != nil {
		set = append(set, fmt.Sprintf("repeat=%v", *p.Repeat))
	}
	if p.Targets != nil {
		set = append(set, fmt.Sprintf("targets=%v/%d", *p.Targets, len(*p.Targets)))
	}
	if p.Paused != nil {
		set = append(set, fmt.Sprintf("paused=%v", *p.Paused))
	}
	return f.rec(fmt.Sprintf("UpdateReminder:%d:%v", id, set))
}

func (f *fakeFull) FinishReminder(_ context.Context, k api.ConversationKey, id int, done bool) error {
	return f.rec(fmt.Sprintf("FinishReminder:%d:%v", id, done))
}

func (f *fakeFull) DeleteReminder(_ context.Context, k api.ConversationKey, id int) error {
	return f.rec(fmt.Sprintf("DeleteReminder:%d", id))
}
