package linkplugin

// 大图没回声按没送到换形式以后，原来那份的回声晚到：撤回补发的那份，群里只剩一份（规格-链接发送改版 B15，late.go）。
// 假出口按剧本回执，晚到的回声由测试直接调出口登记的认领回调（和真出口的 ClaimEcho 通知同一个入口）；
// 不睡、不等固定毫秒：撤回协程用插件自己的等待组等，其余在展开的协程里同步完成。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/plugintrace"
)

// bigScript 是常见剧本：带图的第一帧（原图）没回声，转过的图确认（号 m2），文字版确认（号 mt）
func bigScript(f **flow) func(n int, p sentPart) echoReply {
	return func(n int, p sentPart) echoReply {
		switch {
		case n == 1:
			return unconfirmed((*f).clock)
		case len(p.images) > 0:
			return okSent("-90002", (*f).clock)
		}
		return okSent("-90003", (*f).clock)
	}
}

// lateOrig 是原段（计划 p1 第 0 段）的迟到回声
func lateOrig(id string) channel.EchoClaimed {
	return channel.EchoClaimed{PlanID: "p1", Idx: 0, Late: true, Note: channel.EchoNote{PlatformID: id, Account: acct, LatencyMS: 40000, Strong: true}}
}

func (f *flow) waitDup(n int) {
	f.t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-f.dupCalled:
		case <-time.After(10 * time.Second): // 只是防挂死的上限，正常立刻就有信号
			f.t.Fatalf("等 %d 次撤回补发份，只收到 %d 次", n, i)
		}
	}
}

func (f *flow) recallRows() []plugintrace.Event {
	var out []plugintrace.Event
	for _, e := range f.rows(RouteRecall) {
		if e.Note == noteRecallDup {
			out = append(out, e)
		}
	}
	return out
}

func (f *flow) hasGoodVerdict() bool {
	v := &f.r.p.verdicts
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, x := range v.m {
		if x.good == int(formDirect) {
			return true
		}
	}
	return false
}

// 补发（转 180°）已经确认、展开已经结束，原来那份的回声才到：撤回补发的那份（号是补发份的号，不是回声的号），
// 并把“这张图直接发被拒”的结论翻回去；同一个回声再来一次不重复撤。
// 改坏检查：onClaim 里去掉对 lateEcho 的调用，这条红（没有撤回）
func TestLateOriginalEchoRecallsReplacement(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, bigScript(&f), nil)
	rep := f.deliver(nil, textAndPics("编的正文", bigPicture(t)))
	if s := f.out.sent(); len(s) != 2 || !rep.delivered || !rep.landed {
		t.Fatalf("先决条件：原图没回声、转图补发确认：%d 帧 %+v", len(s), rep)
	}
	if len(f.dupSnapshot()) != 0 {
		t.Fatal("原来的回声还没到，不该撤")
	}
	f.out.hook(lateOrig("-90001"))
	f.waitLate()
	got := f.dupSnapshot()
	if len(got) != 1 || got[0].orig != (PartRef{PlanID: "p1", Idx: 0, PlatformID: "-90001"}) || got[0].dup != (PartRef{PlanID: "p2", Idx: 0, PlatformID: "-90002"}) {
		t.Fatalf("撤的是补发那份（p2 的 m2），原段的回声号 m1 只是凭据：%+v", got)
	}
	if len(f.recalls) != 0 {
		t.Fatalf("吞图那条撤回（Recall）不该动：%v", f.recalls)
	}
	if rows := f.recallRows(); len(rows) != 1 || rows[0].Result != plugintrace.OK || rows[0].URL != "https://x.com/a/status/3101" {
		t.Fatalf("撤回记一行：%+v", rows)
	}
	if !f.hasGoodVerdict() {
		t.Fatal("原图到了，“被拒”的结论要翻回去（不然 6 小时内这张图都不再原样发）")
	}
	f.out.hook(lateOrig("-90001")) // 同一个回声重推
	f.waitLate()
	if len(f.dupSnapshot()) != 1 {
		t.Fatalf("一份只撤一次：%+v", f.dupSnapshot())
	}
}

// 原来的回声在等满之后、换形式发出去之前到了（AwaitPart 放弃的一瞬间）：不再补发，算送到。
// 改坏检查：去掉阶梯里每一级前的 originalArrived，这条红（还是发了第二帧）
func TestLateOriginalEchoBeforeReplacementStopsLadder(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, bigScript(&f), nil)
	f.out.onGiveUp = func(string, int) { f.out.hook(lateOrig("-90001")) }
	rep := f.deliver(nil, textAndPics("编的正文", bigPicture(t)))
	if s := f.out.sent(); len(s) != 1 {
		t.Fatalf("回声在换形式之前到了，不该再发：%d 帧 %+v", len(s), s)
	}
	f.waitLate()
	if !rep.delivered || !rep.landed || rep.unsure || rep.fallback {
		t.Fatalf("按送到算：%+v", rep)
	}
	if len(f.dupSnapshot()) != 0 {
		t.Fatalf("没有补发就没有要撤的：%+v", f.dupSnapshot())
	}
	if !f.hasGoodVerdict() {
		t.Fatal("原图到了，结论要是好的")
	}
}

// 补发在途时（已提交、回执还没读）回声到了：补发一确认就当场撤它（在展开自己的协程里同步撤，不靠回调）。
// 改坏检查：去掉 attachReplacement 里补发确认时的 ready 判断，这条红（没撤）
func TestLateOriginalEchoWhileReplacementInFlight(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, func(n int, p sentPart) echoReply {
		if n == 2 {
			f.out.hook(lateOrig("-90001")) // 提交补发的这一刻，原来的回声推回来了
		}
		return bigScript(&f)(n, p)
	}, nil)
	f.deliver(nil, textAndPics("编的正文", bigPicture(t)))
	f.waitLate()
	got := f.dupSnapshot()
	if len(f.out.sent()) != 2 || len(got) != 1 || got[0].dup.PlatformID != "-90002" {
		t.Fatalf("补发确认时看到回声已经到了，撤它：%d 帧 %+v", len(f.out.sent()), got)
	}
}

// 到达和补发登记同时发生：不管谁先，补发份恰好撤一次（-race 下连跑）
func TestLateOriginalEchoRaceRecallsExactlyOnce(t *testing.T) {
	t.Parallel()
	for i := 0; i < 12; i++ {
		var f *flow
		var wg sync.WaitGroup
		f = newFlow(t, func(n int, p sentPart) echoReply {
			if n == 2 {
				wg.Add(1)
				go func() { defer wg.Done(); f.out.hook(lateOrig("-90001")) }()
			}
			return bigScript(&f)(n, p)
		}, nil)
		f.deliver(nil, textAndPics("编的正文", bigPicture(t)))
		wg.Wait()
		f.waitLate()
		if got := f.dupSnapshot(); len(got) != 1 {
			t.Fatalf("第 %d 轮：补发份该恰好撤一次：%+v", i, got)
		}
	}
}

// 合并转发的大图（节点里的图合计 ≥ 2 MiB）：原转发没回声，转 180° 的转发补发，原来的回声晚到：
// 转发段的认领不比内容（同会话同发送号的任何转发形回声都认得到），不是强匹配，所以不撤补发的转发，只记一行（复核问题 1）。
// 改坏检查：去掉 replacement.ready 里对 g.strong 的要求，这条红（撤了）
func TestLateOriginalEchoOfForwardNeverRecalls(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, bigScript(&f), nil)
	fw := types.Part{Kind: types.PartForward, Nodes: []types.ForwardNode{{Name: "编的作者", Body: []types.BodySegment{{Kind: types.BodyText, Text: "字"}, {Kind: types.BodyImage, Media: bigPicture(t)}}}}}
	rep := f.deliver(nil, fw)
	if s := f.out.sent(); len(s) != 2 || s[1].kind != types.PartForward || !rep.landed {
		t.Fatalf("先决条件：转发没回声、转 180° 的转发补发确认：%+v %+v", s, rep)
	}
	weak := lateOrig("-90001")
	weak.Note.Strong = false // 真出口认领转发段时永远是这个样子（channel 的 TestEchoClaimStrongness）
	f.out.hook(weak)
	f.waitLate()
	if got := f.dupSnapshot(); len(got) != 0 {
		t.Fatalf("转发段的认领不是强匹配，不撤：%+v", got)
	}
	if rows := f.recallRows(); len(rows) != 0 {
		t.Fatalf("没有撤回就不记撤回那一行：%+v", rows)
	}
	if rows := f.rows(RouteRecall); len(rows) != 1 || rows[0].Kind != kindRecallWeak {
		t.Fatalf("记一行“只凭形状认领”：%+v", rows)
	}
}

// 第一次发送就发生在阶梯里：同一张大图第一次没回声记了“直接发被拒”，再发时直接从转 180° 起步。
// 这一级没回声，它就是被看守的原段，退下来的文字版是补发份：转图的回声晚到，撤文字版（看守的键和形式用阶梯的，不是转过的图算的）。
// 改坏检查：去掉 ladder 里接过 o.guard 的那几行，这条红（文字版没撤）
func TestLateEchoOfRotatedStartingRungRecallsTextVersion(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, func(n int, p sentPart) echoReply {
		switch {
		case n == 1 || n == 3:
			return unconfirmed(f.clock) // 第一次的原图、第二次起步的转图：都没回声
		case n == 2:
			return okSent("-90002", f.clock) // 第一次的转图确认了
		}
		return okSent("-90004", f.clock) // 第二次的文字版
	}, nil)
	f.deliver(nil, textAndPics("编的正文", bigPicture(t)))
	rep := f.deliver(nil, textAndPics("编的正文", bigPicture(t)))
	s := f.out.sent()
	if len(s) != 4 || len(s[2].images) != 1 || len(s[3].images) != 0 || !rep.fallback {
		t.Fatalf("先决条件：第二次从转图起步、没回声、退文字版：%d 帧 %+v %+v", len(s), s, rep)
	}
	f.out.hook(channel.EchoClaimed{PlanID: "p3", Idx: 0, Late: true, Note: channel.EchoNote{PlatformID: "-90005", Account: acct, LatencyMS: 40000, Strong: true}})
	f.waitLate()
	got := f.dupSnapshot()
	if len(got) != 1 || got[0].orig.PlanID != "p3" || got[0].dup != (PartRef{PlanID: "p4", Idx: 0, PlatformID: "-90004"}) {
		t.Fatalf("起步那一级的回声晚到，撤文字版：%+v", got)
	}
}

// 补发的是文字版（图转不了）：回声晚到，撤回文字版
func TestLateOriginalEchoRecallsTextVersion(t *testing.T) {
	t.Parallel()
	huge := types.MemoryMedia("huge", "image/png", 4961, 7016, make([]byte, 5<<20)) // 不是合法 PNG，转不了，直接文字版
	var f *flow
	f = newFlow(t, bigScript(&f), nil)
	rep := f.deliver(nil, textAndPics("编的正文", huge))
	if s := f.out.sent(); len(s) != 2 || len(s[1].images) != 0 || !rep.fallback {
		t.Fatalf("先决条件：原图没回声，转不了，文字版：%+v %+v", s, rep)
	}
	f.out.hook(lateOrig("-90001"))
	f.waitLate()
	if got := f.dupSnapshot(); len(got) != 1 || got[0].dup != (PartRef{PlanID: "p2", Idx: 0, PlatformID: "-90003"}) {
		t.Fatalf("文字版也是后发的那份，撤它：%+v", got)
	}
}

// 补发份装着不是这个原段的内容（别的原段还没到、或有不受看守的内容）时不撤：撤掉会丢内容
func TestReplacementWithOtherContentIsNotRecalled(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, bigScript(&f), nil)
	d := &delivery{p: f.r.p, run: &expansionRun{ctx: context.Background()}, conv: g1}
	a := &lateGuard{conv: g1, orig: PartRef{PlanID: "p1", Idx: 0}, until: f.clock.Add(time.Hour)}
	b := &lateGuard{conv: g1, orig: PartRef{PlanID: "p1", Idx: 1}, until: f.clock.Add(time.Hour)}
	o := PartOutcome{Index: 0, State: OutcomeConfirmed, PlatformID: "-90003", SentAtMS: f.clock.UnixMilli()}
	d.attachReplacement("p9", o, []*lateGuard{a, b})
	if rs := f.r.p.arrive(a, channel.EchoNote{PlatformID: "e1", Strong: true}); len(rs) != 0 {
		t.Fatalf("两个原段只到了一个，文字版不撤：%d", len(rs))
	}
	if rs := f.r.p.arrive(b, channel.EchoNote{PlatformID: "e2", Strong: true}); len(rs) != 1 {
		t.Fatalf("两个原段都到了才撤：%d", len(rs))
	}
	// 里面有不受看守的内容（nil）：永远不撤
	c := &lateGuard{conv: g1, orig: PartRef{PlanID: "p1", Idx: 2}, until: f.clock.Add(time.Hour)}
	d.attachReplacement("p9", o, []*lateGuard{c, nil})
	if rs := f.r.p.arrive(c, channel.EchoNote{PlatformID: "e3", Strong: true}); len(rs) != 0 || len(f.dupSnapshot()) != 0 {
		t.Fatalf("有不受看守的内容，不撤：%d %+v", len(rs), f.dupSnapshot())
	}
}

// 撤回的门槛：补发份没有号、号不规范、和回声同号、超过 2 分钟、渠道不能撤，都不调宿主，只记失败一行；调了失败也只记日志
func TestLateRecallGatesAndFailureOnlyLog(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_800_000_000, 0)
	for name, tc := range map[string]struct {
		id     string
		sentAt time.Time
		echo   string
		noFn   bool
		callEr error
		called bool
	}{
		"撤回失败":    {id: "-90002", sentAt: base, echo: "-90001", callEr: errors.New("message not found"), called: true},
		"号为空":     {id: "", sentAt: base, echo: "-90001"},
		"号为 0":    {id: "0", sentAt: base, echo: "-90001"},
		"号塞了控制字符": {id: "m\n2", sentAt: base, echo: "-90001"},
		"和回声同号":   {id: "-90050", sentAt: base, echo: "-90050"},
		"超过两分钟":   {id: "-90051", sentAt: base.Add(-RecallWithin - time.Millisecond), echo: "-90001"},
		"没有发送时刻":  {id: "-90052", echo: "-90001"},
		"渠道不能撤":   {id: "-90053", sentAt: base, echo: "-90001", noFn: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var f *flow
			f = newFlow(t, func(n int, p sentPart) echoReply {
				switch {
				case n == 1:
					return unconfirmed(base)
				case len(p.images) > 0:
					r := okSent(tc.id, tc.sentAt)
					if tc.sentAt.IsZero() {
						r.out.SentAtMS = 0
					}
					return r
				}
				return okSent("-90003", base)
			}, nil)
			f.clock = base
			f.dupErr = tc.callEr
			if tc.noFn {
				f.r.p.opt.Expand.RecallDup = nil
			}
			f.deliver(nil, textAndPics("编的正文", bigPicture(t)))
			f.out.hook(lateOrig(tc.echo))
			f.waitLate()
			if got := len(f.dupSnapshot()); (got == 1) != tc.called {
				t.Fatalf("调宿主的次数不对（该调 %v）：%d", tc.called, got)
			}
			rows := f.recallRows()
			if len(rows) != 1 || rows[0].Result != plugintrace.Fail {
				t.Fatalf("没撤掉记一行 fail：%+v", rows)
			}
		})
	}
}

// 补发的那份结果不明、没有号：撤不了，只记日志（不乱撤别的）
func TestReplacementUnknownCannotBeRecalled(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, func(n int, p sentPart) echoReply {
		if n == 1 {
			return unconfirmed(f.clock)
		}
		return echoReply{out: PartOutcome{State: OutcomeUnknown, SentAtMS: f.clock.UnixMilli()}} // 没回包也没说要等回声（比如没配号）
	}, nil)
	rep := f.deliver(nil, textAndPics("编的正文", bigPicture(t)))
	if !rep.unsure || rep.landed {
		t.Fatalf("补发结果不明、没有一段确认：%+v", rep)
	}
	f.out.hook(lateOrig("-90001"))
	f.waitLate()
	if len(f.dupSnapshot()) != 0 {
		t.Fatalf("没有号撤不了：%+v", f.dupSnapshot())
	}
}

// 迟到的回声只剩文字（图被吞）：原段不算到了，补发的那份有图，留着
func TestLateMediaMissingKeepsReplacement(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, bigScript(&f), nil)
	f.deliver(nil, textAndPics("编的正文", bigPicture(t)))
	e := lateOrig("-90001")
	e.Note.MediaMissing = true
	f.out.hook(e)
	f.waitLate()
	if len(f.dupSnapshot()) != 0 || f.hasGoodVerdict() {
		t.Fatalf("图被吞的回声不算原段到了：%+v", f.dupSnapshot())
	}
}

// 小图（不到 2 MiB）不开看守：行为不变，迟到的回声只记日志，不撤回已补发的（规格 A 节，用户接受的取舍）。
// 改坏检查：把 confirm 里的 big 条件去掉，这条红（小图也撤了）
func TestSmallImageLateEchoNeverRecalls(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		if n == 1 {
			return unconfirmed(f.clock)
		}
		return okSent("-90002", f.clock)
	}, nil)
	f.deliver(nil, textAndPics("编的正文", picture(t, 2)))
	f.out.hook(lateOrig("-90001"))
	f.waitLate()
	if len(f.dupSnapshot()) != 0 || len(f.recalls) != 0 {
		t.Fatalf("小图不撤：%+v %v", f.dupSnapshot(), f.recalls)
	}
}

// 协议端断线（发不出去）：原图没回声，转图和文字版都没发出去：整条失败（贴失败表情，不是“完成”），不留结果不明
func TestDisconnectedProtocolFailsEverything(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		if n == 1 {
			return unconfirmed(f.clock)
		}
		return echoReply{out: PartOutcome{State: OutcomeFailed, Category: CategoryOther}}
	}, nil)
	rep := f.deliver(nil, textAndPics("编的正文", bigPicture(t)))
	if rep.delivered || rep.landed {
		t.Fatalf("什么都没发出去：不是送到，也不是完成：%+v", rep)
	}
}

// 表情：结果不明且一段都没确认送到时，既不算 delivered 的“完成”，报告里 landed 为假（expand_flow 据此不贴完成）；
// 有一段确认就是 landed。改坏检查：report() 里 landed 恒为真，这条红
func TestReportLandedOnlyWhenSomethingConfirmed(t *testing.T) {
	t.Parallel()
	unknownOnly := newFlow(t, func(int, sentPart) echoReply {
		return echoReply{out: PartOutcome{State: OutcomeUnknown}}
	}, nil)
	rep := unknownOnly.deliver(nil, textAndPics("编的正文", picture(t, 2)))
	if !rep.delivered || !rep.unsure || rep.landed {
		t.Fatalf("只有结果不明：delivered 但没有 landed：%+v", rep)
	}
	confirmed := newFlow(t, func(int, sentPart) echoReply { return ok("-90001") }, nil)
	if rep := confirmed.deliver(nil, textAndPics("编的正文", picture(t, 2))); !rep.landed || rep.unsure {
		t.Fatalf("确认了就是 landed：%+v", rep)
	}
}
