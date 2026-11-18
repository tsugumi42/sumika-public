package desktopread

import (
	"context"
	"errors"
	"sync"

	"sumika-native/internal/alert"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
)

// Runtime 接收宿主已经确认的阶段与连接事实，不从 WS 连通推断 QQ 在线。
// 它不启动后台；宿主装配时 Publish，桌面只持有只读订阅。
type Runtime struct {
	mu     sync.Mutex
	value  api.RuntimeView
	alerts *alert.Hub
	subs   map[*runtimeSub]struct{}
}

type runtimeSub struct {
	owner *Runtime
	wake  chan struct{}
	dirty bool
}

func NewRuntime(initial api.RuntimeView, alerts *alert.Hub) *Runtime {
	r := &Runtime{alerts: alerts, subs: map[*runtimeSub]struct{}{}}
	r.Publish(initial)
	return r
}

func (r *Runtime) Publish(value api.RuntimeView) {
	if value.Account != "" {
		value.Account = config.MaskID(value.Account)
	}
	value.Alerts.Items = append([]api.RuntimeAlert(nil), value.Alerts.Items...)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.value = value
	for sub := range r.subs {
		sub.dirty = true
		select {
		case sub.wake <- struct{}{}:
		default:
		}
	}
}
func (r *Runtime) snapshot() api.RuntimeView {
	r.mu.Lock()
	value := r.value
	r.mu.Unlock()
	if r.alerts != nil {
		value.Alerts = api.List[api.RuntimeAlert]{}
		for _, a := range r.alerts.Open() {
			value.Alerts.Items = append(value.Alerts.Items, api.RuntimeAlert{Key: a.Key, Title: a.Title, Body: a.Body})
		}
	}
	return value
}
func (r *Runtime) subscribe() *runtimeSub {
	r.mu.Lock()
	defer r.mu.Unlock()
	sub := &runtimeSub{owner: r, wake: make(chan struct{}, 1)}
	r.subs[sub] = struct{}{}
	return sub
}
func (s *runtimeSub) take() bool {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	dirty := s.dirty
	s.dirty = false
	return dirty
}
func (s *runtimeSub) close() { s.owner.mu.Lock(); defer s.owner.mu.Unlock(); delete(s.owner.subs, s) }

func (s *Service) WatchRuntime(ctx context.Context) (api.Subscription[api.RuntimeView], error) {
	if s.runtime == nil {
		return nil, errors.New("宿主状态尚未接入")
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	return watch(ctx, nil, s.runtime, func(context.Context) (api.RuntimeView, error) { return s.runtime.snapshot(), nil }, done), nil
}
