package snowluma

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

func TestVerificationRefusalDoesNotPause(t *testing.T) {
	for _, phase := range []int{phaseUnhealthy, phaseMismatch, phaseSelf} {
		name := map[int]string{phaseUnhealthy: "不健康", phaseMismatch: "登录号不对", phaseSelf: "帧里的号不对"}[phase]
		t.Run(name, func(t *testing.T) {
			e := newSendEnv(t, confirmReply())
			e.waitSending()
			e.out.Close()
			roster := channel.Roster{Accounts: []channel.Account{{Channel: "qq", ID: sendAccount, Character: "ca"}}}
			var parts, pauses atomic.Int32
			gate, err := channel.NewGate(channel.GateConfig{Stores: store.Single(e.st), Roster: roster, Release: func(types.Batch) {}, OnPause: func(string, bool, string) { pauses.Add(1) }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(gate.Close)
			e.out, err = channel.NewOutlet(channel.OutletConfig{
				Stores: store.Single(e.st), Roster: roster, Adapters: []types.Adapter{e.ad}, Clock: e.clk, Logf: e.logf,
				OnPart: func(p channel.PartResult) { parts.Add(1); gate.PartEnded("qq", p.Confirmed, !p.Confirmed) },
			})
			if err != nil {
				t.Fatal(err)
			}
			e.ad.mu.Lock()
			sid, live := e.ad.session, e.ad.live
			e.ad.phase = phase
			e.ad.mu.Unlock()
			for range 3 {
				id := e.sendParts(groupConv(), textPart())
				e.waitPart(id, store.PartUnknown, 1)
			}
			if _, paused := gate.Paused("qq"); paused || parts.Load() != 0 || pauses.Load() != 0 || len(e.peer.actions()) != 0 {
				t.Fatalf("未写出被计成发送失败：paused=%v OnPart=%d 提醒=%d action=%d", paused, parts.Load(), pauses.Load(), len(e.peer.actions()))
			}
			if phase == phaseUnhealthy {
				// 同一条套接字复查恢复；不调用 Resume，下一段就能确认。
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				e.ad.watchStatus(ctx, live, sid)
				if !e.ad.Ready() {
					t.Fatal("同一连接健康复查没有恢复")
				}
				id := e.sendParts(groupConv(), textPart())
				e.waitPlan(id, store.PlanDone)
				e.out.Close()
				if len(e.peer.actions()) != 1 || e.peer.nConns() != 1 || parts.Load() != 1 {
					t.Fatalf("恢复后应直接发一段：action=%d 连接=%d OnPart=%d", len(e.peer.actions()), e.peer.nConns(), parts.Load())
				}
			}
		})
	}
}

func TestWrongSendAccountHasNoFailureReceipt(t *testing.T) {
	a, _, conn, _, sid := reviewAdapter()
	a.markReady(sid)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := a.Send(ctx, "90009", types.Part{ID: "编的段", Kind: types.PartMessage, Conv: groupConv(), Body: types.TextBody(sendText)})
	p, pokeErr := a.Poke(ctx, "90009", privateConv(), sendFriend)
	if err != nil || pokeErr != nil || r.Status != types.ReceiptUnknown || p.Status != types.ReceiptUnknown || conn.writes.Load() != 0 {
		t.Fatalf("调用号不对仍算明确失败或写出：文字=%+v 戳=%+v err=%v/%v writes=%d", r, p, err, pokeErr, conn.writes.Load())
	}
}
