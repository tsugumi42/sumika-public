package turn

// P2-9 第 2 步：本地私聊的空闲压缩（规格-私聊从上下文起点重建 第五节、验收 6～7；ADR-388）。
// 计时器换成手动触发的假的，时间往前拨用 prefixClock：不靠墙钟等待。

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/llm"
	"sumika-native/internal/store"
)

// fakeIdleTimers 接管空闲压缩的计时器：排上几个、哪个到点由测试决定
type fakeIdleTimers struct {
	mu    sync.Mutex
	list  []*fakeIdleTimer
	armed chan *fakeIdleTimer
}

type fakeIdleTimer struct {
	after   time.Duration
	f       func()
	stopped bool
}

func newFakeIdleTimers() *fakeIdleTimers {
	return &fakeIdleTimers{armed: make(chan *fakeIdleTimer, 64)}
}

func (c *fakeIdleTimers) AfterFunc(d time.Duration, f func()) func() {
	t := &fakeIdleTimer{after: d, f: f}
	c.mu.Lock()
	c.list = append(c.list, t)
	c.mu.Unlock()
	select {
	case c.armed <- t:
	default:
	}
	return func() {
		c.mu.Lock()
		t.stopped = true
		c.mu.Unlock()
	}
}

// next 等这一个回合结束后排上的计时器（回合里每排一次发一个）
func (c *fakeIdleTimers) next(t *testing.T) *fakeIdleTimer {
	t.Helper()
	select {
	case x := <-c.armed:
		return x
	case <-time.After(barrierLimit):
		t.Fatal("等不到空闲压缩的计时器")
		return nil
	}
}

func (c *fakeIdleTimers) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.list)
}

func (t *fakeIdleTimer) fire() { t.f() }

// newIdleHarness 装一个开了空闲压缩的 Hub（minTokens 由用例给），时间可以往前拨
func newIdleHarness(t *testing.T, o harnessOpts, minTokens int) (*harness, *prefixClock, *fakeIdleTimers) {
	t.Helper()
	clock := &prefixClock{}
	timers := newFakeIdleTimers()
	inner := o.mutate
	o.mutate = func(c *Config) {
		clock.now = c.Now()
		c.Now = clock.Now
		c.IdleCompact = true
		c.IdleCompactAfter = 50 * time.Minute
		c.IdleCompactMinTokens = minTokens
		c.AfterFunc = timers.AfterFunc
		// 8 轮短话也要能切出可压的前半段：保留原文只用 50 token，硬门槛远在实例大小之上（不触发提前压）
		c.Context = ContextLimits{Window: 200_000, SoftRatio: 0.7, HardRatio: 0.85, Reserve: 1, KeepTokens: 50}
		if inner != nil {
			inner(c)
		}
	}
	return newHarnessOpts(t, o), clock, timers
}

func (h *harness) compactNotices(conv string) []string {
	h.t.Helper()
	rows, err := h.st.Events.Query(`SELECT text FROM events WHERE conversation = ? AND kind = 'notice' AND origin = 'system:compact' ORDER BY seq`, conv)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var text string
		rows.Scan(&text)
		out = append(out, text)
	}
	return out
}

func (h *harness) compactRunTriggers() []string {
	h.t.Helper()
	rows, err := h.st.Trace.Query(`SELECT coalesce(json_extract(context_json, '$.compact.trigger'), '') FROM model_runs WHERE stage = 'compact' ORDER BY rowid`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var trigger string
		rows.Scan(&trigger)
		out = append(out, trigger)
	}
	return out
}

// fireIdle 让这一个计时器到点，等后台的活做完
func (h *harness) fireIdle(t *testing.T, timer *fakeIdleTimer) {
	t.Helper()
	timer.fire()
	h.hub.Idle()
}

// 验收 6：闲置满时长、超过下限的排一次；聊天里不提示，运行页记 trigger idle；下一轮换上新快照
func TestIdleCompactRuns(t *testing.T) {
	t.Parallel()
	h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `,` + textReply + `]`}, 1000)
	h.seedRounds(8)
	h.send("先建实例")
	timer := timers.next(t)
	if timer.after != 50*time.Minute {
		t.Fatalf("默认闲 50 分钟才排：%v", timer.after)
	}
	if c := h.countBodies(compactMarker); c != 0 {
		t.Fatalf("没到点不该压：请求了 %d 次摘要", c)
	}
	h.fireIdle(t, timer)
	eps := h.epochs()
	if len(eps) != 1 || eps[0].status != "ok" || eps[0].trigger != "idle" {
		t.Fatalf("空闲压缩写一行 trigger idle 的快照：%+v", eps)
	}
	if got := h.compactRunTriggers(); len(got) != 1 || got[0] != "idle" {
		t.Fatalf("运行页记 trigger idle：%v", got)
	}
	if n := len(h.compactNotices(consoleConv(h.c.CharacterID))); n != 0 {
		t.Fatalf("正常压缩聊天里不提示，记了 %d 行", n)
	}
	h.send("接着说")
	if h.lastRebuild() != "compact" {
		t.Fatalf("下一轮换上新快照：%q", h.lastRebuild())
	}
	if b := h.body(2); !strings.Contains(b, "前面的聊天已经整理成下面的摘要") {
		t.Fatalf("下一轮的请求带上了摘要：%s", b)
	}
}

// 验收 6：没满时长的、不够大的、已经有更新快照的都不排
func TestIdleCompactConditions(t *testing.T) {
	t.Parallel()
	t.Run("没到时长不压", func(t *testing.T) {
		t.Parallel()
		h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `]`}, 1000)
		h.seedRounds(8)
		h.send("先建实例")
		timers.next(t) // 排上了，但不到点
		if c := h.countBodies(compactMarker); c != 0 || len(h.epochs()) != 0 {
			t.Fatalf("没到点不该压：%d 次，%+v", c, h.epochs())
		}
	})
	t.Run("不够大不排", func(t *testing.T) {
		t.Parallel()
		h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `]`}, 1<<20)
		h.seedRounds(8)
		h.send("先建实例")
		if n := timers.count(); n != 0 {
			t.Fatalf("实例小于 minTokens 不该排：排了 %d 个", n)
		}
		if c := h.countBodies(compactMarker); c != 0 || len(h.epochs()) != 0 {
			t.Fatalf("不该压：%d 次，%+v", c, h.epochs())
		}
	})
	t.Run("已经有更新的快照", func(t *testing.T) {
		t.Parallel()
		h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `]`}, 1000)
		owners := h.seedRounds(8)
		h.send("先建实例")
		timer := timers.next(t)
		// 别的路径已经写好一份更新的快照，实例还没换上：同一份内容不再压一次
		if err := h.st.AddEpoch(store.Epoch{ID: "newer", Conversation: consoleConv(h.c.CharacterID), CharacterID: h.c.CharacterID,
			FromSeq: owners[0], Summary: "更早的一份摘要", Kind: "chat", Trigger: "manual", Status: "ok", CreatedAt: h.hub.now().UnixMilli()}); err != nil {
			t.Fatal(err)
		}
		h.fireIdle(t, timer)
		if c := h.countBodies(compactMarker); c != 0 {
			t.Fatalf("有更新的快照不该再压一次：%d 次", c)
		}
		if eps := h.epochs(); len(eps) != 1 || eps[0].id != "newer" {
			t.Fatalf("不写第二份快照：%+v", eps)
		}
	})
}

// 验收 6：失败后 6 小时内不重试；满了 6 小时才再压
func TestIdleCompactFailureBackoff(t *testing.T) {
	t.Parallel()
	h, clock, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `,` + shortSummary + `,` + textReply + `,` + textReply + `,` + summaryReply + `]`}, 1000)
	h.seedRounds(8)
	h.send("先建实例")
	h.fireIdle(t, timers.next(t))
	if eps := h.epochs(); len(eps) != 1 || eps[0].status != "failed" || eps[0].trigger != "idle" {
		t.Fatalf("第一次空闲压没成、写一行失败：%+v", eps)
	}
	clock.add(time.Hour)
	before := timers.count()
	h.send("一小时以后")
	if got := timers.count(); got != before {
		t.Fatalf("失败后 6 小时内连计时都不排：排了 %d 个（原来 %d 个）", got, before)
	}
	if c := h.countBodies(compactMarker); c != 1 {
		t.Fatalf("失败后 6 小时内不再试：压了 %d 次", c)
	}
	clock.add(6 * time.Hour)
	before = timers.count()
	h.send("七小时以后")
	if got := timers.count(); got != before+1 {
		t.Fatalf("满 6 小时以后重新排一个计时：%d 个（原来 %d 个）", got, before)
	}
	h.fireIdle(t, timers.next(t))
	eps := h.epochs()
	if c := h.countBodies(compactMarker); c != 2 || len(eps) != 2 || eps[1].status != "ok" {
		t.Fatalf("满 6 小时以后再压一次、这次成了：%d 次，%+v", c, eps)
	}
}

// 新消息来了就取消上一个空闲计时，到下一条结束重新排
func TestIdleCompactNewMessageResetsTimer(t *testing.T) {
	t.Parallel()
	h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + summaryReply + `]`}, 1000)
	h.seedRounds(8)
	h.send("第一句")
	first := timers.next(t)
	h.send("第二句")
	second := timers.next(t)
	if first == second {
		t.Fatal("第二条消息应该重新排一个计时")
	}
	h.fireIdle(t, first)
	if c := h.countBodies(compactMarker); c != 0 || len(h.epochs()) != 0 {
		t.Fatalf("被取消的计时器不该压：%d 次，%+v", c, h.epochs())
	}
	h.fireIdle(t, second)
	if eps := h.epochs(); len(eps) != 1 || eps[0].trigger != "idle" {
		t.Fatalf("最后一条结束以后排上的才压：%+v", eps)
	}
}

// 验收 6：和提前压共用登记，同一份内容不各跑一次
func TestIdleCompactDoesNotRepeatEarly(t *testing.T) {
	t.Parallel()
	h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + summaryReply + `]`}, 1000)
	h.nearEarly()
	_ = timers.next(t) // "先建实例"那轮排的
	h.send("提前")
	h.hub.Idle()
	if eps := h.epochs(); len(eps) != 1 || eps[0].trigger != "early" {
		t.Fatalf("提前压先写了一份：%+v", eps)
	}
	h.fireIdle(t, timers.next(t))
	if c := h.countBodies(compactMarker); c != 1 {
		t.Fatalf("同一份内容不再压一次：压了 %d 次", c)
	}
	if eps := h.epochs(); len(eps) != 1 || eps[0].trigger != "early" {
		t.Fatalf("空闲压不再写第二份：%+v", eps)
	}
}

// 验收 7：压缩期间来了新消息不丢，切点之后的消息照常接上
func TestIdleCompactNewMessagesSurvive(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + summaryReply + `,` + textReply + `]`, intercept: holdCompact(entered, release)}, 1000)
	h.seedRounds(8)
	h.send("先建实例")
	timer := timers.next(t)
	timer.fire()
	recv(t, entered, "空闲压缩的摘要请求")
	h.send("压缩的时候说的")
	close(release)
	h.hub.Idle()
	eps := h.epochs()
	if len(eps) != 1 || eps[0].status != "ok" || eps[0].trigger != "idle" {
		t.Fatalf("压完写一份快照：%+v", eps)
	}
	h.send("后来")
	b := h.body(3)
	if !strings.Contains(b, "前面的聊天已经整理成下面的摘要") || !strings.Contains(b, "压缩的时候说的") || !strings.Contains(b, "后来") {
		t.Fatalf("切点之后来的消息都在：%s", b)
	}
	if h.ownerSaid(consoleConv(h.c.CharacterID), "压缩的时候说的") != 1 {
		t.Fatal("压缩期间的那句存进库一次")
	}
}

// 验收 7：压缩期间开新话题、检查点回退、换模型，都不写快照；留一行作废
func TestIdleCompactDiscardsWhenVersionChanges(t *testing.T) {
	t.Parallel()
	newCase := func(t *testing.T, prepare func(*harness)) (*harness, *fakeIdleTimers, chan struct{}, chan struct{}) {
		t.Helper()
		entered, release := make(chan struct{}, 1), make(chan struct{})
		h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `,` + textReply + `]`, intercept: holdCompact(entered, release)}, 1000)
		if prepare != nil {
			prepare(h)
		}
		h.seedRounds(8)
		h.send("先建实例")
		timer := timers.next(t)
		timer.fire()
		recv(t, entered, "空闲压缩的摘要请求")
		return h, timers, entered, release
	}
	check := func(t *testing.T, h *harness, release chan struct{}) {
		t.Helper()
		close(release)
		h.hub.Idle()
		for _, e := range h.epochs() {
			if e.status == "ok" {
				t.Fatalf("版本变了不写快照：%+v", e)
			}
		}
		if !h.logged("作废") {
			t.Fatalf("留一行作废：%v", h.logs)
		}
		if got := h.compactRunTriggers(); len(got) != 1 || got[0] != "idle" {
			t.Fatalf("那次摘要调用仍记进运行页：%v", got)
		}
	}
	t.Run("开新话题", func(t *testing.T) {
		t.Parallel()
		h, _, _, release := newCase(t, nil)
		if err := h.hub.StartNewTopic(h.c.CharacterID, consoleConv(h.c.CharacterID)); err != nil {
			t.Fatal(err)
		}
		check(t, h, release)
	})
	t.Run("检查点回退", func(t *testing.T) {
		t.Parallel()
		h, _, _, release := newCase(t, nil)
		// 检查点回退走 RestartIn：丢掉实例、会话代次往前走
		h.hub.RestartIn(h.c.CharacterID, consoleConv(h.c.CharacterID))
		check(t, h, release)
	})
	t.Run("换模型", func(t *testing.T) {
		t.Parallel()
		var switched atomic.Bool
		h, _, _, release := newCase(t, func(h *harness) {
			// 开始压缩时是 v1；摘要请求还在路上时换成 v2
			h.hub.Cfg.ModelSnapshot = func() (Config, []llm.Model) {
				m := map[string]string{h.c.CharacterID: h.c.Characters[h.c.CharacterID].Model}
				v := "v1"
				if switched.Load() {
					m[h.c.CharacterID], v = "switched/model", "v2"
				}
				return Config{Models: m, ModelVersion: v, Context: h.hub.Cfg.Context}, h.hub.Models
			}
		})
		switched.Store(true)
		check(t, h, release)
	})
}

// 作废的压缩（写库之前版本对不上）不算失败：不写失败行，下一次空闲压缩也不为它退避 6 小时
func TestIdleCompactVoidNotFailure(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var switched atomic.Bool
	h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `]`, intercept: holdCompact(entered, release)}, 1000)
	h.hub.Cfg.ModelSnapshot = func() (Config, []llm.Model) {
		m := map[string]string{h.c.CharacterID: h.c.Characters[h.c.CharacterID].Model}
		v := "v1"
		if switched.Load() {
			m[h.c.CharacterID], v = "switched/model", "v2"
		}
		return Config{Models: m, ModelVersion: v, Context: h.hub.Cfg.Context}, h.hub.Models
	}
	h.seedRounds(8)
	h.send("先建实例")
	timer := timers.next(t)
	timer.fire()
	recv(t, entered, "空闲压缩的摘要请求")
	// 摘要请求还在路上时换了模型：写库之前核对不过，这一份作废
	switched.Store(true)
	close(release)
	h.hub.Idle()
	if !h.logged("作废") {
		t.Fatalf("版本变了要留一行作废：%v", h.logs)
	}
	if eps := h.epochs(); len(eps) != 0 {
		t.Fatalf("作废的那次不写快照，也不写失败行：%+v", eps)
	}
	conv := consoleConv(h.c.CharacterID)
	h.hub.mu.Lock()
	_, failed := h.hub.compactFailures[convKey(h.c.CharacterID, conv)]
	h.hub.mu.Unlock()
	if failed {
		t.Fatal("作废不算失败：不该记失败退避")
	}
	if h.hub.idleFailedRecently(h.c.CharacterID, conv) {
		t.Fatal("作废以后空闲压缩不该退避 6 小时")
	}
}

// 验收 7：程序关闭时取消在跑的压缩、不提交；关闭以后不再新开
func TestIdleCompactShutdown(t *testing.T) {
	t.Parallel()
	t.Run("在跑的取消、不提交", func(t *testing.T) {
		t.Parallel()
		entered := make(chan struct{}, 1)
		h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `]`, intercept: func(_ int, body string, _ http.ResponseWriter, r *http.Request) bool {
			if strings.Contains(body, compactMarker) {
				entered <- struct{}{}
				<-r.Context().Done()
				return true
			}
			return false
		}}, 1000)
		h.seedRounds(8)
		h.send("先建实例")
		timer := timers.next(t)
		timer.fire()
		recv(t, entered, "空闲压缩的摘要请求")
		h.cancel()
		h.hub.Idle()
		if eps := h.epochs(); len(eps) != 0 {
			t.Fatalf("后台在关不提交快照：%+v", eps)
		}
	})
	t.Run("关闭以后不再新开", func(t *testing.T) {
		t.Parallel()
		h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `]`}, 1000)
		h.seedRounds(8)
		h.send("先建实例")
		timer := timers.next(t)
		h.cancel()
		h.hub.Idle()
		before := h.countBodies(compactMarker)
		timer.fire()
		h.hub.Idle()
		if got := h.countBodies(compactMarker); got != before {
			t.Fatalf("关了以后不再新开压缩：多出 %d 次", got-before)
		}
		if eps := h.epochs(); len(eps) != 0 {
			t.Fatalf("关了不写快照：%+v", eps)
		}
	})
}

// 场合：只有本地私聊排空闲压缩；主人 QQ 私聊、本地群聊、QQ 群都不排
func TestIdleCompactOnlyLocalPrivate(t *testing.T) {
	t.Parallel()
	char := "suzu"
	localGroup := "console:group:made-name"
	cases := []struct {
		name string
		in   Input
		want bool
	}{
		{"本地私聊", consoleInput(char, 0, ""), true},
		{"主人QQ私聊", Input{CharacterID: char, Conversation: ownerQQConv, Audience: qqOwnerAud()}, false},
		{"本地群聊", Input{CharacterID: char, Conversation: localGroup, Audience: audience.Audience{Channel: "console", Conversation: localGroup, Scene: "group", PrivateOK: true}}, false},
		{"QQ群", Input{CharacterID: char, Conversation: groupConv, Audience: groupAud}, false},
	}
	for _, tc := range cases {
		if got := idleCompactLocal(Config{}, tc.in); got != tc.want {
			t.Errorf("%s：idleCompactLocal=%v，应该 %v", tc.name, got, tc.want)
		}
	}
	t.Run("本地群聊不排", func(t *testing.T) {
		t.Parallel()
		h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `]`}, 1)
		seq := h.storeConv(localGroup, "console", "owner", "owner", "群里说一句")
		wait(t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: localGroup, Seqs: []int64{seq}, Text: "群里说一句",
			Audience: audience.Audience{Channel: "console", Conversation: localGroup, Scene: "group", PrivateOK: true, OwnerAsked: true}, Addressed: true, Scope: "g"}))
		if timers.count() != 0 {
			t.Fatalf("本地群聊不排空闲压缩：排了 %d 个", timers.count())
		}
	})
	t.Run("主人QQ私聊不排", func(t *testing.T) {
		t.Parallel()
		h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `]`}, 1)
		h.seedQQ(4)
		seq := h.storeConv(ownerQQConv, "qq", "owner", "owner", "在吗")
		wait(t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: ownerQQConv, Seqs: []int64{seq}, Text: "在吗", Audience: qqOwnerAud(), Addressed: true, Scope: "owner"}))
		if timers.count() != 0 {
			t.Fatalf("主人 QQ 私聊不排空闲压缩：排了 %d 个", timers.count())
		}
	})
	t.Run("QQ群不排", func(t *testing.T) {
		t.Parallel()
		h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `]`}, 1)
		respond := h.seedGroup()
		respond("群里说一句")
		if timers.count() != 0 {
			t.Fatalf("QQ 群不排空闲压缩：排了 %d 个", timers.count())
		}
	})
}

// 配置：把 minTokens 改大，原来会排的就不排了
func TestIdleCompactMinTokensChangesBehavior(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, minTokens int) (*harness, *fakeIdleTimers) {
		t.Helper()
		h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `]`}, minTokens)
		h.seedRounds(8)
		h.send("先建实例")
		return h, timers
	}
	t.Run("下限低", func(t *testing.T) {
		t.Parallel()
		h, timers := run(t, 1000)
		h.fireIdle(t, timers.next(t))
		if eps := h.epochs(); len(eps) != 1 || eps[0].trigger != "idle" {
			t.Fatalf("超过下限的排一次：%+v", eps)
		}
	})
	t.Run("下限调大", func(t *testing.T) {
		t.Parallel()
		h, timers := run(t, 1<<20)
		if n := timers.count(); n != 0 {
			t.Fatalf("下限调大以后不排：排了 %d 个", n)
		}
		if eps := h.epochs(); len(eps) != 0 {
			t.Fatalf("不压：%+v", eps)
		}
	})
}

// 验收 7：同步等压缩、裁剪丢了内容各在本地私聊里提一行；群聊不提
func TestFitHardChatLines(t *testing.T) {
	t.Parallel()
	t.Run("同步等压缩", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `,` + textReply + `]`})
		h.nearHard()
		h.send("过了门槛")
		got := h.compactNotices(consoleConv(h.c.CharacterID))
		if len(got) != 1 || !strings.Contains(got[0], "回复前先整理了一下") {
			t.Fatalf("回复前同步等压缩提一行：%v", got)
		}
	})
	t.Run("裁剪丢了内容", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + shortSummary + `,` + textReply + `]`})
		h.nearHard()
		h.send("过了门槛")
		got := h.compactNotices(consoleConv(h.c.CharacterID))
		if len(got) != 1 || !strings.Contains(got[0], "放不下了") || !strings.Contains(got[0], "条聊天") {
			t.Fatalf("裁剪兜底丢内容提一行：%v", got)
		}
	})
	t.Run("群聊不提", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`, mutate: shortCompactHook})
		respond := h.seedGroup()
		respond("先来一句")
		h.setHard(sizeOf(h.liveIn(groupConv, "g")) + 300)
		respond("过了门槛")
		if got := h.compactNotices(groupConv); len(got) != 0 {
			t.Fatalf("群聊不写这一行：%v", got)
		}
	})
}

// 正常压缩（空闲压）聊天里没有提示；留痕在运行页
func TestCompactBackgroundNoChatLine(t *testing.T) {
	t.Parallel()
	h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `]`}, 1000)
	h.seedRounds(8)
	h.send("先建实例")
	h.fireIdle(t, timers.next(t))
	if got := h.compactNotices(consoleConv(h.c.CharacterID)); len(got) != 0 {
		t.Fatalf("空闲压不往聊天里写：%v", got)
	}
	var trigger string
	if err := h.st.Trace.QueryRow(`SELECT coalesce(json_extract(context_json, '$.compact.trigger'), '') FROM model_runs WHERE stage = 'compact' ORDER BY rowid DESC LIMIT 1`).Scan(&trigger); err != nil || trigger != "idle" {
		t.Fatalf("运行页有留痕：%q %v", trigger, err)
	}
}
