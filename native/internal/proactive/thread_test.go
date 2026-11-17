package proactive

import (
	"errors"
	"testing"
	"time"
)

// 话题线的状态机（规格-主动联系 第十六节第 3 条）：状态 × 事件穷举，每格写明预期；没关的状态必有截止时间；
// 热度、认领判断、续醒时间。角色、会话、时间都是编的。

func th(state ThreadState, f func(*Thread)) Thread {
	t := Thread{ID: "t1", Character: "aa", Conversation: "console:aa:topic:thread-t1", State: state, OpenedAt: at(9, 14, 0), LastWakeAt: at(9, 14, 0)}
	if f != nil {
		f(&t)
	}
	return t
}

func sentOnce(t *Thread) {
	t.Sends, t.FirstSendAt, t.LastSendAt, t.AwaitingSince = 1, at(9, 14, 5), at(9, 14, 5), at(9, 14, 5)
}

type want struct {
	state  ThreadState
	reason CloseReason
	err    error
}

func ok(s ThreadState) want                            { return want{state: s} }
func closed(r CloseReason) want                        { return want{state: ThreadClosed, reason: r} }
func bad(s ThreadState, e error) want                  { return want{state: s, err: e} }
func same(s ThreadState, e error) want                 { return bad(s, e) }
func table(rows map[EventKind]want) map[EventKind]want { return rows }

func TestApplyEveryStateEveryEvent(t *testing.T) {
	p := params()
	variants := []struct {
		name string
		t    Thread
		rows map[EventKind]want
	}{
		{"想着、还没发过话", th(ThreadThinking, nil), table(map[EventKind]want{
			EvSent: ok(ThreadAwaiting), EvTurnEnd: closed(ClosedQuiet), EvTurnError: closed(ClosedError),
			EvWake: same(ThreadThinking, ErrImpossible), EvClaim: same(ThreadThinking, ErrImpossible), EvOverheard: same(ThreadThinking, ErrImpossible),
			EvTimeout: closed(ClosedError), EvEnd: closed(ClosedQuiet), EvSnooze: closed(ClosedSnoozed), EvCap: same(ThreadThinking, ErrImpossible),
			EvRestart: closed(ClosedError),
		})},
		{"想着、已发过话（续醒）", th(ThreadThinking, func(x *Thread) { sentOnce(x); x.FollowUps = 1; x.LastWakeAt = at(9, 14, 30) }), table(map[EventKind]want{
			EvSent: ok(ThreadAwaiting), EvTurnEnd: ok(ThreadAwaiting), EvTurnError: closed(ClosedError),
			EvWake: same(ThreadThinking, ErrImpossible), EvClaim: ok(ThreadChatting), EvOverheard: ok(ThreadThinking),
			EvTimeout: closed(ClosedError), EvEnd: closed(ClosedDone), EvSnooze: closed(ClosedSnoozed), EvCap: closed(ClosedCap),
			EvRestart: ok(ThreadAwaiting),
		})},
		{"等回复、他没回过", th(ThreadAwaiting, sentOnce), table(map[EventKind]want{
			EvSent: ok(ThreadAwaiting), EvTurnEnd: same(ThreadAwaiting, ErrNoop), EvTurnError: same(ThreadAwaiting, ErrNoop),
			EvWake: ok(ThreadThinking), EvClaim: ok(ThreadChatting), EvOverheard: ok(ThreadAwaiting),
			EvTimeout: closed(ClosedNoReply), EvEnd: closed(ClosedDone), EvSnooze: closed(ClosedSnoozed), EvCap: closed(ClosedCap),
			EvRestart: same(ThreadAwaiting, ErrNoop),
		})},
		{"等回复、他回过", th(ThreadAwaiting, func(x *Thread) { sentOnce(x); x.OwnerReplies = 1 }), table(map[EventKind]want{
			EvSent: ok(ThreadAwaiting), EvTurnEnd: same(ThreadAwaiting, ErrNoop), EvTurnError: same(ThreadAwaiting, ErrNoop),
			EvWake: ok(ThreadThinking), EvClaim: ok(ThreadChatting), EvOverheard: ok(ThreadAwaiting),
			EvTimeout: closed(ClosedDone), EvEnd: closed(ClosedDone), EvSnooze: closed(ClosedSnoozed), EvCap: closed(ClosedCap),
			EvRestart: same(ThreadAwaiting, ErrNoop),
		})},
		{"聊着", th(ThreadChatting, func(x *Thread) { sentOnce(x); x.OwnerReplies = 1; x.ChatLastAt = at(9, 14, 10) }), table(map[EventKind]want{
			EvSent: ok(ThreadAwaiting), EvTurnEnd: same(ThreadChatting, ErrNoop), EvTurnError: same(ThreadChatting, ErrNoop),
			EvWake: same(ThreadChatting, ErrImpossible), EvClaim: ok(ThreadChatting), EvOverheard: ok(ThreadChatting),
			EvTimeout: closed(ClosedDone), EvEnd: closed(ClosedDone), EvSnooze: closed(ClosedSnoozed), EvCap: closed(ClosedCap),
			EvRestart: same(ThreadChatting, ErrNoop),
		})},
		{"已关", th(ThreadClosed, func(x *Thread) { sentOnce(x); x.CloseReason = ClosedDone; x.ClosedAt = at(9, 15, 0) }), func() map[EventKind]want {
			m := map[EventKind]want{}
			for _, ev := range AllEvents {
				m[ev] = same(ThreadClosed, ErrClosed)
			}
			return m
		}()},
	}
	for _, v := range variants {
		for _, ev := range AllEvents {
			w, found := v.rows[ev]
			if !found {
				t.Errorf("%s × %s：表里没写预期（每一格都要写）", v.name, ev)
				continue
			}
			e := Event{Kind: ev, At: at(9, 14, 40)}
			if ev == EvTimeout {
				e.At = CloseAt(p, v.t).Add(time.Second) // 超时事件要在截止时间之后
				if v.t.State == ThreadClosed {
					e.At = at(9, 23, 0)
				}
			}
			got, err := Apply(p, v.t, e)
			if !errors.Is(err, w.err) || (w.err == nil && err != nil) {
				t.Errorf("%s × %s：错误 %v，想要 %v", v.name, ev, err, w.err)
				continue
			}
			if got.State != w.state || (w.reason != "" && got.CloseReason != w.reason) {
				t.Errorf("%s × %s：得到 %s/%s，想要 %s/%s", v.name, ev, got.State, got.CloseReason, w.state, w.reason)
			}
			if w.err != nil && !equalThread(got, v.t) {
				t.Errorf("%s × %s：返回错误时线应原样不变", v.name, ev)
			}
			if got.State == ThreadClosed && w.err == nil && got.ClosedAt.IsZero() {
				t.Errorf("%s × %s：关了要记关闭时间", v.name, ev)
			}
		}
	}
}

func equalThread(a, b Thread) bool {
	return a.State == b.State && a.Sends == b.Sends && a.FollowUps == b.FollowUps && a.OwnerReplies == b.OwnerReplies &&
		a.CloseReason == b.CloseReason && a.LastSendAt.Equal(b.LastSendAt) && a.ChatLastAt.Equal(b.ChatLastAt)
}

func TestApplyTimeoutTooEarlyIsNoop(t *testing.T) {
	p := params()
	for _, x := range []Thread{
		th(ThreadThinking, nil),
		th(ThreadAwaiting, sentOnce),
		th(ThreadChatting, func(x *Thread) { sentOnce(x); x.OwnerReplies = 1; x.ChatLastAt = at(9, 14, 10) }),
	} {
		_, err := Apply(p, x, Event{Kind: EvTimeout, At: CloseAt(p, x).Add(-time.Second)})
		if !errors.Is(err, ErrNoop) {
			t.Errorf("%s：没到截止就超时应当无事可做，得到 %v", x.State, err)
		}
	}
}

func TestEveryOpenStateHasADeadline(t *testing.T) {
	p := params()
	for _, x := range []Thread{
		th(ThreadThinking, nil),
		th(ThreadAwaiting, sentOnce),
		th(ThreadAwaiting, func(x *Thread) { sentOnce(x); x.OwnerReplies = 1; x.FollowUps = 2 }),
		th(ThreadChatting, func(x *Thread) { sentOnce(x); x.OwnerReplies = 1; x.ChatLastAt = at(9, 14, 10) }),
	} {
		c := CloseAt(p, x)
		if c.IsZero() || !c.After(x.OpenedAt) {
			t.Errorf("%s：没关的线必须有截止时间，得到 %v", x.State, c)
		}
		n, okk := NextCheck(p, x)
		if !okk || n.IsZero() || n.After(c) {
			t.Errorf("%s：下次要看的时间不能晚于截止：%v %v", x.State, n, c)
		}
	}
	if _, okk := NextCheck(p, th(ThreadClosed, nil)); okk {
		t.Fatal("关了的线不需要再看")
	}
	if !CloseAt(p, th(ThreadClosed, nil)).IsZero() {
		t.Fatal("关了的线没有截止时间")
	}
}

func TestDeadlineValues(t *testing.T) {
	p := params()
	a := th(ThreadAwaiting, sentOnce)
	if got, want := CloseAt(p, a), at(9, 14, 5).Add(6*time.Hour); !got.Equal(want) {
		t.Errorf("没回等 closeAfterHours：%v，想要 %v", got, want)
	}
	a.OwnerReplies = 1
	if got, want := CloseAt(p, a), at(9, 14, 5).Add(10*time.Minute); !got.Equal(want) {
		t.Errorf("聊过又没下文等 chatIdle：%v，想要 %v", got, want)
	}
	c := th(ThreadChatting, func(x *Thread) { sentOnce(x); x.ChatLastAt = at(9, 14, 20) })
	if got, want := CloseAt(p, c), at(9, 14, 30); !got.Equal(want) {
		t.Errorf("聊着：%v，想要 %v", got, want)
	}
	k := th(ThreadThinking, nil)
	if got, want := CloseAt(p, k), at(9, 14, 30); !got.Equal(want) {
		t.Errorf("想着：%v，想要 %v", got, want)
	}
}

func TestFollowUpAt(t *testing.T) {
	p := params() // followUp 20 分钟，最多 2 次
	a := th(ThreadAwaiting, sentOnce)
	if f, okk := FollowUpAt(p, a); !okk || !f.Equal(at(9, 14, 25)) {
		t.Fatalf("发话后 20 分钟续醒：%v %v", f, okk)
	}
	if FollowUpDue(p, a, at(9, 14, 24)) || !FollowUpDue(p, a, at(9, 14, 25)) {
		t.Fatal("续醒边界：差 1 分钟不到，正好到点就到")
	}
	a.FollowUps, a.LastWakeAt = 1, at(9, 14, 25)
	if f, _ := FollowUpAt(p, a); !f.Equal(at(9, 14, 45)) {
		t.Fatalf("第二次从上次醒起算：%v", f)
	}
	a.FollowUps = 2
	if _, okk := FollowUpAt(p, a); okk {
		t.Fatal("续醒次数用完不再续醒")
	}
	a.FollowUps, a.OwnerReplies = 0, 1
	if _, okk := FollowUpAt(p, a); okk {
		t.Fatal("他回过（认领的或旁听的）就取消续醒")
	}
	if _, okk := FollowUpAt(p, th(ThreadThinking, sentOnce)); okk {
		t.Fatal("只有等回复才续醒")
	}
	if _, okk := FollowUpAt(p, th(ThreadAwaiting, nil)); okk {
		t.Fatal("没发过话没有续醒")
	}
}

func TestHeatOf(t *testing.T) {
	mk := func(reason CloseReason, f func(*Thread)) Thread {
		x := th(ThreadClosed, func(x *Thread) { sentOnce(x); x.CloseReason = reason; x.ClosedAt = at(9, 18, 0) })
		if f != nil {
			f(&x)
		}
		return x
	}
	for _, c := range []struct {
		name string
		t    Thread
		kind CloseKind
		heat Heat
	}{
		{"没回是冷", mk(ClosedNoReply, nil), CloseCold, HeatCold},
		{"10 分钟内回且聊了两轮是热", mk(ClosedDone, func(x *Thread) { x.OwnerReplies = 2; x.FirstReplyAt = x.FirstSendAt.Add(10 * time.Minute) }), CloseHot, HeatHot},
		{"快回但只回了一次是温", mk(ClosedDone, func(x *Thread) { x.OwnerReplies = 1; x.FirstReplyAt = x.FirstSendAt.Add(2 * time.Minute) }), CloseWarm, HeatWarm},
		{"聊了两轮但第一次回得慢是温", mk(ClosedDone, func(x *Thread) { x.OwnerReplies = 3; x.FirstReplyAt = x.FirstSendAt.Add(11 * time.Minute) }), CloseWarm, HeatWarm},
		{"想了没开口不动系数", mk(ClosedQuiet, func(x *Thread) { x.Sends = 0 }), CloseQuiet, HeatNone},
		{"别烦", mk(ClosedSnoozed, nil), CloseSnoozed, HeatNone},
		{"出错不算冷", mk(ClosedError, nil), CloseQuiet, HeatNone},
		{"没真发过话（发送全失败）不算冷", mk(ClosedNoReply, func(x *Thread) { x.Sends = 0 }), CloseQuiet, HeatNone},
		{"发满了上限回过的算温", mk(ClosedCap, func(x *Thread) { x.OwnerReplies = 1; x.FirstReplyAt = x.FirstSendAt.Add(time.Hour) }), CloseWarm, HeatWarm},
	} {
		k, h := HeatOf(c.t)
		if k != c.kind || h != c.heat {
			t.Errorf("%s：得到 %v/%q，想要 %v/%q", c.name, k, h, c.kind, c.heat)
		}
	}
}

func TestClaimAllowedBoundaries(t *testing.T) {
	p := params() // 窗口 10 分钟
	conv := "console:aa"
	base := th(ThreadAwaiting, sentOnce) // 14:05 发的话
	now := func(m int) time.Time { return at(9, 14, 5).Add(time.Duration(m) * time.Minute) }
	facts := ClaimFacts{SentHere: true, ThreadMsgLatest: true}
	for _, c := range []struct {
		name  string
		t     Thread
		f     ClaimFacts
		conv  string
		now   time.Time
		allow bool
	}{
		{"窗口内、线的话仍是最近一条", base, facts, conv, now(9), true},
		{"正好 10 分钟还认", base, facts, conv, now(10), true},
		{"超过 10 分钟不认", base, facts, conv, now(11), false},
		{"普通实例已经接话（线的话不是最近一条）不认", base, ClaimFacts{SentHere: true}, conv, now(3), false},
		{"窗口外但引用了线的话认", base, ClaimFacts{SentHere: true, QuotedThreadMsg: true}, conv, now(300), true},
		{"线没在这个会话发过话不认", base, ClaimFacts{ThreadMsgLatest: true}, conv, now(3), false},
		{"hand_back 拉黑的会话不认（即使引用）", func() Thread { x := base; x.BlockedConvs = []string{conv}; return x }(), ClaimFacts{SentHere: true, QuotedThreadMsg: true}, conv, now(3), false},
		{"聊着时同一会话的话继续认（不看时间）", th(ThreadChatting, func(x *Thread) { sentOnce(x); x.OwnerReplies = 1; x.ChatLastAt = at(9, 14, 8) }), ClaimFacts{SentHere: true}, conv, now(200), true},
		{"线关了不认", th(ThreadClosed, sentOnce), facts, conv, now(1), false},
		{"没发过话不认", th(ThreadThinking, nil), facts, conv, now(1), false},
		{"时钟倒退（主人这句早于线的话）不认", base, facts, conv, now(-1), false},
	} {
		if got := ClaimAllowed(p, c.t, c.conv, c.f, c.now); got != c.allow {
			t.Errorf("%s：得到 %v，想要 %v", c.name, got, c.allow)
		}
	}
}

func TestThreadSnapshotText(t *testing.T) {
	now := at(9, 15, 0)
	a := th(ThreadAwaiting, sentOnce)
	for _, c := range []struct {
		t    Thread
		want string
	}{
		{th(ThreadThinking, nil), "正在想"},
		{a, "等回复 55 分钟"},
		{th(ThreadChatting, nil), "聊着"},
		{th(ThreadClosed, func(x *Thread) { x.CloseReason = ClosedNoReply }), "已关：没等到回复"},
	} {
		if got := ThreadSnapshot(c.t, now); got != c.want {
			t.Errorf("得到 %q，想要 %q", got, c.want)
		}
	}
}
