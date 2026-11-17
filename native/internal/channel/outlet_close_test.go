package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// 关闭和重启的测试（规格 10.6，第十六节 B"关闭""重启"，第十八节 ④）。号、群、文字都是编的。

// closeAsync 在另一个 goroutine 里关出口，done 在 Close 返回时关闭
func (h *oh) closeAsync() <-chan struct{} {
	done := make(chan struct{})
	go func() { h.out.Close(); close(done) }()
	return done
}

func isDone(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// 关闭：排着没开始的计划不发（标已中止）；正在发送的那一段等它的回执，5 秒内回来就照常记；
// 之后 Send 返回 ErrClosed，回声认领什么也不做
func TestCloseAbortsQueuedAndWaitsForInFlight(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	d := h.hold()
	first := h.say("甲", "乙")
	queued := h.say("排在后面的")
	d.next(t)

	done := h.closeAsync()
	h.clk.WaitPending(t, 2) // 在发的那一段的超时 + 关闭等回执的 5 秒
	h.clk.Advance(4999 * time.Millisecond)
	if isDone(done) {
		t.Fatal("还差 1 毫秒到 5 秒，Close 不该返回：它在等发送中的回执")
	}
	d.release(confirmedWith("1")) // 回执在 5 秒内回来
	<-done

	if got := h.partStates(first); fmt.Sprint(got) != fmt.Sprint([]string{store.PartConfirmed, store.PartAborted}) {
		t.Fatalf("在发的那一段记确认，后面的中止：%v", got)
	}
	if st := h.plan(first).State; st != store.PlanPartial {
		t.Fatalf("%s", st)
	}
	if got := h.partStates(queued); fmt.Sprint(got) != fmt.Sprint([]string{store.PartAborted}) || h.plan(queued).State != store.PlanAborted {
		t.Fatalf("排着的整条中止：%v", got)
	}
	if n := len(h.ad.Sent()); n != 1 {
		t.Fatalf("关闭以后不再 Send：%d", n)
	}
}

// 等不到回执（5 秒到了）：这一段标结果未知；回执在关闭以后才回来，什么也不写
func TestCloseGivesUpAfterFiveSecondsAndLateReceiptWritesNothing(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	late, returned := make(chan struct{}), make(chan struct{})
	h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
		defer close(returned)
		<-late // 不看 ctx
		return fake.Confirmed(c.Part.ID, "9999", c.At), nil
	})
	id := h.say("甲", "乙")
	h.waitSent(1)

	done := h.closeAsync()
	h.clk.WaitPending(t, 2)
	h.clk.Advance(5 * time.Second)
	<-done
	if got := h.partStates(id); fmt.Sprint(got) != fmt.Sprint([]string{store.PartUnknown, store.PartAborted}) || h.plan(id).State != store.PlanUnknown {
		t.Fatalf("等不到回执的标结果未知，后面的中止：%v %s", got, h.plan(id).State)
	}

	// 之后关库（真实使用的顺序）前，迟到的回执来了
	close(late)
	<-returned
	time.Sleep(20 * time.Millisecond)
	if got := h.partStates(id); fmt.Sprint(got) != fmt.Sprint([]string{store.PartUnknown, store.PartAborted}) {
		t.Fatalf("关闭以后到的回执不该写库：%v", got)
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM channel_msgs`); n != 0 {
		t.Fatalf("关闭以后到的回执不该映射：%d", n)
	}
}

// 停顿和重试间隔里关闭：等的人立刻醒，不用拨钟 Close 就能返回；没发的段中止
func TestCloseWakesPauseAndRetryWaits(t *testing.T) {
	t.Parallel()
	t.Run("停顿", func(t *testing.T) {
		t.Parallel()
		h := newOH(t, types.Caps{})
		h.run(nil)
		id := h.say("甲", "乙是比较长的一句")
		h.waitPart(id, 0, store.PartConfirmed)
		h.clk.WaitTimer(t, 320*time.Millisecond)
		select {
		case <-h.closeAsync():
		case <-time.After(5 * time.Second):
			t.Fatal("停顿里关闭，Close 该立刻返回")
		}
		if got := h.partStates(id); fmt.Sprint(got) != fmt.Sprint([]string{store.PartConfirmed, store.PartAborted}) {
			t.Fatalf("%v", got)
		}
		if n := len(h.ad.Sent()); n != 1 {
			t.Fatalf("Send %d 次", n)
		}
	})
	t.Run("重试间隔", func(t *testing.T) {
		t.Parallel()
		h := newOH(t, types.Caps{})
		h.run(nil)
		h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
			return fake.Failed(c.Part.ID, "拒绝", c.At), nil
		})
		id := h.say("甲")
		h.waitPart(id, 0, store.PartRetrying)
		h.clk.WaitTimer(t, time.Second)
		select {
		case <-h.closeAsync():
		case <-time.After(5 * time.Second):
			t.Fatal("重试间隔里关闭，Close 该立刻返回")
		}
		if got := h.partStates(id); fmt.Sprint(got) != fmt.Sprint([]string{store.PartAborted}) || h.plan(id).State != store.PlanAborted {
			t.Fatalf("%v %s", got, h.plan(id).State)
		}
		h.clk.Advance(time.Hour)
		if n := len(h.ad.Sent()); n != 1 {
			t.Fatalf("关闭以后不重试：Send %d 次", n)
		}
	})
}

// 关闭以后：Send、Record、回声认领、读计划都不碰库（封库）；ErrClosed
func TestNothingTouchesTheDatabaseAfterClose(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	h.unknownReceipts()
	id := h.say("甲")
	h.waitPlan(id, store.PlanUnknown)
	h.out.Close()

	plans := countRows(t, h.st, `SELECT count(*) FROM outbound_plans`)
	events := countRows(t, h.st, `SELECT count(*) FROM events`)
	if _, err := h.out.Send(context.Background(), "ca", "character", group("g1"), []types.Part{txt("x")}); !errors.Is(err, ErrClosed) {
		t.Errorf("Send：%v", err)
	}
	if _, err := h.out.Record(context.Background(), "ca", "character", group("g1"), store.PlanKindBlocked, ""); !errors.Is(err, ErrClosed) {
		t.Errorf("Record：%v", err)
	}
	if _, err := h.out.Plan(id, group("g1")); !errors.Is(err, ErrClosed) {
		t.Errorf("Plan：%v", err)
	}
	if ok, err := h.out.ClaimEcho(echoOf(acctA, group("g1"), "7001", "甲")); ok || err != nil {
		t.Errorf("关闭以后的回声不认领：%v %v", ok, err)
	}
	if got := countRows(t, h.st, `SELECT count(*) FROM outbound_plans`); got != plans {
		t.Errorf("计划数变了：%d → %d", plans, got)
	}
	if got := countRows(t, h.st, `SELECT count(*) FROM events`); got != events {
		t.Errorf("事件数变了：%d → %d", events, got)
	}
	if st := h.plan(id).Parts[0].State; st != store.PartUnknown {
		t.Errorf("结果未知的段没认领就该保持：%s", st)
	}
	h.out.Close() // 再关一次不出事
}

// 重启：上次没关干净留下的计划，发送中的段转结果未知（不重发），待发和等重试的转已中止（不续发），计划状态相应写，
// 没有任何 Send；已经结束的不碰。之后在窗口内，回声认领照常
func TestRestartSweepsUnfinishedPlans(t *testing.T) {
	t.Parallel()
	conv := group("g1").String()
	mk := func(st *store.Store, id string, texts ...string) {
		p := store.NewPlan{ID: id, CharacterID: "ca", Conversation: conv, Account: acctA, Origin: "character", Kind: store.PlanKindSend, At: baseTime.UnixMilli(),
			Event: &store.NewEvent{Channel: chQQ, Conversation: conv, ActorKind: ActorCharacter, ActorID: "ca", Origin: "character", Kind: "message", Text: strings.Join(texts, "\n\n"), CreatedAt: baseTime.UnixMilli()}}
		for _, tx := range texts {
			p.Parts = append(p.Parts, store.NewPart{Kind: types.PartMessage, Text: tx})
		}
		if _, err := st.AppendPlan(p); err != nil {
			t.Fatal(err)
		}
	}
	sentAt := baseTime.Add(-time.Minute).UnixMilli() // 一分钟前发的
	h := newOHWith(t, types.Caps{}, func(st *store.Store) {
		mk(st, "R1", "甲", "乙", "丙") // 甲确认，乙发送中，丙待发
		st.BeginSend("R1", 0, sentAt)
		st.EndPart("R1", 0, []string{store.PartSending}, store.PartConfirmed, store.PartEnd{PlatformID: "7001"}, sentAt)
		st.BeginSend("R1", 1, sentAt)
		mk(st, "R2", "丁", "戊") // 全待发
		mk(st, "R3", "己")      // 等重试
		st.BeginSend("R3", 0, sentAt)
		st.EndPart("R3", 0, []string{store.PartSending}, store.PartRetrying, store.PartEnd{Err: "没连上"}, sentAt)
		mk(st, "R4", "庚") // 已经完成
		st.BeginSend("R4", 0, sentAt)
		st.EndPart("R4", 0, []string{store.PartSending}, store.PartConfirmed, store.PartEnd{PlatformID: "7002"}, sentAt)
	})
	h.run(nil)

	want := map[string]struct {
		state string
		parts []string
	}{
		"R1": {store.PlanUnknown, []string{store.PartConfirmed, store.PartUnknown, store.PartAborted}},
		"R2": {store.PlanAborted, []string{store.PartAborted, store.PartAborted}},
		"R3": {store.PlanAborted, []string{store.PartAborted}},
		"R4": {store.PlanDone, []string{store.PartConfirmed}},
	}
	for id, w := range want {
		if got := h.partStates(id); fmt.Sprint(got) != fmt.Sprint(w.parts) || h.plan(id).State != w.state {
			t.Errorf("%s：段 %v 计划 %s，应该 %v %s", id, got, h.plan(id).State, w.parts, w.state)
		}
	}
	h.clk.Advance(time.Hour)
	if n := len(h.ad.Sent()) + len(h.ad.Pokes()); n != 0 {
		t.Fatalf("重启不重发、不续发：适配器被调了 %d 次", n)
	}
	if !strings.Contains(h.logText(), "启动时清理了 3 条") {
		t.Errorf("启动时该记一行清理了多少：%s", h.logText())
	}
}

// 重启后窗口还开着（一分钟前发的，窗口 5 分钟）：结果未知的段照样能被回声认领；窗口过了就不认
func TestEchoAfterRestartStillClaims(t *testing.T) {
	t.Parallel()
	conv := group("g1").String()
	sentAt := baseTime.Add(-time.Minute).UnixMilli()
	h := newOHWith(t, types.Caps{}, func(st *store.Store) {
		p := store.NewPlan{ID: "R1", CharacterID: "ca", Conversation: conv, Account: acctA, Origin: "character", Kind: store.PlanKindSend, At: sentAt,
			Event: &store.NewEvent{Channel: chQQ, Conversation: conv, ActorKind: ActorCharacter, ActorID: "ca", Origin: "character", Kind: "message", Text: "重启前发的", CreatedAt: sentAt},
			Parts: []store.NewPart{{Kind: types.PartMessage, Text: `[{"kind":"text","text":"重启前发的"}]`}}}
		if _, err := st.AppendPlan(p); err != nil {
			t.Fatal(err)
		}
		st.BeginSend("R1", 0, sentAt)
	})
	h.run(nil)
	if st := h.plan("R1").State; st != store.PlanUnknown {
		t.Fatalf("准备：%s", st)
	}
	if !h.claim(echoOf(acctA, group("g1"), "7001", "重启前发的")) {
		t.Fatal("重启后窗口还开着，回声该认领")
	}
	if h.plan("R1").State != store.PlanDone {
		t.Fatalf("%+v", h.plan("R1"))
	}
}
