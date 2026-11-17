package store

import (
	"database/sql"
)

// ChangeSub 保存失效身份；唤醒只占一格，正文留在已提交的库里。
type ChangeSub struct {
	owner  *Store
	wake   chan struct{}
	dirty  map[string]bool
	closed bool
}

func (s *Store) SubscribeChanges() *ChangeSub {
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	sub := &ChangeSub{owner: s, wake: make(chan struct{}, 1), dirty: map[string]bool{}}
	if s.changeSubs == nil {
		s.changeSubs = map[*ChangeSub]bool{}
	}
	s.changeSubs[sub] = true
	return sub
}
func (s *ChangeSub) Wake() <-chan struct{} { return s.wake }

// Take 在查库之前交换集合，读取期间的新提交属于下一批。
func (s *ChangeSub) Take() map[string]bool {
	s.owner.changeMu.Lock()
	defer s.owner.changeMu.Unlock()
	dirty := s.dirty
	s.dirty = map[string]bool{}
	return dirty
}
func (s *ChangeSub) Close() {
	s.owner.changeMu.Lock()
	defer s.owner.changeMu.Unlock()
	if !s.closed {
		s.closed = true
		delete(s.owner.changeSubs, s)
	}
}
func (s *Store) changed(conversations ...string) {
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	for sub := range s.changeSubs {
		for _, conv := range conversations {
			sub.dirty[conv] = true
		}
		select {
		case sub.wake <- struct{}{}:
		default:
		}
	}
}
func (s *Store) commitChanged(tx *sql.Tx, conversations ...string) error {
	if err := tx.Commit(); err != nil {
		return err
	}
	s.changed(conversations...)
	return nil
}
func (s *Store) commitPlanChanged(tx *sql.Tx, plans ...string) error {
	convs := make([]string, 0, len(plans))
	for _, id := range plans {
		var conv string
		if err := tx.QueryRow(`SELECT conversation FROM outbound_plans WHERE id=?`, id).Scan(&conv); err != nil {
			return err
		}
		convs = append(convs, conv)
	}
	return s.commitChanged(tx, convs...)
}
