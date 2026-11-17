package main

// 看了就标已读的测试（readmark.go）：条件四种（前台到底、前台在中间、后台、archive）加失败、没未读、换会话、重复。
// 数据全是编的；后台是 markReader（记下每次调用，不碰库）。

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
)

type markCall struct {
	key  api.ConversationKey
	upTo int64
}

// markReader 是肯标已读的数据源：记下每次调用，err 不空就按它回；hold 不为 nil 时等它关了才回（测换会话）
type markReader struct {
	demoReader
	mu    sync.Mutex
	calls []markCall
	err   error
	got   chan struct{}
	hold  chan struct{}
}

func (r *markReader) MarkRead(_ context.Context, key api.ConversationKey, upTo int64) error {
	r.mu.Lock()
	r.calls = append(r.calls, markCall{key, upTo})
	err, hold := r.err, r.hold
	r.mu.Unlock()
	r.got <- struct{}{}
	if hold != nil {
		<-hold
	}
	return err
}

func (r *markReader) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// wait 等到至少 n 次调用
func (r *markReader) wait(t *testing.T, n int) {
	t.Helper()
	for r.count() < n {
		select {
		case <-r.got:
		case <-time.After(uiWaitLimit):
			t.Fatalf("等了 5 秒，只调用了 %d 次，应有 %d 次", r.count(), n)
		}
	}
}

const markConv = "qq:group:1001"

// markEnv 一个读 markReader 的生产模式 app：markConv 会话里 n 条消息，会话列表里它有 unread 条未读；窗口一开始在后台（front 才切到前台），免得建好的头几帧就标了
type markEnv struct {
	a     *app
	r     *markReader
	tt    *ui.Tester
	queue chan func()
	logs  []string
}

func newMarkEnv(t *testing.T, n, unread int) *markEnv {
	t.Helper()
	e := &markEnv{r: &markReader{got: make(chan struct{}, 64)}, queue: make(chan func(), 64)}
	a := chatApp(chain(t, n, -1, 0), true)
	a.demoMode = false
	a.postFn = func(f func()) { e.queue <- f }
	a.reader = e.r
	k := demoKey(markConv)
	a.keys = map[string]api.ConversationKey{markConv: k}
	a.convs = []conv{{Key: k, Kind: "group", Title: "测试群", Unread: unread, HasUnread: true}}
	a.sel, a.firstDone = markConv, true
	a.list = a.newList()
	a.rm.logf = func(format string, args ...any) { e.logs = append(e.logs, fmt.Sprintf(format, args...)) }
	e.a = a
	e.tt, _ = chatTester(a, 900, 700)
	return e
}

// settle 画几帧、把回界面线程的活都做完（标的结果在里面）。再画一轮，让结果引起的变化也落地
func (e *markEnv) settle() {
	for range 3 {
		e.tt.Frame()
		for {
			select {
			case f := <-e.queue:
				f()
				continue
			default:
			}
			break
		}
	}
}

// front 把窗口切到前台
func (e *markEnv) front() { e.a.rm.fg.Store(true); e.settle() }

// until 一直画帧、做回界面线程的活，直到 cond 成立（标的结果从后台线程回来要一会儿）
func (e *markEnv) until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等了 5 秒还没有：%s", what)
		}
		e.settle()
		time.Sleep(5 * time.Millisecond)
	}
}

// 前台、滚在最新一条、有未读：标到界面上最新一条的时间；之后画再多帧也不重复标
func TestMarkReadFrontAtEnd(t *testing.T) {
	e := newMarkEnv(t, 60, 5)
	e.front()
	e.r.wait(t, 1)
	e.until(t, "标完", func() bool { return !e.a.rm.busy })
	last := e.a.msgs[len(e.a.msgs)-1].at
	if c := e.r.calls[0]; c.upTo != last || c.key != demoKey(markConv) {
		t.Fatalf("应标到最新一条 %d、会话 %s，实际 %+v", last, markConv, c)
	}
	if e.a.rm.at != last || e.a.rm.busy {
		t.Fatalf("标完应记下 at=%d 且不忙，实际 at=%d busy=%v", last, e.a.rm.at, e.a.rm.busy)
	}
	for range 5 {
		e.settle()
	}
	if n := e.r.count(); n != 1 {
		t.Fatalf("同一个时间点只标一次，实际 %d 次", n)
	}
	// 又来了一条新消息，还在最新一条：再标一次，标到新的那条
	v := view("new1", "human", "某人", "新来的", last+60_000)
	got := msgsOf(t, v)
	got[0].live = true
	e.a.msgs, _ = mergeLive(e.a.msgs, got)
	e.settle()
	e.r.wait(t, 2)
	if c := e.r.calls[1]; c.upTo != last+60_000 {
		t.Fatalf("第二次应标到 %d，实际 %d", last+60_000, c.upTo)
	}
}

// 前台但滚在中间看旧消息：不算读到最新，不标；滚回最新一条才标
func TestMarkReadFrontScrolledUp(t *testing.T) {
	e := newMarkEnv(t, 60, 5) // 窗口在后台，先摆好位置
	e.a.list.ScrollTo(10, ui.Start)
	e.settle()
	if e.a.list.AtEnd() {
		t.Fatal("起点不对：应该滚在中间")
	}
	e.front()
	time.Sleep(100 * time.Millisecond)
	e.settle()
	if n := e.r.count(); n != 0 {
		t.Fatalf("滚在中间不该标，实际标了 %d 次", n)
	}
	e.a.list.ScrollToEnd()
	e.settle()
	e.r.wait(t, 1)
	if !e.a.list.AtEnd() {
		t.Fatal("滚回去以后应在最新一条")
	}
}

// 窗口在后台：滚在最新一条也不标；回到前台才标
func TestMarkReadBackground(t *testing.T) {
	e := newMarkEnv(t, 60, 5)
	for range 4 {
		e.settle()
	}
	time.Sleep(100 * time.Millisecond)
	e.settle()
	if !e.a.list.AtEnd() {
		t.Fatal("起点不对：应该在最新一条")
	}
	if n := e.r.count(); n != 0 {
		t.Fatalf("后台不该标，实际标了 %d 次", n)
	}
	e.front()
	e.r.wait(t, 1)
}

// archive：后台说不标（ErrReadMarkUnsupported）。问一次就记住，之后不再问、不记日志、不显示什么
func TestMarkReadArchive(t *testing.T) {
	e := newMarkEnv(t, 60, 5)
	e.r.mu.Lock()
	e.r.err = api.ErrReadMarkUnsupported
	e.r.mu.Unlock()
	e.front()
	e.r.wait(t, 1)
	e.until(t, "记下 off", func() bool { return e.a.rm.off })
	if !e.a.rm.off || e.a.rm.busy {
		t.Fatalf("说不标以后应记 off：%+v", e.a.rm.off)
	}
	v := view("new1", "human", "某人", "新来的", e.a.msgs[len(e.a.msgs)-1].at+60_000)
	got := msgsOf(t, v)
	got[0].live = true
	e.a.msgs, _ = mergeLive(e.a.msgs, got)
	for range 5 {
		e.settle()
	}
	if n := e.r.count(); n != 1 || len(e.logs) != 0 {
		t.Fatalf("问过一次就不再问、不记日志：%d 次，日志 %q", n, e.logs)
	}
	// 换了数据源（archive → receive）重新问
	e.a.attach(&markReader{got: make(chan struct{}, 64)})
	if e.a.rm.off {
		t.Fatal("换数据源应清掉 off")
	}
}

// 数据源根本没有 ReadMarker（演示、测试里的假实现）：不问、不崩
func TestMarkReadReaderWithoutMarker(t *testing.T) {
	e := newMarkEnv(t, 60, 5)
	e.a.reader = demoReader{}
	e.front()
	for range 3 {
		e.settle()
	}
	if !e.a.rm.off || e.r.count() != 0 {
		t.Fatalf("没有 ReadMarker 应记 off 且不问：off=%v 调用 %d", e.a.rm.off, e.r.count())
	}
}

// 写失败：只记一行日志，不崩、不弹、同一个时间点不每帧重试；下一条新消息再试
func TestMarkReadFailureLogsOnce(t *testing.T) {
	e := newMarkEnv(t, 60, 5)
	e.r.mu.Lock()
	e.r.err = errors.New("磁盘满了")
	e.r.mu.Unlock()
	e.front()
	e.r.wait(t, 1)
	e.until(t, "记下失败", func() bool { return len(e.logs) > 0 })
	for range 5 {
		e.settle()
	}
	if n := e.r.count(); n != 1 {
		t.Fatalf("失败后同一个时间点不重试，实际 %d 次", n)
	}
	if len(e.logs) != 1 || e.a.status != "" {
		t.Fatalf("应只记一行日志、状态栏不动：日志 %q 状态 %q", e.logs, e.a.status)
	}
	v := view("new1", "human", "某人", "新来的", e.a.msgs[len(e.a.msgs)-1].at+60_000)
	got := msgsOf(t, v)
	got[0].live = true
	e.a.msgs, _ = mergeLive(e.a.msgs, got)
	e.settle()
	e.r.wait(t, 2)
}

// 没有未读不标（不白写库）；本地会话没有已读记录也不标
func TestMarkReadSkipsWhenNothingToMark(t *testing.T) {
	e := newMarkEnv(t, 60, 0)
	e.front()
	for range 4 {
		e.settle()
	}
	time.Sleep(100 * time.Millisecond)
	e.settle()
	if n := e.r.count(); n != 0 {
		t.Fatalf("没有未读却标了 %d 次", n)
	}
	local := newMarkEnv(t, 60, 5)
	k := demoKey("local:aoi")
	local.a.keys[k.Conversation] = k
	local.a.convs[0].Key = k
	local.a.sel = k.Conversation
	local.front()
	for range 4 {
		local.settle()
	}
	time.Sleep(100 * time.Millisecond)
	if n := local.r.count(); n != 0 {
		t.Fatalf("本地会话不该标，实际 %d 次", n)
	}
}

// 标的活在路上时换了会话：旧会话的结果回来不能写进新会话的状态
func TestMarkReadSwitchWhileInFlight(t *testing.T) {
	e := newMarkEnv(t, 60, 5)
	e.r.hold = make(chan struct{})
	e.front()
	e.r.wait(t, 1)
	if !e.a.rm.busy {
		t.Fatal("起点不对：应该有一个标的活在路上")
	}
	e.a.switchTo("qq:group:1002")
	if e.a.rm.busy || e.a.rm.at != 0 {
		t.Fatalf("换会话应清掉忙和 at：%+v", e.a.rm.at)
	}
	close(e.r.hold)
	time.Sleep(50 * time.Millisecond)
	e.settle()
	if e.a.rm.at != 0 || e.a.rm.busy {
		t.Fatalf("旧会话的结果改了新会话的状态：at=%d busy=%v", e.a.rm.at, e.a.rm.busy)
	}
}
