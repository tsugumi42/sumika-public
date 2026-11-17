package app

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
	"sumika-native/internal/askowner"
	"sumika-native/internal/audience"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/turn"
)

// 向主人提问（规格-向主人提问）的装配测试：完整路径走正式写接口，假模型只回事先排好的工具调用。

const askArgs = `{"question":"编的问题：先做哪个","options":[{"label":"方案甲","detail":"快"},{"label":"方案乙"}]}`

func askRows(t *testing.T, a *App, key api.ConversationKey) []string {
	t.Helper()
	return slashRows(t, a, key.Conversation)
}

// waitQuestion 等她把问题挂出来，返回卡片
func waitQuestion(t *testing.T, d *Desktop) api.LocalQuestion {
	t.Helper()
	sub, err := api.LocalQuestions(&d.Reader).WatchLocalQuestions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for {
		snap := localRecv(t, sub.Updates())
		if len(snap.Value) > 0 {
			return snap.Value[0]
		}
	}
}

func bodiesOf(e *testEnv) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.bodies...)
}

func sendAsync(d *Desktop, key api.ConversationKey, text string) chan error {
	done := make(chan error, 1)
	go func() { done <- d.Reader.SendLocal(context.Background(), key, text, nil) }()
	return done
}

func indexOfRow(rows []string, sub string) int {
	for i, r := range rows {
		if strings.Contains(r, sub) {
			return i
		}
	}
	return -1
}

// 完整路径：模型收到工具 → 调用 → 卡片出现 → 点选项 → 结果进下一次请求 → 这一轮说完；记录顺序是问题、回答、回复。
func TestAskOwnerChosenFullPath(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("ask1", "ask_owner", askArgs), llmtest.Text("好，按甲做")}})
	key := localKey("console:" + testChar)
	done := sendAsync(d, key, "编的任务：二选一")
	q := waitQuestion(t, d)
	if q.CharacterID != testChar || q.Key != key || len(q.Options) != 2 || q.Options[0].Label != "方案甲" || q.Options[0].Detail != "快" {
		t.Fatalf("卡片内容不对：%+v", q)
	}
	if len(a.approvals.Snapshot()) != 0 {
		t.Fatalf("问题不该进审批：%+v", a.approvals.Snapshot())
	}
	if err := api.LocalQuestions(&d.Reader).AnswerLocal(context.Background(), key, q.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := localRecv(t, done); err != nil {
		t.Fatal(err)
	}
	bodies := bodiesOf(e)
	if len(bodies) != 2 || !strings.Contains(bodies[0], `"name":"ask_owner"`) {
		t.Fatalf("模型没收到工具或请求数不对：%d", len(bodies))
	}
	if !strings.Contains(bodies[1], `"tool_call_id":"ask1"`) || !strings.Contains(bodies[1], "所有者选了：「方案甲」") || !strings.Contains(bodies[1], "不是批准") {
		t.Fatalf("结果没进下一次请求：%s", bodies[1])
	}
	rows := askRows(t, a, key)
	iq, ia, ir := indexOfRow(rows, "message/character/character/编的问题"), indexOfRow(rows, "message/owner/owner/方案甲"), indexOfRow(rows, "好，按甲做")
	if iq < 0 || !(iq < ia && ia < ir) {
		t.Fatalf("记录顺序应是问题、回答、回复：%q", rows)
	}
	if len(d.localQuestions()) != 0 {
		t.Fatal("答完卡片还在")
	}
}

// 打字就是回答：等待中 SubmitLocal 的 start 变成对这一轮的插话，等待结束，工具结果在前、主人的话在后。
func TestAskOwnerTypedAnswerSteers(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("ask1", "ask_owner", askArgs), llmtest.Text("明白，按你说的做")}})
	key := localKey("console:" + testChar)
	done := sendAsync(d, key, "编的任务")
	waitQuestion(t, d)
	run := a.hub.ActiveRunID(testChar, key.Conversation)
	rec, err := d.Reader.SubmitLocal(context.Background(), key, api.LocalInputSubmission{Text: "都不要，先做丙", Mode: "start", ClientKey: "ask-typed"})
	if err != nil {
		t.Fatal(err)
	}
	if run == "" || rec.RunID != run {
		t.Fatalf("应插进正在等的这一轮 %q，回执是 %q", run, rec.RunID)
	}
	start := time.Now()
	if err := localRecv(t, done); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("插话后还在等到超时")
	}
	bodies := bodiesOf(e)
	if len(bodies) != 2 {
		t.Fatalf("请求数：%d", len(bodies))
	}
	i1, i2 := strings.Index(bodies[1], `"tool_call_id":"ask1"`), strings.Index(bodies[1], "都不要，先做丙")
	if i1 < 0 || i2 < 0 || i1 > i2 {
		t.Fatalf("工具结果应在主人的话前面：%d %d", i1, i2)
	}
	if !strings.Contains(bodies[1], "直接回了一句") || strings.Contains(bodies[1], "所有者选了") {
		t.Fatalf("自由回答要用中性措辞：%s", bodies[1])
	}
	if !hasRow(askRows(t, a, key), "message/owner/owner/都不要，先做丙") {
		t.Fatalf("主人的话没存下：%q", askRows(t, a, key))
	}
}

// 到期不算同意；同一轮再问直接不等。
func TestAskOwnerExpiresAndSecondDoesNotWait(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{
		llmtest.Call("ask1", "ask_owner", askArgs), llmtest.Call("ask2", "ask_owner", askArgs), llmtest.Text("没人回，我先停在这")}})
	a.approvalGate.ApprovalMinutes = 0.002
	key := localKey("console:" + testChar)
	if err := localRecv(t, sendAsync(d, key, "编的任务")); err != nil {
		t.Fatal(err)
	}
	bodies := bodiesOf(e)
	if len(bodies) != 3 {
		t.Fatalf("请求数：%d", len(bodies))
	}
	if !strings.Contains(bodies[1], "这不算同意") {
		t.Fatalf("到期结果没写不算同意：%s", bodies[1])
	}
	if !strings.Contains(bodies[2], "上一个问题就没等到回答") {
		t.Fatalf("同一轮再问应直接不等：%s", bodies[2])
	}
	if len(a.asks.Snapshot()) != 0 {
		t.Fatal("到期后卡片还在")
	}
}

// 等待中点停止：卡片立刻收回，这一轮收尾。
func TestAskOwnerStopWithdrawsCard(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("ask1", "ask_owner", askArgs), llmtest.Text("不该有")}})
	key := localKey("console:" + testChar)
	done := sendAsync(d, key, "编的任务")
	waitQuestion(t, d)
	start := time.Now()
	if err := d.Reader.StopLocal(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	localRecv(t, done)
	if time.Since(start) > 4*time.Second {
		t.Fatal("停止后卡片挂了太久")
	}
	if len(a.asks.Snapshot()) != 0 {
		t.Fatal("停止后卡片还在")
	}
	if !hasRow(askRows(t, a, key), "message/character/character/编的问题") {
		t.Fatalf("问题应留在记录里：%q", askRows(t, a, key))
	}
	if n := len(bodiesOf(e)); n != 1 {
		t.Fatalf("停止后不该再请求模型：%d", n)
	}
}

// 回答不是批准：答“允许”以后写文件照样弹审批；问题不在审批快照里。
func TestAskOwnerAnswerIsNotApproval(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	writeArgs, _ := json.Marshal(map[string]string{"purpose": "写编的文件", "path": "编的文件.txt", "content": "没批就不写"})
	args := `{"question":"可以写文件吗","options":[{"label":"允许"},{"label":"不允许"}]}`
	a, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{
		llmtest.Call("ask1", "ask_owner", args), llmtest.Call("w1", "write", string(writeArgs)), llmtest.Text("结束")}})
	target := filepath.Join(a.workdir(), "编的文件.txt")
	ctx := context.Background()
	key := localKey("console:" + testChar)
	asub, err := d.Reader.WatchLocalApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer asub.Close()
	done := sendAsync(d, key, "编的任务")
	q := waitQuestion(t, d)
	if err := api.LocalQuestions(&d.Reader).AnswerLocal(ctx, key, q.ID, 0); err != nil {
		t.Fatal(err)
	}
	var card api.LocalApproval
	for card.ID == "" {
		if snap := localRecv(t, asub.Updates()); len(snap.Value) > 0 {
			card = snap.Value[0]
		}
	}
	if card.Tool != "write" {
		t.Fatalf("答完“允许”以后写文件应照常弹审批：%+v", card)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("没批准就写了：%v", err)
	}
	if err := d.Reader.DecideLocal(ctx, key, card.ID, api.ApproveDeny); err != nil {
		t.Fatal(err)
	}
	localRecv(t, done)
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("拒绝后仍写了：%v", err)
	}
}

// AnswerLocal 的错误入口：别的会话、不存在、已结束、越界、重复点，都是 ErrQuestionGone，问题状态不变；负数是先不答。
func TestAnswerLocalBadEntries(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("ask1", "ask_owner", askArgs), llmtest.Text("好的，我自己定")}})
	ctx := context.Background()
	qs := api.LocalQuestions(&d.Reader)
	key := localKey("console:" + testChar)
	done := sendAsync(d, key, "编的任务")
	q := waitQuestion(t, d)
	st, _ := a.set.For("console")
	if _, err := st.Events.Exec(`INSERT INTO local_topics(id,character_id,created_at) VALUES ('another',?,1)`, testChar); err != nil {
		t.Fatal(err)
	}
	foreign := localKey(key.Conversation + ":topic:another")
	for name, c := range map[string]struct {
		k      api.ConversationKey
		id     string
		option int
	}{
		"别的会话": {foreign, q.ID, 0}, "空 ID": {key, "", 0}, "不存在": {key, "编的号", 0}, "越界": {key, q.ID, 2}, "很大": {key, q.ID, 1 << 30},
	} {
		if err := qs.AnswerLocal(ctx, c.k, c.id, c.option); !errors.Is(err, api.ErrQuestionGone) {
			t.Errorf("%s：%v", name, err)
		}
	}
	if len(a.asks.Snapshot()) != 1 {
		t.Fatal("错误入口不该动问题")
	}
	if err := qs.AnswerLocal(ctx, key, q.ID, api.LocalAnswerSkip); err != nil {
		t.Fatal(err)
	}
	if err := qs.AnswerLocal(ctx, key, q.ID, 0); !errors.Is(err, api.ErrQuestionGone) {
		t.Fatalf("连点第二次：%v", err)
	}
	localRecv(t, done)
	if b := bodiesOf(e); len(b) != 2 || !strings.Contains(b[1], "先不答") {
		t.Fatalf("先不答没进结果：%v", b)
	}
}

// 无人值守：目标进行中不挂卡片、不等，问题带选项留在聊天里；她问完就收尾，目标暂停，不再自动续。
func TestAskOwnerInGoalDoesNotWaitAndPauses(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := autoDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{
		llmtest.Call("ask1", "ask_owner", askArgs), llmtest.Text("我定不了：甲乙差别太大，等你回话"), llmtest.Text(judgeNo)}})
	key := localKey("console:" + testChar)
	start := time.Now()
	if err := d.Reader.SendLocal(context.Background(), key, "/goal 编的目标", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "目标暂停", func() bool { return hasRow(askRows(t, a, key), "目标已暂停") })
	if time.Since(start) > 8*time.Second {
		t.Fatal("目标里的提问不该等")
	}
	if len(a.asks.Snapshot()) != 0 {
		t.Fatal("无人值守不该挂卡片")
	}
	rows := askRows(t, a, key)
	if !hasRow(rows, "message/character/character/编的问题：先做哪个\n可选：方案甲 / 方案乙") {
		t.Fatalf("问题要带选项留在聊天里：%q", rows)
	}
	if b := bodiesOf(e); len(b) < 2 || !strings.Contains(b[1], "现在是自动运行") {
		t.Fatalf("结果没说明没等：%v", b)
	}
	if n := e.llmCalls(); n != 2 {
		t.Fatalf("问完收尾后目标不该再续：请求 %d 次", n)
	}
}

// 谁看得见、谁执行得了：本地私聊（含自检）看得见；本地群聊、线、QQ 私聊、QQ 群看不见；
// 执行时绕过清单直接调，身份不对也不问。
func TestAskOwnerVisibilityAndIdentityMatrix(t *testing.T) {
	t.Parallel()
	conv := "console:" + testChar
	cases := []struct {
		name    string
		aud     audience.Audience
		visible bool
	}{
		{"本地私聊", audience.Console(conv), true},
		{"本地话题", audience.Console(conv + ":topic:t1"), true},
		{"自检话题", audience.Console(audience.SelftestConversation(testChar, "selftest")), true},
		{"本地群聊", audience.Audience{Channel: "console", Conversation: "console:group:g1", Scene: "group", PrivateOK: true, OwnerAsked: true}, false},
		{"本地群聊未指名", audience.Audience{Channel: "console", Conversation: "console:group:g1", Scene: "group", PrivateOK: true}, false},
		{"线", audience.Thread(audience.ThreadConversation(testChar, "th1")), false},
		{"QQ 私聊", audience.Audience{Channel: "qq", Conversation: "qq:private:1", Scene: "private", PrivateOK: true, OwnerAsked: true}, false},
		{"QQ 群", audience.Audience{Channel: "qq", Conversation: "qq:group:1", Scene: "group", OwnerAsked: true}, false},
		{"非主人开的头", audience.Audience{Channel: "console", Conversation: conv, Scene: "private", PrivateOK: true}, false},
	}
	for _, c := range cases {
		if got := askowner.Visible(c.aud); got != c.visible {
			t.Errorf("%s：看得见=%t，想要 %t", c.name, got, c.visible)
		}
	}
	owner := kernel.Scope{CharacterID: testChar, Conversation: conv, Platform: "local", Channel: "console", Scene: "private", ActorKind: "owner", ActorID: "owner"}
	call := func(mut func(*turn.ToolCall)) bool {
		c := turn.ToolCall{Identity: kernel.Invocation{Scope: owner, Kind: kernel.KindForeground, ToolID: "t"}, CharacterID: testChar, Conversation: conv, Audience: audience.Console(conv)}
		mut(&c)
		return askAllowed(c)
	}
	for name, c := range map[string]struct {
		mut  func(*turn.ToolCall)
		want bool
	}{
		"正常":     {func(*turn.ToolCall) {}, true},
		"子任务":    {func(c *turn.ToolCall) { c.Identity.Kind = kernel.KindChild }, false},
		"非主人":    {func(c *turn.ToolCall) { c.Identity.Scope.ActorKind, c.Identity.Scope.ActorID = "human", "p1" }, false},
		"QQ 渠道":  {func(c *turn.ToolCall) { c.Identity.Scope.Platform, c.Identity.Scope.Channel = "qq", "qq" }, false},
		"群":      {func(c *turn.ToolCall) { c.Identity.Scope.Scene = "group"; c.Audience.Scene = "group" }, false},
		"会话对不上":  {func(c *turn.ToolCall) { c.Conversation = conv + ":topic:other" }, false},
		"角色对不上":  {func(c *turn.ToolCall) { c.CharacterID = "别的角色" }, false},
		"听众不是主人": {func(c *turn.ToolCall) { c.Audience.OwnerAsked = false }, false},
	} {
		if got := call(c.mut); got != c.want {
			t.Errorf("执行身份 %s：%t，想要 %t", name, got, c.want)
		}
	}
}

// 重启：问题是问出口时就存进聊天的一条消息，不依赖内存里的卡片。
func TestAskOwnerQuestionPersistsInChat(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("ask1", "ask_owner", askArgs), llmtest.Text("不该有")}})
	key := localKey("console:" + testChar)
	done := sendAsync(d, key, "编的任务")
	waitQuestion(t, d)
	if !hasRow(askRows(t, a, key), "message/character/character/编的问题：先做哪个") {
		t.Fatalf("还在等的时候问题就该在记录里：%q", askRows(t, a, key))
	}
	d.Reader.StopLocal(context.Background(), key)
	localRecv(t, done)
}

// 定期任务的一轮也是无人值守：不挂卡片、不等；自检话题同样（只看宿主自己的状态）。
func TestAskOwnerInLoopDoesNotWait(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := autoDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("ask1", "ask_owner", askArgs), llmtest.Text("问题留给他了")}})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(ctx, key, "/loop 看着点", nil); err != nil {
		t.Fatal(err)
	}
	a.loops.Tick(ctx)
	a.loops.Wait()
	if len(a.asks.Snapshot()) != 0 {
		t.Fatal("定期任务不该挂卡片")
	}
	if b := bodiesOf(e); len(b) != 2 || !strings.Contains(b[1], "现在是自动运行") {
		t.Fatalf("结果没说明没等：%v", b)
	}
	if !a.askUnattended(audience.SelftestConversation(testChar, "selftest")) || a.askUnattended(key.Conversation) {
		t.Fatal("自检话题算无人值守，普通私聊不算")
	}
}
