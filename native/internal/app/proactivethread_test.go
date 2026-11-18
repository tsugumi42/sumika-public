package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/proactive"
)

// 主动联系的话题线（规格-主动联系 第十五节第 2 步、第十六节）：导演放行 → 线里起一轮（假模型）→ 工具 → 写进本地私聊 →
// 主人在本地回，线认领或交回。全程用临时数据目录和假网关，名字、话都是编的；不碰真实 data/、不发任何真实消息。

type threadEnv struct {
	t   *testing.T
	e   *testEnv
	a   *App
	d   *Desktop
	log *attentionLog
	key api.ConversationKey
}

const threadCfg = "proactive:\n  enabled: true\n  characters: [" + testChar + "]\n"

// newThreadEnv 起一个打开了主动联系的后台：假网关按 script 回（线里每次请求和普通回合都排在同一串里）
func newThreadEnv(t *testing.T, script llmtest.Script, extraCfg string) *threadEnv {
	t.Helper()
	e := newEnv(t)
	script.Models = []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}
	srv := httptest.NewServer(llmtest.Handler(script, &e.bodies, &e.mu))
	t.Cleanup(srv.Close)
	e.llm = srv
	e.configure(threadCfg + extraCfg)
	log := &attentionLog{}
	a := e.open()
	a.opt.Attention = log.add
	d := a.Desktop()
	t.Cleanup(func() { d.Close(); a.Close() })
	if a.pthreads == nil || a.proactive == nil {
		t.Fatal("打开并列了角色，应当建导演和线的宿主")
	}
	return &threadEnv{t: t, e: e, a: a, d: d, log: log, key: localKey("console:" + testChar)}
}

// wake 让导演放行一次：第一次轮询只排期，过 4 小时（白天、没人聊）再轮询就放行。返回时线里那一轮已经跑完
func (te *threadEnv) wake() {
	te.t.Helper()
	ctx := context.Background()
	te.a.proactive.Tick(ctx, te.e.now)
	te.e.setNow(te.e.now.Add(4 * time.Hour))
	te.a.proactive.Tick(ctx, te.e.now)
	te.idle()
}

// idle 等线里在跑的轮都收尾（cancel 后 finishTurn 才从 runs 里删，所以等 runs 空）
func (te *threadEnv) idle() {
	te.t.Helper()
	waitFor(te.t, "线里的轮跑完", func() bool {
		te.a.pthreads.mu.Lock()
		defer te.a.pthreads.mu.Unlock()
		return len(te.a.pthreads.runs) == 0
	})
}

func (te *threadEnv) thread() proactive.Thread {
	te.t.Helper()
	rs, err := te.a.pthreads.mgr.Recent(testChar, 1)
	if err != nil || len(rs) != 1 {
		te.t.Fatalf("应当有一条线：%v %v", rs, err)
	}
	return rs[0]
}

func (te *threadEnv) rows() []proactive.Row {
	te.t.Helper()
	return proactiveRows(te.t, te.a)
}

func (te *threadEnv) localRows() []string { return slashRows(te.t, te.a, te.key.Conversation) }

// requestTools 是第 n 次（从 1 数）模型请求带的工具名
func (te *threadEnv) requestTools(n int) []string {
	te.t.Helper()
	te.e.mu.Lock()
	defer te.e.mu.Unlock()
	if n > len(te.e.bodies) {
		te.t.Fatalf("只有 %d 次请求，想看第 %d 次", len(te.e.bodies), n)
	}
	var req struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(te.e.bodies[n-1]), &req); err != nil {
		te.t.Fatalf("请求体不是 JSON：%v", err)
	}
	var names []string
	for _, x := range req.Tools {
		names = append(names, x.Function.Name)
	}
	return names
}

func (te *threadEnv) body(n int) string {
	te.e.mu.Lock()
	defer te.e.mu.Unlock()
	return te.e.bodies[n-1]
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

const (
	callPresence = `{}`
	callSendT1   = `{"target":"t1","text":"在忙吗？昨天那件事我一直惦记着","reason":"想接着问昨天的事"}`
)

func sendScript() llmtest.Script {
	return llmtest.Script{Replies: []llmtest.Reply{
		llmtest.Call("c1", "owner_presence", callPresence),
		llmtest.Call("c2", "thread_send", callSendT1),
		llmtest.Text("发好了，等他回。"),
	}}
}

// 线里醒来 → 看他在哪 → 发到本地私聊：消息是她说的（origin character），带"主动联系"标记和理由；
// 窗口不在前台时通知；线进等回复；日志补上 model-spoke；下次醒排到续醒时间
func TestThreadWakeSendsToLocalPrivate(t *testing.T) {
	t.Parallel()
	te := newThreadEnv(t, sendScript(), "")
	te.wake()

	th := te.thread()
	if th.State != proactive.ThreadAwaiting || th.Sends != 1 || th.OpenedReason != "想接着问昨天的事" || th.FirstSendAt.IsZero() {
		t.Fatalf("发过话进等回复，记下开口理由：%+v", th)
	}
	if !strings.HasPrefix(th.Conversation, "console:"+testChar+":topic:thread-") {
		t.Fatalf("线是 console:<角色>:topic:thread-<号>：%q", th.Conversation)
	}
	// 本地私聊里是一条她说的话，不是 automation
	rows := te.localRows()
	if len(rows) != 1 || !strings.Contains(rows[0], "message/character/character/在忙吗？昨天那件事我一直惦记着") {
		t.Fatalf("本地私聊里应当有一条她说的话（origin character）：%v", rows)
	}
	st, _ := te.a.set.ForConversation(te.key.Conversation)
	var seg string
	if err := st.Events.QueryRow(`SELECT segments_json FROM events WHERE conversation=? AND actor_kind='character'`, te.key.Conversation).Scan(&seg); err != nil || !strings.Contains(seg, `"proactive"`) || !strings.Contains(seg, "想接着问昨天的事") || !strings.Contains(seg, th.ID) {
		t.Fatalf("消息带主动联系标记、线号和理由：%q %v", seg, err)
	}
	// 通知
	got := te.log.all()
	if len(got) != 1 || got[0].Kind != AttentionProactive || got[0].Conversation != te.key.Conversation || !strings.Contains(got[0].Title, "来找你了") || !strings.Contains(got[0].Body, "在忙吗") {
		t.Fatalf("应当通知一次：%+v", got)
	}
	// 日志
	var wakeRow proactive.Row
	for _, r := range te.rows() {
		if r.Kind == proactive.KindWake {
			wakeRow = r
		}
	}
	if wakeRow.Reason != reasonModelSpoke || wakeRow.Thread != th.ID {
		t.Fatalf("放行那一行补上结局和线号：%+v", wakeRow)
	}
	// 下次醒排到续醒时间（发话后 20 分钟）
	st0, _, _ := (proactive.DBStore{DB: te.a.set.Trace()}).LoadState(testChar)
	if !st0.NextWake.Equal(th.LastSendAt.Add(20 * time.Minute)) {
		t.Fatalf("线开着时下次醒是续醒的检查时间：%v，发话 %v", st0.NextWake, th.LastSendAt)
	}
	// 请求：线的工具清单只有这几个，没有执行工具
	tl := te.requestTools(1)
	for _, want := range []string{"owner_presence", "thread_send", "stay_quiet", "end_thread", "hand_back", "proactive_snooze"} {
		if !has(tl, want) {
			t.Errorf("线的工具清单缺 %s：%v", want, tl)
		}
	}
	for _, bad := range []string{"powershell", "write", "edit", "read", "notify", "clipboard", "open", "remember", "memory_forget", "schedule_next", "todo_write", "ask_owner", "task", "web_fetch", "mcp_call"} {
		if has(tl, bad) {
			t.Errorf("线里不该有 %s：%v", bad, tl)
		}
	}
	if b := te.body(1); !strings.Contains(b, "（导演：现在 16:00 周三；还没找过他；今天已找 0 次，上限 4 次；睡觉时段里：否。") {
		t.Fatalf("第一次请求里要有醒来行：%s", b)
	}
	if n := te.e.llmCalls(); n != 3 {
		t.Fatalf("线里三次请求（看他在哪、发、收尾）：%d", n)
	}
}

// 没有由头：stay_quiet 关线（想了没开口），不写本地私聊、不通知、不动系数；没表态也一样，原因不同
func TestThreadStayQuietAndNoStanceCloseQuiet(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		script llmtest.Script
		reason string
		note   string
	}{
		{"stay_quiet", llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("c1", "stay_quiet", `{"reason":"他刚忙完，不想打扰"}`), llmtest.Text("好。")}}, reasonModelQuiet, "他刚忙完，不想打扰"},
		{"没表态", llmtest.Script{Replies: []llmtest.Reply{llmtest.Text("我想想……算了。")}}, reasonModelNoStance, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			te := newThreadEnv(t, c.script, "")
			te.wake()
			th := te.thread()
			if th.State != proactive.ThreadClosed || th.CloseReason != proactive.ClosedQuiet || th.Sends != 0 {
				t.Fatalf("想了没开口关成 quiet：%+v", th)
			}
			if rows := te.localRows(); len(rows) != 0 {
				t.Fatalf("没开口不该往本地私聊写东西：%v", rows)
			}
			if got := te.log.all(); len(got) != 0 {
				t.Fatalf("没开口不通知：%+v", got)
			}
			var wake, closeRow *proactive.Row
			for _, r := range te.rows() {
				r := r
				switch r.Kind {
				case proactive.KindWake:
					wake = &r
				case proactive.KindClose:
					closeRow = &r
				}
			}
			if wake == nil || wake.Reason != c.reason || wake.ModelNote != c.note || wake.Thread != th.ID {
				t.Fatalf("放行行：%+v", wake)
			}
			if closeRow == nil || closeRow.Reason != string(proactive.ClosedQuiet) || closeRow.Thread != th.ID {
				t.Fatalf("收线也有一行：%+v", closeRow)
			}
			st0, _, _ := (proactive.DBStore{DB: te.a.set.Trace()}).LoadState(testChar)
			if st0.Factor != 1 || st0.ColdStreak != 0 {
				t.Fatalf("想了没开口不改系数、不算冷：%+v", st0)
			}
			if !st0.NextWake.After(te.e.now.Add(44 * time.Minute)) {
				t.Fatalf("线一关下次醒回到正常间隔：%v", st0.NextWake.Sub(te.e.now))
			}
		})
	}
}

// send: think：走完整流程，但不写目标会话、不通知，线按没开口处理，日志写 think-only
func TestThreadThinkModeSendsNothing(t *testing.T) {
	t.Parallel()
	te := newThreadEnv(t, sendScript(), "  send: think\n")
	te.wake()
	th := te.thread()
	if th.State != proactive.ThreadClosed || th.CloseReason != proactive.ClosedQuiet || th.Sends != 0 {
		t.Fatalf("think 档按没开口处理：%+v", th)
	}
	if rows := te.localRows(); len(rows) != 0 {
		t.Fatalf("think 档不写本地私聊：%v", rows)
	}
	if len(te.log.all()) != 0 {
		t.Fatal("think 档不通知")
	}
	sends, _ := te.a.pthreads.mgr.Sends(th.ID)
	if len(sends) != 1 || sends[0].State != proactive.SendThought {
		t.Fatalf("记下本想说的：%+v", sends)
	}
	var reason string
	for _, r := range te.rows() {
		if r.Kind == proactive.KindWake {
			reason = r.Reason
		}
	}
	if reason != reasonThinkOnly {
		t.Fatalf("日志写 think-only：%q", reason)
	}
}

// ───────────── 主人在本地回：认领、交回、旁听 ─────────────

const callSendReply = `{"target":"t1","text":"那太好了，我就是想问问这个"}`

// ownerSays 是主人在本地私聊说一句（走和界面一样的入口）
func (te *threadEnv) ownerSays(text string) {
	te.t.Helper()
	if err := te.d.Reader.SendLocal(context.Background(), te.key, text, nil); err != nil {
		te.t.Fatal(err)
	}
}

func (te *threadEnv) countOwnerEvents(text string) int {
	te.t.Helper()
	st, _ := te.a.set.ForConversation(te.key.Conversation)
	var n int
	if err := st.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation=? AND actor_kind='owner' AND origin='owner' AND text=?`, te.key.Conversation, text).Scan(&n); err != nil {
		te.t.Fatal(err)
	}
	return n
}

func (te *threadEnv) claims(state string) []proactive.Claim {
	te.t.Helper()
	cs, err := te.a.pthreads.mgr.ClaimsOf(te.thread().ID, state)
	if err != nil {
		te.t.Fatal(err)
	}
	return cs
}

func (te *threadEnv) waitNormalReply(sub string) {
	te.t.Helper()
	waitFor(te.t, "普通对话回了："+sub, func() bool { return hasRow(te.localRows(), "character/character/"+sub) })
}

func replyScript(more ...llmtest.Reply) llmtest.Script {
	return llmtest.Script{Replies: append([]llmtest.Reply{
		llmtest.Call("c1", "owner_presence", callPresence),
		llmtest.Call("c2", "thread_send", callSendT1),
		llmtest.Text("发好了，等他回。"),
	}, more...)}
}

// 主人在窗口内回了她主动发的话：认领。主人那句在本地私聊里有且只有一条；线里回他（经 thread_send 写回原会话）；
// 普通实例这一句没有回（没有多出普通回合的请求）；转述行转义；线的前缀只追加
func TestOwnerReplyIsClaimedByThread(t *testing.T) {
	t.Parallel()
	te := newThreadEnv(t, replyScript(llmtest.Call("c3", "thread_send", callSendReply), llmtest.Text("回他了。")), "")
	te.wake()
	te.ownerSays("在的，怎么啦")
	te.idle()

	rows := te.localRows()
	want := []string{"message/character/character/在忙吗", "message/owner/owner/在的，怎么啦", "message/character/character/那太好了"}
	if len(rows) != 3 {
		t.Fatalf("本地私聊应当是：她主动发的、主人回的、线里回的：%v", rows)
	}
	for i, w := range want {
		if !strings.HasPrefix(rows[i], w) {
			t.Errorf("第 %d 条：%q，想要前缀 %q", i, rows[i], w)
		}
	}
	if n := te.countOwnerEvents("在的，怎么啦"); n != 1 {
		t.Fatalf("主人那句在目标会话里有且只有一条：%d", n)
	}
	th := te.thread()
	if th.State != proactive.ThreadAwaiting || th.OwnerReplies != 1 || th.Sends != 2 {
		t.Fatalf("认领后线回了他，回到等回复：%+v", th)
	}
	if cs := te.claims(proactive.ClaimReplied); len(cs) != 1 || cs[0].Conversation != te.key.Conversation {
		t.Fatalf("认领记录改成已回：%+v", cs)
	}
	if n := te.e.llmCalls(); n != 5 {
		t.Fatalf("线里 3 次 + 2 次，普通实例没有回这一句：%d", n)
	}
	if b := te.body(4); !strings.Contains(b, "（所有者在本地私聊说：在的，怎么啦）") {
		t.Fatalf("第 4 次请求里要有转述行：%s", b)
	}
	if tl := te.requestTools(4); has(tl, "powershell") || has(tl, "remember") {
		t.Fatalf("线里认领这一轮仍然没有执行工具：%v", tl)
	}
	// 转述存成线里的 system 事件，来源 proactive:relay
	thSt, _ := te.a.set.ForConversation(th.Conversation)
	var relays int
	thSt.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation=? AND actor_kind='system' AND origin='proactive:relay'`, th.Conversation).Scan(&relays)
	if relays != 1 {
		t.Fatalf("线里应当有一条转述：%d", relays)
	}
	assertAppendOnly(t, te, 5)
}

// assertAppendOnly 线里连续请求的消息只在末尾追加：前一次请求的消息是后一次的前缀，系统提示字节不变（规格第十六节第 8 条）
func assertAppendOnly(t *testing.T, te *threadEnv, n int) {
	t.Helper()
	msgs := func(i int) []json.RawMessage {
		var req struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal([]byte(te.body(i)), &req); err != nil {
			t.Fatal(err)
		}
		return req.Messages
	}
	prev := msgs(1)
	for i := 2; i <= n; i++ {
		cur := msgs(i)
		if len(cur) < len(prev) {
			t.Fatalf("第 %d 次请求的消息比上一次少：%d < %d", i, len(cur), len(prev))
		}
		for j := range prev {
			if string(prev[j]) != string(cur[j]) {
				t.Fatalf("第 %d 次请求的第 %d 条消息和上一次不一样（前缀被改了）：\n%s\n%s", i, j, prev[j], cur[j])
			}
		}
		prev = cur
	}
}

// hand_back：主人的回复和这条线无关，线交回；同一条主人消息只被处理一次，交回后由普通对话回；
// 本线内该会话不再认领，之后窗口内的话走普通路径，只记"他回过"
func TestHandBackReturnsToNormalPathAndBlocksConversation(t *testing.T) {
	t.Parallel()
	te := newThreadEnv(t, replyScript(llmtest.Call("c3", "hand_back", `{}`), llmtest.Text("交回了。"), llmtest.Text("晴天，出门带伞也不亏"), llmtest.Text("好的")), "")
	te.wake()
	te.ownerSays("顺便问问，今天天气怎么样")
	te.idle()
	te.waitNormalReply("晴天")

	if n := te.countOwnerEvents("顺便问问，今天天气怎么样"); n != 1 {
		t.Fatalf("交回前后主人那句都只有一条：%d", n)
	}
	cs := te.claims("")
	if len(cs) != 1 || cs[0].State != proactive.ClaimHanded {
		t.Fatalf("认领记录是已交回：%+v", cs)
	}
	th := te.thread()
	if !th.Blocked(te.key.Conversation) {
		t.Fatalf("hand_back 之后本线内不再认领这个会话：%+v", th.BlockedConvs)
	}
	if n := te.e.llmCalls(); n != 6 {
		t.Fatalf("线里 3 + 2 次，普通回合 1 次：%d", n)
	}
	// 请求 6 是普通回合：有执行工具，主人那句只出现一次，她主动发的话在历史里是她说的
	if tl := te.requestTools(6); !has(tl, "powershell") || has(tl, "thread_send") {
		t.Fatalf("交回的是普通对话：%v", tl)
	}
	if b := te.body(6); strings.Count(b, "顺便问问，今天天气怎么样") != 1 || !strings.Contains(b, "在忙吗？昨天那件事我一直惦记着") {
		t.Fatalf("普通回合里主人那句一次、她主动发的话在历史里：%s", b)
	}
	// 窗口内再来一句：被拉黑，走普通路径；仍然记他回过
	before := te.e.llmCalls()
	te.ownerSays("谢谢")
	te.waitNormalReply("好的")
	if te.e.llmCalls() != before+1 {
		t.Fatal("拉黑后的话只走普通路径，不再起线里的轮")
	}
	if len(te.claims("")) != 1 {
		t.Fatalf("拉黑后不再有新的认领：%+v", te.claims(""))
	}
	if th := te.thread(); th.OwnerReplies != 2 {
		t.Fatalf("认领的一句加没认领的旁听一句，他回过 2 次：%+v", th)
	}
}
