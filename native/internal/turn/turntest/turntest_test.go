package turntest_test

// 夹具自己的测试：每一样给出去的东西各走一遍（主窗口、渠道回合加交回、停在半句上停下、请求卡住时插话、收尾时还有请求挂着）

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/turn"
	"sumika-native/internal/turn/turntest"
)

const group = "qq:group:test-1"

var friend = audience.Audience{Channel: "qq", Scene: "group", PersonIDs: []string{"qq:1"}}

func recv[T any](t *testing.T, c <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(30 * time.Second):
		t.Fatalf("30 秒还没等到：%s", what)
		var zero T
		return zero
	}
}

// settled 是一个只听"回合定了"的插件
type settled chan turn.Settled

func (s settled) ID() string { return "settled" }
func (s settled) Setup(r *turn.Registry) (func(), error) {
	r.OnSettled(func(_ context.Context, e turn.Settled) { s <- e })
	return nil, nil
}

// 主窗口：一句话一次请求，请求的最后一条是这句话，回的是默认的那句
func TestSend(t *testing.T) {
	k := turntest.New(t, turntest.Options{})
	r := k.Send("今天天气怎么样")
	ms := k.Messages(0)
	if k.Requests() != 1 || ms[len(ms)-1].Role != "user" || !strings.Contains(ms[len(ms)-1].Text, "今天天气怎么样") || r.Reply != "在呢～" {
		t.Fatalf("请求 %d 次，最后一条 %+v，回的 %q", k.Requests(), ms[len(ms)-1], r.Reply)
	}
	if b := k.Bodies(); len(b) != 1 || !strings.Contains(b[0], `"model":"test/plain"`) {
		t.Fatalf("请求体：%v", b)
	}
}

// 渠道回合：她说的照 Replies 回；交回以后，下一次请求的历史里她那句只有一份（交回的那一行替换了实例里的）
func TestSayAndCommit(t *testing.T) {
	k := turntest.New(t, turntest.Options{Replies: []llmtest.Reply{llmtest.Text("第一句回话"), llmtest.Text("第二句回话")}})
	r := k.Say(group, friend, "public", "human", "qq:1", "大家好")
	if len(r.Said) != 1 || r.Said[0].Text != "第一句回话" {
		t.Fatalf("说的：%+v", r.Said)
	}
	lines := k.Commit(group, r)
	if len(lines) != 1 || lines[0].Seq == 0 {
		t.Fatalf("交回：%+v", lines)
	}
	var seen int
	k.StoreFor(group).Events.QueryRow(`SELECT count(*) FROM observations WHERE event_seq = ? AND character_id = ?`, lines[0].Seq, turntest.CharacterID).Scan(&seen)
	if seen != 1 {
		t.Fatal("存进库的那一行她看得到")
	}
	k.Say(group, friend, "public", "human", "qq:1", "再说一句")
	n := 0
	for _, m := range k.Messages(1) {
		n += strings.Count(m.Text, "第一句回话")
	}
	if n != 1 {
		t.Fatalf("交回以后历史里她那句该只有一份：%d", n)
	}
}

// 停在半句上：第一次请求吐完一段就停住（她收到了前半句、后半句一直没来：Hold 真的传给了假网关），这时停得到这个会话，
// 结局是 stopped，"回合定了"带着同一个回合
func TestHoldThenStop(t *testing.T) {
	deltas := make(chan string, 4)
	k := turntest.New(t, turntest.Options{
		Reply: llmtest.Text("先说半句", "后半句"),
		Hold: func(nth int) int {
			if nth == 1 {
				return 1
			}
			return -1
		},
		Mutate: func(c *turn.Config) { c.OnDelta = func(s string) { deltas <- s } },
	})
	s := make(settled, 4)
	if err := k.Hub.Use(s); err != nil {
		t.Fatal(err)
	}
	seq := k.Append(group, "human", "qq:1", "说点什么")
	o := k.Hub.Respond(turn.Input{CharacterID: turntest.CharacterID, Conversation: group, Seqs: []int64{seq}, Text: "说点什么", Audience: friend, Addressed: true, Scope: "public"})
	if d := recv(t, deltas, "前半句"); d != "先说半句" {
		t.Fatalf("第一段：%q", d)
	}
	if !k.Hub.StopIn(turntest.CharacterID, group) {
		t.Fatal("停在半句上时应该停得到")
	}
	r := k.Wait(o)
	if !r.Stopped || len(deltas) != 0 {
		t.Fatalf("结局应该是 stopped、后半句没来：%+v，还有 %d 段", r, len(deltas))
	}
	if e := recv(t, s, "回合定了"); e.Outcome != turn.OutcomeStopped || e.TurnID != r.TurnID {
		t.Fatalf("回合定了：%+v", e)
	}
}

// 请求卡在 Before 里时插话：插话收下，放行以后下一次请求里有它
func TestBeforeBlocksThenSteer(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	k := turntest.New(t, turntest.Options{
		Before: func(ctx context.Context, n int, _ string) {
			if n != 1 {
				return
			}
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
		},
	})
	seq := k.Append(group, "human", "qq:1", "第一句")
	o := k.Hub.Respond(turn.Input{CharacterID: turntest.CharacterID, Conversation: group, Seqs: []int64{seq}, Text: "第一句", Audience: friend, Addressed: true, Scope: "public"})
	recv(t, entered, "第一次请求")
	if k.Requests() != 0 {
		t.Fatal("卡在 Before 里的还没回，不算")
	}
	if _, ok := k.Hub.SteerIn(turntest.CharacterID, group, "顺便问一句", []int64{k.Append(group, "human", "qq:1", "顺便问一句")}); !ok {
		t.Fatal("在跑的时候插话应该收下")
	}
	close(release)
	k.Wait(o)
	if k.Requests() != 2 || strings.Count(k.Bodies()[1], "顺便问一句") != 1 {
		t.Fatalf("放行以后插话在下一次请求里：%d 次", k.Requests())
	}
}

// 收尾：请求还挂在假网关上（Before 只等 ctx），测试结束时先取消内核，挂着的请求跟着结束，不卡住
func TestCleanupWithRequestInFlight(t *testing.T) {
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		t.Run("挂着", func(t *testing.T) {
			entered := make(chan struct{}, 1)
			k := turntest.New(t, turntest.Options{Before: func(ctx context.Context, _ int, _ string) {
				entered <- struct{}{}
				<-ctx.Done()
			}})
			k.Hub.Send(turntest.CharacterID, "这一句回不完")
			recv(t, entered, "请求")
		})
	}()
	recv(t, done, "收尾")
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("收尾用了 %v", d)
	}
}
