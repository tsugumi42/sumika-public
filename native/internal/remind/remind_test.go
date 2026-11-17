package remind

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

// 假时钟加 Tick，不用墙钟等待；名字、内容都是编的。

var testLoc = time.FixedZone("本机", 8*3600)

func at(y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, testLoc)
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

type ringRec struct {
	mu   sync.Mutex
	reqs []RingRequest
	res  func(RingRequest) RingResult
}

func (r *ringRec) ring(_ context.Context, q RingRequest) RingResult {
	r.mu.Lock()
	r.reqs = append(r.reqs, q)
	f := r.res
	r.mu.Unlock()
	if f != nil {
		return f(q)
	}
	return RingResult{Note: "她说的"}
}

func (r *ringRec) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.reqs) }

func newSched(t *testing.T, store Store, clock *fakeClock, rec *ringRec) *Scheduler {
	t.Helper()
	s, err := New(Options{Store: store, Ring: rec.ring, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var chat = Actor{Source: SourceChat, Character: "r1"}

func tick(s *Scheduler) {
	s.Tick(context.Background())
	s.Wait()
}

func TestNext(t *testing.T) {
	for _, c := range []struct {
		name          string
		rep           Repeat
		anchor, after time.Time
		want          time.Time
	}{
		{"每天还没到第一次", Repeat{Unit: UnitDay}, at(2026, 11, 10, 9, 0), at(2026, 11, 9, 12, 0), at(2026, 11, 10, 9, 0)},
		{"每天正好在点上取下一天", Repeat{Unit: UnitDay}, at(2026, 11, 10, 9, 0), at(2026, 11, 10, 9, 0), at(2026, 11, 11, 9, 0)},
		{"每天过了点", Repeat{Unit: UnitDay}, at(2026, 11, 10, 9, 0), at(2026, 11, 12, 10, 0), at(2026, 11, 13, 9, 0)},
		{"隔一天", Repeat{Unit: UnitDay, Every: 2}, at(2026, 11, 10, 9, 0), at(2026, 11, 11, 9, 0), at(2026, 11, 12, 9, 0)},
		{"每两小时", Repeat{Unit: UnitHour, Every: 2}, at(2026, 11, 10, 9, 0), at(2026, 11, 10, 12, 30), at(2026, 11, 10, 13, 0)},
		{"每 90 分钟", Repeat{Unit: UnitMinute, Every: 90}, at(2026, 11, 10, 9, 0), at(2026, 11, 10, 10, 30), at(2026, 11, 10, 12, 0)},
		{"每周同一天", Repeat{Unit: UnitWeek}, at(2026, 11, 10, 9, 0), at(2026, 11, 10, 9, 0), at(2026, 11, 17, 9, 0)},
		{"工作日跳过周末", Repeat{Unit: UnitWeek, Weekdays: []int{1, 2, 3, 4, 5}}, at(2026, 11, 9, 9, 0), at(2026, 11, 13, 9, 0), at(2026, 11, 16, 9, 0)},
		{"每周一三五", Repeat{Unit: UnitWeek, Weekdays: []int{1, 3, 5}}, at(2026, 11, 9, 9, 0), at(2026, 11, 9, 9, 0), at(2026, 11, 11, 9, 0)},
		{"隔周一", Repeat{Unit: UnitWeek, Every: 2, Weekdays: []int{1}}, at(2026, 11, 9, 9, 0), at(2026, 11, 9, 10, 0), at(2026, 11, 23, 9, 0)},
		{"第一次不早于基准", Repeat{Unit: UnitWeek, Weekdays: []int{1}}, at(2026, 11, 11, 9, 0), at(2026, 11, 10, 0, 0), at(2026, 11, 16, 9, 0)},
		{"每周日", Repeat{Unit: UnitWeek, Weekdays: []int{7}}, at(2026, 11, 9, 9, 0), at(2026, 11, 15, 9, 0), at(2026, 11, 22, 9, 0)},
		{"每月", Repeat{Unit: UnitMonth}, at(2026, 11, 10, 9, 0), at(2026, 11, 10, 9, 0), at(2026, 12, 10, 9, 0)},
		{"每月跨年", Repeat{Unit: UnitMonth}, at(2026, 11, 10, 9, 0), at(2026, 12, 10, 9, 0), at(2027, 1, 10, 9, 0)},
		{"31 号落到小月月底", Repeat{Unit: UnitMonth}, at(2027, 1, 31, 9, 0), at(2027, 1, 31, 9, 0), at(2027, 2, 28, 9, 0)},
		{"月底以后回到 31 号", Repeat{Unit: UnitMonth}, at(2027, 1, 31, 9, 0), at(2027, 2, 28, 9, 0), at(2027, 3, 31, 9, 0)},
		{"每两个月", Repeat{Unit: UnitMonth, Every: 2}, at(2026, 11, 10, 9, 0), at(2026, 11, 11, 9, 0), at(2027, 1, 10, 9, 0)},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Next(c.rep, c.anchor, c.after); !got.Equal(c.want) {
				t.Fatalf("Next = %s，要 %s", got, c.want)
			}
		})
	}
	if !Next(Repeat{}, at(2026, 11, 10, 9, 0), at(2026, 11, 9, 0, 0)).IsZero() {
		t.Fatal("一次性没有下一次")
	}
}

func TestRepeatTextAndGap(t *testing.T) {
	for _, c := range []struct {
		r    Repeat
		text string
		gap  time.Duration
	}{
		{Repeat{Unit: UnitDay}, "每天", 24 * time.Hour},
		{Repeat{Unit: UnitHour, Every: 2}, "每 2 小时", 2 * time.Hour},
		{Repeat{Unit: UnitMinute, Every: 30}, "每 30 分钟", 30 * time.Minute},
		{Repeat{Unit: UnitWeek, Weekdays: []int{1, 2, 3, 4, 5}}, "工作日", 24 * time.Hour},
		{Repeat{Unit: UnitWeek, Weekdays: []int{1, 3, 5}}, "每周一三五", 48 * time.Hour},
		{Repeat{Unit: UnitWeek}, "每周", 7 * 24 * time.Hour},
		{Repeat{Unit: UnitMonth}, "每月", 28 * 24 * time.Hour},
	} {
		if c.r.Text() != c.text || c.r.Gap() != c.gap {
			t.Errorf("%+v：%s %s", c.r, c.r.Text(), c.r.Gap())
		}
	}
}

func TestParseTime(t *testing.T) {
	now := at(2026, 11, 9, 12, 0)
	for _, s := range []string{"2026-11-10 09:00", "2026-11-10T09:00", "2026-11-10 09:00:00", "2026-11-10T01:00:00Z", "2026-11-10T09:00:00+08:00"} {
		got, err := ParseTime(s, now)
		if err != nil || !got.Equal(at(2026, 11, 10, 9, 0)) {
			t.Errorf("%s：%v %v", s, got, err)
		}
	}
	for _, s := range []string{"", "明天九点", "2026-11-10", "09:00", "2026-13-10 09:00", "2026-11-31 09:00"} {
		_, err := ParseTime(s, now)
		if err == nil || !strings.Contains(err.Error(), "现在是 2026-11-09") {
			t.Errorf("%q 应当报错并写现在几点：%v", s, err)
		}
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	s := newSched(t, &MemoryStore{}, clock, &ringRec{})
	future := at(2026, 11, 10, 9, 0)
	for _, c := range []struct {
		name string
		d    Draft
		want string
	}{
		{"空内容", Draft{Text: "  \n", At: future}, "空的"},
		{"超长", Draft{Text: strings.Repeat("字", TextMax+1), At: future}, "太长"},
		{"控制字符", Draft{Text: "开会\x00", At: future}, "控制字符"},
		{"过去", Draft{Text: "开会", At: at(2026, 11, 9, 11, 0)}, "已经过了"},
		{"正好现在", Draft{Text: "开会", At: clock.Now()}, "已经过了"},
		{"太远", Draft{Text: "开会", At: at(2040, 1, 1, 0, 0)}, "太远"},
		{"不认识的单位", Draft{Text: "开会", At: future, Repeat: Repeat{Unit: "fortnight"}}, "repeat.unit"},
		{"星期超范围", Draft{Text: "开会", At: future, Repeat: Repeat{Unit: UnitWeek, Weekdays: []int{8}}}, "weekdays"},
		{"星期 0", Draft{Text: "开会", At: future, Repeat: Repeat{Unit: UnitWeek, Weekdays: []int{0}}}, "weekdays"},
		{"非 week 写星期", Draft{Text: "开会", At: future, Repeat: Repeat{Unit: UnitDay, Weekdays: []int{1}}}, "weekdays"},
		{"每负数", Draft{Text: "开会", At: future, Repeat: Repeat{Unit: UnitDay, Every: -1}}, "every"},
		{"不认识的目标", Draft{Text: "开会", At: future, Targets: []string{"group"}}, "targets"},
		{"QQ 没开", Draft{Text: "开会", At: future, Targets: []string{TargetQQ}}, "reminders.qq"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := s.Create(chat, c.d); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("要拒并提到 %q：%v", c.want, err)
			}
		})
	}
	if len(s.List("")) != 0 {
		t.Fatal("被拒的都不该留下")
	}
}

func TestCreateIsIdempotentAndCaps(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	s, err := New(Options{Store: &MemoryStore{}, Ring: (&ringRec{}).ring, Now: clock.Now, Limits: func() Limits {
		return Limits{MaxActive: 2, MinInterval: time.Hour, ThreadMaxActive: 1}
	}})
	if err != nil {
		t.Fatal(err)
	}
	future := at(2026, 11, 10, 9, 0)
	a, existed, err := s.Create(chat, Draft{Text: "开会", At: future})
	if err != nil || existed || a.ID != 1 {
		t.Fatalf("%+v %v %v", a, existed, err)
	}
	b, existed, err := s.Create(chat, Draft{Text: "开会", At: future})
	if err != nil || !existed || b.ID != 1 {
		t.Fatalf("一样的应当回原来那条：%+v %v %v", b, existed, err)
	}
	if _, _, err := s.Create(chat, Draft{Text: "交房租", At: future}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(chat, Draft{Text: "喝水", At: future}); err == nil || !strings.Contains(err.Error(), "reminders.maxActive") {
		t.Fatalf("第三条该被挡：%v", err)
	}
	// 主人不受上限管
	if _, _, err := s.Create(Actor{Source: SourceOwner, Character: "r1"}, Draft{Text: "喝水", At: future}); err != nil {
		t.Fatalf("主人建的不受上限：%v", err)
	}
	// 别的角色单独算
	if _, _, err := s.Create(Actor{Source: SourceChat, Character: "r2"}, Draft{Text: "喝水", At: future}); err != nil {
		t.Fatal(err)
	}
	// 结束一条就又能建
	if _, err := s.Finish(chat, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(chat, 3); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(chat, Draft{Text: "喝水", At: future}); err != nil {
		t.Fatalf("结束以后应当能建：%v", err)
	}
}

func TestIntervalFloor(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	s := newSched(t, &MemoryStore{}, clock, &ringRec{})
	future := at(2026, 11, 10, 9, 0)
	_, _, err := s.Create(chat, Draft{Text: "喝水", At: future, Repeat: Repeat{Unit: UnitMinute, Every: 59}})
	if err == nil || !strings.Contains(err.Error(), "reminders.minIntervalMinutes") || !strings.Contains(err.Error(), "主动联系") {
		t.Fatalf("低于下限要明确说：%v", err)
	}
	if _, _, err := s.Create(chat, Draft{Text: "喝水", At: future, Repeat: Repeat{Unit: UnitHour}}); err != nil {
		t.Fatalf("正好 60 分钟放行：%v", err)
	}
	if _, _, err := s.Create(Actor{Source: SourceOwner, Character: "r1"}, Draft{Text: "活动一下", At: future, Repeat: Repeat{Unit: UnitMinute, Every: 5}}); err != nil {
		t.Fatalf("主人可以建更短的：%v", err)
	}
	// 改也查下限
	if _, err := s.Update(chat, 1, Patch{Repeat: &Repeat{Unit: UnitMinute, Every: 10}}); err == nil {
		t.Fatal("改成低于下限要拒")
	}
}

func TestThreadRules(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	s := newSched(t, &MemoryStore{}, clock, &ringRec{})
	thread := Actor{Source: SourceThread, Character: "r1"}
	future := at(2026, 11, 10, 9, 0)
	if _, _, err := s.Create(thread, Draft{Text: "每天叫他", At: future, Repeat: Repeat{Unit: UnitDay}}); err == nil || !strings.Contains(err.Error(), "一次性") {
		t.Fatalf("线里不能建周期：%v", err)
	}
	if _, _, err := s.Create(thread, Draft{Text: "马上叫他", At: clock.Now().Add(10 * time.Minute)}); err == nil || !strings.Contains(err.Error(), "thread_send") {
		t.Fatalf("线里建的不能马上响：%v", err)
	}
	for i, text := range []string{"甲", "乙", "丙"} {
		if _, _, err := s.Create(thread, Draft{Text: text, At: future.Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.Create(thread, Draft{Text: "丁", At: future}); err == nil || !strings.Contains(err.Error(), "reminders.threadMaxActive") {
		t.Fatalf("线里超条数：%v", err)
	}
	owned, _, err := s.Create(chat, Draft{Text: "主人要的", At: future})
	if err != nil {
		t.Fatal(err)
	}
	text := "改了"
	if _, err := s.Update(thread, owned.ID, Patch{Text: &text}); err == nil || !strings.Contains(err.Error(), "线里不能动") {
		t.Fatalf("线里不能改主人那边的：%v", err)
	}
	if _, err := s.Finish(thread, owned.ID, false); err == nil {
		t.Fatal("线里不能取消主人那边的")
	}
	if _, err := s.Delete(thread, owned.ID); err == nil {
		t.Fatal("线里不能删主人那边的")
	}
	if _, err := s.Update(thread, 1, Patch{Repeat: &Repeat{Unit: UnitDay}}); err == nil {
		t.Fatal("线里不能把一次性改成周期")
	}
	soon := clock.Now().Add(time.Minute)
	if _, err := s.Update(thread, 1, Patch{At: &soon}); err == nil {
		t.Fatal("线里改时间也不能绕过最早时间")
	}
	if _, err := s.Finish(thread, 1, false); err != nil {
		t.Fatalf("线里能动线里建的：%v", err)
	}
}

func TestOtherCharacterInvisible(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	s := newSched(t, &MemoryStore{}, clock, &ringRec{})
	r, _, err := s.Create(chat, Draft{Text: "甲的事", At: at(2026, 11, 10, 9, 0)})
	if err != nil {
		t.Fatal(err)
	}
	other := Actor{Source: SourceChat, Character: "r2"}
	text := "改了"
	for name, e := range map[string]error{
		"改":  func() error { _, err := s.Update(other, r.ID, Patch{Text: &text}); return err }(),
		"结束": func() error { _, err := s.Finish(other, r.ID, false); return err }(),
		"删":  func() error { _, err := s.Delete(other, r.ID); return err }(),
	} {
		if !errors.Is(e, ErrNotFound) {
			t.Errorf("%s：别的角色的要回“没有”：%v", name, e)
		}
	}
	if got := s.List("r2"); len(got) != 0 {
		t.Fatalf("别的角色看不到：%+v", got)
	}
}

func TestSaveFailureRollsBack(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	st := &MemoryStore{}
	rec := &ringRec{}
	s := newSched(t, st, clock, rec)
	r, _, err := s.Create(chat, Draft{Text: "开会", At: at(2026, 11, 10, 9, 0)})
	if err != nil {
		t.Fatal(err)
	}
	st.FailSave = errors.New("磁盘满了")
	if _, _, err := s.Create(chat, Draft{Text: "别的", At: at(2026, 11, 10, 9, 0)}); err == nil {
		t.Fatal("存不上要报错")
	}
	text := "改了"
	if _, err := s.Update(chat, r.ID, Patch{Text: &text}); err == nil {
		t.Fatal("存不上要报错")
	}
	if _, err := s.Finish(chat, r.ID, true); err == nil {
		t.Fatal("存不上要报错")
	}
	if _, err := s.Delete(chat, r.ID); err == nil {
		t.Fatal("存不上要报错")
	}
	got := s.List("")
	if len(got) != 1 || got[0].Text != "开会" || got[0].Status != StatusActive {
		t.Fatalf("内存要回滚：%+v", got)
	}
	// 认领写不上盘就不响，盘好了再响
	clock.Set(at(2026, 11, 10, 9, 0))
	tick(s)
	if rec.count() != 0 {
		t.Fatal("认领没落盘不能响")
	}
	st.FailSave = nil
	tick(s)
	if rec.count() != 1 {
		t.Fatal("盘好了应当响")
	}
}

func TestOneShotRingsOnceThenDone(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	rec := &ringRec{}
	s := newSched(t, &MemoryStore{}, clock, rec)
	r, _, err := s.Create(chat, Draft{Text: "开会", At: at(2026, 11, 10, 9, 0)})
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(at(2026, 11, 10, 8, 59))
	tick(s)
	if rec.count() != 0 {
		t.Fatal("没到点不响")
	}
	clock.Set(at(2026, 11, 10, 9, 0))
	tick(s)
	tick(s)
	if rec.count() != 1 {
		t.Fatalf("响 %d 次", rec.count())
	}
	got, _ := s.Get(r.ID)
	if got.Status != StatusDone || got.Rings != 1 || got.Pending != nil || got.EndNote == "" {
		t.Fatalf("%+v", got)
	}
	if q := rec.reqs[0]; !q.Scheduled.Equal(at(2026, 11, 10, 9, 0)) || q.Missed != 0 || q.Resumed {
		t.Fatalf("%+v", q)
	}
}

func TestMissedRecurringRingsOnce(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	rec := &ringRec{}
	s := newSched(t, &MemoryStore{}, clock, rec)
	r, _, err := s.Create(chat, Draft{Text: "吃药", At: at(2026, 11, 10, 9, 0), Repeat: Repeat{Unit: UnitDay}})
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(at(2026, 11, 13, 10, 0)) // 关了三天多
	tick(s)
	tick(s)
	if rec.count() != 1 {
		t.Fatalf("错过多次也只响一次：%d", rec.count())
	}
	if q := rec.reqs[0]; q.Missed != 3 || !q.Scheduled.Equal(at(2026, 11, 10, 9, 0)) {
		t.Fatalf("%+v", q)
	}
	got, _ := s.Get(r.ID)
	if got.Status != StatusActive || !got.NextAt.Equal(at(2026, 11, 14, 9, 0)) {
		t.Fatalf("下一次从现在往后算：%+v", got)
	}
	if note := LateNote(at(2026, 11, 10, 9, 0), clock.Now(), 3); !strings.Contains(note, "错过 4 次") || !strings.Contains(note, "3 天 1 小时") {
		t.Fatalf("说明写错了：%s", note)
	}
}

func TestLateNote(t *testing.T) {
	sched := at(2026, 11, 10, 9, 0)
	if s := LateNote(sched, sched.Add(90*time.Second), 0); s != "" {
		t.Fatalf("差一点不算晚：%s", s)
	}
	s := LateNote(sched, at(2026, 11, 10, 12, 12), 0)
	if !strings.Contains(s, "本该 09:00") || !strings.Contains(s, "3 小时 12 分") {
		t.Fatalf("%s", s)
	}
	if s := LateNote(sched, at(2026, 11, 11, 9, 0), 0); !strings.Contains(s, "11-10 09:00") {
		t.Fatalf("隔天要带日期：%s", s)
	}
}

func TestConcurrentTicksRingOnce(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	release := make(chan struct{})
	rec := &ringRec{res: func(RingRequest) RingResult { <-release; return RingResult{Note: "说了"} }}
	s := newSched(t, &MemoryStore{}, clock, rec)
	if _, _, err := s.Create(chat, Draft{Text: "开会", At: at(2026, 11, 10, 9, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(chat, Draft{Text: "交房租", At: at(2026, 11, 10, 9, 0)}); err != nil {
		t.Fatal(err)
	}
	clock.Set(at(2026, 11, 10, 9, 0))
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() { defer wg.Done(); s.Tick(context.Background()) }()
	}
	wg.Wait()
	close(release)
	s.Wait()
	if rec.count() != 1 {
		t.Fatalf("并发检查只能响一条（同一角色同时一条）：%d", rec.count())
	}
	tick(s)
	if rec.count() != 2 {
		t.Fatalf("下一次检查响另一条：%d", rec.count())
	}
	tick(s)
	if rec.count() != 2 {
		t.Fatalf("响过的不再响：%d", rec.count())
	}
}

func TestDifferentCharactersRingTogether(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	rec := &ringRec{}
	s := newSched(t, &MemoryStore{}, clock, rec)
	for _, c := range []string{"r1", "r2"} {
		if _, _, err := s.Create(Actor{Source: SourceChat, Character: c}, Draft{Text: "开会", At: at(2026, 11, 10, 9, 0)}); err != nil {
			t.Fatal(err)
		}
	}
	clock.Set(at(2026, 11, 10, 9, 0))
	tick(s)
	if rec.count() != 2 {
		t.Fatalf("%d", rec.count())
	}
}

func TestAbortedStaysPendingAndResumesAfterRestart(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	st := FileStore{Path: filepath.Join(t.TempDir(), "go", "reminders.json")}
	rec := &ringRec{res: func(RingRequest) RingResult { return RingResult{Aborted: true} }}
	s := newSched(t, st, clock, rec)
	r, _, err := s.Create(chat, Draft{Text: "开会", At: at(2026, 11, 10, 9, 0)})
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(at(2026, 11, 10, 9, 0))
	tick(s)
	got, _ := s.Get(r.ID)
	if got.Pending == nil || got.Status != StatusActive || got.Rings != 0 {
		t.Fatalf("程序退出时这一次不算：%+v", got)
	}

	// 重启：读回来接着响，Resumed 为真，本该响的时间不变
	clock.Set(at(2026, 11, 10, 11, 0))
	rec2 := &ringRec{}
	s2 := newSched(t, st, clock, rec2)
	tick(s2)
	if rec2.count() != 1 {
		t.Fatalf("重启后要接着响：%d", rec2.count())
	}
	if q := rec2.reqs[0]; !q.Resumed || !q.Scheduled.Equal(at(2026, 11, 10, 9, 0)) {
		t.Fatalf("%+v", q)
	}
	got, _ = s2.Get(r.ID)
	if got.Status != StatusDone || got.Pending != nil {
		t.Fatalf("%+v", got)
	}
	tick(s2)
	if rec2.count() != 1 {
		t.Fatal("不能再响")
	}
	// 再重启也不会再响
	rec3 := &ringRec{}
	s3 := newSched(t, st, clock, rec3)
	tick(s3)
	if rec3.count() != 0 {
		t.Fatal("响过的重启后不再响")
	}
}

func TestSendingBecomesUnknownOnRestart(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	st := &MemoryStore{}
	rec := &ringRec{}
	rec.res = func(q RingRequest) RingResult { return RingResult{Aborted: true} }
	s := newSched(t, st, clock, rec)
	r, _, err := s.Create(chat, Draft{Text: "开会", At: at(2026, 11, 10, 9, 0)})
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(at(2026, 11, 10, 9, 0))
	tick(s)
	if err := s.Mark(r.ID, at(2026, 11, 10, 9, 0), TargetQQ, DeliverySending); err != nil {
		t.Fatal(err)
	}
	rec2 := &ringRec{}
	s2 := newSched(t, st, clock, rec2)
	tick(s2)
	if rec2.count() != 1 || rec2.reqs[0].Prior[TargetQQ] != DeliveryUnknown {
		t.Fatalf("发到一半重启要记成不明：%+v", rec2.reqs)
	}
}

func TestRetryWhenNothingDelivered(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	fail := true
	rec := &ringRec{}
	rec.res = func(q RingRequest) RingResult {
		if fail {
			return RingResult{Retry: true, Note: "写库出错"}
		}
		return RingResult{Note: "说了"}
	}
	s := newSched(t, &MemoryStore{}, clock, rec)
	r, _, err := s.Create(chat, Draft{Text: "开会", At: at(2026, 11, 10, 9, 0)})
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(at(2026, 11, 10, 9, 0))
	tick(s)
	tick(s)
	if rec.count() != 1 {
		t.Fatalf("重试要等一会儿：%d", rec.count())
	}
	if got, _ := s.Get(r.ID); got.Status != StatusActive || got.Pending == nil {
		t.Fatalf("没送到不能算响过：%+v", got)
	}
	clock.Set(at(2026, 11, 10, 9, 2))
	fail = false
	tick(s)
	if rec.count() != 2 || !rec.reqs[1].Resumed || rec.reqs[1].Attempt != 1 {
		t.Fatalf("%+v", rec.reqs)
	}
	if got, _ := s.Get(r.ID); got.Status != StatusDone {
		t.Fatalf("%+v", got)
	}
}

func TestCancelledWhileRinging(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	rec := &ringRec{}
	var s *Scheduler
	rec.res = func(q RingRequest) RingResult {
		if _, err := s.Finish(chat, q.Reminder.ID, false); err != nil {
			t.Error(err)
		}
		if s.StillWanted(q.Reminder.ID, q.Scheduled) {
			t.Error("取消了就不该再发")
		}
		return RingResult{Dropped: true}
	}
	s = newSched(t, &MemoryStore{}, clock, rec)
	r, _, err := s.Create(chat, Draft{Text: "开会", At: at(2026, 11, 10, 9, 0), Repeat: Repeat{Unit: UnitDay}})
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(at(2026, 11, 10, 9, 0))
	tick(s)
	got, _ := s.Get(r.ID)
	if got.Status != StatusCancelled || got.Pending != nil || got.Rings != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestDeletedWhileRinging(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	rec := &ringRec{}
	var s *Scheduler
	rec.res = func(q RingRequest) RingResult {
		if _, err := s.Delete(chat, q.Reminder.ID); err != nil {
			t.Error(err)
		}
		return RingResult{Note: "说了"}
	}
	s = newSched(t, &MemoryStore{}, clock, rec)
	if _, _, err := s.Create(chat, Draft{Text: "开会", At: at(2026, 11, 10, 9, 0)}); err != nil {
		t.Fatal(err)
	}
	clock.Set(at(2026, 11, 10, 9, 0))
	tick(s)
	if len(s.List("")) != 0 {
		t.Fatal("删了就没了")
	}
	// 同一个角色不能卡在“正在响”
	if _, _, err := s.Create(chat, Draft{Text: "别的", At: at(2026, 11, 10, 10, 0)}); err != nil {
		t.Fatal(err)
	}
	clock.Set(at(2026, 11, 10, 10, 0))
	rec.res = nil
	tick(s)
	if rec.count() != 2 {
		t.Fatalf("%d", rec.count())
	}
}

func TestEditedWhileRingingKeepsNewTime(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	rec := &ringRec{}
	var s *Scheduler
	later := at(2026, 11, 12, 9, 0)
	rec.res = func(q RingRequest) RingResult {
		if _, err := s.Update(chat, q.Reminder.ID, Patch{At: &later}); err != nil {
			t.Error(err)
		}
		return RingResult{Note: "说了"}
	}
	s = newSched(t, &MemoryStore{}, clock, rec)
	r, _, err := s.Create(chat, Draft{Text: "开会", At: at(2026, 11, 10, 9, 0)})
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(at(2026, 11, 10, 9, 0))
	tick(s)
	got, _ := s.Get(r.ID)
	if got.Status != StatusActive || !got.NextAt.Equal(later) {
		t.Fatalf("响的时候改了时间不能覆盖：%+v", got)
	}
}

func TestPauseResume(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	rec := &ringRec{}
	s := newSched(t, &MemoryStore{}, clock, rec)
	r, _, err := s.Create(chat, Draft{Text: "吃药", At: at(2026, 11, 10, 9, 0), Repeat: Repeat{Unit: UnitDay}})
	if err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	if _, err := s.Update(chat, r.ID, Patch{Paused: &yes}); err != nil {
		t.Fatal(err)
	}
	clock.Set(at(2026, 11, 12, 10, 0))
	tick(s)
	if rec.count() != 0 {
		t.Fatal("暂停时不响")
	}
	got, err := s.Update(chat, r.ID, Patch{Paused: &no})
	if err != nil {
		t.Fatal(err)
	}
	if !got.NextAt.Equal(at(2026, 11, 13, 9, 0)) {
		t.Fatalf("周期恢复后不补暂停期间的：%+v", got)
	}
	// 一次性暂停过了点，恢复后响
	o, _, err := s.Create(chat, Draft{Text: "交房租", At: at(2026, 11, 12, 11, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(chat, o.ID, Patch{Paused: &yes}); err != nil {
		t.Fatal(err)
	}
	clock.Set(at(2026, 11, 12, 12, 0))
	if _, err := s.Update(chat, o.ID, Patch{Paused: &no}); err != nil {
		t.Fatal(err)
	}
	tick(s)
	if rec.count() != 1 {
		t.Fatalf("%d", rec.count())
	}
}

func TestUpdateRecomputesAndEndedRefused(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	s := newSched(t, &MemoryStore{}, clock, &ringRec{})
	r, _, err := s.Create(chat, Draft{Text: "开会", At: at(2026, 11, 10, 9, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(chat, r.ID, Patch{}); err == nil {
		t.Fatal("没有要改的要报错")
	}
	later := at(2026, 11, 11, 9, 0)
	got, err := s.Update(chat, r.ID, Patch{At: &later})
	if err != nil || !got.NextAt.Equal(later) {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = s.Update(chat, r.ID, Patch{Repeat: &Repeat{Unit: UnitWeek, Weekdays: []int{1}}})
	if err != nil || !got.NextAt.Equal(at(2026, 11, 16, 9, 0)) {
		t.Fatalf("改周期要重算：%+v %v", got, err)
	}
	got, err = s.Update(chat, r.ID, Patch{Repeat: &Repeat{}})
	if err != nil || !got.Repeat.None() {
		t.Fatalf("变回一次性：%+v %v", got, err)
	}
	past := at(2026, 11, 9, 1, 0)
	if _, err := s.Update(chat, r.ID, Patch{At: &past}); err == nil {
		t.Fatal("过去的时间要拒")
	}
	if _, err := s.Finish(chat, r.ID, false); err != nil {
		t.Fatal(err)
	}
	text := "改了"
	if _, err := s.Update(chat, r.ID, Patch{Text: &text}); err == nil || !strings.Contains(err.Error(), "新建") {
		t.Fatalf("结束的不能改：%v", err)
	}
	// 结束两次不算错，状态不变
	got, err = s.Finish(chat, r.ID, true)
	if err != nil || got.Status != StatusCancelled {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestThreadReminderWaitsForSnooze(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	rec := &ringRec{}
	quiet := at(2026, 11, 10, 20, 0)
	s, err := New(Options{Store: &MemoryStore{}, Ring: rec.ring, Now: clock.Now, QuietUntil: func(string) time.Time { return quiet }})
	if err != nil {
		t.Fatal(err)
	}
	th, _, err := s.Create(Actor{Source: SourceThread, Character: "r1"}, Draft{Text: "问问面试", At: at(2026, 11, 10, 9, 0)})
	if err != nil {
		t.Fatal(err)
	}
	asked, _, err := s.Create(chat, Draft{Text: "开会", At: at(2026, 11, 10, 9, 0)})
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(at(2026, 11, 10, 9, 0))
	tick(s)
	if rec.count() != 1 || rec.reqs[0].Reminder.ID != asked.ID {
		t.Fatalf("主人让建的照响，线里建的等：%+v", rec.reqs)
	}
	got, _ := s.Get(th.ID)
	if !got.NextAt.Equal(quiet) || got.Status != StatusActive || got.History[len(got.History)-1].Kind != "defer" {
		t.Fatalf("%+v", got)
	}
	clock.Set(at(2026, 11, 10, 19, 0))
	tick(s)
	if rec.count() != 1 {
		t.Fatal("别烦没到期不响")
	}
	quiet = time.Time{}
	clock.Set(at(2026, 11, 10, 20, 0))
	tick(s)
	if rec.count() != 2 {
		t.Fatalf("到期响：%d", rec.count())
	}
}

func TestPruneOldEnded(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	st := &MemoryStore{}
	s := newSched(t, st, clock, &ringRec{})
	r, _, err := s.Create(chat, Draft{Text: "开会", At: at(2026, 11, 10, 9, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish(chat, r.ID, true); err != nil {
		t.Fatal(err)
	}
	clock.Set(clock.Now().Add(29 * 24 * time.Hour))
	if s2 := newSched(t, st, clock, &ringRec{}); len(s2.List("")) != 1 {
		t.Fatal("没到 30 天还留着")
	}
	clock.Set(clock.Now().Add(2 * 24 * time.Hour))
	if s2 := newSched(t, st, clock, &ringRec{}); len(s2.List("")) != 0 {
		t.Fatal("过了 30 天清掉")
	}
}

func TestBadFileIsNotOverwritten(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.json")
	st := FileStore{Path: p}
	if err := st.Save(State{NextID: 3}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load()
	if err != nil || got.NextID != 3 {
		t.Fatalf("%+v %v", got, err)
	}
	if err := writeRaw(p, "{坏的"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(); err == nil || !strings.Contains(err.Error(), "读不懂") {
		t.Fatalf("%v", err)
	}
	if err := writeRaw(p, `{"version":99}`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(); err == nil || !strings.Contains(err.Error(), "更新的版本") {
		t.Fatalf("%v", err)
	}
}

func TestReminderRoundTripsThroughFile(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	st := FileStore{Path: filepath.Join(t.TempDir(), "r.json")}
	s := newSched(t, st, clock, &ringRec{})
	r, _, err := s.Create(chat, Draft{Text: "吃药", At: at(2026, 11, 10, 9, 0), Repeat: Repeat{Unit: UnitWeek, Every: 2, Weekdays: []int{1, 3}}, Targets: []string{TargetLocal}})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := newSched(t, st, clock, &ringRec{}).Get(r.ID)
	if !ok || got.Text != "吃药" || got.Repeat.Every != 2 || len(got.Repeat.Weekdays) != 2 || got.Source != SourceChat || !got.NextAt.Equal(r.NextAt) || got.Conv != "console:r1" {
		t.Fatalf("%+v", got)
	}
}

func parseArgs(t *testing.T, s string) jsjson.Value {
	t.Helper()
	v, err := jsjson.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParseCreateShapes(t *testing.T) {
	now := at(2026, 11, 9, 12, 0)
	d, err := ParseCreate(parseArgs(t, `{"text":"开会","at":"2026-11-10 09:00","repeat":{"unit":"week","every":2,"weekdays":[1,3]},"targets":["local"]}`), now)
	if err != nil || d.Text != "开会" || !d.At.Equal(at(2026, 11, 10, 9, 0)) || d.Repeat.Every != 2 || len(d.Targets) != 1 {
		t.Fatalf("%+v %v", d, err)
	}
	d, err = ParseCreate(parseArgs(t, `{"text":"开会","in_minutes":30}`), now)
	if err != nil || !d.At.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("%+v %v", d, err)
	}
	for _, c := range []struct{ name, args, want string }{
		{"不是对象", `[1]`, "对象"},
		{"null 没时间", `null`, "什么时候响"},
		{"缺时间", `{"text":"开会"}`, "什么时候响"},
		{"两个都写", `{"text":"开会","at":"2026-11-10 09:00","in_minutes":3}`, "只写一个"},
		{"text 是数字", `{"text":1,"in_minutes":3}`, "text"},
		{"at 是数字", `{"text":"开会","at":123}`, "at"},
		{"at 乱写", `{"text":"开会","at":"明天"}`, "现在是"},
		{"in_minutes 是 0", `{"text":"开会","in_minutes":0}`, "in_minutes"},
		{"in_minutes 负数", `{"text":"开会","in_minutes":-5}`, "in_minutes"},
		{"in_minutes 小数", `{"text":"开会","in_minutes":1.5}`, "in_minutes"},
		{"in_minutes 是文字", `{"text":"开会","in_minutes":"3"}`, "in_minutes"},
		{"repeat 是文字", `{"text":"开会","in_minutes":3,"repeat":"daily"}`, "repeat"},
		{"repeat 没单位", `{"text":"开会","in_minutes":3,"repeat":{}}`, "unit"},
		{"every 是 0", `{"text":"开会","in_minutes":3,"repeat":{"unit":"day","every":0}}`, "every"},
		{"every 小数", `{"text":"开会","in_minutes":3,"repeat":{"unit":"day","every":1.5}}`, "every"},
		{"weekdays 是数字", `{"text":"开会","in_minutes":3,"repeat":{"unit":"week","weekdays":1}}`, "weekdays"},
		{"weekdays 夹文字", `{"text":"开会","in_minutes":3,"repeat":{"unit":"week","weekdays":["一"]}}`, "weekdays"},
		{"targets 不是数组", `{"text":"开会","in_minutes":3,"targets":"qq"}`, "targets"},
		{"targets 夹数字", `{"text":"开会","in_minutes":3,"targets":[1]}`, "targets"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseCreate(parseArgs(t, c.args), now); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("要提到 %q：%v", c.want, err)
			}
		})
	}
}

func TestParseOtherShapes(t *testing.T) {
	now := at(2026, 11, 9, 12, 0)
	for _, args := range []string{`{}`, `null`, `{"id":0}`, `{"id":-1}`, `{"id":1.5}`, `{"id":"1"}`, `[1]`} {
		if _, err := ParseID(parseArgs(t, args)); err == nil {
			t.Errorf("%s 要拒", args)
		}
	}
	id, p, err := ParseUpdate(parseArgs(t, `{"id":3,"text":"新","in_minutes":5,"repeat":{"unit":"none"},"targets":[],"paused":true}`), now)
	if err != nil || id != 3 || *p.Text != "新" || !p.At.Equal(now.Add(5*time.Minute)) || !p.Repeat.None() || len(*p.Targets) != 0 || !*p.Paused {
		t.Fatalf("%d %+v %v", id, p, err)
	}
	if _, _, err := ParseUpdate(parseArgs(t, `{"id":3,"paused":"yes"}`), now); err == nil {
		t.Fatal("paused 类型错")
	}
	if id, done, err := ParseFinish(parseArgs(t, `{"id":2,"outcome":"done"}`)); err != nil || id != 2 || !done {
		t.Fatalf("%d %v %v", id, done, err)
	}
	if _, done, err := ParseFinish(parseArgs(t, `{"id":2,"outcome":"cancelled"}`)); err != nil || done {
		t.Fatalf("%v %v", done, err)
	}
	for _, args := range []string{`{"id":2}`, `{"id":2,"outcome":"maybe"}`, `{"id":2,"outcome":1}`} {
		if _, _, err := ParseFinish(parseArgs(t, args)); err == nil {
			t.Errorf("%s 要拒", args)
		}
	}
	if b, err := ParseIncludeEnded(parseArgs(t, `{"include_ended":true}`)); err != nil || !b {
		t.Fatalf("%v %v", b, err)
	}
	if b, err := ParseIncludeEnded(parseArgs(t, `null`)); err != nil || b {
		t.Fatalf("%v %v", b, err)
	}
	if _, err := ParseIncludeEnded(parseArgs(t, `{"include_ended":1}`)); err == nil {
		t.Fatal("类型错要拒")
	}
}

func TestDeclsAreValidJSON(t *testing.T) {
	for _, name := range ToolNames {
		d, ok := Decls[name]
		if !ok || d.Name != name || d.Description == "" {
			t.Fatalf("%s：%+v", name, d)
		}
		if _, err := jsjson.Parse(d.Parameters); err != nil {
			t.Fatalf("%s 的参数声明不是合法 JSON：%v", name, err)
		}
	}
}

func TestUpdateClearsPending(t *testing.T) {
	for _, c := range []struct {
		name  string
		patch func(r Reminder) []Patch
	}{
		{"改时间", func(Reminder) []Patch {
			at2 := at(2026, 11, 11, 8, 0)
			return []Patch{{At: &at2}}
		}},
		{"暂停再恢复", func(Reminder) []Patch {
			on, off := true, false
			return []Patch{{Paused: &on}, {Paused: &off}}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
			fail := true
			rec := &ringRec{}
			rec.res = func(q RingRequest) RingResult {
				if fail {
					return RingResult{Retry: true, Note: "写库出错"}
				}
				return RingResult{Note: "说了"}
			}
			s := newSched(t, &MemoryStore{}, clock, rec)
			r, _, err := s.Create(chat, Draft{Text: "开会", At: at(2026, 11, 10, 8, 10)})
			if err != nil {
				t.Fatal(err)
			}
			clock.Set(at(2026, 11, 10, 8, 10))
			tick(s)
			if got, _ := s.Get(r.ID); got.Pending == nil {
				t.Fatal("应当留着没收尾的一次")
			}
			for _, p := range c.patch(r) {
				if _, err := s.Update(chat, r.ID, p); err != nil {
					t.Fatal(err)
				}
			}
			if got, _ := s.Get(r.ID); got.Pending != nil {
				t.Fatalf("要清掉旧的一次：%+v", got.Pending)
			}
			fail = false
			clock.Set(at(2026, 11, 11, 8, 0))
			tick(s)
			tick(s)
			if got, _ := s.Get(r.ID); got.Rings != 1 || got.Status != StatusDone {
				t.Fatalf("只该响一次：%+v", got)
			}
			if rec.count() != 2 || rec.reqs[1].Resumed {
				t.Fatalf("%+v", rec.reqs)
			}
		})
	}
}
