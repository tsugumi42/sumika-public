package channel

import (
	"context"
	"sync"
	"testing"

	"sumika-native/internal/selfcheck"
)

// S2（规格-运行时自检 B3）：入库失败 → 计数 +1、类别只有分类；正常路径不动这个计数；失败的行为（错误、不挡队列）和以前一样
func TestSelfcheckStoreFailedCounted(t *testing.T) {
	t.Parallel()
	sc := selfcheck.NewCounters()
	h := newHarness(t, func(c *Config) { c.Selfcheck = sc })
	if _, err := h.st.Events.Exec(`CREATE TRIGGER boom BEFORE INSERT ON events WHEN NEW.text = '坏的那条' BEGIN SELECT RAISE(ABORT, '模拟写不进'); END`); err != nil {
		t.Fatal(err)
	}
	good, _ := h.in.Submit(context.Background(), msg(acctA, group("900"), "1", friend, "好的"), Options{})
	if _, err := waitTicket(t, good); err != nil {
		t.Fatal(err)
	}
	if n := sc.Get(selfcheck.IntakeStoreFailed); n != 0 {
		t.Fatalf("正常入库不该数入库失败：%d", n)
	}
	bad, _ := h.in.Submit(context.Background(), msg(acctA, group("900"), "2", friend, "坏的那条"), Options{})
	if _, err := waitTicket(t, bad); err == nil {
		t.Fatal("坏的那条应该照旧失败（埋点不改返回值）")
	}
	if n := sc.Get(selfcheck.IntakeStoreFailed); n != 1 || sc.LastStoreError() != selfcheck.StoreOther {
		t.Fatalf("入库失败应数 1 次、类别 other：%d %q", n, sc.LastStoreError())
	}
	after, _ := h.in.Submit(context.Background(), msg(acctA, group("900"), "3", friend, "之后的"), Options{})
	if r, err := waitTicket(t, after); err != nil || r.Seq == 0 {
		t.Fatalf("后面的应该照常入库：%+v %v", r, err)
	}
	if n := sc.Get(selfcheck.IntakeStoreFailed); n != 1 {
		t.Fatalf("成功的不加：%d", n)
	}
}

// S1 的交接计数：实时入口（Inbound）数 IntakeLive，补录走的 Submit 不数；Pending 是队列里排着、没做完的活
func TestSelfcheckLiveCountedOnlyOnInboundAndPendingTracksQueue(t *testing.T) {
	t.Parallel()
	sc := selfcheck.NewCounters()
	h := newHarness(t, func(c *Config) { c.Selfcheck = sc })
	if n := h.in.Pending(); n != 0 {
		t.Fatalf("闲着时 Pending 应为 0：%d", n)
	}
	// 队列里先塞一件卡住的活：后面交来的排在它后面
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock) // 测试中途失败也要放开卡住的活，不然关库时一直等它
	started := make(chan struct{})
	key := queueKey(group("900"))
	h.in.q.submit(key, job{run: func() { close(started); <-release }, drop: func() {}})
	<-started
	if err := h.in.Inbound(context.Background(), msg(acctA, group("900"), "1", friend, "实时的")); err != nil {
		t.Fatal(err)
	}
	if n := h.in.Pending(); n != 2 {
		t.Fatalf("卡住的 1 件加排着的 1 件：Pending 应为 2：%d", n)
	}
	tk, err := h.in.Submit(context.Background(), msg(acctA, group("900"), "2", friend, "补录的"), Options{Backfill: true})
	if err != nil {
		t.Fatal(err)
	}
	if live, sub := sc.Get(selfcheck.IntakeLive), sc.Get(selfcheck.IntakeSubmitted); live != 1 || sub != 2 {
		t.Fatalf("实时 1 条、交来 2 条（补录不算实时）：live=%d submitted=%d", live, sub)
	}
	unblock()
	if _, err := waitTicket(t, tk); err != nil {
		t.Fatal(err)
	}
	<-h.in.q.idleChan()
	if n := h.in.Pending(); n != 0 {
		t.Fatalf("都做完了 Pending 应为 0：%d", n)
	}
}
