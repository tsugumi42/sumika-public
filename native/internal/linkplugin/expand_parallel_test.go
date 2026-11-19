package linkplugin

// 一条消息里多个链接：取数并发、发送按原顺序，前面的发完就发，不等后面取完（用户 2026-11-13）。
// 号、群、链接都是编的；取数是假的，由测试用“闸门”一个一个放行；全程事件同步，不靠固定等待

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/plugintrace"
)

// gateFetcher 的每个链接号对应一道闸门：放行前 Fetch 一直卡着（或卡到 ctx 取消）
type gateFetcher struct {
	fakeFetcher
	mu      sync.Mutex
	gate    map[string]chan struct{}
	started map[string]chan struct{} // Fetch 进来就关
	done    map[string]chan struct{} // Fetch 返回就关
	resolve map[string]string        // 短链号 → 它指向的规范链接（造“不同写法、同一个规范链接”）
	active  atomic.Int32
	begun   atomic.Int32 // 进过闸门的取数一共多少次
}

func newGate(ids ...string) *gateFetcher {
	g := &gateFetcher{gate: map[string]chan struct{}{}, started: map[string]chan struct{}{}, done: map[string]chan struct{}{}, resolve: map[string]string{}}
	for _, id := range ids {
		g.gate[id], g.started[id], g.done[id] = make(chan struct{}), make(chan struct{}), make(chan struct{})
	}
	return g
}

// enter 在闸门前停下：返回 false 是 ctx 先取消了；退出时要调返回的 leave
func (g *gateFetcher) enter(ctx context.Context, id string) (leave func(), ok bool) {
	g.active.Add(1)
	gate, started, done := g.gate[id], g.started[id], g.done[id]
	if started == nil {
		return func() { g.active.Add(-1) }, true
	}
	g.begun.Add(1)
	close(started)
	leave = func() { close(done); g.active.Add(-1) }
	select {
	case <-gate:
		return leave, true
	case <-ctx.Done():
		return leave, false
	}
}

func (g *gateFetcher) Fetch(ctx context.Context, l linkreader.Link) (linkreader.Result, error) {
	leave, ok := g.enter(ctx, l.ID)
	defer leave()
	if !ok {
		return linkreader.Result{}, ctx.Err()
	}
	return g.fakeFetcher.Fetch(ctx, l)
}

// Resolve 给短链用：在闸门前停下，放行后指向 resolve 里写的规范链接
func (g *gateFetcher) Resolve(ctx context.Context, l linkreader.Link) (linkreader.Link, error) {
	id := l.Clean[strings.LastIndex(l.Clean, "/")+1:]
	leave, ok := g.enter(ctx, id)
	defer leave()
	if !ok {
		return linkreader.Link{}, ctx.Err()
	}
	target, found := linkreader.Recognize(g.resolve[id])
	if !found {
		return linkreader.Link{}, fmt.Errorf("测试没给 %s 配目标", id)
	}
	return target, nil
}

func (g *gateFetcher) release(ids ...string) {
	for _, id := range ids {
		close(g.gate[id])
	}
}

func (g *gateFetcher) didStart(id string) bool {
	select {
	case <-g.started[id]:
		return true
	default:
		return false
	}
}

func waitClosed(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("等不到：%s", what)
	}
}

// 这几个链接的取数应当同时在途（旧写法一个取完才取下一个，第二个永远等不到）
func (g *gateFetcher) waitStarted(t *testing.T, ids ...string) {
	t.Helper()
	for _, id := range ids {
		waitClosed(t, "链接 "+id+" 开始取数", g.started[id])
	}
}

func newGateRig(t *testing.T, g *gateFetcher, opt Options) (*rig, *traceSink) {
	t.Helper()
	r := newRig(t, &g.fakeFetcher, opt, g)
	sink := &traceSink{}
	r.p.setTrace(sink.add)
	return r, sink
}

const biliVideo = "https://www.bilibili.com/video/BV1xx411c7mD"

func statusURL(id string) string { return "https://x.com/a/status/" + id }

// 链接号以 s 开头的是 B 站短链，别的是推文
func statusSegs(ids ...string) []types.Seg {
	us := make([]string, len(ids))
	for i, id := range ids {
		us[i] = statusURL(id)
		if strings.HasPrefix(id, "s") {
			us[i] = "https://b23.tv/" + id
		}
	}
	return []types.Seg{{Kind: types.SegText, Text: strings.Join(us, " ")}}
}

func rowsByNote(rows []plugintrace.Event, note string) []plugintrace.Event {
	var out []plugintrace.Event
	for _, e := range rows {
		if e.Note == note {
			out = append(out, e)
		}
	}
	return out
}

// 第 2 个取数被卡住时，第 1 个的结果已经发出；放开以后按 1、2、3 的顺序发完（第 3 个先于第 2 个取好也要等第 2 个）。
// 旧写法：取数一个接一个，卡在第 1 个时第 2、3 个不会开始，测试等不到它们开始
func TestFirstLinkSentWhileLaterOnesStillFetching(t *testing.T) {
	t.Parallel()
	g := newGate("2101", "2102", "2103")
	r, sink := newGateRig(t, g, Options{Settings: expandSettings(nil), Expand: sendingExpand(confirmed, nil)})
	r.p.OnStored(storedMsg(41, statusSegs("2101", "2102", "2103")))
	g.waitStarted(t, "2101", "2102", "2103")
	g.release("2101")
	rows := waitSendRows(t, sink, 1)
	if rows[0].URL != statusURL("2101") || rows[0].Result != plugintrace.OK {
		t.Fatalf("第 1 个应当先发出去：%+v", rows)
	}
	g.release("2103")
	waitClosed(t, "第 3 个取完", g.done["2103"])
	if n := len(sendRows(sink)); n != 1 {
		t.Fatalf("第 2 个还没取好，第 3 个不能抢在它前面发：%+v", sendRows(sink))
	}
	g.release("2102")
	rows = waitSendRows(t, sink, 3)
	r.idle()
	for i, id := range []string{"2101", "2102", "2103"} {
		if rows[i].URL != statusURL(id) || rows[i].Result != plugintrace.OK {
			t.Fatalf("第 %d 行应当是 %s 发成：%+v", i+1, id, rows)
		}
	}
}

// 第 1 个慢、第 2 个快：第 2 个先取好也不抢先，发送顺序仍是 1、2。
// 旧写法：第 1 个卡着时第 2 个不会开始取，等不到
func TestSlowFirstFastSecondStillSendsInOrder(t *testing.T) {
	t.Parallel()
	g := newGate("2201", "2202")
	r, sink := newGateRig(t, g, Options{Settings: expandSettings(nil), Expand: sendingExpand(confirmed, nil)})
	r.p.OnStored(storedMsg(42, statusSegs("2201", "2202")))
	g.waitStarted(t, "2201", "2202")
	g.release("2202")
	waitClosed(t, "第 2 个取完", g.done["2202"])
	if n := len(sendRows(sink)); n != 0 {
		t.Fatalf("第 1 个还没取好，第 2 个不能先发：%+v", sendRows(sink))
	}
	g.release("2201")
	rows := waitSendRows(t, sink, 2)
	r.idle()
	if rows[0].URL != statusURL("2201") || rows[1].URL != statusURL("2202") {
		t.Fatalf("发送顺序应当是 1、2：%+v", rows)
	}
}

// 5 个链接、上限 3，第 2 个取到的规范链接和第 1 个相同：1、3、4 都处理，第 2 个记“去重”，第 5 个没取、记超限；
// 只在发现重复后才去取第 4 个，没有多取（一共取了 4 个）。
// 旧写法：取到第 1 个才取第 2 个，三个初始取数不会同时在途
func TestDuplicateFreesSlotAndNothingExtraIsFetched(t *testing.T) {
	t.Parallel()
	g := newGate("s2301", "s2302", "2303", "2304", "2305")
	g.resolve["s2301"], g.resolve["s2302"] = biliVideo, biliVideo
	r, sink := newGateRig(t, g, Options{Settings: expandSettings(nil), Expand: sendingExpand(confirmed, nil)})
	r.p.OnStored(storedMsg(43, statusSegs("s2301", "s2302", "2303", "2304", "2305")))
	g.waitStarted(t, "s2301", "s2302", "2303")
	if g.didStart("2304") || g.didStart("2305") {
		t.Fatal("还没发现重复，不该多取第 4、5 个")
	}
	g.release("s2301", "s2302", "2303")
	g.waitStarted(t, "2304") // 发现第 2 个重复才空出名额
	g.release("2304")
	rows := waitSendRows(t, sink, 5)
	r.idle()
	if n := len(rowsByNote(rows, noteSameLink)); n != 1 {
		t.Fatalf("重复的应当记一行去重：%+v", rows)
	}
	over := rowsByNote(rows, noteOverLimit)
	if len(over) != 1 || over[0].URL != statusURL("2305") {
		t.Fatalf("第 5 个应当记超限：%+v", rows)
	}
	ok := 0
	for _, e := range rows {
		if e.Result == plugintrace.OK {
			ok++
		}
	}
	if ok != 3 {
		t.Fatalf("三个不同的都应当发成：%+v", rows)
	}
	if g.didStart("2305") || g.begun.Load() != 4 {
		t.Fatalf("一共应当只取 4 个，第 5 个不取（取了 %d）", g.begun.Load())
	}
}

// 取数中撤回源消息：三个在途取数全被取消、不再发送，各记一行取消，没有取数协程留下。
// 旧写法：取数一个接一个，撤回时只有第 1 个在取，等不到另两个开始
func TestRecallCancelsEveryInFlightFetch(t *testing.T) {
	t.Parallel()
	g := newGate("2401", "2402", "2403")
	r, sink := newGateRig(t, g, Options{Settings: expandSettings(nil), Expand: sendingExpand(confirmed, nil)})
	r.p.OnStored(storedMsg(44, statusSegs("2401", "2402", "2403")))
	g.waitStarted(t, "2401", "2402", "2403")
	r.p.Recall(g1, 44)
	rows := waitSendRows(t, sink, 3)
	r.idle()
	rows = sendRows(sink)
	if len(rows) != 3 {
		t.Fatalf("三个链接各一行：%+v", rows)
	}
	for _, e := range rows {
		if e.Result != plugintrace.Skip || e.Note != noteCanceled {
			t.Fatalf("应当都记取消：%+v", rows)
		}
	}
	for _, id := range []string{"2401", "2402", "2403"} {
		waitClosed(t, "取数 "+id+" 退出", g.done[id])
	}
	if n := g.active.Load(); n != 0 {
		t.Fatalf("还有 %d 个取数没退出", n)
	}
}

// 许可中途被撤：已经启动的取数照常取，之后不再启动新的取数（第 4 个不取），没取的记许可的原因。
// 旧写法：三个初始取数不会同时在途
func TestPermitRevokedStopsStartingFetches(t *testing.T) {
	t.Parallel()
	const why = "闸门：测试撤了许可"
	var deny atomic.Bool
	ex := sendingExpand(confirmed, nil)
	ex.Permit = func(channel.Stored, []types.Part) error {
		if deny.Load() {
			return &Blocked{Why: why}
		}
		return nil
	}
	g := newGate("s2501", "s2502", "2503", "2504")
	g.resolve["s2501"], g.resolve["s2502"] = biliVideo, biliVideo
	r, sink := newGateRig(t, g, Options{Settings: expandSettings(nil), Expand: ex})
	r.p.OnStored(storedMsg(45, statusSegs("s2501", "s2502", "2503", "2504")))
	g.waitStarted(t, "s2501", "s2502", "2503")
	g.release("s2501")
	waitSendRows(t, sink, 1) // 第 1 个已发出
	deny.Store(true)
	g.release("s2502", "2503") // 第 2 个重复，本来要空出名额去取第 4 个
	rows := waitSendRows(t, sink, 4)
	r.idle()
	if g.didStart("2504") || g.begun.Load() != 3 {
		t.Fatalf("许可撤了以后不该再启动取数（取了 %d）", g.begun.Load())
	}
	if n := len(rowsByNote(rows, why)); n != 2 {
		t.Fatalf("第 3、4 个应当记许可的原因：%+v", rows)
	}
	if n := len(rowsByNote(rows, noteSameLink)); n != 1 {
		t.Fatalf("第 2 个记去重：%+v", rows)
	}
	if rows[0].Result != plugintrace.OK {
		t.Fatalf("撤之前发出去的第 1 个是成功的：%+v", rows)
	}
}
