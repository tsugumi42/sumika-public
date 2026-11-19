package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/llm/llmtest"
)

type attentionLog struct {
	mu  sync.Mutex
	got []Attention
}

func (l *attentionLog) add(a Attention) { l.mu.Lock(); l.got = append(l.got, a); l.mu.Unlock() }
func (l *attentionLog) kinds() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, a := range l.got {
		out = append(out, a.Kind)
	}
	return out
}
func (l *attentionLog) all() []Attention {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Attention(nil), l.got...)
}

func attentionDesktop(t *testing.T, e *testEnv, script llmtest.Script) (*App, *Desktop, *attentionLog) {
	t.Helper()
	log := &attentionLog{}
	a, d := autoDesktop(t, e, script)
	a.opt.Attention = log.add // 装配之后、有活动之前接上
	return a, d, log
}

// 目标跑了三轮才达成：只在达成时说一次，中间两轮续轮不说。
func TestAttentionGoalDoneOnceNotEveryRound(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d, log := attentionDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{
		llmtest.Text("第一轮"), llmtest.Text(judgeNo), llmtest.Text("第二轮"), llmtest.Text(judgeNo), llmtest.Text("第三轮"), llmtest.Text(judgeYes),
	}})
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(context.Background(), key, "/goal 做事", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "目标达成", func() bool { return hasRow(slashRows(t, a, key.Conversation), "目标达成：") })
	// 提醒在写完那一行之后才异步发出，先等它到（前两轮的“未达成”都在这之前，多发也逃不掉）
	waitFor(t, "收到提醒", func() bool { return len(log.kinds()) >= 1 })
	if got := log.kinds(); len(got) != 1 || got[0] != AttentionGoalDone {
		t.Fatalf("应只在达成时说一次：%v", got)
	}
	ev := log.all()[0]
	if ev.Conversation != key.Conversation || ev.Character != testChar || !strings.Contains(ev.Body, "文件已读回") || ev.Title != "目标达成" {
		t.Fatalf("内容不对：%+v", ev)
	}
}

// 暂停要主人看一眼的才说（连着两轮卡住）；主人自己点停止、/goal pause 不说。
func TestAttentionGoalPausedOnlyWhenOwnerShouldLook(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	blocked := `{"achieved": false, "blocked": true, "reason": "卡在权限上"}`
	a, d, log := attentionDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{
		llmtest.Text("第一轮"), llmtest.Text(blocked), llmtest.Text("第二轮"), llmtest.Text(blocked),
	}})
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(context.Background(), key, "/goal 做事", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "目标暂停", func() bool { return hasRow(slashRows(t, a, key.Conversation), "目标已暂停") })
	waitFor(t, "收到提醒", func() bool { return len(log.kinds()) == 1 })
	if got := log.kinds(); got[0] != AttentionGoalPaused || !strings.Contains(log.all()[0].Body, "卡住") {
		t.Fatalf("卡住暂停应提醒：%+v", log.all())
	}
}

// 主人自己点停止：目标暂停，但不提醒（主人正在看着）。
func TestAttentionOwnerStopIsSilent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d, log := attentionDesktop(t, e, llmtest.Script{
		Replies: []llmtest.Reply{llmtest.Text("第一轮进展"), llmtest.Text(judgeNo), llmtest.Text("续轮前半", "续轮后半")},
		Hold: func(nth int) int {
			if nth == 3 {
				return 1
			}
			return -1
		},
	})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(ctx, key, "/goal 做事", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "续轮开始", func() bool { return e.llmCalls() == 3 && a.hub.Running(testChar, key.Conversation) })
	if err := d.Reader.StopLocal(ctx, key); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "目标暂停", func() bool { return hasRow(slashRows(t, a, key.Conversation), "目标已暂停：主人停止") })
	waitFor(t, "自动轮收尾", func() bool { return !a.hub.Running(testChar, key.Conversation) && !a.auto.has(key.Conversation) })
	time.Sleep(100 * time.Millisecond)
	if got := log.kinds(); len(got) != 0 {
		t.Fatalf("主人自己停止不该提醒：%v", got)
	}
}

// 定期任务：有内容提醒，[无事] 不提醒。
func TestAttentionLoopOnlyForContent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d, log := attentionDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{llmtest.Text("下雨了，带伞"), llmtest.Text("[无事]")}})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(ctx, key, "/loop 1m 看看天气", nil); err != nil {
		t.Fatal(err)
	}
	a.loops.Tick(ctx)
	a.loops.Wait()
	if got := log.all(); len(got) != 1 || got[0].Kind != AttentionLoop || got[0].Title != "定期任务 #1 有新消息" || !strings.Contains(got[0].Body, "带伞") {
		t.Fatalf("有内容的一轮应提醒：%+v", got)
	}
	e.setNow(e.now.Add(2 * time.Minute))
	a.loops.Tick(ctx)
	a.loops.Wait()
	if got := log.kinds(); len(got) != 1 {
		t.Fatalf("[无事] 不该提醒：%v", got)
	}
}

// 自检话题（sumika-ctl 开发用）里的目标不提醒。
func TestAttentionSkipsSelftestTopic(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d, log := attentionDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{llmtest.Text("做完了"), llmtest.Text(judgeYes)}})
	res, err := d.Reader.SlashCommand(context.Background(), slashReq("goal", "set", "做事"))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "目标达成", func() bool { return hasRow(slashRows(t, a, res.Conversation), "目标达成：") })
	time.Sleep(100 * time.Millisecond)
	if got := log.kinds(); len(got) != 0 {
		t.Fatalf("自检话题不该提醒：%v", got)
	}
}

// 审批卡出现：本地私聊的提醒，别的会话（比如群聊）的不提醒；同一会话 20 秒内第二张不重复提醒。
func TestAttentionApproval(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, _, log := attentionDesktop(t, e, llmtest.Script{})
	ask := func(id, conv string) context.CancelFunc {
		ctx, cancel := context.WithCancel(context.Background())
		go a.approvals.Ask(ctx, approval.Request{ID: id, CharacterID: testChar, Conversation: conv, Tool: "write", Title: "写编的文件", CreatedAt: 1, ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}, nil)
		return cancel
	}
	defer ask("a1", "console:"+testChar)()
	waitFor(t, "收到审批提醒", func() bool { return len(log.kinds()) == 1 })
	if ev := log.all()[0]; ev.Kind != AttentionApproval || ev.Body != "写编的文件" || ev.Conversation != "console:"+testChar {
		t.Fatalf("内容不对：%+v", ev)
	}
	defer ask("a2", "console:"+testChar)()
	defer ask("a3", "qq:group:1")()
	time.Sleep(300 * time.Millisecond)
	if got := log.kinds(); len(got) != 1 {
		t.Fatalf("同一会话短时间内不重复、别的场合不提醒：%v", got)
	}
}
