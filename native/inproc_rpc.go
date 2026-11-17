package main

// 主窗口默认经过协议客户端（规格-Bun后台第2批 第四节）：后台还是同一个 exe 里的 Go，
// 界面拿到的 Reader 是 rpcBridge——Reader 的 8 个方法和第 2 批的 35 个方法走进程内管道上的协议客户端，
// 其余没上协议的方法靠内嵌的 *DesktopReader 原样直接调。
// 连接走 rpcserve.Pipe：不开端口、不写 run 文件、没有令牌，权限边界和直接调一样，只多一层编解码。
// 退路：-inproc-rpc=false 回到直接把 *DesktopReader 交给界面。
//
// 本地私聊交给 Bun 时（配置 backend.local 是 bun，规格-本地私聊切到Bun 第四节）桥多持一个客户端：到 Bun 子进程的连接（bunLink）。
// 每个方法归哪一边看 bunroutes.go 的表：会写本地数据的走 Bun，读的走 Go，QQ 全走 Go。Bun 不在时走 Bun 的方法回
// api.ErrLocalBackendDown，不退回 Go；没上协议、直接调内嵌 DesktopReader 的方法，会写本地的在 Go 那边就回 ErrLocalOwnedByBun
// （internal/app 的 bunOwned），所以这条路也写不到 local.db。go 模式（默认）bun 为空，所有方法照旧走 Go。
//
// 方法集是静态的：*DesktopReader 有的接口，桥全都有（内嵌提升），界面的类型断言一个不少。
// 下面 63 个方法必须每一个都自己声明（不能靠内嵌提升漏过去直接调）；inproc_rpc_test.go 用源码和真实流量两路核对。

import (
	"context"
	"fmt"
	"sync"
	"time"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/rpcclient"
	"sumika-native/internal/rpcserve"
	"sumika-native/internal/rpcwire"
)

type rpcBridge struct {
	*backend.DesktopReader
	c   *rpcclient.Full // 到 Go（进程内管道）
	bun *bunLink        // 到 Bun；go 模式是 nil

	closeOnce sync.Once
	closeFn   func()
}

// newRPCBridge 建一对管道：一头起协议服务端（开写方法）服务 desk，另一头是客户端。wrap 只给测试：在客户端那头套一层计数的传输。
// bun 不是 nil 时按 bunroutes.go 把写本地数据的方法分给 Bun；关桥不关 bun（它属于宿主）
func newRPCBridge(ctx context.Context, desk *backend.DesktopReader, wrap func(rpcclient.Transport) rpcclient.Transport, bun *bunLink) (*rpcBridge, error) {
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
	b := &rpcBridge{DesktopReader: desk, c: rpcclient.NewFull(c), bun: bun}
	b.closeFn = func() {
		c.Close() // 断管道：服务端的 Serve 读到断开返回，收掉全部订阅
		cancel()
		<-served
	}
	return b, nil
}

// Close 断开管道、等服务端收完；可以多次调
func (b *rpcBridge) Close() { b.closeOnce.Do(b.closeFn) }

// via 是这个协议方法该用的客户端。go 模式永远是 Go；bun 模式查 bunroutes.go：没归类的不放行，走 Bun 的在 Bun 不在时回 ErrLocalBackendDown
func (b *rpcBridge) via(method string) (*rpcclient.Full, error) {
	if b.bun == nil {
		return b.c, nil
	}
	switch routes[method] {
	case routeGo:
		return b.c, nil
	case routeBun:
		return b.bun.get()
	}
	return nil, fmt.Errorf("桥的方法表里没有 %s：不知道该走哪个后台，没有放行", method)
}

// ───── 手写的几个：按会话键分流、合并、跟着连接重订阅、没有 ctx ─────

// MarkRead：本地会话的已读标记写 local.db，bun 模式归 Bun；别的会话（QQ）走 Go
func (b *rpcBridge) MarkRead(ctx context.Context, k api.ConversationKey, upTo int64) error {
	c := b.c
	if b.bun != nil && k.Platform == "local" {
		var err error
		if c, err = b.bun.get(); err != nil {
			return err
		}
	}
	return c.MarkRead(ctx, k, upTo)
}

// WatchLocalApprovals：审批卡在 bun 模式下全在 Bun 那边；Bun 重启后订阅自己重订，不让界面卡在旧的上
func (b *rpcBridge) WatchLocalApprovals(ctx context.Context) (api.Subscription[[]api.LocalApproval], error) {
	if b.bun == nil {
		return b.c.WatchLocalApprovals(ctx)
	}
	return watchBun(ctx, b.bun, []api.LocalApproval(nil), func(ctx context.Context, f *rpcclient.Full) (api.Subscription[[]api.LocalApproval], error) {
		return f.WatchLocalApprovals(ctx)
	}), nil
}

// WatchLocalQuestions：同上
func (b *rpcBridge) WatchLocalQuestions(ctx context.Context) (api.Subscription[[]api.LocalQuestion], error) {
	if b.bun == nil {
		return b.c.WatchLocalQuestions(ctx)
	}
	return watchBun(ctx, b.bun, []api.LocalQuestion(nil), func(ctx context.Context, f *rpcclient.Full) (api.Subscription[[]api.LocalQuestion], error) {
		return f.WatchLocalQuestions(ctx)
	}), nil
}

// WatchLocalStatus：bun 模式下两边的状态拼在一起——Bun 管本地私聊的目标、定期任务、提醒和自动轮，Go 管 QQ 的“正在输入”
func (b *rpcBridge) WatchLocalStatus(ctx context.Context) (api.Subscription[[]api.LocalStatus], error) {
	goSub, err := b.c.WatchLocalStatus(ctx)
	if b.bun == nil || err != nil {
		return goSub, err
	}
	bunSub := watchBun(ctx, b.bun, []api.LocalStatus(nil), func(ctx context.Context, f *rpcclient.Full) (api.Subscription[[]api.LocalStatus], error) {
		return f.WatchLocalStatus(ctx)
	})
	return mergeSubs(ctx, goSub, bunSub), nil
}

// ReminderOptions 没有 ctx 也没有错误：Bun 不在就当提醒这一块关着
func (b *rpcBridge) ReminderOptions() api.ReminderOptions {
	c, err := b.via(rpcwire.MethodReminderOptions)
	if err != nil {
		return api.ReminderOptions{}
	}
	return c.ReminderOptions()
}

// ───── 其余的方法：按 bunroutes.go 选客户端，选不出来（Bun 不在、没归类）直接回错 ─────

func (b *rpcBridge) WatchOverview(ctx context.Context, r api.OverviewRequest) (api.Subscription[api.Overview], error) {
	c, err := b.via(rpcwire.MethodOverviewSubscribe)
	if err != nil {
		var zero api.Subscription[api.Overview]
		return zero, err
	}
	return c.WatchOverview(ctx, r)
}

func (b *rpcBridge) ReadMessages(ctx context.Context, r api.ConversationRequest, cur api.Cursor) (api.Result[api.MessagePage], error) {
	c, err := b.via(rpcwire.MethodMessagesRead)
	if err != nil {
		var zero api.Result[api.MessagePage]
		return zero, err
	}
	return c.ReadMessages(ctx, r, cur)
}

func (b *rpcBridge) WatchConversation(ctx context.Context, r api.ConversationRequest) (api.Subscription[api.ConversationSnapshot], error) {
	c, err := b.via(rpcwire.MethodConversationSubscribe)
	if err != nil {
		var zero api.Subscription[api.ConversationSnapshot]
		return zero, err
	}
	return c.WatchConversation(ctx, r)
}

func (b *rpcBridge) ReadMedia(ctx context.Context, ref api.MediaRef, tier api.MediaTier) (api.Result[api.MediaView], error) {
	c, err := b.via(rpcwire.MethodMediaRead)
	if err != nil {
		var zero api.Result[api.MediaView]
		return zero, err
	}
	return c.ReadMedia(ctx, ref, tier)
}

func (b *rpcBridge) ReadFaceIndex(ctx context.Context) (api.Result[api.List[api.FaceView]], error) {
	c, err := b.via(rpcwire.MethodFacesIndex)
	if err != nil {
		var zero api.Result[api.List[api.FaceView]]
		return zero, err
	}
	return c.ReadFaceIndex(ctx)
}

func (b *rpcBridge) ReadFacePNG(ctx context.Context, id string) (api.Result[api.MediaView], error) {
	c, err := b.via(rpcwire.MethodFacesPNG)
	if err != nil {
		var zero api.Result[api.MediaView]
		return zero, err
	}
	return c.ReadFacePNG(ctx, id)
}

func (b *rpcBridge) ReadSettings(ctx context.Context) (api.Result[api.Settings], error) {
	c, err := b.via(rpcwire.MethodSettingsRead)
	if err != nil {
		var zero api.Result[api.Settings]
		return zero, err
	}
	return c.ReadSettings(ctx)
}

func (b *rpcBridge) WatchRuntime(ctx context.Context) (api.Subscription[api.RuntimeView], error) {
	c, err := b.via(rpcwire.MethodRuntimeSubscribe)
	if err != nil {
		var zero api.Subscription[api.RuntimeView]
		return zero, err
	}
	return c.WatchRuntime(ctx)
}

func (b *rpcBridge) ReadAvatar(ctx context.Context, key string) (api.Result[api.MediaView], error) {
	c, err := b.via(rpcwire.MethodAvatarRead)
	if err != nil {
		var zero api.Result[api.MediaView]
		return zero, err
	}
	return c.ReadAvatar(ctx, key)
}

func (b *rpcBridge) ReadForward(ctx context.Context, req api.ForwardRequest) (api.Result[api.ForwardView], error) {
	c, err := b.via(rpcwire.MethodForwardRead)
	if err != nil {
		var zero api.Result[api.ForwardView]
		return zero, err
	}
	return c.ReadForward(ctx, req)
}

func (b *rpcBridge) PrepareVideo(ctx context.Context, ref api.MediaRef) (string, error) {
	c, err := b.via(rpcwire.MethodVideoPrepare)
	if err != nil {
		var zero string
		return zero, err
	}
	return c.PrepareVideo(ctx, ref)
}

func (b *rpcBridge) SendLocal(ctx context.Context, k api.ConversationKey, text string, draft func(api.LocalDraft)) error {
	c, err := b.via(rpcwire.MethodLocalSend)
	if err != nil {
		return err
	}
	return c.SendLocal(ctx, k, text, draft)
}

func (b *rpcBridge) StopLocal(ctx context.Context, k api.ConversationKey) error {
	c, err := b.via(rpcwire.MethodLocalStop)
	if err != nil {
		return err
	}
	return c.StopLocal(ctx, k)
}

func (b *rpcBridge) DecideLocal(ctx context.Context, k api.ConversationKey, id string, choice api.ApprovalChoice) error {
	c, err := b.via(rpcwire.MethodLocalDecide)
	if err != nil {
		return err
	}
	return c.DecideLocal(ctx, k, id, choice)
}

func (b *rpcBridge) SendLocalImages(ctx context.Context, k api.ConversationKey, text string, images [][]byte, draft func(api.LocalDraft)) error {
	c, err := b.via(rpcwire.MethodLocalSendImages)
	if err != nil {
		return err
	}
	return c.SendLocalImages(ctx, k, text, images, draft)
}

func (b *rpcBridge) SaveLocalImage(ctx context.Context, k api.ConversationKey, png []byte) (api.LocalAttachment, error) {
	c, err := b.via(rpcwire.MethodLocalSaveImage)
	if err != nil {
		var zero api.LocalAttachment
		return zero, err
	}
	return c.SaveLocalImage(ctx, k, png)
}

func (b *rpcBridge) SendLocalParts(ctx context.Context, k api.ConversationKey, parts []api.LocalPart, draft func(api.LocalDraft)) error {
	c, err := b.via(rpcwire.MethodLocalSendParts)
	if err != nil {
		return err
	}
	return c.SendLocalParts(ctx, k, parts, draft)
}

func (b *rpcBridge) SendWhisper(ctx context.Context, k api.ConversationKey, parts []api.LocalPart) (api.WhisperReceipt, error) {
	c, err := b.via(rpcwire.MethodWhisperSend)
	if err != nil {
		var zero api.WhisperReceipt
		return zero, err
	}
	return c.SendWhisper(ctx, k, parts)
}

func (b *rpcBridge) ReadWhisperLog(ctx context.Context, k api.ConversationKey, cursor string, limit int) (ctlproto.LogPage, error) {
	c, err := b.via(rpcwire.MethodWhisperLogRead)
	if err != nil {
		var zero ctlproto.LogPage
		return zero, err
	}
	return c.ReadWhisperLog(ctx, k, cursor, limit)
}

func (b *rpcBridge) ReadWhisperLogEntry(ctx context.Context, k api.ConversationKey, id string, withThinking bool) (ctlproto.LogDetail, error) {
	c, err := b.via(rpcwire.MethodWhisperLogEntry)
	if err != nil {
		var zero ctlproto.LogDetail
		return zero, err
	}
	return c.ReadWhisperLogEntry(ctx, k, id, withThinking)
}

func (b *rpcBridge) WatchWhisperLog(ctx context.Context, k api.ConversationKey) (api.Subscription[ctlproto.LogPage], error) {
	c, err := b.via(rpcwire.MethodWhisperLogSubscribe)
	if err != nil {
		var zero api.Subscription[ctlproto.LogPage]
		return zero, err
	}
	return c.WatchWhisperLog(ctx, k)
}

func (b *rpcBridge) CancelWhisper(ctx context.Context, k api.ConversationKey, batch, character string) (int, error) {
	c, err := b.via(rpcwire.MethodWhisperLogCancel)
	if err != nil {
		var zero int
		return zero, err
	}
	return c.CancelWhisper(ctx, k, batch, character)
}

func (b *rpcBridge) ReadLocalStatus(ctx context.Context, k api.ConversationKey) (api.LocalStatus, error) {
	c, err := b.via(rpcwire.MethodLocalStatusRead)
	if err != nil {
		var zero api.LocalStatus
		return zero, err
	}
	return c.ReadLocalStatus(ctx, k)
}

func (b *rpcBridge) AddLoop(ctx context.Context, k api.ConversationKey, sp api.LoopSpec) (int, error) {
	c, err := b.via(rpcwire.MethodLoopAdd)
	if err != nil {
		var zero int
		return zero, err
	}
	return c.AddLoop(ctx, k, sp)
}

func (b *rpcBridge) EditLoop(ctx context.Context, k api.ConversationKey, id int, sp api.LoopSpec) error {
	c, err := b.via(rpcwire.MethodLoopEdit)
	if err != nil {
		return err
	}
	return c.EditLoop(ctx, k, id, sp)
}

func (b *rpcBridge) PauseLoop(ctx context.Context, k api.ConversationKey, id int) error {
	c, err := b.via(rpcwire.MethodLoopPause)
	if err != nil {
		return err
	}
	return c.PauseLoop(ctx, k, id)
}

func (b *rpcBridge) ResumeLoop(ctx context.Context, k api.ConversationKey, id int) error {
	c, err := b.via(rpcwire.MethodLoopResume)
	if err != nil {
		return err
	}
	return c.ResumeLoop(ctx, k, id)
}

func (b *rpcBridge) RemoveLoop(ctx context.Context, k api.ConversationKey, id int) error {
	c, err := b.via(rpcwire.MethodLoopRemove)
	if err != nil {
		return err
	}
	return c.RemoveLoop(ctx, k, id)
}

func (b *rpcBridge) CreateReminder(ctx context.Context, k api.ConversationKey, d api.ReminderDraft) error {
	c, err := b.via(rpcwire.MethodReminderCreate)
	if err != nil {
		return err
	}
	return c.CreateReminder(ctx, k, d)
}

func (b *rpcBridge) UpdateReminder(ctx context.Context, k api.ConversationKey, id int, p api.ReminderPatch) error {
	c, err := b.via(rpcwire.MethodReminderUpdate)
	if err != nil {
		return err
	}
	return c.UpdateReminder(ctx, k, id, p)
}

func (b *rpcBridge) FinishReminder(ctx context.Context, k api.ConversationKey, id int, done bool) error {
	c, err := b.via(rpcwire.MethodReminderFinish)
	if err != nil {
		return err
	}
	return c.FinishReminder(ctx, k, id, done)
}

func (b *rpcBridge) DeleteReminder(ctx context.Context, k api.ConversationKey, id int) error {
	c, err := b.via(rpcwire.MethodReminderDelete)
	if err != nil {
		return err
	}
	return c.DeleteReminder(ctx, k, id)
}

func (b *rpcBridge) CreateLocalGroup(ctx context.Context, req api.LocalGroupCreate) (api.ConversationView, error) {
	c, err := b.via(rpcwire.MethodLocalGroupCreate)
	if err != nil {
		var zero api.ConversationView
		return zero, err
	}
	return c.CreateLocalGroup(ctx, req)
}

func (b *rpcBridge) SendLocalGroup(ctx context.Context, req api.LocalGroupSend) error {
	c, err := b.via(rpcwire.MethodLocalGroupSend)
	if err != nil {
		return err
	}
	return c.SendLocalGroup(ctx, req)
}

func (b *rpcBridge) StopLocalGroup(ctx context.Context, k api.ConversationKey) error {
	c, err := b.via(rpcwire.MethodLocalGroupStop)
	if err != nil {
		return err
	}
	return c.StopLocalGroup(ctx, k)
}

func (b *rpcBridge) SubmitLocal(ctx context.Context, k api.ConversationKey, in api.LocalInputSubmission) (api.InputReceipt, error) {
	c, err := b.via(rpcwire.MethodLocalSubmit)
	if err != nil {
		var zero api.InputReceipt
		return zero, err
	}
	return c.SubmitLocal(ctx, k, in)
}

func (b *rpcBridge) StopLocalRun(ctx context.Context, k api.ConversationKey, runID string, through uint64) (api.StopReceipt, error) {
	c, err := b.via(rpcwire.MethodLocalStopRun)
	if err != nil {
		var zero api.StopReceipt
		return zero, err
	}
	return c.StopLocalRun(ctx, k, runID, through)
}

func (b *rpcBridge) WatchLocalRun(ctx context.Context, k api.ConversationKey, runID string) (api.Subscription[api.LocalRunSnapshot], error) {
	c, err := b.via(rpcwire.MethodLocalRunSubscribe)
	if err != nil {
		var zero api.Subscription[api.LocalRunSnapshot]
		return zero, err
	}
	return c.WatchLocalRun(ctx, k, runID)
}

func (b *rpcBridge) HeldLocal(ctx context.Context) ([]api.HeldInput, error) {
	c, err := b.via(rpcwire.MethodHeldList)
	if err != nil {
		var zero []api.HeldInput
		return zero, err
	}
	return c.HeldLocal(ctx)
}

func (b *rpcBridge) ReleaseHeldLocal(ctx context.Context, k api.ConversationKey, inputID string) (api.InputReceipt, error) {
	c, err := b.via(rpcwire.MethodHeldRelease)
	if err != nil {
		var zero api.InputReceipt
		return zero, err
	}
	return c.ReleaseHeldLocal(ctx, k, inputID)
}

func (b *rpcBridge) DiscardHeldLocal(ctx context.Context, k api.ConversationKey, inputID string) error {
	c, err := b.via(rpcwire.MethodHeldDiscard)
	if err != nil {
		return err
	}
	return c.DiscardHeldLocal(ctx, k, inputID)
}

func (b *rpcBridge) WithdrawQueuedLocal(ctx context.Context, k api.ConversationKey, inputID string) error {
	c, err := b.via(rpcwire.MethodLocalWithdraw)
	if err != nil {
		return err
	}
	return c.WithdrawQueuedLocal(ctx, k, inputID)
}

func (b *rpcBridge) AnswerLocal(ctx context.Context, k api.ConversationKey, id string, option int) error {
	c, err := b.via(rpcwire.MethodLocalAnswer)
	if err != nil {
		return err
	}
	return c.AnswerLocal(ctx, k, id, option)
}

func (b *rpcBridge) ListApprovalRules(ctx context.Context) ([]api.ApprovalRule, error) {
	c, err := b.via(rpcwire.MethodApprovalRulesList)
	if err != nil {
		var zero []api.ApprovalRule
		return zero, err
	}
	return c.ListApprovalRules(ctx)
}

func (b *rpcBridge) RevokeApprovalRule(ctx context.Context, r api.ApprovalRule) error {
	c, err := b.via(rpcwire.MethodApprovalRulesRevoke)
	if err != nil {
		return err
	}
	return c.RevokeApprovalRule(ctx, r)
}

func (b *rpcBridge) AddApprovalRule(ctx context.Context, raw, effect, project, session string) error {
	c, err := b.via(rpcwire.MethodApprovalRulesAdd)
	if err != nil {
		return err
	}
	return c.AddApprovalRule(ctx, raw, effect, project, session)
}

func (b *rpcBridge) ConversationApprovalMode(ctx context.Context, k api.ConversationKey) (api.ConversationApprovalMode, error) {
	c, err := b.via(rpcwire.MethodConversationApprovalRead)
	if err != nil {
		var zero api.ConversationApprovalMode
		return zero, err
	}
	return c.ConversationApprovalMode(ctx, k)
}

func (b *rpcBridge) SetConversationApprovalMode(ctx context.Context, k api.ConversationKey, mode string) error {
	c, err := b.via(rpcwire.MethodConversationApprovalSet)
	if err != nil {
		return err
	}
	return c.SetConversationApprovalMode(ctx, k, mode)
}

func (b *rpcBridge) ReadLocalWorkspace(ctx context.Context, k api.ConversationKey) (api.WorkspaceInfo, error) {
	c, err := b.via(rpcwire.MethodWorkspaceRead)
	if err != nil {
		var zero api.WorkspaceInfo
		return zero, err
	}
	return c.ReadLocalWorkspace(ctx, k)
}

func (b *rpcBridge) SetLocalWorkspace(ctx context.Context, k api.ConversationKey, dir string) (api.WorkspaceInfo, error) {
	c, err := b.via(rpcwire.MethodWorkspaceSet)
	if err != nil {
		var zero api.WorkspaceInfo
		return zero, err
	}
	return c.SetLocalWorkspace(ctx, k, dir)
}

func (b *rpcBridge) ClearLocalWorkspace(ctx context.Context, k api.ConversationKey) (api.WorkspaceInfo, error) {
	c, err := b.via(rpcwire.MethodWorkspaceClear)
	if err != nil {
		var zero api.WorkspaceInfo
		return zero, err
	}
	return c.ClearLocalWorkspace(ctx, k)
}

func (b *rpcBridge) RecentWorkspaces(ctx context.Context) ([]string, error) {
	c, err := b.via(rpcwire.MethodWorkspaceRecent)
	if err != nil {
		var zero []string
		return zero, err
	}
	return c.RecentWorkspaces(ctx)
}

func (b *rpcBridge) ListCheckpoints(ctx context.Context, k api.ConversationKey) ([]api.CheckpointSummary, error) {
	c, err := b.via(rpcwire.MethodCheckpointsList)
	if err != nil {
		var zero []api.CheckpointSummary
		return zero, err
	}
	return c.ListCheckpoints(ctx, k)
}

func (b *rpcBridge) PreviewCheckpoint(ctx context.Context, k api.ConversationKey, id, mode string) (api.CheckpointPlan, error) {
	c, err := b.via(rpcwire.MethodCheckpointsPreview)
	if err != nil {
		var zero api.CheckpointPlan
		return zero, err
	}
	return c.PreviewCheckpoint(ctx, k, id, mode)
}

func (b *rpcBridge) RestoreCheckpoint(ctx context.Context, k api.ConversationKey, id, mode, version string) (api.CheckpointRestore, error) {
	c, err := b.via(rpcwire.MethodCheckpointsRestore)
	if err != nil {
		var zero api.CheckpointRestore
		return zero, err
	}
	return c.RestoreCheckpoint(ctx, k, id, mode, version)
}

func (b *rpcBridge) ReadCheckpointProgress(ctx context.Context, k api.ConversationKey, targetID string) (api.CheckpointProgress, error) {
	c, err := b.via(rpcwire.MethodCheckpointsProgress)
	if err != nil {
		var zero api.CheckpointProgress
		return zero, err
	}
	return c.ReadCheckpointProgress(ctx, k, targetID)
}

func (b *rpcBridge) WatchCheckpointProgress(ctx context.Context, k api.ConversationKey, targetID string) (api.Subscription[api.CheckpointProgress], error) {
	c, err := b.via(rpcwire.MethodCheckpointProgressSubscribe)
	if err != nil {
		var zero api.Subscription[api.CheckpointProgress]
		return zero, err
	}
	return c.WatchCheckpointProgress(ctx, k, targetID)
}

func (b *rpcBridge) CancelCheckpointRestore(ctx context.Context, k api.ConversationKey, targetID string) (api.CheckpointProgress, error) {
	c, err := b.via(rpcwire.MethodCheckpointsCancel)
	if err != nil {
		var zero api.CheckpointProgress
		return zero, err
	}
	return c.CancelCheckpointRestore(ctx, k, targetID)
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
