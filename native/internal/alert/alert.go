// Package alert 是"现在有什么事没恢复"的小登记处：按键去重的 Raise / Clear、
// Open() 快照、不丢事件的订阅。
//
// 只做登记和分发，不含计时器（"掉线满 30 秒才报"属于生产者）、不发系统通知、
// 不 import 渠道 / 记忆 / mygo；出口（系统通知、主窗口）在装配处订阅。
// 依据：docs/archive/notes/方案-后台主动提醒主人-2026-10-26.md 第五节、第八节。
package alert

import "sync"

// Alert 是一件"现在有问题"的事。同一个 Key 同时只有一条。
type Alert struct {
	Key    string // 如 "qq:<账号>:conn"、"guard:route"
	Class  string // 对应设置·通知页的一行；第一版出口不读
	Title  string // 一句话；换了标题就算新的一次
	Body   string // 原因和影响；不含聊天原文、不含私事
	Detail string // 只给窗口和日志看，不进系统通知
}

// Note 是恢复时的标题和正文。
type Note struct{ Title, Body string }

// Kind 是订阅者收到的变化种类。
type Kind int

const (
	Raised  Kind = iota + 1 // 新出现，或同键换了标题
	Updated                 // 同键同标题，只更新了正文 / 详情
	Cleared                 // 恢复了
)

// Change 是订阅者收到的一条变化。Note 只有 Cleared 有；
// Cleared 的 Alert 是关掉之前最后一次的内容。
type Change struct {
	Kind  Kind
	Alert Alert
	Note  Note
}

// Hub 线程安全。Raise / Clear 不阻塞调用者，任何协程里都能调。
type Hub struct {
	mu    sync.Mutex
	open  map[string]Alert
	order []string // 开着的键，按出现先后
	subs  map[*Sub]struct{}
}

func New() *Hub {
	return &Hub{open: map[string]Alert{}, subs: map[*Sub]struct{}{}}
}

// Raise：键没开着 → 开，发 Raised；开着且 Title 变了 → 更新，发 Raised；
// 开着且 Title 没变 → 只更新 Body / Detail / Class，发 Updated。
func (h *Hub) Raise(a Alert) {
	h.mu.Lock()
	defer h.mu.Unlock()
	old, ok := h.open[a.Key]
	h.open[a.Key] = a
	kind := Raised
	if !ok {
		h.order = append(h.order, a.Key)
	} else if old.Title == a.Title {
		kind = Updated
	}
	h.emit(Change{Kind: kind, Alert: a})
}

// Clear：键开着 → 关，发 Cleared；没开着 → 什么也不做。
func (h *Hub) Clear(key string, note Note) {
	h.mu.Lock()
	defer h.mu.Unlock()
	old, ok := h.open[key]
	if !ok {
		return
	}
	delete(h.open, key)
	for i, k := range h.order {
		if k == key {
			h.order = append(h.order[:i:i], h.order[i+1:]...)
			break
		}
	}
	h.emit(Change{Kind: Cleared, Alert: old, Note: note})
}

// Open 返回现在没恢复的，按出现先后（同键换标题不改变位置）。
func (h *Hub) Open() []Alert {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshot()
}

func (h *Hub) snapshot() []Alert {
	out := make([]Alert, 0, len(h.order))
	for _, k := range h.order {
		out = append(out, h.open[k])
	}
	return out
}

// Subscribe 登记一个订阅者：之后发生的变化按先后送到 C()，队列不限长、不丢。
// 要当前状态又不想在 Open() 和登记之间漏变化，用 SubscribeWithOpen。
func (h *Hub) Subscribe() *Sub {
	s, _ := h.SubscribeWithOpen()
	return s
}

// SubscribeWithOpen 在同一把锁里取快照并登记：快照之后的变化一定都在 C() 里。
func (h *Hub) SubscribeWithOpen() (*Sub, []Alert) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := newSub(h)
	h.subs[s] = struct{}{}
	return s, h.snapshot()
}

func (h *Hub) emit(c Change) {
	for s := range h.subs {
		s.push(c)
	}
}

// Sub 是一个订阅者。用完要 Close，否则它的协程一直在。
type Sub struct {
	hub  *Hub
	out  chan Change
	wake chan struct{}
	done chan struct{}
	once sync.Once

	mu sync.Mutex
	q  []Change
}

func newSub(h *Hub) *Sub {
	s := &Sub{
		hub:  h,
		out:  make(chan Change),
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
	go s.pump()
	return s
}

// C 按先后送出变化。Close 之后会被关闭。
func (s *Sub) C() <-chan Change { return s.out }

// Close 取消订阅，没送出的丢弃。可重复调用。
func (s *Sub) Close() {
	s.once.Do(func() {
		s.hub.mu.Lock()
		delete(s.hub.subs, s)
		s.hub.mu.Unlock()
		close(s.done)
	})
}

func (s *Sub) push(c Change) {
	s.mu.Lock()
	s.q = append(s.q, c)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Sub) pump() {
	defer close(s.out)
	for {
		s.mu.Lock()
		var c Change
		has := len(s.q) > 0
		if has {
			c = s.q[0]
			s.q[0] = Change{}
			s.q = s.q[1:]
		}
		s.mu.Unlock()
		if !has {
			select {
			case <-s.wake:
				continue
			case <-s.done:
				return
			}
		}
		select {
		case s.out <- c:
		case <-s.done:
			return
		}
	}
}
