package loop

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

type harness struct {
	t     *testing.T
	clock *fakeClock
	store Store
	s     *Scheduler
	mu    sync.Mutex
	runs  []Request
	reps  []Report
	// reply 决定每一轮的回答；nil 时回 "ok"
	reply func(r Request) (string, error)
	busy  func(session string) bool
	goal  func(session string) bool
}

func newHarness(t *testing.T, store Store) *harness {
	h := &harness{t: t, clock: &fakeClock{t: t0}, store: store}
	if store == nil {
		h.store = FileStore{Path: filepath.Join(t.TempDir(), "loop.json")}
	}
	h.open()
	return h
}

func (h *harness) open() {
	s, err := New(Options{
		Store: h.store,
		Now:   h.clock.Now,
		Run: func(_ context.Context, r Request) (string, error) {
			h.mu.Lock()
			h.runs = append(h.runs, r)
			f := h.reply
			h.mu.Unlock()
			if f == nil {
				return "ok", nil
			}
			return f(r)
		},
		SessionBusy: func(s string) bool { return h.busy != nil && h.busy(s) },
		GoalActive:  func(s string) bool { return h.goal != nil && h.goal(s) },
		Deliver:     func(r Report) { h.mu.Lock(); h.reps = append(h.reps, r); h.mu.Unlock() },
	})
	if err != nil {
		h.t.Fatal(err)
	}
	h.s = s
}

func (h *harness) tick() { h.s.Tick(context.Background()); h.s.Wait() }
func (h *harness) runCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.runs)
}
func (h *harness) task(id int) Task {
	t, ok := h.s.Get(id)
	if !ok {
		h.t.Fatalf("没有任务 #%d", id)
	}
	return t
}

func TestFixedIntervalRunsWhenDue(t *testing.T) {
	h := newHarness(t, nil)
	task, err := h.s.Add("s1", "报一下现在几点", AddOptions{Interval: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	h.tick() // 第一轮马上跑
	if h.runCount() != 1 {
		t.Fatalf("第一轮没跑，runs=%d", h.runCount())
	}
	if !strings.Contains(h.runs[0].Input, "报一下现在几点") || !strings.Contains(h.runs[0].Input, "定期任务第 1 次") {
		t.Fatalf("输入不对：%q", h.runs[0].Input)
	}
	if got := h.task(task.ID).NextRun; !got.Equal(t0.Add(30 * time.Minute)) {
		t.Fatalf("下次应是 30 分钟后，得到 %v", got)
	}
	h.clock.Add(29 * time.Minute)
	h.tick()
	if h.runCount() != 1 {
		t.Fatal("没到点就跑了")
	}
	h.clock.Add(time.Minute)
	h.tick()
	if h.runCount() != 2 {
		t.Fatal("到点没跑")
	}
	if !strings.Contains(h.runs[1].Input, "定期任务第 2 次，上次在 2026-11-08 12:00") {
		t.Fatalf("第二轮的输入缺次数和上次时间：%q", h.runs[1].Input)
	}
	got := h.task(task.ID)
	if got.Runs != 2 || got.LastResult != ResultContent || len(got.History) != 2 {
		t.Fatalf("记录不对：%+v", got)
	}
}

func TestSelfPacedUsesScheduleNext(t *testing.T) {
	h := newHarness(t, nil)
	task, _ := h.s.Add("s1", "盯着构建", AddOptions{})
	h.reply = func(r Request) (string, error) {
		out, err := h.s.ExecScheduleNext("s1", mustJSON(`{"seconds":600,"reason":"构建还在跑"}`))
		if err != nil || !strings.Contains(out, "10 分钟") {
			t.Errorf("schedule_next 结果 %q %v", out, err)
		}
		return "还在构建", nil
	}
	h.tick()
	got := h.task(task.ID)
	if !got.NextRun.Equal(t0.Add(10*time.Minute)) || got.NextReason != "构建还在跑" {
		t.Fatalf("应按她定的 10 分钟：%+v", got)
	}
}

func TestSelfPacedDefaultsToOneHour(t *testing.T) {
	h := newHarness(t, nil)
	task, _ := h.s.Add("s1", "盯着构建", AddOptions{})
	h.tick() // 她没调用 schedule_next
	got := h.task(task.ID)
	if !got.NextRun.Equal(t0.Add(time.Hour)) {
		t.Fatalf("没调用应 1 小时后再醒，得到 %v", got.NextRun)
	}
	// 上一轮定过的理由不残留
	h.reply = func(Request) (string, error) {
		_ = h.s.ScheduleNext("s1", 120, "紧一点")
		return "x", nil
	}
	h.clock.Add(time.Hour)
	h.tick()
	h.reply = nil
	h.clock.Add(2 * time.Minute)
	h.tick()
	if got := h.task(task.ID); got.NextReason != "" || !got.NextRun.Equal(t0.Add(time.Hour+2*time.Minute+time.Hour)) {
		t.Fatalf("理由应清掉并回到默认 1 小时：%+v", got)
	}
}

func TestScheduleNextRangeAndScope(t *testing.T) {
	h := newHarness(t, nil)
	fixed, _ := h.s.Add("fixed", "固定", AddOptions{Interval: time.Hour})
	_, _ = h.s.Add("auto", "自定", AddOptions{})
	// 两个任务的回调在各自的协程里同时跑，写 errs 要加锁（2026-11-11 合并队列里撞上过 concurrent map writes）
	var errs = map[string]error{}
	var mu sync.Mutex
	h.reply = func(r Request) (string, error) {
		sess := r.Task.Session
		mu.Lock()
		defer mu.Unlock()
		if sess == "auto" {
			for i, sec := range []int{0, 59, 86401, -5} {
				errs[sess+string(rune('a'+i))] = h.s.ScheduleNext(sess, sec, "越界")
			}
			// 边界值本身合法
			if err := h.s.ScheduleNext(sess, 60, "下限"); err != nil {
				t.Errorf("60 秒应合法：%v", err)
			}
			if err := h.s.ScheduleNext(sess, 86400, "上限"); err != nil {
				t.Errorf("86400 秒应合法：%v", err)
			}
		} else {
			errs["fixed"] = h.s.ScheduleNext(sess, 600, "不该有效")
		}
		return "x", nil
	}
	h.tick()
	for k, err := range errs {
		if err == nil {
			t.Errorf("%s 应该报错", k)
		}
	}
	if len(errs) != 5 {
		t.Fatalf("预期 5 个错误，得到 %d", len(errs))
	}
	if !strings.Contains(errs["fixed"].Error(), "固定") {
		t.Errorf("固定间隔任务的错误说明不清：%v", errs["fixed"])
	}
	if got := h.task(fixed.ID).NextRun; !got.Equal(t0.Add(time.Hour)) {
		t.Errorf("固定任务不该被改：%v", got)
	}
	// 不在一轮里调用
	if err := h.s.ScheduleNext("auto", 600, ""); err == nil {
		t.Error("不在定期任务的一轮里应报错")
	}
	// 工具入口：小数、非数字、缺失
	for _, raw := range []string{`{"seconds":59}`, `{"seconds":90.5}`, `{"seconds":"600"}`, `{}`, `[]`} {
		if _, err := h.s.ExecScheduleNext("auto", mustJSON(raw)); err == nil {
			t.Errorf("%s 应报错", raw)
		}
	}
}

func TestBusySessionPostponesAndOthersRun(t *testing.T) {
	h := newHarness(t, nil)
	block := make(chan struct{})
	started := make(chan struct{}, 4)
	h.reply = func(r Request) (string, error) {
		if r.Task.Session == "s1" {
			started <- struct{}{}
			<-block
		}
		return "ok", nil
	}
	a, _ := h.s.Add("s1", "慢任务", AddOptions{Interval: time.Minute})
	b, _ := h.s.Add("s1", "同会话的另一个", AddOptions{Interval: time.Minute})
	c, _ := h.s.Add("s2", "别的会话", AddOptions{Interval: time.Minute})

	h.s.Tick(context.Background())
	<-started
	// 同一次检查里：s1 只有一个在跑，另一个顺延；s2 照跑
	deadline := time.Now().Add(5 * time.Second)
	for h.task(c.ID).Runs == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.task(c.ID).Runs != 1 {
		t.Fatal("别的会话不该被拖住")
	}
	if got := h.task(b.ID); got.Runs != 0 || !got.NextRun.Equal(t0.Add(time.Minute)) {
		t.Fatalf("同会话第二个应顺延 1 分钟：%+v", got)
	}
	// 第一轮还没结束，再到点：继续顺延，不叠着跑
	h.clock.Add(time.Minute)
	h.s.Tick(context.Background())
	if got := h.task(b.ID); got.Runs != 0 {
		t.Fatalf("会话忙时 b 不该跑：%+v", got)
	}
	close(block)
	h.s.Wait()
	if h.task(a.ID).Runs != 1 {
		t.Fatalf("a 应跑完一次")
	}
	// 结束后到点就能跑了
	h.clock.Add(2 * time.Minute)
	h.tick()
	if h.task(b.ID).Runs == 0 && h.task(a.ID).Runs < 2 {
		t.Fatal("会话空了以后应该继续跑")
	}
}

func TestPostponeWhenHostBusyOrGoalActive(t *testing.T) {
	for _, mode := range []string{"busy", "goal"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, nil)
			on := true
			if mode == "busy" {
				h.busy = func(string) bool { return on }
			} else {
				h.goal = func(s string) bool { return on && s == "s1" }
			}
			task, _ := h.s.Add("s1", "x", AddOptions{Interval: 10 * time.Minute})
			h.tick()
			if h.runCount() != 0 {
				t.Fatal("忙时不该跑")
			}
			got := h.task(task.ID)
			if got.Runs != 0 || !got.NextRun.Equal(t0.Add(time.Minute)) {
				t.Fatalf("应顺延 1 分钟且不计次数：%+v", got)
			}
			h.clock.Add(time.Minute)
			h.tick()
			if h.runCount() != 0 {
				t.Fatal("仍忙时继续顺延")
			}
			on = false
			h.clock.Add(time.Minute)
			h.tick()
			if h.runCount() != 1 {
				t.Fatal("不忙了应该跑")
			}
		})
	}
}

func TestMissedRunsNotReplayedAndReported(t *testing.T) {
	h := newHarness(t, nil)
	task, _ := h.s.Add("s1", "x", AddOptions{Interval: 10 * time.Minute, Forever: true})
	h.tick() // 12:00 跑一次，下次 12:10
	// 程序关了一个半小时后重开：12:10、12:20 ... 12:90(13:30) 共错过 ...
	h.clock.Add(90 * time.Minute)
	h.open()
	h.tick()
	if h.runCount() != 2 {
		t.Fatalf("启动后只该补跑一次，runs=%d", h.runCount())
	}
	// NextRun 是 12:10，现在 13:30：(80 分钟)/10 = 8 个间隔，除掉这次要跑的是错过 8 次
	got := h.task(task.ID)
	if got.History[1].Missed != 8 || !strings.Contains(got.LastNote, "错过了 8 次") {
		t.Fatalf("应写明错过 8 次：%+v", got.History)
	}
	if !strings.Contains(h.runs[1].Input, "错过了 8 次") {
		t.Fatalf("输入里也要写：%q", h.runs[1].Input)
	}
	if !got.NextRun.After(h.clock.Now()) || got.NextRun.Sub(h.clock.Now()) > 10*time.Minute {
		t.Fatalf("补完后下次应在 10 分钟内：%v", got.NextRun)
	}
	if got.PendingMissed != 0 {
		t.Fatal("错过的次数写完要清零")
	}
	h.tick()
	if h.runCount() != 2 {
		t.Fatal("同一时刻不该再跑")
	}
	// 没错过一整个间隔：不写"错过"
	h.clock.Add(11 * time.Minute)
	h.open()
	h.tick()
	if strings.Contains(h.task(task.ID).LastNote, "错过") {
		t.Fatalf("晚了不到一个间隔不算错过：%q", h.task(task.ID).LastNote)
	}
}

func TestThreeConsecutiveErrorsStop(t *testing.T) {
	h := newHarness(t, nil)
	task, _ := h.s.Add("s1", "x", AddOptions{Interval: time.Minute})
	h.reply = func(Request) (string, error) { return "", errors.New("额度用完了") }
	for i := 0; i < 2; i++ {
		h.tick()
		h.clock.Add(time.Minute)
	}
	got := h.task(task.ID)
	if got.Status != StatusRunning || got.ConsecErrors != 2 || got.LastResult != ResultError {
		t.Fatalf("两次出错还该继续：%+v", got)
	}
	h.tick()
	got = h.task(task.ID)
	if got.Status != StatusStopped || !strings.Contains(got.StopReason, "额度用完了") || !strings.Contains(got.StopReason, "3") {
		t.Fatalf("三次出错应停并写明原因：%+v", got)
	}
	h.clock.Add(time.Hour)
	before := h.runCount()
	h.tick()
	if h.runCount() != before {
		t.Fatal("停了就不该再跑")
	}
	if last := h.reps[len(h.reps)-1]; last.Kind != ResultError || last.Err == nil {
		t.Fatalf("宿主应收到出错报告：%+v", last)
	}
}

func TestSuccessResetsErrorCountAndPanicCounts(t *testing.T) {
	h := newHarness(t, nil)
	task, _ := h.s.Add("s1", "x", AddOptions{Interval: time.Minute})
	seq := []string{"err", "err", "ok", "err", "panic", "err"}
	i := 0
	h.reply = func(Request) (string, error) {
		k := seq[i]
		i++
		switch k {
		case "err":
			return "", errors.New("坏了")
		case "panic":
			panic("炸了")
		}
		return "好", nil
	}
	for j := 0; j < 5; j++ {
		h.tick()
		h.clock.Add(time.Minute)
	}
	got := h.task(task.ID)
	if got.Status != StatusRunning || got.ConsecErrors != 2 {
		t.Fatalf("成功过一次后计数应重来，再 err、panic 才是 2：%+v", got)
	}
	if !strings.Contains(got.LastNote, "炸了") {
		t.Fatalf("panic 要当出错记下原因：%q", got.LastNote)
	}
	h.tick()
	if h.task(task.ID).Status != StatusStopped {
		t.Fatal("连着第 3 次应停")
	}
}

func TestExpiryStops(t *testing.T) {
	h := newHarness(t, nil)
	week, _ := h.s.Add("s1", "默认 7 天", AddOptions{Interval: 24 * time.Hour})
	forever, _ := h.s.Add("s2", "不停", AddOptions{Interval: 24 * time.Hour, Forever: true})
	if got := h.task(week.ID).Expires; !got.Equal(t0.Add(DefaultTTL)) {
		t.Fatalf("默认 7 天：%v", got)
	}
	if !h.task(forever.ID).Expires.IsZero() {
		t.Fatal("Forever 不该有到期时间")
	}
	h.tick()
	h.clock.Add(DefaultTTL - time.Second)
	h.tick()
	if h.task(week.ID).Status != StatusRunning {
		t.Fatal("没到期不该停")
	}
	h.clock.Add(time.Second)
	n := h.runCount()
	h.tick()
	got := h.task(week.ID)
	if got.Status != StatusStopped || got.StopReason != "到期" {
		t.Fatalf("到期应停：%+v", got)
	}
	if h.task(forever.ID).Status != StatusRunning {
		t.Fatal("不停的任务不受影响")
	}
	if h.runCount() != n+1 { // 只有 forever 的那个又跑了一次
		t.Fatalf("到期的任务不该再跑：%d → %d", n, h.runCount())
	}
	// 恢复已到期的任务，重新给 7 天
	if _, err := h.s.Resume("s1", week.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.task(week.ID); got.Status != StatusRunning || !got.Expires.Equal(h.clock.Now().Add(DefaultTTL)) {
		t.Fatalf("恢复后应重新计 7 天：%+v", got)
	}
}

func TestQuietTokenClassification(t *testing.T) {
	cases := []struct {
		in   string
		want ResultKind
	}{
		{"[无事]", ResultQuiet},
		{"  \n[无事]\t\n", ResultQuiet},
		{"　[无事]　", ResultQuiet},
		{"[无事] 但是天气变了", ResultContent},
		{"今天 [无事] 发生", ResultContent},
		{"[无事]\n[无事]", ResultContent},
		{"【无事】", ResultContent},
		{"无事", ResultContent},
		{"[ 无事 ]", ResultContent},
		{"天气不错", ResultContent},
	}
	for _, c := range cases {
		if got, _ := Classify(c.in); got != c.want {
			t.Errorf("Classify(%q)=%v 想要 %v", c.in, got, c.want)
		}
	}
	if k, note := Classify("   "); k != ResultQuiet || note == "" {
		t.Errorf("空回答应按无事记并写原因：%v %q", k, note)
	}

	// 走完整流程：无事不交文字、历史里记一笔
	h := newHarness(t, nil)
	task, _ := h.s.Add("s1", "x", AddOptions{Interval: time.Minute})
	h.reply = func(Request) (string, error) { return "  [无事]\n", nil }
	h.tick()
	rep := h.reps[0]
	if rep.Kind != ResultQuiet || rep.Text != "" {
		t.Fatalf("无事不该带文字：%+v", rep)
	}
	got := h.task(task.ID)
	if got.LastResult != ResultQuiet || len(got.History) != 1 || got.History[0].Result != ResultQuiet || got.Runs != 1 {
		t.Fatalf("历史里要记一笔：%+v", got)
	}
	h.reply = func(Request) (string, error) { return "有新消息", nil }
	h.clock.Add(time.Minute)
	h.tick()
	if rep := h.reps[1]; rep.Kind != ResultContent || rep.Text != "有新消息" {
		t.Fatalf("有内容要原样交给宿主：%+v", rep)
	}
}

func TestPersistenceAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "loop.json")
	h := newHarness(t, FileStore{Path: path})
	a, _ := h.s.Add("s1", "固定", AddOptions{Interval: time.Hour})
	b, _ := h.s.Add("s1", "自定", AddOptions{})
	h.tick()
	_, _ = h.s.Pause("s1", b.ID)
	before := h.s.List("")

	h2 := newHarness(t, FileStore{Path: path})
	h2.clock = h.clock
	after := h2.s.List("")
	if len(after) != 2 {
		t.Fatalf("重启后应读回 2 个任务，得到 %d", len(after))
	}
	for i := range after {
		if after[i].ID != before[i].ID || after[i].Prompt != before[i].Prompt || after[i].Runs != before[i].Runs ||
			after[i].Status != before[i].Status || !after[i].NextRun.Equal(before[i].NextRun) || len(after[i].History) != len(before[i].History) {
			t.Fatalf("读回不一致：\n%+v\n%+v", before[i], after[i])
		}
	}
	// 编号接着往后，不重用
	c, err := h2.s.Add("s1", "新的", AddOptions{})
	if err != nil || c.ID <= b.ID || c.ID <= a.ID {
		t.Fatalf("编号应接着排：%v %v", c.ID, err)
	}
	// 暂停的任务重启后仍是暂停，不会跑
	h2.clock.Add(3 * time.Hour)
	h2.tick()
	if h2.task(b.ID).Runs != before[1].Runs || h2.task(b.ID).Status != StatusPaused {
		t.Fatalf("暂停的不该跑：%+v", h2.task(b.ID))
	}
}

func TestFileStoreCorruptAndNewer(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := writeFile(bad, "{不是 json"); err != nil {
		t.Fatal(err)
	}
	if _, err := (FileStore{Path: bad}).Load(); err == nil {
		t.Fatal("坏文件应报错，不能当成空表覆盖掉")
	}
	newer := filepath.Join(dir, "new.json")
	_ = writeFile(newer, `{"version":99,"nextId":1,"tasks":[]}`)
	if _, err := (FileStore{Path: newer}).Load(); err == nil {
		t.Fatal("更新版本的文件应拒绝读")
	}
	st, err := (FileStore{Path: filepath.Join(dir, "none.json")}).Load()
	if err != nil || st.NextID != 1 {
		t.Fatalf("文件不存在应是空表：%+v %v", st, err)
	}
}

func TestStopPauseResumeByIDAndSession(t *testing.T) {
	h := newHarness(t, nil)
	a, _ := h.s.Add("s1", "a", AddOptions{Interval: time.Hour})
	b, _ := h.s.Add("s1", "b", AddOptions{Interval: time.Hour})
	c, _ := h.s.Add("s2", "c", AddOptions{Interval: time.Hour})

	if _, err := h.s.Stop("s1", c.ID); err == nil {
		t.Fatal("不能停别的会话的任务")
	}
	if n, _ := h.s.Pause("s1", a.ID); n != 1 || h.task(a.ID).Status != StatusPaused || h.task(b.ID).Status != StatusRunning {
		t.Fatal("按编号暂停")
	}
	h.tick()
	if h.task(a.ID).Runs != 0 || h.task(b.ID).Runs != 1 || h.task(c.ID).Runs != 1 {
		t.Fatal("暂停的不跑")
	}
	h.clock.Add(time.Hour)
	if n, _ := h.s.Resume("s1", 0); n != 1 || h.task(a.ID).Status != StatusRunning {
		t.Fatal("不带编号恢复会话里所有暂停的")
	}
	if n, _ := h.s.Stop("s1", 0); n != 2 || h.task(c.ID).Status != StatusRunning {
		t.Fatal("不带编号停这个会话所有的，不动别的会话")
	}
	if n, _ := h.s.Stop("s1", 0); n != 0 {
		t.Fatal("没有可停的应返回 0")
	}
	// 在跑的一轮期间被停：这一轮结束后不再排下一次
	block := make(chan struct{})
	started := make(chan struct{})
	h.reply = func(Request) (string, error) { close(started); <-block; return "x", nil }
	d, _ := h.s.Add("s3", "d", AddOptions{Interval: time.Minute})
	h.s.Tick(context.Background())
	<-started
	_, _ = h.s.Stop("s3", d.ID)
	close(block)
	h.s.Wait()
	if got := h.task(d.ID); got.Status != StatusStopped || got.Runs != 1 {
		t.Fatalf("跑的时候被停：%+v", got)
	}
}

func TestContextCancelDoesNotCountAsError(t *testing.T) {
	h := newHarness(t, nil)
	task, _ := h.s.Add("s1", "x", AddOptions{Interval: time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	h.reply = func(Request) (string, error) { cancel(); return "", context.Canceled }
	h.s.Tick(ctx)
	h.s.Wait()
	got := h.task(task.ID)
	if got.Runs != 0 || got.ConsecErrors != 0 {
		t.Fatalf("程序退出打断的一轮不该算出错：%+v", got)
	}
}

func TestParseCommand(t *testing.T) {
	type c = Command
	ok := []struct {
		in   string
		want c
	}{
		{"/loop", c{Kind: CmdList}},
		{"  /loop  ", c{Kind: CmdList}},
		{"/LOOP", c{Kind: CmdList}},
		{"/loop 30m 报一下天气", c{Kind: CmdAdd, Interval: 30 * time.Minute, Prompt: "报一下天气"}},
		{"/loop 1m x", c{Kind: CmdAdd, Interval: time.Minute, Prompt: "x"}},
		{"/loop 2h 看看   构建", c{Kind: CmdAdd, Interval: 2 * time.Hour, Prompt: "看看   构建"}},
		{"/loop 1d 每日总结", c{Kind: CmdAdd, Interval: 24 * time.Hour, Prompt: "每日总结"}},
		{"/loop 90m x", c{Kind: CmdAdd, Interval: 90 * time.Minute, Prompt: "x"}},
		{"/loop 30M x", c{Kind: CmdAdd, Interval: 30 * time.Minute, Prompt: "x"}},
		{"/loop 报一下天气", c{Kind: CmdAdd, Prompt: "报一下天气"}},
		{"/loop 30m 第一行\n第二行", c{Kind: CmdAdd, Interval: 30 * time.Minute, Prompt: "第一行\n第二行"}},
		{"/loop\t查一下", c{Kind: CmdAdd, Prompt: "查一下"}},
		{"/loop stop", c{Kind: CmdStop}},
		{"/loop stop 3", c{Kind: CmdStop, ID: 3}},
		{"/loop stop #3", c{Kind: CmdStop, ID: 3}},
		{"/loop pause 12", c{Kind: CmdPause, ID: 12}},
		{"/loop Resume", c{Kind: CmdResume}},
		{"/loop resume #2", c{Kind: CmdResume, ID: 2}},
		{"/loop stop the server", c{Kind: CmdAdd, Prompt: "stop the server"}},
		{"/loop stopwatch", c{Kind: CmdAdd, Prompt: "stopwatch"}},
		{"/loop 3 个文件有没有改", c{Kind: CmdAdd, Prompt: "3 个文件有没有改"}},
		{"/loop -5m x", c{Kind: CmdAdd, Prompt: "-5m x"}},
		{"/loop 30 m x", c{Kind: CmdAdd, Prompt: "30 m x"}},
	}
	for _, k := range ok {
		got, isLoop, err := ParseCommand(k.in)
		if !isLoop || err != nil || got != k.want {
			t.Errorf("%q → %+v %v %v，想要 %+v", k.in, got, isLoop, err, k.want)
		}
	}
	notLoop := []string{"", "/loops", "/looper 1m x", "loop 30m x", "/goal x", "hello /loop", "/lo", "/循环 1m x"}
	for _, in := range notLoop {
		if _, isLoop, err := ParseCommand(in); isLoop || err != nil {
			t.Errorf("%q 不是 /loop 命令：%v %v", in, isLoop, err)
		}
	}
	bad := []string{
		"/loop 30m", "/loop 30m   ", "/loop 30s x", "/loop 0m x", "/loop 0h x", "/loop 1.5h x",
		"/loop 5x x", "/loop 99999d x", "/loop stop 1 2", "/loop 30mm x",
	}
	for _, in := range bad {
		_, isLoop, err := ParseCommand(in)
		if !isLoop || err == nil {
			t.Errorf("%q 应是写错了的 /loop：isLoop=%v err=%v", in, isLoop, err)
		}
	}
}

func TestApplyAndLine(t *testing.T) {
	h := newHarness(t, nil)
	reply := func(text string) string {
		cmd, ok, err := ParseCommand(text)
		if !ok || err != nil {
			t.Fatalf("%q 解析失败：%v", text, err)
		}
		out, err := h.s.Apply("s1", cmd)
		if err != nil {
			return "ERR:" + err.Error()
		}
		return out
	}
	if out := reply("/loop"); !strings.Contains(out, "还没有定期任务") {
		t.Fatalf("空列表：%q", out)
	}
	if out := reply("/loop 30m 报一下天气"); !strings.Contains(out, "#1 每 30 分钟 · 报一下天气 · 已跑 0 次") {
		t.Fatalf("建立回复：%q", out)
	}
	reply("/loop 看看有没有新邮件")
	h.reply = func(Request) (string, error) { return "晴", nil }
	h.tick()
	h.clock.Add(30 * time.Minute)
	h.tick()
	list := reply("/loop")
	lines := strings.Split(strings.TrimSuffix(list, "\n"+Usage), "\n")
	if len(lines) != 2 {
		t.Fatalf("列表应两行：%q", list)
	}
	if !strings.HasPrefix(lines[0], "#1 每 30 分钟 · 报一下天气 · 已跑 2 次 · 下次 13:00") {
		t.Fatalf("第一行：%q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "#2 自定节奏 · 看看有没有新邮件 · 已跑 0 次 · 下次 12:31") { // 同会话的第一轮只跑了 #1，#2 顺延
		t.Fatalf("第二行：%q", lines[1])
	}
	if !strings.Contains(lines[0], "11-15 12:00 到期") {
		t.Fatalf("到期时间应带日期：%q", lines[0])
	}
	if out := reply("/loop pause 1"); out != "已暂停 1 个定期任务" {
		t.Fatalf("暂停：%q", out)
	}
	if out := reply("/loop"); !strings.Contains(out, "#1 每 30 分钟 · 报一下天气 · 已跑 2 次 · 已暂停") {
		t.Fatalf("暂停后的列表：%q", out)
	}
	if out := reply("/loop stop 9"); !strings.HasPrefix(out, "ERR:") {
		t.Fatalf("不存在的编号应报错：%q", out)
	}
	if out := reply("/loop stop"); out != "已停止 2 个定期任务" { // 暂停的和在跑的都算没停的
		t.Fatalf("停止：%q", out)
	}
	if out := reply("/loop stop"); !strings.HasPrefix(out, "ERR:") {
		t.Fatalf("没有可停的应说清楚：%q", out)
	}
	if out := reply("/loop"); !strings.Contains(out, "已停止：手动停止") {
		t.Fatalf("停止后的列表：%q", out)
	}
}

func TestLineClipAndDuration(t *testing.T) {
	long := strings.Repeat("长", 30)
	line := Task{ID: 3, Prompt: long, Interval: 2 * time.Hour, Status: StatusRunning, NextRun: t0.Add(24 * time.Hour), Expires: time.Time{}}.Line(t0)
	if !strings.Contains(line, strings.Repeat("长", 20)+"…") || !strings.Contains(line, "每 2 小时") || !strings.Contains(line, "11-09 12:00") || !strings.Contains(line, "不自动停") {
		t.Fatalf("%q", line)
	}
	for d, want := range map[time.Duration]string{time.Minute: "1 分钟", 90 * time.Minute: "90 分钟", 24 * time.Hour: "1 天", 48 * time.Hour: "2 天", 25 * time.Hour: "25 小时", 100 * time.Minute: "100 分钟"} {
		if got := DurationText(d); got != want {
			t.Errorf("%v → %q 想要 %q", d, got, want)
		}
	}
}

func TestDeclarationIsValidJSONSchema(t *testing.T) {
	d := ScheduleNextDecl
	if d.Name != "schedule_next" || d.Description == "" {
		t.Fatal("声明不全")
	}
	v, err := jsjson.Parse(d.Parameters)
	if err != nil || v.Kind != jsjson.Object || v.Obj["properties"].Obj["seconds"].Obj["minimum"].Num != MinNextSeconds ||
		v.Obj["properties"].Obj["seconds"].Obj["maximum"].Num != MaxNextSeconds {
		t.Fatalf("参数 schema 不对：%v %v", v.Kind, err)
	}
}

func TestNewRequiresStoreAndRun(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("缺 Store 和 Run 应报错")
	}
	if _, err := New(Options{Store: &MemoryStore{}}); err == nil {
		t.Fatal("缺 Run 应报错")
	}
}

func mustJSON(s string) jsjson.Value {
	v, err := jsjson.Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}

func TestParseEdit(t *testing.T) {
	cases := []struct {
		in   string
		want Command
		err  bool
	}{
		{"/loop edit 3 2h", Command{Kind: CmdEdit, ID: 3, Interval: 2 * time.Hour}, false},
		{"/loop edit #3 改成新任务", Command{Kind: CmdEdit, ID: 3, Prompt: "改成新任务"}, false},
		{"/loop edit 3 15m 新任务 文字", Command{Kind: CmdEdit, ID: 3, Interval: 15 * time.Minute, Prompt: "新任务 文字"}, false},
		{"/loop edit", Command{}, true},
		{"/loop edit 3", Command{}, true},
		{"/loop edit x 2h", Command{}, true},
		{"/loop edit 3 0m 任务", Command{}, true},
		{"/loop edit 3 5x 任务", Command{}, true},
	}
	for _, c := range cases {
		got, ok, err := ParseCommand(c.in)
		if !ok || (err != nil) != c.err || (!c.err && got != c.want) {
			t.Errorf("%q → %+v %v %v", c.in, got, ok, err)
		}
	}
}

func TestEditIntervalAndPrompt(t *testing.T) {
	h := newHarness(t, nil)
	if _, err := h.s.Add("s1", "旧任务", AddOptions{Interval: time.Hour}); err != nil {
		t.Fatal(err)
	}
	h.tick() // 12:00 跑第一次，下次 13:00
	h.clock.Add(10 * time.Minute)
	// 改间隔：从上次开始跑的 12:00 起按 30 分钟算 → 12:30
	task, err := h.s.Edit("s1", 1, EditOptions{Interval: 30 * time.Minute})
	if err != nil || task.Interval != 30*time.Minute || !task.NextRun.Equal(t0.Add(30*time.Minute)) || task.Prompt != "旧任务" {
		t.Fatalf("%+v %v", task, err)
	}
	// 改成已经过点的间隔：马上跑
	task, _ = h.s.Edit("s1", 1, EditOptions{Interval: time.Minute, Prompt: " 新任务 "})
	if task.Prompt != "新任务" || !task.NextRun.Equal(h.clock.Now()) {
		t.Fatalf("%+v", task)
	}
	h.tick()
	if h.runCount() != 2 || !strings.HasPrefix(h.runs[1].Input, "新任务") {
		t.Fatalf("改后应按新任务再跑：%d", h.runCount())
	}
	// 错误：什么都没改、间隔越界、别的会话、不存在
	if _, err := h.s.Edit("s1", 1, EditOptions{}); err == nil {
		t.Fatal("空修改应报错")
	}
	if _, err := h.s.Edit("s1", 1, EditOptions{Interval: time.Second}); err == nil {
		t.Fatal("间隔太短应报错")
	}
	if _, err := h.s.Edit("s2", 1, EditOptions{Prompt: "x"}); err == nil {
		t.Fatal("别的会话不能改")
	}
	if _, err := h.s.Edit("s1", 9, EditOptions{Prompt: "x"}); err == nil {
		t.Fatal("不存在的编号应报错")
	}
}

func TestEditSelfPacedBecomesFixedAndApply(t *testing.T) {
	h := newHarness(t, nil)
	h.s.Add("s1", "自定", AddOptions{})
	cmd, _, _ := ParseCommand("/loop edit 1 1h")
	out, err := h.s.Apply("s1", cmd)
	if err != nil || !strings.HasPrefix(out, "已修改 #1 每 1 小时") {
		t.Fatalf("%q %v", out, err)
	}
	if h.task(1).SelfPaced() {
		t.Fatal("改了间隔应不再自定节奏")
	}
	if list, _ := h.s.Apply("s1", Command{Kind: CmdList}); !strings.Contains(list, "/loop edit <编号>") {
		t.Fatalf("列表里该写 edit 用法：%q", list)
	}
}

func TestInFlight(t *testing.T) {
	h := newHarness(t, nil)
	inRun := false
	h.reply = func(Request) (string, error) { inRun = h.s.InFlight("s1"); return "ok", nil }
	h.s.Add("s1", "任务", AddOptions{Interval: time.Hour})
	if h.s.InFlight("s1") {
		t.Fatal("没跑时不该在途")
	}
	h.tick()
	if !inRun || h.s.InFlight("s1") {
		t.Fatalf("跑的时候在途、跑完不在途：%v", inRun)
	}
}
