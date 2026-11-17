package proactive

import (
	"sync"
	"time"
)

// MemStore 只放内存，给测试和不想落盘的宿主用
type MemStore struct {
	mu     sync.Mutex
	States map[string]State
	Rows   []Row // 按写入顺序，ID 从 1 起
}

func (m *MemStore) LoadState(c string) (State, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.States[c]
	if !ok {
		return State{Character: c, Factor: 1}, false, nil
	}
	return st, true, nil
}

func (m *MemStore) SaveState(st State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.States == nil {
		m.States = map[string]State{}
	}
	m.States[st.Character] = st
	return nil
}

func (m *MemStore) AddRow(r Row) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.ID = int64(len(m.Rows) + 1)
	if r.Count < 1 {
		r.Count = 1
	}
	m.Rows = append(m.Rows, r)
	return r.ID, nil
}

func (m *MemStore) AddCount(id int64, n int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.Rows {
		if m.Rows[i].ID == id {
			m.Rows[i].Count += n
		}
	}
	return nil
}

func (m *MemStore) Annotate(id int64, reason, thread, note string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.Rows {
		if m.Rows[i].ID == id {
			if reason != "" {
				m.Rows[i].Reason = reason
			}
			if thread != "" {
				m.Rows[i].Thread = thread
			}
			if note != "" {
				m.Rows[i].ModelNote = note
			}
		}
	}
	return nil
}

func (m *MemStore) HasRowSince(c string, since time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.Rows {
		if r.Character == c && !r.At.Before(since) {
			return true, nil
		}
	}
	return false, nil
}

// Prune 删掉 before 以前的行。行 ID 不重排：测试只在不依赖 ID 的地方用它
func (m *MemStore) Prune(before time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	kept := m.Rows[:0:0]
	for _, r := range m.Rows {
		if r.At.Before(before) {
			n++
			continue
		}
		kept = append(kept, r)
	}
	m.Rows = kept
	return n, nil
}
