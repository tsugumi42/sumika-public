package approval

import (
	"context"
	"sync"
	"testing"
	"time"
)

// DecideVia 只收 once、deny：不认识的 choice 不能当批准（Decide 对不认识的按批准算，这里不行），也不记任何授权。
func TestDecideViaRejectsOtherChoices(t *testing.T) {
	for _, choice := range []string{"", "approve", "yes", "conversation", "always", "ONCE", "once "} {
		a := newApprovals(t)
		got := make(chan Decision, 1)
		go func() {
			got <- a.Ask(context.Background(), card("x"), &Pending{Key: "k", Label: "这一类", Tool: "powershell", Conversation: "console:c"})
		}()
		waitPending(t, a, "x")
		ok, err := a.DecideVia("x", choice, "qq")
		if ok || err == nil {
			t.Fatalf("%q 不能结算：%v %v", choice, ok, err)
		}
		if len(a.Pending()) != 1 {
			t.Fatalf("%q 不能动这张卡", choice)
		}
		if a.Grants.Has("k") {
			t.Fatalf("%q 不能写始终授权", choice)
		}
		if a.Conv.Has("console:c", "k") {
			t.Fatalf("%q 不能写对话授权", choice)
		}
		a.Cancel("x")
		<-got
	}
}

func TestDecideViaNeedsSource(t *testing.T) {
	a := newApprovals(t)
	if ok, err := a.DecideVia("x", "once", ""); ok || err == nil {
		t.Fatal("没有来源说明不能结算")
	}
}

func TestDecideViaOnceAndDeny(t *testing.T) {
	for choice, want := range map[string]string{"once": "approved", "deny": "denied"} {
		a := newApprovals(t)
		resolved := make(chan Decision, 1)
		a.Subscribe(func(Request) {}, func(_ Request, d Decision) { resolved <- d })
		got := make(chan Decision, 1)
		go func() {
			got <- a.Ask(context.Background(), card("x"), &Pending{Key: "k", Label: "这一类", Tool: "powershell", Conversation: "console:c"})
		}()
		waitPending(t, a, "x")
		if ok, err := a.DecideVia("x", choice, "qq"); !ok || err != nil {
			t.Fatal(ok, err)
		}
		if d := approvalRecv(t, got, "结局"); d.Kind != want {
			t.Fatalf("%s 应是 %s，得到 %v", choice, want, d)
		}
		if d := approvalRecv(t, resolved, "订阅方的结局"); d.Via != "qq" || d.Kind != want {
			t.Fatalf("订阅方要看到来源：%+v", d)
		}
		if a.Grants.Has("k") {
			t.Fatal("once 不记授权")
		}
		if a.Conv.Has("console:c", "k") {
			t.Fatal("once 不记对话授权")
		}
		if ok, _ := a.DecideVia("x", "once", "qq"); ok {
			t.Fatal("一次性：第二次不能再结算")
		}
	}
}

// 本机和 QQ 抢同一张卡：恰一方赢，输的得到 false。
func TestDecideViaRacesLocal(t *testing.T) {
	for i := 0; i < 200; i++ {
		a := newApprovals(t)
		got := make(chan Decision, 1)
		go func() { got <- a.Ask(context.Background(), card("x"), nil) }()
		waitPending(t, a, "x")
		var wg sync.WaitGroup
		var viaOK, localOK bool
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; viaOK, _ = a.DecideVia("x", "once", "qq") }()
		go func() { defer wg.Done(); <-start; localOK, _ = a.Decide("x", "deny", "") }()
		close(start)
		wg.Wait()
		if viaOK == localOK {
			t.Fatalf("第 %d 轮必须恰一方赢：via=%v local=%v", i, viaOK, localOK)
		}
		d := approvalRecv(t, got, "结局")
		if viaOK && (d.Kind != "approved" || d.Via != "qq") || localOK && (d.Kind != "denied" || d.Via != "") {
			t.Fatalf("结局和赢家对不上：%+v via=%v", d, viaOK)
		}
	}
}

func waitPending(t *testing.T, a *Approvals, id string) {
	t.Helper()
	for range 2000 {
		for _, r := range a.Pending() {
			if r.ID == id {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("卡片 %s 没有进入等待", id)
}
