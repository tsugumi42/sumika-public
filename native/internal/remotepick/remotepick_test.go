package remotepick

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// rig 是假的取数和假的“回到界面线程”：post 的函数排队，测试里手动放行
type rig struct {
	mu      sync.Mutex
	queue   []func()
	started chan string // 每次真的发请求，报一次词
	gate    map[string]chan struct{}
	results map[string][]Candidate
	errs    map[string]error
	cancels map[string]bool
	s       *State
}

func newRig() *rig {
	r := &rig{started: make(chan string, 16), gate: map[string]chan struct{}{}, results: map[string][]Candidate{}, errs: map[string]error{}, cancels: map[string]bool{}}
	r.s = &State{Debounce: 300 * time.Millisecond, Post: func(f func()) { r.mu.Lock(); r.queue = append(r.queue, f); r.mu.Unlock() }}
	r.s.Fetch = func(ctx context.Context, q string) ([]Candidate, error) {
		r.mu.Lock()
		g := r.gate[q]
		r.mu.Unlock()
		r.started <- q
		if g != nil {
			select {
			case <-g:
			case <-ctx.Done():
				r.mu.Lock()
				r.cancels[q] = true
				r.mu.Unlock()
				return nil, ctx.Err()
			}
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.results[q], r.errs[q]
	}
	return r
}

// pump 放行已经排队的结果
func (r *rig) pump() {
	r.mu.Lock()
	q := r.queue
	r.queue = nil
	r.mu.Unlock()
	for _, f := range q {
		f()
	}
}

func (r *rig) waitStarted(t *testing.T) string {
	t.Helper()
	select {
	case q := <-r.started:
		return q
	case <-time.After(2 * time.Second):
		t.Fatal("没有发请求")
		return ""
	}
}

func (r *rig) noRequest(t *testing.T) {
	t.Helper()
	select {
	case q := <-r.started:
		t.Fatalf("不该发请求：%q", q)
	case <-time.After(30 * time.Millisecond):
	}
}

func (r *rig) waitPost(t *testing.T) {
	t.Helper()
	for i := 0; i < 200; i++ {
		r.mu.Lock()
		n := len(r.queue)
		r.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("结果没回来")
}

var t0 = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func TestDebounceSendsOnceAfterTyping(t *testing.T) {
	r := newRig()
	r.results["海淀"] = []Candidate{{ID: "1", Label: "海淀，北京市，中国"}}
	now := t0
	for _, text := range []string{"海", "海淀"} { // 打字过程中不发
		r.s.Edit(now, text)
		if w := r.s.Step(now); w <= 0 {
			t.Fatalf("还在去抖，该告诉界面等多久：%v", w)
		}
		now = now.Add(100 * time.Millisecond)
	}
	r.noRequest(t)
	now = now.Add(250 * time.Millisecond) // 距最后一次输入 350ms
	r.s.Step(now)
	if q := r.waitStarted(t); q != "海淀" {
		t.Fatalf("该只查最后的词：%q", q)
	}
	if !r.s.Loading {
		t.Fatal("在途应标 Loading")
	}
	r.waitPost(t)
	r.pump()
	if r.s.Loading || len(r.s.Items) != 1 || r.s.Items[0].Label != "海淀，北京市，中国" || r.s.Settled != "海淀" {
		t.Fatalf("结果：%+v", r.s)
	}
	// 同一个词不重复查
	r.s.Step(now.Add(time.Second))
	r.noRequest(t)
}

func TestStaleRequestCancelledAndLateResultDropped(t *testing.T) {
	r := newRig()
	gate := make(chan struct{})
	r.gate["北"] = gate // 第一个请求卡住
	r.results["北"] = []Candidate{{ID: "old", Label: "旧结果"}}
	r.results["北京"] = []Candidate{{ID: "new", Label: "新结果"}}
	now := t0
	r.s.Edit(now, "北")
	now = now.Add(400 * time.Millisecond)
	r.s.Step(now)
	if q := r.waitStarted(t); q != "北" {
		t.Fatal(q)
	}
	// 在途时字变了：旧请求要被取消
	r.s.Edit(now, "北京")
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		c := r.cancels["北"]
		r.mu.Unlock()
		if c {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("旧请求没被取消")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if r.s.Loading {
		t.Fatal("字变了，旧的 Loading 要撤掉")
	}
	now = now.Add(400 * time.Millisecond)
	r.s.Step(now)
	if q := r.waitStarted(t); q != "北京" {
		t.Fatal(q)
	}
	// 即使旧请求的结果晚到（模拟：直接把旧代数的结果送进来），也要丢掉
	r.s.deliver(1, "北", []Candidate{{ID: "old"}}, nil)
	r.waitPost(t)
	r.pump()
	if len(r.s.Items) != 1 || r.s.Items[0].ID != "new" || r.s.Settled != "北京" {
		t.Fatalf("过期结果污染了候选：%+v", r.s.Items)
	}
	close(gate)
}

func TestFailureHasTextAndNoAutoRetry(t *testing.T) {
	r := newRig()
	r.errs["海淀"] = errors.New("没取到候选：网络不通")
	now := t0
	r.s.Edit(now, "海淀")
	now = now.Add(400 * time.Millisecond)
	r.s.Step(now)
	r.waitStarted(t)
	r.waitPost(t)
	r.pump()
	if r.s.Err != "没取到候选：网络不通" || r.s.Loading || len(r.s.Items) != 0 {
		t.Fatalf("失败要有文字：%+v", r.s)
	}
	// 字不变不自动重试
	r.s.Step(now.Add(5 * time.Second))
	r.noRequest(t)
	// 点重试：马上再发
	delete(r.errs, "海淀")
	r.results["海淀"] = []Candidate{{ID: "1", Label: "海淀"}}
	r.s.Retry()
	if r.s.Err != "" {
		t.Fatal("重试要先清掉失败文字")
	}
	r.s.Step(now.Add(6 * time.Second))
	r.waitStarted(t)
	r.waitPost(t)
	r.pump()
	if r.s.Err != "" || len(r.s.Items) != 1 {
		t.Fatalf("重试后：%+v", r.s)
	}
	// 改字也会清掉失败文字
	r.errs["海"] = errors.New("坏了")
	r.s.Edit(now.Add(7*time.Second), "海")
	if r.s.Err != "" {
		t.Fatal("改字要清掉失败文字")
	}
}

func TestEmptyResultAndShortText(t *testing.T) {
	r := newRig()
	r.results["无此地"] = nil
	r.s.MinRunes = 2
	now := t0
	r.s.Edit(now, "无")
	r.s.Step(now.Add(time.Second))
	r.noRequest(t) // 太短不发
	r.s.Edit(now, "无此地")
	now = now.Add(400 * time.Millisecond)
	r.s.Step(now)
	r.waitStarted(t)
	r.waitPost(t)
	r.pump()
	if !r.s.Empty() {
		t.Fatalf("查过没有结果该标 Empty：%+v", r.s)
	}
	// 字清空：候选和 Empty 都没了，也不发请求
	r.s.Edit(now, "")
	if r.s.Empty() || len(r.s.Items) != 0 || r.s.Settled != "" {
		t.Fatalf("清空：%+v", r.s)
	}
	r.s.Step(now.Add(time.Second))
	r.noRequest(t)
}

func TestWhitespaceOnlyChangeDoesNotRefetch(t *testing.T) {
	r := newRig()
	r.results["海淀"] = []Candidate{{ID: "1", Label: "海淀"}}
	now := t0
	r.s.Edit(now, "海淀")
	now = now.Add(400 * time.Millisecond)
	r.s.Step(now)
	r.waitStarted(t)
	r.waitPost(t)
	r.pump()
	r.s.Edit(now, "海淀 ") // 只多了个空格：不重查，候选不闪
	r.s.Step(now.Add(time.Second))
	r.noRequest(t)
	if len(r.s.Items) != 1 {
		t.Fatal("候选不该丢")
	}
}

func TestResetAndCloseCancelInflight(t *testing.T) {
	r := newRig()
	r.gate["海淀"] = make(chan struct{})
	now := t0
	r.s.Edit(now, "海淀")
	r.s.Step(now.Add(400 * time.Millisecond))
	r.waitStarted(t)
	r.s.Reset()
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		c := r.cancels["海淀"]
		r.mu.Unlock()
		if c {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Reset 没取消在途请求")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if r.s.Text != "" || r.s.Loading || len(r.s.Items) != 0 {
		t.Fatalf("Reset 后：%+v", r.s)
	}
	r.s.Close() // 重复关没有副作用
}
