package web

import (
	"context"
	"fmt"

	"sumika-native/internal/ecosystem"
)

type flight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	value   any
	err     error
}

// 单飞只合并同 Root；每个等待者已独立扣逻辑次数，最后一人取消才停共享执行。
func (s *Service) shared(ctx context.Context, a Access, key string, run func(context.Context) (any, error)) (any, error) {
	key = a.inv.RootID + ":" + key
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ecosystem.ErrDenied
	}
	if s.flights == nil {
		s.flights = map[string]*flight{}
	}
	f := s.flights[key]
	if f == nil {
		if s.opts.Lifetime == nil {
			s.mu.Unlock()
			return nil, fmt.Errorf("web 缺内核寿命端口")
		}
		workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.opts.Timeout)
		workCtx, activity, err := s.opts.Lifetime.Begin(workCtx, a.inv)
		if err != nil {
			cancel()
			s.mu.Unlock()
			return nil, err
		}
		f = &flight{done: make(chan struct{}), cancel: cancel}
		s.flights[key] = f
		root := s.roots[a.inv.RootID]
		if root == nil {
			root = &rootState{slot: make(chan struct{}, s.opts.MaxInflightPerTask)}
			s.roots[a.inv.RootID] = root
		}
		go func() {
			var err error
			var value any
			select {
			case root.slot <- struct{}{}:
				select {
				case s.slot <- struct{}{}:
					value, err = run(workCtx)
					<-s.slot
				case <-workCtx.Done():
					err = workCtx.Err()
				}
				<-root.slot
			case <-workCtx.Done():
				err = workCtx.Err()
			}
			cancel()
			activity.End(err)
			s.mu.Lock()
			f.value, f.err = value, err
			delete(s.flights, key)
			close(f.done)
			s.mu.Unlock()
		}()
	}
	f.waiters++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		f.waiters--
		if f.waiters == 0 {
			f.cancel()
		}
		s.mu.Unlock()
	}()
	select {
	case <-f.done:
		return f.value, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	done := make([]chan struct{}, 0, len(s.flights))
	for _, f := range s.flights {
		f.cancel()
		done = append(done, f.done)
	}
	s.mu.Unlock()
	for _, ch := range done {
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	clear(s.cache)
	s.lru.Init()
	s.cacheBytes = 0
	clear(s.roots)
	s.mu.Unlock()
	return nil
}
