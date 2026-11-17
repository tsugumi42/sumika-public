package approval

import (
	"log"
	"sync"
	"sync/atomic"
)

// State 是重连时可重建的一张卡片；Decision 为空表示仍在等待。
// Request.Version 随状态递增，接收方不得用旧版本覆盖新版本。
type State struct {
	Request  Request   `json:"request"`
	Decision *Decision `json:"decision,omitempty"`
}

type delivery struct {
	state   State
	receipt *receipt
}

type stateVersion struct {
	version uint64
	ended   bool
}

// receipt 由投递协程直接收尾，不为每张卡片另留一个等慢订阅的协程。
type receipt struct {
	remaining atomic.Int32
	done      chan struct{}
}

func (r *receipt) complete() {
	if r != nil && r.remaining.Add(-1) == 0 {
		close(r.done)
	}
}

type subscription struct {
	mu         sync.Mutex
	onRequest  func(Request)
	onResolved func(Request, Decision)
	queue      []string
	queued     map[string]delivery
	latest     map[string]stateVersion
	running    bool
}

// Subscribe 成对注册一个订阅方，串行投递请求和结局；卡住只影响这个订阅方。
// 注册时重放现有状态，和后续增量在同一把锁里衔接，不漏掉重连期间的结局。
// 待投递按卡片合并；不同卡片的结局不丢，已投递结局不再占版本索引。
func (a *Approvals) Subscribe(onRequest func(Request), onResolved func(Request, Decision)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := newSubscription(onRequest, onResolved)
	for _, state := range a.records {
		s.enqueue(state, nil)
	}
	a.subscribers = append(a.subscribers, s)
}

func newSubscription(onRequest func(Request), onResolved func(Request, Decision)) *subscription {
	return &subscription{onRequest: onRequest, onResolved: onResolved, queued: map[string]delivery{}, latest: map[string]stateVersion{}}
}

// registerCallbacks 在 a.mu 内把旧的每一次单边注册变成独立订阅，不重放以前的卡片。
func (a *Approvals) registerCallbacks() {
	for ; a.requests < len(a.OnRequest); a.requests++ {
		a.subscribers = append(a.subscribers, newSubscription(a.OnRequest[a.requests], nil))
	}
	for ; a.resolved < len(a.OnResolved); a.resolved++ {
		a.subscribers = append(a.subscribers, newSubscription(nil, a.OnResolved[a.resolved]))
	}
}

// Snapshot 同时取等待和已结束的记录；重连方按版本合并，不能只取 Pending。
// 已结束的记录只保留最近 256 张；跨进程的审批结局由步骤库和启动清理恢复。
func (a *Approvals) Snapshot() []State {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]State, 0, len(a.records))
	for _, state := range a.records {
		out = append(out, copyState(state))
	}
	return out
}

func copyState(state State) State {
	state.Request.Notes = append([]string(nil), state.Request.Notes...)
	state.Request.Options = append([]string(nil), state.Request.Options...)
	if state.Request.Preview != nil {
		preview := *state.Request.Preview
		state.Request.Preview = &preview
	}
	if state.Decision != nil {
		decision := *state.Decision
		state.Decision = &decision
	}
	return state
}

func (a *Approvals) publish(state State) <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	r := &receipt{done: make(chan struct{})}
	r.remaining.Store(1)
	if a.records == nil {
		a.records = map[string]State{}
	}
	old := a.records[state.Request.ID]
	if old.Request.Version < state.Request.Version && (old.Decision == nil || state.Decision != nil) {
		a.records[state.Request.ID] = copyState(state)
		if state.Decision != nil && old.Decision == nil {
			a.finished = append(a.finished, state.Request.ID)
			if len(a.finished) > approvalRecordLimit {
				delete(a.records, a.finished[0])
				copy(a.finished, a.finished[1:])
				a.finished = a.finished[:approvalRecordLimit]
			}
		}
		r.remaining.Add(int32(len(a.subscribers)))
		for _, s := range a.subscribers {
			s.enqueue(state, r)
		}
	}
	r.complete()
	return r.done
}

func (s *subscription) enqueue(state State, receipt *receipt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.latest[state.Request.ID]
	if old.version >= state.Request.Version || (old.ended && state.Decision == nil) {
		receipt.complete()
		return
	}
	s.latest[state.Request.ID] = stateVersion{version: state.Request.Version, ended: state.Decision != nil}
	if previous, ok := s.queued[state.Request.ID]; ok {
		previous.receipt.complete()
	} else {
		s.queue = append(s.queue, state.Request.ID)
	}
	s.queued[state.Request.ID] = delivery{state: copyState(state), receipt: receipt}
	if !s.running {
		s.running = true
		go s.run()
	}
}

func (s *subscription) run() {
	var current delivery
	inCallback := false
	defer func() {
		if err := recover(); err != nil {
			log.Printf("审批通知回调 panic：%v", err)
		}
		s.mu.Lock()
		if inCallback {
			current.receipt.complete()
			s.forget(current)
		}
		s.running = false
		if len(s.queue) != 0 {
			s.running = true
			go s.run()
		}
		s.mu.Unlock()
	}()
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.queue = nil
			s.mu.Unlock()
			return
		}
		id := s.queue[0]
		d := s.queued[id]
		delete(s.queued, id)
		s.queue[0] = ""
		s.queue = s.queue[1:]
		current = d
		inCallback = true
		s.mu.Unlock()
		if d.state.Decision == nil {
			if s.onRequest != nil {
				s.onRequest(d.state.Request)
			}
		} else if s.onResolved != nil {
			s.onResolved(d.state.Request, *d.state.Decision)
		}
		d.receipt.complete()
		s.mu.Lock()
		s.forget(d)
		inCallback = false
		current = delivery{}
		s.mu.Unlock()
	}
}

// forget 在 s.mu 内释放已交付结局的版本，不影响同卡片更晚入队的状态。
func (s *subscription) forget(d delivery) {
	if d.state.Decision != nil && s.latest[d.state.Request.ID].version == d.state.Request.Version {
		delete(s.latest, d.state.Request.ID)
	}
}

// 重连缓存取最近 256 张结局，避免按进程累计；未交付结局另由各订阅方保留。
const approvalRecordLimit = 256
