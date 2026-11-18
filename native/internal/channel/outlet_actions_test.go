package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

func actionCaps() types.Caps {
	return types.Caps{QuoteGroup: true, QuotePrivate: true, ForwardGroup: true, ForwardPrivate: true, ReactGroup: true}
}

func actionParts() []types.Part {
	return []types.Part{
		{Kind: types.PartMessage, Body: types.TextBody("编的导语"), Quote: "-90004"},
		{Kind: types.PartForward, Nodes: []types.ForwardNode{{Name: "外部内容·纸鸢", Body: types.TextBody("第一段")}, {Name: "外部内容·石榴", Body: types.TextBody("第二段")}}},
		{Kind: types.PartReact, Ref: "-90004", EmojiID: "66"},
	}
}

func allowActionGuard(c *OutletConfig) { c.GuardFor = func(string) Guard { return &fakeGuard{} } }

func TestActionOutlet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, conv := range []types.Conversation{group("g1"), {Channel: chQQ, Scene: types.ScenePrivate, ID: friend, Self: acctA}} {
		for _, p := range actionParts() {
			t.Run(conv.Scene+"/能力/"+p.Kind, func(t *testing.T) {
				h := newOH(t, types.Caps{}, allowActionGuard)
				h.run(nil)
				_, err := h.out.Send(ctx, "ca", "automation:link-reader", conv, []types.Part{p})
				if !errors.Is(err, types.ErrUnsupported) || len(h.ad.Sent()) != 0 {
					t.Fatalf("能力关闭仍排队：err=%v sends=%d", err, len(h.ad.Sent()))
				}
			})
		}
	}
	for _, p := range actionParts() {
		t.Run("暂停/"+p.Kind, func(t *testing.T) {
			var paused atomic.Bool
			h := newOH(t, actionCaps(), allowActionGuard, func(c *OutletConfig) {
				c.Check = func(string, string, types.Conversation, []types.Part) error {
					if paused.Load() {
						return fmt.Errorf("已暂停")
					}
					return nil
				}
			})
			h.run(nil)
			held, err := h.out.SendHeld(ctx, "ca", "automation:link-reader", group("g1"), []types.Part{p})
			if err != nil {
				t.Fatal(err)
			}
			paused.Store(true)
			if err := held.Start(); err != nil {
				t.Fatal(err)
			}
			h.waitPlan(held.PlanID, store.PlanAborted)
			if len(h.ad.Sent()) != 0 {
				t.Fatalf("暂停后发送 %d 次", len(h.ad.Sent()))
			}
			if _, err := h.out.Send(ctx, "ca", "automation:link-reader", group("g1"), []types.Part{p}); !errors.Is(err, ErrSendCheck) {
				t.Fatalf("暂停时入口未拒绝：%v", err)
			}
		})
	}
	t.Run("映射和节点快照", func(t *testing.T) {
		h := newOH(t, actionCaps(), allowActionGuard)
		h.run(nil)
		h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
			return fake.Confirmed(c.Part.ID, fmt.Sprintf("-%d", 90010+c.N), c.At), nil
		})
		parts := actionParts()
		held, err := h.out.SendHeld(ctx, "ca", "automation:link-reader", group("g1"), parts)
		if err != nil {
			t.Fatal(err)
		}
		parts[1].Nodes[1].Body[0].Text = "调用方后来改过"
		if err := held.Start(); err != nil {
			t.Fatal(err)
		}
		h.waitPlan(held.PlanID, store.PlanDone)
		for i, want := range []int{1, 1, 0} {
			var n int
			if err := h.st.Events.QueryRow(`SELECT count(*) FROM channel_msgs WHERE platform_id = ?`, fmt.Sprintf("-%d", 90011+i)).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != want {
				t.Fatalf("第 %d 段 channel_msgs=%d，要 %d", i, n, want)
			}
		}
		if sent := h.ad.Sent(); len(sent) != 3 || sent[0].Part.Quote != "-90004" || types.BodyTextOf(sent[1].Part.Nodes[1].Body) != "第二段" {
			t.Fatalf("实际发送：%+v", sent)
		}
		plan := h.plan(held.PlanID)
		if plan.Parts[0].Ref != "-90004" || !strings.Contains(plan.Parts[1].Text, "第二段") || !strings.Contains(plan.Parts[2].Ref, `"emoji_id":"66"`) {
			t.Fatalf("计划没有保留动作字段：%+v", plan.Parts)
		}
	})
}

func TestActionGuard(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{types.PartForward, types.PartMessage} {
		for _, mode := range []string{"命中", "放行", "缺守卫", "出错", "panic", "取消"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				t.Parallel()
				g := &fakeGuard{}
				switch mode {
				case "命中":
					g.blockOn = []string{"第二段"}
				case "出错":
					g.err = errors.New("编的错误")
				case "panic":
					g.panics = true
				}
				h := newOH(t, actionCaps(), func(c *OutletConfig) {
					if mode != "缺守卫" {
						c.GuardFor = func(string) Guard { return g }
					}
				})
				h.run(nil)
				p := actionParts()[1]
				if kind == types.PartMessage {
					p = types.Part{Kind: kind, Body: types.TextBody("第二段"), Quote: "-90004"}
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "取消" {
					cancel()
				}
				id, err := h.out.Send(ctx, "ca", "automation:link-reader", group("g1"), []types.Part{p})
				if mode == "放行" {
					if err != nil {
						t.Fatal(err)
					}
					h.waitPlan(id, store.PlanDone)
					if len(h.ad.Sent()) != 1 {
						t.Fatalf("放行发送 %d 次", len(h.ad.Sent()))
					}
				} else if err == nil || len(h.ad.Sent()) != 0 {
					t.Fatalf("应整条不发：err=%v sends=%d", err, len(h.ad.Sent()))
				}
				if mode == "命中" && kind == types.PartForward {
					if got := g.texts(); len(got) == 0 || !strings.Contains(got[len(got)-1], "第一段") || !strings.Contains(got[len(got)-1], "第二段") {
						t.Fatalf("没有累计检查第二节点：%q", got)
					}
					var n int
					if err := h.st.Events.QueryRow(`SELECT count(*) FROM events`).Scan(&n); err != nil {
						t.Fatal(err)
					}
					if n != 0 {
						t.Fatalf("拦下仍落原文 %d 行", n)
					}
				}
			})
		}
	}
}

func TestActionEcho(t *testing.T) {
	t.Parallel()
	h := newOH(t, actionCaps(), allowActionGuard)
	h.run(nil)
	h.unknownReceipts()
	id, err := h.out.Send(context.Background(), "ca", "automation:link-reader", group("g1"), []types.Part{actionParts()[0]})
	if err != nil {
		t.Fatal(err)
	}
	h.waitPlan(id, store.PlanUnknown)
	in := types.Inbound{Conv: group("g1"), From: types.User{ID: acctA}, PlatformID: "-90020", Segs: []types.Seg{{Kind: types.SegReply, Target: "-99999"}, {Kind: types.SegText, Text: "编的导语"}}}
	if ok, err := h.out.ClaimEcho(in); err != nil || !ok {
		t.Fatalf("引用回声没有认领：%v %v", ok, err)
	}
	if ok, err := h.out.ClaimEcho(in); err != nil || ok {
		t.Fatalf("回声重复认领：%v %v", ok, err)
	}
	h.waitPlan(id, store.PlanDone)
	other := h.say("[合并转发]")
	h.waitPlan(other, store.PlanUnknown)
	in.PlatformID, in.Segs = "-90021", []types.Seg{{Kind: types.SegForward, Ref: "编的转发资源"}}
	if ok, err := h.out.ClaimEcho(in); err != nil || ok {
		t.Fatalf("转发回声误认文字：%v %v", ok, err)
	}
	if h.plan(other).State != store.PlanUnknown {
		t.Fatal("转发回声改了别的文字段")
	}
	forward, err := h.out.Send(context.Background(), "ca", "automation:link-reader", group("g1"), []types.Part{actionParts()[1]})
	if err != nil {
		t.Fatal(err)
	}
	h.waitPlan(forward, store.PlanUnknown)
	in.PlatformID, in.Segs = "-90022", []types.Seg{{Kind: types.SegText, Text: "第一段\n\n第二段"}}
	if ok, err := h.out.ClaimEcho(in); err != nil || ok {
		t.Fatalf("转发被文字认领：%v %v", ok, err)
	}
}

// 真正走出口、入库站和临时库：回声与回包交换顺序，映射、确认和通知都只能有一份。
func TestForwardEchoReceiptOrder(t *testing.T) {
	t.Parallel()
	for _, conv := range []types.Conversation{group("g1"), private(acctA, friend)} {
		for _, echoFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/回声先到=%v", conv.Scene, echoFirst), func(t *testing.T) {
				t.Parallel()
				spy := &partSpy{}
				var stored atomic.Int32
				h := newOH(t, actionCaps(), allowActionGuard, func(c *OutletConfig) { c.OnPart = spy.hook })
				h.withIntake(func(c *Config) { c.OnStored = func(Stored) { stored.Add(1) } })
				reply := make(chan struct{}, 1)
				returned := make(chan struct{})
				t.Cleanup(func() { reply <- struct{}{} })
				h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
					// 故意让迟到回包不理 ctx 取消，确认它回来以后也不会再改库、通知。
					<-reply
					defer close(returned)
					return fake.Confirmed(c.Part.ID, "-90030", c.At), nil
				})
				id, err := h.out.Send(context.Background(), "ca", "automation:link-reader", conv, []types.Part{actionParts()[1]})
				if err != nil {
					t.Fatal(err)
				}
				h.waitSent(1)
				h.waitPart(id, 0, store.PartSending)
				in := echoOf(acctA, conv, "-90030", "")
				in.Segs = []types.Seg{{Kind: types.SegForward, Ref: "编的转发资源"}}
				via := types.ViaReply
				if echoFirst {
					h.push(in)
					if n := countRows(t, h.st, `SELECT count(*) FROM events WHERE origin = 'human'`); n != 0 {
						t.Fatalf("转发回声被存成 human：%d 条", n)
					}
					via = types.ViaEcho
				} else {
					reply <- struct{}{}
					h.waitPlan(id, store.PlanDone)
					h.push(in)
				}
				if echoFirst {
					reply <- struct{}{}
				}
				select {
				case <-returned:
				case <-time.After(5 * time.Second):
					t.Fatal("放行以后转发回包还没有返回")
				}
				select {
				case <-h.out.q.idleChan():
				case <-time.After(5 * time.Second):
					t.Fatalf("转发发送没有结束：%+v\n%s", h.plan(id), h.logText())
				}
				h.push(in) // 同一个号重推，不能再确认或通知。
				p := h.plan(id)
				if pt := p.Parts[0]; p.State != store.PlanDone || pt.State != store.PartConfirmed || pt.Via != via || pt.PlatformID != in.PlatformID || pt.Attempts != 1 {
					t.Fatalf("转发只该确认一次且保留先到的结果：%+v", p)
				}
				if n := countRows(t, h.st, `SELECT count(*) FROM channel_msgs WHERE channel = ? AND conversation = ? AND platform_id = ? AND event_seq = ?`, chQQ, conv.String(), in.PlatformID, p.EventSeq); n != 1 {
					t.Fatalf("转发的号没有指向计划事件 %d：%d 条映射", p.EventSeq, n)
				}
				if n := countRows(t, h.st, `SELECT count(*) FROM events`); n != 1 {
					t.Fatalf("只该有计划事件，实际 %d 条", n)
				}
				if got := spy.list(); len(got) != 1 || got[0].PlanID != id || got[0].Index != 0 || !got[0].Confirmed {
					t.Fatalf("OnPart 该恰好确认一次：%+v", got)
				}
				if n := stored.Load(); n != 0 {
					t.Fatalf("自己的转发回声不该进 OnStored：%d 次", n)
				}
			})
		}
	}
}

func TestForwardEchoWithoutForwardIsHuman(t *testing.T) {
	t.Parallel()
	for _, conv := range []types.Conversation{group("g1"), private(acctA, friend)} {
		t.Run(conv.Scene, func(t *testing.T) {
			t.Parallel()
			h := newOH(t, actionCaps(), allowActionGuard)
			h.withIntake()
			h.unknownReceipts()
			textID := h.sayIn(conv, "[合并转发]")
			h.waitPlan(textID, store.PlanUnknown)
			in := echoOf(acctA, conv, "-90031", "")
			in.Segs = []types.Seg{{Kind: types.SegForward, Ref: "编的手动转发"}}
			h.push(in)
			if n := countRows(t, h.st, `SELECT count(*) FROM events ev JOIN channel_msgs cm ON cm.event_seq = ev.seq WHERE cm.conversation = ? AND cm.platform_id = ? AND ev.origin = 'human' AND ev.text = '[合并转发]'`, conv.String(), in.PlatformID); n != 1 {
				t.Fatalf("没有在途转发，手动转发该正常存成 human：%d 条", n)
			}
			if p := h.plan(textID); p.State != store.PlanUnknown || p.Parts[0].PlatformID != "" {
				t.Fatalf("手动转发不能认文字段：%+v", p)
			}
		})
	}
}

func TestForwardEchoClaimsOnlyForwardParts(t *testing.T) {
	t.Parallel()
	for _, conv := range []types.Conversation{group("g1"), private(acctA, friend)} {
		t.Run(conv.Scene, func(t *testing.T) {
			t.Parallel()
			h := newOH(t, actionCaps(), allowActionGuard)
			h.withIntake()
			h.unknownReceipts()
			// 文字发得更早，且恰好等于转发回声的占位和附带正文。
			textID := h.sayIn(conv, "[合并转发]第一段")
			h.waitPlan(textID, store.PlanUnknown)
			var ids []string
			for range 2 {
				h.clk.Advance(time.Second)
				id, err := h.out.Send(context.Background(), "ca", "automation:link-reader", conv, []types.Part{actionParts()[1]})
				if err != nil {
					t.Fatal(err)
				}
				h.waitPlan(id, store.PlanUnknown)
				ids = append(ids, id)
			}
			for i, id := range ids {
				in := echoOf(acctA, conv, fmt.Sprintf("-9004%d", i), "")
				in.Segs = []types.Seg{{Kind: types.SegForward, Ref: "编的转发资源"}, {Kind: types.SegText, Text: "第一段"}}
				h.push(in)
				h.push(in)
				if p := h.plan(textID); p.State != store.PlanUnknown || p.Parts[0].PlatformID != "" {
					t.Fatalf("forward 回声误认更早的文字段：%+v", p)
				}
				p := h.plan(id)
				if p.State != store.PlanDone || p.Parts[0].Via != types.ViaEcho || p.Parts[0].PlatformID != in.PlatformID {
					t.Fatalf("第 %d 个回声该认第 %d 条转发：%+v", i+1, i+1, p)
				}
				if i == 0 && h.plan(ids[1]).State != store.PlanUnknown {
					t.Fatal("重复回声不能认第二条转发")
				}
			}
			if n := countRows(t, h.st, `SELECT count(*) FROM events WHERE origin = 'human'`); n != 0 {
				t.Fatalf("转发回声不该新增 human 事件：%d 条", n)
			}
		})
	}
}
