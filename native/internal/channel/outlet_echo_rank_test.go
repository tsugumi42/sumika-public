package channel

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/store"
)

// 跨计划的回声归属（复核问题 1）：原段等过回声、到点放弃了，同形状的重发段再发；回声里图的标识读不出时，
// 认给重发段，不按“先发先认”给已经放弃的原段
func TestEchoPrefersRetryOverGaveUpPart(t *testing.T) {
	t.Parallel()
	h := newOH(t, mediaCaps, unguardedLinks)
	h.run(nil)
	h.unknownReceipts()

	a := h.sendPics("同样的字", 1)
	h.waitPlan(a, store.PlanUnknown)
	done := make(chan bool, 1)
	go func() {
		_, ok := h.out.AwaitPart(context.Background(), "automation:link-reader", a, 0, 2*time.Second)
		done <- ok
	}()
	h.clk.WaitTimer(t, 2*time.Second)
	h.clk.Advance(2 * time.Second)
	if <-done {
		t.Fatal("原段没有回声")
	}
	b := h.sendPics("同样的字", 2)
	h.waitPlan(b, store.PlanUnknown)
	if !h.claim(picEcho("7101", "同样的字", "opaque-file-id")) {
		t.Fatal("回声该被认领")
	}
	if pa, pb := h.plan(a).Parts[0], h.plan(b).Parts[0]; pb.State != store.PartConfirmed || pb.PlatformID != "7101" || pa.State != store.PartUnknown {
		t.Fatalf("回声该认给重发段：原段 %+v，重发段 %+v", pa, pb)
	}
	// 只剩放弃了的原段对得上：仍认它（迟到的回声）
	if !h.claim(picEcho("7102", "同样的字", "opaque-file-id-2")) || h.plan(a).Parts[0].State != store.PartConfirmed {
		t.Fatalf("只剩原段时仍认它：%+v", h.plan(a).Parts[0])
	}
}

// 认领回调在出口的结果锁外调（复核问题 5）：回调里写日志等慢操作不该让所有发送等着
func TestClaimHookRunsOutsideResultLock(t *testing.T) {
	t.Parallel()
	h := newOH(t, mediaCaps, unguardedLinks)
	h.run(nil)
	h.unknownReceipts()
	free := make(chan bool, 1)
	h.out.onClaim("automation:link-reader", func(EchoClaimed) {
		ok := h.out.resultMu.TryLock()
		if ok {
			h.out.resultMu.Unlock()
		}
		free <- ok
	})
	a := h.sendPics("一张", 3)
	h.waitPlan(a, store.PlanUnknown)
	if !h.claim(picEcho("7201", "一张", md5Of(3)+".png")) {
		t.Fatal("回声该被认领")
	}
	if !<-free {
		t.Fatal("认领回调时出口的结果锁还被占着")
	}
}
