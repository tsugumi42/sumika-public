package rpcclient

// Full 是协议客户端的"全功能"版（规格-Bun后台第2批 4.2）：Reader 的 8 个方法加第 2 批的 35 个，
// 实现 16 个可选接口。后台没登记的方法（initialize 的 methods 里没有）不发请求，按 3.2 回对应接口的"不支持"。

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/rpcconv"
	"sumika-native/internal/rpcwire"
)

type Full struct {
	Reader
}

// NewFull 包一条已握手的连接
func NewFull(c *Client) *Full { return &Full{Reader: Reader{C: c}} }

var (
	_ api.Reader               = (*Full)(nil)
	_ api.AvatarReader         = (*Full)(nil)
	_ api.ForwardReader        = (*Full)(nil)
	_ api.ReadMarker           = (*Full)(nil)
	_ api.VideoReader          = (*Full)(nil)
	_ api.LocalChat            = (*Full)(nil)
	_ api.LocalImageChat       = (*Full)(nil)
	_ api.LocalImageSaver      = (*Full)(nil)
	_ api.LocalPartsChat       = (*Full)(nil)
	_ api.LocalGroupWriter     = (*Full)(nil)
	_ api.WhisperChat          = (*Full)(nil)
	_ api.WhisperLogReader     = (*Full)(nil)
	_ api.LocalStatusReader    = (*Full)(nil)
	_ api.LoopControl          = (*Full)(nil)
	_ api.ReminderControl      = (*Full)(nil)
	_ api.LocalInputs          = (*Full)(nil)
	_ api.LocalHeld            = (*Full)(nil)
	_ api.LocalQuestions       = (*Full)(nil)
	_ api.ApprovalRules        = (*Full)(nil)
	_ api.ApprovalRuleEditor   = (*Full)(nil)
	_ api.ConversationApproval = (*Full)(nil)
	_ api.LocalWorkspaces      = (*Full)(nil)
	_ api.CheckpointActions    = (*Full)(nil)
)

// remoteError 是服务端回的、带哨兵码的错误：Error() 是服务端的原文（"第 N 块：……"这类前缀照旧），errors.Is 认哨兵
type remoteError struct {
	sentinel error
	msg      string
}

func (e *remoteError) Error() string { return e.msg }
func (e *remoteError) Unwrap() error { return e.sentinel }

var codeSentinels = map[int]error{
	rpcwire.CodeLocalChatUnsupported:  api.ErrLocalChatUnsupported,
	rpcwire.CodeLocalChatBusy:         api.ErrLocalChatBusy,
	rpcwire.CodeApprovalGone:          api.ErrApprovalGone,
	rpcwire.CodeQuestionGone:          api.ErrQuestionGone,
	rpcwire.CodeQuoteTarget:           api.ErrQuoteTarget,
	rpcwire.CodeBadPart:               api.ErrPart,
	rpcwire.CodeReadMarkUnsupported:   api.ErrReadMarkUnsupported,
	rpcwire.CodeNoAvatar:              api.ErrNoAvatar,
	rpcwire.CodeNotFound:              api.ErrNotFound,
	rpcwire.CodeLocalGroupUnsupported: api.ErrLocalGroupUnsupported,
	rpcwire.CodeLocalGroupBusy:        api.ErrLocalGroupBusy,
}

// mapErr 把带哨兵码的协议错误还原成同一个哨兵；其余原样（1001、1002 等是普通错误）
func mapErr(err error) error {
	var we *rpcwire.Error
	if errors.As(err, &we) {
		if s, ok := codeSentinels[we.Code]; ok {
			return &remoteError{sentinel: s, msg: we.Message}
		}
	}
	return err
}

// invoke：后台没登记就不发请求，回 unsup；否则调用并还原错误
func (f *Full) invoke(ctx context.Context, method string, params, out any, unsup error) error {
	if !f.C.Has(method) {
		return unsup
	}
	return mapErr(f.C.Call(ctx, method, params, out))
}

// 本地私聊那批不支持时的错误
var errChat = api.ErrLocalChatUnsupported

// ───── 头像、转发、已读、视频 ─────

func (f *Full) ReadAvatar(ctx context.Context, key string) (api.Result[api.MediaView], error) {
	if !f.C.Has(rpcwire.MethodAvatarRead) {
		return api.Result[api.MediaView]{State: api.Failed, Err: api.ErrNoAvatar}, api.ErrNoAvatar
	}
	res, err := read(ctx, f.C, rpcwire.MethodAvatarRead, rpcwire.AvatarReadParams{Key: key}, rpcconv.MediaIn)
	if err != nil {
		err = mapErr(err)
		res.Err = err
	}
	return res, err
}

func (f *Full) ReadForward(ctx context.Context, req api.ForwardRequest) (api.Result[api.ForwardView], error) {
	res, err := read(ctx, f.C, rpcwire.MethodForwardRead, rpcconv.ForwardRequestOut(req), rpcconv.ForwardIn)
	if err != nil {
		err = mapErr(err)
		res.Err = err
		return res, err
	}
	// 服务端把"带说明状态的失败"（太大等）放在 Result 里一起回；还原成 Go 直接调用的样子：值和错误同时给
	if res.State == api.Failed && res.Err != nil {
		return res, res.Err
	}
	return res, nil
}

func (f *Full) MarkRead(ctx context.Context, key api.ConversationKey, upTo int64) error {
	return f.invoke(ctx, rpcwire.MethodReadMarkSet, rpcwire.ReadMarkParams{Conversation: rpcconv.KeyOut(key), UpTo: upTo}, nil, api.ErrReadMarkUnsupported)
}

func (f *Full) PrepareVideo(ctx context.Context, ref api.MediaRef) (string, error) {
	var out rpcwire.PathResult
	if err := f.invoke(ctx, rpcwire.MethodVideoPrepare, rpcconv.MediaRefOut(ref), &out, errors.New("这个后台没有 "+rpcwire.MethodVideoPrepare)); err != nil {
		return "", err
	}
	return out.Path, nil
}

// ───── 本地私聊：发送与草稿 ─────

// draftStream 把一次发送的 local.draft 通知按顺序交给那次调用的回调。回调不在读循环里跑（回调慢不能卡住整条连接），
// 队列不限长（草稿是整份快照，数量跟着模型吐字节奏，不会堆很多）
type draftStream struct {
	mu     sync.Mutex
	q      []func()
	closed bool
	wake   chan struct{}
	done   chan struct{}

	draft   func(api.LocalDraft)
	process func(string, []api.LocalProcessStep)
}

// push 排一件要在回调线程上做的事。草稿和本地群聊的过程通知共用一个流，所以先后顺序和后台回调的顺序一致
func (s *draftStream) push(fn func()) {
	s.mu.Lock()
	if !s.closed {
		s.q = append(s.q, fn)
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *draftStream) run() {
	defer close(s.done)
	for {
		s.mu.Lock()
		q, closed := s.q, s.closed
		s.q = nil
		s.mu.Unlock()
		for _, fn := range q {
			fn()
		}
		if closed && len(q) == 0 {
			return
		}
		if len(q) == 0 {
			<-s.wake
		}
	}
}

// close：不再收新的，把已经收到的交完再返回
func (s *draftStream) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	<-s.done
}

// openStream 登记一个流号；draft 和 process 都为 nil 就不登记（params 里 stream 留空，服务端不推）
func (c *Client) openStream(draft func(api.LocalDraft), process func(string, []api.LocalProcessStep)) (string, func()) {
	if draft == nil && process == nil {
		return "", func() {}
	}
	s := &draftStream{wake: make(chan struct{}, 1), done: make(chan struct{}), draft: draft, process: process}
	c.mu.Lock()
	c.nextStream++
	id := "d" + strconv.Itoa(c.nextStream)
	if c.streams == nil {
		c.streams = map[string]*draftStream{}
	}
	c.streams[id] = s
	c.mu.Unlock()
	go s.run()
	return id, func() {
		c.mu.Lock()
		delete(c.streams, id)
		c.mu.Unlock()
		s.close()
	}
}

func (c *Client) onDraft(params []byte) {
	var n rpcwire.LocalDraftNotice
	if json.Unmarshal(params, &n) != nil {
		return
	}
	c.mu.Lock()
	s := c.streams[n.Stream]
	c.mu.Unlock()
	if s != nil && s.draft != nil {
		d := rpcconv.DraftIn(n.Draft)
		s.push(func() { s.draft(d) })
	}
}

func (c *Client) onGroupProcess(params []byte) {
	var n rpcwire.LocalGroupProcessNotice
	if json.Unmarshal(params, &n) != nil {
		return
	}
	c.mu.Lock()
	s := c.streams[n.Stream]
	c.mu.Unlock()
	if s != nil && s.process != nil {
		steps := rpcconv.StepsIn(n.Steps)
		s.push(func() { s.process(n.Turn, steps) })
	}
}

func (f *Full) SendLocal(ctx context.Context, k api.ConversationKey, text string, draft func(api.LocalDraft)) error {
	if !f.C.Has(rpcwire.MethodLocalSend) {
		return errChat
	}
	id, done := f.C.openStream(draft, nil)
	defer done()
	return f.invoke(ctx, rpcwire.MethodLocalSend, rpcwire.LocalSendParams{Conversation: rpcconv.KeyOut(k), Text: text, Stream: id}, nil, errChat)
}

func (f *Full) SendLocalImages(ctx context.Context, k api.ConversationKey, text string, images [][]byte, draft func(api.LocalDraft)) error {
	if !f.C.Has(rpcwire.MethodLocalSendImages) {
		return errChat
	}
	id, done := f.C.openStream(draft, nil)
	defer done()
	return f.invoke(ctx, rpcwire.MethodLocalSendImages, rpcwire.LocalSendImagesParams{Conversation: rpcconv.KeyOut(k), Text: text, Images: images, Stream: id}, nil, errChat)
}

func (f *Full) SendLocalParts(ctx context.Context, k api.ConversationKey, parts []api.LocalPart, draft func(api.LocalDraft)) error {
	if !f.C.Has(rpcwire.MethodLocalSendParts) {
		return errChat
	}
	id, done := f.C.openStream(draft, nil)
	defer done()
	return f.invoke(ctx, rpcwire.MethodLocalSendParts, rpcwire.LocalSendPartsParams{Conversation: rpcconv.KeyOut(k), Parts: rpcconv.PartsOut(parts), Stream: id}, nil, errChat)
}

// ───── 悄悄话、对话日志 ─────

func (f *Full) SendWhisper(ctx context.Context, k api.ConversationKey, parts []api.LocalPart) (api.WhisperReceipt, error) {
	var out rpcwire.WhisperReceipt
	if err := f.invoke(ctx, rpcwire.MethodWhisperSend, rpcwire.WhisperSendParams{Conversation: rpcconv.KeyOut(k), Parts: rpcconv.PartsOut(parts)}, &out, errChat); err != nil {
		return api.WhisperReceipt{}, err
	}
	return rpcconv.WhisperReceiptIn(out), nil
}

func (f *Full) ReadWhisperLog(ctx context.Context, k api.ConversationKey, cursor string, limit int) (ctlproto.LogPage, error) {
	var out rpcwire.LogPage
	if err := f.invoke(ctx, rpcwire.MethodWhisperLogRead, rpcwire.WhisperLogReadParams{Conversation: rpcconv.KeyOut(k), Cursor: cursor, Limit: limit}, &out, errChat); err != nil {
		return ctlproto.LogPage{}, err
	}
	return rpcconv.LogPageIn(out), nil
}

func (f *Full) ReadWhisperLogEntry(ctx context.Context, k api.ConversationKey, id string, withThinking bool) (ctlproto.LogDetail, error) {
	var out rpcwire.LogDetail
	if err := f.invoke(ctx, rpcwire.MethodWhisperLogEntry, rpcwire.WhisperLogEntryParams{Conversation: rpcconv.KeyOut(k), ID: id, WithThinking: withThinking}, &out, errChat); err != nil {
		return ctlproto.LogDetail{}, err
	}
	return rpcconv.LogDetailIn(out), nil
}

func (f *Full) WatchWhisperLog(ctx context.Context, k api.ConversationKey) (api.Subscription[ctlproto.LogPage], error) {
	return watchOr(ctx, f, rpcwire.MethodWhisperLogSubscribe, rpcwire.MethodWhisperLogUnsubscribe, rpcwire.ConversationParams{Conversation: rpcconv.KeyOut(k)},
		func(w rpcwire.Result[rpcwire.LogPage]) api.Result[ctlproto.LogPage] {
			return rpcconv.ResultIn(w, rpcconv.LogPageIn)
		})
}

func (f *Full) CancelWhisper(ctx context.Context, k api.ConversationKey, batch, character string) (int, error) {
	var out rpcwire.WhisperCancelResult
	if err := f.invoke(ctx, rpcwire.MethodWhisperLogCancel, rpcwire.WhisperCancelParams{Conversation: rpcconv.KeyOut(k), Batch: batch, Character: character}, &out, errChat); err != nil {
		return 0, err
	}
	return out.Cancelled, nil
}

// ───── 目标、定期任务、提醒的状态和控制 ─────

// 这个后台不支持定期任务、提醒时的错误（界面按接口断言选页面，走到这里多半是对端版本不对）
var errWork = errors.New("当前数据源不支持目标、定期任务和提醒")

func (f *Full) ReadLocalStatus(ctx context.Context, k api.ConversationKey) (api.LocalStatus, error) {
	var out rpcwire.LocalStatus
	if err := f.invoke(ctx, rpcwire.MethodLocalStatusRead, rpcwire.ConversationParams{Conversation: rpcconv.KeyOut(k)}, &out, errWork); err != nil {
		return api.LocalStatus{}, err
	}
	return rpcconv.LocalStatusIn(out), nil
}

func (f *Full) WatchLocalStatus(ctx context.Context) (api.Subscription[[]api.LocalStatus], error) {
	if !f.C.Has(rpcwire.MethodLocalStatusSubscribe) {
		return nil, errWork
	}
	sub, err := watch(ctx, f.C, rpcwire.MethodLocalStatusSubscribe, rpcwire.MethodLocalStatusUnsubscribe, rpcwire.Empty{},
		func(w rpcwire.Result[[]rpcwire.LocalStatus]) api.Result[[]api.LocalStatus] {
			return rpcconv.ResultIn(w, rpcconv.LocalStatusesIn)
		})
	if err != nil {
		return nil, mapErr(err)
	}
	return sub, nil
}

func (f *Full) AddLoop(ctx context.Context, k api.ConversationKey, sp api.LoopSpec) (int, error) {
	var out rpcwire.LoopAddResult
	if err := f.invoke(ctx, rpcwire.MethodLoopAdd, rpcwire.LoopAddParams{Conversation: rpcconv.KeyOut(k), Spec: rpcconv.LoopSpecOut(sp)}, &out, errWork); err != nil {
		return 0, err
	}
	return out.ID, nil
}

func (f *Full) EditLoop(ctx context.Context, k api.ConversationKey, id int, sp api.LoopSpec) error {
	return f.invoke(ctx, rpcwire.MethodLoopEdit, rpcwire.LoopEditParams{Conversation: rpcconv.KeyOut(k), ID: id, Spec: rpcconv.LoopSpecOut(sp)}, nil, errWork)
}

func (f *Full) PauseLoop(ctx context.Context, k api.ConversationKey, id int) error {
	return f.invoke(ctx, rpcwire.MethodLoopPause, rpcwire.ItemParams{Conversation: rpcconv.KeyOut(k), ID: id}, nil, errWork)
}

func (f *Full) ResumeLoop(ctx context.Context, k api.ConversationKey, id int) error {
	return f.invoke(ctx, rpcwire.MethodLoopResume, rpcwire.ItemParams{Conversation: rpcconv.KeyOut(k), ID: id}, nil, errWork)
}

func (f *Full) RemoveLoop(ctx context.Context, k api.ConversationKey, id int) error {
	return f.invoke(ctx, rpcwire.MethodLoopRemove, rpcwire.ItemParams{Conversation: rpcconv.KeyOut(k), ID: id}, nil, errWork)
}

// reminderOptionsTimeout：ReminderOptions 接口没有 ctx，编辑框同步调它；后台卡住时最多等这么久就当提醒没开（测试会调短）
var reminderOptionsTimeout = 2 * time.Second

// ReminderOptions 没有错误可回：问不到（后台不支持、网断了、卡住超时）就当提醒没开
func (f *Full) ReminderOptions() api.ReminderOptions {
	var out rpcwire.ReminderOptions
	ctx, cancel := context.WithTimeout(context.Background(), reminderOptionsTimeout)
	defer cancel()
	if err := f.invoke(ctx, rpcwire.MethodReminderOptions, rpcwire.Empty{}, &out, errWork); err != nil {
		return api.ReminderOptions{}
	}
	return rpcconv.ReminderOptionsIn(out)
}

func (f *Full) CreateReminder(ctx context.Context, k api.ConversationKey, d api.ReminderDraft) error {
	return f.invoke(ctx, rpcwire.MethodReminderCreate, rpcwire.ReminderCreateParams{Conversation: rpcconv.KeyOut(k), Draft: rpcconv.ReminderDraftOut(d)}, nil, errWork)
}

func (f *Full) UpdateReminder(ctx context.Context, k api.ConversationKey, id int, p api.ReminderPatch) error {
	return f.invoke(ctx, rpcwire.MethodReminderUpdate, rpcwire.ReminderUpdateParams{Conversation: rpcconv.KeyOut(k), ID: id, Patch: rpcconv.ReminderPatchOut(p)}, nil, errWork)
}

func (f *Full) FinishReminder(ctx context.Context, k api.ConversationKey, id int, done bool) error {
	return f.invoke(ctx, rpcwire.MethodReminderFinish, rpcwire.ReminderFinishParams{Conversation: rpcconv.KeyOut(k), ID: id, Done: done}, nil, errWork)
}

func (f *Full) DeleteReminder(ctx context.Context, k api.ConversationKey, id int) error {
	return f.invoke(ctx, rpcwire.MethodReminderDelete, rpcwire.ItemParams{Conversation: rpcconv.KeyOut(k), ID: id}, nil, errWork)
}

// ───── 本地群聊 ─────

var errGroup = api.ErrLocalGroupUnsupported

func (f *Full) CreateLocalGroup(ctx context.Context, req api.LocalGroupCreate) (api.ConversationView, error) {
	var out rpcwire.ConversationView
	if err := f.invoke(ctx, rpcwire.MethodLocalGroupCreate, rpcwire.LocalGroupCreateParams{Name: req.Name, Characters: req.Characters}, &out, errGroup); err != nil {
		return api.ConversationView{}, err
	}
	return rpcconv.ConversationIn(out), nil
}

func (f *Full) SendLocalGroup(ctx context.Context, req api.LocalGroupSend) error {
	if !f.C.Has(rpcwire.MethodLocalGroupSend) {
		return errGroup
	}
	id, done := f.C.openStream(req.Draft, req.Process)
	defer done()
	return f.invoke(ctx, rpcwire.MethodLocalGroupSend, rpcwire.LocalGroupSendParams{Conversation: rpcconv.KeyOut(req.Key), Text: req.Text, Mentions: req.Mentions, Stream: id}, nil, errGroup)
}

func (f *Full) StopLocalGroup(ctx context.Context, k api.ConversationKey) error {
	return f.invoke(ctx, rpcwire.MethodLocalGroupStop, rpcwire.ConversationParams{Conversation: rpcconv.KeyOut(k)}, nil, errGroup)
}

func (f *Full) SaveLocalImage(ctx context.Context, k api.ConversationKey, png []byte) (api.LocalAttachment, error) {
	var out rpcwire.LocalAttachment
	if err := f.invoke(ctx, rpcwire.MethodLocalSaveImage, rpcwire.LocalSaveImageParams{Conversation: rpcconv.KeyOut(k), PNG: png}, &out, errChat); err != nil {
		return api.LocalAttachment{}, err
	}
	return rpcconv.AttachmentIn(out), nil
}

func (f *Full) StopLocal(ctx context.Context, k api.ConversationKey) error {
	return f.invoke(ctx, rpcwire.MethodLocalStop, rpcwire.ConversationParams{Conversation: rpcconv.KeyOut(k)}, nil, errChat)
}

func (f *Full) DecideLocal(ctx context.Context, k api.ConversationKey, id string, choice api.ApprovalChoice) error {
	return f.invoke(ctx, rpcwire.MethodLocalDecide, rpcwire.LocalDecideParams{Conversation: rpcconv.KeyOut(k), ID: id, Choice: string(choice)}, nil, errChat)
}

// watchOr 开订阅；后台没登记就回 unsup，开订阅出错还原哨兵
func watchOr[W, T any](ctx context.Context, f *Full, subscribe, unsubscribe string, params any, conv func(rpcwire.Result[W]) api.Result[T]) (api.Subscription[T], error) {
	if !f.C.Has(subscribe) {
		return nil, errChat
	}
	sub, err := watch(ctx, f.C, subscribe, unsubscribe, params, conv)
	if err != nil {
		return nil, mapErr(err)
	}
	return sub, nil
}

func (f *Full) WatchLocalApprovals(ctx context.Context) (api.Subscription[[]api.LocalApproval], error) {
	return watchOr(ctx, f, rpcwire.MethodLocalApprovalsSubscribe, rpcwire.MethodLocalApprovalsUnsubscribe, rpcwire.Empty{},
		func(w rpcwire.Result[[]rpcwire.LocalApproval]) api.Result[[]api.LocalApproval] {
			return rpcconv.ResultIn(w, rpcconv.ApprovalsIn)
		})
}

// ───── 输入语义 ─────

func (f *Full) SubmitLocal(ctx context.Context, k api.ConversationKey, in api.LocalInputSubmission) (api.InputReceipt, error) {
	var out rpcwire.InputReceipt
	if err := f.invoke(ctx, rpcwire.MethodLocalSubmit, rpcwire.LocalSubmitParams{Conversation: rpcconv.KeyOut(k), Submission: rpcconv.SubmissionOut(in)}, &out, errChat); err != nil {
		return api.InputReceipt{}, err
	}
	return rpcconv.ReceiptIn(out), nil
}

func (f *Full) StopLocalRun(ctx context.Context, k api.ConversationKey, runID string, through uint64) (api.StopReceipt, error) {
	var out rpcwire.StopReceipt
	if err := f.invoke(ctx, rpcwire.MethodLocalStopRun, rpcwire.LocalStopRunParams{Conversation: rpcconv.KeyOut(k), RunID: runID, Through: int64(through)}, &out, errChat); err != nil {
		return api.StopReceipt{}, err
	}
	return rpcconv.StopReceiptIn(out), nil
}

func (f *Full) WatchLocalRun(ctx context.Context, k api.ConversationKey, runID string) (api.Subscription[api.LocalRunSnapshot], error) {
	return watchOr(ctx, f, rpcwire.MethodLocalRunSubscribe, rpcwire.MethodLocalRunUnsubscribe, rpcwire.LocalRunParams{Conversation: rpcconv.KeyOut(k), RunID: runID},
		func(w rpcwire.Result[rpcwire.LocalRunSnapshot]) api.Result[api.LocalRunSnapshot] {
			return rpcconv.ResultIn(w, rpcconv.RunIn)
		})
}

// ───── 没处理的话、排队 ─────

func (f *Full) HeldLocal(ctx context.Context) ([]api.HeldInput, error) {
	var out rpcwire.HeldList
	if err := f.invoke(ctx, rpcwire.MethodHeldList, rpcwire.Empty{}, &out, errChat); err != nil {
		return nil, err
	}
	return rpcconv.HeldIn(out), nil
}

func (f *Full) ReleaseHeldLocal(ctx context.Context, k api.ConversationKey, inputID string) (api.InputReceipt, error) {
	var out rpcwire.InputReceipt
	if err := f.invoke(ctx, rpcwire.MethodHeldRelease, rpcwire.InputParams{Conversation: rpcconv.KeyOut(k), InputID: inputID}, &out, errChat); err != nil {
		return api.InputReceipt{}, err
	}
	return rpcconv.ReceiptIn(out), nil
}

func (f *Full) DiscardHeldLocal(ctx context.Context, k api.ConversationKey, inputID string) error {
	return f.invoke(ctx, rpcwire.MethodHeldDiscard, rpcwire.InputParams{Conversation: rpcconv.KeyOut(k), InputID: inputID}, nil, errChat)
}

func (f *Full) WithdrawQueuedLocal(ctx context.Context, k api.ConversationKey, inputID string) error {
	return f.invoke(ctx, rpcwire.MethodLocalWithdraw, rpcwire.InputParams{Conversation: rpcconv.KeyOut(k), InputID: inputID}, nil, errChat)
}

// ───── 提问卡 ─────

func (f *Full) WatchLocalQuestions(ctx context.Context) (api.Subscription[[]api.LocalQuestion], error) {
	return watchOr(ctx, f, rpcwire.MethodLocalQuestionsSubscribe, rpcwire.MethodLocalQuestionsUnsubscribe, rpcwire.Empty{},
		func(w rpcwire.Result[[]rpcwire.LocalQuestion]) api.Result[[]api.LocalQuestion] {
			return rpcconv.ResultIn(w, rpcconv.QuestionsIn)
		})
}

func (f *Full) AnswerLocal(ctx context.Context, k api.ConversationKey, id string, option int) error {
	return f.invoke(ctx, rpcwire.MethodLocalAnswer, rpcwire.LocalAnswerParams{Conversation: rpcconv.KeyOut(k), ID: id, Option: option}, nil, errChat)
}

// ───── 放行规则、审批档 ─────

func (f *Full) ListApprovalRules(ctx context.Context) ([]api.ApprovalRule, error) {
	var out rpcwire.ApprovalRuleList
	if err := f.invoke(ctx, rpcwire.MethodApprovalRulesList, rpcwire.Empty{}, &out, errChat); err != nil {
		return nil, err
	}
	return rpcconv.RulesIn(out), nil
}

func (f *Full) RevokeApprovalRule(ctx context.Context, r api.ApprovalRule) error {
	return f.invoke(ctx, rpcwire.MethodApprovalRulesRevoke, rpcconv.RuleOut(r), nil, errChat)
}

func (f *Full) AddApprovalRule(ctx context.Context, raw, effect, project, session string) error {
	return f.invoke(ctx, rpcwire.MethodApprovalRulesAdd, rpcwire.ApprovalRuleAddParams{Raw: raw, Effect: effect, Project: project, Session: session}, nil, errChat)
}

func (f *Full) ConversationApprovalMode(ctx context.Context, k api.ConversationKey) (api.ConversationApprovalMode, error) {
	var out rpcwire.ConversationApprovalMode
	if err := f.invoke(ctx, rpcwire.MethodConversationApprovalRead, rpcwire.ConversationParams{Conversation: rpcconv.KeyOut(k)}, &out, errChat); err != nil {
		return api.ConversationApprovalMode{}, err
	}
	return rpcconv.ApprovalModeIn(out), nil
}

func (f *Full) SetConversationApprovalMode(ctx context.Context, k api.ConversationKey, mode string) error {
	return f.invoke(ctx, rpcwire.MethodConversationApprovalSet, rpcwire.ConversationApprovalSetParams{Conversation: rpcconv.KeyOut(k), Mode: mode}, nil, errChat)
}

// ───── 工作目录 ─────

func (f *Full) workspace(ctx context.Context, method string, params any) (api.WorkspaceInfo, error) {
	var out rpcwire.WorkspaceInfo
	if err := f.invoke(ctx, method, params, &out, errChat); err != nil {
		return api.WorkspaceInfo{}, err
	}
	return rpcconv.WorkspaceIn(out), nil
}

func (f *Full) ReadLocalWorkspace(ctx context.Context, k api.ConversationKey) (api.WorkspaceInfo, error) {
	return f.workspace(ctx, rpcwire.MethodWorkspaceRead, rpcwire.ConversationParams{Conversation: rpcconv.KeyOut(k)})
}

func (f *Full) SetLocalWorkspace(ctx context.Context, k api.ConversationKey, dir string) (api.WorkspaceInfo, error) {
	return f.workspace(ctx, rpcwire.MethodWorkspaceSet, rpcwire.WorkspaceSetParams{Conversation: rpcconv.KeyOut(k), Dir: dir})
}

func (f *Full) ClearLocalWorkspace(ctx context.Context, k api.ConversationKey) (api.WorkspaceInfo, error) {
	return f.workspace(ctx, rpcwire.MethodWorkspaceClear, rpcwire.ConversationParams{Conversation: rpcconv.KeyOut(k)})
}

func (f *Full) RecentWorkspaces(ctx context.Context) ([]string, error) {
	var out rpcwire.StringList
	if err := f.invoke(ctx, rpcwire.MethodWorkspaceRecent, rpcwire.Empty{}, &out, errChat); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// ───── 检查点 ─────

func (f *Full) ListCheckpoints(ctx context.Context, k api.ConversationKey) ([]api.CheckpointSummary, error) {
	var out rpcwire.CheckpointList
	if err := f.invoke(ctx, rpcwire.MethodCheckpointsList, rpcwire.ConversationParams{Conversation: rpcconv.KeyOut(k)}, &out, errChat); err != nil {
		return nil, err
	}
	return rpcconv.CheckpointListIn(out), nil
}

func (f *Full) PreviewCheckpoint(ctx context.Context, k api.ConversationKey, id, mode string) (api.CheckpointPlan, error) {
	var out rpcwire.CheckpointPlan
	if err := f.invoke(ctx, rpcwire.MethodCheckpointsPreview, rpcwire.CheckpointPreviewParams{Conversation: rpcconv.KeyOut(k), ID: id, Mode: mode}, &out, errChat); err != nil {
		return api.CheckpointPlan{}, err
	}
	return rpcconv.CheckpointPlanIn(out), nil
}

func (f *Full) RestoreCheckpoint(ctx context.Context, k api.ConversationKey, id, mode, version string) (api.CheckpointRestore, error) {
	var out rpcwire.CheckpointRestore
	if err := f.invoke(ctx, rpcwire.MethodCheckpointsRestore, rpcwire.CheckpointRestoreParams{Conversation: rpcconv.KeyOut(k), ID: id, Mode: mode, Version: version}, &out, errChat); err != nil {
		return api.CheckpointRestore{}, err
	}
	return rpcconv.CheckpointRestoreIn(out), nil
}

func (f *Full) ReadCheckpointProgress(ctx context.Context, k api.ConversationKey, targetID string) (api.CheckpointProgress, error) {
	var out rpcwire.CheckpointProgress
	if err := f.invoke(ctx, rpcwire.MethodCheckpointsProgress, rpcwire.CheckpointTargetParams{Conversation: rpcconv.KeyOut(k), TargetID: targetID}, &out, errChat); err != nil {
		return api.CheckpointProgress{}, err
	}
	return rpcconv.CheckpointProgressIn(out), nil
}

func (f *Full) CancelCheckpointRestore(ctx context.Context, k api.ConversationKey, targetID string) (api.CheckpointProgress, error) {
	var out rpcwire.CheckpointProgress
	if err := f.invoke(ctx, rpcwire.MethodCheckpointsCancel, rpcwire.CheckpointTargetParams{Conversation: rpcconv.KeyOut(k), TargetID: targetID}, &out, errChat); err != nil {
		return api.CheckpointProgress{}, err
	}
	return rpcconv.CheckpointProgressIn(out), nil
}

func (f *Full) WatchCheckpointProgress(ctx context.Context, k api.ConversationKey, targetID string) (api.Subscription[api.CheckpointProgress], error) {
	return watchOr(ctx, f, rpcwire.MethodCheckpointProgressSubscribe, rpcwire.MethodCheckpointProgressUnsubscribe, rpcwire.CheckpointTargetParams{Conversation: rpcconv.KeyOut(k), TargetID: targetID},
		func(w rpcwire.Result[rpcwire.CheckpointProgress]) api.Result[api.CheckpointProgress] {
			return rpcconv.ResultIn(w, rpcconv.CheckpointProgressIn)
		})
}
