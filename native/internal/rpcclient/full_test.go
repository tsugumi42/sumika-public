package rpcclient

// 后台没登记的方法（initialize 的 methods 里没有）：Full 不发请求，按规格 3.2 回各接口的"不支持"。

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/rpcwire"
)

// scripted 是只会回 initialize 的传输：记下握手以外发来的所有请求
type scripted struct {
	methods []string
	in      chan []byte
	done    chan struct{}
	once    sync.Once

	mu   sync.Mutex
	seen []string
}

func newScripted(methods ...string) *scripted {
	return &scripted{methods: methods, in: make(chan []byte, 8), done: make(chan struct{})}
}

func (s *scripted) Read(ctx context.Context) ([]byte, error) {
	select {
	case b := <-s.in:
		return b, nil
	case <-s.done:
		return nil, errors.New("关了")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *scripted) Write(_ context.Context, b []byte) error {
	var m rpcwire.Message
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	if m.Method != rpcwire.MethodInitialize {
		s.mu.Lock()
		s.seen = append(s.seen, m.Method)
		s.mu.Unlock()
		return nil
	}
	res, _ := json.Marshal(rpcwire.InitializeResult{ProtocolVersion: rpcwire.ProtocolVersion, Server: rpcwire.ServerInfo{Impl: "test"}, Methods: s.methods})
	out, _ := json.Marshal(rpcwire.Message{JSONRPC: rpcwire.Version, ID: m.ID, Result: res})
	s.in <- out
	return nil
}

func (s *scripted) Close() error { s.once.Do(func() { close(s.done) }); return nil }

func (s *scripted) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// 后台登记了 ReminderOptions 却一直不回（卡住）：编辑框不能跟着挂，等到超时就当提醒没开。
func TestReminderOptionsDoesNotHangOnStuckBackend(t *testing.T) {
	old := reminderOptionsTimeout
	reminderOptionsTimeout = 100 * time.Millisecond
	defer func() { reminderOptionsTimeout = old }()
	tr := newScripted(rpcwire.MethodReminderOptions) // 只回握手，别的请求都不回
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := New(ctx, tr, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	f := NewFull(c)
	done := make(chan api.ReminderOptions, 1)
	go func() { done <- f.ReminderOptions() }()
	select {
	case o := <-done:
		if o.Enabled {
			t.Fatalf("问不到应当提醒没开：%+v", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReminderOptions 跟着卡住的后台挂住了")
	}
}

func TestUnregisteredMethodsAreNotSent(t *testing.T) {
	tr := newScripted(rpcwire.MethodMessagesRead) // 后台只登记了一个读方法
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := New(ctx, tr, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	f := NewFull(c)
	k := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:a"}

	chat := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, api.ErrLocalChatUnsupported) {
			t.Errorf("%s 应回 ErrLocalChatUnsupported：%v", name, err)
		}
	}
	chat("SendLocal", f.SendLocal(ctx, k, "x", func(api.LocalDraft) { t.Error("不该有草稿") }))
	chat("SendLocalImages", f.SendLocalImages(ctx, k, "x", [][]byte{{1}}, nil))
	chat("SendLocalParts", f.SendLocalParts(ctx, k, nil, nil))
	_, err = f.SaveLocalImage(ctx, k, []byte{1})
	chat("SaveLocalImage", err)
	chat("StopLocal", f.StopLocal(ctx, k))
	group := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, api.ErrLocalGroupUnsupported) {
			t.Errorf("%s 应回 ErrLocalGroupUnsupported：%v", name, err)
		}
	}
	_, err = f.CreateLocalGroup(ctx, api.LocalGroupCreate{Name: "x", Characters: []string{"a", "b"}})
	group("CreateLocalGroup", err)
	group("SendLocalGroup", f.SendLocalGroup(ctx, api.LocalGroupSend{Key: k, Text: "x", Draft: func(api.LocalDraft) { t.Error("不该有草稿") }}))
	group("StopLocalGroup", f.StopLocalGroup(ctx, k))
	_, err = f.SendWhisper(ctx, k, nil)
	chat("SendWhisper", err)
	_, err = f.ReadWhisperLog(ctx, k, "", 0)
	chat("ReadWhisperLog", err)
	_, err = f.ReadWhisperLogEntry(ctx, k, "say:1", false)
	chat("ReadWhisperLogEntry", err)
	_, err = f.WatchWhisperLog(ctx, k)
	chat("WatchWhisperLog", err)
	_, err = f.CancelWhisper(ctx, k, "b", "a")
	chat("CancelWhisper", err)
	chat("DecideLocal", f.DecideLocal(ctx, k, "a", api.ApproveOnce))
	_, err = f.WatchLocalApprovals(ctx)
	chat("WatchLocalApprovals", err)
	_, err = f.SubmitLocal(ctx, k, api.LocalInputSubmission{Text: "x"})
	chat("SubmitLocal", err)
	_, err = f.StopLocalRun(ctx, k, "r", 0)
	chat("StopLocalRun", err)
	_, err = f.WatchLocalRun(ctx, k, "r")
	chat("WatchLocalRun", err)
	_, err = f.HeldLocal(ctx)
	chat("HeldLocal", err)
	_, err = f.ReleaseHeldLocal(ctx, k, "i")
	chat("ReleaseHeldLocal", err)
	chat("DiscardHeldLocal", f.DiscardHeldLocal(ctx, k, "i"))
	chat("WithdrawQueuedLocal", f.WithdrawQueuedLocal(ctx, k, "i"))
	_, err = f.WatchLocalQuestions(ctx)
	chat("WatchLocalQuestions", err)
	chat("AnswerLocal", f.AnswerLocal(ctx, k, "q", 0))
	_, err = f.ListApprovalRules(ctx)
	chat("ListApprovalRules", err)
	chat("RevokeApprovalRule", f.RevokeApprovalRule(ctx, api.ApprovalRule{}))
	chat("AddApprovalRule", f.AddApprovalRule(ctx, "x", "allow", "", ""))
	_, err = f.ConversationApprovalMode(ctx, k)
	chat("ConversationApprovalMode", err)
	chat("SetConversationApprovalMode", f.SetConversationApprovalMode(ctx, k, "auto"))
	_, err = f.ReadLocalWorkspace(ctx, k)
	chat("ReadLocalWorkspace", err)
	_, err = f.SetLocalWorkspace(ctx, k, "C:\\x")
	chat("SetLocalWorkspace", err)
	_, err = f.ClearLocalWorkspace(ctx, k)
	chat("ClearLocalWorkspace", err)
	_, err = f.RecentWorkspaces(ctx)
	chat("RecentWorkspaces", err)
	_, err = f.ListCheckpoints(ctx, k)
	chat("ListCheckpoints", err)
	_, err = f.PreviewCheckpoint(ctx, k, "c", "all")
	chat("PreviewCheckpoint", err)
	_, err = f.RestoreCheckpoint(ctx, k, "c", "all", "v")
	chat("RestoreCheckpoint", err)
	_, err = f.ReadCheckpointProgress(ctx, k, "c")
	chat("ReadCheckpointProgress", err)
	_, err = f.CancelCheckpointRestore(ctx, k, "c")
	chat("CancelCheckpointRestore", err)
	_, err = f.WatchCheckpointProgress(ctx, k, "c")
	chat("WatchCheckpointProgress", err)

	if err := f.MarkRead(ctx, k, 1); !errors.Is(err, api.ErrReadMarkUnsupported) {
		t.Errorf("MarkRead：%v", err)
	}
	if res, err := f.ReadAvatar(ctx, "user:12345"); !errors.Is(err, api.ErrNoAvatar) || res.State != api.Failed {
		t.Errorf("ReadAvatar：%+v %v", res, err)
	}
	if res, _ := f.ReadForward(ctx, api.ForwardRequest{}); res.State != api.Unsupported {
		t.Errorf("ReadForward：%+v", res)
	}
	if _, err := f.PrepareVideo(ctx, api.MediaRef{}); err == nil {
		t.Error("PrepareVideo 应报错")
	}

	if got := tr.requests(); len(got) != 0 {
		t.Fatalf("后台没登记的方法不该发请求，却发了：%v", got)
	}
}

// 哨兵码还原：Error() 是服务端原文，errors.Is 认哨兵；别的码原样
func TestMapErr(t *testing.T) {
	err := mapErr(&rpcwire.Error{Code: rpcwire.CodeQuoteTarget, Message: "第 3 块：引用的消息不存在"})
	if !errors.Is(err, api.ErrQuoteTarget) || err.Error() != "第 3 块：引用的消息不存在" {
		t.Fatalf("%v", err)
	}
	plain := mapErr(&rpcwire.Error{Code: rpcwire.CodeFailed, Message: "x"})
	var we *rpcwire.Error
	if !errors.As(plain, &we) || errors.Is(plain, api.ErrQuoteTarget) {
		t.Fatalf("%v", plain)
	}
	if mapErr(nil) != nil {
		t.Fatal("nil 要还是 nil")
	}
}
