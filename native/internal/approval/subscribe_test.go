package approval

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func approvalWait(t *testing.T, what string, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("等%s超过 1 秒", what)
		}
	}
}

func TestApprovalSubscriberCoalescesAndReleases(t *testing.T) {
	a := newApprovals(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	const cards = 1000
	results := make(chan State, cards)
	var requestOnce sync.Once
	a.Subscribe(func(Request) { requestOnce.Do(func() { close(entered); <-release }) }, func(r Request, d Decision) {
		results <- State{Request: r, Decision: &d}
	})
	first := card("0")
	first.Version = 1
	a.publish(State{Request: first})
	approvalRecv(t, entered, "慢订阅进入")
	var last <-chan struct{}
	for i := range cards {
		r := card(fmt.Sprint(i))
		r.Version = 1
		a.publish(State{Request: r})
		r.Version = 2
		last = a.publish(State{Request: r, Decision: &Decision{Kind: "cancelled"}})
		// 旧等待和错误的高版本等待都不能盖掉结局。
		r.Version = 3
		a.publish(State{Request: r})
		// 直接覆盖订阅方入口，不能只靠 publish 的过滤掩盖订阅版本保护。
		r.Version = 1
		a.subscribers[0].enqueue(State{Request: r, Decision: &Decision{Kind: "denied"}}, nil)
		r.Version = 3
		a.subscribers[0].enqueue(State{Request: r}, nil)
	}
	s := a.subscribers[0]
	s.mu.Lock()
	queued, versions := len(s.queue), len(s.latest)
	s.mu.Unlock()
	if queued != cards || versions != cards || len(a.Snapshot()) != approvalRecordLimit {
		t.Fatalf("积压没有按卡片有界合并：queue=%d latest=%d records=%d", queued, versions, len(a.Snapshot()))
	}
	for _, state := range a.Snapshot() {
		var id int
		fmt.Sscan(state.Request.ID, &id)
		if id < cards-approvalRecordLimit || state.Decision == nil {
			t.Fatalf("快照应该只留最近结局：%+v", state)
		}
	}
	unblock()
	seen := map[string]bool{}
	for range cards {
		s := approvalRecv(t, results, "积压卡片的最终结局")
		if seen[s.Request.ID] || s.Request.Version != 2 || s.Decision.Kind != "cancelled" {
			t.Fatalf("结局重复、被等待覆盖或版本错：%+v", s)
		}
		seen[s.Request.ID] = true
	}
	approvalRecv(t, last, "最终结局回执")
	approvalWait(t, "投递后释放索引", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.running && len(s.queue) == 0 && len(s.queued) == 0 && len(s.latest) == 0
	})
	t.Logf("卡住时 queue=%d latest=%d records=%d；放开后交付 %d 张结局，队列和索引归零", queued, versions, len(a.Snapshot()), len(seen))
}

func TestApprovalSubscriberContinuesAfterPanic(t *testing.T) {
	for _, phase := range []string{"request", "resolved"} {
		t.Run(phase, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			got := make(chan string, 2)
			callback := func(r Request) {
				if r.ID == "panic" {
					close(entered)
					<-release
					panic("测试通知异常")
				}
				got <- r.ID
			}
			s := newSubscription(callback, func(r Request, _ Decision) { callback(r) })
			state := func(id string) State {
				r := card(id)
				r.Version = 2
				d := State{Request: r}
				if phase == "resolved" {
					d.Decision = &Decision{Kind: "cancelled"}
				}
				return d
			}
			r := &receipt{done: make(chan struct{})}
			r.remaining.Store(1)
			s.enqueue(state("panic"), r)
			approvalRecv(t, entered, "将要 panic 的回调")
			s.enqueue(state("queued"), nil)
			unblock()
			approvalRecv(t, r.done, "panic 卡片回执收尾")
			if id := approvalRecv(t, got, "panic 后积压通知"); id != "queued" {
				t.Fatalf("积压通知不对：%s", id)
			}
			approvalWait(t, "投递协程退出", func() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.running })
			s.enqueue(state("later"), nil)
			if id := approvalRecv(t, got, "空闲后新通知"); id != "later" {
				t.Fatalf("后续通知不对：%s", id)
			}
		})
	}
}
