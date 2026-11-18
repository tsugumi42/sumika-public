package proactive

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// 话题线的存取（规格-主动联系 第四、五、九节）：同时最多开几条、先写后发、发送上限、认领只一次、重启恢复、并发。
// 用真的 trace.db（临时目录）；角色、会话、线号都是编的。

type mgrEnv struct {
	t   *testing.T
	m   *Threads
	p   *config.Proactive
	now time.Time
	mu  sync.Mutex
	db  *store.Store
}

func newMgr(t *testing.T) *mgrEnv {
	t.Helper()
	pp, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenFresh(pp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	p := params()
	e := &mgrEnv{t: t, p: &p, now: at(9, 14, 0), db: s}
	e.m = NewThreads(s.Trace, func() config.Proactive { return *e.p }, func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now })
	return e
}

func (e *mgrEnv) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }

func (e *mgrEnv) open(id string) Thread {
	e.t.Helper()
	x, err := e.m.Create(Thread{ID: id, Character: "aa", Conversation: "console:aa:topic:thread-" + id, OpenedAt: e.now})
	if err != nil {
		e.t.Fatal(err)
	}
	return x
}

func (e *mgrEnv) send(id string) Send {
	e.t.Helper()
	s, err := e.m.BeginSend(id, "console:aa", "本地私聊", "本地私聊", "你好呀")
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.m.FinishSend(s.ID, SendSent, 10, "evt1"); err != nil {
		e.t.Fatal(err)
	}
	return s
}

func TestCreateOnlyOneOpenPerCharacter(t *testing.T) {
	e := newMgr(t)
	e.open("a1")
	if _, err := e.m.Create(Thread{ID: "a2", Character: "aa", Conversation: "console:aa:topic:thread-a2", OpenedAt: e.now}); !errors.Is(err, ErrOpenExists) {
		t.Fatalf("同一角色同时只开一条（maxOpenThreads=1）：%v", err)
	}
	// 另一个角色不受影响
	if _, err := e.m.Create(Thread{ID: "b1", Character: "bb", Conversation: "console:bb:topic:thread-b1", OpenedAt: e.now}); err != nil {
		t.Fatal(err)
	}
	// 关了就能再开
	if _, err := e.m.Apply("a1", Event{Kind: EvEnd}); err != nil {
		t.Fatal(err)
	}
	e.open("a3")
	// id 撞了要拒
	e.p.MaxOpenThreads = 5
	if _, err := e.m.Create(Thread{ID: "a3", Character: "aa", Conversation: "console:aa:topic:thread-a3", OpenedAt: e.now}); err == nil {
		t.Fatal("线号重复必须拒绝")
	}
}

func TestSendLifecycleAndCap(t *testing.T) {
	e := newMgr(t) // maxSends 3
	e.open("a1")
	s, err := e.m.BeginSend("a1", "console:aa", "本地私聊", "本地私聊", "第一句")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := e.m.Get("a1"); got.Sends != 0 || got.State != ThreadThinking {
		t.Fatalf("先写后发：pending 时线还没算发过：%+v", got)
	}
	if sends, _ := e.m.Sends("a1"); len(sends) != 1 || sends[0].State != SendPending || sends[0].PlanID != "a1#1" {
		t.Fatalf("发送前先写一行 pending，幂等键是线号加序号：%+v", sends)
	}
	got, err := e.m.FinishSend(s.ID, SendSent, 7, "evt-1")
	if err != nil || got.State != ThreadAwaiting || got.Sends != 1 || got.FirstSendAt.IsZero() {
		t.Fatalf("发出去以后进等回复：%+v %v", got, err)
	}
	if send, ok, _ := e.m.LastSendTo("a1", "console:aa"); !ok || send.EventSeq != 7 || send.EventID != "evt-1" {
		t.Fatalf("最后一次发到这个会话的：%+v %v", send, ok)
	}
	if send, ok, _ := e.m.SendByEventID("console:aa", "evt-1"); !ok || send.Thread != "a1" {
		t.Fatalf("按事件 id 找发送记录（引用认领用）：%+v %v", send, ok)
	}
	if _, ok, _ := e.m.SendByEventID("console:aa", ""); ok {
		t.Fatal("空 id 不该匹配任何记录")
	}
	if _, ok, _ := e.m.SendByEventID("console:other", "evt-1"); ok {
		t.Fatal("别的会话里同 id 不算")
	}
	// 失败的发送：不算发过、不推进线
	f, _ := e.m.BeginSend("a1", "console:aa", "本地私聊", "本地私聊", "第二句")
	got, _ = e.m.FinishSend(f.ID, SendFailed, 0, "")
	if got.Sends != 1 {
		t.Fatalf("发送失败不算开口：%+v", got)
	}
	if _, ok, _ := e.m.LastSendTo("a1", "console:other"); ok {
		t.Fatal("没发到过的会话不该有记录")
	}
	// 失败的不占名额以外：已经 3 行（1 sent + 1 failed），sends=1，in-flight=0 → 还能发 2 条
	e.send("a1")
	e.send("a1")
	if _, err := e.m.BeginSend("a1", "console:aa", "本地私聊", "本地私聊", "第四句"); !errors.Is(err, ErrCap) {
		t.Fatalf("发满 3 条以后拒绝：%v", err)
	}
	if _, err := e.m.Apply("a1", Event{Kind: EvEnd}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.BeginSend("a1", "console:aa", "本地私聊", "本地私聊", "x"); !errors.Is(err, ErrClosed) {
		t.Fatalf("线关了以后到的发送拒绝：%v", err)
	}
	if _, err := e.m.BeginSend("nope", "console:aa", "本地私聊", "本地私聊", "x"); !errors.Is(err, ErrNoThread) {
		t.Fatalf("没有这条线：%v", err)
	}
}

func TestInflightSendsCountTowardCap(t *testing.T) {
	e := newMgr(t)
	e.p.MaxSendsPerThread = 2
	e.open("a1")
	var ids []int64
	for i := 0; i < 2; i++ {
		s, err := e.m.BeginSend("a1", "console:aa", "l", "l", "x")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID)
	}
	if _, err := e.m.BeginSend("a1", "console:aa", "l", "l", "x"); !errors.Is(err, ErrCap) {
		t.Fatalf("还在途的 pending 也占名额，并发调用不能超发：%v", err)
	}
}

func TestConcurrentBeginSendNeverExceedsCap(t *testing.T) {
	e := newMgr(t)
	e.p.MaxSendsPerThread = 3
	e.open("a1")
	var wg sync.WaitGroup
	var okN, capN, other int
	var mu sync.Mutex
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.m.BeginSend("a1", "console:aa", "l", "l", "x")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				okN++
			case errors.Is(err, ErrCap):
				capN++
			default:
				other++
				t.Errorf("意外错误：%v", err)
			}
		}()
	}
	wg.Wait()
	if okN != 3 || capN != 9 || other != 0 {
		t.Fatalf("12 个并发请求只能成 3 个：成 %d、超限 %d、别的 %d", okN, capN, other)
	}
}

func TestClaimsOnlyOnceAndStateTransitions(t *testing.T) {
	e := newMgr(t)
	e.open("a1")
	if err := e.m.AddClaim("console:aa", 5, "a1", e.now); err != nil {
		t.Fatal(err)
	}
	if err := e.m.AddClaim("console:aa", 5, "a1", e.now); !errors.Is(err, ErrDupClaim) {
		t.Fatalf("同一条（会话 + 序号）只能认领一次：%v", err)
	}
	if err := e.m.AddClaim("console:aa", 6, "a1", e.now); err != nil {
		t.Fatalf("另一条可以：%v", err)
	}
	if ch, _ := e.m.SetClaim("console:aa", 5, ClaimHanded); !ch {
		t.Fatal("pending 可以改成 handed")
	}
	if ch, _ := e.m.SetClaim("console:aa", 5, ClaimHanded); ch {
		t.Fatal("交回只发生一次：已经 handed 的不再改")
	}
	if ch, _ := e.m.SetClaim("console:aa", 5, ClaimReplied); ch {
		t.Fatal("已经交回的不能再改成已回")
	}
	pending, _ := e.m.ClaimsOf("a1", ClaimPending)
	if len(pending) != 1 || pending[0].Seq != 6 {
		t.Fatalf("还在等的认领：%+v", pending)
	}
	if all, _ := e.m.ClaimsOf("a1", ""); len(all) != 2 {
		t.Fatalf("全部认领：%+v", all)
	}
}

func TestBlockAndOpenedReasonAndSentToday(t *testing.T) {
	e := newMgr(t)
	e.open("a1")
	if err := e.m.Block("a1", "console:aa"); err != nil {
		t.Fatal(err)
	}
	e.m.Block("a1", "console:aa") // 重复拉黑不重复记
	got, _ := e.m.Get("a1")
	if !got.Blocked("console:aa") || len(got.BlockedConvs) != 1 {
		t.Fatalf("hand_back 之后本线内不再认领：%+v", got.BlockedConvs)
	}
	long := "为了"
	for i := 0; i < 80; i++ {
		long += "很长"
	}
	e.m.SetOpenedReason("a1", long)
	e.m.SetOpenedReason("a1", "第二次不覆盖")
	got, _ = e.m.Get("a1")
	if r := []rune(got.OpenedReason); len(r) != 60 || got.OpenedReason[:6] != "为了" {
		t.Fatalf("开口理由限 60 字且只记第一次：%d %q", len(r), got.OpenedReason)
	}
	// 今天开线并真发过话：按第一次发话的时间算；没发过的、昨天的、发送失败的都不算
	dayStart := time.Date(2026, 11, 9, 0, 0, 0, 0, tz)
	if n, _ := e.m.SentToday("aa", dayStart); n != 0 {
		t.Fatalf("还没发过：%d", n)
	}
	e.send("a1")
	if n, _ := e.m.SentToday("aa", dayStart); n != 1 {
		t.Fatalf("今天找过 1 次：%d", n)
	}
	e.send("a1") // 同一条线再发不重复算
	if n, _ := e.m.SentToday("aa", dayStart); n != 1 {
		t.Fatalf("同一条线只算一次：%d", n)
	}
	if n, _ := e.m.SentToday("aa", dayStart.Add(24*time.Hour)); n != 0 {
		t.Fatalf("明天的起点之前发的不算：%d", n)
	}
	if n, _ := e.m.SentToday("bb", dayStart); n != 0 {
		t.Fatalf("别的角色不算：%d", n)
	}
}

func TestRecoverAfterRestart(t *testing.T) {
	e := newMgr(t)
	e.p.MaxOpenThreads = 10
	// a1：想着、没发过话 → 关成 error(restart)
	e.open("a1")
	// a2：想着（续醒）、发过话 → 回到等回复
	e.open("a2")
	e.send("a2")
	e.m.Apply("a2", Event{Kind: EvWake, At: e.now})
	// a3：发送写了 pending 就退出了：记 unknown、线按发过话回到等回复，不重发
	e.open("a3")
	pend, _ := e.m.BeginSend("a3", "console:aa", "本地私聊", "本地私聊", "话")
	// a4：等回复、a5：聊着，照旧
	e.open("a4")
	e.send("a4")
	e.open("a5")
	e.send("a5")
	e.m.Apply("a5", Event{Kind: EvClaim, At: e.now})
	e.m.AddClaim("console:aa", 9, "a5", e.now) // 认领中途退出：丢，不重放
	// a6：已经关了的线先被关、发送还是 pending
	e.open("a6")
	pend6, _ := e.m.BeginSend("a6", "console:aa", "本地私聊", "本地私聊", "话")
	e.m.Apply("a6", Event{Kind: EvEnd})

	rep, err := e.m.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Unknown != 2 || rep.Lost != 1 {
		t.Fatalf("pending 发送记 unknown 两条、丢认领一条：%+v", rep)
	}
	state := func(id string) Thread { x, _ := e.m.Get(id); return x }
	if x := state("a1"); x.State != ThreadClosed || x.CloseReason != ClosedError || x.Conclusion != "restart" {
		t.Errorf("没发过话的想着的线关成 error(restart)：%+v", x)
	}
	if x := state("a2"); x.State != ThreadAwaiting {
		t.Errorf("发过话的想着的线回到等回复：%+v", x)
	}
	if x := state("a3"); x.State != ThreadAwaiting || x.Sends != 1 {
		t.Errorf("pending 发送按发过话算、回到等回复，不关成 error：%+v", x)
	}
	if sends, _ := e.m.Sends("a3"); len(sends) != 1 || sends[0].State != SendUnknown || sends[0].ID != pend.ID {
		t.Errorf("pending 一律记 unknown：%+v", sends)
	}
	if x := state("a4"); x.State != ThreadAwaiting {
		t.Errorf("等回复照旧：%+v", x)
	}
	if x := state("a5"); x.State != ThreadChatting {
		t.Errorf("聊着照旧：%+v", x)
	}
	if cl, _ := e.m.ClaimsOf("a5", ClaimLost); len(cl) != 1 {
		t.Errorf("认领记 lost：%+v", cl)
	}
	if x := state("a6"); x.State != ThreadClosed || x.Sends != 1 {
		t.Errorf("先关了的线补上发送数（今天找过几次不能漏算）：%+v", x)
	}
	if sends, _ := e.m.Sends("a6"); sends[0].State != SendUnknown || sends[0].ID != pend6.ID {
		t.Errorf("a6 的 pending 也记 unknown：%+v", sends)
	}
	// 再恢复一次没有副作用
	rep2, err := e.m.Recover()
	if err != nil || rep2.Unknown != 0 || rep2.Lost != 0 || len(rep2.Threads) != 0 {
		t.Fatalf("第二次恢复不该再改东西：%+v %v", rep2, err)
	}
	// 重启后导演数今天次数把 unknown 当已找过
	dayStart := time.Date(2026, 11, 9, 0, 0, 0, 0, tz)
	if n, _ := e.m.SentToday("aa", dayStart); n < 4 {
		t.Fatalf("unknown 也算今天找过（a2、a3、a4、a5、a6 里发过话的）：%d", n)
	}
}

func TestRecentAndLastContactedAndOpenOf(t *testing.T) {
	e := newMgr(t)
	e.open("a1")
	e.send("a1")
	e.m.Apply("a1", Event{Kind: EvEnd})
	e.advance(time.Hour)
	e.open("a2")
	if x, ok, _ := e.m.OpenOf("aa"); !ok || x.ID != "a2" {
		t.Fatalf("开着的是 a2：%+v %v", x, ok)
	}
	if _, ok, _ := e.m.OpenOf("bb"); ok {
		t.Fatal("别的角色没有开着的线")
	}
	if x, ok, _ := e.m.LastContacted("aa"); !ok || x.ID != "a1" {
		t.Fatalf("最近一条真发过话的是 a1（a2 还没发）：%+v %v", x, ok)
	}
	rs, _ := e.m.Recent("aa", 10)
	if len(rs) != 2 || rs[0].ID != "a2" || rs[1].ID != "a1" {
		t.Fatalf("最近的线新的在前：%+v", rs)
	}
	if all, _ := e.m.OpenThreads(); len(all) != 1 || all[0].ID != "a2" {
		t.Fatalf("所有开着的线：%+v", all)
	}
}

// 导演的状态更新（线关闭时更新系数、别烦、重排）和调度壳同时读改写，不会互相冲掉
func TestDirectorStateUpdatesDoNotLoseWrites(t *testing.T) {
	st := &MemStore{}
	h := newHarness(t, st)
	h.tick(at(9, 14, 0)) // 排第一次
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.d.ThreadClosed("aa", CloseCold, at(9, 14, 5)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	s, _, _ := st.LoadState("aa")
	if s.ColdStreak != 20 || s.Factor != 3 {
		t.Fatalf("20 次并发的冷都要算上：连冷 %d、系数 %v", s.ColdStreak, s.Factor)
	}
	until := at(9, 14, 5).Add(24 * time.Hour)
	if err := h.d.SetQuiet("aa", until, at(9, 14, 5)); err != nil {
		t.Fatal(err)
	}
	s, _, _ = st.LoadState("aa")
	if !s.QuietUntil.Equal(until) || s.Factor != 3 || s.ColdStreak != 20 {
		t.Fatalf("别烦只设 quiet_until，不动系数和连冷：%+v", s)
	}
	if !s.NextWake.Equal(until) {
		t.Fatalf("下次醒不早于 quiet_until：%v", s.NextWake)
	}
	// 线关闭后下次从现在起按新系数重排：热 → 系数变小 → 间隔变短
	if err := h.d.ThreadClosed("aa", CloseHot, at(9, 20, 0)); err != nil {
		t.Fatal(err)
	}
	s, _, _ = st.LoadState("aa")
	if s.ColdStreak != 0 || !(s.Factor < 3) || !s.NextWake.After(at(9, 20, 0)) {
		t.Fatalf("热：连冷清零、系数变小、从关线时刻起重排：%+v", s)
	}
	if err := h.d.SetNextWake("aa", at(9, 21, 0)); err != nil {
		t.Fatal(err)
	}
	if s, _, _ = st.LoadState("aa"); !s.NextWake.Equal(at(9, 21, 0)) {
		t.Fatalf("SetNextWake：%v", s.NextWake)
	}
}

// 调度每次轮询先调 Housekeeping；它不受各角色 nextWake 限制，关着时不调
func TestHousekeepingRunsEveryTickWhenActive(t *testing.T) {
	st := &MemStore{}
	h := newHarness(t, st)
	var n int
	h.d.opt.Housekeeping = func(context.Context, time.Time) { n++ }
	h.tick(at(9, 14, 0))
	h.tick(at(9, 14, 1))
	if n != 2 {
		t.Fatalf("每次轮询调一遍：%d", n)
	}
	h.p.Enabled = false
	h.tick(at(9, 14, 2))
	if n != 2 {
		t.Fatalf("关着不调：%d", n)
	}
}

func TestThreadsUseRealTraceDBMigration(t *testing.T) {
	e := newMgr(t)
	for _, table := range []string{"proactive_threads", "proactive_sends", "proactive_claims"} {
		var n int
		if err := e.db.Trace.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("迁移 0018 没建 %s：%d %v", table, n, err)
		}
	}
	_ = fmt.Sprint
}
