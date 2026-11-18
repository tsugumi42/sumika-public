package turn

// 规格 8.2 A8 的 C3：实例回收（第七节 T20，agent.ts evictOverflow、sweep）。对着写明的预期测

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/store"
)

// clock 是能往前拨的钟（回收按"上次用的时刻"挑）
type clock struct {
	mu  sync.Mutex
	cur time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}

func (c *clock) tick(d time.Duration) {
	c.mu.Lock()
	c.cur = c.cur.Add(d)
	c.mu.Unlock()
}

func newClock() *clock { return &clock{cur: time.Date(2026, 10, 27, 12, 0, 0, 0, time.Local)} }

func recycleGroup(i int) string { return fmt.Sprintf("qq:group:recycle-%d", i) }

// sayIn 在平台群 conv 里有人说一句、请她回；commit 为真时把她说的交回写库（不然这一档挂着等交回的那一段）
func (h *harness) sayIn(conv, text string, commit bool) *Result {
	h.t.Helper()
	seq, err := h.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "p1", Origin: "human", Kind: "message", Text: text,
		CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
	if err != nil {
		h.t.Fatal(err)
	}
	r := wait(h.t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: conv, Seqs: []int64{seq}, Text: text,
		Audience: audience.Audience{Channel: "qq", Conversation: conv, Scene: "group", PersonIDs: []string{"p1"}}, Addressed: true, Scope: "g"}))
	if commit {
		h.commitSaid(conv, r, nil)
	}
	h.queueIdle(conv)
	return r
}

// queueIdle 等这个会话的回合队列空下来（结果先交出、队列尾巴后关：回收只放队列空着的，不等的话下一句取实例时可能还没关）
func (h *harness) queueIdle(conv string) {
	h.t.Helper()
	h.hub.mu.Lock()
	tail := h.hub.turnTail[convKey(h.c.CharacterID, conv)]
	h.hub.mu.Unlock()
	if tail != nil {
		recv(h.t, (<-chan struct{})(tail), "会话 "+conv+" 的回合队列空下来")
	}
}

// liveConvs 是现在有实例的会话
func (h *harness) liveConvs() map[string]bool {
	h.hub.mu.Lock()
	defer h.hub.mu.Unlock()
	out := map[string]bool{}
	for _, l := range h.hub.live {
		out[l.in.Conversation] = true
	}
	return out
}

func replies(n int) string {
	r := make([]string, n)
	for i := range r {
		r[i] = textReply
	}
	return "[" + strings.Join(r, ",") + "]"
}

// 多于 maxLive 个就从最久没用的放起；放掉的那个会话下一轮从库里重建（rebuild 记 fresh），历史照样在
func TestRecycleOverMaxLive(t *testing.T) {
	t.Parallel()
	c := newClock()
	h := newHarnessOpts(t, harnessOpts{replies: replies(4), mutate: func(cfg *Config) { cfg.MaxLive, cfg.Now = 2, c.now }})
	g1, g2, g3 := recycleGroup(1), recycleGroup(2), recycleGroup(3)
	h.sayIn(g1, "第一个群的话", true)
	c.tick(time.Minute)
	h.sayIn(g2, "第二个群的话", true)
	c.tick(time.Minute)
	h.sayIn(g3, "第三个群的话", true)
	if lc := h.liveConvs(); len(lc) != 2 || lc[g1] || !lc[g2] || !lc[g3] {
		t.Fatalf("放掉最久没用的第一个群：%v", lc)
	}
	c.tick(time.Minute)
	h.sayIn(g1, "回到第一个群", true)
	if b := h.body(3); !strings.Contains(b, "第一个群的话") || h.lastRebuild() != "fresh" {
		t.Fatalf("放掉的会话从库里重建，历史照样在：%s", b)
	}
	if lc := h.liveConvs(); len(lc) != 2 || lc[g2] || !lc[g1] || !lc[g3] {
		t.Fatalf("这次放掉第二个群（最久没用的）：%v", lc)
	}
}

// 正在跑回合的、等着交回写库的不放（多于 maxLive 也先多着）；交回以后、跑完以后照常放
func TestRecycleKeepsBusy(t *testing.T) {
	t.Parallel()
	t.Run("等着交回写库的", func(t *testing.T) {
		t.Parallel()
		c := newClock()
		h := newHarnessOpts(t, harnessOpts{replies: replies(3), mutate: func(cfg *Config) { cfg.MaxLive, cfg.Now = 1, c.now }})
		g1, g2, g3 := recycleGroup(1), recycleGroup(2), recycleGroup(3)
		r := h.sayIn(g1, "第一个群的话", false)
		c.tick(time.Minute)
		h.sayIn(g2, "第二个群的话", true)
		if lc := h.liveConvs(); !lc[g1] || !lc[g2] {
			t.Fatalf("第一个群还等着交回写库，不放：%v", lc)
		}
		h.commitSaid(g1, r, nil) // 找得到它才交得回来
		c.tick(time.Minute)
		h.sayIn(g3, "第三个群的话", true)
		if lc := h.liveConvs(); len(lc) != 1 || !lc[g3] {
			t.Fatalf("交回以后照常放：%v", lc)
		}
	})
	t.Run("正在跑的", func(t *testing.T) {
		t.Parallel()
		entered, release := make(chan struct{}, 1), make(chan struct{})
		c := newClock()
		h := newHarnessOpts(t, harnessOpts{replies: replies(3), mutate: func(cfg *Config) { cfg.MaxLive, cfg.Now = 1, c.now },
			intercept: func(_ int, body string, _ http.ResponseWriter, r *http.Request) bool {
				if strings.Contains(body, "跑着的那句") {
					entered <- struct{}{}
					select {
					case <-release:
					case <-r.Context().Done():
					}
				}
				return false
			}})
		g1, g2 := recycleGroup(1), recycleGroup(2)
		h.sayIn(g1, "第一个群的话", true)
		c.tick(time.Minute)
		seq := h.storeIn(g1, "跑着的那句")
		out := h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: g1, Seqs: []int64{seq}, Text: "跑着的那句",
			Audience: audience.Audience{Channel: "qq", Conversation: g1, Scene: "group", PersonIDs: []string{"p1"}}, Addressed: true, Scope: "g"})
		recv(t, entered, "第一个群那一轮的请求")
		c.tick(time.Hour) // 闲了也不算：它在跑
		h.sayIn(g2, "第二个群的话", true)
		if lc := h.liveConvs(); !lc[g1] {
			t.Fatalf("第一个群正在跑，不放：%v", lc)
		}
		close(release)
		wait(t, out)
	})
}

// storeIn 在平台群 conv 里存一句群友的话
func (h *harness) storeIn(conv, text string) int64 {
	h.t.Helper()
	seq, err := h.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "p1", Origin: "human", Kind: "message", Text: text,
		CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
	if err != nil {
		h.t.Fatal(err)
	}
	return seq
}

// 闲了 idleAfter（默认 45 分钟）以上的放掉，没到的留着
func TestRecycleIdle(t *testing.T) {
	t.Parallel()
	c := newClock()
	h := newHarnessOpts(t, harnessOpts{replies: replies(3), mutate: func(cfg *Config) { cfg.Now = c.now }})
	g1, g2, g3 := recycleGroup(1), recycleGroup(2), recycleGroup(3)
	h.sayIn(g1, "第一个群的话", true)
	c.tick(44 * time.Minute)
	h.sayIn(g2, "第二个群的话", true)
	if lc := h.liveConvs(); !lc[g1] || !lc[g2] {
		t.Fatalf("闲了 44 分钟，留着：%v", lc)
	}
	c.tick(2 * time.Minute)
	h.sayIn(g3, "第三个群的话", true)
	if lc := h.liveConvs(); lc[g1] || !lc[g2] || !lc[g3] {
		t.Fatalf("闲了 46 分钟的放掉，2 分钟的留着：%v", lc)
	}
}

// "上次用的时刻"：一轮跑了很久的按结束的时刻算；手动压缩（不走一轮）取实例时也算用过
func TestRecycleLastUsed(t *testing.T) {
	t.Parallel()
	t.Run("一轮跑了很久", func(t *testing.T) {
		t.Parallel()
		c := newClock()
		h := newHarnessOpts(t, harnessOpts{replies: replies(2), mutate: func(cfg *Config) { cfg.Now = c.now },
			intercept: func(_ int, body string, _ http.ResponseWriter, _ *http.Request) bool {
				if strings.Contains(body, "跑很久的那句") {
					c.tick(46 * time.Minute) // 这一轮跑了 46 分钟
				}
				return false
			}})
		g1, g2 := recycleGroup(1), recycleGroup(2)
		h.sayIn(g1, "跑很久的那句", true)
		h.sayIn(g2, "第二个群的话", true)
		if lc := h.liveConvs(); !lc[g1] {
			t.Fatalf("刚跑完的不算闲：%v", lc)
		}
	})
	t.Run("手动压缩也算用过", func(t *testing.T) {
		t.Parallel()
		c := newClock()
		h := newHarnessOpts(t, harnessOpts{replies: replies(2), mutate: func(cfg *Config) { cfg.Now = c.now }})
		h.send("你好")
		c.tick(46 * time.Minute)
		if r, _ := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0); r.EpochID != "" {
			t.Fatalf("前面没东西可压：%+v", r)
		}
		h.sayIn(recycleGroup(1), "别的群来了一句", true)
		if !h.liveConvs()[consoleConv(h.c.CharacterID)] {
			t.Fatal("刚手动压过（闲了 46 分钟，压的时候取实例就重建了）的主窗口不算闲")
		}
	})
}

// 7.1 A42 在回收这条路上也成立：按过停止、还没提醒，实例闲久了被放掉，下一句重建时照样提醒；
// 中间点了"开始新话题"就不提醒（和没被回收时一样，提醒随实例丢掉）
func TestStopReminderSurvivesRecycle(t *testing.T) {
	t.Parallel()
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "接着说", true: "开始新话题"}[restart], func(t *testing.T) {
			t.Parallel()
			c := newClock()
			var h *harness
			var once sync.Once
			stopped := make(chan struct{})
			h = newHarnessOpts(t, harnessOpts{
				hold:    2,
				replies: `[` + textReply + `,{"content":["我先说","后半句"],` + usage + `},` + textReply + `,` + textReply + `]`,
				mutate: func(cfg *Config) {
					cfg.Now = c.now
					cfg.OnDelta = func(s string) {
						if s == "我先说" {
							once.Do(func() { go func() { h.hub.Stop(h.c.CharacterID); close(stopped) }() })
						}
					}
				},
			})
			h.send("先建实例")
			h.send("说点什么")
			recv(t, stopped, "按停止")
			h.queueIdle(consoleConv(h.c.CharacterID))
			c.tick(time.Hour)
			h.sayIn(recycleGroup(1), "别的群来了一句", true)
			if h.liveConvs()[consoleConv(h.c.CharacterID)] {
				t.Fatal("主窗口闲了一小时，该放掉了")
			}
			if restart {
				h.hub.Restart(h.c.CharacterID)
			}
			h.send("接着说")
			b := h.body(3)
			if strings.Contains(b, "上一轮被所有者手动停止") == restart || h.lastRebuild() != "fresh" {
				t.Fatalf("放掉以后重建的这一轮：开始新话题 %v，提醒被停止的应该 %v：%s", restart, !restart, b)
			}
		})
	}
}

// 这个会话自己闲了 IdleAfter 以上再回来：不接着用旧实例，从库里重建（Bun 的定时器这时已经放掉它了）；没到的接着用
func TestRecycleSelfIdle(t *testing.T) {
	t.Parallel()
	for _, idle := range []time.Duration{44 * time.Minute, 46 * time.Minute} {
		t.Run(idle.String(), func(t *testing.T) {
			t.Parallel()
			c := newClock()
			h := newHarnessOpts(t, harnessOpts{replies: replies(2), mutate: func(cfg *Config) { cfg.Now = c.now }})
			h.send("你好")
			c.tick(idle)
			h.send("回来了")
			if rebuilt := h.lastRebuild() == "fresh"; rebuilt != (idle > 45*time.Minute) || rebuilt == isPrefix(h.messages(0), h.messages(1)) {
				t.Fatalf("闲了 %v：重建 %v", idle, rebuilt)
			}
		})
	}
}
