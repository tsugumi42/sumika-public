package channel

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

func TestEchoNotifiesPartOnce(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"回包迟到", "等待取消", "结果未知后回声", "重试等待"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var results []PartResult
			h := newOH(t, types.Caps{}, func(c *OutletConfig) {
				c.OnPart = func(r PartResult) { mu.Lock(); results = append(results, r); mu.Unlock() }
			})
			h.run(nil)
			release, done := make(chan struct{}), make(chan struct{})
			h.ad.OnSend(func(ctx context.Context, c fake.SendCall) (types.Receipt, error) {
				defer close(done)
				switch kind {
				case "回包迟到":
					select {
					case <-release:
					case <-ctx.Done():
						<-release
					}
					return fake.Confirmed(c.Part.ID, "99001", c.At), nil
				case "等待取消":
					<-ctx.Done()
					return fake.Unknown(c.Part.ID, "取消", c.At), nil
				case "重试等待":
					return fake.Failed(c.Part.ID, "拒绝", c.At), nil
				default:
					return fake.Unknown(c.Part.ID, "超时", c.At), nil
				}
			})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			id := h.say("编的回声")
			h.waitSent(1)
			if kind == "结果未知后回声" {
				h.waitPlan(id, store.PlanUnknown)
			}
			if kind == "重试等待" {
				h.waitPart(id, 0, store.PartRetrying)
				h.clk.WaitTimer(t, time.Second)
			}
			if !h.claim(echoOf(acctA, group("g1"), "98001", "编的回声")) {
				t.Fatal("没有认领回声")
			}
			once.Do(func() { close(release) })
			h.waitPlan(id, store.PlanDone)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("回声以后适配器没有返回")
			}
			// Close 等队列退出，迟到回包和 lost 路径都完成以后再数，避免只看到了第一次通知。
			h.out.Close()
			mu.Lock()
			defer mu.Unlock()
			if len(results) != 1 || !results[0].Confirmed || results[0].PlanID != id || results[0].Index != 0 {
				t.Fatalf("回声确认必须恰好通知一次：%+v", results)
			}
			if len(h.ad.Sent()) != 1 {
				t.Fatalf("回声后仍重发：%d", len(h.ad.Sent()))
			}
		})
	}
}

func TestQueuedSendChecksPauseAgain(t *testing.T) {
	t.Parallel()
	var paused atomic.Bool
	h := newOH(t, types.Caps{}, func(c *OutletConfig) {
		c.Check = func(_ string, origin string, _ types.Conversation, _ []types.Part) error {
			if origin != "automation:repeater" {
				return fmt.Errorf("发送检查来源丢失：%q", origin)
			}
			if paused.Load() {
				return fmt.Errorf("暂停了 QQ 发送")
			}
			return nil
		}
	})
	h.run(nil)
	held, err := h.out.SendHeld(context.Background(), "ca", "automation:repeater", group("g1"), []types.Part{txt("编的排队消息")})
	if err != nil {
		t.Fatal(err)
	}
	paused.Store(true)
	if err := held.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitPart(held.PlanID, 0, store.PartAborted)
	if len(h.ad.Sent()) != 0 {
		t.Fatalf("排队以后暂停仍发送了 %d 次", len(h.ad.Sent()))
	}
}
