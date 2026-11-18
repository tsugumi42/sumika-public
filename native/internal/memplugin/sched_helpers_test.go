package memplugin

// 后台整理排程（schedule.go）的测试台：拨得动的假钟、假的事件来源和后台模型、一个迁移过的临时库副本。
// 名字、会话 id、句子都是编的；库只在临时目录里，绝不碰真实的 data/

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/memory"
	"sumika-native/internal/memory/memtest"
	"sumika-native/internal/prompts"
)

// repoRoot：出厂提示词在项目根的 prompts/ 下（native/internal/memplugin → 往上三层）
var repoRoot = filepath.Join("..", "..", "..")

// testPrompts：台子里每个角色用的提示词来源，默认只有出厂的（只读，不要改）。要换别的来源，用 rigExtras.prompts，
// 它只对这一个台子生效，所以用到它的测试可以并行
var testPrompts = prompts.Store{Root: repoRoot}

// rigExtras 是台子里每个角色的库要另配的地方：memOpts 改 memory.Options（测自动提升时设 AutoConfirm），prompts 换提示词来源。
// 都只对这一个台子生效，不碰包级变量，所以用到它的测试可以并行
type rigExtras struct {
	memOpts func(*memory.Options)
	prompts *prompts.Store
}

// ───────────── 假钟 ─────────────

// fakeClock 是拨得动的钟：After 记下计时，Advance 往后拨并按先后调到点的回调。
// 回调在 Advance 的调用里同步跑（排程的回调只标记一下、叫起协程就返回，不会卡住）
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.stopped && !t.fired
		t.stopped = true
		return was
	}
}

// Advance 往后拨 d，到点的计时按先后调回调
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, t := range c.timers {
			if !t.stopped && !t.fired && !t.at.After(target) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		next.fired = true
		if next.at.After(c.now) {
			c.now = next.at
		}
		c.mu.Unlock()
		next.f()
	}
}

// pending：还没到点、没取消的计时有几个
func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.stopped && !t.fired {
			n++
		}
	}
	return n
}

// ───────────── 假的事件来源 ─────────────

// fakeEvents 是一个角色看到的事件：按 seq 升序。Events 取游标之后最早的 N 条
type fakeEvents struct {
	mu    sync.Mutex
	clock *fakeClock
	evs   []memory.Event
	next  int64
	reads int
	err   error // 不为空时 Events / Conversations 返回它
}

func newEvents(c *fakeClock) *fakeEvents { return &fakeEvents{clock: c} }

// add 加一条主人说的话（mod 可以改字段），At 取假钟的现在；seq 自己排
func (e *fakeEvents) add(conv, text string, mod ...func(*memory.Event)) memory.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.next++
	ev := memory.Event{Seq: e.next, Conversation: conv, At: e.clock.Now().UnixMilli(), ActorKind: "owner", ActorID: "owner", Origin: "owner",
		Kind: "message", Text: text, Seen: true}
	for _, f := range mod {
		f(&ev)
	}
	e.evs = append(e.evs, ev)
	return ev
}

// addN 加 n 条一样的话，返回最后一条
func (e *fakeEvents) addN(conv string, n int, mod ...func(*memory.Event)) (last memory.Event) {
	for i := 0; i < n; i++ {
		last = e.add(conv, fmt.Sprintf("第%d句闲话。", i+1), mod...)
	}
	return last
}

func (e *fakeEvents) setErr(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.err = err
}

func (e *fakeEvents) Events(_ context.Context, conv string, after int64, limit int) ([]memory.Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reads++
	if e.err != nil {
		return nil, e.err
	}
	var out []memory.Event
	for _, ev := range e.evs {
		if ev.Conversation == conv && ev.Seq > after {
			out = append(out, ev)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (e *fakeEvents) readCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.reads
}

func (e *fakeEvents) Revoked(context.Context, string, []int64) (map[int64]bool, error) {
	return nil, nil
}
func (e *fakeEvents) Find(context.Context, string, []string, int) ([]memory.Event, error) {
	return nil, nil
}

// LastSaid：这个会话里最近一句 owner 或 peer 的话（和真的事件来源一样）
func (e *fakeEvents) LastSaid(_ context.Context, conv string) (memory.Event, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := len(e.evs) - 1; i >= 0; i-- {
		if ev := e.evs[i]; ev.Conversation == conv && ev.Kind == "message" && (ev.Origin == "owner" || ev.Origin == "peer") {
			return ev, true, nil
		}
	}
	return memory.Event{}, false, nil
}

// Conversations：每个会话最大的 seq，最近有动静的在前
func (e *fakeEvents) Conversations(context.Context) ([]memory.ConvHead, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return nil, e.err
	}
	last := map[string]int64{}
	for _, ev := range e.evs {
		last[ev.Conversation] = max(last[ev.Conversation], ev.Seq)
	}
	var out []memory.ConvHead
	for c, s := range last {
		out = append(out, memory.ConvHead{Conversation: c, LastSeq: s})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeq > out[j].LastSeq })
	return out, nil
}

// ───────────── 假的后台模型 ─────────────

// fakeGen 记下每次调用；reply 不为空时由它给回复或错误（第 n 次调用，提示词），否则回一个合法的空整理；block 先跑，拿来卡住、放行
type fakeGen struct {
	mu      sync.Mutex
	calls   int
	prompts []string
	live    int // 现在同时在调的
	peak    int // 同时在调的最多几个
	reply   func(call int, prompt string) (string, error)
	block   func(ctx context.Context) error
}

func (g *fakeGen) Call(ctx context.Context, _, prompt, _ string) (memory.Reply, error) {
	g.mu.Lock()
	g.calls++
	n := g.calls
	g.prompts = append(g.prompts, prompt)
	g.live++
	g.peak = max(g.peak, g.live)
	reply, block := g.reply, g.block
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.live--
		g.mu.Unlock()
	}()
	if block != nil {
		if err := block(ctx); err != nil {
			return memory.Reply{}, err
		}
	}
	if reply != nil {
		text, err := reply(n, prompt)
		return memory.Reply{Text: text}, err
	}
	return memory.Reply{Text: `{"episode_record":"no","ops":[]}`}, nil
}

func (g *fakeGen) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func (g *fakeGen) peakLive() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.peak
}

func (g *fakeGen) promptOf(i int) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.prompts[i]
}

// ───────────── 库副本 ─────────────

// migratedFile 在临时目录里造一个 Bun 样子的库，seed 往里放几行，迁移好写入侧的表；返回文件路径
func migratedFile(t *testing.T, seed func(db *sql.DB)) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "memory.db")
	memtest.BunShaped(t, file)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		seed(db)
	}
	db.Close()
	if err := memory.Migrate(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	return file
}

// writable 的开法：角色、事件来源、模型都是假的，钟和插件共用一个
type writable struct {
	char  string
	file  string
	ev    *fakeEvents
	gen   *fakeGen
	m     *memory.Memory
	model string
	mu    sync.Mutex
}

func (w *writable) setModel(s string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.model = s
}

func newWritable(t *testing.T, c *fakeClock, char string, logs *logSink, seed func(db *sql.DB)) *writable {
	t.Helper()
	return newWritableWith(t, c, char, logs, seed, rigExtras{})
}

func newWritableWith(t *testing.T, c *fakeClock, char string, logs *logSink, seed func(db *sql.DB), ex rigExtras) *writable {
	t.Helper()
	store := testPrompts
	if ex.prompts != nil {
		store = *ex.prompts
	}
	w := &writable{char: char, file: migratedFile(t, seed), ev: newEvents(c), gen: &fakeGen{}, model: "bg-model"}
	opt := memory.Options{Writable: true, SummarizeEnabled: true, WritePlaces: memory.AllWritePlaces(), Character: char, Name: "铃", Logf: logs.logf,
		Events: w.ev, Gen: w.gen, Prompts: store, Now: c.Now,
		Model: func() string { w.mu.Lock(); defer w.mu.Unlock(); return w.model }}
	if ex.memOpts != nil {
		ex.memOpts(&opt)
	}
	m, err := memory.Open(context.Background(), w.file, nil, nil, opt)
	if err != nil {
		t.Fatal(err)
	}
	w.m = m
	t.Cleanup(func() { m.Close() })
	return w
}

// cursor 读一个会话的游标；没有行 ok 为假
func (w *writable) cursor(conv string) (seq int64, ok bool) {
	st, ok, err := w.m.SummaryState(context.Background(), conv)
	if err != nil {
		panic(err)
	}
	return st.LastSeq, ok
}

func (w *writable) state(t *testing.T, conv string) memory.SummaryState {
	t.Helper()
	st, ok, err := w.m.SummaryState(context.Background(), conv)
	if err != nil || !ok {
		t.Fatalf("读状态：%v %v", ok, err)
	}
	return st
}

func (w *writable) initCursor(t *testing.T, conv string, seq int64) {
	t.Helper()
	if _, err := w.m.InitCursor(context.Background(), conv, seq); err != nil {
		t.Fatal(err)
	}
}

// rows 数一张表的行数
func (w *writable) rows(t *testing.T, table string) int {
	t.Helper()
	return w.num(t, `SELECT count(*) FROM `+table)
}

// num、text 是对库副本的只读查询
func (w *writable) num(t *testing.T, query string, args ...any) (n int) {
	t.Helper()
	w.query(t, query, args, &n)
	return n
}

func (w *writable) text(t *testing.T, query string, args ...any) (s string) {
	t.Helper()
	w.query(t, query, args, &s)
	return s
}

func (w *writable) query(t *testing.T, query string, args []any, dest any) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(w.file)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(query, args...).Scan(dest); err != nil {
		t.Fatal(err)
	}
}

// ───────────── 排程台 ─────────────

// rig 是一个插件值 + 它的一次启用 + 假钟。mems 里是各角色的库
type rig struct {
	t     *testing.T
	clock *fakeClock
	logs  *logSink
	mems  map[string]*writable
	p     *Plugin
	lf    *life
}

// newRig 建一个只有一个角色的台子；mutate 改插件的选项（Quiet 等默认是 10 分钟、40 条）。启用了，没有启动扫描（Characters 为空）
func newRig(t *testing.T, mutate func(*Options), chars ...string) *rig {
	t.Helper()
	return newRigSeeded(t, mutate, nil, chars...)
}

// newRigSeeded：库里先放几行（迁移之前，Bun 样子的表）
func newRigSeeded(t *testing.T, mutate func(*Options), seed func(db *sql.DB), chars ...string) *rig {
	t.Helper()
	return newRigExtra(t, rigExtras{}, mutate, seed, chars...)
}

// newRigExtra：同 newRigSeeded，另外给各角色的库配 rigExtras
func newRigExtra(t *testing.T, ex rigExtras, mutate func(*Options), seed func(db *sql.DB), chars ...string) *rig {
	t.Helper()
	if len(chars) == 0 {
		chars = []string{"suzu"}
	}
	r := &rig{t: t, clock: newClock(), logs: &logSink{}, mems: map[string]*writable{}}
	for _, c := range chars {
		r.mems[c] = newWritableWith(t, r.clock, c, r.logs, seed, ex)
	}
	r.p = r.newPlugin(mutate)
	return r
}

func (r *rig) newPlugin(mutate func(*Options)) *Plugin {
	opt := Options{Memories: func(c string) *memory.Memory {
		if w := r.mems[c]; w != nil {
			return w.m
		}
		return nil
	}, Logf: r.logs.logf, Now: r.clock.Now, After: r.clock.After}
	if mutate != nil {
		mutate(&opt)
	}
	return New(opt)
}

// enable 启用（不经内核，直接起一次生命周期）；停掉在测试收尾
func (r *rig) enable() *life {
	r.t.Helper()
	r.lf = r.p.start()
	r.p.mu.Lock()
	r.p.cur = r.lf
	r.p.mu.Unlock()
	r.lf.startScan()
	r.t.Cleanup(r.lf.stop)
	return r.lf
}

// idle 等到排程安静下来：启动扫描退出、没有协程在干活也没有没干的活。假钟是同步调回调的，所以拨完钟再等它，不会漏
func (r *rig) idle() {
	r.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	if ch := r.lf.s.scanning; ch != nil {
		select {
		case <-ch:
		case <-time.After(time.Until(deadline)):
			r.t.Fatal("启动扫描 20 秒还没退出")
		}
	}
	for r.lf.s.busy() {
		if time.Now().After(deadline) {
			r.t.Fatal("排程 20 秒还没安静下来")
		}
		time.Sleep(time.Millisecond)
	}
}

// advance 拨钟，再等排程安静
func (r *rig) advance(d time.Duration) {
	r.t.Helper()
	r.clock.Advance(d)
	r.idle()
}

// notify 通知 n 次，等排程安静
func (r *rig) notify(char, conv string, n int) {
	r.t.Helper()
	for i := 0; i < n; i++ {
		r.p.Notify(char, conv)
	}
	r.idle()
}

// ok 回一个合法的、带一条候选事实的整理输出（src 是这一批里的第一个 seq）
func candidateReply(text string, src int64) string {
	return fmt.Sprintf(`{"episode_record":"no","ops":[{"op":"candidate","kind":"fact","text":%q,"subject":"owner","src":[%d]}]}`, text, src)
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
