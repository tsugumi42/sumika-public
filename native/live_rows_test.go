package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/segview"
)

func liveApp() (*app, string) {
	a := shellApp()
	conv := "console:r1"
	a.sel, a.title, a.group, a.rail = conv, "白羽", false, 0
	a.msgs = nil
	a.list = a.newList()
	return a, conv
}

func realRow(id string, mine bool, text string, at int64, char string) *msg {
	return &msg{id: id, mine: mine, md: !mine, isChar: !mine, actor: char, text: text, at: at, live: true, seq: at, blocks: segview.TextBlocks(text)}
}

func liveKinds(a *app) []int8 {
	var out []int8
	for _, m := range a.msgs {
		out = append(out, m.liveKind)
	}
	return out
}

// 点发送当场多一行；真消息到了原地接替，行数不变、列表键不变、不重播进场。
func TestPendingHandedOverKeepsRowKey(t *testing.T) {
	t.Parallel()
	a, conv := liveApp()
	p := a.addPending(conv, "你好")
	if len(a.msgs) != 1 || a.msgs[0] != p || p.liveKind != livePending {
		t.Fatalf("发送后该有一行临时行：%v", liveKinds(a))
	}
	key := p.rowKey()
	real := realRow("e1", true, "你好", p.at+50, "")
	a.msgs = append(a.msgs, real)
	a.reconcileLive()
	if len(a.msgs) != 1 || a.msgs[0] != real {
		t.Fatalf("真消息该接替临时行：%v", liveKinds(a))
	}
	if real.rowKey() != key || !real.noEnter {
		t.Fatalf("接替后列表键 %q（应为 %q），noEnter=%v", real.rowKey(), key, real.noEnter)
	}
	if real.id != "e1" {
		t.Fatalf("真消息的 id 不能改：%q", real.id)
	}
}

// 早就在列表里的旧消息、文字不一样的消息，都不接替。
func TestPendingNotClaimedByOthers(t *testing.T) {
	t.Parallel()
	a, conv := liveApp()
	p := a.addPending(conv, "你好")
	old := realRow("e0", true, "你好", p.at-60_000, "")
	other := realRow("e1", true, "别的话", p.at-1, "")
	a.msgs = append([]*msg{old, other}, a.msgs...)
	a.reconcileLive()
	if n := len(a.localRun(conv).pending); n != 1 {
		t.Fatalf("不该被接替：还剩 %d 行临时行", n)
	}
	if a.msgs[len(a.msgs)-1] != p {
		t.Fatal("临时行该排在末尾")
	}
}

func TestPendingDroppedOnReject(t *testing.T) {
	t.Parallel()
	a, conv := liveApp()
	p := a.addPending(conv, "你好")
	a.dropPending(conv, p)
	if len(a.msgs) != 0 || len(a.localRun(conv).pending) != 0 {
		t.Fatalf("拒收后该撤掉：%v", liveKinds(a))
	}
}

// 回复：先“正在输入”；有字了变气泡；存库前真消息来了也不接替；存库后接替，文字清理过也认。
func TestReplyLifecycle(t *testing.T) {
	t.Parallel()
	a, conv := liveApp()
	st := a.localRun(conv)
	p := a.addPending(conv, "你好")
	a.msgs = append(a.msgs, realRow("e1", true, "你好", p.at-1, ""))
	st.busy = true
	a.reconcileLive()
	if k := liveKinds(a); len(k) != 2 || k[1] != liveTyping {
		t.Fatalf("没字时该是“正在输入”：%v", k)
	}
	a.liveReplyUpdate(conv, st, 0, "第一", 0)
	if k := liveKinds(a); len(k) != 2 || k[1] != liveReply {
		t.Fatalf("有字了该是回复气泡：%v", k)
	}
	reply := a.msgs[1]
	if !reply.streaming || reply.text != "第一" {
		t.Fatalf("%+v", reply)
	}
	a.liveReplyUpdate(conv, st, 0, "第一段全文", 0)
	if a.msgs[1] != reply || reply.text != "第一段全文" {
		t.Fatal("同一步该更新同一个气泡")
	}
	key := reply.rowKey()
	// 还没存库：别人的消息不接替
	early := realRow("e2", false, "别的角色的话", p.at-3000, "r2")
	a.msgs = append(a.msgs, early)
	a.reconcileLive()
	if !hasRowID(a, reply.id) {
		t.Fatal("没存库不该被接替")
	}
	a.msgs = removeRow(a.msgs, early)
	// 存库了：真消息接替，文字可以不一样（存库前清理过）
	a.liveReplyUpdate(conv, st, 0, "第一段全文", 1)
	real := realRow("e3", false, "第一段全文（清理后）", p.at, "r1")
	a.msgs = append(a.msgs, real)
	a.reconcileLive()
	if hasRowID(a, reply.id) || real.rowKey() != key || !real.noEnter {
		t.Fatalf("存库后该接替：%v，键 %q（应为 %q）", liveKinds(a), real.rowKey(), key)
	}
	// 第二步：新气泡，不动上一步
	a.liveReplyUpdate(conv, st, 1, "第二段", 1)
	last := a.msgs[len(a.msgs)-1]
	if last.liveKind != liveReply || last.text != "第二段" || last.liveStep != 1 {
		t.Fatalf("第二步该是新气泡：%+v", last)
	}
	if real.text != "第一段全文（清理后）" {
		t.Fatal("上一步的真消息不该变")
	}
	// 收尾
	st.busy = false
	a.clearLive(conv, st)
	if k := liveKinds(a); k[len(k)-1] != liveReply {
		t.Fatalf("收尾后等真消息接替，不该立刻消失：%v", k)
	}
}

func TestLiveReplyRetractsFailedDraft(t *testing.T) {
	t.Parallel()
	a, conv := liveApp()
	st := a.localRun(conv)
	st.busy = true
	a.liveReplyUpdate(conv, st, 0, "前半句", 0)
	if len(st.replies) != 1 || st.replies[0].text != "前半句" {
		t.Fatalf("该先有半句气泡：%+v", st.replies)
	}
	a.liveReplyUpdate(conv, st, 0, "", 0)
	if len(st.replies) != 0 || len(st.replied) != 0 {
		t.Fatalf("失败草稿该从列表拿掉：replies=%d replied=%v", len(st.replies), st.replied)
	}
	if k := liveKinds(a); len(k) != 1 || k[0] != liveTyping {
		t.Fatalf("忙着但没字该回到正在输入：%v", k)
	}
}

// 这一步存了但真消息一直不来：到点撤掉，不留假消息。
func TestReplyExpires(t *testing.T) {
	t.Parallel()
	a, conv := liveApp()
	st := a.localRun(conv)
	a.postFn = func(fn func()) { fn() }
	st.busy = true
	a.liveReplyUpdate(conv, st, 0, "一句", 1)
	if hasLiveReply(a) {
		t.Fatal("已存库的步骤不该再开新气泡")
	}
	a.liveReplyUpdate(conv, st, 0, "一句", 0)
	r := st.replies[0]
	a.liveReplyUpdate(conv, st, 0, "一句", 1)
	if !r.liveSealed {
		t.Fatal("该标记已存库")
	}
	r.liveDeadline = r.liveDeadline.Add(-2 * liveHandoffWait)
	keep := st.replies[:0:0]
	for _, x := range st.replies {
		if x.liveDeadline.After(time.Now()) {
			keep = append(keep, x)
		}
	}
	if len(keep) != 0 {
		t.Fatal("过期判定不对")
	}
}

// 切到别的会话：临时行不跟过去，切回来还在。
func TestLiveRowsStayWithConversation(t *testing.T) {
	t.Parallel()
	a, conv := liveApp()
	a.addPending(conv, "你好")
	a.sel = "console:r2"
	a.msgs = nil
	a.reconcileLive()
	if len(a.msgs) != 0 {
		t.Fatalf("别的会话不该有：%v", liveKinds(a))
	}
	a.sel = conv
	a.reconcileLive()
	if len(a.msgs) != 1 {
		t.Fatalf("切回来该还在：%v", liveKinds(a))
	}
}

func hasRowID(a *app, id string) bool {
	for _, m := range a.msgs {
		if m.id == id {
			return true
		}
	}
	return false
}

func hasLiveReply(a *app) bool {
	for _, m := range a.msgs {
		if m.liveKind == liveReply {
			return true
		}
	}
	return false
}

func removeRow(list []*msg, x *msg) []*msg {
	out := list[:0:0]
	for _, m := range list {
		if m != x {
			out = append(out, m)
		}
	}
	return out
}

// 一整轮：发出当场就在列表里；出字时输入框不变高，回复在消息列里；收尾后没有临时行，也不重复。
func TestLiveRowsEndToEnd(t *testing.T) {
	release := make(chan struct{})
	h := wbUI(t, llmtest.Script{Reply: llmtest.Text("前半句", "，完整回复"), Hold: func(n int) int {
		if n == 2 {
			return 1
		}
		return -1
	}}, release)
	h.tt.Frame()
	before := boxOf(t, h.tt, "composer").Box.H
	h.click(t, "发一条消息")
	h.tt.Type("编的输入")
	h.tt.Key(0, ui.KeyEnter)
	if n := countText(h.a, "编的输入"); n != 1 {
		t.Fatalf("发送后同一帧列表里就该有这句话，现在 %d 条：%v", n, liveKinds(h.a))
	}
	h.tt.Frame()
	if !h.tt.HasText("编的输入") {
		t.Fatal("发送后没有立刻画出来")
	}
	st := h.a.localRun("console:char00")
	h.until(t, "流式前半句", func() bool { return h.tt.HasText("前半句") && st.busy })
	if got := boxOf(t, h.tt, "composer").Box.H; !closeTo(got, before) {
		t.Fatalf("出字时输入框变高了：%v → %v", before, got)
	}
	if hasKey(h.tt, "draft-scroll") {
		t.Fatal("回复不该再画在输入框上方")
	}
	var reply *msg
	for _, m := range h.a.msgs {
		if m.liveKind == liveReply {
			reply = m
		}
	}
	if reply == nil || !reply.streaming {
		t.Fatalf("回复该是消息列里的流式气泡：%v", liveKinds(h.a))
	}
	key := reply.rowKey()
	close(release)
	h.until(t, "完整回复落入历史", func() bool {
		return !st.busy && countText(h.a, "前半句，完整回复") == 1 && !anyLive(h.a)
	})
	var final *msg
	for _, m := range h.a.msgs {
		if m.text == "前半句，完整回复" {
			final = m
		}
	}
	if final.rowKey() != key {
		t.Fatalf("接替时列表键变了：%q → %q", key, final.rowKey())
	}
	if n := countText(h.a, "编的输入"); n != 1 {
		t.Fatalf("发出的话重复了：%d", n)
	}
	if got := boxOf(t, h.tt, "composer").Box.H; !closeTo(got, before) {
		t.Fatalf("收尾后输入框高度变了：%v → %v", before, got)
	}
}

func countText(a *app, text string) int {
	n := 0
	for _, m := range a.msgs {
		if m.text == text {
			n++
		}
	}
	return n
}

func anyLive(a *app) bool {
	for _, m := range a.msgs {
		if m.liveKind != liveNone {
			return true
		}
	}
	return false
}

// 想了、调了工具、再回复：过程气泡实时出现，真回复到了挂在它前面；切走再回来从后台读，内容还在。
func TestLiveProcessEndToEnd(t *testing.T) {
	release := make(chan struct{})
	h := wbUI(t, llmtest.Script{Replies: []llmtest.Reply{
		{Reasoning: []string{"先", "想想"}, ToolCalls: []llmtest.ToolCall{{ID: "c1", Name: "ls", Args: []string{`{"path":"."}`}}}, Usage: json.RawMessage(`{"prompt_tokens":100,"completion_tokens":10}`)},
		llmtest.Text("看完了"),
	}}, release)
	h.tt.Frame()
	h.click(t, "发一条消息")
	h.tt.Type("看一下")
	h.tt.Key(0, ui.KeyEnter)
	st := h.a.localRun("console:char00")
	close(release)
	h.until(t, "这一轮结束", func() bool {
		if h.tt.HasText("允许一次") {
			h.click(t, "允许一次")
		}
		return !st.busy && countText(h.a, "看完了") == 1 && len(st.replies) == 0
	})
	var reply, before *msg
	for i, m := range h.a.msgs {
		if m.text == "看完了" {
			reply = m
			if i > 0 {
				before = h.a.msgs[i-1]
			}
		}
	}
	if reply == nil || before == nil || before.steps == nil {
		t.Fatalf("过程气泡该在回复前面：%v", liveKinds(h.a))
	}
	if len(before.steps) != 2 || before.steps[0].Kind != chatui.StepThink || before.steps[1].State != chatui.StepDone {
		t.Fatalf("该是想了一段加一次工具，都做完了：%d 步", len(before.steps))
	}
}

func TestProcessGapsMergeWithoutReply(t *testing.T) {
	t.Parallel()
	a, conv := liveApp()
	st := a.localRun(conv)
	st.busy = true
	st.startRun()
	a.liveProcessUpdate(conv, st, []api.LocalProcessStep{
		{ID: "t0", Kind: "think", Step: 0, State: "done", Started: 1},
		{ID: "c0", Kind: "tool", Tool: "read", Step: 0, State: "done", Started: 2},
		{ID: "t1", Kind: "think", Step: 1, State: "running", Started: 3},
	})
	if len(st.procs) != 1 || len(st.procs[0].steps) != 3 {
		t.Fatalf("没说话的几步该在同一个过程气泡里：%d 行", len(st.procs))
	}
	// 说了话以后的工具另起一个
	a.liveReplyUpdate(conv, st, 1, "一句", 0)
	a.liveProcessUpdate(conv, st, []api.LocalProcessStep{
		{ID: "t0", Kind: "think", Step: 0, State: "done", Started: 1},
		{ID: "c0", Kind: "tool", Tool: "read", Step: 0, State: "done", Started: 2},
		{ID: "t1", Kind: "think", Step: 1, State: "done", Started: 3},
		{ID: "c1", Kind: "tool", Tool: "grep", Step: 1, State: "running", Started: 4},
	})
	if len(st.procs) != 2 {
		t.Fatalf("说过话以后该另起一个过程气泡：%d 行", len(st.procs))
	}
	if st.procs[1].steps[0].Kind != chatui.StepSearch || st.procs[1].steps[0].State != chatui.StepRunning {
		t.Fatalf("%+v", st.procs[1].steps[0])
	}
}

// 对方正在输入显示在标题栏的状态位。
func TestSubtitleShowsTyping(t *testing.T) {
	t.Parallel()
	a, _ := liveApp()
	a.sel = "qq:private:80001:90001"
	a.localStatus = map[string]api.LocalStatus{a.sel: {Typing: true}}
	if got := a.wbSubtitle(); got != "对方正在输入…" {
		t.Fatalf("%q", got)
	}
	a.localStatus = nil
	if got := a.wbSubtitle(); got == "对方正在输入…" {
		t.Fatal("不该再写正在输入")
	}
}

// 本地群聊：回复也在消息列里流式长出来，名字是说话的那位，真消息原地接替；不再有输入框上方的草稿框。
func TestLiveGroupEndToEnd(t *testing.T) {
	release := make(chan struct{})
	reply0 := llmtest.Text("前半句", "，群里的回复")
	reply0.Reasoning = []string{"编的思考"}
	h := wbUI(t, llmtest.Script{Reply: reply0}, release)
	g := localUIGroup(t, h, "编造流式群")
	h.a.open(g.Conversation)
	h.until(t, "群打开", func() bool { return h.a.firstDone && h.a.sel == g.Conversation })
	h.tt.Frame()
	before := boxOf(t, h.tt, "composer").Box.H
	h.click(t, "发一条消息")
	h.tt.Type("编的群消息")
	h.tt.Key(0, ui.KeyEnter)
	if n := countText(h.a, "编的群消息"); n != 1 {
		t.Fatalf("发送后同一帧列表里就该有这句话，现在 %d 条：%v", n, liveKinds(h.a))
	}
	st := h.a.localRun(g.Conversation)
	h.until(t, "流式前半句", func() bool { return h.tt.HasText("前半句") && st.busy })
	if hasKey(h.tt, "draft-scroll") {
		t.Fatal("回复不该再画在输入框上方")
	}
	if got := boxOf(t, h.tt, "composer").Box.H; !closeTo(got, before) {
		t.Fatalf("出字时输入框变高了：%v → %v", before, got)
	}
	var reply *msg
	for _, m := range h.a.msgs {
		if m.liveKind == liveReply {
			reply = m
		}
	}
	if reply == nil || !reply.streaming {
		t.Fatalf("回复该是消息列里的流式气泡：%v", liveKinds(h.a))
	}
	if reply.actor == "" || reply.who == "" {
		t.Fatalf("回复要标明是谁说的：actor=%q who=%q", reply.actor, reply.who)
	}
	var proc *msg
	for _, m := range h.a.msgs {
		if m.liveKind == liveProcess {
			proc = m
		}
	}
	if proc == nil || proc.actor != reply.actor || proc.who == "" {
		t.Fatalf("思考要进过程气泡，也标明是谁的：%+v；%v", proc, liveKinds(h.a))
	}
	actor, key := reply.actor, reply.rowKey()
	close(release)
	h.until(t, "这一轮结束、都落入历史", func() bool {
		if st.busy || countText(h.a, "前半句，群里的回复") < 1 {
			return false
		}
		for _, m := range h.a.msgs {
			if m.liveKind != liveNone && m.liveKind != liveProcess { // 过程行挂在真回复前面，留着
				return false
			}
		}
		return true
	})
	if len(st.procs) != 0 {
		t.Fatalf("过程行都该挂到各自的真回复上，还剩 %d 行", len(st.procs))
	}
	var final *msg
	for _, m := range h.a.msgs {
		if m.text == "前半句，群里的回复" && m.actor == actor {
			final = m
			break
		}
	}
	if final == nil {
		t.Fatalf("没有 %s 的真消息", actor)
	}
	if final.rowKey() != key {
		t.Fatalf("接替时列表键变了：%q → %q", key, final.rowKey())
	}
	for _, m := range h.a.msgs {
		if m.text == "前半句，群里的回复" && m.who == "" {
			t.Fatalf("真消息没有名字：%+v", m)
		}
	}
}

// 本地群聊里模型中途失败换备选：已流出的字撤回，不算这一步存好，备选的字接着长出来。
func TestLiveGroupResetRedoesStep(t *testing.T) {
	t.Parallel()
	a, conv := liveApp()
	st := a.localRun(conv)
	st.busy = true
	a.liveGroupDraft(conv, st, api.LocalDraft{TurnID: "0", Step: -1, CharacterID: "r1"})
	a.liveGroupDraft(conv, st, api.LocalDraft{TurnID: "0", Step: 0, Text: "半截的字", CharacterID: "r1"})
	if len(st.replies) != 1 {
		t.Fatalf("该有一条流式回复：%d", len(st.replies))
	}
	a.liveGroupDraft(conv, st, api.LocalDraft{TurnID: "0", Step: 0, Reset: true, CharacterID: "r1"})
	if len(st.replies) != 0 || st.sealed != 0 {
		t.Fatalf("撤回后不该留半截、也不算存好：replies=%d sealed=%d", len(st.replies), st.sealed)
	}
	a.liveGroupDraft(conv, st, api.LocalDraft{TurnID: "0", Step: 0, Text: "备选的回答", CharacterID: "r1"})
	if len(st.replies) != 1 || st.replies[0].text != "备选的回答" || st.replies[0].liveSealed {
		t.Fatalf("备选的字要接着长：%+v", st.replies)
	}
}

// 本地群聊点停止：马上收掉“正在输入”和流式状态，晚到的进度不再开新行。
func TestLiveGroupStopClearsAtOnce(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	h := wbUI(t, llmtest.Script{Reply: llmtest.Text("前半句", "，后半句")}, release)
	g := localUIGroup(t, h, "编造停止群")
	h.a.open(g.Conversation)
	h.until(t, "群打开", func() bool { return h.a.firstDone && h.a.sel == g.Conversation })
	h.tt.Frame()
	h.click(t, "发一条消息")
	h.tt.Type("编的群消息")
	h.tt.Key(0, ui.KeyEnter)
	st := h.a.localRun(g.Conversation)
	h.until(t, "流式前半句", func() bool { return h.tt.HasText("前半句") && st.busy })
	h.a.stopLocal(h.a.reader, g)
	if !st.stopped {
		t.Fatal("点了停止要马上标记")
	}
	for _, m := range h.a.msgs {
		if m.liveKind == liveTyping || (m.liveKind == liveReply && m.streaming) {
			t.Fatalf("停止后不该还有正在输入或流式：%v", liveKinds(h.a))
		}
	}
}

// 只有真读文件才算读，别的工具都写清是哪个工具（主人 2026-11-09：“都是读某个工具”）。
func TestChatStepsKinds(t *testing.T) {
	t.Parallel()
	for tool, want := range map[string]chatui.StepKind{
		"read": chatui.StepRead, "grep": chatui.StepSearch, "memory_search": chatui.StepSearch, "powershell": chatui.StepCommand, "edit": chatui.StepEdit,
		"todo_write": chatui.StepTool, "ls": chatui.StepTool, "process_output": chatui.StepTool, "read_link_detail": chatui.StepTool, "mcp_编造": chatui.StepTool,
	} {
		got := chatSteps([]api.LocalProcessStep{{ID: "s", Kind: "tool", Tool: tool, State: "done"}})
		if got[0].Kind != want || got[0].Tool != tool {
			t.Errorf("%s：种类 %v，想要 %v；工具名 %q", tool, got[0].Kind, want, got[0].Tool)
		}
	}
}

// 自动召回不是工具：单独一种，明细原样带到界面，摘要不把它算进“调用了”。
func TestChatStepsRecall(t *testing.T) {
	t.Parallel()
	in := []api.RecallItem{{Text: "编造", Kind: "fact", Source: "原话", CreatedAt: 7, Pinned: true, Hearsay: true, Score: 1.5, Rerank: 2.5, Sim: 3.5, Lexical: 4, Vector: 5, Previous: []api.RecallPrior{{Text: "旧编造", CreatedAt: 3}}}}
	got := chatSteps([]api.LocalProcessStep{{ID: "r", Kind: "recall", State: "done", Recall: in}, {ID: "t", Kind: "tool", Tool: "todo_write", State: "done"}})
	if got[0].Kind != chatui.StepRecall || got[0].Tool != "" || len(got[0].Recall) != 1 {
		t.Fatalf("召回步：%+v", got[0])
	}
	if r := got[0].Recall[0]; !reflect.DeepEqual(r, chatui.RecallItem{Text: "编造", Kind: "fact", Source: "原话", At: 7, Pinned: true, Hearsay: true, Score: 1.5, Rerank: 2.5, Sim: 3.5, Lexical: 4, Vector: 5, Previous: []chatui.RecallPrior{{Text: "旧编造", At: 3}}}) {
		t.Fatalf("明细转换丢了字段：%+v", r)
	}
	if s := chatui.Summarize(got); s.Text != "召回 1 条记忆 · 调用了 todo_write" {
		t.Fatalf("摘要：%q", s.Text)
	}
}
