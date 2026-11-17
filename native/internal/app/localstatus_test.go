package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
)

// 状态接口：目标行、定期任务、自动轮；订阅在命令之后收到新快照。
func TestLocalStatusReadAndWatch(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := autoDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{llmtest.Text("好"), llmtest.Text(judgeNo), llmtest.Text("续轮前半", "续轮后半")}, Hold: func(nth int) int {
		if nth == 3 {
			return 1
		}
		return -1
	}})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	sub, err := d.Reader.WatchLocalStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	next := func(ok func([]api.LocalStatus) bool) []api.LocalStatus {
		t.Helper()
		deadline := time.After(90 * time.Second)
		for {
			select {
			case s := <-sub.Updates():
				if ok(s.Value) {
					return s.Value
				}
			case <-deadline:
				t.Fatal("状态订阅没等到想要的快照")
			}
		}
	}
	next(func(v []api.LocalStatus) bool { return len(v) == 0 })
	if err := d.Reader.SendLocal(ctx, key, "/loop 30m 看看", nil); err != nil {
		t.Fatal(err)
	}
	got := next(func(v []api.LocalStatus) bool { return len(v) == 1 && len(v[0].Loops) == 1 })
	if !strings.Contains(got[0].Loops[0], "#1 每 30 分钟") {
		t.Fatalf("%+v", got)
	}
	if err := d.Reader.SendLocal(ctx, key, "/goal 做事", nil); err != nil {
		t.Fatal(err)
	}
	// 第 3 次请求（续轮）停在半句上：自动轮在跑，目标进行中
	got = next(func(v []api.LocalStatus) bool { return len(v) == 1 && v[0].AutoRunning && v[0].GoalState == "active" })
	if !strings.HasPrefix(got[0].GoalLine, "目标：做事") {
		t.Fatalf("%+v", got)
	}
	if err := d.Reader.StopLocal(ctx, key); err != nil {
		t.Fatal(err)
	}
	got = next(func(v []api.LocalStatus) bool { return len(v) == 1 && !v[0].AutoRunning && v[0].GoalState == "paused" })
	one, err := d.Reader.ReadLocalStatus(ctx, key)
	if err != nil || one.GoalState != "paused" || len(one.Loops) != 1 || one.Key != key {
		t.Fatalf("%+v %v", one, err)
	}
	// 伪造的会话读不了
	if _, err := d.Reader.ReadLocalStatus(ctx, api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "private", Conversation: "qq:private:1:2"}); err == nil {
		t.Fatal("QQ 会话不该能读本地状态")
	}
	_ = a
}

// 状态里带结构化的定期任务和目标详情（界面的定期任务页、目标展开用），暂停、继续、删除走同一套命令逻辑。
func TestLocalStatusStructuredAndLoopControl(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	quiet := func() llmtest.Reply { return llmtest.Text("[无事]") }
	_, d := autoDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{quiet(), quiet(), quiet(), quiet(), quiet(), quiet()}})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	sub, err := d.Reader.WatchLocalStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	next := func(ok func([]api.LocalStatus) bool) []api.LocalStatus {
		t.Helper()
		deadline := time.After(90 * time.Second)
		for {
			select {
			case s := <-sub.Updates():
				if ok(s.Value) {
					return s.Value
				}
			case <-deadline:
				t.Fatal("状态订阅没等到想要的快照")
			}
		}
	}
	if err := d.Reader.SendLocal(ctx, key, "/loop 30m 看看天气", nil); err != nil {
		t.Fatal(err)
	}
	got := next(func(v []api.LocalStatus) bool { return len(v) == 1 && len(v[0].LoopTasks) == 1 })
	lt := got[0].LoopTasks[0]
	if lt.ID != 1 || lt.Prompt != "看看天气" || lt.IntervalSec != 1800 || lt.Status != "running" || lt.Expires == 0 {
		t.Fatalf("定期任务的结构化内容不对：%+v", lt)
	}
	if len(got[0].Loops) != len(got[0].LoopTasks) {
		t.Fatalf("文字行和结构化行该一一对应：%+v", got[0])
	}
	ctl := api.LoopControl(&d.Reader)
	// 暂停：状态变 paused，会话里多一行系统行
	if err := ctl.PauseLoop(ctx, key, 1); err != nil {
		t.Fatal(err)
	}
	got = next(func(v []api.LocalStatus) bool {
		return len(v) == 1 && len(v[0].LoopTasks) == 1 && v[0].LoopTasks[0].Status == "paused"
	})
	if err := ctl.ResumeLoop(ctx, key, 1); err != nil {
		t.Fatal(err)
	}
	next(func(v []api.LocalStatus) bool {
		return len(v) == 1 && len(v[0].LoopTasks) == 1 && v[0].LoopTasks[0].Status == "running"
	})
	// 删除：任务不再出现在列表里
	if err := ctl.RemoveLoop(ctx, key, 1); err != nil {
		t.Fatal(err)
	}
	next(func(v []api.LocalStatus) bool { return len(v) == 0 || len(v[0].LoopTasks) == 0 })
	// 不合格的请求都报错：编号 0（命令里表示“全部”，这里不允许）、不存在的编号、非本地私聊的会话
	if err := ctl.PauseLoop(ctx, key, 0); err == nil {
		t.Fatal("编号 0 不该能暂停全部")
	}
	if err := ctl.RemoveLoop(ctx, key, 99); err == nil {
		t.Fatal("不存在的任务不该删除成功")
	}
	qq := api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "private", Conversation: "qq:private:1:2"}
	if err := ctl.RemoveLoop(ctx, qq, 1); err == nil {
		t.Fatal("QQ 会话不该能动本地的定期任务")
	}
	// 目标详情
	if err := d.Reader.SendLocal(ctx, key, "/goal 写完说明", nil); err != nil {
		t.Fatal(err)
	}
	got = next(func(v []api.LocalStatus) bool { return len(v) == 1 && v[0].Goal != nil })
	if g := got[0].Goal; g.Text != "写完说明" || g.StartedAt == 0 || g.MaxRounds == 0 {
		t.Fatalf("目标详情不对：%+v", g)
	}
}
