package turn

// "她看到了新事件"的通知（规格-Go后台 8.1"一轮结束的通知"那一行、四点八 3e；规格-记忆-v2 13.2.5）：对着写明的预期测

import (
	"context"
	"sync"
	"testing"
	"time"
)

func (h *harness) watchSeen() *notificationLog[Seen] {
	s := newNotificationLog[Seen]()
	h.use(fakePlugin{"seen", func(r *Registry) (func(), error) {
		r.OnSeen(s.add)
		return nil, nil
	}})
	return s
}

// actorOf 是库里这一条是谁说的
func (h *harness) actorOf(seq int64) string {
	var kind, text string
	h.st.Events.QueryRow(`SELECT actor_kind, text FROM events WHERE seq = ?`, seq).Scan(&kind, &text)
	return kind + "：" + text
}

// 主窗口按持久事件序通知；插话受理时即落库，不等模型这一步说完。
func TestSeenConsole(t *testing.T) {
	t.Parallel()
	var h *harness
	var once sync.Once
	h = newHarnessOpts(t, harnessOpts{
		replies: `[{"content":["一","二"],` + usage + `},` + textReply + `]`,
		mutate: func(c *Config) {
			c.OnDelta = func(string) { once.Do(func() { h.hub.Steer(h.c.CharacterID, "插一句") }) }
		},
	})
	s := h.watchSeen()
	h.send("你好")
	got := s.waitN(t, 4)
	want := []string{"owner：你好", "owner：插一句", "character：一二", "character：好的"}
	for i, e := range got {
		if i >= len(want) || h.actorOf(e.Seq) != want[i] || e.CharacterID != h.c.CharacterID || e.Conversation != consoleConv(h.c.CharacterID) ||
			e.Backfill || e.Stranger || e.FromSelf || e.Selftest {
			t.Fatalf("第 %d 条：%+v（库里是 %s），要 %v", i, e, h.actorOf(e.Seq), want)
		}
	}
}

// 渠道回合内核不发（中间件存了以后它发）；中间件发的原样送到，标签照带
func TestSeenChannel(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`})
	s := h.watchSeen()
	wait(t, h.inGroup("群里说一句", []string{"scene-group", "platform-qq"}))
	h.hub.NotifySeen(Seen{CharacterID: h.c.CharacterID, Conversation: groupConv, Seq: 42, Backfill: true, FromSelf: true})
	ctx, cancel := context.WithTimeout(t.Context(), barrierLimit)
	defer cancel()
	if err := h.hub.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	got := s.snapshot()
	if len(got) != 1 || got[0].Seq != 42 || !got[0].Backfill || !got[0].FromSelf || got[0].Stranger || got[0].Selftest {
		t.Fatalf("只收到中间件发的那一条、标签照带：%+v", got)
	}
}

// 订阅者卡住不拖慢回合；卡过时限记一行日志、接着送下一条；插件停掉以后不再收到
func TestSeenSlowSubscriber(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`,
		mutate: func(c *Config) { c.Hooks.Notify = 100 * time.Millisecond }})
	free := sync.OnceFunc(func() { close(release) })
	defer func() {
		free()
		ctx, cancel := context.WithTimeout(context.Background(), barrierLimit)
		defer cancel()
		if err := h.hub.Drain(ctx); err != nil {
			t.Error(err)
		}
	}()
	s := newNotificationLog[Seen]()
	h.use(fakePlugin{"slow", func(r *Registry) (func(), error) {
		r.OnSeen(func(ctx context.Context, e Seen) {
			s.add(ctx, e)
			// 故意忽略取消，直到测试放行；先证明通知超时后仍送下一条。
			<-release
		})
		return nil, nil
	}})
	start := time.Now()
	h.send("你好")
	if d := time.Since(start); d > barrierLimit { // 订阅者一直卡到测试放行：等它的改法这里过不去
		t.Fatalf("订阅者卡住拖慢了回合：%v", d)
	}
	s.waitN(t, 2) // 第一条卡过时限以后接着送第二条
	if !h.logged("处理新事件超时") {
		t.Fatalf("卡过时限要记一行：%v", h.logs)
	}
	free()
	h.hub.StopPlugin("slow")
	h.send("再说一句")
	ctx, cancel := context.WithTimeout(t.Context(), barrierLimit)
	defer cancel()
	if err := h.hub.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.snapshot(); len(got) != 2 {
		t.Fatalf("插件停掉以后不再收到：%d 条", len(got))
	}
}
