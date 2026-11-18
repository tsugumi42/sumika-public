package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/llm"
	"sumika-native/internal/segview"
	"sumika-native/internal/turn"
)

// liveFake 是能控制“一轮什么时候结束”的假后台：记下插话、排队、放行、放弃、撤回，
// 每一轮的进度订阅由测试自己推。不碰真实数据。
type liveFake struct {
	api.Reader
	mu       sync.Mutex
	got      []api.LocalInputSubmission
	n        uint64
	runs     map[string]chan api.Snapshot[api.LocalRunSnapshot]
	stops    []uint64
	released []string
	dropped  []string
	withdrew []string
	held     []api.HeldInput

	withdrawErr error
	releaseErr  error
}

func (f *liveFake) run(id string) chan api.Snapshot[api.LocalRunSnapshot] {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runs == nil {
		f.runs = map[string]chan api.Snapshot[api.LocalRunSnapshot]{}
	}
	ch := f.runs[id]
	if ch == nil {
		ch = make(chan api.Snapshot[api.LocalRunSnapshot], 8)
		f.runs[id] = ch
	}
	return ch
}

func (f *liveFake) SubmitLocal(_ context.Context, _ api.ConversationKey, s api.LocalInputSubmission) (api.InputReceipt, error) {
	f.mu.Lock()
	f.got = append(f.got, s)
	f.n++
	n := f.n
	f.mu.Unlock()
	rec := api.InputReceipt{InputID: "in" + strconv.Itoa(int(n)), RunID: "run" + strconv.Itoa(int(n)), State: "accepted", Sequence: n, Revision: 1}
	if s.Mode == "steer" {
		rec.RunID = s.ExpectedRunID
	}
	return rec, nil
}

func (f *liveFake) StopLocalRun(_ context.Context, _ api.ConversationKey, _ string, through uint64) (api.StopReceipt, error) {
	f.mu.Lock()
	f.stops = append(f.stops, through)
	f.mu.Unlock()
	return api.StopReceipt{}, nil
}

type liveSub struct {
	ch chan api.Snapshot[api.LocalRunSnapshot]
}

func (s liveSub) Updates() <-chan api.Snapshot[api.LocalRunSnapshot] { return s.ch }
func (s liveSub) Close()                                             {}

func (f *liveFake) WatchLocalRun(_ context.Context, _ api.ConversationKey, run string) (api.Subscription[api.LocalRunSnapshot], error) {
	return liveSub{f.run(run)}, nil
}

func (f *liveFake) HeldLocal(context.Context) ([]api.HeldInput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]api.HeldInput(nil), f.held...), nil
}

func (f *liveFake) ReleaseHeldLocal(_ context.Context, _ api.ConversationKey, id string) (api.InputReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.releaseErr != nil {
		return api.InputReceipt{}, f.releaseErr
	}
	f.released = append(f.released, id)
	f.n++
	return api.InputReceipt{InputID: id, RunID: "rel" + strconv.Itoa(int(f.n)), State: "accepted", Sequence: 100 + f.n}, nil
}

func (f *liveFake) DiscardHeldLocal(_ context.Context, _ api.ConversationKey, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropped = append(f.dropped, id)
	return nil
}

func (f *liveFake) WithdrawQueuedLocal(_ context.Context, _ api.ConversationKey, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.withdrawErr != nil {
		return f.withdrawErr
	}
	f.withdrew = append(f.withdrew, id)
	return nil
}

func (f *liveFake) stopCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.stops)
}

func (f *liveFake) subs() []api.LocalInputSubmission {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]api.LocalInputSubmission(nil), f.got...)
}

// finish 让某一轮收尾（排空）。
func (f *liveFake) finish(run string, v api.LocalRunSnapshot) {
	v.RunID, v.Drained, v.State = run, true, "settled"
	f.run(run) <- api.Snapshot[api.LocalRunSnapshot]{Result: api.Result[api.LocalRunSnapshot]{State: api.Ready, Value: v}}
}

func newLiveRig(t *testing.T, f *liveFake) *wbRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:char00"}
	a := &app{ctx: ctx, viewCtx: ctx, sel: key.Conversation, keys: map[string]api.ConversationKey{key.Conversation: key}, reader: f}
	a.msgs = []*msg{{id: "m1", who: "编造角色", isChar: true, actor: "char00", at: 1000, text: "第一句。", blocks: segview.TextBlocks("第一句。")}}
	a.list = a.newList()
	q := &uiQueue{}
	a.postFn = q.post
	tt := ui.NewTester(func(c *ui.Context) { a.workbenchChat(c) }, 900, 700)
	tt.SetScale(1.25)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	return &wbRig{a: a, q: q, tt: tt, key: key, p: a.composerOf(key.Conversation)}
}

func typeIn(t *testing.T, r *wbRig, text string) {
	t.Helper()
	if err := r.tt.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tt.Type(text)
	r.tt.Frame()
}

func composerRect(t *testing.T, r *wbRig) ui.Rect {
	t.Helper()
	rect, ok := r.tt.Find("输入区")
	if !ok {
		t.Fatal("找不到输入区")
	}
	return rect
}

func TestSteerOnEnterWhileBusyKeepsLayoutAndFocus(t *testing.T) {
	f := &liveFake{}
	r := newLiveRig(t, f)
	st := r.beginT(t, f, "先写个排序")
	r.settle()
	idle := composerRect(t, r)
	if _, ok := r.tt.Find("停止生成"); !ok {
		t.Fatal("回复中没字时右下角应当是停止")
	}
	typeIn(t, r, "不对，要稳定排序")
	if _, ok := r.tt.Find("发送（插话）"); !ok {
		t.Fatalf("有字时应当是发送键：%v", r.tt.Texts())
	}
	before := composerRect(t, r)
	r.tt.Key(0, ui.KeyEnter)
	r.settleUntil(t, func() bool { return len(f.subs()) == 2 && r.p.Main.Text == "" })
	got := f.subs()
	if len(got) != 2 || got[1].Mode != "steer" || got[1].ExpectedRunID != "run1" || got[1].Text != "不对，要稳定排序" {
		t.Fatalf("Enter 应当插话到这一轮：%+v", got)
	}
	if r.p.Main.Text != "" {
		t.Fatalf("收下后输入框该清空：%q", r.p.Main.Text)
	}
	if len(st.queued) != 0 || !st.busy || st.runID != "run1" {
		t.Fatalf("插话不另起一轮，也不进排队：%+v", st)
	}
	after := composerRect(t, r)
	if after.H != before.H || after.Y != before.Y || after.H < idle.H-1 {
		t.Fatalf("插话发出后输入区尺寸或位置跳了：空闲 %+v 发前 %+v 发后 %+v", idle, before, after)
	}
	// 插话之后焦点还在输入框：接着打的字落进输入框
	r.tt.Type("还有")
	r.tt.Frame()
	if r.p.Main.Text != "还有" {
		t.Fatalf("发出插话后焦点不该被抢走：%q", r.p.Main.Text)
	}
	t.Logf("输入区 空闲 %+v 发前 %+v 发后 %+v", idle, before, after)
}

func (r *wbRig) beginT(t *testing.T, f *liveFake, text string) *localRun {
	t.Helper()
	r.p.Main.Text = text
	r.a.submitComposer(f, r.key, r.p)
	st := r.a.localRun(r.key.Conversation)
	r.settleUntil(t, func() bool { return st.busy && st.runID != "" })
	if !st.busy {
		t.Fatalf("发出第一句后应当进入回复中：%+v", st)
	}
	return st
}

func TestQueueOnAltEnterShowsRowAndWithdraws(t *testing.T) {
	f := &liveFake{}
	r := newLiveRig(t, f)
	st := r.beginT(t, f, "第一件事")
	typeIn(t, r, "做完再看第二件")
	r.tt.Key(ui.Alt, ui.KeyEnter)
	r.settleUntil(t, func() bool { return len(f.subs()) == 2 && len(st.queued) == 1 })
	got := f.subs()
	if len(got) != 2 || got[1].Mode != "queue" || got[1].ExpectedRunID != "" {
		t.Fatalf("Alt+Enter 应当排队：%+v", got)
	}
	if strings.Contains(r.p.Main.Text, "\n") || r.p.Main.Text != "" {
		t.Fatalf("排队后输入框应当清空且没有换行：%q", r.p.Main.Text)
	}
	if len(st.queued) != 1 || st.queued[0].InputID != "in2" {
		t.Fatalf("排队的话应当列出来：%+v", st.queued)
	}
	if !r.tt.HasText("排队中，说完这一轮再处理：「做完再看第二件」") {
		t.Fatalf("输入框附近应当看得到排队的话：%v", r.tt.Texts())
	}
	// 撤回：后台取消、列表里消失，这一轮照常
	if err := r.tt.Click("取消排队"); err != nil {
		t.Fatal(err)
	}
	r.settleUntil(t, func() bool { return len(f.withdrew) == 1 && len(st.queued) == 0 })
	if len(f.withdrew) != 1 || f.withdrew[0] != "in2" || len(st.queued) != 0 {
		t.Fatalf("取消排队没生效：%v %+v", f.withdrew, st.queued)
	}
	if r.tt.HasText("取消排队") || !st.busy {
		t.Fatalf("取消后列表该空，这一轮照常：%v busy=%v", r.tt.Texts(), st.busy)
	}
}

func TestWithdrawTooLateIsSaidAndRowStays(t *testing.T) {
	f := &liveFake{withdrawErr: turn.ErrInputNotPending}
	r := newLiveRig(t, f)
	st := r.beginT(t, f, "第一件事")
	typeIn(t, r, "排一句")
	r.tt.Key(ui.Alt, ui.KeyEnter)
	r.settleUntil(t, func() bool { return len(st.queued) == 1 })
	if err := r.tt.Click("取消排队"); err != nil {
		t.Fatal(err)
	}
	r.settleUntil(t, func() bool {
		return len(st.queued) == 1 && !st.queued[0].withdrawing && r.tt.HasText("这句话已经开始处理了，取消不了")
	})
	if len(st.queued) != 1 || st.queued[0].withdrawing {
		t.Fatalf("撤不回的话该留在列表里继续跟着：%+v", st.queued)
	}
	if !r.tt.HasText("这句话已经开始处理了，取消不了") {
		t.Fatalf("要照实说取消不了：%v", r.tt.Texts())
	}
}

func TestQueuedRunStartsAfterCurrentEndsAndStopClearsQueue(t *testing.T) {
	f := &liveFake{}
	r := newLiveRig(t, f)
	st := r.beginT(t, f, "第一件事")
	for i, s := range []string{"第二件", "第三件"} {
		r.p.Main.Text = s
		r.a.submitLive(f, r.key, r.p, "queue")
		r.settleUntil(t, func() bool { return len(st.queued) == i+1 })
	}
	if len(st.queued) != 2 || st.through != 3 {
		t.Fatalf("两句都该排上，停止范围要盖住它们：%d 句，through=%d", len(st.queued), st.through)
	}
	// 第一轮正常结束：接上排在最前面那句的那一轮
	f.finish("run1", api.LocalRunSnapshot{Outcome: "done"})
	r.settleUntil(t, func() bool { return st.busy && st.runID == "run2" && len(st.queued) == 1 })
	if !st.busy || st.runID != "run2" || len(st.queued) != 1 {
		t.Fatalf("应当接上第二句的那一轮：busy=%v run=%q 排队 %d", st.busy, st.runID, len(st.queued))
	}
	// 在第二轮里按停止：范围盖住第三句，列表清空
	r.a.stopComposer(f, r.key)
	r.settleUntil(t, func() bool { return f.stopCount() == 1 && len(st.queued) == 0 && st.stopped })
	if len(f.stops) != 1 || f.stops[0] != 3 {
		t.Fatalf("停止的序号范围不对：%v", f.stops)
	}
	if len(st.queued) != 0 || !st.stopped {
		t.Fatalf("停下后排队的先不发：%+v", st)
	}
	f.finish("run2", api.LocalRunSnapshot{Outcome: "stopped"})
	r.settleUntil(t, func() bool { return !st.busy })
	for range 6 { // 停下后不该再接上下一轮：给后续（若有）转几帧的机会，上面已等到停下
		r.settle()
	}
	if st.busy || len(st.queued) != 0 || st.runID != "" {
		t.Fatalf("停下后不该再接上下一轮：%+v", st)
	}
}

func TestLiveSendIgnoredWhenNotRunningOrStopping(t *testing.T) {
	f := &liveFake{}
	r := newLiveRig(t, f)
	r.p.Main.Text = "闲着的时候"
	r.a.submitLive(f, r.key, r.p, "steer")
	r.settle()
	if len(f.subs()) != 0 || r.p.Main.Text != "闲着的时候" {
		t.Fatal("没在回复时 submitLive 不发、草稿留着")
	}
	st := r.beginT(t, f, "开始")
	r.a.stopComposer(f, r.key)
	r.settleUntil(t, func() bool { return f.stopCount() == 1 })
	r.p.Main.Text = "停的时候"
	r.a.submitLive(f, r.key, r.p, "queue")
	r.settle()
	if len(f.subs()) != 1 || len(st.queued) != 0 || r.p.Main.Text != "停的时候" {
		t.Fatal("正在停时不接插话和排队")
	}
}

func TestHeldInputsShownReleasedAndDropped(t *testing.T) {
	f := &liveFake{held: []api.HeldInput{
		{Key: api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:char00"}, InputID: "h1", Text: "第一句没处理的"},
		{Key: api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:char00"}, InputID: "h2", Text: "第二句没处理的"},
		{Key: api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:other"}, InputID: "h3", Text: "别的会话里的"},
	}}
	r := newLiveRig(t, f)
	typeIn(t, r, "正在打的字")
	r.a.refreshHeld()
	r.settleUntil(t, func() bool {
		return len(r.a.heldBy["console:char00"]) == 2 && len(r.a.heldBy["console:other"]) == 1 && r.tt.HasText("第二句没处理的")
	})
	if len(r.a.heldBy["console:char00"]) != 2 || len(r.a.heldBy["console:other"]) != 1 {
		t.Fatalf("所有会话的 held 都要取到：%+v", r.a.heldBy)
	}
	if !r.tt.HasText("第一句没处理的") || !r.tt.HasText("第二句没处理的") || r.tt.HasText("别的会话里的") {
		t.Fatalf("前台只显示本会话的：%v", r.tt.Texts())
	}
	if !r.tt.HasText("这句话还没处理（重启时没来得及回）") {
		t.Fatalf("要标明还没处理：%v", r.tt.Texts())
	}
	// 正在输入时列表出现，焦点不被抢走
	r.tt.Type("继续")
	r.tt.Frame()
	if r.p.Main.Text != "正在打的字继续" {
		t.Fatalf("held 出现后输入框不该失焦：%q", r.p.Main.Text)
	}
	if _, ok := r.tt.Find("现在发"); !ok {
		t.Fatalf("要有“现在发”：%v", r.tt.Texts())
	}
	if _, ok := r.tt.Find("算了"); !ok {
		t.Fatalf("要有“算了”：%v", r.tt.Texts())
	}
}

func TestHeldReleaseAttachesRunAndDiscardKeepsMessages(t *testing.T) {
	f := &liveFake{}
	r := newLiveRig(t, f)
	k := r.key
	r.a.heldBy = map[string][]api.HeldInput{k.Conversation: {
		{Key: k, InputID: "h1", Text: "放行这句"}, {Key: k, InputID: "h2", Text: "放弃这句"}, {Key: k, InputID: "h3", Text: "还留着"}}}
	r.settle()
	n := len(r.a.msgs)
	r.a.discardHeld(k, r.a.heldBy[k.Conversation][1])
	r.settleUntil(t, func() bool { return len(r.a.heldBy[k.Conversation]) == 2 })
	if len(f.dropped) != 1 || f.dropped[0] != "h2" || len(r.a.heldBy[k.Conversation]) != 2 {
		t.Fatalf("放弃没生效：%v %+v", f.dropped, r.a.heldBy)
	}
	if len(r.a.msgs) != n {
		t.Fatalf("放弃不能动聊天里的消息：%d → %d", n, len(r.a.msgs))
	}
	r.a.releaseHeld(k, r.a.heldBy[k.Conversation][0])
	r.settleUntil(t, func() bool { return len(r.a.heldBy[k.Conversation]) == 1 && r.a.localRun(k.Conversation).busy })
	st := r.a.localRun(k.Conversation)
	if len(f.released) != 1 || f.released[0] != "h1" || !st.busy || !strings.HasPrefix(st.runID, "rel") {
		t.Fatalf("放行后应当跟上它那一轮：%v busy=%v run=%q", f.released, st.busy, st.runID)
	}
	if len(r.a.heldBy[k.Conversation]) != 1 || r.a.heldBy[k.Conversation][0].InputID != "h3" {
		t.Fatalf("放行的该从列表里去掉，别的不动：%+v", r.a.heldBy)
	}
	// 放行失败：写原因、这句话还在
	f.releaseErr = errors.New("sqlite: database is locked (5) 内部细节")
	st.busy = false
	r.a.releaseHeld(k, r.a.heldBy[k.Conversation][0])
	r.settleUntil(t, func() bool { return st.err != "" })
	if len(r.a.heldBy[k.Conversation]) != 1 || st.err == "" || strings.Contains(st.err, "sqlite") || !strings.Contains(st.errDetail, "sqlite") {
		t.Fatalf("放行失败要留着这句话、写原因、细节折叠：err=%q detail=%q", st.err, st.errDetail)
	}
}

func TestBackgroundConversationShowsHeldCount(t *testing.T) {
	f := &liveFake{}
	r := newLiveRig(t, f)
	r.a.heldBy = map[string][]api.HeldInput{"console:other": {{InputID: "h1"}, {InputID: "h2"}}}
	cv := &conv{Key: api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:other"}, Title: "编造会话", Last: lastLine{Text: "最后一句"}}
	if got := r.a.wbConversation(cv).Preview; !strings.Contains(got, "2 句没处理") || !strings.Contains(got, "最后一句") {
		t.Fatalf("不在前台的会话要在列表里标出没处理的话：%q", got)
	}
}

func TestRunEndFailEveryClassHasPlainLineAndKeepsDetail(t *testing.T) {
	t.Parallel()
	raw := "429: {\"error\":{\"message\":\"secret-token-abc rate\"}}"
	for _, class := range append([]string{"exit", "unknown", "budget_exhausted", ""}, llm.ErrorClasses...) {
		outcome := "error"
		if class == "budget_exhausted" {
			outcome = "budget_exhausted"
		}
		f := runEndFail(api.LocalRunSnapshot{Outcome: outcome, ReasonClass: class, ReasonDetail: raw, ReasonModel: "编造模型-1"})
		if class == "exit" {
			f = runEndFail(api.LocalRunSnapshot{Outcome: "stopped", ReasonClass: "exit"})
		}
		if f.Line == "" {
			t.Errorf("类别 %q 的出错没有写原因（不能回成成功）", class)
			continue
		}
		if strings.Contains(f.Line, "secret-token") || strings.Contains(f.Line, "{") {
			t.Errorf("类别 %q 把原始错误糊进了常驻行：%q", class, f.Line)
		}
		if class != "exit" && f.Detail != raw {
			t.Errorf("类别 %q 的细节该原样放进折叠：%q", class, f.Detail)
		}
	}
	if f := runEndFail(api.LocalRunSnapshot{Outcome: "error", ReasonClass: llm.ClassModelUnknown, ReasonModel: "编造模型-1"}); !strings.Contains(f.Line, "编造模型-1") || !strings.Contains(f.Line, "设置") {
		t.Errorf("模型不可用要说哪个模型、怎么处理：%q", f.Line)
	}
	for _, o := range []string{"done", "stopped", ""} {
		if f := runEndFail(api.LocalRunSnapshot{Outcome: o}); f != (runFail{}) {
			t.Errorf("结局 %q 不是失败，不该有提示：%+v", o, f)
		}
	}
	if f := runEndFail(api.LocalRunSnapshot{Outcome: "interrupted"}); !strings.Contains(f.Line, "退出") {
		t.Errorf("程序退出中断要说清：%q", f.Line)
	}
}

func TestUserFailNeverShowsRawErrorInLine(t *testing.T) {
	t.Parallel()
	raw := errors.New("rpc error: code = Internal desc = panic at 0xdeadbeef")
	f := userFail("没能停下", raw)
	if strings.Contains(f.Line, "0xdeadbeef") || f.Detail != raw.Error() || !strings.HasPrefix(f.Line, "没能停下") {
		t.Fatalf("未认出的错误要写成一句话、原文折叠：%+v", f)
	}
	if got := userFail("x", api.ErrLocalChatBusy); !strings.Contains(got.Line, "请先停止") || got.Detail != "" {
		t.Fatalf("已经是写给人看的错误原样用：%+v", got)
	}
	if got := userFail("x", nil); got != (runFail{}) {
		t.Fatalf("没错误就没提示：%+v", got)
	}
}

// 内核故障（这个对话暂时没法继续）：一行说清是哪个对话停了、为什么，原始错误进折叠；不写成“后台出错了、再试一次”。
func TestUserFailExplainsKernelFault(t *testing.T) {
	t.Parallel()
	fe := &turn.FaultError{Fault: turn.Fault{Kind: turn.FaultRunState, CharacterID: "c1", Conversation: "console:c1", Reason: "上一轮结束时，状态没能写进记录库", Detail: "database is locked"}}
	// 走 RPC 以后只剩文字：外面还会包一层
	wrapped := errors.New("rpc: " + fe.Error())
	for _, err := range []error{fe, wrapped} {
		f := userFail("没有发出去", err)
		if f.Line != "没有发出去：这个对话暂时停下了，刚才有内容没能保存。别的对话不受影响，重启 Sumika 可以恢复。" || strings.ContainsAny(f.Line, "库") || strings.Contains(f.Line, "sumika-ctl") || strings.Contains(f.Line, "按钮") ||
			!strings.HasPrefix(f.Detail, "也可以运行 sumika-ctl kernel retry 重试") || !strings.Contains(f.Detail, "database is locked") {
			t.Fatalf("常驻一行说日常话，命令行和原始错误进折叠：%+v", f)
		}
	}
	kernel := &turn.FaultError{Fault: turn.Fault{Scope: turn.FaultScopeKernel, Kind: turn.FaultStartupRecover, Reason: "启动时收拾上次没处理完的话没做成"}}
	if f := userFail("没有发出去", kernel); f.Line != "没有发出去：所有对话暂时停下了，有内容没能保存。重启 Sumika 可以恢复。" || strings.Contains(f.Line, "别的对话不受影响") {
		t.Fatalf("内核故障是所有对话都停：%+v", f)
	}
}

func TestRunErrorShownWithFoldedDetail(t *testing.T) {
	f := &liveFake{}
	r := newLiveRig(t, f)
	st := r.beginT(t, f, "你好")
	f.finish("run1", api.LocalRunSnapshot{Outcome: "error", ReasonClass: llm.ClassModelUnknown, ReasonModel: "编造模型-1", ReasonDetail: "404 model not found: 编造模型-1"})
	r.settleUntil(t, func() bool { return !st.busy && st.err != "" })
	if st.busy || !r.tt.HasText(st.err) || !strings.Contains(st.err, "编造模型-1") {
		t.Fatalf("出错后应当写出具体原因：busy=%v err=%q", st.busy, st.err)
	}
	if r.tt.HasText("404 model not found: 编造模型-1") {
		t.Fatal("技术细节默认要折叠")
	}
	if err := r.tt.Click("详情"); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if !r.tt.HasText("404 model not found: 编造模型-1") {
		t.Fatalf("点详情后要显示原文：%v", r.tt.Texts())
	}
	if err := r.tt.Click("收起"); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if r.tt.HasText("404 model not found: 编造模型-1") {
		t.Fatal("点收起后要折回去")
	}
}

func TestSendingNewMessageClearsOldFailure(t *testing.T) {
	f := &liveFake{}
	r := newLiveRig(t, f)
	st := r.a.localRun(r.key.Conversation)
	st.fail(runFail{Line: "旧的失败", Detail: "旧细节"})
	st.errOpen = true
	r.p.Main.Text = "重来"
	r.a.submitComposer(f, r.key, r.p)
	r.settle()
	if st.err != "" || st.errDetail != "" {
		t.Fatalf("重新发送要清掉旧失败：%q %q", st.err, st.errDetail)
	}
}

var _ = chatui.NewComposer
