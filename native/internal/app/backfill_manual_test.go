package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/desktopread"
	"sumika-native/internal/store"
)

func gapRef(g store.ChannelGap) api.GapRef {
	c, _ := types.ParseConversation(g.Conversation)
	return api.GapRef{ConversationKey: backfillKey(c), ID: g.ID, FromMS: g.FromMS, ToMS: g.ToMS}
}
func manualGap(t *testing.T, e *backfillEnv, c types.Conversation, from, to time.Duration, state string) store.ChannelGap {
	t.Helper()
	err := e.st.RecordChannelGap(context.Background(), store.ChannelGap{Channel: "qq", Conversation: c.String(), FromMS: e.clk.Now().Add(-from).UnixMilli(), ToMS: e.clk.Now().Add(-to).UnixMilli(), Reason: "outage", LastTryMS: e.clk.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	gaps := e.gaps()
	var g store.ChannelGap
	for _, row := range gaps {
		if row.Conversation == c.String() {
			g = row
		}
	}
	g.State, g.Tries = state, 3
	if err = e.st.UpdateChannelGap(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	return g
}

// 手动测试走生产 runQueued；只跳过连接防抖，连接防抖另用真实 App.Run 验收。
func manualConnection(t *testing.T, e *backfillEnv) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e.b.cancel = cancel
	done := make(chan struct{})
	e.b.done = done
	e.b.tasks.Lock()
	e.b.initTasksLocked()
	e.b.tasks.online = true
	e.b.tasks.Unlock()
	t.Cleanup(func() { cancel(); e.b.cancelQueued(ctx); e.b.endQueries(); close(done) })
	return ctx
}
func drainManual(t *testing.T, e *backfillEnv, ctx context.Context, j api.BackfillJob) api.BackfillJob {
	t.Helper()
	if err := e.b.runQueued(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := e.b.job(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func requireBackfillCode(t *testing.T, err error, code string) {
	t.Helper()
	var be *api.BackfillError
	if !errors.As(err, &be) || be.Code != code {
		t.Fatalf("want %s got %v", code, err)
	}
}

// A02：非法身份、ID、区间和模式拒绝后没有历史查询或业务写入。
func TestGapActionInputMatrix(t *testing.T) {
	t.Parallel()
	e := newBackfillEnv(t)
	e.b.groups = []string{testGroup}
	g := manualGap(t, e, backfillGroup(), 20*time.Minute, 10*time.Minute, "open")
	ctx := manualConnection(t, e)
	ref := gapRef(g)
	cases := []struct {
		name, code string
		change     func(*api.GapRef)
	}{
		{"缺ID", "invalid", func(r *api.GapRef) { r.ID = 0 }}, {"负ID", "invalid", func(r *api.GapRef) { r.ID = -1 }},
		{"空身份", "invalid", func(r *api.GapRef) { r.ConversationKey = api.ConversationKey{} }},
		{"反向", "invalid", func(r *api.GapRef) { r.FromMS = r.ToMS + 1 }},
		{"伪造平台", "invalid", func(r *api.GapRef) { r.ConversationKey.Platform = "local" }},
		{"本地私聊", "invalid", func(r *api.GapRef) {
			r.ConversationKey = api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:example"}
		}},
		{"本地群聊", "invalid", func(r *api.GapRef) {
			r.ConversationKey = api.ConversationKey{Platform: "local", Channel: "console", Scene: "group", Conversation: "console:group:pretend"}
		}},
		{"伪造self", "invalid", func(r *api.GapRef) {
			r.ConversationKey = backfillKey(types.Conversation{Channel: "qq", Scene: "private", Self: "999", ID: testOwner})
		}},
		{"冒充名字", "invalid", func(r *api.GapRef) {
			r.ConversationKey = backfillKey(types.Conversation{Channel: "qq", Scene: "group", ID: "主人"})
		}},
		{"非白名单", "invalid", func(r *api.GapRef) {
			r.ConversationKey = backfillKey(types.Conversation{Channel: "qq", Scene: "group", ID: "999"})
		}},
		{"变宽", "stale-gap", func(r *api.GapRef) { r.ToMS++ }}, {"消失", "stale-gap", func(r *api.GapRef) { r.ID = 999999 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := ref
			tc.change(&r)
			_, err := e.b.requestGap(ctx, r)
			requireBackfillCode(t, err, tc.code)
		})
	}
	e.p.mu.Lock()
	e.p.ready = false
	e.p.mu.Unlock()
	_, err := e.b.requestGap(ctx, ref)
	requireBackfillCode(t, err, "unavailable")
	e.p.mu.Lock()
	e.p.ready = true
	e.p.unsupported = true
	e.p.mu.Unlock()
	_, err = e.b.requestGap(ctx, ref)
	requireBackfillCode(t, err, "unsupported")
	current, _ := e.st.ChannelGap(ctx, g.ID)
	if current.LastManualMS != 0 || e.n() != 0 || e.p.count() != 0 {
		t.Fatalf("被拒仍产生副作用 %+v events=%d calls=%d", current, e.n(), e.p.count())
	}
}

// M01：同页左右端外消息只提供衔接；只提交登记区间，不动水位或邻居缺口。
func TestManualGapOnlyStoresTargetInterval(t *testing.T) {
	t.Parallel()
	for _, c := range []types.Conversation{backfillPrivate(), backfillGroup()} {
		t.Run(c.Scene, func(t *testing.T) {
			t.Parallel()
			var stored []channel.Stored
			var mu sync.Mutex
			e := newBackfillEnv(t, func(c *channel.Config) {
				c.OnStored = func(s channel.Stored) { mu.Lock(); stored = append(stored, s); mu.Unlock() }
			})
			e.b.groups = []string{testGroup}
			g := manualGap(t, e, c, 20*time.Minute, 10*time.Minute, "abandoned")
			if err := e.st.RecordChannelGap(context.Background(), store.ChannelGap{Channel: "qq", Conversation: c.String(), FromMS: g.ToMS + 1, ToMS: g.ToMS + 100, Reason: "outage", LastTryMS: 9}); err != nil {
				t.Fatal(err)
			}
			before := e.through()
			ctx := manualConnection(t, e)
			e.p.read = func(context.Context, historyCall) ([]types.Inbound, error) {
				return []types.Inbound{e.msg(c, "1100", 5*time.Minute), e.msg(c, "1101", 10*time.Minute), e.msg(c, "1102", 15*time.Minute), e.msg(c, "1103", 20*time.Minute), e.msg(c, "1104", 25*time.Minute)}, nil
			}
			seed, seedErr := e.b.intake.Submit(ctx, e.msg(c, "1102", 15*time.Minute), channel.Options{Backfill: true})
			if seedErr != nil {
				t.Fatal(seedErr)
			}
			if _, seedErr = seed.Wait(ctx); seedErr != nil {
				t.Fatal(seedErr)
			}
			j, err := e.b.requestGap(ctx, gapRef(g))
			if err != nil {
				t.Fatal(err)
			}
			done := drainManual(t, e, ctx, j)
			if e.n() != 3 || e.through() != before || done.Inserted != 2 || done.Duplicates != 1 || !done.CompleteCoverage || done.State != "completed" {
				t.Fatalf("越界或计数错误 events=%d through=%d job=%+v", e.n(), e.through(), done)
			}
			current, _ := e.st.ChannelGap(ctx, g.ID)
			if current.State != "filled" || current.Tries != 3 || current.LastTryMS != g.LastTryMS || current.LastManualMS == 0 {
				t.Fatal(current)
			}
			// 自动轮次仍持有旧 open 副本时，不能复活已手动填平的原区间。
			staleAuto := g
			staleAuto.State = "open"
			if err = e.st.SettleQQBackfill(ctx, testAccount, before, []store.QQBackfillResult{{Conversation: g.Conversation, FromMS: g.FromMS, Updates: []store.ChannelGap{staleAuto}}}); err != nil {
				t.Fatal(err)
			}
			current, err = e.st.ChannelGap(ctx, g.ID)
			if err != nil || current.State != "filled" {
				t.Fatal("旧自动副本复活缺口", current, err)
			}
			neighbors := e.gaps()
			if len(neighbors) != 2 || neighbors[1].State != "open" || neighbors[1].LastTryMS != 9 {
				t.Fatal(neighbors)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, s := range stored {
				if len(s.Tags) != 1 || s.Tags[0] != channel.TagBackfill {
					t.Fatalf("不是补录入库 %+v", s)
				}
			}
			var out int
			if err = e.st.Events.QueryRow(`SELECT count(*) FROM outbound_parts`).Scan(&out); err != nil || out != 0 {
				t.Fatal("补录产生出站", out, err)
			}
			queries := e.p.count()
			e.p.mu.Lock()
			e.p.ready = false
			e.p.mu.Unlock()
			again, err := e.b.requestGap(ctx, gapRef(g))
			if err != nil || again.ID != j.ID || again.State != "completed" || !again.CompleteCoverage || e.p.count() != queries {
				t.Fatal("已填平重试不幂等", again, err)
			}
		})
	}
}

// M02：部分成果与未知首页保留缺口；手动不复活 abandoned，也不耗自动 tries。
func TestManualGapCoverageAndAbandonedRetry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, state string
	}{
		{"仅两端无新增", "abandoned"}, {"失败放弃段", "abandoned"}, {"失败开放段", "open"}, {"未知尾段", "open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newBackfillEnv(t)
			e.b.groups = []string{testGroup}
			c := backfillGroup()
			g := manualGap(t, e, c, 20*time.Minute, 10*time.Minute, tc.state)
			ctx := manualConnection(t, e)
			e.p.stale = tc.name == "未知尾段"
			e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
				if tc.name == "仅两端无新增" {
					return []types.Inbound{e.msg(c, "1200", 5*time.Minute), e.msg(c, "1201", 25*time.Minute)}, nil
				}
				if tc.name == "未知尾段" {
					return []types.Inbound{e.msg(c, "1201", 25*time.Minute)}, nil
				}
				if q.anchor != "" {
					return nil, errors.New("编的失败")
				}
				return []types.Inbound{e.msg(c, "1202", 12*time.Minute), e.msg(c, "1203", 15*time.Minute)}, nil
			}
			j, err := e.b.requestGap(ctx, gapRef(g))
			if err != nil {
				t.Fatal(err)
			}
			done := drainManual(t, e, ctx, j)
			current, _ := e.st.ChannelGap(ctx, g.ID)
			want := tc.state
			if want == "open" {
				want = "blocked"
			}
			if current.State != want || current.Tries != 3 || current.LastTryMS != g.LastTryMS || done.CompleteCoverage {
				t.Fatalf("错误结算 %+v %+v", current, done)
			}
			if tc.name == "失败放弃段" || tc.name == "失败开放段" {
				if e.n() != 2 || done.Inserted != 2 {
					t.Fatal("丢掉部分成果", e.n(), done)
				}
			}
		})
	}
}

// M03：并发合并、16 / 17 上限、共享预算、冷却边界及合并继承。
func TestManualGapQueueAndCooldown(t *testing.T) {
	t.Parallel()
	e := newBackfillEnv(t)
	ctx := manualConnection(t, e)
	refs := make([]api.GapRef, 17)
	for i := range refs {
		c := types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: fmt.Sprint(600 + i)}
		refs[i] = gapRef(manualGap(t, e, c, 20*time.Minute, 10*time.Minute, "abandoned"))
	}
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := e.b.requestGap(ctx, refs[0])
			if err != nil {
				t.Error(err)
				return
			}
			ids <- j.ID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if first != id {
			t.Fatal("重复点击产生多任务")
		}
	}
	for _, r := range refs[1:16] {
		if _, err := e.b.requestGap(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.b.requestGap(ctx, refs[16]); err == nil {
		t.Fatal("第17个排队未拒绝")
	} else {
		requireBackfillCode(t, err, "busy")
	}
	if err := e.b.runQueued(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.requestGap(ctx, refs[0]); err == nil {
		t.Fatal("连续手动未冷却")
	} else {
		requireBackfillCode(t, err, "cooldown")
	}
	if e.p.RemainingQueryBudget() != 104 {
		t.Fatalf("16次手动重置共享预算，剩余 %d", e.p.RemainingQueryBudget())
	}
	e.clk.Advance(backfillRetry - time.Millisecond)
	_, err := e.b.requestGap(ctx, refs[0])
	requireBackfillCode(t, err, "cooldown")
	e.clk.Advance(time.Millisecond)
	if _, err = e.b.requestGap(ctx, refs[0]); err != nil {
		t.Fatal("10分钟边界仍被拒", err)
	}
	g, _ := e.st.ChannelGap(ctx, refs[0].ID)
	// 手动补录已把它填平；这里要预置回 open，直接改库（UpdateChannelGap 不改终局行）
	if _, err = e.st.Events.ExecContext(ctx, `UPDATE channel_gaps SET state='open' WHERE id=?`, g.ID); err != nil {
		t.Fatal(err)
	}
	if err = e.st.RecordChannelGap(ctx, store.ChannelGap{Channel: "qq", Conversation: g.Conversation, FromMS: g.ToMS + 1, ToMS: g.ToMS + 100, Reason: "outage", LastTryMS: 1}); err != nil {
		t.Fatal(err)
	}
	merged, _ := e.st.ChannelGap(ctx, g.ID)
	if merged.LastManualMS != g.LastManualMS {
		t.Fatal("合并清空手动冷却", merged)
	}
	// 新工人使用同一个临时持久库，不能靠内存记冷却。
	other := &qqBackfill{st: e.st, intake: e.b.intake, source: e.p, clock: e.clk, account: testAccount, cancel: func() {}, logf: t.Logf}
	other.tasks.online = true
	_, err = other.requestGap(ctx, gapRef(merged))
	requireBackfillCode(t, err, "cooldown")
	t.Run("自动在途与单段终局", manualGapDuringAccount)
	t.Run("提交不绕过任务发布节奏", manualProgressCadence)
}

// M05：已开始的 Ticket 必须在 stop 返回前收尾，取消任务保留真实提交数。
func TestManualBackfillStopDrainsAcceptedWrites(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	e := newBackfillEnv(t, func(c *channel.Config) {
		c.Admit = func(types.Inbound) channel.Admission { close(entered); <-release; return channel.Admission{} }
	})
	e.b.groups = []string{testGroup}
	c := backfillGroup()
	g := manualGap(t, e, c, 20*time.Minute, 10*time.Minute, "open")
	ctx := manualConnection(t, e)
	e.p.read = func(context.Context, historyCall) ([]types.Inbound, error) {
		return []types.Inbound{e.msg(c, "1300", 15*time.Minute)}, nil
	}
	j, err := e.b.requestGap(ctx, gapRef(g))
	if err != nil {
		t.Fatal(err)
	}
	e.b.done = make(chan struct{})
	go func() { defer close(e.b.done); defer e.b.cancelQueued(ctx); _ = e.b.runQueued(ctx) }()
	<-entered
	stopped := make(chan struct{})
	go func() { e.b.stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Ticket未收尾就返回")
	case <-time.After(30 * time.Millisecond):
	}
	unblock.Do(func() { close(release) })
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop未收尾")
	}
	done, err := e.b.job(j.ID)
	if err != nil || done.State != "canceled" || done.Inserted != 1 || e.n() != 1 {
		t.Fatalf("取消丢计数 %+v %v events=%d", done, err, e.n())
	}
	current, _ := e.st.ChannelGap(context.Background(), g.ID)
	if current.State != "open" {
		t.Fatal("取消填平缺口", current)
	}
}

// 自动会话在途超过十分钟再接单段，仍共享该轮预算；父轮结束后单段终局仍可读取。
func manualGapDuringAccount(t *testing.T) {
	e := newBackfillEnv(t)
	e.b.owner = ""
	e.b.groups = []string{testGroup}
	c := backfillGroup()
	g := manualGap(t, e, c, 20*time.Minute, 10*time.Minute, "abandoned")
	ctx := manualConnection(t, e)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	rows := []types.Inbound{e.msg(c, "2401", 10*time.Minute), e.msg(c, "2402", 15*time.Minute), e.msg(c, "2403", 20*time.Minute), e.msg(c, "2400", 2*time.Hour)}
	e.p.read = func(context.Context, historyCall) ([]types.Inbound, error) {
		if e.p.count() == 1 {
			close(entered)
			<-release
		}
		return rows, nil
	}
	go func() { done <- e.b.executeAccount(ctx, "connected", nil) }()
	released := false
	finished := false
	defer func() {
		if !released {
			close(release)
		}
		if !finished {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("自动父轮次未收尾")
			}
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("自动历史请求没有开始")
	}
	e.clk.Advance(backfillRetry)
	j, err := e.b.requestGap(ctx, gapRef(g))
	if err != nil || j.State != "queued" {
		t.Fatal(j, err)
	}
	duplicate, err := e.b.requestGap(ctx, gapRef(g))
	if err != nil || duplicate.ID != j.ID {
		t.Fatal("自动在途时重复请求未合并", duplicate, err)
	}
	close(release)
	released = true
	select {
	case err = <-done:
		finished = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("插队任务或父轮次未收尾")
	}
	child, err := e.b.job(j.ID)
	if err != nil || child.State != "completed" || child.CompleteCoverage || child.Inserted != 0 || child.Duplicates != 3 {
		t.Fatal("父轮次结束吃掉单段终局", child, err)
	}
	current, err := e.st.ChannelGap(ctx, g.ID)
	if err != nil || current.State != "abandoned" || e.p.count() != 2 || e.p.RemainingQueryBudget() != 118 {
		t.Fatal("插队没有沿用同一预算", current, err, e.p.count(), e.p.RemainingQueryBudget())
	}
}

// 逐条真实提交不能绕过任务发布合并；初始、开始和终局单独计算。
func manualProgressCadence(t *testing.T) {
	e := newBackfillEnv(t, func(c *channel.Config) {
		c.Admit = func(types.Inbound) channel.Admission { time.Sleep(2 * time.Millisecond); return channel.Admission{} }
	})
	e.b.groups = []string{testGroup}
	c := backfillGroup()
	g := manualGap(t, e, c, 20*time.Minute, 10*time.Minute, "abandoned")
	ctx := manualConnection(t, e)
	reader := desktopread.New(e.set, []api.CharacterView{{ID: testChar}}, nil).WithBackfills(desktopread.BackfillProvider{Account: testAccount, Project: e.b.projectGap, RequestGap: e.b.requestGap, Job: e.b.job})
	defer reader.Close()
	e.p.read = func(context.Context, historyCall) ([]types.Inbound, error) {
		rows := []types.Inbound{e.msg(c, "2500", 5*time.Minute), e.msg(c, "2599", 25*time.Minute)}
		for i := range 98 {
			rows = append(rows, e.msg(c, fmt.Sprint(2501+i), 11*time.Minute+time.Duration(i)*time.Second))
		}
		return rows, nil
	}
	j, err := reader.RequestGapBackfill(ctx, gapRef(g))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := reader.WatchBackfill(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	guard := time.NewTimer(10 * time.Second)
	defer guard.Stop()
	select {
	case snap := <-sub.Updates():
		if snap.Err != nil || snap.Value.State != "queued" {
			t.Fatal(snap)
		}
	case <-guard.C:
		t.Fatal("排队快照未发布")
	}
	done := make(chan error, 1)
	e.b.done = make(chan struct{})
	go func() { defer close(e.b.done); defer e.b.cancelQueued(ctx); done <- e.b.runQueued(ctx) }()
	started := time.Now()
	progress := 0
	for {
		select {
		case snap, ok := <-sub.Updates():
			if !ok || snap.Err != nil {
				t.Fatal("任务订阅提前结束", snap)
			}
			if snap.Value.State == "running" && snap.Value.Inserted > 0 {
				progress++
			}
			if snap.Value.State == "completed" {
				elapsed := time.Since(started)
				allowed := int(elapsed/(200*time.Millisecond)) + 1
				if snap.Value.Inserted != 98 || !snap.Value.CompleteCoverage {
					t.Fatal("终局不真实", snap.Value)
				}
				if progress > allowed {
					t.Fatalf("提交失效绕过200ms合并：progress=%d allowed=%d elapsed=%v", progress, allowed, elapsed)
				}
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-guard.C:
					t.Fatal("任务工人未收尾")
				}
				return
			}
		case <-guard.C:
			t.Fatal("任务终局未发布")
		}
	}
}
