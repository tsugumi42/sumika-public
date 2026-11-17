package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// t0 是 2026-11-08 12:00 UTC，星期日。

func TestScheduleValidate(t *testing.T) {
	cases := []struct {
		name string
		s    Schedule
		bad  string // 空表示合法；否则错误里应含的字
	}{
		{"自定节奏", Schedule{}, ""},
		{"间隔", Schedule{Interval: time.Hour}, ""},
		{"间隔太短", Schedule{Interval: time.Second}, "间隔要在"},
		{"间隔太长", Schedule{Interval: MaxInterval + time.Minute}, "间隔要在"},
		{"每天", Schedule{At: "09:30"}, ""},
		{"每周", Schedule{At: "00:00", Days: []int{1, 3}}, ""},
		{"时间写错", Schedule{At: "9:30"}, "写得不对"},
		{"时间有字母", Schedule{At: "0a:30"}, "写得不对"},
		{"小时越界", Schedule{At: "24:00"}, "不在 00:00"},
		{"分钟越界", Schedule{At: "12:60"}, "不在 00:00"},
		{"星期越界", Schedule{At: "09:00", Days: []int{7}}, "星期要在"},
		{"负星期", Schedule{At: "09:00", Days: []int{-1}}, "星期要在"},
		{"只选星期没写时间", Schedule{Days: []int{1}}, "还要写几点"},
		{"间隔加钟点", Schedule{Interval: time.Hour, At: "09:00"}, "不能同时写"},
	}
	for _, c := range cases {
		err := c.s.Validate()
		switch {
		case c.bad == "" && err != nil:
			t.Errorf("%s：不该报错：%v", c.name, err)
		case c.bad != "" && (err == nil || !strings.Contains(err.Error(), c.bad)):
			t.Errorf("%s：应报含「%s」的错，得到 %v", c.name, c.bad, err)
		}
	}
}

func TestScheduleKindTextAndNext(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 11, 8, 8, 0, 0, 0, cst) // 周日 08:00
	cases := []struct {
		s    Schedule
		kind ScheduleKind
		text string
		next time.Time
	}{
		{Schedule{}, KindSelf, "自定节奏", time.Time{}},
		{Schedule{Interval: 90 * time.Minute}, KindInterval, "每 90 分钟", time.Time{}},
		{Schedule{At: "09:00"}, KindDaily, "每天 09:00", time.Date(2026, 11, 8, 9, 0, 0, 0, cst)},
		{Schedule{At: "08:00"}, KindDaily, "每天 08:00", time.Date(2026, 11, 9, 8, 0, 0, 0, cst)}, // 严格晚于现在
		{Schedule{At: "07:00", Days: []int{3, 1, 1}}, KindWeekly, "每周一三 07:00", time.Date(2026, 11, 9, 7, 0, 0, 0, cst)},
		{Schedule{At: "07:00", Days: []int{0}}, KindWeekly, "每周日 07:00", time.Date(2026, 11, 15, 7, 0, 0, 0, cst)},
		{Schedule{At: "09:00", Days: []int{0, 1, 2, 3, 4, 5, 6}}, KindDaily, "每天 09:00", time.Date(2026, 11, 8, 9, 0, 0, 0, cst)},
	}
	for _, c := range cases {
		n := c.s.Normalized()
		if n.Kind() != c.kind || n.Text() != c.text {
			t.Errorf("%+v：种类 %s 文字 %q，预期 %s %q", c.s, n.Kind(), n.Text(), c.kind, c.text)
		}
		if got := n.Next(now); !got.Equal(c.next) {
			t.Errorf("%+v：下次 %v，预期 %v", c.s, got, c.next)
		}
	}
}

func clockTask(t *testing.T, h *harness, o AddOptions) Task {
	t.Helper()
	task, err := h.s.Add("s1", "早安", o)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// 每天的任务第一轮等到钟点，不是马上；跑完排到明天同一时间。
func TestAddDailyRunsAtClockNotImmediately(t *testing.T) {
	h := newHarness(t, nil)
	task := clockTask(t, h, AddOptions{At: "13:00"})
	if !task.NextRun.Equal(t0.Add(time.Hour)) {
		t.Fatalf("第一轮应在 13:00，得到 %v", task.NextRun)
	}
	h.tick()
	if h.runCount() != 0 {
		t.Fatal("没到钟点就跑了")
	}
	h.clock.Add(time.Hour)
	h.tick()
	if h.runCount() != 1 {
		t.Fatal("到钟点没跑")
	}
	if got := h.task(task.ID).NextRun; !got.Equal(t0.Add(25 * time.Hour)) {
		t.Fatalf("下一轮应在明天 13:00，得到 %v", got)
	}
}

func TestAddWeeklySkipsToChosenDay(t *testing.T) {
	h := newHarness(t, nil)
	task := clockTask(t, h, AddOptions{At: "09:00", Days: []int{3, 1}})
	if want := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC); !task.NextRun.Equal(want) || task.Days[0] != 1 {
		t.Fatalf("应排到周一 09:00 且星期已排序：%+v", task)
	}
}

func TestAddValidation(t *testing.T) {
	h := newHarness(t, nil)
	bad := []AddOptions{{At: "25:00"}, {At: "09:00", Interval: time.Hour}, {Days: []int{1}}, {Interval: time.Second}, {Notify: "loud"}, {At: "09:00", Days: []int{8}}}
	for _, o := range bad {
		if _, err := h.s.Add("s1", "x", o); err == nil {
			t.Errorf("%+v 应报错", o)
		}
	}
	if len(h.s.List("")) != 0 {
		t.Fatal("非法输入不该留下任务")
	}
}

func TestEditScheduleRecomputesNext(t *testing.T) {
	h := newHarness(t, nil)
	task := clockTask(t, h, AddOptions{Interval: time.Hour})
	h.tick() // 12:00 跑过，下次 13:00
	h.clock.Add(10 * time.Minute)
	// 固定间隔 -> 每天 18:30：等下一个钟点，间隔清掉
	got, err := h.s.Edit("s1", task.ID, EditOptions{Schedule: &Schedule{At: "18:30"}})
	if err != nil || got.Interval != 0 || got.At != "18:30" || !got.NextRun.Equal(time.Date(2026, 11, 8, 18, 30, 0, 0, time.UTC)) {
		t.Fatalf("%+v %v", got, err)
	}
	// 每天 -> 每周二四：星期排序
	got, _ = h.s.Edit("s1", task.ID, EditOptions{Schedule: &Schedule{At: "07:00", Days: []int{4, 2}}})
	if !got.NextRun.Equal(time.Date(2026, 11, 10, 7, 0, 0, 0, time.UTC)) || got.Days[0] != 2 {
		t.Fatalf("%+v", got)
	}
	// 每周 -> 让她自己定：从上次跑的 12:00 起过一小时 = 13:00，已过点（现在 12:10 未过）
	got, _ = h.s.Edit("s1", task.ID, EditOptions{Schedule: &Schedule{}})
	if !got.SelfPaced() || got.At != "" || got.Days != nil || !got.NextRun.Equal(t0.Add(time.Hour)) {
		t.Fatalf("%+v", got)
	}
	// 自定 -> 固定间隔 30 分钟：从上次 12:00 起算 = 12:30
	got, _ = h.s.Edit("s1", task.ID, EditOptions{Schedule: &Schedule{Interval: 30 * time.Minute}})
	if got.Interval != 30*time.Minute || !got.NextRun.Equal(t0.Add(30*time.Minute)) {
		t.Fatalf("%+v", got)
	}
}

func TestEditPausedStaysPausedAndResumeUsesClock(t *testing.T) {
	h := newHarness(t, nil)
	task := clockTask(t, h, AddOptions{Interval: time.Hour})
	if _, err := h.s.Pause("s1", task.ID); err != nil {
		t.Fatal(err)
	}
	got, err := h.s.Edit("s1", task.ID, EditOptions{Schedule: &Schedule{At: "20:00"}, Prompt: "晚安"})
	if err != nil || got.Status != StatusPaused {
		t.Fatalf("暂停中的任务改后应仍是暂停：%+v %v", got, err)
	}
	h.clock.Add(9 * time.Hour) // 21:00，已过钟点
	h.tick()
	if h.runCount() != 0 {
		t.Fatal("暂停的任务不该跑")
	}
	if _, err := h.s.Resume("s1", task.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.task(task.ID); got.Status != StatusRunning || !got.NextRun.Equal(time.Date(2026, 11, 9, 20, 0, 0, 0, time.UTC)) {
		t.Fatalf("继续后应等明天 20:00，不是马上：%+v", got)
	}
}

func TestEditStoppedStaysStopped(t *testing.T) {
	h := newHarness(t, nil)
	task := clockTask(t, h, AddOptions{Interval: time.Hour})
	h.s.Stop("s1", task.ID)
	got, err := h.s.Edit("s1", task.ID, EditOptions{Schedule: &Schedule{At: "20:00"}})
	if err != nil || got.Status != StatusStopped {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestEditNotifyAndNewChatKeepNextRun(t *testing.T) {
	h := newHarness(t, nil)
	task := clockTask(t, h, AddOptions{At: "13:00"})
	none, yes := NotifyNone, true
	got, err := h.s.Edit("s1", task.ID, EditOptions{Notify: &none, NewChat: &yes})
	if err != nil || got.Notify != NotifyNone || !got.NewChat || !got.NextRun.Equal(task.NextRun) {
		t.Fatalf("%+v %v", got, err)
	}
	if got.NotifyLevel() != NotifyNone || (Task{}).NotifyLevel() != NotifyContent {
		t.Fatal("通知档的默认值不对")
	}
}

func TestEditInvalidInputsLeaveTaskUntouched(t *testing.T) {
	h := newHarness(t, nil)
	task := clockTask(t, h, AddOptions{Interval: time.Hour})
	loud := "loud"
	bad := map[string]EditOptions{
		"时间越界":  {Schedule: &Schedule{At: "24:01"}},
		"星期越界":  {Schedule: &Schedule{At: "09:00", Days: []int{9}}},
		"间隔太短":  {Schedule: &Schedule{Interval: time.Second}},
		"只有星期":  {Schedule: &Schedule{Days: []int{1}}},
		"通知档不对": {Notify: &loud},
		"空修改":   {},
	}
	for name, o := range bad {
		if _, err := h.s.Edit("s1", task.ID, o); err == nil {
			t.Errorf("%s 应报错", name)
		}
	}
	after := h.task(task.ID)
	if after.Interval != time.Hour || after.At != "" || !after.NextRun.Equal(task.NextRun) || after.Notify != "" {
		t.Fatalf("报错后任务不该变：%+v", after)
	}
}

func TestMissedClockRunsCountedOnRestart(t *testing.T) {
	h := newHarness(t, nil)
	task := clockTask(t, h, AddOptions{At: "13:00"}) // 下次 13:00
	h.clock.Add(3*24*time.Hour + 2*time.Hour)        // 关机三天多，现在是 11-11 14:00
	h.open()
	got := h.task(task.ID)
	if got.PendingMissed != 3 {
		t.Fatalf("11-08、09、10 的 13:00 和 11-11 这次里，除了要跑的那次错过应是 3，得到 %d", got.PendingMissed)
	}
	h.tick()
	if h.runCount() != 1 || !strings.Contains(h.runs[0].Input, "错过了 3 次") {
		t.Fatalf("只跑一次并写明错过：%d", h.runCount())
	}
}

func TestAttachLetsScheduleNextWorkInNewChat(t *testing.T) {
	h := newHarness(t, nil)
	var visible bool
	h.reply = func(r Request) (string, error) {
		h.s.Attach(r.Task.Session, "s1:new")
		visible = h.s.InFlight("s1:new")
		_, err := h.s.ExecScheduleNext("s1:new", mustJSON(`{"seconds":600,"reason":"新聊天里定的"}`))
		if err != nil {
			t.Error(err)
		}
		return "ok", nil
	}
	task := clockTask(t, h, AddOptions{NewChat: true})
	h.tick()
	if !visible {
		t.Fatal("Attach 之后新会话应算在途")
	}
	if got := h.task(task.ID); !got.NextRun.Equal(t0.Add(10*time.Minute)) || got.NextReason != "新聊天里定的" {
		t.Fatalf("%+v", got)
	}
	if h.s.InFlight("s1:new") {
		t.Fatal("跑完不该还在途")
	}
}

// 老文件没有 at、days、notify 字段：照原样读回，间隔推成固定间隔，没有间隔推成自定节奏。
func TestOldFileWithoutScheduleFieldsLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loop.json")
	old := `{"version":1,"nextId":3,"tasks":[
{"id":1,"session":"s1","prompt":"a","interval":3600000000000,"nextRun":"2026-11-08T13:00:00Z","status":"running","created":"2026-11-08T12:00:00Z"},
{"id":2,"session":"s1","prompt":"b","nextRun":"2026-11-08T13:00:00Z","status":"running","created":"2026-11-08T12:00:00Z"}]}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, FileStore{Path: path})
	a, b := h.task(1), h.task(2)
	if a.Schedule().Kind() != KindInterval || b.Schedule().Kind() != KindSelf || a.NotifyLevel() != NotifyContent || a.NewChat {
		t.Fatalf("%+v %+v", a, b)
	}
}

func TestLineShowsClockSchedule(t *testing.T) {
	h := newHarness(t, nil)
	task := clockTask(t, h, AddOptions{At: "09:00", Days: []int{1, 5}})
	if line := task.Line(t0); !strings.Contains(line, "每周一五 09:00") {
		t.Fatal(line)
	}
}
