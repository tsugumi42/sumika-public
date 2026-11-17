package channel

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// 闸门要从出口和回合那里听到的两样（gate.go 的 TurnDone、PartEnded 各接一头）：
// 回合定了以后告诉她说了没有（OnTurnDone），一段发完以后告诉通道门结果（OutletConfig.OnPart）。号、话都是编的。

type turnDone struct {
	Character string
	Conv      types.Conversation
	Spoke     bool
	Count     int
}

type tdSpy struct {
	mu  sync.Mutex
	got []turnDone
}

func (s *tdSpy) hook(character string, conv types.Conversation, spoke bool, count int, _ time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, turnDone{character, conv, spoke, count})
}

func (s *tdSpy) list() []turnDone {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]turnDone(nil), s.got...)
}

func TestOnTurnDone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		mode    string
		outcome string
		said    []turn.Said
		guard   func(*fakeGuard)
		want    []turnDone // nil 是不该调
	}{
		{"说了话，写进了计划", ModeSpeak, turn.OutcomeDone, []turn.Said{{Step: 0, Text: "在的"}}, nil, []turnDone{{"ca", convGroup, true, 1}}},
		{"试运行想说的也算", ModeDryRun, turn.OutcomeDone, []turn.Said{{Step: 0, Text: "在的"}}, nil, []turnDone{{"ca", convGroup, true, 1}}},
		{"看完没说话", ModeSpeak, turn.OutcomeDone, nil, nil, []turnDone{{"ca", convGroup, false, 0}}},
		{"想说但被守卫拦下：说了话，不计开口", ModeSpeak, turn.OutcomeDone, []turn.Said{{Step: 0, Text: "考试在周五"}}, func(g *fakeGuard) { g.blockOn = []string{"考试在周五"} }, []turnDone{{"ca", convGroup, true, 0}}},
		{"字数保险丝拦下：说了话，不计开口", ModeSpeak, turn.OutcomeDone, []turn.Said{{Step: 0, Text: repeatRune("字", 201)}}, nil, []turnDone{{"ca", convGroup, true, 0}}},
		{"被打断不调", ModeSpeak, turn.OutcomeStopped, []turn.Said{{Step: 0, Text: "说到一半"}}, nil, nil},
		{"出错不调", ModeSpeak, turn.OutcomeError, nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spy := &tdSpy{}
			x := newRO(t, func(c *ResponderConfig) { c.OnTurnDone = spy.hook })
			x.setMode(tc.mode)
			if tc.guard != nil {
				x.guard.set(tc.guard)
			}
			x.start(convGroup, x.say(convGroup, whoFriend, "在吗", true))
			if tc.outcome == turn.OutcomeError {
				x.k.finishErr("ca", convGroup.String(), turn.OutcomeError, fmt.Errorf("上下文超限"))
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := x.r.Idle(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				x.settleAndWait(convGroup, tc.outcome, tc.said...)
			}
			got := spy.list()
			if len(got) != len(tc.want) {
				t.Fatalf("调了 %d 次：%+v，想要 %+v", len(got), got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("第 %d 次：%+v，想要 %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func repeatRune(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// 钩子 panic 了：只记日志，屏障照样放开，后面的回合照常
func TestOnTurnDonePanicDoesNotStickBarrier(t *testing.T) {
	t.Parallel()
	x := newRO(t, func(c *ResponderConfig) {
		c.OnTurnDone = func(string, types.Conversation, bool, int, time.Time) { panic("钩子坏了") }
	})
	x.start(convGroup, x.say(convGroup, whoFriend, "在吗", true))
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "在的"})
	x.waitSent(1)
	x.start(convGroup, x.say(convGroup, whoFriend, "再问", true))
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "还在"})
	x.waitSent(2)
	if !strings.Contains(x.logText(), "回合定了的通知") {
		t.Fatalf("该记一行日志：%s", x.logText())
	}
}

// ---------- 出口：一段发完告诉通道门 ----------

type partSpy struct {
	mu  sync.Mutex
	got []PartResult
}

func (s *partSpy) hook(r PartResult) { s.mu.Lock(); s.got = append(s.got, r); s.mu.Unlock() }
func (s *partSpy) list() []PartResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PartResult(nil), s.got...)
}

func TestOutletOnPart(t *testing.T) {
	t.Parallel()
	// 确认：调一次，Confirmed
	spy := &partSpy{}
	h := newOH(t, types.Caps{}, func(c *OutletConfig) { c.OnPart = spy.hook })
	h.run(nil)
	id := h.say("一条")
	h.waitPlan(id, store.PlanDone)
	eventually(t, "确认通知到达", func() bool { return len(spy.list()) >= 1 }) // 计划落库和通知不在同一步，机器忙时会晚一点
	if got := spy.list(); len(got) != 1 || !got[0].Confirmed || got[0].PlanID != id || got[0].Character != "ca" || got[0].Index != 0 {
		t.Fatalf("确认该调一次：%+v", got)
	}

	// 明确失败，用完 3 次：只在用完那一刻调一次，Confirmed 为假；重试中间不调
	spy2 := &partSpy{}
	h2 := newOH(t, types.Caps{}, func(c *OutletConfig) { c.OnPart = spy2.hook })
	h2.run(nil)
	h2.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
		return fake.Failed(c.Part.ID, "拒绝", c.At), nil
	})
	id2 := h2.say("一条")
	h2.waitSent(1)
	h2.waitPart(id2, 0, store.PartRetrying)
	if n := len(spy2.list()); n != 0 {
		t.Fatalf("重试中间不该调：%d", n)
	}
	h2.clk.WaitTimer(t, time.Second)
	h2.clk.Advance(time.Second)
	h2.waitSent(2)
	h2.waitPart(id2, 0, store.PartRetrying)
	h2.clk.WaitTimer(t, time.Second)
	h2.clk.Advance(time.Second)
	h2.waitPlan(id2, store.PlanFailed)
	if got := spy2.list(); len(got) != 1 || got[0].Confirmed {
		t.Fatalf("用完重试该调一次、Confirmed 为假：%+v", got)
	}

	// 适配器说不支持：明确失败，调一次
	spy3 := &partSpy{}
	h3 := newOH(t, types.Caps{}, func(c *OutletConfig) { c.OnPart = spy3.hook })
	h3.run(nil)
	h3.ad.OnSend(func(context.Context, fake.SendCall) (types.Receipt, error) {
		return types.Receipt{}, types.ErrUnsupported
	})
	id3 := h3.say("一条")
	h3.waitPlan(id3, store.PlanFailed)
	if got := spy3.list(); len(got) != 1 || got[0].Confirmed {
		t.Fatalf("不支持是明确失败：%+v", got)
	}

	// 结果未知：不调（不算失败也不算确认）；后面没发的段中止，也不调
	spy4 := &partSpy{}
	h4 := newOH(t, types.Caps{}, func(c *OutletConfig) { c.OnPart = spy4.hook })
	h4.run(nil)
	h4.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
		return fake.Unknown(c.Part.ID, "异步", c.At), nil
	})
	id4 := h4.say("一条", "两条")
	h4.waitPlan(id4, store.PlanUnknown)
	if got := spy4.list(); len(got) != 0 {
		t.Fatalf("结果未知、已中止都不该调：%+v", got)
	}

	// 钩子 panic：记日志，发送照常
	h5 := newOH(t, types.Caps{}, func(c *OutletConfig) { c.OnPart = func(PartResult) { panic("坏了") } })
	h5.run(nil)
	id5 := h5.say("一条")
	h5.waitPlan(id5, store.PlanDone)
	eventually(t, "日志里有 OnPart 的意外", func() bool { return strings.Contains(h5.logText(), "OnPart") }, func() string { return h5.logText() })
}
