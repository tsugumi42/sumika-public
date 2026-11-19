package channel

// 规格-运行页 第七节：发送的每一段结局记进 task_runs（成功记耗时，不支持、重试用完、结果未知记原因类别）。
// 用假适配器走真实的出口，旧代码不接 Degrade 时这里收不到行，会超时变红。

import (
	"context"
	"errors"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
	"sumika-native/internal/taskrun"
)

type chanSink struct{ ch chan taskrun.Row }

func (s chanSink) AddTaskRun(r taskrun.Row) error { s.ch <- r; return nil }

func withDegrade(s chanSink) ohOpt {
	return func(c *OutletConfig) { c.Degrade = &taskrun.Degrader{Sink: s} }
}

func (s chanSink) next(t *testing.T) taskrun.Row {
	t.Helper()
	select {
	case r := <-s.ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("没有收到 task_runs 行")
		return taskrun.Row{}
	}
}

func TestOutletRecordsTaskRuns(t *testing.T) {
	t.Parallel()
	t.Run("确认记成功和耗时，没有类别", func(t *testing.T) {
		t.Parallel()
		s := chanSink{make(chan taskrun.Row, 8)}
		h := newOH(t, types.Caps{}, withDegrade(s))
		h.run(nil)
		id := h.say("一条")
		h.waitPlan(id, store.PlanDone)
		r := s.next(t)
		if r.Module != taskrun.ModuleSend || r.Outcome != taskrun.OutcomeOK || r.Class != "" || r.Attempts != 1 || r.Ms < 0 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("适配器不支持：skipped、unsupported，OnPart 带类别", func(t *testing.T) {
		t.Parallel()
		s := chanSink{make(chan taskrun.Row, 8)}
		got := make(chan PartResult, 2)
		h := newOH(t, types.Caps{}, withDegrade(s), func(c *OutletConfig) { c.OnPart = func(p PartResult) { got <- p } })
		h.run(nil)
		h.ad.OnSend(func(context.Context, fake.SendCall) (types.Receipt, error) {
			return types.Receipt{}, types.ErrUnsupported
		})
		id := h.say("一条")
		h.waitPlan(id, store.PlanFailed)
		r := s.next(t)
		if r.Outcome != taskrun.OutcomeSkipped || r.Class != taskrun.ClassUnsupported || r.Attempts != 1 {
			t.Fatalf("%+v", r)
		}
		if p := <-got; p.Confirmed || p.Class != taskrun.ClassUnsupported {
			t.Fatalf("OnPart：%+v", p)
		}
	})
	t.Run("重试用完：failed、retry_exhausted、三次", func(t *testing.T) {
		t.Parallel()
		s := chanSink{make(chan taskrun.Row, 8)}
		got := make(chan PartResult, 2)
		h := newOH(t, types.Caps{}, withDegrade(s), func(c *OutletConfig) { c.OnPart = func(p PartResult) { got <- p } })
		h.run(nil)
		h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
			return fake.Failed(c.Part.ID, "没写出去", c.At), nil
		})
		id := h.say("一条")
		for i := 0; i < 2; i++ {
			h.waitPart(id, 0, store.PartRetrying)
			h.clk.WaitTimer(t, time.Second)
			h.clk.Advance(time.Second)
		}
		h.waitPlan(id, store.PlanFailed)
		r := s.next(t)
		if r.Outcome != taskrun.OutcomeFailed || r.Class != taskrun.ClassRetryExhausted || r.Attempts != 3 {
			t.Fatalf("%+v", r)
		}
		if p := <-got; p.Class != taskrun.ClassRetryExhausted {
			t.Fatalf("OnPart：%+v", p)
		}
	})
	t.Run("结果未知：failed、receipt_unknown", func(t *testing.T) {
		t.Parallel()
		s := chanSink{make(chan taskrun.Row, 8)}
		h := newOH(t, types.Caps{}, withDegrade(s))
		h.run(nil)
		h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
			return fake.Unknown(c.Part.ID, "retcode=1401 已接受，异步执行", c.At), nil
		})
		id := h.say("一条")
		h.waitPlan(id, store.PlanUnknown)
		if r := s.next(t); r.Outcome != taskrun.OutcomeFailed || r.Class != taskrun.ClassReceiptUnknown {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("发送检查没过：skipped、send_check", func(t *testing.T) {
		t.Parallel()
		s := chanSink{make(chan taskrun.Row, 8)}
		h := newOH(t, types.Caps{}, withDegrade(s), func(c *OutletConfig) {
			c.Check = func(string, string, types.Conversation, []types.Part) error { return context.DeadlineExceeded }
		})
		h.run(nil)
		if _, err := h.out.Send(context.Background(), "ca", types.OriginCharacter, group("g1"), []types.Part{txt("一条")}); !errors.Is(err, ErrSendCheck) {
			t.Fatalf("应该报发送检查没过：%v", err)
		}
		if r := s.next(t); r.Outcome != taskrun.OutcomeSkipped || r.Class != taskrun.ClassSendCheck {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("没接 Degrade 的出口照常发", func(t *testing.T) {
		t.Parallel()
		h := newOH(t, types.Caps{})
		h.run(nil)
		h.waitPlan(h.say("一条"), store.PlanDone)
	})
}
