package channel

import (
	"context"
	"errors"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// B12：相同文字的老历史不能抢走当前unknown回复；真正实时回声仍能认领。
func TestBackfillOldSelfCannotClaimCurrentEcho(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.withIntake()
	h.unknownReceipts()
	id := h.say("编的同一句")
	h.waitPlan(id, store.PlanUnknown)
	old := echoOf(acctA, group("g1"), "8701", "编的同一句")
	old.Time = h.clk.Now().Add(-6 * time.Minute)
	tk, err := h.in.Submit(context.Background(), old, Options{Backfill: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitTicket(t, tk); err != nil {
		t.Fatal(err)
	}
	if p := h.plan(id); p.State != store.PlanUnknown || p.Parts[0].PlatformID != "" {
		t.Fatalf("老补录偷走当前回声：%+v", p)
	}
	live := echoOf(acctA, group("g1"), "8702", "编的同一句")
	live.Time = h.clk.Now()
	tk, err = h.in.Submit(context.Background(), live, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitTicket(t, tk); err != nil {
		t.Fatal(err)
	}
	h.waitPlan(id, store.PlanDone)
	if p := h.plan(id); p.Parts[0].PlatformID != "8702" || p.Parts[0].Via != types.ViaEcho {
		t.Fatalf("实时回声认领错误：%+v", p)
	}
}

// B7：一条正在处理时，后一条补录排着队；断线取消以后后一条不准写库。
func TestBackfillQueuedCancellation(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	h := newHarness(t, func(c *Config) {
		c.OnStored = func(s Stored) {
			if s.Inbound.PlatformID == "8801" {
				close(entered)
				<-release
			}
		}
	})
	h.submit(msg(acctA, group("900"), "8801", friend, "第一条"), Options{})
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	tk, err := h.in.Submit(ctx, msg(acctA, group("900"), "8802", friend, "排队的补录"), Options{Backfill: true})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	close(release)
	if _, err := waitTicket(t, tk); !errors.Is(err, context.Canceled) {
		t.Fatalf("排队补录取消结果：%v", err)
	}
	if n := h.count(`SELECT count(*) FROM events`); n != 1 {
		t.Fatalf("取消后仍入库：%d", n)
	}
}
