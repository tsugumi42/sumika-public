package main

// sumika-ctl 在 bun 模式下转给 Bun（ctl_bun.go，规格-本地私聊切到Bun 第十一节）。
// 宿主是真的（bun 模式，假 Bun 子进程），到 Bun 的连接是假传输（fakeRPC）：记收到了哪个协议方法、什么参数，按表回固定结果。
// 覆盖：chat、goal/loop/trust、悄悄话三件拿到 Bun 的结果并翻成和 go 模式同一份 ctlproto 类型；Bun 不在、只读、只看历史阶段、
// 角色不存在的回法；go 模式一个 Bun 请求都没有；还不能用的几条（whisper log、checkpoint）回明确的话。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
	"sumika-native/internal/paths"
	"sumika-native/internal/persona"
	"sumika-native/internal/rpcclient"
	"sumika-native/internal/rpcwire"
)

type bunCtl struct {
	h    *host
	b    *ctlBackend
	fake *fakeRPC
	log  *stateLog
}

func addCharacters(t *testing.T, dataDir string, ids ...string) {
	t.Helper()
	p, err := paths.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		dir := persona.CardDir(p.CharactersDir(), id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "character.yml"), []byte("name: 编造"+id), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// newBunCtl 造 bun 模式的宿主，连着 fakeRPC；receive 为真时把宿主标成接收阶段（真接收要连 QQ，这里只改闸门看的那个状态）
func newBunCtl(t *testing.T, receive bool) *bunCtl {
	t.Helper()
	h, log, _ := bunHostFixture(t)
	addCharacters(t, h.opt.DataDir, "a", "b")
	fake := newFakeRPC()
	fake.results = map[string]any{rpcwire.MethodRuntimeRead: rpcwire.Result[rpcwire.RuntimeView]{State: "ready", Value: &rpcwire.RuntimeView{Mode: "bun"}}}
	tune := h.bunTune
	h.bunTune = func(s *bunSupervisor) {
		tune(s)
		s.dial = func(ctx context.Context, _, _ string) (*rpcclient.Client, error) {
			return rpcclient.New(ctx, fake, "test")
		}
	}
	h.start()
	t.Cleanup(func() { _ = h.shutdown() })
	waitHost(t, "Bun 连上", func() bool { return log.last().LocalOK })
	if receive {
		h.op.Lock()
		h.mode = backend.Receive
		h.op.Unlock()
		t.Cleanup(func() { h.op.Lock(); h.mode = backend.Archive; h.op.Unlock() })
	}
	return &bunCtl{h: h, b: &ctlBackend{s: &shell{host: h}}, fake: fake, log: log}
}

// selftestCalls 是 Bun 收到的 selftest.* 请求（监护自己还会发 runtime.read，不算）
func selftestCalls(f *fakeRPC) []string {
	var out []string
	for _, m := range f.took() {
		if strings.HasPrefix(m, "selftest.") {
			out = append(out, m)
		}
	}
	return out
}

func ctlErr(t *testing.T, err error) *ctlproto.Error {
	t.Helper()
	var ce *ctlproto.Error
	if !errors.As(err, &ce) {
		t.Fatalf("该是控制接口的错：%T %v", err, err)
	}
	return ce
}

func TestCtlChatViaBunGetsReplyAndForwardsOnlyWhatItShould(t *testing.T) {
	t.Parallel()
	e := newBunCtl(t, true)
	e.fake.results[rpcwire.MethodSelftestChat] = rpcwire.SelftestChatResult{
		Status: "done", Character: "a", Topic: "selftest-a", Conversation: "console:a:topic:selftest-a", TurnID: "T1", Reply: "你好呀",
		Tools: []rpcwire.SelftestTool{{Name: "ls", Args: `{"path":"x"}`, Result: "ok", Status: "done"}}, Model: "fake", Route: "p/fake", ElapsedMS: 12,
	}
	// 角色空：Go 先换成主窗口当前角色（宿主配置里的小号角色 a），Bun 收到的一定是具体的 id
	res, err := e.b.Chat(context.Background(), ctlserver.ChatRequest{Text: "在吗", Timeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "done" || res.Reply != "你好呀" || res.Character != "a" || res.Conversation != "console:a:topic:selftest-a" || res.TurnID != "T1" ||
		len(res.Tools) != 1 || res.Tools[0].Name != "ls" || res.Model != "fake" || res.Route != "p/fake" || res.ElapsedMS != 12 {
		t.Fatalf("结果没翻对：%+v", res)
	}
	var p map[string]any
	if err := json.Unmarshal(e.fake.paramsOf(rpcwire.MethodSelftestChat), &p); err != nil {
		t.Fatal(err)
	}
	if p["character"] != "a" || p["text"] != "在吗" || p["timeout_ms"] != float64(90000) {
		t.Fatalf("发给 Bun 的参数不对：%v", p)
	}
	for k := range p { // 参数里除了这几项什么都没有：没有会话 id、话题
		if k != "character" && k != "text" && k != "timeout_ms" && k != "new" {
			t.Errorf("发给 Bun 的参数多了 %q", k)
		}
	}
	if got := selftestCalls(e.fake); len(got) != 1 || got[0] != rpcwire.MethodSelftestChat {
		t.Fatalf("Bun 该只收到一个 selftest.chat：%v", got)
	}
	// 写了角色、new 也原样带过去
	if _, err := e.b.Chat(context.Background(), ctlserver.ChatRequest{Character: "b", New: true, Text: "x", Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(e.fake.paramsOf(rpcwire.MethodSelftestChat), &p); err != nil || p["character"] != "b" || p["new"] != true {
		t.Fatalf("%v %v", p, err)
	}
}

func TestCtlChatViaBunStatusesMapLikeGoMode(t *testing.T) {
	t.Parallel()
	e := newBunCtl(t, true)
	cases := []struct {
		status string
		code   string
		msg    string
	}{
		{"timeout", ctlproto.CodeTimeout, "到了时限"},
		{"waiting_approval", ctlproto.CodeFailed, "停在审批上了"},
		{"error", ctlproto.CodeFailed, "模型报错了"},
		{"stopped", ctlproto.CodeFailed, "没有正常结束"},
		{"busy", ctlproto.CodeFailed, "上一轮还在跑"},
	}
	for _, c := range cases {
		e.fake.results[rpcwire.MethodSelftestChat] = rpcwire.SelftestChatResult{Status: c.status, Character: "a", Reply: "半截", Error: map[string]string{"error": "模型报错了", "busy": "这个自检话题上一轮还在跑，这一句没发"}[c.status],
			Approval: &rpcwire.SelftestApproval{ID: "ap1", Tool: "write"}}
		res, err := e.b.Chat(context.Background(), ctlserver.ChatRequest{Character: "a", Text: "x", Timeout: time.Second})
		ce := ctlErr(t, err)
		if ce.Code != c.code || !strings.Contains(ce.Msg, c.msg) {
			t.Errorf("%s：%s %q", c.status, ce.Code, ce.Msg)
		}
		if res.Status != c.status || res.Approval == nil || res.Approval.ID != "ap1" {
			t.Errorf("%s：失败时 data 里也要带着结果和审批卡：%+v", c.status, res)
		}
	}
}

func TestCtlViaBunWhenBunIsDownNeverFallsBackToGo(t *testing.T) {
	t.Parallel()
	e := newBunCtl(t, true)
	e.h.bunLink.down("测试：Bun 崩了")
	before := len(selftestCalls(e.fake))
	ctx := context.Background()
	_, err1 := e.b.Chat(ctx, ctlserver.ChatRequest{Character: "a", Text: "x", Timeout: time.Second})
	_, err2 := e.b.Slash(ctx, ctlserver.SlashRequest{Command: "goal", Sub: "show", Character: "a", Text: "/goal"})
	_, err3 := e.b.WhisperCreate(ctx, ctlserver.WhisperCreateRequest{Characters: []string{"a"}, SourceText: "x", Say: "y", Timeout: time.Second})
	_, err4 := e.b.WhisperStatus(ctx, "abc")
	_, err5 := e.b.WhisperCancel(ctx, "abc")
	for i, err := range []error{err1, err2, err3, err4, err5} {
		ce := ctlErr(t, err)
		// 回的是“没连上”，不是 Go 自己的“本地私聊现在由 Bun 管”或“只看历史”（那说明 Go 的 DesktopReader 被调了）
		if ce.Code != ctlproto.CodeFailed || !strings.Contains(ce.Msg, "本地后台没连上") || !strings.Contains(ce.Msg, "Bun 崩了") {
			t.Errorf("第 %d 个：%s %q", i, ce.Code, ce.Msg)
		}
		if strings.Contains(ce.Msg, "由 Bun 管") {
			t.Errorf("第 %d 个：Go 的守卫被碰到了：%q", i, ce.Msg)
		}
	}
	if len(selftestCalls(e.fake)) != before {
		t.Fatalf("Bun 不在，不该有请求发出去：%v", e.fake.took())
	}
}

func TestCtlViaBunReadOnlyBunHasNoSelftestMethods(t *testing.T) {
	t.Parallel()
	e := newBunCtl(t, true)
	// 重连一个只读的 Bun：initialize 里没有 selftest.*
	f2 := newFakeRPC()
	var keep []string
	for _, m := range f2.methods {
		if !strings.HasPrefix(m, "selftest.") {
			keep = append(keep, m)
		}
	}
	f2.methods = keep
	c, err := rpcclient.New(context.Background(), f2, "test")
	if err != nil {
		t.Fatal(err)
	}
	e.h.bunLink.up(rpcclient.NewFull(c))
	_, err = e.b.Chat(context.Background(), ctlserver.ChatRequest{Character: "a", Text: "x", Timeout: time.Second})
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeFailed || !strings.Contains(ce.Msg, "只读") {
		t.Fatalf("%s %q", ce.Code, ce.Msg)
	}
	if len(f2.took()) != 0 {
		t.Fatalf("没有这个方法就不该发请求：%v", f2.took())
	}
}

func TestCtlViaBunArchiveAndMissingCharacterAnswerLikeGoMode(t *testing.T) {
	t.Parallel()
	// 只看历史阶段：和 go 模式同一句 readonly，一个 Bun 请求都不发
	e := newBunCtl(t, false)
	ctx := context.Background()
	_, err := e.b.Chat(ctx, ctlserver.ChatRequest{Character: "a", Text: "x", Timeout: time.Second})
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeReadOnly || ce.Msg != "只看历史模式不能对话，请先开始接收" {
		t.Fatalf("%s %q", ce.Code, ce.Msg)
	}
	_, err = e.b.Slash(ctx, ctlserver.SlashRequest{Command: "goal", Sub: "show", Character: "a", Text: "/goal"})
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeReadOnly || !strings.Contains(ce.Msg, "只看历史模式不能设目标或定期任务") {
		t.Fatalf("%s %q", ce.Code, ce.Msg)
	}
	for _, f := range []func() error{
		func() error {
			_, err := e.b.WhisperCreate(ctx, ctlserver.WhisperCreateRequest{Characters: []string{"a"}, Timeout: time.Second})
			return err
		},
		func() error { _, err := e.b.WhisperStatus(ctx, "abc"); return err },
		func() error { _, err := e.b.WhisperCancel(ctx, "abc"); return err },
	} {
		if ce := ctlErr(t, f()); ce.Code != ctlproto.CodeReadOnly || !strings.Contains(ce.Msg, "只看历史模式不能发悄悄话") {
			t.Fatalf("%s %q", ce.Code, ce.Msg)
		}
	}
	if n := len(selftestCalls(e.fake)); n != 0 {
		t.Fatalf("只看历史阶段不该有 Bun 请求：%v", e.fake.took())
	}
	// 角色不存在：invalid“没有这个角色”，不发 Bun 请求
	e2 := newBunCtl(t, true)
	_, err = e2.b.Chat(ctx, ctlserver.ChatRequest{Character: "没有这位", Text: "x", Timeout: time.Second})
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeInvalid || ce.Msg != "没有这个角色" {
		t.Fatalf("%s %q", ce.Code, ce.Msg)
	}
	_, err = e2.b.Slash(ctx, ctlserver.SlashRequest{Command: "goal", Sub: "show", Character: "没有这位", Text: "/goal"})
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeInvalid || ce.Msg != "没有这个角色" {
		t.Fatalf("%s %q", ce.Code, ce.Msg)
	}
	if n := len(selftestCalls(e2.fake)); n != 0 {
		t.Fatalf("角色不存在不该有 Bun 请求：%v", e2.fake.took())
	}
	// Bun 说角色不存在（Bun 的名单和 Go 的目录对不上时）：同样 invalid
	e2.fake.errs = map[string]*rpcwire.Error{rpcwire.MethodSelftestChat: {Code: rpcwire.CodeSelftestCharacter, Message: "没有这个角色"}}
	_, err = e2.b.Chat(ctx, ctlserver.ChatRequest{Character: "b", Text: "x", Timeout: time.Second})
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeInvalid || ce.Msg != "没有这个角色" {
		t.Fatalf("%s %q", ce.Code, ce.Msg)
	}
}

func TestCtlSlashViaBun(t *testing.T) {
	t.Parallel()
	e := newBunCtl(t, true)
	ctx := context.Background()
	at := time.Date(2026, 11, 12, 9, 30, 0, 0, time.FixedZone("x", 8*3600))
	e.fake.results[rpcwire.MethodSelftestSlash] = rpcwire.SelftestSlashResult{
		Character: "a", Topic: "selftest-a", Conversation: "console:a:topic:selftest-a", Reply: "目标已设定", Started: true,
		Goal:  &rpcwire.SelftestGoal{Text: "写报告", Status: "active", Rounds: 1, MaxRounds: 10, TokensUsed: 5, Line: "目标：写报告"},
		Loops: []rpcwire.SelftestLoop{{ID: 3, Status: "running", Prompt: "报天气", IntervalSec: 1800, Runs: 2, NextRun: at.Format(time.RFC3339), Line: "#3", Kind: "weekly", At: "09:30", Days: []int{0, 3}, Notify: "errors", NewChat: true}},
	}
	nc := true
	res, err := e.b.Slash(ctx, ctlserver.SlashRequest{Command: "loop", Sub: "add", Character: "", Text: "报天气", Loop: &ctlproto.LoopPatch{Kind: "weekly", At: "09:30", Days: []int{0, 3}, NewChat: &nc, Notify: "errors"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply != "目标已设定" || !res.Started || res.Goal == nil || res.Goal.Text != "写报告" || res.Goal.MaxRounds != 10 || len(res.Loops) != 1 {
		t.Fatalf("%+v", res)
	}
	l := res.Loops[0]
	if l.ID != 3 || l.Kind != "weekly" || l.At != "09:30" || len(l.Days) != 2 || l.Days[1] != 3 || !l.NewChat || l.Notify != "errors" || l.NextRun != at.Format(time.RFC3339) {
		t.Fatalf("定期任务没翻对：%+v", l)
	}
	var p rpcwire.SelftestSlashParams
	if err := json.Unmarshal(e.fake.paramsOf(rpcwire.MethodSelftestSlash), &p); err != nil {
		t.Fatal(err)
	}
	if p.Command != "loop" || p.Sub != "add" || p.Character != "a" || p.Text != "报天气" || p.Loop == nil || p.Loop.NewChat != "new" || p.Loop.Kind != "weekly" || len(p.Loop.Days) != 2 {
		t.Fatalf("发给 Bun 的参数不对：%+v %+v", p, p.Loop)
	}
	// new_chat 三态：nil 不写、false 是 same
	f := false
	for _, tc := range []struct {
		in   *bool
		want string
	}{{nil, ""}, {&nc, "new"}, {&f, "same"}} {
		if got := newChatWire(tc.in); got != tc.want {
			t.Errorf("newChatWire = %q，应是 %q", got, tc.want)
		}
	}
	// Bun 说命令不合格：invalid；说没有这个功能（trust）：failed 带原文；命令自己的错：failed 带原文
	e.fake.errs = map[string]*rpcwire.Error{rpcwire.MethodSelftestSlash: {Code: rpcwire.CodeSelftestInvalid, Message: "目标文字和子命令词撞了"}}
	_, err = e.b.Slash(ctx, ctlserver.SlashRequest{Command: "goal", Sub: "set", Character: "a", Text: "/goal pause"})
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeInvalid || !strings.Contains(ce.Msg, "撞了") {
		t.Fatalf("%s %q", ce.Code, ce.Msg)
	}
	e.fake.errs = map[string]*rpcwire.Error{rpcwire.MethodSelftestSlash: {Code: rpcwire.CodeUnsupported, Message: "Bun 后台还没有项目说明信任（/trust）"}}
	_, err = e.b.Slash(ctx, ctlserver.SlashRequest{Command: "trust", Character: "a", Text: "/trust"})
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeFailed || !strings.Contains(ce.Msg, "还没有项目说明信任") {
		t.Fatalf("%s %q", ce.Code, ce.Msg)
	}
	e.fake.errs = map[string]*rpcwire.Error{rpcwire.MethodSelftestSlash: {Code: rpcwire.CodeFailed, Message: "这个会话正在生成，先点停止"}}
	_, err = e.b.Slash(ctx, ctlserver.SlashRequest{Command: "goal", Sub: "set", Character: "a", Text: "/goal 写"})
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeFailed || !strings.Contains(ce.Msg, "正在生成") {
		t.Fatalf("%s %q", ce.Code, ce.Msg)
	}
}

func TestCtlWhisperViaBun(t *testing.T) {
	t.Parallel()
	e := newBunCtl(t, true)
	ctx := context.Background()
	done := rpcwire.SelftestWhisperResult{Batch: "B1", ElapsedMS: 40, Entries: []rpcwire.SelftestWhisperEntry{{Character: "a", State: "done", Note: "记下了"}, {Character: "b", State: "done"}}}
	e.fake.results[rpcwire.MethodSelftestWhisper] = done
	e.fake.results[rpcwire.MethodSelftestWhisperStatus] = done
	e.fake.results[rpcwire.MethodSelftestWhisperCancel] = done
	res, err := e.b.WhisperCreate(ctx, ctlserver.WhisperCreateRequest{Characters: []string{"a", "b"}, SourceText: "群友说", QuoteName: "某人", Say: "记一下", Timeout: 30 * time.Second})
	if err != nil || res.Status != ctlproto.WhisperDone || res.Batch != "B1" || len(res.Entries) != 2 || res.Entries[0].Note != "记下了" || res.ElapsedMS != 40 {
		t.Fatalf("%+v %v", res, err)
	}
	var p rpcwire.SelftestWhisperParams
	if err := json.Unmarshal(e.fake.paramsOf(rpcwire.MethodSelftestWhisper), &p); err != nil || len(p.Characters) != 2 || p.SourceText != "群友说" || p.QuoteName != "某人" || p.Say != "记一下" || p.TimeoutMS != 30000 {
		t.Fatalf("%+v %v", p, err)
	}
	if res, err = e.b.WhisperStatus(ctx, "B1"); err != nil || res.Status != ctlproto.WhisperDone {
		t.Fatalf("%+v %v", res, err)
	}
	if res, err = e.b.WhisperCancel(ctx, "B1"); err != nil || res.Status != ctlproto.WhisperDone {
		t.Fatalf("%+v %v", res, err)
	}
	var bp rpcwire.SelftestWhisperBatchParams
	if err := json.Unmarshal(e.fake.paramsOf(rpcwire.MethodSelftestWhisperCancel), &bp); err != nil || bp.Batch != "B1" {
		t.Fatalf("%+v %v", bp, err)
	}
	// 到时限还没落定：timeout，data 带着；有失败的：failed
	e.fake.results[rpcwire.MethodSelftestWhisper] = rpcwire.SelftestWhisperResult{Batch: "B2", Pending: true, Entries: []rpcwire.SelftestWhisperEntry{{Character: "a", State: "processing"}}}
	_, err = e.b.WhisperCreate(ctx, ctlserver.WhisperCreateRequest{Characters: []string{"a"}, SourceText: "x", Say: "y", Timeout: time.Second})
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeTimeout {
		t.Fatalf("%s", ce.Code)
	}
	e.fake.results[rpcwire.MethodSelftestWhisper] = rpcwire.SelftestWhisperResult{Batch: "B3", Entries: []rpcwire.SelftestWhisperEntry{{Character: "a", State: "failed", Detail: "出错"}}}
	_, err = e.b.WhisperCreate(ctx, ctlserver.WhisperCreateRequest{Characters: []string{"a"}, SourceText: "x", Say: "y", Timeout: time.Second})
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeFailed {
		t.Fatalf("%s", ce.Code)
	}
	// 批号没见过：invalid（和 go 模式的 ErrWhisperBatch 一样）；角色不存在：invalid
	e.fake.errs = map[string]*rpcwire.Error{rpcwire.MethodSelftestWhisperStatus: {Code: rpcwire.CodeSelftestInvalid, Message: "没有这个批号：只认这次运行里发出的悄悄话"}}
	_, err = e.b.WhisperStatus(ctx, "NOPE")
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeInvalid || !strings.Contains(ce.Msg, "没有这个批号") {
		t.Fatalf("%s %q", ce.Code, ce.Msg)
	}
	e.fake.errs = map[string]*rpcwire.Error{rpcwire.MethodSelftestWhisper: {Code: rpcwire.CodeSelftestCharacter, Message: "没有这个角色"}}
	_, err = e.b.WhisperCreate(ctx, ctlserver.WhisperCreateRequest{Characters: []string{"zz"}, SourceText: "x", Say: "y", Timeout: time.Second})
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeInvalid || ce.Msg != "没有这个角色" {
		t.Fatalf("%s %q", ce.Code, ce.Msg)
	}
}

func TestCtlNotYetCommandsSayWhyAndNeverReachBunOrGo(t *testing.T) {
	t.Parallel()
	e := newBunCtl(t, true)
	ctx := context.Background()
	before := len(selftestCalls(e.fake))
	_, e1 := e.b.WhisperLog(ctx, ctlproto.WhisperLogArgs{Conversation: "console:a:topic:selftest-a"})
	_, e2 := e.b.WhisperList(ctx, true)
	_, e3 := e.b.CheckpointList(ctx, "console:a")
	_, e4 := e.b.CheckpointPreview(ctx, "console:a", "x", "file")
	_, e5 := e.b.CheckpointRestore(ctx, "console:a", "x", "file", "v")
	for i, err := range []error{e1, e2, e3, e4, e5} {
		ce := ctlErr(t, err)
		if ce.Code != ctlproto.CodeFailed || !strings.Contains(ce.Msg, "Bun 模式下 sumika-ctl 还不能") {
			t.Errorf("第 %d 个：%s %q", i, ce.Code, ce.Msg)
		}
	}
	if len(selftestCalls(e.fake)) != before {
		t.Fatalf("这几条不该发请求给 Bun：%v", e.fake.took())
	}
}

func TestCtlGoModeNeverTouchesBun(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	addCharacters(t, opt.DataDir, "a")
	var log stateLog
	h := newHost(opt, log.add)
	h.bunSpawn = func(bunSpec, func(string, ...any)) (bunProc, error) {
		t.Error("go 模式不该拉 Bun")
		return nil, errors.New("x")
	}
	h.start()
	t.Cleanup(func() { _ = h.shutdown() })
	b := &ctlBackend{s: &shell{host: h}}
	if link, _, on := b.bunMode(); on || link != nil {
		t.Fatalf("go 模式 bunMode 该是关的")
	}
	ctx := context.Background()
	// go 模式（这里是 archive）：直调 DesktopReader，回它原来的话；没有 Bun 连接可发
	_, err := b.Chat(ctx, ctlserver.ChatRequest{Character: "a", Text: "x", Timeout: time.Second})
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeReadOnly || ce.Msg != "只看历史模式不能对话，请先开始接收" {
		t.Fatalf("%s %q", ce.Code, ce.Msg)
	}
	_, err = b.Slash(ctx, ctlserver.SlashRequest{Command: "goal", Sub: "show", Character: "a", Text: "/goal"})
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeReadOnly || !strings.Contains(ce.Msg, "只看历史模式不能设目标或定期任务") {
		t.Fatalf("%s %q", ce.Code, ce.Msg)
	}
	_, err = b.WhisperCreate(ctx, ctlserver.WhisperCreateRequest{Characters: []string{"a"}, SourceText: "x", Say: "y", Timeout: time.Second})
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeReadOnly {
		t.Fatalf("%s %q", ce.Code, ce.Msg)
	}
	if h.bunLink != nil {
		t.Fatal("go 模式不该有 Bun 的连接")
	}
	// go 模式里 Go 的错误也没被换成“还不能”那句
	_, err = b.WhisperLog(ctx, ctlproto.WhisperLogArgs{Conversation: "console:a:topic:selftest-a"})
	if err != nil && strings.Contains(err.Error(), "Bun 模式下") {
		t.Fatalf("go 模式不该出现 bun 的说法：%v", err)
	}
	if errors.Is(err, api.ErrLocalOwnedByBun) {
		t.Fatalf("go 模式不该回由 Bun 管：%v", err)
	}
}

// Bun 回的错误文字（模型报错可能带密钥或号码片段）和 go 模式一样过打码再出到命令行；每条路径都测：chat、slash、whisper 各自的 invalid 和 failed
func TestCtlViaBunRedactsErrorText(t *testing.T) {
	t.Parallel()
	e := newBunCtl(t, true)
	ctx := context.Background()
	secret := strings.Repeat("7", 12) // 长数字：打码会遮成掩码，原样出去就是泄露
	leak := func(where string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s：该报错", where)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s：错误文字没打码：%q", where, err.Error())
		}
		if ce := new(ctlproto.Error); errors.As(err, &ce) && strings.Contains(ce.Msg, secret) {
			t.Errorf("%s：Msg 没打码：%q", where, ce.Msg)
		}
	}
	fail := func(code int) *rpcwire.Error {
		return &rpcwire.Error{Code: code, Message: "模型报错了 号码 " + secret}
	}
	for _, code := range []int{rpcwire.CodeFailed, rpcwire.CodeSelftestInvalid, rpcwire.CodeLocalChatBusy} {
		e.fake.errs = map[string]*rpcwire.Error{rpcwire.MethodSelftestChat: fail(code), rpcwire.MethodSelftestSlash: fail(code), rpcwire.MethodSelftestWhisper: fail(code), rpcwire.MethodSelftestWhisperStatus: fail(code)}
		_, err := e.b.Chat(ctx, ctlserver.ChatRequest{Character: "a", Text: "x", Timeout: time.Second})
		leak("chat", err)
		_, err = e.b.Slash(ctx, ctlserver.SlashRequest{Command: "goal", Sub: "show", Character: "a", Text: "/goal"})
		leak("slash", err)
		_, err = e.b.WhisperCreate(ctx, ctlserver.WhisperCreateRequest{Characters: []string{"a"}, SourceText: "x", Say: "y", Timeout: time.Second})
		leak("whisper create", err)
		_, err = e.b.WhisperStatus(ctx, "B1")
		leak("whisper status", err)
	}
	// 打码以后其余的话还在：不是整句换掉
	e.fake.errs = map[string]*rpcwire.Error{rpcwire.MethodSelftestChat: fail(rpcwire.CodeFailed)}
	_, err := e.b.Chat(ctx, ctlserver.ChatRequest{Character: "a", Text: "x", Timeout: time.Second})
	if ce := ctlErr(t, err); ce.Code != ctlproto.CodeFailed || !strings.Contains(ce.Msg, "模型报错了") {
		t.Fatalf("%s %q", ce.Code, ce.Msg)
	}
}

// 时限不足 1 毫秒：ctlserver 放行（大于 0），换成毫秒不能得 0（Bun 会回 invalid，go 模式却接受），向上取整成 1
func TestCtlViaBunSubMillisecondTimeoutRoundsUp(t *testing.T) {
	t.Parallel()
	e := newBunCtl(t, true)
	ctx := context.Background()
	e.fake.results[rpcwire.MethodSelftestChat] = rpcwire.SelftestChatResult{Status: "done", Character: "a", Reply: "好"}
	e.fake.results[rpcwire.MethodSelftestWhisper] = rpcwire.SelftestWhisperResult{Batch: "B1", Entries: []rpcwire.SelftestWhisperEntry{{Character: "a", State: "done"}}}
	if _, err := e.b.Chat(ctx, ctlserver.ChatRequest{Character: "a", Text: "x", Timeout: 500 * time.Microsecond}); err != nil {
		t.Fatal(err)
	}
	var cp rpcwire.SelftestChatParams
	if err := json.Unmarshal(e.fake.paramsOf(rpcwire.MethodSelftestChat), &cp); err != nil || cp.TimeoutMS != 1 {
		t.Fatalf("chat 的 timeout_ms 该是 1：%+v %v", cp, err)
	}
	if _, err := e.b.WhisperCreate(ctx, ctlserver.WhisperCreateRequest{Characters: []string{"a"}, SourceText: "x", Say: "y", Timeout: 500 * time.Microsecond}); err != nil {
		t.Fatal(err)
	}
	var wp rpcwire.SelftestWhisperParams
	if err := json.Unmarshal(e.fake.paramsOf(rpcwire.MethodSelftestWhisper), &wp); err != nil || wp.TimeoutMS != 1 {
		t.Fatalf("whisper 的 timeout_ms 该是 1：%+v %v", wp, err)
	}
	// 整毫秒和更长的不变
	for d, want := range map[time.Duration]int64{time.Millisecond: 1, 1500 * time.Microsecond: 2, 90 * time.Second: 90000} {
		if got := timeoutMS(d); got != want {
			t.Errorf("timeoutMS(%v) = %d，应是 %d", d, got, want)
		}
	}
}

// 本地可用阶段（零小号、只开本地）里本地私聊照旧归 Bun：bunMode 说 on，但不把 phaseLocal 当只看历史。
// ctl 的转发条件就是 on 且 archive=false；这里钉住它，免得以后把“Go 的 receive 模式”误当成“Bun 不能写本地”。
func TestCtlBunModeForwardsLocalChatInLocalPhase(t *testing.T) {
	t.Parallel()
	opt := phaseLocalOptions(t, false)
	cfgFile := filepath.Join(opt.DataDir, "config", "sumika.yml")
	b, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgFile, append(b, []byte("backend:\n  local: bun\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	h, log, _ := bunHostOn(t, opt)
	h.start()
	t.Cleanup(func() { _ = h.shutdown() })
	waitHost(t, "Bun 连上", func() bool { return log.last().LocalOK })
	if s := log.last(); s.Phase != phaseLocal {
		t.Fatalf("零小号该停在本地可用：%+v", s)
	}
	bctl := &ctlBackend{s: &shell{host: h}}
	link, archive, on := bctl.bunMode()
	if !on || archive || link == nil {
		t.Fatalf("本地可用阶段 bunMode 该把本地私聊转给 Bun：on=%v archive=%v link=%v", on, archive, link)
	}
}
