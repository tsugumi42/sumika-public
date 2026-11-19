package app

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// 只编 QQ 历史，不连接真实协议端。网络帧形状另外由 HistoryPage 及完整 WS 场景验证。
type historyCall struct {
	conv   types.Conversation
	anchor string
	count  int
}
type historyPeer struct {
	mu          sync.Mutex
	calls       []historyCall
	read        func(context.Context, historyCall) ([]types.Inbound, error)
	ready       bool
	unsupported bool
	stale       bool
	continuous  bool
	left        int
}

func (p *historyPeer) Caps() types.Caps          { return types.Caps{History: !p.unsupported} }
func (p *historyPeer) Ready() bool               { p.mu.Lock(); defer p.mu.Unlock(); return p.ready }
func (p *historyPeer) BeginQueryRound(n int)     { p.mu.Lock(); p.left = n; p.mu.Unlock() }
func (p *historyPeer) EndQueryRound()            {}
func (p *historyPeer) RemainingQueryBudget() int { p.mu.Lock(); defer p.mu.Unlock(); return p.left }
func (p *historyPeer) HistoryPage(ctx context.Context, _ string, c types.Conversation, a string, n int) (types.HistoryPage, error) {
	p.mu.Lock()
	if p.left == 0 {
		p.mu.Unlock()
		return types.HistoryPage{}, types.ErrQueryBudget
	}
	p.left--
	call := historyCall{c, a, n}
	p.calls = append(p.calls, call)
	read := p.read
	p.mu.Unlock()
	rows, err := read(ctx, call)
	return types.HistoryPage{Messages: rows, Latest: !p.stale, Continuous: p.continuous}, err
}
func (p *historyPeer) count() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.calls) }

type backfillEnv struct {
	t   *testing.T
	b   *qqBackfill
	p   *historyPeer
	clk *fake.Clock
	set *store.Set
	st  *store.Store
}

func newBackfillEnv(t *testing.T, tweaks ...func(*channel.Config)) *backfillEnv {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "qq")
	if err != nil {
		t.Fatal(err)
	}
	st, err := set.For("qq")
	if err != nil {
		t.Fatal(err)
	}
	clk := fake.NewClock(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	peer := &historyPeer{ready: true, continuous: true, read: func(context.Context, historyCall) ([]types.Inbound, error) { return nil, nil }}
	cfg := channel.Config{Stores: set, Roster: channel.Roster{Owners: map[string]string{"qq": testOwner}, Accounts: []channel.Account{{Channel: "qq", ID: testAccount, Character: testChar}}}, Now: clk.Now}
	for _, tweak := range tweaks {
		tweak(&cfg)
	}
	in, err := channel.NewIntake(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b := &qqBackfill{st: st, intake: in, source: peer, clock: clk, account: testAccount, owner: testOwner, logf: func(string, ...any) {}}
	e := &backfillEnv{t, b, peer, clk, set, st}
	t.Cleanup(func() { b.stop(); b.intake.Close(); set.Close() })
	e.watermark(clk.Now().Add(-time.Hour))
	return e
}
func (e *backfillEnv) watermark(at time.Time) {
	e.t.Helper()
	if _, err := e.st.Events.Exec(`INSERT INTO qq_sync(account,through)VALUES(?,?) ON CONFLICT(account) DO UPDATE SET through=excluded.through`, testAccount, at.UnixMilli()); err != nil {
		e.t.Fatal(err)
	}
}
func (e *backfillEnv) through() int64 {
	e.t.Helper()
	var at int64
	if err := e.st.Events.QueryRow(`SELECT through FROM qq_sync WHERE account=?`, testAccount).Scan(&at); err != nil {
		e.t.Fatal(err)
	}
	return at
}
func (e *backfillEnv) run() {
	e.t.Helper()
	if err := e.b.round(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}
func (e *backfillEnv) gaps() []store.ChannelGap {
	e.t.Helper()
	g, err := e.st.ChannelGaps(context.Background(), "qq")
	if err != nil {
		e.t.Fatal(err)
	}
	return g
}
func (e *backfillEnv) n() int {
	e.t.Helper()
	var n int
	if err := e.st.Events.QueryRow(`SELECT count(*) FROM events`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}
func (e *backfillEnv) msg(conv types.Conversation, id string, ago time.Duration) types.Inbound {
	return types.Inbound{Kind: types.InboundMessage, Account: testAccount, Conv: conv, PlatformID: id, From: types.User{ID: testOwner, Nick: "编的甲"}, Time: e.clk.Now().Add(-ago), Segs: []types.Seg{{Kind: types.SegText, Text: "编的话"}}}
}
func (e *backfillEnv) save(in types.Inbound) {
	e.t.Helper()
	tk, err := e.b.intake.Submit(context.Background(), in, channel.Options{})
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err = tk.Wait(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}
func backfillPrivate() types.Conversation {
	return types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: testOwner}
}
func backfillGroup() types.Conversation {
	return types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
}

// B1、B3、B10：重启几秒的空档补上；旧号仅补观察；补录后实时重推仍是一条。
func TestBackfillRestartDedup(t *testing.T) {
	t.Parallel()
	e := newBackfillEnv(t)
	c := backfillPrivate()
	e.watermark(e.clk.Now().Add(-6 * time.Second))
	live := e.msg(c, "1001", 8*time.Second)
	e.save(live)
	if _, err := e.st.Events.Exec(`DELETE FROM observations`); err != nil {
		t.Fatal(err)
	}
	missing := e.msg(c, "1002", 3*time.Second)
	e.p.read = func(context.Context, historyCall) ([]types.Inbound, error) {
		return []types.Inbound{missing, live}, nil
	}
	e.run()
	e.save(missing)
	if e.n() != 2 {
		t.Fatalf("重启补录后应为两条，实际 %d", e.n())
	}
	var obs int
	if err := e.st.Events.QueryRow(`SELECT count(*) FROM observations WHERE character_id=?`, testChar).Scan(&obs); err != nil {
		t.Fatal(err)
	}
	if obs != 2 {
		t.Fatalf("已有事件应补观察：%d", obs)
	}
	if len(e.gaps()) != 1 || e.gaps()[0].Reason != "page-cap" {
		t.Fatalf("未覆盖水位前60秒须记缺口：%+v", e.gaps())
	}
	if e.through() != e.clk.Now().UnixMilli() {
		t.Fatal("缺口持久登记后应推进处理水位")
	}
	// 下一轮读的是持久水位；历史早于起点那条不再新写。
	e.p.read = func(context.Context, historyCall) ([]types.Inbound, error) {
		return []types.Inbound{e.msg(c, "1000", 2*time.Minute), missing}, nil
	}
	e.run()
	if e.n() != 2 {
		t.Fatal("水位重叠不得重复写")
	}
}

// B3：群两页，锚点重复；只按时间排序，最旧页越过起点即停止。
func TestBackfillGroupPagination(t *testing.T) {
	t.Parallel()
	e := newBackfillEnv(t)
	e.b.owner = ""
	e.b.groups = []string{testGroup}
	c := backfillGroup()
	e.watermark(e.clk.Now().Add(-2 * time.Minute))
	e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
		if q.count != 100 {
			t.Errorf("每页条数 %d", q.count)
		}
		if q.anchor == "" {
			return []types.Inbound{e.msg(c, "-10", 20*time.Second), e.msg(c, "-2", 90*time.Second)}, nil
		}
		if q.anchor != "-2" {
			t.Errorf("锚点应取最旧消息，实际 %q", q.anchor)
		}
		return []types.Inbound{e.msg(c, "-2", 90*time.Second), e.msg(c, "-1", 3*time.Minute), e.msg(c, "-4", 4*time.Minute)}, nil
	}
	e.run()
	if e.p.count() != 2 || e.n() != 3 || len(e.gaps()) != 0 {
		t.Fatalf("群两页未接上：calls=%d events=%d gaps=%+v", e.p.count(), e.n(), e.gaps())
	}
	rows, err := e.st.Events.Query(`SELECT created_at FROM events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ats []int64
	for rows.Next() {
		var at int64
		if err := rows.Scan(&at); err != nil {
			t.Fatal(err)
		}
		ats = append(ats, at)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !sort.SliceIsSorted(ats, func(i, j int) bool { return ats[i] < ats[j] }) {
		t.Fatalf("未按时间从早到晚提交：%v", ats)
	}
}

// B14 推出的单页限制：有两个发送者的私聊不靠 seq，100条也不能假装接到起点。
func TestBackfillPrivateOnePage(t *testing.T) {
	t.Parallel()
	e := newBackfillEnv(t)
	c := backfillPrivate()
	e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
		if q.anchor != "" {
			t.Error("私聊不允许带锚点")
		}
		var rows []types.Inbound
		for i := range 100 {
			in := e.msg(c, fmt.Sprint(2000+i), time.Duration(i)*time.Second)
			if i%2 == 0 {
				in.Kind = types.InboundSelf
				in.From.ID = testAccount
			}
			rows = append(rows, in)
		}
		return rows, nil
	}
	e.run()
	g := e.gaps()
	if e.p.count() != 1 || e.n() != 100 || len(g) != 1 || g[0].Reason != "page-cap" || g[0].FromMS != e.clk.Now().Add(-61*time.Minute).UnixMilli() || g[0].ToMS != e.clk.Now().Add(-99*time.Second).UnixMilli() {
		t.Fatalf("私聊单页缺口错误：calls=%d n=%d %+v", e.p.count(), e.n(), g)
	}
}

// B3、B5、B6：短页、重复锚点都不能代替起点覆盖；已取得的消息照存。
func TestBackfillPartialAndEmpty(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"cap", "failed", "anchor", "empty-known", "empty-new", "unsupported"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newBackfillEnv(t)
			e.b.owner = ""
			e.b.groups = []string{testGroup}
			c := backfillGroup()
			if name == "empty-known" {
				e.save(e.msg(c, "900", 2*time.Hour))
			}
			e.p.unsupported = name == "unsupported"
			e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
				n := e.p.count()
				if name == "failed" && n == 3 {
					return nil, errors.New("编的超时")
				}
				if name == "empty-known" || name == "empty-new" {
					return nil, nil
				}
				if name == "anchor" && q.anchor != "" {
					return []types.Inbound{e.msg(c, q.anchor, time.Minute)}, nil
				}
				rows := []types.Inbound{e.msg(c, fmt.Sprint(3000+n), time.Duration(n)*time.Minute)}
				if q.anchor != "" {
					rows = append(rows, e.msg(c, q.anchor, time.Duration(n-1)*time.Minute))
				}
				return rows, nil
			}
			e.run()
			g := e.gaps()
			reason := map[string]string{"cap": "page-cap", "failed": "call-failed", "anchor": "stalled", "empty-known": "no-anchor", "empty-new": "no-anchor", "unsupported": "unsupported"}[name]
			if len(g) != 1 || g[0].Reason != reason {
				t.Fatalf("缺口原因要 %s：%+v", reason, g)
			}
			if name == "cap" && (e.p.count() != 5 || e.n() != 5) {
				t.Fatalf("cap calls=%d n=%d", e.p.count(), e.n())
			}
			if name == "failed" {
				// 页查询失败后此前的证明也失效，但已经取得的两条消息仍须保存。
				if e.n() != 2 || g[0].State != "blocked" || g[0].FromMS != e.clk.Now().Add(-61*time.Minute).UnixMilli() || g[0].ToMS != e.clk.Now().UnixMilli() {
					t.Fatalf("失败应保留整个未知区间和前两页消息：stored=%d gaps=%+v", e.n(), g)
				}
			}
			if name == "anchor" && (e.p.count() != 2 || e.n() != 1) {
				t.Fatal("只有重复锚点应停止且去重")
			}
		})
	}
}

// B7：回包在取消后才到，不能写消息、缺口或水位；实时新消息也不能盖掉旧洞。
func TestBackfillDisconnect(t *testing.T) {
	t.Parallel()
	e := newBackfillEnv(t)
	before := e.through()
	entered := make(chan struct{})
	release := make(chan struct{})
	e.p.read = func(ctx context.Context, q historyCall) ([]types.Inbound, error) {
		close(entered)
		<-release
		return []types.Inbound{e.msg(q.conv, "4001", time.Second)}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.b.round(ctx) }()
	<-entered
	cancel()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消结果 %v", err)
	}
	if e.n() != 0 || e.through() != before || len(e.gaps()) != 0 {
		t.Fatal("断线后继续写库了")
	}
	e.save(e.msg(backfillPrivate(), "4002", 0))
	if e.through() != before {
		t.Fatal("实时消息不得推进水位")
	}
	e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
		return []types.Inbound{e.msg(q.conv, "4001", time.Second), e.msg(q.conv, "4000", 2*time.Hour)}, nil
	}
	e.run()
	if e.n() != 2 {
		t.Fatal("重连没补回取消时的消息")
	}
	t.Run("入库已开始", func(t *testing.T) {
		t.Parallel()
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		e := newBackfillEnv(t, func(c *channel.Config) {
			c.Admit = func(types.Inbound) channel.Admission {
				close(entered)
				<-release
				return channel.Admission{}
			}
		})
		defer unblock()
		before := e.through()
		e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
			return []types.Inbound{e.msg(q.conv, "4010", time.Second)}, nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- e.b.round(ctx) }()
		<-entered
		cancel()
		select {
		case <-done:
			t.Fatal("停止返回时已开始的入库还没收尾，会迟到写库")
		case <-time.After(100 * time.Millisecond):
		}
		unblock()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("入库收尾后的取消结果：%v", err)
		}
		if e.n() != 1 || e.through() != before {
			t.Fatal("已开始的入库应在停止返回前收尾，水位不推")
		}
	})
}

// B8：120查询后剩余会话记budget，到期缺口优先，不能被活跃会话永远挤掉。
func TestBackfillRoundBudget(t *testing.T) {
	t.Parallel()
	e := newBackfillEnv(t)
	e.b.owner = ""
	for i := range 26 {
		e.b.groups = append(e.b.groups, fmt.Sprint(500+i))
	}
	e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
		rows := []types.Inbound{e.msg(q.conv, fmt.Sprint(e.p.count()), time.Second)}
		if q.anchor != "" {
			rows = append(rows, e.msg(q.conv, q.anchor, time.Second))
		}
		return rows, nil
	}
	e.run()
	if e.p.count() != 120 {
		t.Fatalf("查询数 %d", e.p.count())
	}
	budget := 0
	for _, g := range e.gaps() {
		if g.Reason == "budget" {
			budget++
		}
	}
	if budget != 2 {
		t.Fatalf("剩余两会话应记budget：%+v", e.gaps())
	}
	originals := e.gaps()
	e.clk.Advance(backfillRetry)
	e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
		e.p.mu.Lock()
		e.p.left = 0 // 同期 get_msg 用掉这一轮剩余预算。
		e.p.mu.Unlock()
		return []types.Inbound{e.msg(q.conv, "998", time.Second)}, nil
	}
	e.run()
	if got := e.p.calls[120].conv.ID; got != "524" {
		t.Fatalf("上轮预算未轮到的会话应先补，首个却是 %s", got)
	}
	for _, g := range e.gaps() {
		if g.State == "open" && (g.Tries != 0 || g.LastTryMS != e.clk.Now().UnixMilli()) {
			t.Fatalf("预算未扫描区间应继续等待：%+v", g)
		}
	}
	e.clk.Advance(backfillRetry)
	e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
		return []types.Inbound{e.msg(q.conv, "999", 24*time.Hour)}, nil
	}
	e.run()
	for _, old := range originals {
		g, err := e.st.ChannelGap(context.Background(), old.ID)
		if err != nil {
			t.Fatal(err)
		}
		if old.Reason == "budget" {
			if g.State != "filled" {
				t.Fatal("预算未扫描区间没在后续轮次补齐", g)
			}
		} else if g != old {
			t.Fatal("已扫描受阻段被预算轮次复查", old, g)
		}
	}
}

// B9、B10：三次connected只起一轮；稳定前掉线不查；抖动推迟的一轮补完前不推水位。
func TestBackfillDebounceWatermark(t *testing.T) {
	t.Parallel()
	e := newBackfillEnv(t)
	e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
		return []types.Inbound{e.msg(q.conv, "5001", 2*time.Hour)}, nil
	}
	for range 3 {
		e.b.connected(context.Background())
	}
	e.clk.WaitTimer(t, 30*time.Second)
	e.clk.Advance(29 * time.Second)
	if e.p.count() != 0 {
		t.Fatal("稳定30秒前不应查询")
	}
	e.b.stop()
	e.b.connected(context.Background())
	e.clk.WaitTimer(t, 30*time.Second)
	e.clk.Advance(30 * time.Second)
	e.clk.WaitTimer(t, time.Minute)
	if e.p.count() != 1 {
		t.Fatalf("首轮次数 %d", e.p.count())
	}
	at := e.through()
	e.clk.Advance(time.Minute)
	e.clk.WaitTimer(t, time.Minute)
	// 一轮刚结束的第一个分钟推进仍是旧值，第二个分钟才严格增加。
	e.clk.Advance(time.Minute)
	e.clk.WaitTimer(t, time.Minute)
	if e.through() <= at {
		t.Fatal("在线水位未按分钟推进")
	}
	e.b.stop()
	e.b.connected(context.Background())
	e.clk.WaitTimer(t, 30*time.Second)
	e.clk.Advance(30 * time.Second)
	e.clk.WaitTimer(t, time.Minute)
	saved := e.through()
	e.clk.Advance(time.Minute)
	e.clk.WaitTimer(t, time.Minute)
	if e.p.count() != 1 || e.through() != saved {
		t.Fatal("抖动期间重复查询或在补空档前推进水位")
	}
	e.b.stop()
	// 未就绪的连接一分钟内不拉历史、不推进水位。
	e.p.mu.Lock()
	e.p.ready = false
	e.p.mu.Unlock()
	e.b.connected(context.Background())
	e.clk.WaitTimer(t, time.Second)
	e.clk.Advance(time.Minute)
	<-e.b.done
	if e.p.count() != 1 || e.through() != saved {
		t.Fatal("未就绪连接动了水位")
	}
}

// ADR-278：无法证明的历史只自动扫一轮；在线定时和重连的新窗口都不复查旧洞。
func TestBackfillBlockedScannedOnce(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"私聊", "群空号", "缓存"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newBackfillEnv(t)
			c := backfillPrivate()
			if name != "私聊" {
				c = backfillGroup()
				e.b.owner, e.b.groups = "", []string{testGroup}
			}
			e.p.continuous, e.p.stale = false, name == "缓存"
			e.p.read = func(context.Context, historyCall) ([]types.Inbound, error) {
				left, right := e.msg(c, "6100", 2*time.Hour), e.msg(c, "6101", time.Minute)
				left.MessageSeq, right.MessageSeq = 40, 42
				return []types.Inbound{right, left}, nil
			}
			e.b.connected(context.Background())
			e.clk.WaitTimer(t, 30*time.Second)
			e.clk.Advance(30 * time.Second)
			e.clk.WaitTimer(t, time.Minute)
			original, calls := e.gaps()[0], e.p.count()
			if original.State != "blocked" {
				t.Fatal("首轮未明确受阻", original)
			}
			for range 60 {
				e.clk.Advance(time.Minute)
				e.clk.WaitTimer(t, time.Minute)
			}
			e.b.stop()
			current, err := e.st.ChannelGap(context.Background(), original.ID)
			if err != nil || current != original || e.p.count() != calls {
				t.Fatalf("受阻旧洞被定时复查: before=%+v after=%+v calls=%d/%d err=%v", original, current, calls, e.p.count(), err)
			}
			e.b.connected(context.Background())
			e.clk.WaitTimer(t, 30*time.Second)
			e.clk.Advance(30 * time.Second)
			e.clk.WaitTimer(t, time.Minute)
			e.b.stop()
			current, err = e.st.ChannelGap(context.Background(), original.ID)
			if err != nil || current != original {
				t.Fatal("重连新尾段合并或复活旧洞", current, err)
			}
			page, err := e.st.ReadAccountGaps(context.Background(), testAccount, "", 100)
			if err != nil || len(page.Items) < 1 || page.Items[0].ID != original.ID {
				t.Fatal("停止自动后缺口不可见", page, err)
			}
		})
	}
}

// B15：十个角色观察同群，10000条通过真实Submit，多会话多轮，历史跨度30天。
func TestBackfillScale10000(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("压力和规模测试，放在每日整套")
	}
	e := newBackfillEnv(t)
	e.b.owner = ""
	var accounts []channel.Account
	for i := range 10 {
		accounts = append(accounts, channel.Account{Channel: "qq", ID: fmt.Sprint(100 + i), Character: fmt.Sprintf("role%d", i)})
	}
	e.b.intake.Close()
	in, err := channel.NewIntake(channel.Config{Stores: e.set, Roster: channel.Roster{Accounts: accounts}, Now: e.clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	e.b.intake = in
	for i := range 10 {
		e.b.groups = append(e.b.groups, fmt.Sprint(700+i))
	}
	e.watermark(e.clk.Now().Add(-30 * 24 * time.Hour))
	var lat []time.Duration
	var heapPeak uint64
	started := time.Now()
	round := 0
	e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
		if round == 2 {
			var rows []types.Inbound
			if q.conv.ID == "700" {
				for i := range 80 {
					rows = append(rows, e.msg(q.conv, fmt.Sprint(30000+i), time.Duration(i)*time.Second))
				}
			}
			return append(rows, e.msg(q.conv, "39999", 31*24*time.Hour)), nil
		}
		begin := 0
		if q.anchor != "" {
			var id int
			if _, err := fmt.Sscanf(q.anchor, "%d", &id); err != nil {
				t.Fatal(err)
			}
			begin = id % 10000 // B14：下一页包含上一页最旧的锚点本身。
		}
		rows := make([]types.Inbound, 100)
		for i := range rows {
			rows[i] = e.msg(q.conv, fmt.Sprint((round+1)*10000+begin+i), time.Duration(begin+i)*time.Second)
		}
		return rows, nil
	}
	for round = 0; round < 3; round++ {
		if round != 0 {
			e.clk.Advance(backfillRetry)
		}
		e.run()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		heapPeak = max(heapPeak, m.HeapAlloc)
	}
	if e.n() != 10000 {
		t.Fatalf("放大量只补到 %d", e.n())
	}
	newElapsed := time.Since(started)
	// 一万条新事件之后，另9个角色各观察1000条重叠消息，验证同群多角色不重复事件。
	rows, err := e.st.Events.Query(`SELECT conversation,platform_id FROM channel_msgs ORDER BY event_seq LIMIT 1000`)
	if err != nil {
		t.Fatal(err)
	}
	var saved []types.Inbound
	for rows.Next() {
		var c, id string
		if err := rows.Scan(&c, &id); err != nil {
			t.Fatal(err)
		}
		conv, err := types.ParseConversation(c)
		if err != nil {
			t.Fatal(err)
		}
		saved = append(saved, e.msg(conv, id, time.Second))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for i := range 9 {
		for _, m := range saved {
			m.Account = fmt.Sprint(101 + i)
			ts := time.Now()
			tk, err := in.Submit(context.Background(), m, channel.Options{Backfill: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tk.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			lat = append(lat, time.Since(ts))
		}
	}
	var observations int
	if err := e.st.Events.QueryRow(`SELECT count(*) FROM observations`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if e.n() != 10000 || observations != 19000 {
		t.Fatalf("十角色去重 events=%d obs=%d", e.n(), observations)
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	t.Logf("B15 new10000Elapsed=%v newMessageMean=%v", newElapsed, newElapsed/10000)
	t.Logf("B15 events=10000 roles=10 observations=%d elapsed=%v heapSamplePeak=%d bytes intakeP99=%v", observations, time.Since(started), heapPeak, lat[len(lat)*99/100])
}
