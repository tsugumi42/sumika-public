package main

// 到 Bun 子进程的连接和三种“跟着连接走”的订阅（规格-本地私聊切到Bun 第四节）。
// bunLink 属于宿主（host），不属于桥：archive ↔ receive 切换会重建桥，Bun 子进程和它的连接不跟着重来。
// Bun 不在（没起来、崩了、重启中）时，走 Bun 的方法回 ErrLocalBackendDown，绝不退回 Go 去写。

import (
	"context"
	"fmt"
	"sync"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/rpcclient"
)

type bunLink struct {
	mu      sync.Mutex
	cur     *rpcclient.Full // nil = Bun 不在
	reason  string          // 不在的原因，给人看
	changed chan struct{}   // 每次连上、断开都关掉旧的、换一个新的：等变化的人 select 它
}

func newBunLink(reason string) *bunLink {
	return &bunLink{reason: reason, changed: make(chan struct{})}
}

// state 是此刻的连接（nil = 不在）、不在的原因，和“下一次变化”的通道
func (l *bunLink) state() (*rpcclient.Full, string, <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cur, l.reason, l.changed
}

// get 给走 Bun 的方法用：不在就回带原因的 ErrLocalBackendDown
func (l *bunLink) get() (*rpcclient.Full, error) {
	f, reason, _ := l.state()
	if f == nil {
		if reason == "" {
			return nil, api.ErrLocalBackendDown
		}
		return nil, fmt.Errorf("%w：%s", api.ErrLocalBackendDown, reason)
	}
	// 连接已经断了但监护还没来得及标记：也当不在
	select {
	case <-f.C.Done():
		return nil, fmt.Errorf("%w：和 Bun 的连接断了", api.ErrLocalBackendDown)
	default:
	}
	return f, nil
}

func (l *bunLink) up(f *rpcclient.Full) {
	l.mu.Lock()
	l.cur, l.reason = f, ""
	close(l.changed)
	l.changed = make(chan struct{})
	l.mu.Unlock()
}

func (l *bunLink) down(reason string) {
	l.mu.Lock()
	l.cur, l.reason = nil, reason
	close(l.changed)
	l.changed = make(chan struct{})
	l.mu.Unlock()
}

// bunSub 是 watchBun / mergeSubs 交给界面的订阅
type bunSub[T any] struct {
	out    chan api.Snapshot[T]
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *bunSub[T]) Updates() <-chan api.Snapshot[T] { return s.out }
func (s *bunSub[T]) Close() {
	s.cancel()
	<-s.done
}

// bunRetry：订阅没订上、或连接刚断时，隔多久再看一次（连接变化会提前叫醒，这只是兜底，免得热循环）
const bunRetry = time.Second

// watchBun 订阅 Bun 那头的长期订阅（审批卡、提问、状态）：
// Bun 不在就先发一份空值，连上了再订；连接断了（崩了、重启）发空值，等重连再订。
// 界面只订一次，Bun 重启后不会卡在旧的订阅上。
func watchBun[T any](ctx context.Context, link *bunLink, empty T, open func(context.Context, *rpcclient.Full) (api.Subscription[T], error)) api.Subscription[T] {
	ctx, cancel := context.WithCancel(ctx)
	sub := &bunSub[T]{out: make(chan api.Snapshot[T]), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(sub.done)
		defer close(sub.out)
		defer cancel()
		var version uint64
		emit := func(r api.Result[T]) bool {
			version++
			select {
			case sub.out <- api.Snapshot[T]{Version: version, Result: r}:
				return true
			case <-ctx.Done():
				return false
			}
		}
		ready := func(v T) api.Result[T] { return api.Result[T]{State: api.Ready, Value: v} }
		pause := func(changed <-chan struct{}) bool {
			t := time.NewTimer(bunRetry)
			defer t.Stop()
			select {
			case <-changed:
			case <-t.C:
			case <-ctx.Done():
				return false
			}
			return true
		}
		for {
			f, _, changed := link.state()
			if f == nil {
				if !emit(ready(empty)) {
					return
				}
				select {
				case <-changed:
					continue
				case <-ctx.Done():
					return
				}
			}
			inner, err := open(ctx, f)
			if err != nil {
				if !emit(ready(empty)) || !pause(changed) {
					return
				}
				continue
			}
			for snap := range inner.Updates() {
				if !emit(snap.Result) {
					inner.Close()
					return
				}
			}
			inner.Close()
			if ctx.Err() != nil {
				return
			}
			// 订阅断了：Bun 的状态此刻不可知，先清空，等连接变化（或隔一会儿）再订
			if !emit(ready(empty)) || !pause(changed) {
				return
			}
		}
	}()
	return sub
}

// mergeSubs 把两个“一份份整换”的列表订阅合成一个：任何一边来新快照就发一份“两边最近一份拼起来”的快照，
// 谁也不等谁（慢的那边先当空）。用在目标 / 定期任务 / 提醒的状态：Bun 管本地的，Go 管 QQ 的“正在输入”
func mergeSubs[T any](ctx context.Context, a, b api.Subscription[[]T]) api.Subscription[[]T] {
	ctx, cancel := context.WithCancel(ctx)
	sub := &bunSub[[]T]{out: make(chan api.Snapshot[[]T]), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(sub.done)
		defer close(sub.out)
		defer cancel()
		defer a.Close()
		defer b.Close()
		var last [2]api.Result[[]T]
		var version uint64
		ac, bc := a.Updates(), b.Updates()
		for ac != nil || bc != nil {
			select {
			case s, ok := <-ac:
				if !ok {
					ac = nil
					continue
				}
				last[0] = s.Result
			case s, ok := <-bc:
				if !ok {
					bc = nil
					continue
				}
				last[1] = s.Result
			case <-ctx.Done():
				return
			}
			merged := api.Result[[]T]{State: api.Ready}
			for _, r := range last {
				merged.Value = append(merged.Value, r.Value...)
				if r.State == api.Failed && merged.State == api.Ready {
					merged.State, merged.Err, merged.Stale = api.Failed, r.Err, true
				}
			}
			version++
			select {
			case sub.out <- api.Snapshot[[]T]{Version: version, Result: merged}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return sub
}
