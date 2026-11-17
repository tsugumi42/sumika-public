package proactive

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// 调度壳（规格-主动联系 第十六节第 2 条）：每次醒、被拦、没醒到都有日志行，同样的错误只记两次，启动补记，关着不记。角色、时间都是编的。

type harness struct {
	t   *testing.T
	st  Store
	p   *config.Proactive
	sg  Snapshot
	err error
	now time.Time
	d   *Director
	mu  sync.Mutex
	w   Waker
}

func newHarness(t *testing.T, st Store) *harness {
	t.Helper()
	p := params()
	h := &harness{t: t, st: st, p: &p, now: at(9, 14, 0)}
	d, err := New(Options{
		Store:  st,
		Config: func() config.Proactive { return *h.p },
		Signals: func(context.Context, string, time.Time) (Snapshot, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.sg, h.err
		},
		Waker: nil,
		Now:   func() time.Time { return h.now },
		Rand:  fixed(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.d = d
	return h
}

func (h *harness) tick(at time.Time) {
	h.t.Helper()
	h.now = at
	h.d.Tick(context.Background(), at)
}

func rowsOf(t *testing.T, st Store) []Row {
	t.Helper()
	switch s := st.(type) {
	case *MemStore:
		return append([]Row(nil), s.Rows...)
	case DBStore:
		rs, err := s.Recent(1000)
		if err != nil {
			t.Fatal(err)
		}
		for i, j := 0, len(rs)-1; i < j; i, j = i+1, j-1 {
			rs[i], rs[j] = rs[j], rs[i]
		}
		return rs
	}
	t.Fatal("不认识的 Store")
	return nil
}

func TestFirstTickSchedulesWithoutRow(t *testing.T) {
	st := &MemStore{}
	h := newHarness(t, st)
	h.tick(at(9, 14, 0))
	s, ok, _ := st.LoadState("aa")
	if !ok || !s.NextWake.Equal(at(9, 16, 0)) || s.Factor != 1 {
		t.Fatalf("第一次只排下次（基准下限 120 分钟）：%+v", s)
	}
	if len(st.Rows) != 0 {
		t.Fatalf("排第一次不该写日志行：%+v", st.Rows)
	}
	h.tick(at(9, 15, 59))
	if len(st.Rows) != 0 {
		t.Fatalf("没到点不该有行：%+v", st.Rows)
	}
}

func TestDueWritesWakeRowAndReschedules(t *testing.T) {
	st := &MemStore{}
	h := newHarness(t, st)
	h.tick(at(9, 14, 0))
	h.sg = Snapshot{LastContact: at(9, 10, 0), OwnerLastActive: at(9, 15, 0)}
	h.tick(at(9, 16, 0))
	rs := st.Rows
	if len(rs) != 1 || rs[0].Kind != KindWake || rs[0].Reason != ReasonDue || rs[0].Thread != "" || rs[0].Character != "aa" {
		t.Fatalf("到点只写一行本该醒：%+v", rs)
	}
	if !rs[0].At.Equal(at(9, 16, 0)) || !rs[0].NextAt.Equal(at(9, 18, 0)) {
		t.Fatalf("时间：%+v", rs[0])
	}
	s, _, _ := st.LoadState("aa")
	if !s.NextWake.Equal(at(9, 18, 0)) || !s.LastWake.Equal(at(9, 16, 0)) || s.Factor != 1 {
		t.Fatalf("状态：%+v", s)
	}
	h.tick(at(9, 17, 59))
	if len(st.Rows) != 1 {
		t.Fatalf("没到点又多了行：%+v", st.Rows)
	}
}

func TestEveryReasonProducesARow(t *testing.T) {
	type c struct {
		name   string
		prep   func(h *harness)
		reason string
		kind   Kind
	}
	cases := []c{
		{"due", func(h *harness) {}, ReasonDue, KindWake},
		{"due-asleep-active", func(h *harness) { h.sg = Snapshot{OwnerLastActive: at(9, 1, 40)} }, ReasonDueAsleepActive, KindWake},
		{"snoozed", func(h *harness) { s, _, _ := h.st.LoadState("aa"); s.QuietUntil = at(9, 20, 0); h.st.SaveState(s) }, ReasonSnoozed, KindSkip},
		{"daily-cap", func(h *harness) { h.sg = Snapshot{ThreadsToday: 4} }, ReasonDailyCap, KindSkip},
		{"already-open", func(h *harness) { h.sg = Snapshot{OpenThread: true} }, ReasonAlreadyOpen, KindSkip},
		{"followup", func(h *harness) { h.sg = Snapshot{OpenThread: true, FollowUpDue: true} }, ReasonDue, KindFollowUp},
		{"owner-chatting", func(h *harness) { h.sg = Snapshot{OwnerLastActive: at(9, 1, 58)} }, ReasonOwnerChatting, KindSkip},
		{"asleep-no-activity", func(h *harness) {}, ReasonAsleepNoActive, KindSkip},
		{"cold-streak", func(h *harness) {
			s, _, _ := h.st.LoadState("aa")
			s.ColdStreak, s.LastWake = 3, at(9, 0, 30)
			h.st.SaveState(s)
		}, ReasonColdStreak, KindSkip},
		{"error", func(h *harness) { h.err = errors.New("读库失败") }, ReasonError, KindSkip},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &MemStore{}
			h := newHarness(t, st)
			// 默认放在凌晨 2 点，让 due-asleep-active、asleep-no-activity 能成立；要白天的用例自己换时间
			now := at(9, 2, 0)
			if tc.name == "due" || tc.name == "snoozed" || tc.name == "daily-cap" || tc.name == "already-open" || tc.name == "followup" || tc.name == "cold-streak" || tc.name == "error" {
				now = at(9, 14, 0)
			}
			h.tick(now.Add(-3 * time.Hour))
			s, _, _ := st.LoadState("aa")
			s.NextWake = now.Add(-time.Minute)
			st.SaveState(s)
			tc.prep(h)
			h.tick(now)
			if len(st.Rows) != 1 {
				t.Fatalf("应有且只有一行：%+v", st.Rows)
			}
			r := st.Rows[0]
			if r.Reason != tc.reason || r.Kind != tc.kind || len(r.Inputs) == 0 || r.NextAt.IsZero() {
				t.Fatalf("日志行 %+v，想要 %s/%s 且带 inputs 和下次", r, tc.kind, tc.reason)
			}
			s, _, _ = st.LoadState("aa")
			if !s.NextWake.Equal(r.NextAt) {
				t.Fatalf("状态里的下次 %v 要和日志一致 %v", s.NextWake, r.NextAt)
			}
		})
	}
}

func TestDisabledOrUnlistedWritesNothing(t *testing.T) {
	st := &MemStore{}
	h := newHarness(t, st)
	h.p.Enabled = false
	h.tick(at(9, 14, 0))
	h.tick(at(9, 20, 0))
	h.d.Catchup(context.Background(), at(9, 21, 0))
	if len(st.Rows) != 0 || len(st.States) != 0 {
		t.Fatalf("关着不该有任何状态和日志：%+v %+v", st.Rows, st.States)
	}
	h.p.Enabled, h.p.Characters = true, nil
	h.tick(at(9, 22, 0))
	if len(st.Rows) != 0 || len(st.States) != 0 {
		t.Fatalf("没列角色也不该有：%+v", st.States)
	}
	// 热更新：打开以后下一次轮询就生效
	h.p.Characters = []string{"aa"}
	h.tick(at(9, 22, 5))
	if _, ok, _ := st.LoadState("aa"); !ok {
		t.Fatal("打开以后应当排上第一次")
	}
}

func TestCatchupWritesMissedOfflineOnce(t *testing.T) {
	st := &MemStore{}
	h := newHarness(t, st)
	// 角色 aa：上次排的 14:00，一直没开机，18:00 起来
	st.SaveState(State{Character: "aa", Factor: 2, NextWake: at(9, 14, 0)})
	h.d.Catchup(context.Background(), at(9, 18, 0))
	if len(st.Rows) != 1 || st.Rows[0].Reason != ReasonMissedOffline || st.Rows[0].Kind != KindSkip {
		t.Fatalf("应补一行没醒到：%+v", st.Rows)
	}
	r := st.Rows[0]
	s, _, _ := st.LoadState("aa")
	if !s.NextWake.After(at(9, 18, 0)) || !r.NextAt.Equal(s.NextWake) || s.NextWake.Sub(at(9, 18, 0)) != 5*time.Minute {
		t.Fatalf("不当场补跑，也不从头排一整段：5 分钟后醒（随机取 0）：%+v", s)
	}
	var in map[string]any
	if err := json.Unmarshal(r.Inputs, &in); err != nil || in["missedMin"].(float64) != 240 {
		t.Fatalf("inputs 要记晚了多少分钟：%v %v", in, err)
	}
	// 再启动一次：下次在未来，不重复补
	h.d.Catchup(context.Background(), at(9, 18, 1))
	if len(st.Rows) != 1 {
		t.Fatalf("不该重复补记：%+v", st.Rows)
	}
	// 到点时进程开着、已经有那次之后的行：不补
	st2 := &MemStore{}
	h2 := newHarness(t, st2)
	st2.SaveState(State{Character: "aa", Factor: 1, NextWake: at(9, 14, 0)})
	st2.AddRow(Row{At: at(9, 14, 0), Character: "aa", Kind: KindSkip, Reason: ReasonAlreadyOpen})
	h2.d.Catchup(context.Background(), at(9, 18, 0))
	if len(st2.Rows) != 1 {
		t.Fatalf("有日志行就说明到点时在运行：%+v", st2.Rows)
	}
	// 从没排过的角色不补
	st3 := &MemStore{}
	newHarness(t, st3).d.Catchup(context.Background(), at(9, 18, 0))
	if len(st3.Rows) != 0 {
		t.Fatalf("没排过的不补：%+v", st3.Rows)
	}
}

func TestRepeatedErrorsOnlyLoggedTwice(t *testing.T) {
	st := &MemStore{}
	h := newHarness(t, st)
	h.tick(at(9, 14, 0))
	h.err = errors.New("读库失败")
	now := at(9, 16, 0)
	for i := 0; i < 6; i++ {
		h.tick(now)
		now = now.Add(15 * time.Minute) // 每次错都顺延 deferMinutes，下次轮询又到点
	}
	if len(st.Rows) != 2 {
		t.Fatalf("同样的错误只记两行：%d 行 %+v", len(st.Rows), st.Rows)
	}
	if st.Rows[0].Count != 1 || st.Rows[1].Count != 5 {
		t.Fatalf("第二行合并计数应是 1+4=5：%d %d", st.Rows[0].Count, st.Rows[1].Count)
	}
	// 换一种错误：重新记
	h.err = errors.New("另一种错误")
	h.tick(now)
	if len(st.Rows) != 3 || st.Rows[2].Count != 1 {
		t.Fatalf("不同的错误要另记：%+v", st.Rows)
	}
	// 中间有过正常的行：错误不再算连着
	h.err = nil
	now = now.Add(15 * time.Minute)
	h.sg = Snapshot{ThreadsToday: 4}
	h.tick(now)
	h.err = errors.New("另一种错误")
	now = at(10, 10, 0)
	for i := 0; i < 3; i++ {
		h.tick(now)
		now = now.Add(15 * time.Minute)
	}
	last := st.Rows[len(st.Rows)-1]
	if last.Reason != ReasonError || last.Count != 2 {
		t.Fatalf("正常行之后重新计：后两次里第二行应合并成 2：%+v", last)
	}
	if n := len(st.Rows); n != 3+1+2 {
		t.Fatalf("总行数：%d：%+v", n, st.Rows)
	}
}

type fakeWaker struct {
	res  WakeResult
	err  error
	reqs []WakeRequest
}

func (f *fakeWaker) Wake(_ context.Context, r WakeRequest) (WakeResult, error) {
	f.reqs = append(f.reqs, r)
	return f.res, f.err
}

func TestWakerGetsRequestAndRowIsAnnotated(t *testing.T) {
	st := &MemStore{}
	h := newHarness(t, st)
	w := &fakeWaker{res: WakeResult{Reason: "model-quiet", ModelNote: "他在忙，先不打扰", NextAt: at(9, 19, 0)}}
	h.d.opt.Waker = w
	h.tick(at(9, 14, 0))
	h.tick(at(9, 16, 0))
	if len(w.reqs) != 1 || w.reqs[0].Character != "aa" || w.reqs[0].Kind != KindWake || w.reqs[0].RowID != 1 || w.reqs[0].Reason != ReasonDue {
		t.Fatalf("叫醒请求：%+v", w.reqs)
	}
	if len(st.Rows) != 1 || st.Rows[0].Reason != "model-quiet" || st.Rows[0].ModelNote != "他在忙，先不打扰" {
		t.Fatalf("结局补到同一行：%+v", st.Rows)
	}
	if s, _, _ := st.LoadState("aa"); !s.NextWake.Equal(at(9, 19, 0)) {
		t.Fatalf("结果里给了下次：%+v", s)
	}
	// 叫醒失败：记一行错误，仍然重排，不循环
	w.err = errors.New("模型不可用")
	h.tick(at(9, 19, 0))
	if len(w.reqs) != 2 {
		t.Fatalf("应再叫一次：%d", len(w.reqs))
	}
	last := st.Rows[len(st.Rows)-1]
	if last.Reason != ReasonError || last.ModelNote == "" {
		t.Fatalf("失败要有错误行：%+v", st.Rows)
	}
	if s, _, _ := st.LoadState("aa"); !s.NextWake.After(at(9, 19, 0)) {
		t.Fatalf("失败也要往后排：%+v", s)
	}
	// 备注限 80 字
	w.err, w.res = nil, WakeResult{ModelNote: string(make([]rune, 0)) + repeat("字", 200)}
	h.tick(st.States["aa"].NextWake)
	if n := len([]rune(st.Rows[len(st.Rows)-2].ModelNote)); n > 80 {
		t.Fatalf("备注超过 80 字：%d", n)
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func TestPrunesOldRows(t *testing.T) {
	st := &MemStore{}
	h := newHarness(t, st)
	st.AddRow(Row{At: at(9, 1, 0).AddDate(0, 0, -100), Character: "aa", Kind: KindSkip, Reason: ReasonSnoozed})
	st.AddRow(Row{At: at(9, 1, 0).AddDate(0, 0, -10), Character: "aa", Kind: KindSkip, Reason: ReasonSnoozed})
	h.tick(at(9, 14, 0))
	if len(st.Rows) != 1 {
		t.Fatalf("90 天前的应被清掉，10 天前的留着：%+v", st.Rows)
	}
}

func TestDBStoreRoundTrip(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	db := DBStore{DB: s.Trace}

	if _, ok, err := db.LoadState("aa"); ok || err != nil {
		t.Fatalf("没存过：%v %v", ok, err)
	}
	want := State{Character: "aa", Factor: 1.5, QuietUntil: at(9, 20, 0), ColdStreak: 2, NextWake: at(9, 16, 0), LastWake: at(9, 14, 0)}
	if err := db.SaveState(want); err != nil {
		t.Fatal(err)
	}
	want.Factor = 0.7
	if err := db.SaveState(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := db.LoadState("aa")
	if err != nil || !ok || got.Factor != 0.7 || got.ColdStreak != 2 || !got.QuietUntil.Equal(want.QuietUntil) || !got.NextWake.Equal(want.NextWake) || !got.LastWake.Equal(want.LastWake) {
		t.Fatalf("读回：%+v %v %v", got, ok, err)
	}
	if sts, err := db.States(); err != nil || len(sts) != 1 {
		t.Fatalf("States：%+v %v", sts, err)
	}

	id, err := db.AddRow(Row{At: at(9, 14, 0), Character: "aa", Kind: KindWake, Reason: ReasonDue, Inputs: []byte(`{"factor":1}`), NextAt: at(9, 16, 0)})
	if err != nil || id == 0 {
		t.Fatal(id, err)
	}
	if err := db.AddCount(id, 2); err != nil {
		t.Fatal(err)
	}
	if err := db.Annotate(id, "model-quiet", "", "先不打扰"); err != nil {
		t.Fatal(err)
	}
	if err := db.Annotate(id, "", "th-1", ""); err != nil {
		t.Fatal(err)
	}
	rs, err := db.Recent(10)
	if err != nil || len(rs) != 1 {
		t.Fatal(rs, err)
	}
	r := rs[0]
	if r.Count != 3 || r.Reason != "model-quiet" || r.ModelNote != "先不打扰" || r.Thread != "th-1" || string(r.Inputs) != `{"factor":1}` || !r.NextAt.Equal(at(9, 16, 0)) || r.Kind != KindWake {
		t.Fatalf("读回的行：%+v", r)
	}
	if seen, _ := db.HasRowSince("aa", at(9, 14, 0)); !seen {
		t.Fatal("应能查到")
	}
	if seen, _ := db.HasRowSince("aa", at(9, 14, 1)); seen {
		t.Fatal("之后没有行")
	}
	if seen, _ := db.HasRowSince("bb", at(9, 0, 0)); seen {
		t.Fatal("别的角色没有行")
	}
	if n, err := db.Prune(at(9, 15, 0)); err != nil || n != 1 {
		t.Fatalf("清理：%d %v", n, err)
	}

	// 整套走一遍真库：排第一次、到点、没醒到
	h := newHarness(t, db)
	h.p.Characters = []string{"bb"}
	h.tick(at(9, 14, 0))
	h.sg = Snapshot{}
	h.tick(at(9, 16, 0))
	h.d.Catchup(context.Background(), at(9, 23, 0))
	rows := rowsOf(t, db)
	if len(rows) != 2 || rows[0].Reason != ReasonDue || rows[1].Reason != ReasonMissedOffline {
		t.Fatalf("真库里的日志：%+v", rows)
	}
}
