package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
)

// fakeReminders 只记下收到的动作和参数，可以让它报错；不碰真实数据。
type fakeReminders struct {
	api.Reader
	mu    sync.Mutex
	calls chan string
	err   error
	opts  api.ReminderOptions
	hold  chan struct{} // 非 nil 时 ReminderOptions 卡到它关掉才返回（模拟后台卡住）
	last  struct {
		draft api.ReminderDraft
		patch api.ReminderPatch
	}
}

func newFakeReminders() *fakeReminders {
	return &fakeReminders{calls: make(chan string, 16), opts: api.ReminderOptions{Enabled: true, DefaultTargets: []string{"local"}}}
}

func (f *fakeReminders) rec(kind string, k api.ConversationKey, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls <- fmt.Sprintf("%s %s#%d", kind, k.Conversation, id)
	return f.err
}
func (f *fakeReminders) ReminderOptions() api.ReminderOptions {
	f.mu.Lock()
	hold := f.hold
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opts
}

func (f *fakeReminders) setOpts(o api.ReminderOptions) {
	f.mu.Lock()
	f.opts = o
	f.mu.Unlock()
}

// remindWaitOpts 等提醒页在后台问到的“开没开”回到界面（结果经 a.postFn 的队列送回）
func remindWaitOpts(t *testing.T, a *app, tt *ui.Tester, q *uiQueue) {
	t.Helper()
	remoteWait(t, "提醒开没开回到界面", func() bool { q.drain(); tt.Frame(); return !a.remindUI.optsAt.IsZero() && !a.remindUI.optsLoading })
}
func (f *fakeReminders) CreateReminder(_ context.Context, k api.ConversationKey, d api.ReminderDraft) error {
	f.mu.Lock()
	f.last.draft = d
	f.mu.Unlock()
	return f.rec("create", k, 0)
}
func (f *fakeReminders) UpdateReminder(_ context.Context, k api.ConversationKey, id int, p api.ReminderPatch) error {
	f.mu.Lock()
	f.last.patch = p
	f.mu.Unlock()
	return f.rec("update", k, id)
}
func (f *fakeReminders) FinishReminder(_ context.Context, k api.ConversationKey, id int, done bool) error {
	return f.rec(fmt.Sprintf("finish(done=%v)", done), k, id)
}
func (f *fakeReminders) DeleteReminder(_ context.Context, k api.ConversationKey, id int) error {
	return f.rec("delete", k, id)
}

func (f *fakeReminders) next(t *testing.T) string {
	t.Helper()
	select {
	case s := <-f.calls:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("后台没收到调用")
	}
	return ""
}

func (f *fakeReminders) none(t *testing.T) {
	t.Helper()
	select {
	case s := <-f.calls:
		t.Fatalf("不该有调用，收到 %s", s)
	case <-time.After(150 * time.Millisecond):
	}
}

func remindFixture(id int, text, source string, in time.Duration) api.ReminderInfo {
	now := time.Now()
	return api.ReminderInfo{ID: id, Character: "r1", Text: text, Status: "active", Source: source, At: now.Add(in).UnixMilli(), NextAt: now.Add(in).UnixMilli(),
		EffectiveTargets: []string{"local"}, Created: now.UnixMilli()}
}

// remindPageApp 是停在“设置 → 提醒”的新界面，编造的角色和提醒。
func remindPageApp(t *testing.T, rs ...api.ReminderInfo) (*app, *ui.Tester, *fakeReminders, api.ConversationKey) {
	t.Helper()
	a := shellApp()
	a.rail = 4
	f := newFakeReminders()
	a.reader = f
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:r1"}
	if len(rs) > 0 {
		a.localStatus = map[string]api.LocalStatus{key.Conversation: {Key: key, Reminders: rs}}
	}
	q := &uiQueue{}
	a.postFn = q.post
	tt := shellTester(a, 2048, 1030)
	click(t, tt, "提醒")
	remindWaitOpts(t, a, tt, q)
	return a, tt, f, key
}

// “提醒”在设置左栏里排在“通知”后面、“定期任务”前面；点了标题和内容换成它，旧分类的下标不动。
func TestRemindersCategoryPlacedBeforeLoops(t *testing.T) {
	a, tt, _, _ := remindPageApp(t)
	if !a.remindUI.page || a.loopsUI.page || a.realTab != tabLook {
		t.Fatalf("页面状态：remind=%v loops=%v realTab=%d", a.remindUI.page, a.loopsUI.page, a.realTab)
	}
	y := func(key string) float32 { return boxOf(t, tt, key).Box.Y }
	if !(y(categoryKey(tabNotify)) < y("category-reminders") && y("category-reminders") < y("category-loops") && y("category-loops") < y(categoryKey(tabData))) {
		t.Fatalf("顺序应是 通知、提醒、定期任务、数据：%v %v %v %v", y(categoryKey(tabNotify)), y("category-reminders"), y("category-loops"), y(categoryKey(tabData)))
	}
	click(t, tt, "定期任务")
	if a.remindUI.page || !a.loopsUI.page {
		t.Fatal("点定期任务应切走提醒页")
	}
	click(t, tt, "通知")
	if a.remindUI.page || a.loopsUI.page || a.realTab != tabNotify {
		t.Fatalf("点回通知后 remind=%v loops=%v realTab=%d", a.remindUI.page, a.loopsUI.page, a.realTab)
	}
}

func TestRemindersPageEmptyState(t *testing.T) {
	_, tt, _, _ := remindPageApp(t)
	if !tt.HasText("还没有提醒") || !hasKey(tt, "remind-empty") || !hasKey(tt, "remind-new") {
		t.Fatalf("空态不对：%q", tt.Texts())
	}
}

// 一行：内容、角色、下次时间、重复、发到哪、来源徽标；详情里有记录，新的在前。
func TestRemindersPageRowAndDetail(t *testing.T) {
	r := remindFixture(1, "晚上给编造的朋友回电话", "chat", 3*time.Hour)
	r.RepeatText = "每天"
	r.Repeat = api.ReminderRepeat{Unit: "day", Every: 1}
	r.EffectiveTargets = []string{"local", "qq"}
	r.Rings = 2
	r.History = []api.ReminderEvent{
		{At: time.Now().Add(-50 * time.Hour).UnixMilli(), Kind: "create", By: "chat"},
		{At: time.Now().Add(-26 * time.Hour).UnixMilli(), Kind: "ring", Note: "她说了一句；到本地"},
	}
	_, tt, _, _ := remindPageApp(t, r, remindFixture(2, "交电费", "owner", 5*time.Hour), remindFixture(3, "线里问他吃了没", "thread", 6*time.Hour))
	want := "白羽 · 下次 " + loopClock(r.NextAt, time.Now()) + " · 每天 · 发到本地 QQ"
	if !tt.HasText("晚上给编造的朋友回电话") || !tt.HasText(want) {
		t.Fatalf("摘要不对，应为 %q：%q", want, tt.Texts())
	}
	for _, w := range []string{"她建的", "你建的", "她主动建的"} {
		if !tt.HasText(w) {
			t.Errorf("缺来源徽标 %q", w)
		}
	}
	click(t, tt, "详情") // 第一个“详情”是第一行
	got := strings.Join(tt.Texts(), "\n")
	for _, w := range []string{"2 次", "响了：她说了一句；到本地", "建立"} {
		if !strings.Contains(got, w) {
			t.Errorf("详情里缺“%s”", w)
		}
	}
	if i, j := strings.Index(got, "响了："), strings.Index(got, "建立"); i < 0 || j < 0 || i > j {
		t.Errorf("记录应新的在前：%q", got)
	}
	click(t, tt, "收起")
	if hasKey(tt, "remind-console:r1#1-history") {
		t.Fatal("点了收起，详情还在")
	}
}

// 暂停、继续、取消各自调对（会话、编号、参数对得上）；她建的和主人建的按钮一样。
func TestRemindersPageActionsCallRightReminder(t *testing.T) {
	paused := remindFixture(2, "已暂停的", "owner", 5*time.Hour)
	paused.Status = "paused"
	_, tt, f, _ := remindPageApp(t, remindFixture(1, "她建的这条", "chat", time.Hour), paused)
	clickKey(t, tt, "remind-console:r1#1-pause")
	if got := f.next(t); got != "update console:r1#1" || f.last.patch.Paused == nil || !*f.last.patch.Paused {
		t.Fatalf("暂停调用不对：%s %+v", got, f.last.patch)
	}
	if !tt.HasText("白羽 · 已暂停 · 发到本地") {
		t.Fatalf("暂停行摘要不对：%q", tt.Texts())
	}
	clickKey(t, tt, "remind-console:r1#2-resume")
	if got := f.next(t); got != "update console:r1#2" || f.last.patch.Paused == nil || *f.last.patch.Paused {
		t.Fatalf("继续调用不对：%s", got)
	}
	clickKey(t, tt, "remind-console:r1#2-cancel")
	if got := f.next(t); got != "finish(done=false) console:r1#2" {
		t.Fatalf("取消调用不对：%s", got)
	}
}

// 删除先确认：点删除只弹框，取消不调用，确认才调用。
func TestRemindersPageDeleteNeedsConfirmation(t *testing.T) {
	_, tt, f, _ := remindPageApp(t, remindFixture(3, "买编造的东西", "chat", time.Hour))
	clickKey(t, tt, "remind-console:r1#3-remove")
	if !tt.HasText("删除提醒") {
		t.Fatalf("没弹确认框：%q", tt.Texts())
	}
	f.none(t)
	clickKey(t, tt, "action-cancel")
	f.none(t)
	if tt.HasText("删除提醒") {
		t.Fatal("取消后框还开着")
	}
	clickKey(t, tt, "remind-console:r1#3-remove")
	clickKey(t, tt, "action-delete")
	if got := f.next(t); got != "delete console:r1#3" {
		t.Fatalf("删除调用不对：%s", got)
	}
}

// 结束的收在“已结束”下面，默认折叠；展开后只留详情和删除。
func TestRemindersPageEndedFolded(t *testing.T) {
	done := remindFixture(4, "早就响过的", "chat", -time.Hour)
	done.Status, done.EndNote, done.EndedAt = "done", "响过了", time.Now().UnixMilli()
	_, tt, _, _ := remindPageApp(t, remindFixture(1, "还没响的", "chat", time.Hour), done)
	if !tt.HasText("已结束（1）") || tt.HasText("早就响过的") {
		t.Fatalf("结束的该折叠：%q", tt.Texts())
	}
	clickKey(t, tt, "remind-ended")
	if !tt.HasText("早就响过的") || !hasKey(tt, "remind-console:r1#4-detail") || !hasKey(tt, "remind-console:r1#4-remove") {
		t.Fatalf("展开后该有详情和删除：%q", tt.Texts())
	}
	for _, k := range []string{"edit", "pause", "cancel", "resume"} {
		if hasKey(tt, "remind-console:r1#4-"+k) {
			t.Errorf("结束的不该有 %s 按钮", k)
		}
	}
}

// 新建：填内容和时间、选每天，保存调 CreateReminder；框里错误不关框，成功才关。
func TestRemindersEditorCreateAndError(t *testing.T) {
	a, tt, f, _ := remindPageApp(t)
	q := &uiQueue{}
	a.postFn = q.post
	drain := func(cond func() bool) {
		remoteWait(t, "后台结果回到界面", func() bool { q.drain(); tt.Frame(); return cond() })
	}
	clickKey(t, tt, "remind-new")
	if !tt.HasText("新建提醒") || !hasKey(tt, "remind-text") || !hasKey(tt, "remind-at") {
		t.Fatalf("编辑框没出来：%q", tt.Texts())
	}
	// 空内容：框里提示，不调后台
	clickKey(t, tt, "action-save")
	if !tt.HasText("写一下要提醒什么") {
		t.Fatalf("空内容没提示：%q", tt.Texts())
	}
	f.none(t)
	a.remindUI.ed.text, a.remindUI.ed.at, a.remindUI.ed.rep = "给编造的朋友寄信", "2030-01-02 09:30", 1
	tt.Frame()
	f.err = errors.New("时间已经过了，现在是 2026-11-09 12:00")
	clickKey(t, tt, "action-save")
	if got := f.next(t); got != "create console:r1#0" || f.last.draft.Text != "给编造的朋友寄信" || f.last.draft.At != "2030-01-02 09:30" || f.last.draft.Repeat.Unit != "day" {
		t.Fatalf("新建调用不对：%s %+v", got, f.last.draft)
	}
	drain(func() bool {
		return a.remindUI.ed.open && tt.HasText("时间已经过了，现在是 2026-11-09 12:00")
	})
	if !a.remindUI.ed.open || !tt.HasText("时间已经过了，现在是 2026-11-09 12:00") {
		t.Fatalf("出错该留在框里写原因：open=%v %q", a.remindUI.ed.open, tt.Texts())
	}
	f.err = nil
	clickKey(t, tt, "action-save")
	f.next(t)
	drain(func() bool { return !a.remindUI.ed.open })
	if a.remindUI.ed.open {
		t.Fatal("成功后该关框")
	}
}

// 改：时间没动就不传（不把到点的秒数改掉）；改了才传；QQ 没开时勾选灰着并写原因。
func TestRemindersEditorEditAndQQOff(t *testing.T) {
	a, tt, f, _ := remindPageApp(t, remindFixture(1, "旧内容", "chat", 2*time.Hour))
	q := &uiQueue{}
	a.postFn = q.post
	clickKey(t, tt, "remind-console:r1#1-edit")
	if !tt.HasText("修改提醒") || a.remindUI.ed.text != "旧内容" || !tt.HasText("QQ 投递没开（reminders.qq）") {
		t.Fatalf("编辑框内容不对：%q %+v", tt.Texts(), a.remindUI.ed)
	}
	a.remindUI.ed.text = "新内容"
	tt.Frame()
	clickKey(t, tt, "action-save")
	if got := f.next(t); got != "update console:r1#1" || f.last.patch.Text == nil || *f.last.patch.Text != "新内容" || f.last.patch.At != nil {
		t.Fatalf("没动时间不该传 At：%s %+v", got, f.last.patch)
	}
	remoteWait(t, "保存结果回到界面、框关掉", func() bool { q.drain(); tt.Frame(); return !a.remindUI.ed.open })
	clickKey(t, tt, "remind-console:r1#1-edit")
	a.remindUI.ed.at = "2031-05-06 07:08"
	tt.Frame()
	clickKey(t, tt, "action-save")
	f.next(t)
	if f.last.patch.At == nil || *f.last.patch.At != "2031-05-06 07:08" {
		t.Fatalf("改了时间该传：%+v", f.last.patch)
	}
}

// 周期选项和“原样”：已有的非预设周期多一个选项，保存时保持原周期。
func TestRemindersRepeatChoices(t *testing.T) {
	t.Parallel()
	cases := []struct {
		r    api.ReminderRepeat
		want int
	}{
		{api.ReminderRepeat{}, 0},
		{api.ReminderRepeat{Unit: "day", Every: 1}, 1},
		{api.ReminderRepeat{Unit: "week", Every: 1, Weekdays: []int{1, 2, 3, 4, 5}}, 2},
		{api.ReminderRepeat{Unit: "week", Every: 1}, 3},
		{api.ReminderRepeat{Unit: "month", Every: 1}, 4},
		{api.ReminderRepeat{Unit: "hour", Every: 6}, remindRepeatCustom},
		{api.ReminderRepeat{Unit: "week", Every: 2}, remindRepeatCustom},
	}
	for _, c := range cases {
		i := remindRepeatIndex(c.r)
		if i != c.want {
			t.Errorf("%+v：想要 %d，得到 %d", c.r, c.want, i)
		}
		if back := remindRepeatOf(i, c.r); back.Unit != c.r.Unit || (i != 0 && back.Every != max(c.r.Every, 1)) {
			t.Errorf("%+v 往返变了：%+v", c.r, back)
		}
	}
}

// 提醒功能关着：新建灰着并写原因；没有后台入口时点动作有说明，不崩。
func TestRemindersDisabledAndNoControl(t *testing.T) {
	a, tt, f, _ := remindPageApp(t, remindFixture(1, "x", "chat", time.Hour))
	f.setOpts(api.ReminderOptions{Enabled: false})
	q := &uiQueue{}
	a.postFn = q.post
	a.remindUI.optsAt = time.Time{} // 让页面马上重问一次（平时 10 秒重问一次）
	remindWaitOpts(t, a, tt, q)
	if !tt.HasText("提醒功能没开（reminders.enabled），已有的不会响。") {
		t.Fatalf("没说明：%q", tt.Texts())
	}
	a.reader = nil
	clickKey(t, tt, "remind-console:r1#1-pause")
	if !tt.HasText("后台没有开着，暂时不能改提醒") {
		t.Fatalf("没有说明：%q", tt.Texts())
	}
}

// TestShotReminders 出组件区域图给人看（设 REMIND_SHOT 才出）：列表、编辑框、已结束展开，浅深各一套。
func TestShotReminders(t *testing.T) {
	dir := os.Getenv("REMIND_SHOT")
	if dir == "" {
		t.Skip("设 REMIND_SHOT 才出图")
	}
	for _, dark := range []bool{false, true} {
		suffix := map[bool]string{false: "浅", true: "深"}[dark]
		a := shellApp()
		a.rail = 4
		f := newFakeReminders()
		f.opts.AllowQQ = true
		a.reader = f
		key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:r1"}
		key2 := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:r2"}
		now := time.Now()
		r1 := remindFixture(1, "明早九点叫我起床，别忘了带编造的文件夹", "chat", 18*time.Hour)
		r2 := remindFixture(2, "每周五下午交周报", "owner", 40*time.Hour)
		r2.RepeatText, r2.Repeat, r2.EffectiveTargets = "每周五", api.ReminderRepeat{Unit: "week", Every: 1, Weekdays: []int{5}}, []string{"local", "qq"}
		r2.Character = "r2"
		r3 := remindFixture(3, "晚饭后问问他今天过得怎么样", "thread", 5*time.Hour)
		r4 := remindFixture(4, "已暂停的体检预约", "owner", 90*time.Hour)
		r4.Status = "paused"
		r5 := remindFixture(5, "昨晚的会议提醒", "chat", -time.Hour)
		r5.Status, r5.EndedAt, r5.EndNote = "done", now.Add(-time.Hour).UnixMilli(), "响过了"
		r5.History = []api.ReminderEvent{{At: now.Add(-30 * time.Hour).UnixMilli(), Kind: "create", By: "chat"}, {At: now.Add(-time.Hour).UnixMilli(), Kind: "ring", Note: "她说了一句；到本地"}}
		a.localStatus = map[string]api.LocalStatus{key.Conversation: {Key: key, Reminders: []api.ReminderInfo{r1, r3, r4, r5}}, key2.Conversation: {Key: key2, Reminders: []api.ReminderInfo{r2}}}
		q := &uiQueue{}
		a.postFn = q.post
		tt := shellTester(a, 2048, 1030)
		if dark {
			a.look.settings.Mode = "night"
			tt.Frame()
			tt.Frame()
		}
		click(t, tt, "提醒")
		remindWaitOpts(t, a, tt, q)
		save := func(name string) {
			tt.Frame()
			tt.Frame()
			img := tt.Image()
			// 截设置内容区：从“提醒”标题所在的栏起，整块右侧
			title := boxOf(t, tt, "settings-title").Box
			x0 := int(title.X*1.25) - 40
			if x0 < 0 {
				x0 = 0
			}
			sub := img.SubImage(image.Rect(x0, 0, img.Bounds().Max.X, img.Bounds().Max.Y))
			f, err := os.Create(filepath.Join(dir, name+"-"+suffix+".png"))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, sub)
			f.Close()
		}
		save("1-列表")
		clickKey(t, tt, "remind-console:r1#1-detail")
		clickKey(t, tt, "remind-ended")
		clickKey(t, tt, "remind-console:r1#5-detail")
		save("2-详情和已结束")
		clickKey(t, tt, "remind-console:r1#1-detail")
		clickKey(t, tt, "remind-new")
		a.remindUI.ed.text, a.remindUI.ed.rep = "周五给编造的朋友寄信", 3
		tt.Frame()
		save("3-新建框")
		clickKey(t, tt, "action-cancel")
		clickKey(t, tt, "remind-console:r1#4-remove")
		save("4-删除确认")
	}
}

// 后台卡住（ReminderOptions 一直不返回）时提醒页照样能画：以前构建里同步问，每帧都卡在这里（2026-11-12）
func TestRemindersPageDoesNotBlockOnOptions(t *testing.T) {
	a := shellApp()
	a.rail = 4
	f := newFakeReminders()
	f.hold = make(chan struct{})
	a.reader = f
	q := &uiQueue{}
	a.postFn = q.post
	type built struct {
		tt  *ui.Tester
		err error
	}
	done := make(chan built, 1)
	go func() { // 这里不调 click 这类会 t.Fatal 的辅助函数：子协程里 Fatal 会让主协程干等时限、报错也不对
		tt := shellTester(a, 2048, 1030)
		err := tt.Click("提醒")
		for range 5 {
			tt.Frame()
		}
		done <- built{tt, err}
	}()
	var tt *ui.Tester
	select {
	case b := <-done:
		if b.err != nil {
			close(f.hold)
			t.Fatal(b.err)
		}
		tt = b.tt
	case <-time.After(uiWaitLimit):
		close(f.hold)
		t.Fatal("后台卡住时提醒页画不出来")
	}
	if tt.HasText("提醒功能没开（reminders.enabled），已有的不会响。") {
		t.Fatal("还没问到就说没开")
	}
	close(f.hold)
	remindWaitOpts(t, a, tt, q)
	if !a.remindUI.opts.Enabled {
		t.Fatal("问到以后应是开着")
	}
}

// 开没开 10 秒重问一次：后台那边改了，页面过一会儿跟上；换了后台旧结果作废
func TestRemindersOptionsRefreshAndSourceChange(t *testing.T) {
	a, tt, f, _ := remindPageApp(t)
	q := &uiQueue{}
	a.postFn = q.post
	f.setOpts(api.ReminderOptions{Enabled: false})
	tt.Advance(remindOptsRefresh - time.Second)
	q.drain()
	tt.Frame()
	if tt.HasText("提醒功能没开（reminders.enabled），已有的不会响。") {
		t.Fatal("不到 10 秒不该重问")
	}
	tt.Advance(2 * time.Second)
	remoteWait(t, "10 秒后重问到“没开”", func() bool {
		q.drain()
		tt.Frame()
		return tt.HasText("提醒功能没开（reminders.enabled），已有的不会响。")
	})
	// 换数据源时正在问：旧后台的结果回来也不写进缓存
	f.setOpts(api.ReminderOptions{Enabled: true})
	f.mu.Lock()
	f.hold = make(chan struct{})
	f.mu.Unlock()
	tt.Advance(remindOptsRefresh)
	tt.Frame() // 发出一次在路上的问
	if !a.remindUI.optsLoading {
		t.Fatal("应该在问")
	}
	a.srcGen++ // 等同换了后台（真换数据源还会清缓存，见 main.go 换 reader 那段）
	close(f.hold)
	remoteWait(t, "旧后台的结果回来", func() bool { q.drain(); return q.pending() == 0 && len(f.calls) == 0 })
	q.drain()
	if a.remindUI.opts.Enabled {
		t.Fatal("换了后台以后旧结果不该写进缓存")
	}
}
