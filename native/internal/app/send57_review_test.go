package app

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/store"
)

func TestSend57PrivatePoke(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"主人", "白名单里的别人", "reply关闭", "仍钳制"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			e, a, peer := sendApp(t, "全部齐全")
			a.cfg.QQ.Reply = kind != "reply关闭"
			if kind == "仍钳制" {
				a.cfg.QQ.GoSend = false
			}
			actor := testOwner
			if kind == "白名单里的别人" {
				actor = "90005"
				a.cfg.QQ.PrivateAllow = []config.QQID{config.QQID(actor)}
			}
			c := startSend(t, e, a)
			e.peer.send(t, c, fmt.Sprintf(`{"post_type":"notice","notice_type":"notify","sub_type":"poke","self_id":%s,"user_id":%s,"target_id":%s,"time":%d}`, testAccount, actor, testAccount, e.now.Unix()))
			conv := types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: actor}
			waitFor(t, "私聊戳一戳入库", func() bool { n, err := e.eventCount(a, conv.String()); return err == nil && n == 1 })
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := a.intake.Idle(ctx); err != nil {
				t.Fatal(err)
			}
			if kind == "主人" {
				waitFor(t, "给主人戳回一次", func() bool { return peer.sends.Load() == 1 })
			}
			a.outlet.Close()
			if kind != "主人" {
				kinds, err := e.planKinds(a, conv.String())
				if err != nil || len(kinds) != 0 {
					t.Fatalf("只存不回仍建了发送计划：%v err=%v action=%d", kinds, err, peer.sends.Load())
				}
			}
			want := int32(0)
			if kind == "主人" {
				want = 1
			}
			if peer.sends.Load() != want || a.kernel.calls.Load() != 0 {
				t.Fatalf("私聊戳回去次数不对：action=%d，要 %d，内核=%d", peer.sends.Load(), want, a.kernel.calls.Load())
			}
		})
	}
}

func TestSend57QueuedLockLost(t *testing.T) {
	t.Parallel()
	e, a, peer := sendApp(t, "全部齐全")
	conv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
	if err := a.set.TraceStore().SetGateMode(conv.String(), "speak", e.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	e.peer.onAction = func(c *websocket.Conn, b []byte) {
		if isSendAction(string(b)) {
			enteredOnce.Do(func() { close(entered) })
			<-release
		}
		peer.handle(c, b)
	}
	startSend(t, e, a)
	first := sendPlan(t, a, "编的在途消息")
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("第一条没有进入套接字")
	}
	queued := sendPlan(t, a, "编的排队后丢锁")
	st, _ := a.set.For("qq")
	p, err := st.GetPlan(queued)
	if err != nil || p.Kind != store.PlanKindSend || len(p.Parts) != 1 || p.Parts[0].State != store.PartPending {
		t.Fatalf("没有排在在途消息后面：%+v err=%v", p, err)
	}
	if err := a.sendLock.Release(); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	waitSendPlan(t, a, first, store.PlanDone)
	waitFor(t, "排队丢锁后的计划结束", func() bool { p, err := st.GetPlan(queued); return err == nil && p.Parts[0].State != store.PartPending })
	a.outlet.Close()
	p, err = st.GetPlan(queued)
	if err != nil || p.Parts[0].State != store.PartAborted || peer.sends.Load() != 1 {
		t.Fatalf("已排队的消息丢锁后仍发出：state=%+v action=%d，要只有在途的 1 次，err=%v", p, peer.sends.Load(), err)
	}
}
