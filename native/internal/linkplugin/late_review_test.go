package linkplugin

// 2026-11-14 复核（data/dev/review-bigimage-572d948b.md 问题 4）补的几条：原来改坏后仍全绿的几处，各配一条会变红的测试。
// 假出口、夹具同 late_test.go；不睡、不等固定毫秒。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
)

// 一个计划里两段：第一段大图没回声，后面那段被出口中止、原样补发（它从没发出过）。
// 大图原来的回声晚到：只撤大图的补发份（转 180° 的图），不撤那条补发的后续段（那是另一段的原内容，撤了群里就少一条）。
// 改坏检查（复核 m1）：去掉 settle 里 `d.guard = outer`，后续段的补发被记进大图的看守名下，这条红（撤了两份）
func TestTailAfterBigImageIsNotRecalledAsReplacement(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, func(n int, p sentPart) echoReply {
		switch {
		case n == 1:
			return unconfirmed(f.clock)
		case n == 2:
			return echoReply{out: PartOutcome{State: OutcomeAborted}} // 前面那段没确认，这一段被出口中止
		case len(p.images) > 0:
			return okSent("-90002", f.clock) // 转 180° 的图
		}
		return okSent("-90004", f.clock) // 原样补发的后续段
	}, nil)
	rep := f.deliver(nil, textAndPics("大图那段", bigPicture(t)), types.Part{Kind: types.PartMessage, Body: []types.BodySegment{{Kind: types.BodyText, Text: "后面的段"}}})
	s := f.out.sent()
	if len(s) != 4 || len(s[2].images) != 1 || s[3].text != "后面的段" || !rep.landed {
		t.Fatalf("先决条件：第 1 段没回声转图补发、第 2 段被中止后原样补发：%d 帧 %+v %+v", len(s), s, rep)
	}
	f.out.hook(lateOrig("-90001"))
	f.waitLate()
	got := f.dupSnapshot()
	if len(got) != 1 || got[0].dup != (PartRef{PlanID: "p2", Idx: 0, PlatformID: "-90002"}) {
		t.Fatalf("只撤大图的补发份（p2 的 -90002），不撤后面段的补发（p3 的 -90004）：%+v", got)
	}
}

// 回声在文字版发出去之前到了：这一块不并进文字版。大动图没有可换的形式，阶梯里一级也没试，
// 阶梯循环里的 originalArrived 碰不到，只有 finishText 里那一道问得到（静态图会先被阶梯那一道接住）。
// 改坏检查（复核 m10）：去掉 finishText 里文字版前的 originalArrived 过滤，这条红（还是发了文字版）
func TestLateOriginalEchoBeforeTextVersionDropsIt(t *testing.T) {
	t.Parallel()
	gif := types.Part{Kind: types.PartMessage, Body: []types.BodySegment{{Kind: types.BodyText, Text: "动图"}, {Kind: types.BodyGIF, Media: types.MemoryMedia("g", "image/gif", 720, 405, make([]byte, 5<<20))}}}
	var f *flow
	f = newFlow(t, func(n int, p sentPart) echoReply {
		if len(p.images) > 0 {
			return unconfirmed(f.clock)
		}
		return okSent("-90003", f.clock)
	}, nil)
	f.out.onGiveUp = func(string, int) { f.out.hook(lateOrig("-90001")) } // 等满放弃的那一瞬间，回声到了
	rep := f.deliver(nil, gif)
	f.waitLate()
	if s := f.out.sent(); len(s) != 1 {
		t.Fatalf("回声在文字版之前到了，不该再发文字版：%d 帧 %+v", len(s), s)
	}
	if !rep.delivered || !rep.landed || rep.unsure {
		t.Fatalf("按送到算：%+v", rep)
	}
}

// 带视频的转发（图大）没回声：视频拿出来单独补发，它和转图的转发都是这个原段的补发份，都登记在同一个看守名下。
// 改坏检查（复核 m11）：去掉 settle 里的 noteReplacement，视频那份没登记，这条红（只剩转图的转发一份）
func TestVideoSplitOffIsRegisteredUnderGuard(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, func(n int, p sentPart) echoReply {
		if n == 1 {
			return unconfirmed(f.clock)
		}
		return okSent(fmt.Sprintf("-9000%d", n), f.clock)
	}, nil)
	fw := types.Part{Kind: types.PartForward, Nodes: []types.ForwardNode{
		{Name: "编的作者", Body: []types.BodySegment{{Kind: types.BodyText, Text: "正文"}, {Kind: types.BodyImage, Media: bigPicture(t)}}},
		{Name: "编的作者", Body: []types.BodySegment{{Kind: types.BodyVideo, Media: fileVideo(t)}}},
	}}
	f.deliver(nil, fw)
	if s := f.out.sent(); len(s) != 3 || s[1].kind != types.PartForward || s[2].videos != 1 {
		t.Fatalf("先决条件：转图的转发和单独的视频：%+v", s)
	}
	g := f.r.p.guards.get("p1", 0, f.clock)
	if g == nil {
		t.Fatal("大图没回声，该开着看守")
	}
	g.mu.Lock()
	var got []PartRef
	for _, r := range g.repls {
		got = append(got, r.ref)
	}
	g.mu.Unlock()
	if len(got) != 2 || got[0].PlanID != "p2" || got[1].PlanID != "p3" {
		t.Fatalf("转图的转发（p2）和视频（p3）都记在看守名下：%+v", got)
	}
}

// 到达和补发登记的两种先后，都按固定顺序逐步走，各验补发份恰好撤一次（以前的并发测试逼不出交错，去掉 CompareAndSwap 也绿）。
//
//	次序甲：登记（补发份进了看守的名单）→ 原段到达（名单里看得到它，到达这边去撤）→ 登记那边接着判“该撤了”（也撤）：
//	        两边都看到“该撤”，只有 taken 的 CompareAndSwap 能保证只撤一次。
//	次序乙：原段先到达（名单还是空）→ 再登记：登记这边发现已到达，自己撤。
//
// 改坏检查（复核 m4）：去掉 arrive 里或 settleReplacement 里的 taken.CompareAndSwap，次序甲红（撤了两次）
func TestLateOriginalEchoOrdersEachRecallsOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		registerFirst bool
	}{{"登记在前", true}, {"到达在前", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var f *flow
			f = newFlow(t, bigScript(&f), nil)
			d := &delivery{p: f.r.p, run: &expansionRun{ctx: context.Background()}, conv: g1}
			g := &lateGuard{conv: g1, orig: PartRef{PlanID: "p1", Idx: 0}, until: f.clock.Add(time.Hour)}
			o := PartOutcome{Index: 0, State: OutcomeConfirmed, PlatformID: "-90002", SentAtMS: f.clock.UnixMilli()}
			arrive := func() {
				for _, r := range f.r.p.arrive(g, channel.EchoNote{PlatformID: "-90001", Strong: true}) {
					f.r.p.dropDup(g, r)
				}
			}
			if tc.registerFirst {
				r := d.registerReplacement("p2", o, []*lateGuard{g})
				arrive()
				d.settleReplacement(r, []*lateGuard{g})
			} else {
				arrive()
				r := d.registerReplacement("p2", o, []*lateGuard{g})
				d.settleReplacement(r, []*lateGuard{g})
			}
			if got := f.dupSnapshot(); len(got) != 1 || got[0].dup.PlatformID != "-90002" {
				t.Fatalf("补发份恰好撤一次：%+v", got)
			}
		})
	}
}

// 只凭形状认领的晚到回声（图标识读不出，直接发的消息段）：不撤补发的那份，不翻“被拒”的结论，记一行；
// 补发份在回声之前、之后、同时登记，都一样不撤。
// 改坏检查（复核问题 1）：去掉 replacement.ready 里对 g.strong 的要求，这条红；去掉 markGood 里的 strong 判断，第三格红
func TestWeakLateEchoNeverRecallsAnyOrder(t *testing.T) {
	t.Parallel()
	weak := func(f *flow) {
		e := lateOrig("-90001")
		e.Note.Strong = false
		f.out.hook(e)
	}
	t.Run("补发确认之后才到", func(t *testing.T) {
		t.Parallel()
		var f *flow
		f = newFlow(t, bigScript(&f), nil)
		f.deliver(nil, textAndPics("编的正文", bigPicture(t)))
		weak(f)
		f.waitLate()
		if len(f.dupSnapshot()) != 0 || f.hasGoodVerdict() {
			t.Fatalf("弱认领不撤、不翻结论：%+v", f.dupSnapshot())
		}
		if rows := f.rows(RouteRecall); len(rows) != 1 || rows[0].Kind != kindRecallWeak || rows[0].URL != "https://x.com/a/status/3101" {
			t.Fatalf("记一行“只凭形状认领”：%+v", rows)
		}
	})
	t.Run("补发在途时到", func(t *testing.T) {
		t.Parallel()
		var f *flow
		f = newFlow(t, func(n int, p sentPart) echoReply {
			if n == 2 {
				weak(f)
			}
			return bigScript(&f)(n, p)
		}, nil)
		f.deliver(nil, textAndPics("编的正文", bigPicture(t)))
		f.waitLate()
		if len(f.dupSnapshot()) != 0 {
			t.Fatalf("补发确认时原段已到但是弱认领，不撤：%+v", f.dupSnapshot())
		}
	})
	t.Run("补发之前到：不再补发、算送到，但不翻结论", func(t *testing.T) {
		t.Parallel()
		var f *flow
		f = newFlow(t, bigScript(&f), nil)
		f.out.onGiveUp = func(string, int) { weak(f) }
		rep := f.deliver(nil, textAndPics("编的正文", bigPicture(t)))
		if s := f.out.sent(); len(s) != 1 || !rep.landed {
			t.Fatalf("和按时到的回声一样：不再补发：%d 帧 %+v", len(s), rep)
		}
		if f.hasGoodVerdict() {
			t.Fatal("弱认领不是“到了”的证据，不翻“被拒”的结论")
		}
	})
}
