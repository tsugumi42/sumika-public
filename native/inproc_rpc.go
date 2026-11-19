package main

// 主窗口默认经过协议客户端（规格-Bun后台第2批 第四节）：后台还是同一个 exe 里的 Go，
// 界面拿到的 Reader 是 rpcBridge——Reader 的 8 个方法和第 2 批的 35 个方法走进程内管道上的协议客户端，
// 其余没上协议的方法靠内嵌的 *DesktopReader 原样直接调。
// 连接走 rpcserve.Pipe：不开端口、不写 run 文件、没有令牌，权限边界和直接调一样，只多一层编解码。
// 退路：-inproc-rpc=false 回到直接把 *DesktopReader 交给界面。
//
// 方法集是静态的：*DesktopReader 有的接口，桥全都有（内嵌提升），界面的类型断言一个不少。
// 下面 63 个方法必须每一个都自己声明（不能靠内嵌提升漏过去直接调）；inproc_rpc_test.go 用源码和真实流量两路核对。

import (
	"context"
	"sync"
	"time"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/rpcclient"
	"sumika-native/internal/rpcserve"
)

type rpcBridge struct {
	*backend.DesktopReader
	c *rpcclient.Full // 到 Go（进程内管道）

	closeOnce sync.Once
	closeFn   func()
}

// newRPCBridge 建一对管道：一头起协议服务端（开写方法）服务 desk，另一头是客户端。wrap 只给测试：在客户端那头套一层计数的传输。
func newRPCBridge(ctx context.Context, desk *backend.DesktopReader, wrap func(rpcclient.Transport) rpcclient.Transport) (*rpcBridge, error) {
	st, ct := rpcserve.Pipe()
	var client rpcclient.Transport = ct
	if wrap != nil {
		client = wrap(ct)
	}
	sctx, cancel := context.WithCancel(ctx)
	srv := rpcserve.New(desk, buildVersion(), rpcserve.WithWrites())
	served := make(chan struct{})
	go func() {
		defer close(served)
		srv.Serve(sctx, st)
	}()
	hctx, hcancel := context.WithTimeout(ctx, 10*time.Second)
	defer hcancel()
	c, err := rpcclient.New(hctx, client, "sumika-desktop-inproc")
	if err != nil {
		cancel()
		ct.Close()
		<-served
		return nil, err
	}
	b := &rpcBridge{DesktopReader: desk, c: rpcclient.NewFull(c)}
	b.closeFn = func() {
		c.Close() // 断管道：服务端的 Serve 读到断开返回，收掉全部订阅
		cancel()
		<-served
	}
	return b, nil
}

// Close 断开管道、等服务端收完；可以多次调
func (b *rpcBridge) Close() { b.closeOnce.Do(b.closeFn) }

// ───── 手写的几个：不按“方法 → 协议方法”一对一翻译的（没有 ctx、订阅）─────

func (b *rpcBridge) MarkRead(ctx context.Context, k api.ConversationKey, upTo int64) error {
	return b.c.MarkRead(ctx, k, upTo)
}

func (b *rpcBridge) WatchLocalApprovals(ctx context.Context) (api.Subscription[[]api.LocalApproval], error) {
	return b.c.WatchLocalApprovals(ctx)
}

func (b *rpcBridge) WatchLocalQuestions(ctx context.Context) (api.Subscription[[]api.LocalQuestion], error) {
	return b.c.WatchLocalQuestions(ctx)
}

func (b *rpcBridge) WatchLocalStatus(ctx context.Context) (api.Subscription[[]api.LocalStatus], error) {
	return b.c.WatchLocalStatus(ctx)
}

func (b *rpcBridge) ReminderOptions() api.ReminderOptions { return b.c.ReminderOptions() }

// ───── 其余的方法：原样交给进程内管道上的协议客户端 ─────

func (b *rpcBridge) WatchOverview(ctx context.Context, r api.OverviewRequest) (api.Subscription[api.Overview], error) {
	return b.c.WatchOverview(ctx, r)
}

func (b *rpcBridge) ReadMessages(ctx context.Context, r api.ConversationRequest, cur api.Cursor) (api.Result[api.MessagePage], error) {
	return b.c.ReadMessages(ctx, r, cur)
}

func (b *rpcBridge) WatchConversation(ctx context.Context, r api.ConversationRequest) (api.Subscription[api.ConversationSnapshot], error) {
	return b.c.WatchConversation(ctx, r)
}

func (b *rpcBridge) ReadMedia(ctx context.Context, ref api.MediaRef, tier api.MediaTier) (api.Result[api.MediaView], error) {
	return b.c.ReadMedia(ctx, ref, tier)
}

func (b *rpcBridge) ReadFaceIndex(ctx context.Context) (api.Result[api.List[api.FaceView]], error) {
	return b.c.ReadFaceIndex(ctx)
}

func (b *rpcBridge) ReadFacePNG(ctx context.Context, id string) (api.Result[api.MediaView], error) {
	return b.c.ReadFacePNG(ctx, id)
}

func (b *rpcBridge) ReadSettings(ctx context.Context) (api.Result[api.Settings], error) {
	return b.c.ReadSettings(ctx)
}

func (b *rpcBridge) WatchRuntime(ctx context.Context) (api.Subscription[api.RuntimeView], error) {
	return b.c.WatchRuntime(ctx)
}

func (b *rpcBridge) ReadAvatar(ctx context.Context, key string) (api.Result[api.MediaView], error) {
	return b.c.ReadAvatar(ctx, key)
}

func (b *rpcBridge) ReadForward(ctx context.Context, req api.ForwardRequest) (api.Result[api.ForwardView], error) {
	return b.c.ReadForward(ctx, req)
}

func (b *rpcBridge) PrepareVideo(ctx context.Context, ref api.MediaRef) (string, error) {
	return b.c.PrepareVideo(ctx, ref)
}

func (b *rpcBridge) SendLocal(ctx context.Context, k api.ConversationKey, text string, draft func(api.LocalDraft)) error {
	return b.c.SendLocal(ctx, k, text, draft)
}

func (b *rpcBridge) StopLocal(ctx context.Context, k api.ConversationKey) error {
	return b.c.StopLocal(ctx, k)
}

func (b *rpcBridge) DecideLocal(ctx context.Context, k api.ConversationKey, id string, choice api.ApprovalChoice) error {
	return b.c.DecideLocal(ctx, k, id, choice)
}

func (b *rpcBridge) SendLocalImages(ctx context.Context, k api.ConversationKey, text string, images [][]byte, draft func(api.LocalDraft)) error {
	return b.c.SendLocalImages(ctx, k, text, images, draft)
}

func (b *rpcBridge) SaveLocalImage(ctx context.Context, k api.ConversationKey, png []byte) (api.LocalAttachment, error) {
	return b.c.SaveLocalImage(ctx, k, png)
}

func (b *rpcBridge) SendLocalParts(ctx context.Context, k api.ConversationKey, parts []api.LocalPart, draft func(api.LocalDraft)) error {
	return b.c.SendLocalParts(ctx, k, parts, draft)
}

func (b *rpcBridge) SendWhisper(ctx context.Context, k api.ConversationKey, parts []api.LocalPart) (api.WhisperReceipt, error) {
	return b.c.SendWhisper(ctx, k, parts)
}

func (b *rpcBridge) ReadWhisperLog(ctx context.Context, k api.ConversationKey, cursor string, limit int) (ctlproto.LogPage, error) {
	return b.c.ReadWhisperLog(ctx, k, cursor, limit)
}

func (b *rpcBridge) ReadWhisperLogEntry(ctx context.Context, k api.ConversationKey, id string, withThinking bool) (ctlproto.LogDetail, error) {
	return b.c.ReadWhisperLogEntry(ctx, k, id, withThinking)
}

func (b *rpcBridge) WatchWhisperLog(ctx context.Context, k api.ConversationKey) (api.Subscription[ctlproto.LogPage], error) {
	return b.c.WatchWhisperLog(ctx, k)
}

func (b *rpcBridge) CancelWhisper(ctx context.Context, k api.ConversationKey, batch, character string) (int, error) {
	return b.c.CancelWhisper(ctx, k, batch, character)
}

func (b *rpcBridge) ReadLocalStatus(ctx context.Context, k api.ConversationKey) (api.LocalStatus, error) {
	return b.c.ReadLocalStatus(ctx, k)
}

func (b *rpcBridge) AddLoop(ctx context.Context, k api.ConversationKey, sp api.LoopSpec) (int, error) {
	return b.c.AddLoop(ctx, k, sp)
}

func (b *rpcBridge) EditLoop(ctx context.Context, k api.ConversationKey, id int, sp api.LoopSpec) error {
	return b.c.EditLoop(ctx, k, id, sp)
}

func (b *rpcBridge) PauseLoop(ctx context.Context, k api.ConversationKey, id int) error {
	return b.c.PauseLoop(ctx, k, id)
}

func (b *rpcBridge) ResumeLoop(ctx context.Context, k api.ConversationKey, id int) error {
	return b.c.ResumeLoop(ctx, k, id)
}

func (b *rpcBridge) RemoveLoop(ctx context.Context, k api.ConversationKey, id int) error {
	return b.c.RemoveLoop(ctx, k, id)
}

func (b *rpcBridge) CreateReminder(ctx context.Context, k api.ConversationKey, d api.ReminderDraft) error {
	return b.c.CreateReminder(ctx, k, d)
}

func (b *rpcBridge) UpdateReminder(ctx context.Context, k api.ConversationKey, id int, p api.ReminderPatch) error {
	return b.c.UpdateReminder(ctx, k, id, p)
}

func (b *rpcBridge) FinishReminder(ctx context.Context, k api.ConversationKey, id int, done bool) error {
	return b.c.FinishReminder(ctx, k, id, done)
}

func (b *rpcBridge) DeleteReminder(ctx context.Context, k api.ConversationKey, id int) error {
	return b.c.DeleteReminder(ctx, k, id)
}

func (b *rpcBridge) CreateLocalGroup(ctx context.Context, req api.LocalGroupCreate) (api.ConversationView, error) {
	return b.c.CreateLocalGroup(ctx, req)
}

func (b *rpcBridge) SendLocalGroup(ctx context.Context, req api.LocalGroupSend) error {
	return b.c.SendLocalGroup(ctx, req)
}

func (b *rpcBridge) StopLocalGroup(ctx context.Context, k api.ConversationKey) error {
	return b.c.StopLocalGroup(ctx, k)
}

func (b *rpcBridge) SubmitLocal(ctx context.Context, k api.ConversationKey, in api.LocalInputSubmission) (api.InputReceipt, error) {
	return b.c.SubmitLocal(ctx, k, in)
}

func (b *rpcBridge) StopLocalRun(ctx context.Context, k api.ConversationKey, runID string, through uint64) (api.StopReceipt, error) {
	return b.c.StopLocalRun(ctx, k, runID, through)
}

func (b *rpcBridge) WatchLocalRun(ctx context.Context, k api.ConversationKey, runID string) (api.Subscription[api.LocalRunSnapshot], error) {
	return b.c.WatchLocalRun(ctx, k, runID)
}

func (b *rpcBridge) HeldLocal(ctx context.Context) ([]api.HeldInput, error) {
	return b.c.HeldLocal(ctx)
}

func (b *rpcBridge) ReleaseHeldLocal(ctx context.Context, k api.ConversationKey, inputID string) (api.InputReceipt, error) {
	return b.c.ReleaseHeldLocal(ctx, k, inputID)
}

func (b *rpcBridge) DiscardHeldLocal(ctx context.Context, k api.ConversationKey, inputID string) error {
	return b.c.DiscardHeldLocal(ctx, k, inputID)
}

func (b *rpcBridge) WithdrawQueuedLocal(ctx context.Context, k api.ConversationKey, inputID string) error {
	return b.c.WithdrawQueuedLocal(ctx, k, inputID)
}

func (b *rpcBridge) AnswerLocal(ctx context.Context, k api.ConversationKey, id string, option int) error {
	return b.c.AnswerLocal(ctx, k, id, option)
}

func (b *rpcBridge) ListApprovalRules(ctx context.Context) ([]api.ApprovalRule, error) {
	return b.c.ListApprovalRules(ctx)
}

func (b *rpcBridge) RevokeApprovalRule(ctx context.Context, r api.ApprovalRule) error {
	return b.c.RevokeApprovalRule(ctx, r)
}

func (b *rpcBridge) AddApprovalRule(ctx context.Context, raw, effect, project, session string) error {
	return b.c.AddApprovalRule(ctx, raw, effect, project, session)
}

func (b *rpcBridge) ConversationApprovalMode(ctx context.Context, k api.ConversationKey) (api.ConversationApprovalMode, error) {
	return b.c.ConversationApprovalMode(ctx, k)
}

func (b *rpcBridge) SetConversationApprovalMode(ctx context.Context, k api.ConversationKey, mode string) error {
	return b.c.SetConversationApprovalMode(ctx, k, mode)
}

func (b *rpcBridge) ReadLocalWorkspace(ctx context.Context, k api.ConversationKey) (api.WorkspaceInfo, error) {
	return b.c.ReadLocalWorkspace(ctx, k)
}

func (b *rpcBridge) SetLocalWorkspace(ctx context.Context, k api.ConversationKey, dir string) (api.WorkspaceInfo, error) {
	return b.c.SetLocalWorkspace(ctx, k, dir)
}

func (b *rpcBridge) ClearLocalWorkspace(ctx context.Context, k api.ConversationKey) (api.WorkspaceInfo, error) {
	return b.c.ClearLocalWorkspace(ctx, k)
}

func (b *rpcBridge) RecentWorkspaces(ctx context.Context) ([]string, error) {
	return b.c.RecentWorkspaces(ctx)
}

func (b *rpcBridge) ListCheckpoints(ctx context.Context, k api.ConversationKey) ([]api.CheckpointSummary, error) {
	return b.c.ListCheckpoints(ctx, k)
}

func (b *rpcBridge) PreviewCheckpoint(ctx context.Context, k api.ConversationKey, id, mode string) (api.CheckpointPlan, error) {
	return b.c.PreviewCheckpoint(ctx, k, id, mode)
}

func (b *rpcBridge) RestoreCheckpoint(ctx context.Context, k api.ConversationKey, id, mode, version string) (api.CheckpointRestore, error) {
	return b.c.RestoreCheckpoint(ctx, k, id, mode, version)
}

func (b *rpcBridge) ReadCheckpointProgress(ctx context.Context, k api.ConversationKey, targetID string) (api.CheckpointProgress, error) {
	return b.c.ReadCheckpointProgress(ctx, k, targetID)
}

func (b *rpcBridge) WatchCheckpointProgress(ctx context.Context, k api.ConversationKey, targetID string) (api.Subscription[api.CheckpointProgress], error) {
	return b.c.WatchCheckpointProgress(ctx, k, targetID)
}

func (b *rpcBridge) CancelCheckpointRestore(ctx context.Context, k api.ConversationKey, targetID string) (api.CheckpointProgress, error) {
	return b.c.CancelCheckpointRestore(ctx, k, targetID)
}

var (
	_ api.Reader               = (*rpcBridge)(nil)
	_ api.AvatarReader         = (*rpcBridge)(nil)
	_ api.ForwardReader        = (*rpcBridge)(nil)
	_ api.ReadMarker           = (*rpcBridge)(nil)
	_ api.VideoReader          = (*rpcBridge)(nil)
	_ api.LocalChat            = (*rpcBridge)(nil)
	_ api.LocalImageChat       = (*rpcBridge)(nil)
	_ api.LocalImageSaver      = (*rpcBridge)(nil)
	_ api.LocalPartsChat       = (*rpcBridge)(nil)
	_ api.LocalGroupWriter     = (*rpcBridge)(nil)
	_ api.WhisperChat          = (*rpcBridge)(nil)
	_ api.WhisperLogReader     = (*rpcBridge)(nil)
	_ api.LocalStatusReader    = (*rpcBridge)(nil)
	_ api.LoopControl          = (*rpcBridge)(nil)
	_ api.ReminderControl      = (*rpcBridge)(nil)
	_ api.LocalInputs          = (*rpcBridge)(nil)
	_ api.LocalHeld            = (*rpcBridge)(nil)
	_ api.LocalQuestions       = (*rpcBridge)(nil)
	_ api.ApprovalRules        = (*rpcBridge)(nil)
	_ api.ApprovalRuleEditor   = (*rpcBridge)(nil)
	_ api.ConversationApproval = (*rpcBridge)(nil)
	_ api.LocalWorkspaces      = (*rpcBridge)(nil)
	_ api.CheckpointActions    = (*rpcBridge)(nil)
	_ api.SecretsAdmin         = (*rpcBridge)(nil) // 没上协议：内嵌 DesktopReader 原样提升，和 SettingsRegistry 一样
)
