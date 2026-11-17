package linkplugin

// 2026-11-12 复核插件日志时找到的几处漏记、误记：回执读不到记成 ok、取消时整条消息没有发送行、超限和队列满没有行、
// 补录和重复送到的自发、陌生人等消息多记了“补录”“去重”。号、群、链接都是编的；出口是假的，不碰协议端

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/plugintrace"
)

// fakeOut 是假出口：每个计划立刻终态，回执交给 ExpandOptions.Outcomes
type fakeOut struct{ n atomic.Int32 }

func (f *fakeOut) Submit(context.Context, string, types.Conversation, []types.Part, []*types.MediaResource) (channel.Held, error) {
	done := make(chan struct{})
	close(done)
	return channel.Held{PlanID: fmt.Sprintf("p%d", f.n.Add(1)), Done: done}, nil
}
func (f *fakeOut) CancelPlan(string) {}

// sendingExpand 是真的会走到发送的展开选项：准备结果给一段文字，回执由 outcomes 决定，许可由 permit 决定（nil 是放行）
func sendingExpand(outcomes func(types.Conversation, string) ([]PartOutcome, error), permit error) *ExpandOptions {
	return &ExpandOptions{Outlet: &fakeOut{},
		Admit:  func(channel.Stored) bool { return true },
		Permit: func(channel.Stored, []types.Part) error { return permit },
		Prepare: func(context.Context, channel.Stored, Snapshot, *linkreader.Result, *linkexpand.Draft) (linkexpand.Prepared, error) {
			return linkexpand.Prepared{Parts: []types.Part{{Kind: types.PartMessage, Body: types.TextBody("编的展开")}}}, nil
		},
		Outcomes: outcomes,
		Block:    func(types.Conversation, types.Part, string) {}}
}

func confirmed(types.Conversation, string) ([]PartOutcome, error) {
	return []PartOutcome{{Index: 0, State: OutcomeConfirmed, PlatformID: "m1"}}, nil
}

func sendRows(sink *traceSink) []plugintrace.Event {
	var out []plugintrace.Event
	for _, e := range sink.snapshot() {
		if e.Route == RouteSend {
			out = append(out, e)
		}
	}
	return out
}

// 等到发送行有 n 行（轮询，不靠固定等待）
func waitSendRows(t *testing.T, sink *traceSink, n int) []plugintrace.Event {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if rows := sendRows(sink); len(rows) >= n {
			return rows
		}
		if time.Now().After(deadline) {
			t.Fatalf("等不到 %d 行发送：%+v", n, sink.snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// bug（degrade.go sendGroup）：段提交了、读回执出错时直接 return，没记成结果不明，插件日志记成了 ok。
// 改后这几段算结果不明，记 fail（kind unknown）。放回旧代码就红：记的是 ok
func TestUnreadableReceiptIsUnknown(t *testing.T) {
	t.Parallel()
	bad := func(types.Conversation, string) ([]PartOutcome, error) { return nil, errors.New("库读失败") }
	r, sink := newTraceRig(t, &fakeFetcher{}, Options{Settings: expandSettings(nil), Expand: sendingExpand(bad, nil)})
	r.p.OnStored(storedMsg(11, []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/1101"}}))
	rows := waitSendRows(t, sink, 1)
	if rows[0].Result != plugintrace.Fail || rows[0].Kind != "unknown" {
		t.Fatalf("回执读不到应当记 fail/unknown：%+v", rows)
	}
	// 对照：回执确认的记 ok（假出口本身没问题）
	r2, sink2 := newTraceRig(t, &fakeFetcher{}, Options{Settings: expandSettings(nil), Expand: sendingExpand(confirmed, nil)})
	r2.p.OnStored(storedMsg(12, []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/1102"}}))
	if rows := waitSendRows(t, sink2, 1); rows[0].Result != plugintrace.OK {
		t.Fatalf("确认的应当记 ok：%+v", rows)
	}
}

// bug（degrade.go finishText）：文字版提交了、读回执出错时直接 return，结论是确认送到。改后按结果不明（fail/unknown）。
// 放回旧代码就红：textOdd 没设，记的是 ok
func TestUnreadableTextReceiptIsUnknown(t *testing.T) {
	t.Parallel()
	bad := func(types.Conversation, string) ([]PartOutcome, error) { return nil, errors.New("库读失败") }
	r, sink := newTraceRig(t, &fakeFetcher{}, Options{Settings: expandSettings(nil), Expand: sendingExpand(bad, nil)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := &expansionRun{source: storedMsg(13, nil), ctx: ctx, cancel: cancel}
	d := &delivery{p: r.p, run: run, conv: g1, link: "https://x.com/a/status/1103", quote: "123", excluded: map[string]bool{},
		items: []textItem{{part: types.Part{Kind: types.PartMessage, Body: types.TextBody("编的图说明")}, category: CategoryOther}}}
	d.finishText()
	if !d.textOdd {
		t.Fatal("文字版回执读不到应当按结果不明")
	}
	l, _ := linkreader.Recognize("https://x.com/a/status/1103")
	r.p.traceDelivered(l, nil, time.Now(), d.report(true))
	if rows := sendRows(sink); len(rows) != 1 || rows[0].Result != plugintrace.Fail || rows[0].Kind != "unknown" {
		t.Fatalf("应当记 fail/unknown：%+v", rows)
	}
}

// bug（expand.go 取数后的取消检查）：只为展开的任务取数中途关闭（或源消息撤回）时直接 return，当前和后面的链接都没有发送行。
// 改后各记一行 skip（取消了）。放回旧代码就红：发送行一行都没有
func TestCanceledMidFetchTracesEveryLink(t *testing.T) {
	t.Parallel()
	f := &fakeFetcher{block: make(chan struct{})}
	r, sink := newTraceRig(t, f, Options{Settings: expandSettings(nil), Expand: sendingExpand(confirmed, nil)})
	r.p.OnStored(storedMsg(14, []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/1401 https://x.com/a/status/1402"}}))
	deadline := time.Now().Add(10 * time.Second)
	for f.calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("等不到开始取数")
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.p.Close() // 等后台退出：之后不会再有行
	rows := sendRows(sink)
	if len(rows) != 2 || rows[0].Result != plugintrace.Skip || rows[0].Note != noteCanceled || rows[1].Note != noteCanceled ||
		rows[0].URL != "https://x.com/a/status/1401" || rows[1].URL != "https://x.com/a/status/1402" {
		t.Fatalf("两个链接各记一行 skip（取消了）：%+v", rows)
	}
}

// bug（expand.go 到上限就 break）：超出 MaxLinks 的链接一行都没有。改后各记一行 skip（超出上限）。
// 放回旧代码就红：第二个链接没有行
func TestOverLimitLinksAreTraced(t *testing.T) {
	t.Parallel()
	one := expandSettings(func(e *config.LinkExpand) { e.MaxLinks = 1 })
	r, sink := newTraceRig(t, &fakeFetcher{}, Options{Settings: one, Expand: sendingExpand(confirmed, nil)})
	r.p.OnStored(storedMsg(15, []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/1501 https://x.com/a/status/1502"}}))
	rows := waitSendRows(t, sink, 2)
	r.idle()
	byURL := map[string]plugintrace.Event{}
	for _, e := range rows {
		byURL[e.URL] = e
	}
	if e := byURL["https://x.com/a/status/1502"]; e.Result != plugintrace.Skip || e.Note != noteOverLimit {
		t.Fatalf("超出上限的应当记 skip（超出上限）：%+v", rows)
	}
	if e := byURL["https://x.com/a/status/1501"]; e.Result != plugintrace.OK {
		t.Fatalf("上限内的照常发：%+v", rows)
	}
}

// 许可失败那支以前把超出上限的也记成许可的原因，和正常路径不一致。改后上限内记许可原因、超出的记超限。
// 放回旧代码就红：第二个链接记的是许可原因
func TestPermitFailSplitsOverLimit(t *testing.T) {
	t.Parallel()
	one := expandSettings(func(e *config.LinkExpand) { e.MaxLinks = 1 })
	r, sink := newTraceRig(t, &fakeFetcher{}, Options{Settings: one, Expand: sendingExpand(confirmed, &Blocked{Why: "闸门：QQ 发送已暂停"})})
	r.p.OnStored(storedMsg(16, []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/1601 https://x.com/a/status/1602"}}))
	rows := waitSendRows(t, sink, 2)
	if len(rows) != 2 || rows[0].Note != "闸门：QQ 发送已暂停" || rows[1].Note != noteOverLimit {
		t.Fatalf("上限内记许可原因、超出的记超限：%+v", rows)
	}
}

// bug（plugin.go enqueue）：队列满了丢掉的链接插件日志里没有行（只有 task_runs）。改后能展开的链接各记一行 skip（队列满）。
// 放回旧代码就红：没有这一行
func TestQueueFullLinksAreTraced(t *testing.T) {
	t.Parallel()
	r, sink := newTraceRig(t, &fakeFetcher{block: make(chan struct{})}, Options{Workers: 1, Queue: 1, Settings: expandSettings(nil), Expand: sendingExpand(confirmed, nil)})
	for i := int64(1); i <= 3; i++ {
		r.p.OnStored(storedMsg(20+i, []types.Seg{{Kind: types.SegText, Text: fmt.Sprintf("https://x.com/a/status/17%02d", i)}}))
	}
	if _, ok := sink.find(func(e plugintrace.Event) bool {
		return e.Route == RouteSend && e.Result == plugintrace.Skip && e.Note == noteQueueFull
	}); !ok {
		t.Fatalf("队列满丢掉的应当记 skip（队列满）：%+v", sink.snapshot())
	}
}

// bug（plugin.go OnStored）：补录、重复送到的判断排在自发、陌生人、自检、角色、能否触发的过滤前面，
// 这些本来永远不展开的消息也记了“补录”“去重”。改后一行都不记。放回旧代码就红：每格都有一行
func TestIgnoredNeverExpandableAreNotTraced(t *testing.T) {
	t.Parallel()
	link := []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/1801"}}
	cases := []struct {
		name  string
		admit bool
		s     channel.Stored
	}{
		{"补录 + 自己号发的", true, storedMsg(31, link, channel.TagBackfill, channel.TagFromSelf)},
		{"补录 + 陌生人", true, storedMsg(32, link, channel.TagBackfill, "stranger")},
		{"补录 + 自检", true, storedMsg(33, link, channel.TagBackfill, "selftest")},
		{"补录 + 不能触发", false, storedMsg(34, link, channel.TagBackfill)},
		{"重复送到 + 角色", true, channel.Stored{Character: "c1", Duplicate: true, Seq: 35, Actor: channel.Actor{Kind: channel.ActorCharacter, ID: "c2"}, Inbound: types.Inbound{Conv: g1, Segs: link}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r, sink := newTraceRig(t, &fakeFetcher{}, Options{Settings: expandSettings(nil), Expand: stubExpand(c.admit)})
			r.p.OnStored(c.s)
			if rows := sink.snapshot(); len(rows) != 0 {
				t.Fatalf("本来就不展开的不记：%+v", rows)
			}
		})
	}
}
