package channel

import (
	"context"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// 回声认领的“强匹配”（EchoNote.Strong）：只有它才允许链接插件撤回补发的那份（规格-链接发送改版 B15，复核问题 1）。
// 强 = 消息段、回声里每张图的标识读得出且对上了段里图的 md5、图没少、对得上的候选里只有这一段是强对上的。
// 转发段、读不出图标识的、被吞了图的、同一张图在两个计划里的都不强。号、群、文字、图都是编的。

// 每种认领各发一段、各来一个回声，看认领的样子和出口记的强弱。
// 改坏检查：strongClaim 里去掉任一条（转发、被吞、不强、不唯一），对应的一格红
func TestEchoClaimStrongness(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		send   func(h *oh) string
		echo   func(id string) types.Inbound
		strong bool
	}
	fwd := func(h *oh) string {
		id, err := h.out.Send(context.Background(), "ca", "automation:link-reader", group("g1"), []types.Part{actionParts()[1]})
		if err != nil {
			h.t.Fatal(err)
		}
		return id
	}
	forwardEcho := func(pid string) types.Inbound {
		in := echoOf(acctA, group("g1"), pid, "")
		in.Segs = []types.Seg{{Kind: types.SegForward, Ref: "编的转发资源"}}
		return in
	}
	for _, c := range []tc{
		{"图的 md5 对上", func(h *oh) string { return h.sendPics("看图", 1) }, func(pid string) types.Inbound { return picEcho(pid, "看图", md5Of(1)+".png") }, true},
		{"图标识读不出（NT 的不透明号）", func(h *oh) string { return h.sendPics("看图", 1) }, func(pid string) types.Inbound { return picEcho(pid, "看图", "opaque-file-id") }, false},
		{"两张图只有一张读得出", func(h *oh) string { return h.sendPics("看图", 1, 2) }, func(pid string) types.Inbound { return picEcho(pid, "看图", md5Of(1)+".png", "opaque-file-id") }, false},
		{"两张图只回来一张（吞了一张）", func(h *oh) string { return h.sendPics("看图", 1, 2) }, func(pid string) types.Inbound { return picEcho(pid, "看图", md5Of(1)+".png") }, false},
		{"图被吞只剩文字", func(h *oh) string { return h.sendPics("看图", 1) }, func(pid string) types.Inbound { return echoOf(acctA, group("g1"), pid, "看图") }, false},
		{"转发段不比内容", fwd, forwardEcho, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newOH(t, mediaCaps, unguardedLinks)
			h.withIntake()
			h.unknownReceipts()
			id := c.send(h)
			h.waitPlan(id, store.PlanUnknown)
			if !h.claim(c.echo("7001")) {
				t.Fatal("该认领")
			}
			n, ok := h.out.AwaitPart(context.Background(), "automation:link-reader", id, 0, 0)
			if !ok || n.PlatformID != "7001" || n.Strong != c.strong {
				t.Fatalf("强弱不对（该 %v）：%+v %v", c.strong, n, ok)
			}
			if got := h.out.ClaimedStrong(id, 0, "7001"); got != c.strong {
				t.Fatalf("出口记的强弱不对（该 %v）：%v", c.strong, got)
			}
			if h.out.ClaimedStrong(id, 0, "7002") || h.out.ClaimedStrong(id, 1, "7001") || h.out.ClaimedStrong("别的计划", 0, "7001") {
				t.Fatal("号、段、计划对不上，不算强匹配")
			}
		})
	}
}

// 同一张图在两个计划里（同会话、同发送号、同文字，比如同一条链接 5 分钟内展开了两次）：回声的图 md5 两边都对得上，
// 认到哪一个都不能保证，不算强匹配；其中一个认过以后，剩下那个的回声才是唯一的强匹配。
// 改坏检查：strongClaim 里去掉“只有一个强对上的候选”那一条，第一次认领这格红
func TestEchoClaimOfSameImageInTwoPlansIsNotStrong(t *testing.T) {
	t.Parallel()
	h := newOH(t, mediaCaps, unguardedLinks)
	h.withIntake()
	h.unknownReceipts()
	a := h.sendPics("看图", 1)
	h.waitPlan(a, store.PlanUnknown)
	b := h.sendPics("看图", 1)
	h.waitPlan(b, store.PlanUnknown)
	if !h.claim(picEcho("7001", "看图", md5Of(1)+".png")) {
		t.Fatal("该认领")
	}
	if h.out.ClaimedStrong(a, 0, "7001") || h.out.ClaimedStrong(b, 0, "7001") {
		t.Fatal("两个计划里同一张图，认到哪个都不强")
	}
	if !h.claim(picEcho("7002", "看图", md5Of(1)+".png")) {
		t.Fatal("剩下那个的回声该认领")
	}
	if !h.out.ClaimedStrong(b, 0, "7002") && !h.out.ClaimedStrong(a, 0, "7002") {
		t.Fatal("只剩一个候选时，图 md5 对上就是强匹配")
	}
}

// 计划号 → 源消息的内存表：几段写的源消息一致才记，不一致、没写、过期都查不到
func TestPlanSourceLedger(t *testing.T) {
	t.Parallel()
	h := newOH(t, mediaCaps, unguardedLinks)
	h.run(nil)
	h.unknownReceipts()
	body := func(text string) types.Part {
		return types.Part{Kind: types.PartMessage, Body: []types.BodySegment{{Kind: types.BodyText, Text: text}}}
	}
	send := func(seqs ...int64) string {
		var parts []types.Part
		for i, s := range seqs {
			p := body("第" + string(rune('一'+i)) + "段")
			p.SourceSeq = s
			parts = append(parts, p)
		}
		id, err := h.out.SendOwned(context.Background(), "ca", "automation:link-reader", group("g1"), parts, nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	same, mixed, none := send(41, 41), send(41, 42), send(0)
	if got, ok := h.out.PlanSource(same); !ok || got != 41 {
		t.Fatalf("两段同一条源消息：%d %v", got, ok)
	}
	if _, ok := h.out.PlanSource(mixed); ok {
		t.Fatal("两段的源消息不一样，不记")
	}
	if _, ok := h.out.PlanSource(none); ok {
		t.Fatal("没写源消息，不记")
	}
	if _, ok := h.out.PlanSource("不存在的计划"); ok {
		t.Fatal("不存在的计划查不到")
	}
}
