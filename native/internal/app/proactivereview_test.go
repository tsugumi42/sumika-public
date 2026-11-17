package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/proactive"
	"sumika-native/internal/store"
)

// 复核意见的回归测试（规格-主动联系 第十八节）：每条在旧代码上都是红的。名字、话都是编的。

// 线里的回合还在跑（慢模型、或排在前一句后面），认领宽限到了也不交回：交回会让主人那句被回两次
func TestClaimNotHandedBackWhileThreadTurnIsRunning(t *testing.T) {
	t.Parallel()
	script := replyScript(llmtest.Text("线里正在想……"), llmtest.Text("普通对话回你了"))
	script.Hold = func(nth int) int {
		if nth == 4 { // 认领后线里那一轮：吐出第一段前就卡住
			return 0
		}
		return -1
	}
	te := newThreadEnv(t, script, "")
	te.wake()
	te.ownerSays("你在吗")
	waitFor(t, "线里的认领轮卡在请求上", func() bool { return te.e.llmCalls() == 4 })
	te.e.setNow(te.e.now.Add(5 * time.Minute)) // 远过 claimGraceMinutes（2 分钟）
	te.a.pthreads.sweep(context.Background(), te.e.now)
	if cs := te.claims(proactive.ClaimPending); len(cs) != 1 {
		t.Fatalf("线里的轮还在跑，认领不该被宽限交回：%+v", te.claims(""))
	}
	time.Sleep(50 * time.Millisecond)
	if n := te.e.llmCalls(); n != 4 || hasRow(te.localRows(), "character/character/普通对话回你了") {
		t.Fatalf("不该起普通回合：请求 %d 次", n)
	}
	// 轮被停以后照旧立刻交回
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
	if cs := te.claims(proactive.ClaimHanded); len(cs) != 1 {
		t.Fatalf("轮结束后交回：%+v", te.claims(""))
	}
}

// 在跑的表按轮记：前一轮收尾只拿掉自己，排在后面的轮仍算在跑，最老的那一轮是正在执行的
func TestRunTableKeepsQueuedRuns(t *testing.T) {
	t.Parallel()
	te := newThreadEnv(t, llmtest.Script{Replies: []llmtest.Reply{llmtest.Text("x")}}, "")
	h := te.a.pthreads
	t0 := te.e.now
	h.mu.Lock()
	h.runs["q1"] = []threadRun{{id: 1, started: t0}, {id: 2, started: t0.Add(time.Minute)}}
	h.mu.Unlock()
	if at, ok := h.currentRunStart("q1"); !ok || !at.Equal(t0) {
		t.Fatalf("最老的一轮：%v %v", at, ok)
	}
	h.mu.Lock()
	h.removeRunLocked("q1", 1)
	h.mu.Unlock()
	if at, ok := h.currentRunStart("q1"); !ok || !at.Equal(t0.Add(time.Minute)) || !h.running("q1") {
		t.Fatalf("前一轮收尾后，后面的轮仍在：%v %v", at, ok)
	}
	h.mu.Lock()
	h.removeRunLocked("q1", 2)
	h.mu.Unlock()
	if h.running("q1") {
		t.Fatal("全收尾后不算在跑")
	}
}

// thread_send 只把这一轮起跑以前认领的算成已回；跑着时又来的那句（排在后面的轮里）仍是 pending
func TestThreadSendRepliesOnlyClaimsTheRunHasSeen(t *testing.T) {
	t.Parallel()
	te := newThreadEnv(t, llmtest.Script{Replies: []llmtest.Reply{llmtest.Text("x")}}, "")
	te.openThreadForTool("cl1")
	h := te.a.pthreads
	conv := localConvOf(testChar)
	t0 := te.e.now
	h.mu.Lock()
	h.runs["cl1"] = []threadRun{{id: 1, started: t0, cancel: func() {}}, {id: 2, started: t0.Add(time.Minute), cancel: func() {}}}
	h.mu.Unlock()
	if err := h.mgr.AddClaim(conv, 1001, "cl1", t0); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.AddClaim(conv, 1002, "cl1", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	res := te.run(threadCall("thread_send", "cl1", `{"target":"t1","text":"回你啦"}`))
	if res.IsError {
		t.Fatalf("发送应当成功：%+v", res)
	}
	cs, _ := h.mgr.ClaimsOf("cl1", "")
	if len(cs) != 2 || cs[0].Seq != 1001 || cs[0].State != proactive.ClaimReplied || cs[1].Seq != 1002 || cs[1].State != proactive.ClaimPending {
		t.Fatalf("只有第一句算回过，第二句留给后面的轮：%+v", cs)
	}
}

// 主人带上传的图回了她：不认领（要替主人写库，附件还没存），但"他回过"照记，不会被当成没回再催一次
func TestUploadReplyCountsAsOwnerReplied(t *testing.T) {
	t.Parallel()
	te := newThreadEnv(t, replyScript(llmtest.Text("收到图了")), "")
	te.wake()
	te.e.setNow(te.e.now.Add(2 * time.Minute))
	if err := te.d.Reader.SendLocalImages(context.Background(), te.key, "看这个", [][]byte{tinyPNG(t)}, nil); err != nil {
		t.Fatal(err)
	}
	te.waitNormalReply("收到图了")
	if len(te.claims("")) != 0 {
		t.Fatalf("带上传图的不认领：%+v", te.claims(""))
	}
	th := te.thread()
	if th.OwnerReplies != 1 || th.State != proactive.ThreadAwaiting {
		t.Fatalf("旁听记他回过：%+v", th)
	}
	if _, due := proactive.FollowUpAt(te.a.cfg.Proactive, th); due {
		t.Fatal("他回过就取消续醒，不再催")
	}
}

// 关机时（ctx 已取消）交回不先标成已交回：认领留在 pending，下次启动才补得回
func TestHandBackDuringShutdownKeepsClaimPending(t *testing.T) {
	t.Parallel()
	te := newThreadEnv(t, llmtest.Script{Replies: []llmtest.Reply{llmtest.Text("x")}}, "")
	th := te.openThreadForTool("sd1")
	h := te.a.pthreads
	conv := localConvOf(testChar)
	if err := h.mgr.AddClaim(conv, 2001, "sd1", te.e.now); err != nil {
		t.Fatal(err)
	}
	te.a.autoCancel()
	c := proactive.Claim{Conversation: conv, Seq: 2001, Thread: "sd1", State: proactive.ClaimPending, At: te.e.now}
	if h.handBack(th, c, false) {
		t.Fatal("关机中不交回")
	}
	if cs, _ := h.mgr.ClaimsOf("sd1", proactive.ClaimPending); len(cs) != 1 {
		t.Fatalf("认领仍是 pending：%+v", cs)
	}
}

// 重启：想着没发过话的线补做收线（结论、日志、导演下次醒回到正常间隔）；被认领、还没回的主人那句由普通回合补回
func TestRestartFinishesClosedThreadAndRepliesToLostClaim(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	script := llmtest.Script{Replies: []llmtest.Reply{llmtest.Text("补回了你")},
		Models: []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}}
	srv := httptest.NewServer(llmtest.Handler(script, &e.bodies, &e.mu))
	t.Cleanup(srv.Close)
	e.llm = srv
	e.configure(threadCfg)
	a1 := e.open()
	now := e.now
	conv := localConvOf(testChar)
	if _, err := a1.pthreads.mgr.Create(proactive.Thread{ID: "rs1", Character: testChar, Conversation: threadConvOf("rs1"), OpenedAt: now}); err != nil {
		t.Fatal(err)
	}
	a1.Close()
	a2 := e.open()
	t.Cleanup(a2.cleanup)
	a2.runProactive()
	th, err := a2.pthreads.mgr.Get("rs1")
	if err != nil || th.State != proactive.ThreadClosed || th.CloseReason != proactive.ClosedError {
		t.Fatalf("想着没发过话的线重启后关成 error：%+v %v", th, err)
	}
	var closeRows int
	for _, r := range proactiveRows(t, a2) {
		if r.Kind == proactive.KindClose && r.Thread == "rs1" {
			closeRows++
		}
	}
	if closeRows != 1 {
		t.Fatalf("补做收线：应当有一行 close 日志：%d", closeRows)
	}
	st, _, _ := (proactive.DBStore{DB: a2.set.Trace()}).LoadState(testChar)
	if !st.NextWake.After(now.Add(44 * time.Minute)) {
		t.Fatalf("下次醒回到正常间隔，不是 20 分钟后：%v", st.NextWake.Sub(now))
	}
	thSt, _ := a2.set.ForConversation(th.Conversation)
	var concl string
	if err := thSt.Events.QueryRow(`SELECT text FROM events WHERE conversation=? AND origin='proactive:conclusion'`, th.Conversation).Scan(&concl); err != nil || !strings.Contains(concl, "结论") {
		t.Fatalf("线里留结论：%q %v", concl, err)
	}

	// 第二次：线发过话、主人回了被认领、进程退出
	m := a2.pthreads.mgr
	if _, err := m.Create(proactive.Thread{ID: "rs2", Character: testChar, Conversation: threadConvOf("rs2"), OpenedAt: now}); err != nil {
		t.Fatal(err)
	}
	s, err := m.BeginSend("rs2", conv, "本地私聊", "本地私聊", "话")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.FinishSend(s.ID, proactive.SendSent, 1, "e1"); err != nil {
		t.Fatal(err)
	}
	lst, _ := a2.set.ForConversation(conv)
	seq, err := lst.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "我在呢", CreatedAt: now.UnixMilli()}, []string{testChar})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AddClaim(conv, seq, "rs2", now); err != nil {
		t.Fatal(err)
	}
	a2.Close()
	a3 := e.open()
	t.Cleanup(a3.cleanup)
	a3.runProactive()
	waitFor(t, "重启后普通回合补回了主人那句", func() bool { return hasRow(slashRows(t, a3, conv), "character/character/补回了你") })
	if cs, _ := a3.pthreads.mgr.ClaimsOf("rs2", proactive.ClaimLost); len(cs) != 1 {
		t.Fatalf("认领记 lost：%+v", cs)
	}
	if n := e.llmCalls(); n != 1 {
		t.Fatalf("只补回一次：%d", n)
	}
}
