package app

import (
	"context"
	"sort"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/goal"
	"sumika-native/internal/loop"
)

var _ api.LocalStatusReader = (*DesktopReader)(nil)

// statusChanged 唤醒状态订阅（目标、定期任务、自动轮有变化，或写了系统行）。桌面还没建时什么都不做。
func (a *App) statusChanged(string) {
	if d := a.desktopPtr.Load(); d != nil {
		d.statusWake()
	}
}

func (d *Desktop) statusWake() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for ch := range d.statusWakes {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// typingShown 是“对方正在输入”显示多久：协议端输入时隔几秒发一次，没有可靠的“停了”。
const typingShown = 6 * time.Second

// noteTyping 记下对方正在输入，几秒后自己消失。只存内存，不进库。
func (a *App) noteTyping(conv string) {
	a.typingMu.Lock()
	if a.typing == nil {
		a.typing = map[string]time.Time{}
	}
	a.typing[conv] = a.now().Add(typingShown)
	a.typingMu.Unlock()
	a.statusChanged(conv)
	time.AfterFunc(typingShown+50*time.Millisecond, func() { a.statusChanged(conv) })
}

func (a *App) typingConversations() []string {
	a.typingMu.Lock()
	defer a.typingMu.Unlock()
	now := a.now()
	var out []string
	for conv, until := range a.typing {
		if until.After(now) {
			out = append(out, conv)
		} else {
			delete(a.typing, conv)
		}
	}
	return out
}

// localStatuses 汇总所有本地私聊会话的状态，按会话号排序。
func (a *App) localStatuses() []api.LocalStatus {
	by := map[string]*api.LocalStatus{}
	get := func(conv string) *api.LocalStatus {
		if !localPrivateConv(conv) {
			return nil
		}
		if by[conv] == nil {
			by[conv] = &api.LocalStatus{Key: api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: conv}}
		}
		return by[conv]
	}
	if a.goals != nil {
		for _, conv := range a.goals.Sessions() {
			if g, ok := a.goals.Get(conv); ok {
				if s := get(conv); s != nil {
					s.GoalLine, s.GoalState, s.Goal = goal.StatusLine(g), string(g.Status), goalInfo(g)
				}
			}
		}
	}
	if a.loops != nil {
		now := a.now()
		for _, t := range a.loops.List("") {
			if t.Status == loop.StatusStopped {
				continue
			}
			if s := get(t.Session); s != nil {
				s.Loops = append(s.Loops, t.Line(now))
				s.LoopTasks = append(s.LoopTasks, loopInfo(t))
			}
		}
	}
	if a.reminders != nil {
		for _, r := range a.reminders.List("") {
			if s := get(localConvOf(r.Character)); s != nil {
				s.Reminders = append(s.Reminders, reminderInfo(r, a.cfg.Reminders.DefaultTargets))
			}
		}
	}
	for _, conv := range a.auto.keys() {
		if s := get(conv); s != nil {
			s.AutoRunning = true
		}
	}
	out := make([]api.LocalStatus, 0, len(by))
	for _, conv := range a.typingConversations() {
		out = append(out, api.LocalStatus{Key: api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "private", Conversation: conv}, Typing: true})
	}
	for _, s := range by {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.Conversation < out[j].Key.Conversation })
	return out
}

func (r *DesktopReader) ReadLocalStatus(ctx context.Context, k api.ConversationKey) (api.LocalStatus, error) {
	_, done, err := r.desk.enterChat()
	if err != nil {
		return api.LocalStatus{}, err
	}
	defer done()
	if _, err := r.localCharacter(ctx, k); err != nil {
		return api.LocalStatus{}, err
	}
	for _, s := range r.desk.app.localStatuses() {
		if s.Key.Conversation == k.Conversation {
			return s, nil
		}
	}
	return api.LocalStatus{Key: k}, nil
}

type localStatusSub struct {
	out    chan api.Snapshot[[]api.LocalStatus]
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *localStatusSub) Updates() <-chan api.Snapshot[[]api.LocalStatus] { return s.out }
func (s *localStatusSub) Close()                                          { s.cancel(); <-s.done }

func (r *DesktopReader) WatchLocalStatus(ctx context.Context) (api.Subscription[[]api.LocalStatus], error) {
	base, done, err := r.desk.enterChat()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(base, cancel)
	s := &localStatusSub{out: make(chan api.Snapshot[[]api.LocalStatus], 1), cancel: cancel, done: make(chan struct{})}
	d := r.desk
	wake := make(chan struct{}, 1)
	d.mu.Lock()
	d.statusWakes[wake] = struct{}{}
	d.mu.Unlock()
	go func() {
		defer done()
		defer close(s.done)
		defer close(s.out)
		defer stop()
		defer cancel()
		defer func() { d.mu.Lock(); delete(d.statusWakes, wake); d.mu.Unlock() }()
		for version := uint64(1); ; version++ {
			snap := api.Snapshot[[]api.LocalStatus]{Version: version, Result: api.Result[[]api.LocalStatus]{State: api.Ready, Value: d.app.localStatuses()}}
			// 丢掉没读的旧快照，最新的始终可取
			select {
			case <-s.out:
			default:
			}
			s.out <- snap
			select {
			case <-wake:
			case <-ctx.Done():
				return
			}
		}
	}()
	return s, nil
}
