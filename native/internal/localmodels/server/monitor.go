package server

import (
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/localmodels/ort"
)

// Monitor 记着前向（ORT 的 Run）的进行情况，给 /health 的 run_elapsed_ms、last_ok_unix 和看门狗用。
// 不动 engine：把 ort.Runtime 包一层（WrapRuntime），所有会话的 Run 都从这里过。
// 两个模型各有一把行锁，可以同时各有一个前向在跑，所以记的是"进行中的集合"，对外取跑得最久的那个。
type Monitor struct {
	mu     sync.Mutex
	active map[uint64]time.Time
	seq    uint64
	lastOK atomic.Int64 // 上一次成功的前向结束时的 Unix 秒，0 = 还没有过
	now    func() time.Time
}

// NewMonitor 建一个监视器。
func NewMonitor() *Monitor {
	return &Monitor{active: map[uint64]time.Time{}, now: time.Now}
}

// begin 记下一个前向开始了，返回的函数在它结束时调用（ok 为真才算"上一次成功"）。
func (m *Monitor) begin() func(ok bool) {
	m.mu.Lock()
	m.seq++
	id := m.seq
	m.active[id] = m.now()
	m.mu.Unlock()
	return func(ok bool) {
		m.mu.Lock()
		delete(m.active, id)
		m.mu.Unlock()
		if ok {
			m.lastOK.Store(m.now().Unix())
		}
	}
}

// Longest 是进行中的前向里跑得最久的那一个已经跑了多久，没有前向在跑就是 0。
func (m *Monitor) Longest() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var longest time.Duration
	for _, t := range m.active {
		if d := now.Sub(t); d > longest {
			longest = d
		}
	}
	return longest
}

// LastOKUnix 是上一次成功的前向结束的 Unix 秒，还没有过就是 0。
func (m *Monitor) LastOKUnix() int64 { return m.lastOK.Load() }

// WrapRuntime 返回一个行为和 rt 一样、但每个会话的 Run 都被记录的 Runtime。
func (m *Monitor) WrapRuntime(rt ort.Runtime) ort.Runtime { return monitoredRuntime{Runtime: rt, m: m} }

type monitoredRuntime struct {
	ort.Runtime
	m *Monitor
}

func (r monitoredRuntime) NewSession(modelPath string, o ort.Options) (ort.Session, error) {
	s, err := r.Runtime.NewSession(modelPath, o)
	if err != nil {
		return nil, err
	}
	return monitoredSession{Session: s, m: r.m}, nil
}

type monitoredSession struct {
	ort.Session
	m *Monitor
}

// Run 记开始和结束；Run 万一 panic，defer 也会把它从"进行中"里拿掉，不让一个已经死掉的前向永远算在看门狗头上。
func (s monitoredSession) Run(inputs []ort.Int64Tensor, outputs []string) (res map[string]ort.FloatTensor, err error) {
	end := s.m.begin()
	ok := false
	defer func() { end(ok) }()
	res, err = s.Session.Run(inputs, outputs)
	ok = err == nil
	return res, err
}
