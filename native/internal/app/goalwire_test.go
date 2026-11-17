package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/audience"
	"sumika-native/internal/goal"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/turn"
)

// autoDesktop 起一个本地私聊桌面：假网关按 script 回，后台模型就是同一个假模型（裁判的请求也排在同一串里）。
func autoDesktop(t *testing.T, e *testEnv, script llmtest.Script) (*App, *Desktop) {
	t.Helper()
	script.Models = []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}
	srv := httptest.NewServer(llmtest.Handler(script, &e.bodies, &e.mu))
	t.Cleanup(srv.Close)
	e.llm = srv
	e.configure("tools:\n  workdir: '" + filepath.ToSlash(t.TempDir()) + "'\ndefaults:\n  backgroundModel: " + testModel + "\n")
	a := e.open()
	d := a.Desktop()
	t.Cleanup(func() { d.Close(); a.Close() })
	return a, d
}

const (
	judgeNo  = `{"achieved": false, "blocked": false, "reason": "还差文件"}`
	judgeYes = `{"achieved": true, "blocked": false, "reason": "文件已读回"}`
)

func hasRow(rows []string, sub string) bool {
	for _, r := range rows {
		if strings.Contains(r, sub) {
			return true
		}
	}
	return false
}

// 本地私聊 /goal 做 X：自动续轮两次后裁判判达成，会话里有"目标达成"。
func TestGoalAutoContinuesUntilAchieved(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := autoDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{
		llmtest.Text("第一轮进展"), llmtest.Text(judgeNo),
		llmtest.Text("第二轮进展"), llmtest.Text(judgeNo),
		llmtest.Text("第三轮进展"), llmtest.Text(judgeYes),
	}})
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(context.Background(), key, "/goal 建 hello.txt 并读回", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "目标达成", func() bool { return hasRow(slashRows(t, a, key.Conversation), "目标达成：文件已读回") })
	rows := slashRows(t, a, key.Conversation)
	want := []string{
		"message/owner/owner/建 hello.txt 并读回",
		"notice/system/system:goal/目标已设定",
		"message/character/character/第一轮进展",
		"message/owner/goal/目标还没达成（理由：还差文件）",
		"message/character/character/第二轮进展",
		"message/owner/goal/目标还没达成（理由：还差文件）",
		"message/character/character/第三轮进展",
		"notice/system/system:goal/目标达成：文件已读回",
	}
	if len(rows) != len(want) {
		t.Fatalf("会话记录：\n%s", strings.Join(rows, "\n"))
	}
	for i, w := range want {
		if !strings.Contains(rows[i], w) {
			t.Fatalf("第 %d 行 %q 不含 %q\n全部：\n%s", i, rows[i], w, strings.Join(rows, "\n"))
		}
	}
	if _, ok := a.goals.Get(key.Conversation); ok {
		t.Fatal("达成后目标应清除")
	}
	if a.auto.has(key.Conversation) {
		t.Fatal("达成后不该还登记着自动活动")
	}
	// 续轮那句主人身份的消息：来源是 goal，不是主人说的话
	page, err := d.Reader.ReadMessages(context.Background(), api.ConversationRequest{Key: key, Limit: 20}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range page.Value.Items {
		if m.Origin == "goal" && m.DisplayName == "主人" {
			t.Fatalf("续轮那句不该显示成主人说的：%+v", m)
		}
	}
	// 裁判调用走统一的模型调用记录，用途是 goal_judge（用量统计看得到）
	var judged int
	if err := a.set.TraceStore().Trace.QueryRow(`SELECT count(*) FROM model_runs WHERE stage = 'goal_judge' AND conversation = ?`, key.Conversation).Scan(&judged); err != nil || judged != 3 {
		t.Fatalf("trace 里 goal_judge 的行数：%d %v", judged, err)
	}
	// 共 3 轮 + 3 次裁判 = 6 次请求，且裁判请求不进她的历史
	if e.llmCalls() != 6 {
		t.Fatalf("模型请求次数：%d", e.llmCalls())
	}
}

// 停止按钮暂停目标（续轮在进行时）。
func TestGoalStopPausesAndClear(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := autoDesktop(t, e, llmtest.Script{
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
	// 第 3 次请求（续轮）停在半句上：自动轮在跑
	waitFor(t, "续轮开始", func() bool { return e.llmCalls() == 3 && a.hub.Running(testChar, key.Conversation) })
	if err := d.Reader.SendLocal(ctx, key, "普通话", nil); err != api.ErrLocalChatBusy {
		t.Fatalf("自动轮进行时普通消息应得到忙：%v", err)
	}
	if err := d.Reader.StopLocal(ctx, key); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "目标暂停", func() bool {
		g, ok := a.goals.Get(key.Conversation)
		return ok && g.Status == goal.StatusPaused
	})
	waitFor(t, "自动轮收尾", func() bool { return !a.hub.Running(testChar, key.Conversation) && !a.auto.has(key.Conversation) })
	if !hasRow(slashRows(t, a, key.Conversation), "目标已暂停：主人停止") {
		t.Fatalf("没有暂停的系统行：%q", slashRows(t, a, key.Conversation))
	}
	calls := e.llmCalls()
	// 暂停之后不再自动发
	time.Sleep(200 * time.Millisecond)
	if e.llmCalls() != calls {
		t.Fatal("暂停后又发了请求")
	}
	// /goal clear 清除
	if err := d.Reader.SendLocal(ctx, key, "/goal clear", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.goals.Get(key.Conversation); ok {
		t.Fatal("clear 之后目标还在")
	}
	if !hasRow(slashRows(t, a, key.Conversation), "目标已清除") {
		t.Fatal("没有清除的系统行")
	}
}

// 设了目标之后：状态、edit、pause、resume、重启后变暂停。
func TestGoalStatusEditPauseResume(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := autoDesktop(t, e, llmtest.Script{Reply: llmtest.Text(judgeNo), Replies: []llmtest.Reply{llmtest.Text("好"), llmtest.Text(judgeNo), llmtest.Text("接着"), llmtest.Text(judgeNo)}}) // 不让裁判判达成：这条测试看的是暂停和改写
	ctx := context.Background()
	key := localKey("console:" + testChar)
	send := func(text string) {
		t.Helper()
		if err := d.Reader.SendLocal(ctx, key, text, nil); err != nil {
			t.Fatal(err)
		}
	}
	send("/goal")
	if !hasRow(slashRows(t, a, key.Conversation), "这个会话没有目标") {
		t.Fatal("无目标的状态回复")
	}
	// edit 没有目标就当设新目标（并发送）
	send("/goal edit 写报告")
	waitFor(t, "第一轮后目标仍在或已推进", func() bool { return e.llmCalls() >= 2 })
	a.autoStop(key.Conversation, "主人暂停") // 先停住，免得下面的断言和续轮抢
	waitFor(t, "暂停", func() bool { g, ok := a.goals.Get(key.Conversation); return ok && g.Status == goal.StatusPaused })
	waitFor(t, "安静", func() bool { return !a.hub.Running(testChar, key.Conversation) })
	g0, _ := a.goals.Get(key.Conversation)
	send("/goal edit 写报告并检查")
	g1, _ := a.goals.Get(key.Conversation)
	if g1.Text != "写报告并检查" || g1.Rounds != g0.Rounds || g1.Status != goal.StatusPaused {
		t.Fatalf("edit 应保留轮数和状态：%+v → %+v", g0, g1)
	}
	if !hasRow(slashRows(t, a, key.Conversation), "目标改为：写报告并检查") {
		t.Fatal("没有改目标的系统行")
	}
	send("/goal edit   ")
	send("/goal")
	rows := slashRows(t, a, key.Conversation)
	if !hasRow(rows, "要写新目标") || !hasRow(rows, "目标：写报告并检查") {
		t.Fatalf("状态行：%q", rows)
	}
	// 重启：进行中的目标读回来是暂停
	if _, err := a.goals.Resume(key.Conversation); err != nil {
		t.Fatal(err)
	}
	m2, err := goal.NewManager(goal.NewFileStore(a.paths.GoalsDir()), goalJudge{app: a}, goal.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if g, _ := m2.Get(key.Conversation); g.Status != goal.StatusPaused {
		t.Fatalf("重启后应暂停：%+v", g)
	}
}

// 非本地私聊主人的回合（QQ、本地群、别的身份）结束时，即使会话号碰巧有目标也不裁判、不续轮。
func TestGoalSettledIgnoresOtherScenes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, _ := autoDesktop(t, e, llmtest.Script{Reply: llmtest.Text(judgeYes)})
	owner := kernel.Scope{CharacterID: testChar, Platform: "local", Channel: "console", Scene: "private", ActorKind: "owner", ActorID: "owner"}
	cases := map[string]turn.Settled{}
	qq := owner
	qq.Platform, qq.Channel = "qq", "qq"
	cases["QQ 私聊"] = turn.Settled{Identity: kernel.Invocation{Scope: qq}, Conversation: "console:" + testChar, Outcome: turn.OutcomeDone}
	grp := owner
	grp.Scene = "group"
	cases["本地群"] = turn.Settled{Identity: kernel.Invocation{Scope: grp}, Conversation: "console:" + testChar, Outcome: turn.OutcomeDone}
	other := owner
	other.ActorKind, other.ActorID = "human", "p1"
	cases["非主人"] = turn.Settled{Identity: kernel.Invocation{Scope: other}, Conversation: "console:" + testChar, Outcome: turn.OutcomeDone}
	for name, s := range cases {
		s.Audience = audience.Console(s.Conversation)
		conv := s.Conversation
		if _, err := a.goals.Set(conv, "目标"); err != nil {
			t.Fatal(err)
		}
		before := e.llmCalls()
		a.goalSettled(s)
		a.autoWG.Wait()
		if e.llmCalls() != before {
			t.Fatalf("%s：不是本地私聊主人的回合却请求了裁判", name)
		}
		if g, _ := a.goals.Get(conv); g.Rounds != 0 || g.Status != goal.StatusActive {
			t.Fatalf("%s：目标被动了：%+v", name, g)
		}
		a.goals.Clear(conv)
	}
}
