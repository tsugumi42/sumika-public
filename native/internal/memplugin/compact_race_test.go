package memplugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/memory"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

func raceReceive[T any](t *testing.T, ch <-chan T, name string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("等待 %s 超时\n%s", name, buf[:n])
		var zero T
		return zero
	}
}
func raceCleanup(t *testing.T, k *kernel, free func()) {
	t.Helper()
	free()
	for _, step := range []struct {
		name string
		run  func()
	}{{"StopPlugin", func() { k.hub.StopPlugin(ID) }}, {"Idle", k.hub.Idle}} {
		done := make(chan struct{})
		go func() { step.run(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			t.Errorf("清理 %s 超时\n%s", step.name, buf[:n])
		}
	}
}

type raceHookResult struct {
	upTo    int64
	limited bool
	err     error
}

type racePlugin struct {
	*Plugin
	results chan raceHookResult
	live    atomic.Int32
	peak    atomic.Int32
}

func (p *racePlugin) Setup(r *turn.Registry) (func(), error) {
	lf := p.start()
	r.OnContext(lf.inputContext)
	r.OnContext(lf.context)
	r.OnBeforeCompact(func(ctx context.Context, e turn.BeforeCompact) (int64, bool, error) {
		n := p.live.Add(1)
		for old := p.peak.Load(); n > old && !p.peak.CompareAndSwap(old, n); old = p.peak.Load() {
		}
		defer p.live.Add(-1)
		u, limited, err := lf.beforeCompact(ctx, e)
		p.results <- raceHookResult{u, limited, err}
		return u, limited, err
	})
	p.mu.Lock()
	p.cur = lf
	p.mu.Unlock()
	lf.startScan()
	return func() { p.mu.Lock(); p.cur = nil; p.mu.Unlock(); lf.stop() }, nil
}

// raceTokens 照 turn 里 Sumika 自己的估算：宽字（码位 >= U+2E80）一个算一个，其余 3.5 个字一个
func raceTokens(s string) int {
	wide := 0
	for _, r := range s {
		if r >= 0x2e80 {
			wide++
		}
	}
	return wide + int(math.Ceil(float64(store.JSLen(s)-wide)/3.5))
}

const (
	raceNextText   = "再说紫色风筝"
	raceReplyText  = "在呢～"
	raceCtxAllow   = 300 // 记忆插件往上下文里加的参考信息；探测实例不装插件，这里留出来
	raceMinMargin  = 300 // 两条线各自至少要有的余量
	raceHardAhead  = 600 // fitHard 给回复预留的 token
	raceSoftBefore = 0.8 // 提前压的线占硬门槛的比例
)

// raceWindow 先用同一套提示词、工具和历史建一个探测实例发一轮，量出第一轮后的估算，
// 再把窗口定在让这个估算落在"提前压的线"和"硬门槛"正中间的位置。
// 固定前缀（系统提示 + 工具定义）会随新工具变长，窗口不写死数；两边余量不够就直接报错，不等到超时。
func raceWindow(t *testing.T, seed func(k *kernel)) int {
	t.Helper()
	probe := newKernelWith(t, nil)
	seed(probe)
	if o := raceReceive(t, probe.hub.Send(character, "紫色风筝怎么样"), "探测实例的一轮"); o.Err != nil {
		t.Fatal(o.Err)
	}
	est := 4 + raceTokens(raceReplyText) + raceCtxAllow
	for _, m := range probe.request(0) {
		est += 4 + raceTokens(m.Text)
	}
	probe.mu.Lock()
	var body struct {
		Tools json.RawMessage `json:"tools"`
	}
	json.Unmarshal([]byte(probe.bodies[0]), &body)
	probe.mu.Unlock()
	est += raceTokens(string(body.Tools))
	next := raceTokens(raceNextText)
	// 要 est 在 [0.8H, H-600-next] 的正中：H = (2est + 600 + next) / (1+0.8)
	hard := int(math.Round((2*float64(est) + raceHardAhead + float64(next)) / (1 + raceSoftBefore)))
	low := float64(est) - raceSoftBefore*float64(hard)
	high := float64(hard) - float64(est+raceHardAhead+next)
	if low < raceMinMargin || high < raceMinMargin {
		t.Fatalf("窗口定不出两边都有余量的位置：第一轮后估算 %d，硬门槛 %d，离提前压的线 %.0f、离硬门槛 %.0f，都要至少 %d", est, hard, low, high, raceMinMargin)
	}
	t.Logf("探测：第一轮后估算 %d，硬门槛 %d，离提前压的线 %.0f，离硬门槛 %.0f", est, hard, low, high)
	return hard + 1 // Reserve 1、HardRatio 1：硬门槛 = 窗口 - 1
}

func TestRaceCompactNextTurn(t *testing.T) {
	t.Parallel()
	clock := newClock()
	conv := "console:" + character
	seed := func(k *kernel, add func(seq int64, who, id, text string)) int64 {
		var lastSeq int64
		for range 8 {
			for _, who := range []string{"owner", "character"} {
				id := who
				if who == "character" {
					id = character
				}
				text := strings.Repeat("甲", 600)
				seq, err := k.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: who, ActorID: id, Origin: who, Kind: "message", Text: text, CreatedAt: clock.Now().UnixMilli()}, []string{character})
				if err != nil {
					t.Fatal(err)
				}
				lastSeq = seq
				if add != nil {
					add(seq, who, id, text)
				}
			}
		}
		return lastSeq
	}
	window := raceWindow(t, func(k *kernel) { seed(k, nil) })
	k := newKernelWith(t, func(c *turn.Config) {
		c.Context = turn.ContextLimits{Window: window, Reserve: 1, HardRatio: 1, KeepTokens: 50}
	})
	w := newWritable(t, clock, character, k.logs, func(db *sql.DB) {
		if _, err := db.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, status, scope, created_at) VALUES ('race-fact', 'promise', '约好一起种蓝色花朵', 'owner', 'public', 'active', 'public', ?)`, clock.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, status, scope, created_at) VALUES ('race-ordinary', 'fact', '主人喜欢紫色风筝', 'owner', 'public', 'active', 'public', ?)`, clock.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	})
	lastSeq := seed(k, func(seq int64, who, id, text string) {
		w.ev.add(conv, text, func(e *memory.Event) { e.Seq, e.ActorKind, e.ActorID, e.Origin = seq, who, id, who })
	})
	w.initCursor(t, conv, 0)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	free := freeOnce(t, release)
	w.gen.block = func(context.Context) error {
		select {
		case entered <- struct{}{}:
		case <-release:
			return nil
		}
		<-release
		return nil
	}
	p := &racePlugin{Plugin: New(Options{Memories: only(w.m), Now: clock.Now, After: clock.After, Logf: k.logs.logf}), results: make(chan raceHookResult, 4)}
	if err := k.hub.Use(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raceCleanup(t, k, free) })
	if o := raceReceive(t, k.hub.Send(character, "紫色风筝怎么样"), "第一轮"); o.Err != nil {
		t.Fatal(o.Err)
	}
	raceReceive(t, entered, "提前压缩的整理")
	if o := raceReceive(t, k.hub.Send(character, raceNextText), "下一轮"); o.Err != nil {
		t.Fatal(o.Err)
	}
	first, second := k.request(0), k.request(1)
	if !isPrefix(first, second) {
		t.Fatal("下一轮请求没有保留原有前缀")
	}
	if n, _ := inMessages(second, "约好一起种蓝色花朵"); n != 1 {
		t.Fatalf("参考信息出现 %d 次，想要 1 次", n)
	}
	if n, _ := inMessages(second, "主人喜欢紫色风筝"); n != 1 {
		t.Fatalf("普通事实出现 %d 次，想要 1 次", n)
	}
	if seq, _ := w.cursor(conv); seq != 0 {
		t.Fatalf("整理未完成时游标 %d", seq)
	}
	free()
	done := make(chan struct{})
	go func() { k.hub.Idle(); close(done) }()
	raceReceive(t, done, "提前压缩结束")
	hook := raceReceive(t, p.results, "压缩钩子交回")
	if hook.err != nil || !hook.limited || hook.upTo != lastSeq {
		t.Fatalf("钩子交回 %+v，已整理到 %d", hook, lastSeq)
	}
	if seq, _ := w.cursor(conv); seq != lastSeq {
		t.Fatalf("整理后的游标 %d，想要 %d", seq, lastSeq)
	}
}

type raceFixture struct {
	k        *kernel
	w        *writable
	p        *racePlugin
	conv     string
	entered  chan struct{}
	returned chan struct{}
	free     func()
}

func newRaceFixture(t *testing.T) *raceFixture {
	t.Helper()
	k := newKernelWith(t, func(c *turn.Config) {
		// 窗口要让两条线都留余量：第一轮后的估算（约 13500）过硬门槛的八成才会提前压，加上下一句和 600 的预留又不能过硬门槛。
		// 固定前缀（含工具定义）会随新工具变长，窗口别卡着刚好的数
		c.Context = turn.ContextLimits{Window: 15500, Reserve: 1, HardRatio: 1, KeepTokens: 50}
	})
	clock := newClock()
	w := newWritable(t, clock, character, k.logs, nil)
	conv := "console:" + character
	for range 8 {
		for _, who := range []string{"owner", "character"} {
			id := who
			if who == "character" {
				id = character
			}
			text := strings.Repeat("甲", 600)
			seq, err := k.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: who, ActorID: id, Origin: who, Kind: "message", Text: text, CreatedAt: clock.Now().UnixMilli()}, []string{character})
			if err != nil {
				t.Fatal(err)
			}
			w.ev.add(conv, text, func(e *memory.Event) { e.Seq, e.ActorKind, e.ActorID, e.Origin = seq, who, id, who })
		}
	}
	w.initCursor(t, conv, 0)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	free := freeOnce(t, release)
	returned := make(chan struct{}, 1)
	var calls atomic.Int32
	w.gen.block = func(context.Context) error {
		if calls.Add(1) != 1 {
			return nil
		}
		defer func() {
			select {
			case returned <- struct{}{}:
			default:
			}
		}()
		select {
		case entered <- struct{}{}:
		case <-release:
			return nil
		}
		<-release
		return nil
	}
	w.gen.reply = func(int, string) (string, error) { return candidateReply("整理出的虚构记录", 1), nil }
	p := &racePlugin{Plugin: New(Options{Memories: only(w.m), Now: clock.Now, After: clock.After, Logf: k.logs.logf, Threshold: 1}), results: make(chan raceHookResult, 4)}
	if err := k.hub.Use(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raceCleanup(t, k, free) })
	if o := raceReceive(t, k.hub.Send(character, "第一句"), "第一轮"); o.Err != nil {
		t.Fatal(o.Err)
	}
	raceReceive(t, entered, "提前压缩整理")
	return &raceFixture{k, w, p, conv, entered, returned, free}
}

func raceUntil(t *testing.T, name string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			t.Fatalf("等待 %s 超时\n%s", name, buf[:n])
		}
		runtime.Gosched()
	}
}

func TestRaceCompactNotification(t *testing.T) {
	t.Parallel()
	f := newRaceFixture(t)
	f.w.ev.add(f.conv, "整理期间到来的新消息")
	f.p.Notify(character, f.conv)
	lf := f.p.current()
	raceUntil(t, "通知排程等待同会话整理", func() bool {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(stack, "memplugin.(*sched).runBatch") && strings.Contains(stack, "sync.(*Mutex).Lock") {
				return true
			}
		}
		return f.w.gen.peakLive() > 1
	})
	f.free()
	h := raceReceive(t, f.p.results, "整理钩子")
	if h.err != nil || !h.limited || h.upTo != 17 {
		t.Fatalf("钩子交回 %+v", h)
	}
	raceUntil(t, "排程结束", func() bool { return !lf.s.busy() })
	if f.w.gen.peakLive() != 1 || f.w.gen.count() != 2 || f.w.rows(t, "memory_jobs") != 2 || f.w.num(t, "SELECT count(*) FROM memory_jobs a JOIN memory_jobs b ON a.conversation = b.conversation AND b.from_seq = a.to_seq + 1 WHERE a.conversation = ? AND a.from_seq = 1 AND b.to_seq = 17", f.conv) != 1 {
		t.Fatalf("模型并发峰值 %d，调用 %d，批次 %d", f.w.gen.peakLive(), f.w.gen.count(), f.w.rows(t, "memory_jobs"))
	}
}

func TestRaceCompactDifferentConversation(t *testing.T) {
	t.Parallel()
	f := newRaceFixture(t)
	conv := "console:other"
	seq, err := f.k.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "另一会话", CreatedAt: time.Now().UnixMilli()}, []string{character})
	if err != nil {
		t.Fatal(err)
	}
	f.w.ev.add(conv, "另一会话需要整理的内容", func(e *memory.Event) { e.Seq = seq })
	f.w.initCursor(t, conv, 0)
	o := raceReceive(t, f.k.hub.Respond(turn.Input{CharacterID: character, Conversation: conv, Seqs: []int64{seq}, Text: "另一会话", Audience: consoleAud, Addressed: true, Scope: turn.ConsoleScope}), "另一会话回复")
	if o.Err != nil {
		t.Fatal(o.Err)
	}
	f.p.Notify(character, conv)
	raceUntil(t, "另一会话整理进入模型并推进游标", func() bool {
		cursor, _ := f.w.cursor(conv)
		return f.w.gen.count() >= 2 && cursor == seq
	})
	if seq, _ := f.w.cursor(f.conv); seq != 0 {
		t.Fatalf("整理未放行，游标 %d", seq)
	}
	f.free()
	raceReceive(t, f.p.results, "整理钩子")
}

func TestRaceCompactShutdown(t *testing.T) {
	t.Parallel()
	f := newRaceFixture(t)
	f.k.cancel()
	done := make(chan struct{})
	go func() { f.k.hub.Idle(); close(done) }()
	raceReceive(t, done, "内核后台退出")
	lf := f.p.current()
	stopped := make(chan struct{})
	go func() { f.k.hub.StopPlugin(ID); close(stopped) }()
	raceReceive(t, lf.ctx.Done(), "插件取消")
	raceUntil(t, "插件等待整理退出", func() bool {
		select {
		case <-stopped:
			t.Fatal("模型仍在运行，插件已停止")
		default:
		}
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(stack, "memplugin.(*life).stop") && strings.Contains(stack, "sync.(*WaitGroup).Wait") {
				return true
			}
		}
		return false
	})
	f.free()
	raceReceive(t, stopped, "插件停止")
	select {
	case <-f.returned:
	default:
		t.Fatal("插件停止时模型尚未返回")
	}
	if seq, _ := f.w.cursor(f.conv); seq != 0 || f.w.rows(t, "facts") != 0 || f.w.rows(t, "memory_jobs") != 0 {
		t.Fatalf("停后写入：游标 %d", seq)
	}
	if err := f.w.m.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRaceCompactHardWait(t *testing.T) {
	t.Parallel()
	f := newRaceFixture(t)
	// 硬门槛等提前压和钩子单次调用由内核保证；这里检查记忆插件自己的时限契约。
	result := make(chan raceHookResult, 1)
	deadline := time.Now().Add(compactMargin + 200*time.Millisecond)
	go func() {
		u, limited, err := f.p.current().beforeCompact(context.Background(), turn.BeforeCompact{
			CharacterID: character, Conversation: f.conv, Trigger: "rule", WaitUntil: deadline,
		})
		result <- raceHookResult{u, limited, err}
	}()
	h := raceReceive(t, result, "限时钩子交回")
	if h.err != nil || !h.limited || h.upTo != 0 || !time.Now().Before(deadline) {
		t.Fatalf("整理未完成时钩子交回 %+v，时限 %v", h, deadline)
	}
	f.free()
	raceReceive(t, f.p.results, "提前压整理钩子")
	raceUntil(t, "后台整理落库", func() bool { seq, _ := f.w.cursor(f.conv); return seq == 16 })
	if f.w.rows(t, "memory_jobs") == 0 || f.w.rows(t, "facts") != 1 {
		t.Fatalf("后台整理结果：批次 %d，事实 %d", f.w.rows(t, "memory_jobs"), f.w.rows(t, "facts"))
	}
}
