package app

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/goal"
	"sumika-native/internal/llm/llmtest"
)

func owner(rows []string, text string) bool { return hasRow(rows, "message/owner/owner/"+text) }

// 自动续轮正在做事：主人的话当插话塞进这一轮（回执就是这一轮的编号），不再得到"正在生成"。
func TestSubmitLocalSteersIntoAutoTurn(t *testing.T) {
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
	waitFor(t, "续轮开始", func() bool { return e.llmCalls() == 3 && a.hub.ActiveRunID(testChar, key.Conversation) != "" })
	run := a.hub.ActiveRunID(testChar, key.Conversation)
	rec, err := d.Reader.SubmitLocal(ctx, key, api.LocalInputSubmission{Text: "顺便加一条", Mode: "start", ClientKey: "k-steer"})
	if err != nil {
		t.Fatalf("自动轮进行时普通消息不该被挡：%v", err)
	}
	if rec.RunID != run {
		t.Fatalf("应插进正在跑的那一轮 %s，回执是 %s", run, rec.RunID)
	}
	if !owner(slashRows(t, a, key.Conversation), "顺便加一条") {
		t.Fatalf("主人的话没存下：%q", slashRows(t, a, key.Conversation))
	}
	if g, ok := a.goals.Get(key.Conversation); !ok || g.Status != goal.StatusActive {
		t.Fatalf("插话不该动目标：%+v", g)
	}
}

// 两轮之间（裁判还在判断）发消息：排队，下一轮最先处理；被顶替的裁判不暂停目标，新一轮结束后重新判。
func TestSubmitLocalQueuesBetweenAutoTurns(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := autoDesktop(t, e, llmtest.Script{
		Replies: []llmtest.Reply{llmtest.Text("第一轮进展"), llmtest.Text(judgeNo), llmtest.Text("收到你的话"), llmtest.Text(judgeYes)},
		Hold: func(nth int) int {
			if nth == 2 { // 裁判停在半路
				return 0
			}
			return -1
		},
	})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(ctx, key, "/goal 做事", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "裁判开始判断", func() bool {
		return e.llmCalls() == 2 && !a.hub.Running(testChar, key.Conversation) && a.auto.has(key.Conversation)
	})
	rec, err := d.Reader.SubmitLocal(ctx, key, api.LocalInputSubmission{Text: "先听我说", Mode: "start", ClientKey: "k-queue"})
	if err != nil {
		t.Fatalf("两轮之间的消息不该被挡：%v", err)
	}
	if rec.RunID == "" {
		t.Fatal("排队也要有自己的这一轮")
	}
	waitFor(t, "目标达成", func() bool { return hasRow(slashRows(t, a, key.Conversation), "目标达成：文件已读回") })
	rows := slashRows(t, a, key.Conversation)
	if hasRow(rows, "目标已暂停") {
		t.Fatalf("被顶替的裁判不该把目标暂停：%q", rows)
	}
	// 她第三次请求收到的就是主人的话；它排在续轮提示之前（续轮那句被让路，没发出）
	e.mu.Lock()
	third := e.bodies[2]
	e.mu.Unlock()
	if !strings.Contains(third, "先听我说") {
		t.Fatalf("排队的话没在下一轮最先处理：%s", third)
	}
	if hasRow(rows, "message/owner/goal/") {
		t.Fatalf("不该再发续轮提示：%q", rows)
	}
}

// 暂停之后普通消息照常开一轮，不走插话。
func TestSubmitLocalAfterGoalPauseStartsNormally(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := autoDesktop(t, e, llmtest.Script{
		Replies: []llmtest.Reply{llmtest.Text("第一轮进展"), llmtest.Text(judgeNo), llmtest.Text("续轮前半", "续轮后半"), llmtest.Text("普通回复")},
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
	waitFor(t, "续轮开始", func() bool { return e.llmCalls() == 3 && a.hub.ActiveRunID(testChar, key.Conversation) != "" })
	if err := d.Reader.SendLocal(ctx, key, "/goal pause", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "自动轮收尾", func() bool { return !a.hub.Running(testChar, key.Conversation) && !a.auto.has(key.Conversation) })
	rec, err := d.Reader.SubmitLocal(ctx, key, api.LocalInputSubmission{Text: "普通话", Mode: "start", ClientKey: "k-plain"})
	if err != nil || rec.RunID == "" {
		t.Fatalf("暂停后普通消息应照常发出：%v %+v", err, rec)
	}
	waitFor(t, "普通回复", func() bool { return hasRow(slashRows(t, a, key.Conversation), "普通回复") })
}
