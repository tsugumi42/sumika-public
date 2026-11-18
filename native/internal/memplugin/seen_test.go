package memplugin

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/turn"
)

func seenWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("通知或整理未到达")
	}
}

func TestSeenConsoleWakesSummary(t *testing.T) {
	t.Parallel()
	k := newKernel(t)
	r := newRig(t, func(o *Options) { o.Threshold = 1 })
	w := r.mems[character]
	conv := "console:" + character
	w.initCursor(t, conv, 0)
	w.ev.add(conv, "编造的待整理消息")
	called := make(chan struct{}, 1)
	w.gen.block = func(context.Context) error { called <- struct{}{}; return nil }
	if err := k.hub.Use(r.p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.hub.StopPlugin(ID) })
	k.send("编造的主窗口消息")
	seenWait(t, called)
}

func TestSeenChannelNotifications(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, conv string
		tag        func(*turn.Seen)
		wake       bool
	}{
		{"local-group", "console:suzu:group:demo", nil, true},
		{"qq-private", "qq:private:20001:60009", nil, true},
		{"qq-group", "qq:group:5009001", nil, true},
		{"backfill", "qq:group:5009001", func(e *turn.Seen) { e.Backfill = true }, false},
		{"selftest", "qq:group:5009001", func(e *turn.Seen) { e.Selftest = true }, false},
		{"stranger", "qq:private:20001:60009", func(e *turn.Seen) { e.Stranger = true }, false},
		{"from-self", "qq:group:5009001", func(e *turn.Seen) { e.FromSelf = true }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(o *Options) { o.Threshold = 1 })
			w := r.mems[character]
			w.initCursor(t, tc.conv, 0)
			w.ev.add(tc.conv, "编造的待整理消息")
			called := make(chan struct{}, 1)
			w.gen.block = func(context.Context) error { called <- struct{}{}; return nil }
			armed := make(chan struct{}, 4)
			r.p.opt.After = func(d time.Duration, f func()) func() bool {
				stop := r.clock.After(d, f)
				armed <- struct{}{}
				return stop
			}
			hub := turn.NewHub(context.Background(), turn.Config{Logf: func(string, ...any) {}}, nil, nil, nil)
			if err := hub.Use(r.p); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { hub.StopPlugin(ID) })
			e := turn.Seen{CharacterID: character, Conversation: tc.conv}
			if tc.tag != nil {
				tc.tag(&e)
			}
			hub.NotifySeen(e)
			if tc.wake {
				seenWait(t, called)
				return
			}
			// 同一订阅者顺序处理：收到后面的普通通知即证明前一条已经处理完。
			hub.NotifySeen(turn.Seen{CharacterID: character, Conversation: "qq:group:5009002"})
			seenWait(t, armed)
			lf := r.p.current()
			lf.s.mu.Lock()
			_, exists := lf.s.slots[skey{character, tc.conv}]
			_, barrierSeen := lf.s.slots[skey{character, "qq:group:5009002"}]
			lf.s.mu.Unlock()
			if !barrierSeen {
				t.Fatal("栅栏通知没被处理，不能把没处理当成被过滤")
			}
			if exists {
				t.Fatal("被过滤的通知唤醒了整理")
			}
			if w.gen.count() != 0 {
				t.Fatal("被过滤的通知调用了模型")
			}
		})
	}
}

// fencePlugin 只做一件事：收到"她看到了新事件"就往通道里记一笔。订阅者各有各的协程，它只能证明通知已经送到了内核的订阅端，
// 证明不了别的订阅者处理完了，所以停掉之后的断言另外直接同步地调记忆插件的入口。
type fencePlugin struct{ got chan string }

func (f *fencePlugin) ID() string { return "fence" }
func (f *fencePlugin) Setup(r *turn.Registry) (func(), error) {
	r.OnSeen(func(_ context.Context, e turn.Seen) { f.got <- e.Conversation })
	return nil, nil
}

func TestSeenAfterStop(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *Options) { o.Threshold = 1 })
	w := r.mems[character]
	conv := "qq:group:5009001"
	w.initCursor(t, conv, 0)
	w.ev.add(conv, "编造的待整理消息")
	armed := make(chan struct{}, 8)
	r.p.opt.After = func(d time.Duration, f func()) func() bool {
		stop := r.clock.After(d, f)
		armed <- struct{}{}
		return stop
	}
	hub := turn.NewHub(context.Background(), turn.Config{Logf: func(string, ...any) {}}, nil, nil, nil)
	if err := hub.Use(r.p); err != nil {
		t.Fatal(err)
	}
	fence := &fencePlugin{got: make(chan string, 4)}
	if err := hub.Use(fence); err != nil {
		t.Fatal(err)
	}
	old := r.p.current()
	hub.StopPlugin(ID)

	// 经内核发：fence 收到说明这条通知已经到了订阅端，记忆的订阅（若还在）也排上了
	hub.NotifySeen(turn.Seen{CharacterID: character, Conversation: conv})
	select {
	case <-fence.got:
	case <-time.After(5 * time.Second):
		t.Fatal("通知未到达")
	}
	// 同步地走记忆插件的两个入口：新来的调用不收，也不排任何东西
	r.p.Notify(character, conv)
	old.notify(character, conv)
	r.p.Summarize(character, conv, true)
	old.request(character, conv, true)

	old.s.mu.Lock()
	slots := len(old.s.slots)
	old.s.mu.Unlock()
	if slots != 0 {
		t.Fatalf("停止后排程里出现了 %d 个槽", slots)
	}
	if len(armed) != 0 {
		t.Fatal("停止后还排了计时器")
	}
	if r.p.current() != nil {
		t.Fatal("停止后插件仍认着旧的生命周期")
	}
	if w.gen.count() != 0 || w.rows(t, "memory_jobs") != 0 {
		t.Fatal("停止后仍整理或写库")
	}
}

// 停掉再启用同一个插件：新生命周期单独响应，旧的订阅由内核随停掉撤销（memplugin 自己没有登记撤销，
// 改坏不了；这里在行为层面证明一条通知只排一个计时器、只整理一次）
func TestSeenRestartWakesOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *Options) { o.Threshold = 1 })
	w := r.mems[character]
	conv := "qq:group:5009001"
	w.initCursor(t, conv, 0)
	w.ev.add(conv, "编造的待整理消息")
	called := make(chan struct{}, 4)
	w.gen.block = func(context.Context) error { called <- struct{}{}; return nil }
	armed := make(chan struct{}, 8)
	r.p.opt.After = func(d time.Duration, f func()) func() bool {
		stop := r.clock.After(d, f)
		armed <- struct{}{}
		return stop
	}
	hub := turn.NewHub(context.Background(), turn.Config{Logf: func(string, ...any) {}}, nil, nil, nil)
	if err := hub.Use(r.p); err != nil {
		t.Fatal(err)
	}
	old := r.p.current()
	hub.StopPlugin(ID)
	if err := hub.Use(r.p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hub.StopPlugin(ID) })
	if r.p.current() == old {
		t.Fatal("重新启用没有换新的生命周期")
	}
	hub.NotifySeen(turn.Seen{CharacterID: character, Conversation: conv})
	seenWait(t, called)
	hub.NotifySeen(turn.Seen{CharacterID: character, Conversation: "qq:group:5009002"})
	seenWait(t, armed)
	if n := w.gen.count(); n != 1 {
		t.Fatalf("一条通知整理了 %d 次", n)
	}
	old.s.mu.Lock()
	oldSlots := len(old.s.slots)
	old.s.mu.Unlock()
	if oldSlots != 0 {
		t.Fatal("旧的生命周期被唤醒了")
	}
}
