package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/loop"
	"sumika-native/internal/turn"
)

// visibleKinds 数 ReadMessages 里每种消息各几条。
func visibleMessages(t *testing.T, d *Desktop, key api.ConversationKey) []api.MessageView {
	t.Helper()
	page, err := d.Reader.ReadMessages(context.Background(), api.ConversationRequest{Key: key, Limit: 50}, "")
	if err != nil {
		t.Fatal(err)
	}
	return page.Value.Items
}

// /loop 1m 任务：跑两次，[无事] 那次不显示；有内容那次显示（主人身份的输入标成定期任务，不当成主人说的话）。
func TestLoopRunsTwiceAndQuietIsHidden(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := autoDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{llmtest.Text("现在十二点"), llmtest.Text("[无事]")}})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(ctx, key, "/loop 1m 报一下现在几点", nil); err != nil {
		t.Fatal(err)
	}
	if e.llmCalls() != 0 || !hasRow(slashRows(t, a, key.Conversation), "已建立 #1 每 1 分钟") {
		t.Fatalf("/loop 不该发给模型，要有建立的系统行：%d %q", e.llmCalls(), slashRows(t, a, key.Conversation))
	}
	a.loops.Tick(ctx)
	a.loops.Wait()
	afterFirst := visibleMessages(t, d, key)
	var kinds []string
	for _, m := range afterFirst {
		kinds = append(kinds, m.Kind+"/"+m.Origin+"/"+m.DisplayName)
	}
	joined := strings.Join(kinds, " ")
	if !strings.Contains(joined, "message/loop/定期任务") || !strings.Contains(joined, "message/character/") {
		t.Fatalf("第一次应显示输入和回答：%v", kinds)
	}
	for _, m := range afterFirst {
		if m.Origin == "loop" && m.DisplayName == "主人" {
			t.Fatal("定期任务的输入不该显示成主人说的")
		}
	}
	e.setNow(e.now.Add(time.Minute))
	a.loops.Tick(ctx)
	a.loops.Wait()
	afterSecond := visibleMessages(t, d, key)
	if len(afterSecond) != len(afterFirst) {
		t.Fatalf("[无事] 那次不该多出显示的行：%d → %d", len(afterFirst), len(afterSecond))
	}
	rows := slashRows(t, a, key.Conversation)
	if !hasRow(rows, "message/character/automation:loop-quiet/[无事]") || !hasRow(rows, "message/owner/automation:loop-quiet/") {
		t.Fatalf("[无事] 那一轮应存成隐藏来源：\n%s", strings.Join(rows, "\n"))
	}
	if e.llmCalls() != 2 {
		t.Fatalf("模型请求次数：%d", e.llmCalls())
	}
	// 隐藏的行不进下一轮她的历史：第 3 次请求里看不到 [无事]
	e.setNow(e.now.Add(time.Minute))
	a.loops.Tick(ctx)
	a.loops.Wait()
	e.mu.Lock()
	last := e.bodies[len(e.bodies)-1]
	e.mu.Unlock()
	_ = last
	if tasks := a.loops.List(key.Conversation); len(tasks) != 1 || tasks[0].Runs != 3 {
		t.Fatalf("任务表：%+v", tasks)
	}
	// /loop stop
	if err := d.Reader.SendLocal(ctx, key, "/loop stop", nil); err != nil {
		t.Fatal(err)
	}
	if tasks := a.loops.List(key.Conversation); tasks[0].Status != loop.StatusStopped {
		t.Fatalf("stop 之后应停止：%+v", tasks[0])
	}
}

// schedule_next 只在定期任务的那一轮里、只对本地私聊主人可见；别的会话（普通聊天）的请求里没有。
func TestScheduleNextOnlyInLoopRound(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := autoDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{
		llmtest.Text("普通回答"),
		llmtest.Call("c1", "schedule_next", `{"seconds":120,"reason":"盯紧一点"}`), llmtest.Text("好，两分钟后再看"),
	}})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(ctx, key, "普通聊天", nil); err != nil {
		t.Fatal(err)
	}
	if err := d.Reader.SendLocal(ctx, key, "/loop 看着点", nil); err != nil {
		t.Fatal(err)
	}
	a.loops.Tick(ctx)
	a.loops.Wait()
	e.mu.Lock()
	bodies := append([]string(nil), e.bodies...)
	e.mu.Unlock()
	if len(bodies) != 3 {
		t.Fatalf("请求数：%d", len(bodies))
	}
	if strings.Contains(bodies[0], "schedule_next") {
		t.Fatal("普通聊天的请求里不该有 schedule_next")
	}
	if !strings.Contains(bodies[1], "schedule_next") {
		t.Fatal("定期任务那一轮的请求里该有 schedule_next")
	}
	task := a.loops.List(key.Conversation)[0]
	if task.NextReason != "盯紧一点" || !task.NextRun.Equal(e.now.Add(2*time.Minute)) {
		t.Fatalf("她定的下次时间没生效：%+v", task)
	}
}

// schedule_next 在非本地私聊主人的场合不可见，也执行不了（即使会话号对得上、调度器里有在途的轮）。
func TestScheduleNextVisibilityByScene(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, _ := autoDesktop(t, e, llmtest.Script{Reply: llmtest.Text("x")})
	conv := "console:" + testChar
	type result struct{ visible, execOK bool }
	got := map[string]result{}
	ownerScope := kernel.Scope{CharacterID: testChar, Conversation: conv, Platform: "local", Channel: "console", Scene: "private", ActorKind: "owner", ActorID: "owner"}
	qqScope := ownerScope
	qqScope.Platform, qqScope.Channel = "qq", "qq"
	humanScope := ownerScope
	humanScope.ActorKind, humanScope.ActorID = "human", "p1"
	grpAud := audience.Console(conv)
	grpAud.Scene = "group"
	qqAud := audience.Audience{Channel: "qq", Scene: "private", Conversation: conv, PrivateOK: true}
	probe := func(name string, aud audience.Audience, sc kernel.Scope) {
		tool := loopTool{app: a}
		args := mustArgs(t, `{"seconds":120,"reason":"x"}`)
		res, _ := tool.exec(context.Background(), turn.ToolCall{Identity: kernel.Invocation{Scope: sc}, CharacterID: testChar, Conversation: conv, Audience: aud, Args: args})
		got[name] = result{visible: tool.visible(aud), execOK: !res.IsError}
	}
	s, err := loop.New(loop.Options{Store: loop.FileStore{Path: a.paths.LoopFile() + ".probe"}, Now: a.now, Run: func(context.Context, loop.Request) (string, error) {
		probe("本地私聊主人", audience.Console(conv), ownerScope)
		probe("QQ 私聊", qqAud, qqScope)
		probe("本地群聊", grpAud, ownerScope)
		probe("非主人", audience.Console(conv), humanScope)
		return "ok", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	// 用探针调度器代替：在途的轮里逐个场合试
	saved := a.loops
	a.loops = s
	defer func() { a.loops = saved }()
	s.Add(conv, "探针", loop.AddOptions{})
	s.Tick(context.Background())
	s.Wait()
	want := map[string]result{"本地私聊主人": {true, true}, "QQ 私聊": {false, false}, "本地群聊": {false, false}, "非主人": {true, false}}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s：%+v，想要 %+v", name, got[name], w)
		}
	}
	// 没有在途的轮：本地私聊主人也看不见
	if (loopTool{app: a}).visible(audience.Console(conv)) {
		t.Error("没有在途的定期任务轮时不该可见")
	}
}

func mustArgs(t *testing.T, raw string) jsjson.Value {
	t.Helper()
	v, err := jsjson.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
