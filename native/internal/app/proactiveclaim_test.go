package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/proactive"
	"sumika-native/internal/store"
)

// 认领的各种边界、续醒、收线、落地记录（规格-主动联系 第十六节第 3、5、7 条）。名字、话都是编的。

func newCharEvent(te *threadEnv, text string) store.NewEvent {
	return store.NewEvent{Channel: "console", Conversation: te.key.Conversation, ActorKind: "character", ActorID: testChar, Origin: "character", Kind: "message", Text: text, CreatedAt: te.e.now.UnixMilli()}
}

func newOwnerEvent(te *threadEnv, text string) store.NewEvent {
	return store.NewEvent{Channel: "console", Conversation: te.key.Conversation, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: text, CreatedAt: te.e.now.UnixMilli()}
}

// 线里这一轮既没回也没交回（没表态）：主人的话不能没人回，立刻交回普通路径；没有 hand_back，所以不拉黑
func TestClaimWithoutStanceIsHandedBackAtOnce(t *testing.T) {
	t.Parallel()
	te := newThreadEnv(t, replyScript(llmtest.Text("嗯……"), llmtest.Text("我在，说吧")), "")
	te.wake()
	te.ownerSays("你好")
	te.idle()
	te.waitNormalReply("我在，说吧")
	if cs := te.claims(""); len(cs) != 1 || cs[0].State != proactive.ClaimHanded {
		t.Fatalf("没表态的认领交回：%+v", cs)
	}
	if th := te.thread(); th.Blocked(te.key.Conversation) {
		t.Fatalf("自动交回不拉黑（只有 hand_back 拉黑）：%+v", th.BlockedConvs)
	}
	if n := te.countOwnerEvents("你好"); n != 1 {
		t.Fatalf("主人那句只有一条：%d", n)
	}
}

// 线的回合被停（出错、被取消）时，主人的话交回普通路径，不会没人回
func TestClaimHandedBackWhenThreadTurnIsStopped(t *testing.T) {
	t.Parallel()
	script := replyScript(llmtest.Text("线里正在想……"), llmtest.Text("普通对话回你了"))
	script.Hold = func(nth int) int {
		if nth == 4 { // 认领后线里那一轮：吐出第一段前就卡住，等主人把它停掉
			return 0
		}
		return -1
	}
	te := newThreadEnv(t, script, "")
	te.wake()
	te.ownerSays("你在吗")
	waitFor(t, "线里的认领轮卡在请求上", func() bool { return te.e.llmCalls() == 4 })
	id := te.thread().ID
	te.a.pthreads.mu.Lock()
	runs := te.a.pthreads.runs[id]
	te.a.pthreads.mu.Unlock()
	if len(runs) == 0 {
		t.Fatal("线里的轮应当在跑")
	}
	runs[0].cancel()
	te.idle()
	te.waitNormalReply("普通对话回你了")
	if cs := te.claims(""); len(cs) != 1 || cs[0].State != proactive.ClaimHanded {
		t.Fatalf("被停的认领交回：%+v", cs)
	}
	if th := te.thread(); th.State == proactive.ThreadClosed {
		t.Fatalf("线的轮被停不等于关线（话已经发到了，仍在聊着或等回复）：%+v", th)
	}
}

// 认领的边界：窗口外、普通实例已经接过话、没引用线的话，都不认领，走普通路径，同时记"他回过"并取消续醒
func TestClaimWindowAndLatestBoundaries(t *testing.T) {
	t.Parallel()
	t.Run("窗口外不认领，旁听", func(t *testing.T) {
		t.Parallel()
		te := newThreadEnv(t, replyScript(llmtest.Text("好的，那晚点聊")), "")
		te.wake()
		te.e.setNow(te.e.now.Add(11 * time.Minute))
		te.ownerSays("刚看到")
		te.waitNormalReply("好的，那晚点聊")
		if len(te.claims("")) != 0 {
			t.Fatal("窗口外不认领")
		}
		th := te.thread()
		if th.OwnerReplies != 1 || th.State != proactive.ThreadAwaiting {
			t.Fatalf("旁听记他回过，线仍在等：%+v", th)
		}
		if _, due := proactive.FollowUpAt(te.a.cfg.Proactive, th); due {
			t.Fatal("他回过就取消续醒")
		}
		te.e.setNow(te.e.now.Add(20 * time.Minute))
		before := te.e.llmCalls()
		te.a.proactive.Tick(context.Background(), te.e.now)
		te.idle()
		if te.e.llmCalls() != before {
			t.Fatal("他回过以后到点也不续醒")
		}
	})
	t.Run("窗口边界上 10 分钟还认", func(t *testing.T) {
		t.Parallel()
		te := newThreadEnv(t, replyScript(llmtest.Call("c3", "thread_send", callSendReply), llmtest.Text("回他了。")), "")
		te.wake()
		te.e.setNow(te.e.now.Add(10 * time.Minute))
		te.ownerSays("在")
		te.idle()
		if len(te.claims(proactive.ClaimReplied)) != 1 {
			t.Fatalf("正好 10 分钟还在窗口里：%+v", te.claims(""))
		}
	})
	t.Run("普通实例已经接过话就不认领", func(t *testing.T) {
		t.Parallel()
		te := newThreadEnv(t, replyScript(llmtest.Text("普通实例接着聊")), "")
		te.wake()
		st, _ := te.a.set.ForConversation(te.key.Conversation)
		// 普通实例后来又说了一句，线的话不再是最近一条
		if _, err := st.AppendEvent(newCharEvent(te, "刚才在聊别的"), []string{testChar}); err != nil {
			t.Fatal(err)
		}
		te.ownerSays("嗯")
		te.waitNormalReply("普通实例接着聊")
		if len(te.claims("")) != 0 {
			t.Fatalf("线的话不是最近一条角色发言，不截走普通实例的活：%+v", te.claims(""))
		}
	})
	t.Run("窗口外但引用了线发的那条就认领", func(t *testing.T) {
		t.Parallel()
		te := newThreadEnv(t, replyScript(llmtest.Call("c3", "thread_send", callSendReply), llmtest.Text("回他了。")), "")
		te.wake()
		st, _ := te.a.set.ForConversation(te.key.Conversation)
		var id string
		if err := st.Events.QueryRow(`SELECT id FROM events WHERE conversation=? AND actor_kind='character'`, te.key.Conversation).Scan(&id); err != nil {
			t.Fatal(err)
		}
		te.e.setNow(te.e.now.Add(3 * time.Hour))
		q := api.LocalPart{Quote: &api.LocalQuote{Ref: api.EventRef{Conversation: te.key, Event: api.EventKey{Platform: "local", EventID: id}}}, Text: "回你啦"}
		if err := te.d.Reader.SendLocalParts(context.Background(), te.key, []api.LocalPart{q}, nil); err != nil {
			t.Fatal(err)
		}
		te.idle()
		if len(te.claims(proactive.ClaimReplied)) != 1 {
			t.Fatalf("引用线发的那条，不看时间：%+v", te.claims(""))
		}
	})
}

// 续醒：发话后 20 分钟他没回，叫醒同一条线；回合没再发话就回到等回复；最多 2 次；超过 closeAfterHours 收线记没回（冷）
func TestFollowUpThenTimeoutClosesCold(t *testing.T) {
	t.Parallel()
	te := newThreadEnv(t, replyScript(llmtest.Text("再等等"), llmtest.Text("还是等等")), "")
	te.wake()
	th := te.thread()
	te.e.setNow(th.LastSendAt.Add(21 * time.Minute))
	te.a.proactive.Tick(context.Background(), te.e.now)
	te.idle()
	th = te.thread()
	if th.State != proactive.ThreadAwaiting || th.FollowUps != 1 {
		t.Fatalf("续醒一次，回到等回复：%+v", th)
	}
	var follow *proactive.Row
	for _, r := range te.rows() {
		r := r
		if r.Kind == proactive.KindFollowUp {
			follow = &r
		}
	}
	if follow == nil || follow.Thread != th.ID || follow.Reason != reasonModelNoStance {
		t.Fatalf("续醒也有日志行：%+v", follow)
	}
	if b := te.body(4); !strings.Contains(b, "（导演：续醒。") || !strings.Contains(b, "第 1 次续醒，最多 2 次") {
		t.Fatalf("续醒行：%s", b)
	}
	// 续醒不另外带“最近说过什么”：线的历史里已经有开线时的醒来行和她自己发的那句话
	if b := te.body(4); !strings.Contains(b, "（导演：现在 ") || !strings.Contains(b, "在忙吗？昨天那件事我一直惦记着") {
		t.Fatalf("续醒时线的历史里应当还看得到醒来行和她发过的话：%s", b)
	}
	// 第二次续醒
	te.e.setNow(te.e.now.Add(21 * time.Minute))
	te.a.proactive.Tick(context.Background(), te.e.now)
	te.idle()
	if th = te.thread(); th.FollowUps != 2 {
		t.Fatalf("第二次续醒：%+v", th)
	}
	// 没有第三次
	te.e.setNow(te.e.now.Add(21 * time.Minute))
	before := te.e.llmCalls()
	te.a.proactive.Tick(context.Background(), te.e.now)
	te.idle()
	if te.e.llmCalls() != before {
		t.Fatal("续醒最多 2 次")
	}
	// 到 closeAfterHours：收线记没回，导演系数按冷变
	te.e.setNow(th.FirstSendAt.Add(6*time.Hour + time.Minute))
	te.a.proactive.Tick(context.Background(), te.e.now)
	th = te.thread()
	if th.State != proactive.ThreadClosed || th.CloseReason != proactive.ClosedNoReply {
		t.Fatalf("到时收线记没回：%+v", th)
	}
	st0, _, _ := (proactive.DBStore{DB: te.a.set.Trace()}).LoadState(testChar)
	if st0.Factor != 1.5 || st0.ColdStreak != 1 {
		t.Fatalf("没回是冷：系数 ×1.5、连冷 1：%+v", st0)
	}
	var closeRow *proactive.Row
	for _, r := range te.rows() {
		r := r
		if r.Kind == proactive.KindClose {
			closeRow = &r
		}
	}
	if closeRow == nil || closeRow.Reason != "no-reply" || closeRow.Thread != th.ID {
		t.Fatalf("收线有一行：%+v", closeRow)
	}
	// 结论存进线里，带标签
	thSt, _ := te.a.set.ForConversation(th.Conversation)
	var concl, seg string
	if err := thSt.Events.QueryRow(`SELECT text, segments_json FROM events WHERE conversation=? AND origin='proactive:conclusion'`, th.Conversation).Scan(&concl, &seg); err != nil || !strings.Contains(concl, "没等到回复") || !strings.Contains(seg, "proactive-thread") {
		t.Fatalf("线里留结论事件，带线标签：%q %q %v", concl, seg, err)
	}
}

// 聊过又没下文：她回完以后闲够 chatIdle（10 分钟）收线记聊完，不算冷
func TestIdleAfterChatClosesDone(t *testing.T) {
	t.Parallel()
	te := newThreadEnv(t, replyScript(llmtest.Call("c3", "thread_send", callSendReply), llmtest.Text("回他了。")), "")
	te.wake()
	te.e.setNow(te.e.now.Add(2 * time.Minute))
	te.ownerSays("在的")
	te.idle()
	th := te.thread()
	if th.State != proactive.ThreadAwaiting || th.OwnerReplies != 1 {
		t.Fatalf("%+v", th)
	}
	te.e.setNow(th.LastSendAt.Add(9 * time.Minute))
	te.a.proactive.Tick(context.Background(), te.e.now)
	if te.thread().State == proactive.ThreadClosed {
		t.Fatal("没到 10 分钟不收")
	}
	te.e.setNow(th.LastSendAt.Add(11 * time.Minute))
	te.a.proactive.Tick(context.Background(), te.e.now)
	th = te.thread()
	if th.State != proactive.ThreadClosed || th.CloseReason != proactive.ClosedDone {
		t.Fatalf("聊过又没下文，闲够了收线记聊完：%+v", th)
	}
	if st0, _, _ := (proactive.DBStore{DB: te.a.set.Trace()}).LoadState(testChar); st0.ColdStreak != 0 {
		t.Fatalf("回过就不是冷：%+v", st0)
	}
}

// 认领后宽限到点（线里卡着没表态），导演轮询时自动交回
func TestStuckClaimIsHandedBackAfterGrace(t *testing.T) {
	t.Parallel()
	te := newThreadEnv(t, replyScript(llmtest.Text("普通对话来接")), "")
	te.wake()
	h := te.a.pthreads
	th := te.thread()
	st, _ := te.a.set.ForConversation(te.key.Conversation)
	seq, err := st.AppendEvent(newOwnerEvent(te, "有人吗"), []string{testChar})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.AddClaim(te.key.Conversation, seq, th.ID, te.e.now); err != nil {
		t.Fatal(err)
	}
	h.mgr.Apply(th.ID, proactive.Event{Kind: proactive.EvClaim, At: te.e.now})
	te.e.setNow(te.e.now.Add(time.Minute))
	h.sweep(context.Background(), te.e.now)
	if cs := te.claims(proactive.ClaimHanded); len(cs) != 0 {
		t.Fatal("宽限（2 分钟）没到不交回")
	}
	te.e.setNow(te.e.now.Add(90 * time.Second))
	h.sweep(context.Background(), te.e.now)
	te.waitNormalReply("普通对话来接")
	if cs := te.claims(proactive.ClaimHanded); len(cs) != 1 {
		t.Fatalf("宽限到点自动交回：%+v", te.claims(""))
	}
	h.sweep(context.Background(), te.e.now) // 再扫一遍不会重复交回
	time.Sleep(50 * time.Millisecond)
	if n := te.e.llmCalls(); n != 4 {
		t.Fatalf("交回只起一次普通回合：%d", n)
	}
}

// 落地记录：线发到本地私聊的话，该会话已经活着的普通实例下一轮也看得到（MarkStale 让它重建），而且是"她说的"，不是"系统替你自动发的"
func TestProactiveMessageIsVisibleToLiveNormalInstance(t *testing.T) {
	t.Parallel()
	script := llmtest.Script{Replies: []llmtest.Reply{
		llmtest.Text("先聊一句"), // 请求 1：普通回合，建起活着的实例
		llmtest.Call("c1", "owner_presence", callPresence),
		llmtest.Call("c2", "thread_send", callSendT1),
		llmtest.Text("发好了。"),
		llmtest.Text("记得，你问的是昨天那件事"), // 请求 5：普通回合
	}}
	te := newThreadEnv(t, script, "")
	te.ownerSays("早")
	te.waitNormalReply("先聊一句")
	te.wake()
	te.e.setNow(te.e.now.Add(11 * time.Minute)) // 窗口外：这一句走普通路径
	te.ownerSays("你刚才找我是想说什么来着")
	te.waitNormalReply("记得，你问的是昨天那件事")
	b := te.body(5)
	if strings.Contains(b, "系统替你自动发的") {
		t.Fatalf("线发的话不能被渲染成系统替她发的：%s", b)
	}
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(b), &req); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range req.Messages {
		if m.Role == "assistant" && strings.Contains(string(m.Content), "在忙吗？昨天那件事我一直惦记着") {
			found = true
		}
	}
	if !found {
		t.Fatalf("普通实例重建后应当看到她主动发的话（assistant）：%s", b)
	}
}

// 线的实例重建后（回收、重启、压缩），历史里仍有醒来行和线里的来回（toMessage 对线的 system 事件开的例外），
// 而且 system 行以用户消息的身份出现，不是"主人说的"以外的别的东西
func TestThreadHistoryKeepsWakeLineAfterRebuild(t *testing.T) {
	t.Parallel()
	te := newThreadEnv(t, replyScript(llmtest.Text("再等等")), "")
	te.wake()
	th := te.thread()
	te.a.hub.MarkStale(testChar, th.Conversation) // 线的实例下一轮从库里重建
	te.e.setNow(th.LastSendAt.Add(21 * time.Minute))
	te.a.proactive.Tick(context.Background(), te.e.now)
	te.idle()
	b := te.body(4)
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(b), &req); err != nil {
		t.Fatal(err)
	}
	var wakeUser, sendCall bool
	for _, m := range req.Messages {
		c := string(m.Content)
		if m.Role == "user" && strings.Contains(c, "（导演：现在 16:00") {
			wakeUser = true
		}
		if m.Role == "assistant" && strings.Contains(b, "thread_send") {
			sendCall = true
		}
	}
	if !wakeUser || !sendCall {
		t.Fatalf("重建后历史里要有第一次的醒来行（用户消息）和她的工具调用：醒来行 %v、发送 %v\n%s", wakeUser, sendCall, b)
	}
	if !strings.Contains(b, "（导演：续醒。") {
		t.Fatal("续醒行追加在末尾")
	}
}
