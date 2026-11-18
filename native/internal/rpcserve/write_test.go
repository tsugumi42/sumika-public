package rpcserve_test

// 第 2 批的 35 个方法（规格-Bun后台第2批 第二、三节）：默认不开、WithWrites 才开；
// 经管道和经 WebSocket 调到的和直接调假后台的一样；每个哨兵错误经协议回各自的码、客户端还原后 errors.Is 成立。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/rpcclient"
	"sumika-native/internal/rpcserve"
	"sumika-native/internal/rpcwire"
)

// full 是客户端要实现的 20 个接口
type full interface {
	api.AvatarReader
	api.ForwardReader
	api.ReadMarker
	api.VideoReader
	api.LocalChat
	api.LocalImageChat
	api.LocalImageSaver
	api.LocalPartsChat
	api.LocalGroupWriter
	api.WhisperChat
	api.WhisperLogReader
	api.LocalStatusReader
	api.LoopControl
	api.ReminderControl
	api.LocalInputs
	api.LocalHeld
	api.LocalQuestions
	api.ApprovalRules
	api.ApprovalRuleEditor
	api.ConversationApproval
	api.LocalWorkspaces
	api.CheckpointActions
}

var (
	_ full = (*fakeFull)(nil)
	_ full = (*rpcclient.Full)(nil)
)

func newFake() *fakeFull { return &fakeFull{Reader: api.Reader(nil)} }

func pipeClient(t *testing.T, srv *rpcserve.Server) *rpcclient.Full {
	t.Helper()
	st, ct := rpcserve.Pipe()
	go srv.Serve(context.Background(), st)
	c, err := rpcclient.New(ctx(t), ct, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return rpcclient.NewFull(c)
}

func wsClient(t *testing.T, srv *rpcserve.Server) *rpcclient.Full {
	t.Helper()
	l, err := rpcserve.Listen(context.Background(), srv, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	c, err := rpcclient.Dial(ctx(t), l.URL, l.Token(), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return rpcclient.NewFull(c)
}

func first[T any](t *testing.T, sub api.Subscription[T], err error) api.Snapshot[T] {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	select {
	case s := <-sub.Updates():
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("等不到订阅的第一个快照")
	}
	return api.Snapshot[T]{}
}

// exercise 把 35 个方法各调一遍，结果（含错误文字）依次放进列表；直接调、经管道、经 WebSocket 三份要完全一样
func exercise(t *testing.T, x full) []any {
	t.Helper()
	c := context.Background()
	var out []any
	add := func(name string, vals ...any) { out = append(out, append([]any{name}, vals...)) }
	es := func(err error) string {
		if err == nil {
			return ""
		}
		return err.Error()
	}
	k := testKey
	ref := api.EventRef{Conversation: k, Event: api.EventKey{Platform: "local", EventID: "e1"}}

	av, err := x.ReadAvatar(c, "user:12345")
	add("ReadAvatar", av, es(err))
	fw, err := x.ReadForward(c, api.ForwardRequest{Source: ref, ID: "f1", Via: []string{"p"}})
	add("ReadForward", fw, es(err))
	add("MarkRead", es(x.MarkRead(c, k, 5)))
	p, err := x.PrepareVideo(c, api.MediaRef{Hash: "abc"})
	add("PrepareVideo", p, es(err))

	var drafts []api.LocalDraft
	err = x.SendLocal(c, k, "你好", func(d api.LocalDraft) { drafts = append(drafts, d) })
	add("SendLocal", es(err), drafts)
	add("SendLocal/nil", es(x.SendLocal(c, k, "不要草稿", nil)))
	drafts = nil
	err = x.SendLocalImages(c, k, "图", [][]byte{{1, 2}, {3}}, func(d api.LocalDraft) { drafts = append(drafts, d) })
	add("SendLocalImages", es(err), drafts)
	drafts = nil
	parts := []api.LocalPart{{Quote: &api.LocalQuote{Ref: ref, Text: "片段", Start: 1, End: 3}, Text: "回复", Attachments: []api.LocalAttachment{{Kind: "file", Path: "C:\\x"}}}, {Text: "普通"}}
	err = x.SendLocalParts(c, k, parts, func(d api.LocalDraft) { drafts = append(drafts, d) })
	add("SendLocalParts", es(err), drafts)
	var gdrafts []api.LocalDraft
	var gsteps []any
	cv, err := x.CreateLocalGroup(c, api.LocalGroupCreate{Name: "编造的小屋", Characters: []string{"a", "b"}})
	add("CreateLocalGroup", cv, es(err))
	err = x.SendLocalGroup(c, api.LocalGroupSend{Key: groupKey, Text: "大家好", Mentions: []string{"a", "b"},
		Draft:   func(d api.LocalDraft) { gdrafts = append(gdrafts, d) },
		Process: func(turn string, steps []api.LocalProcessStep) { gsteps = append(gsteps, turn, steps) }})
	add("SendLocalGroup", es(err), gdrafts, gsteps)
	add("SendLocalGroup/nil", es(x.SendLocalGroup(c, api.LocalGroupSend{Key: groupKey, Text: "不要进度"})))
	add("StopLocalGroup", es(x.StopLocalGroup(c, groupKey)))
	wr, err := x.SendWhisper(c, groupKey, []api.LocalPart{{Quote: &api.LocalQuote{Ref: ref, Text: "片段", Start: 1, End: 3}, Text: "悄悄说", Mentions: []string{"a", "b"}, All: true}})
	add("SendWhisper", wr, es(err))
	lp, err := x.ReadWhisperLog(c, groupKey, "c1", 20)
	add("ReadWhisperLog", lp, es(err))
	ld, err := x.ReadWhisperLogEntry(c, groupKey, "step:1", true)
	add("ReadWhisperLogEntry", ld, es(err))
	wsub, err := x.WatchWhisperLog(c, groupKey)
	add("WatchWhisperLog", first(t, wsub, err))
	cn, err := x.CancelWhisper(c, groupKey, "b1", "a")
	add("CancelWhisper", cn, es(err))
	st, err := x.ReadLocalStatus(c, k)
	add("ReadLocalStatus", st, es(err))
	ssub, err := x.WatchLocalStatus(c)
	add("WatchLocalStatus", first(t, ssub, err))
	lid, err := x.AddLoop(c, k, api.LoopSpec{Prompt: "看看", Kind: "weekly", IntervalSec: 0, At: "09:00", Days: []int{1, 3}, Notify: "errors", NewChat: true})
	add("AddLoop", lid, es(err))
	add("EditLoop", es(x.EditLoop(c, k, 5, api.LoopSpec{Prompt: "改", Kind: "self"})))
	add("PauseLoop", es(x.PauseLoop(c, k, 5)))
	add("ResumeLoop", es(x.ResumeLoop(c, k, 5)))
	add("RemoveLoop", es(x.RemoveLoop(c, k, 5)))
	add("ReminderOptions", x.ReminderOptions())
	add("CreateReminder", es(x.CreateReminder(c, k, api.ReminderDraft{Text: "开会", At: "2026-11-10 09:00", Repeat: api.ReminderRepeat{Unit: "week", Every: 2, Weekdays: []int{1, 5}}, Targets: []string{"local"}})))
	txt, at, yes, no, none := "改了", "2026-11-11 08:00", true, false, []string{}
	rep := api.ReminderRepeat{Unit: "day"}
	add("UpdateReminder/全部", es(x.UpdateReminder(c, k, 2, api.ReminderPatch{Text: &txt, At: &at, Repeat: &rep, Targets: &none, Paused: &no})))
	add("UpdateReminder/只暂停", es(x.UpdateReminder(c, k, 2, api.ReminderPatch{Paused: &yes})))
	add("UpdateReminder/什么都不改", es(x.UpdateReminder(c, k, 2, api.ReminderPatch{})))
	add("FinishReminder", es(x.FinishReminder(c, k, 2, true)))
	add("DeleteReminder", es(x.DeleteReminder(c, k, 2)))
	att, err := x.SaveLocalImage(c, k, []byte{9, 9, 9})
	add("SaveLocalImage", att, es(err))
	add("StopLocal", es(x.StopLocal(c, k)))
	add("DecideLocal", es(x.DecideLocal(c, k, "ap1", api.ApproveAlways)))
	sub, err := x.WatchLocalApprovals(c)
	add("WatchLocalApprovals", first(t, sub, err))

	rc, err := x.SubmitLocal(c, k, api.LocalInputSubmission{Text: "插话", Mode: "steer", ClientKey: "ck", ExpectedRunID: "r1", Parts: parts[1:]})
	add("SubmitLocal", rc, es(err))
	sr, err := x.StopLocalRun(c, k, "r1", 5)
	add("StopLocalRun", sr, es(err))
	rsub, err := x.WatchLocalRun(c, k, "r1")
	add("WatchLocalRun", first(t, rsub, err))

	held, err := x.HeldLocal(c)
	add("HeldLocal", held, es(err))
	rc, err = x.ReleaseHeldLocal(c, k, "h1")
	add("ReleaseHeldLocal", rc, es(err))
	add("DiscardHeldLocal", es(x.DiscardHeldLocal(c, k, "h1")))
	add("WithdrawQueuedLocal", es(x.WithdrawQueuedLocal(c, k, "i1")))

	qsub, err := x.WatchLocalQuestions(c)
	add("WatchLocalQuestions", first(t, qsub, err))
	add("AnswerLocal", es(x.AnswerLocal(c, k, "q1", api.LocalAnswerSkip)))

	rules, err := x.ListApprovalRules(c)
	add("ListApprovalRules", rules, es(err))
	add("RevokeApprovalRule", es(x.RevokeApprovalRule(c, api.ApprovalRule{Key: "k", Scope: "always"})))
	add("AddApprovalRule", es(x.AddApprovalRule(c, "Bash(ls:*)", "allow", "C:\\p", "s1")))
	am, err := x.ConversationApprovalMode(c, k)
	add("ConversationApprovalMode", am, es(err))
	add("SetConversationApprovalMode", es(x.SetConversationApprovalMode(c, k, "yolo")))

	w, err := x.ReadLocalWorkspace(c, k)
	add("ReadLocalWorkspace", w, es(err))
	w, err = x.SetLocalWorkspace(c, k, "C:\\新目录")
	add("SetLocalWorkspace", w, es(err))
	w, err = x.ClearLocalWorkspace(c, k)
	add("ClearLocalWorkspace", w, es(err))
	rec, err := x.RecentWorkspaces(c)
	add("RecentWorkspaces", rec, es(err))

	cl, err := x.ListCheckpoints(c, k)
	add("ListCheckpoints", cl, es(err))
	plan, err := x.PreviewCheckpoint(c, k, "c1", "all")
	add("PreviewCheckpoint", plan, es(err))
	rs, err := x.RestoreCheckpoint(c, k, "c1", "all", "v1")
	add("RestoreCheckpoint", rs, es(err))
	pg, err := x.ReadCheckpointProgress(c, k, "c1")
	add("ReadCheckpointProgress", pg, es(err))
	pg, err = x.CancelCheckpointRestore(c, k, "c1")
	add("CancelCheckpointRestore", pg, es(err))
	psub, err := x.WatchCheckpointProgress(c, k, "c1")
	add("WatchCheckpointProgress", first(t, psub, err))
	return out
}

func TestWriteMethodsSameAsDirect(t *testing.T) {
	direct := newFake()
	want := exercise(t, direct)
	for _, route := range []struct {
		name string
		dial func(*testing.T, *rpcserve.Server) *rpcclient.Full
	}{{"管道", pipeClient}, {"WebSocket", wsClient}} {
		t.Run(route.name, func(t *testing.T) {
			f := newFake()
			got := exercise(t, route.dial(t, rpcserve.New(f, "test", rpcserve.WithWrites())))
			if !reflect.DeepEqual(want, got) {
				for i := range want {
					if i >= len(got) || !reflect.DeepEqual(want[i], got[i]) {
						t.Errorf("第 %d 项不一样\n直接 %#v\n协议 %#v", i, want[i], func() any {
							if i < len(got) {
								return got[i]
							}
							return nil
						}())
					}
				}
				t.FailNow()
			}
			if !slices.Equal(direct.called(), f.called()) {
				t.Fatalf("后台收到的调用不一样\n直接 %v\n协议 %v", direct.called(), f.called())
			}
		})
	}
	// 55 个方法每个都进了列表（SendLocal/nil、SendLocalGroup/nil 是同一方法的第二次调用，UpdateReminder 另有两种改法）
	if n := len(want); n != 59 {
		t.Fatalf("exercise 应该调 55 个方法加两次不带进度的发送、两次另一种改提醒，实际 %d 项", n)
	}
}

// 错误：每个哨兵经协议回各自的码，客户端还原成同一个哨兵、文字照服务端原文；其余错误是 1002 的普通错误
func TestSentinelErrorsRoundTrip(t *testing.T) {
	cases := []struct {
		sentinel error
		code     int
	}{
		{api.ErrLocalChatUnsupported, rpcwire.CodeLocalChatUnsupported},
		{api.ErrLocalChatBusy, rpcwire.CodeLocalChatBusy},
		{api.ErrApprovalGone, rpcwire.CodeApprovalGone},
		{api.ErrQuestionGone, rpcwire.CodeQuestionGone},
		{api.ErrQuoteTarget, rpcwire.CodeQuoteTarget},
		{api.ErrPart, rpcwire.CodeBadPart},
		{api.ErrReadMarkUnsupported, rpcwire.CodeReadMarkUnsupported},
		{api.ErrNoAvatar, rpcwire.CodeNoAvatar},
		{api.ErrNotFound, rpcwire.CodeNotFound},
		{api.ErrLocalGroupUnsupported, rpcwire.CodeLocalGroupUnsupported},
		{api.ErrLocalGroupBusy, rpcwire.CodeLocalGroupBusy},
	}
	for _, tc := range cases {
		t.Run(tc.sentinel.Error(), func(t *testing.T) {
			f := newFake()
			f.err = fmt.Errorf("第 2 块：%w", tc.sentinel) // 带前缀的包装也要认得
			srv := rpcserve.New(f, "test", rpcserve.WithWrites())
			// 线上码
			raw := newRaw(t, srv)
			raw.initialize()
			msg := raw.call("local.decide", map[string]any{"conversation": testKey, "id": "x", "choice": "once"})
			if msg.Error == nil || msg.Error.Code != tc.code || msg.Error.Message != f.err.Error() {
				t.Fatalf("线上错误不对：%+v", msg.Error)
			}
			// 客户端还原
			c := pipeClient(t, srv)
			err := c.DecideLocal(context.Background(), testKey, "x", api.ApproveOnce)
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("errors.Is 不成立：%v", err)
			}
			if err.Error() != f.err.Error() {
				t.Fatalf("文字不是服务端原文：%q", err.Error())
			}
			// 订阅类方法的错误也还原
			if _, err := c.WatchLocalQuestions(context.Background()); !errors.Is(err, tc.sentinel) {
				t.Fatalf("订阅的错误没还原：%v", err)
			}
		})
	}
	t.Run("其余错误", func(t *testing.T) {
		f := newFake()
		f.err = errors.New("磁盘满了")
		srv := rpcserve.New(f, "test", rpcserve.WithWrites())
		raw := newRaw(t, srv)
		raw.initialize()
		msg := raw.call("local.stop", map[string]any{"conversation": testKey})
		if msg.Error == nil || msg.Error.Code != rpcwire.CodeFailed || msg.Error.Message != "磁盘满了" {
			t.Fatalf("普通错误应是 1002 带原文：%+v", msg.Error)
		}
		err := pipeClient(t, srv).StopLocal(context.Background(), testKey)
		var we *rpcwire.Error
		if !errors.As(err, &we) || we.Code != rpcwire.CodeFailed {
			t.Fatalf("普通错误应保持协议错误：%v", err)
		}
	})
	t.Run("转发失败带状态值", func(t *testing.T) {
		f := newFake()
		f.err = errors.New("太大")
		c := pipeClient(t, rpcserve.New(f, "test", rpcserve.WithWrites()))
		res, err := c.ReadForward(context.Background(), api.ForwardRequest{ID: "f"})
		if err == nil || res.Value.State != "too_large" {
			t.Fatalf("失败时界面要靠 Value.State 挑文案：%+v %v", res, err)
		}
	})
	t.Run("头像缺失是 Failed 加 ErrNoAvatar", func(t *testing.T) {
		f := newFake()
		f.err = api.ErrNoAvatar
		c := pipeClient(t, rpcserve.New(f, "test", rpcserve.WithWrites()))
		res, err := c.ReadAvatar(context.Background(), "user:12345")
		if !errors.Is(err, api.ErrNoAvatar) || res.State != api.Failed || !errors.Is(res.Err, api.ErrNoAvatar) {
			t.Fatalf("%+v %v", res, err)
		}
	})
}

// 默认不开写方法；开了的也只开后台真实现了的
func TestWritesOffByDefaultAndCapsFilter(t *testing.T) {
	writes := writeMethodNames()
	if len(writes) < 44 {
		t.Fatalf("协议里第 2 批的请求方法只有 %d 个", len(writes))
	}
	f := newFake()

	t.Run("默认", func(t *testing.T) {
		raw := newRaw(t, rpcserve.New(f, "test"))
		before := raw.call("local.send", map[string]any{"conversation": testKey, "text": "x"})
		if before.Error == nil || before.Error.Code != rpcwire.CodeMethodNotFound {
			t.Fatalf("握手前调写方法应是 -32601（不是 1003）：%+v", before.Error)
		}
		methods := raw.initialize()
		for _, m := range writes {
			if slices.Contains(methods, m) {
				t.Errorf("默认不该列出 %s", m)
			}
			msg := raw.call(m, map[string]any{})
			if msg.Error == nil || msg.Error.Code != rpcwire.CodeMethodNotFound {
				t.Errorf("默认调 %s 应是 -32601：%+v", m, msg.Error)
			}
		}
		for _, m := range []string{"runtime.read", "messages.read", "settings.read"} {
			if !slices.Contains(methods, m) {
				t.Errorf("第 1 批的 %s 应该一直开", m)
			}
		}
		if len(f.called()) != 0 {
			t.Fatalf("默认配置下后台被调了：%v", f.called())
		}
	})
	t.Run("WithWrites", func(t *testing.T) {
		raw := newRaw(t, rpcserve.New(f, "test", rpcserve.WithWrites()))
		methods := raw.initialize()
		for _, m := range writes {
			if !slices.Contains(methods, m) {
				t.Errorf("WithWrites 应列出 %s", m)
			}
		}
	})
	t.Run("后台没实现接口", func(t *testing.T) {
		raw := newRaw(t, rpcserve.New(bareReader{}, "test", rpcserve.WithWrites()))
		methods := raw.initialize()
		for _, m := range writes {
			if slices.Contains(methods, m) {
				t.Errorf("后台没实现，不该列出 %s", m)
			}
		}
		// 调了回 1001，不是 -32601 也不 panic
		for _, m := range []string{"local.send", "local_group.send", "local_group.create", "local_group.stop", "whisper.send", "whisper_log.read", "whisper_log.subscribe", "whisper_log.unsubscribe", "local.decide", "checkpoints.list", "local_run.subscribe", "local_approvals.unsubscribe", "avatar.read"} {
			msg := raw.call(m, map[string]any{})
			if msg.Error == nil || msg.Error.Code != rpcwire.CodeUnsupported {
				t.Errorf("%s 应回 1001：%+v", m, msg.Error)
			}
		}
	})
}

type bareReader struct{ api.Reader }

// 草稿：stream 非空才推 local.draft，且先于回复；stream 空传 nil、不推
func TestDraftNotifications(t *testing.T) {
	srv := rpcserve.New(newFake(), "test", rpcserve.WithWrites())
	raw := newRaw(t, srv)
	raw.initialize()
	msgs := raw.callAll("local.send", map[string]any{"conversation": testKey, "text": "你好", "stream": "d7"})
	if len(msgs) != 4 {
		t.Fatalf("应是 3 条草稿通知加 1 条回复，实际 %d 条", len(msgs))
	}
	for i, m := range msgs[:3] {
		if m.Method != rpcwire.MethodLocalDraft || len(m.ID) != 0 {
			t.Fatalf("第 %d 条应是 local.draft 通知：%+v", i, m)
		}
		var n rpcwire.LocalDraftNotice
		if err := json.Unmarshal(m.Params, &n); err != nil || n.Stream != "d7" || n.Draft.TurnID != "t1" {
			t.Fatalf("草稿通知内容不对：%s %v", m.Params, err)
		}
	}
	if msgs[3].Error != nil || len(msgs[3].ID) == 0 {
		t.Fatalf("最后一条应是成功的回复：%+v", msgs[3])
	}
	msgs = raw.callAll("local.send", map[string]any{"conversation": testKey, "text": "不要"})
	if len(msgs) != 1 || msgs[0].Error != nil {
		t.Fatalf("没带 stream 不该推草稿：%+v", msgs)
	}
}

// 本地群聊：草稿和过程通知用同一个流号，按后台回调的先后推，都先于回复；没带 stream 不推
func TestGroupSendNotifications(t *testing.T) {
	srv := rpcserve.New(newFake(), "test", rpcserve.WithWrites())
	raw := newRaw(t, srv)
	raw.initialize()
	msgs := raw.callAll("local_group.send", map[string]any{"conversation": groupKey, "text": "你好", "mentions": []string{"a"}, "stream": "d9"})
	want := []string{rpcwire.MethodLocalDraft, rpcwire.MethodLocalDraft, rpcwire.MethodLocalGroupProcess, rpcwire.MethodLocalDraft}
	if len(msgs) != len(want)+1 {
		t.Fatalf("应是 %d 条通知加 1 条回复，实际 %d 条", len(want), len(msgs))
	}
	for i, m := range msgs[:len(want)] {
		if m.Method != want[i] || len(m.ID) != 0 {
			t.Fatalf("第 %d 条应是 %s 通知：%+v", i, want[i], m)
		}
		var head struct{ Stream string }
		if json.Unmarshal(m.Params, &head) != nil || head.Stream != "d9" {
			t.Fatalf("第 %d 条的流号不对：%s", i, m.Params)
		}
	}
	var pn rpcwire.LocalGroupProcessNotice
	if err := json.Unmarshal(msgs[2].Params, &pn); err != nil || pn.Turn != "0" || len(pn.Steps) != 1 || pn.Steps[0].Tool != "read" {
		t.Fatalf("过程通知内容不对：%s %v", msgs[2].Params, err)
	}
	if last := msgs[len(want)]; last.Error != nil || len(last.ID) == 0 {
		t.Fatalf("最后一条应是成功的回复：%+v", last)
	}
	msgs = raw.callAll("local_group.send", map[string]any{"conversation": groupKey, "text": "不要进度"})
	if len(msgs) != 1 || msgs[0].Error != nil {
		t.Fatalf("没带 stream 不该推通知：%+v", msgs)
	}
}

// 订阅：snapshot 带 version；取消订阅和连接断开都收掉后台那头的订阅
func TestSubscriptionsCloseWithConnection(t *testing.T) {
	f := newFake()
	srv := rpcserve.New(f, "test", rpcserve.WithWrites())
	c := pipeClient(t, srv)
	sub, err := c.WatchLocalApprovals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := <-sub.Updates()
	if s.Version != 1 || len(s.Value) != 1 {
		t.Fatalf("初始快照：%+v", s)
	}
	f.approvals.push(nil, 2)
	s = <-sub.Updates()
	if s.Version != 2 || s.State != api.Ready || len(s.Value) != 0 {
		t.Fatalf("空列表的快照：%+v", s)
	}
	sub.Close()
	waitClosed(t, f.approvals)

	// 连接断开
	f2 := newFake()
	c2 := pipeClient(t, rpcserve.New(f2, "test", rpcserve.WithWrites()))
	if _, err := c2.WatchLocalApprovals(context.Background()); err != nil {
		t.Fatal(err)
	}
	c2.C.Close()
	waitClosed(t, f2.approvals)
}

func waitClosed(t *testing.T, s *fakeSub[[]api.LocalApproval]) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.isClosed() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("后台那头的订阅没收掉")
}

// 帧上限：进程内管道不限（界面发的大图 base64 会超 32 MiB），WebSocket 超过就关连接
func TestFrameLimitPipeVsWebSocket(t *testing.T) {
	big := [][]byte{make([]byte, 25<<20)} // base64 后约 33.3 MB，超过 32 MiB
	f := newFake()
	if err := pipeClient(t, rpcserve.New(f, "test", rpcserve.WithWrites())).SendLocalImages(context.Background(), testKey, "大图", big, nil); err != nil {
		t.Fatalf("管道不该限帧：%v", err)
	}
	f2 := newFake()
	if err := wsClient(t, rpcserve.New(f2, "test", rpcserve.WithWrites())).SendLocalImages(context.Background(), testKey, "大图", big, nil); err == nil {
		t.Fatal("WebSocket 超过 32 MiB 应该被关连接")
	}
	if len(f2.called()) != 0 {
		t.Fatalf("超限的请求不该到后台：%v", f2.called())
	}
}

// ───── 原始帧辅助 ─────

type raw struct {
	t  *testing.T
	ct rpcserve.Transport
	id int
}

func newRaw(t *testing.T, srv *rpcserve.Server) *raw {
	st, ct := rpcserve.Pipe()
	go srv.Serve(context.Background(), st)
	t.Cleanup(func() { ct.Close() })
	return &raw{t: t, ct: ct}
}

// callAll 发一个请求，收到它的回复为止的全部消息（含之前的通知）
func (r *raw) callAll(method string, params any) []rpcwire.Message {
	r.t.Helper()
	r.id++
	p, _ := json.Marshal(params)
	b, _ := json.Marshal(rpcwire.Message{JSONRPC: rpcwire.Version, ID: json.RawMessage(strconv.Itoa(r.id)), Method: method, Params: p})
	c := ctx(r.t)
	if err := r.ct.Write(c, b); err != nil {
		r.t.Fatal(err)
	}
	var out []rpcwire.Message
	for {
		raw, err := r.ct.Read(c)
		if err != nil {
			r.t.Fatal(err)
		}
		var m rpcwire.Message
		if err := json.Unmarshal(raw, &m); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, m)
		if len(m.ID) != 0 && string(m.ID) == strconv.Itoa(r.id) {
			return out
		}
	}
}

func (r *raw) call(method string, params any) rpcwire.Message {
	r.t.Helper()
	msgs := r.callAll(method, params)
	return msgs[len(msgs)-1]
}

func (r *raw) initialize() []string {
	r.t.Helper()
	msg := r.call("initialize", rpcwire.InitializeParams{ProtocolVersion: rpcwire.ProtocolVersion, Client: "test"})
	if msg.Error != nil {
		r.t.Fatal(msg.Error)
	}
	var res rpcwire.InitializeResult
	if err := json.Unmarshal(msg.Result, &res); err != nil {
		r.t.Fatal(err)
	}
	return res.Methods
}

// writeMethodNames 是协议里第 2 批的请求方法（不含第 1 批的和通知）
func writeMethodNames() []string {
	read := map[string]bool{"initialize": true, "runtime.read": true, "runtime.subscribe": true, "runtime.unsubscribe": true,
		"overview.read": true, "overview.subscribe": true, "overview.unsubscribe": true, "messages.read": true,
		"conversation.subscribe": true, "conversation.unsubscribe": true, "media.read": true, "faces.index": true, "faces.png": true, "settings.read": true}
	var out []string
	for _, m := range rpcwire.Methods {
		if strings.HasPrefix(m.Name, "selftest.") {
			continue // 只有 Bun 实现（规格-本地私聊切到Bun 第十一节），Go 的服务端不实现，见 TestGoNeverServesSelftest
		}
		if !m.Notification && !read[m.Name] {
			out = append(out, m.Name)
		}
	}
	return out
}

// sumika-ctl 的自检方法（selftest.*）只有 Bun 实现：Go 的服务端哪怕开了写方法、后台对象什么接口都有，也不列出、不分派它们。
// 否则 bun 模式下有人（或将来的改动）能让 Go 绕过 bunOwned 守卫去写本地。
func TestGoNeverServesSelftest(t *testing.T) {
	raw := newRaw(t, rpcserve.New(newFake(), "test", rpcserve.WithWrites()))
	methods := raw.initialize()
	n := 0
	for _, m := range rpcwire.Methods {
		if !strings.HasPrefix(m.Name, "selftest.") {
			continue
		}
		n++
		if slices.Contains(methods, m.Name) {
			t.Errorf("Go 不该列出 %s", m.Name)
		}
		msg := raw.call(m.Name, map[string]any{})
		if msg.Error == nil || msg.Error.Code != rpcwire.CodeMethodNotFound {
			t.Errorf("Go 调 %s 应是 -32601：%+v", m.Name, msg.Error)
		}
	}
	if n != 5 {
		t.Fatalf("协议里的 selftest.* 方法应是 5 个，实际 %d", n)
	}
}
