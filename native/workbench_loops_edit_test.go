package main

import (
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
)

// loopEditApp 是停在定期任务页、后台结果走队列的界面；drain 反复放行队列、画帧，直到条件成立（后台协程什么时候回来不定，不能只刷固定几次）。
func loopEditApp(t *testing.T, tasks ...api.LoopInfo) (*app, *ui.Tester, *fakeLoops, func(func() bool)) {
	t.Helper()
	a, tt, f, _ := loopsPageApp(t, tasks...)
	q := &uiQueue{}
	a.postFn = q.post
	drain := func(cond func() bool) {
		t.Helper()
		remoteWait(t, "后台结果回到界面", func() bool { q.drain(); tt.Frame(); return cond() })
	}
	return a, tt, f, drain
}

// 新建：点按钮出空白表单；取消不调后台、框关掉；再开是空白的。
func TestLoopsEditorOpensBlankAndCancels(t *testing.T) {
	a, tt, f, _ := loopEditApp(t)
	clickKey(t, tt, "loops-new")
	if !tt.HasText("新建定期任务") || !hasKey(tt, "loop-text") || !hasKey(tt, "loop-freq") || !hasKey(tt, "loop-chat") || !hasKey(tt, "loop-notify") {
		t.Fatalf("表单没出来：%q", tt.Texts())
	}
	if ed := a.loopsUI.ed; ed.id != 0 || ed.freq != 0 || ed.newChat != 0 || ed.notify != 0 || ed.text != "" {
		t.Fatalf("默认值不对：%+v", ed)
	}
	if !tt.HasText("每次醒来后，她自己决定下次什么时候再醒。") {
		t.Fatalf("让她自己定时该有说明：%q", tt.Texts())
	}
	a.loopsUI.ed.text = "写了一半的说明"
	clickKey(t, tt, "action-cancel")
	f.none(t)
	tt.Advance(time.Second) // 对话框有退场动画（160 毫秒），拷贝放完才算关掉
	tt.Frame()
	if a.loopsUI.ed.open || tt.HasText("新建定期任务") && hasKey(tt, "loop-text") {
		t.Fatal("取消后框还开着")
	}
	clickKey(t, tt, "loops-new")
	if a.loopsUI.ed.text != "" {
		t.Fatalf("再开应是空白：%q", a.loopsUI.ed.text)
	}
}

// 新建每周几：把表单读成接口的写法（周一在前、周日是 0），出错留在框里写原因，成功才关框。
func TestLoopsEditorCreateWeeklyAndError(t *testing.T) {
	a, tt, f, drain := loopEditApp(t)
	clickKey(t, tt, "loops-new")
	// 空说明：框里提示，不调后台
	clickKey(t, tt, "action-save")
	if !tt.HasText("写一下她每次要做什么") {
		t.Fatalf("空说明没提示：%q", tt.Texts())
	}
	f.none(t)
	ed := &a.loopsUI.ed
	ed.text, ed.freq, ed.at, ed.newChat, ed.notify = "每周整理一遍收件箱", 3, "18:30", 1, 1
	tt.Frame()
	if !hasKey(tt, "loop-day-0") || !hasKey(tt, "loop-day-6") || !hasKey(tt, "loop-at") || !tt.HasText("第一次在下一个选中的日子和这个时间，不会马上跑。") {
		t.Fatalf("每周的字段没出来：%q", tt.Texts())
	}
	// 没选天：框里提示
	clickKey(t, tt, "action-save")
	if !tt.HasText("每周要至少选一天") {
		t.Fatalf("没选天没提示：%q", tt.Texts())
	}
	f.none(t)
	clickKey(t, tt, "loop-day-0") // 点按钮切换选中：周一选上，周三选上再取消
	clickKey(t, tt, "loop-day-2")
	clickKey(t, tt, "loop-day-2")
	if !ed.days[0] || ed.days[2] {
		t.Fatalf("点按钮没切换选中：%v", ed.days)
	}
	ed.days[6] = true // 周日
	tt.Frame()
	f.err = errors.New("时间「25:99」不在 00:00 到 23:59 之间")
	clickKey(t, tt, "action-save")
	if got := f.next(t); got != "add console:r1#0" {
		t.Fatalf("新建调用不对：%s", got)
	}
	sp := f.lastSpec()
	if sp.Kind != "weekly" || sp.Prompt != "每周整理一遍收件箱" || sp.At != "18:30" || !slices.Equal(sp.Days, []int{1, 0}) || !sp.NewChat || sp.Notify != "errors" {
		t.Fatalf("表单读成的写法不对：%+v", sp)
	}
	drain(func() bool { return a.loopsUI.ed.open && tt.HasText("时间「25:99」不在 00:00 到 23:59 之间") })
	if !a.loopsUI.ed.open || !tt.HasText("时间「25:99」不在 00:00 到 23:59 之间") {
		t.Fatalf("出错该留在框里写原因：open=%v %q", a.loopsUI.ed.open, tt.Texts())
	}
	f.err = nil
	clickKey(t, tt, "action-save")
	f.next(t)
	drain(func() bool { return !a.loopsUI.ed.open })
	if a.loopsUI.ed.open {
		t.Fatal("成功后该关框")
	}
}

// 固定间隔：数字和单位换成秒；写不出数字的在框里拦下。
func TestLoopsEditorIntervalAndDaily(t *testing.T) {
	a, tt, f, drain := loopEditApp(t)
	clickKey(t, tt, "loops-new")
	ed := &a.loopsUI.ed
	ed.text, ed.freq, ed.every, ed.unit = "盯着构建", 1, "", 1
	tt.Frame()
	if !hasKey(tt, "loop-every") || !hasKey(tt, "loop-unit") {
		t.Fatalf("间隔的字段没出来：%q", tt.Texts())
	}
	clickKey(t, tt, "action-save")
	if !tt.HasText("写一下多久一次，要是一个大于 0 的整数") {
		t.Fatalf("没提示：%q", tt.Texts())
	}
	f.none(t)
	ed.every = "2"
	tt.Frame()
	clickKey(t, tt, "action-save")
	f.next(t)
	if sp := f.lastSpec(); sp.Kind != "interval" || sp.IntervalSec != 7200 || sp.NewChat || sp.Notify != "content" {
		t.Fatalf("%+v", sp)
	}
	drain(func() bool { return !a.loopsUI.ed.open })
	// 每天：只传时间，不带星期
	clickKey(t, tt, "loops-new")
	ed.text, ed.freq, ed.at = "早安", 2, "07:45"
	tt.Frame()
	if !hasKey(tt, "loop-at") || hasKey(tt, "loop-day-0") {
		t.Fatalf("每天只该有时间：%q", tt.Texts())
	}
	clickKey(t, tt, "action-save")
	f.next(t)
	if sp := f.lastSpec(); sp.Kind != "daily" || sp.At != "07:45" || len(sp.Days) != 0 {
		t.Fatalf("%+v", sp)
	}
}

// 编辑：现值回显（说明、频率、时间、星期、聊天方式、通知档），改了说明保存，调用的是这个任务；取消不调用。
func TestLoopsEditorEditEchoesAndSaves(t *testing.T) {
	task := loopFixture(4, "每周一三五发周报提醒")
	task.IntervalSec, task.Kind, task.At, task.Days, task.NewChat, task.Notify = 0, "weekly", "08:15", []int{1, 3, 5}, true, "none"
	a, tt, f, drain := loopEditApp(t, task)
	clickKey(t, tt, "loop-console:r1#4-edit")
	ed := a.loopsUI.ed
	if !tt.HasText("编辑定期任务") || !tt.HasText("角色：白羽") || ed.text != "每周一三五发周报提醒" || ed.freq != 3 || ed.at != "08:15" ||
		ed.days != [7]bool{true, false, true, false, true, false, false} || ed.newChat != 1 || ed.notify != 2 {
		t.Fatalf("回显不对：%q %+v", tt.Texts(), ed)
	}
	clickKey(t, tt, "action-cancel")
	f.none(t)
	clickKey(t, tt, "loop-console:r1#4-edit")
	a.loopsUI.ed.text = "每周一三五发周报提醒，附上数字"
	tt.Frame()
	clickKey(t, tt, "action-save")
	if got := f.next(t); got != "edit console:r1#4" {
		t.Fatalf("编辑调用不对：%s", got)
	}
	sp := f.lastSpec()
	if sp.Prompt != "每周一三五发周报提醒，附上数字" || sp.Kind != "weekly" || sp.At != "08:15" || !slices.Equal(sp.Days, []int{1, 3, 5}) || !sp.NewChat || sp.Notify != "none" {
		t.Fatalf("保存的内容不对：%+v", sp)
	}
	drain(func() bool { return !a.loopsUI.ed.open })
	if a.loopsUI.ed.open {
		t.Fatal("成功后该关框")
	}
}

// 固定间隔回显时选最大的整除单位：7200 秒是 2 小时，86400 秒是 1 天，90 分钟不整小时留在分钟。
func TestLoopsEditorIntervalUnitEcho(t *testing.T) {
	t.Parallel()
	a := shellApp()
	for _, c := range []struct {
		sec   int64
		every string
		unit  int
	}{{7200, "2", 1}, {86400, "1", 2}, {5400, "90", 0}, {60, "1", 0}} {
		a.loopEditOpen(loopRow{info: api.LoopInfo{ID: 1, Prompt: "x", Kind: "interval", IntervalSec: c.sec}})
		if ed := a.loopsUI.ed; ed.every != c.every || ed.unit != c.unit || ed.freq != 1 {
			t.Errorf("%d 秒：%+v", c.sec, ed)
		}
	}
	a.loopEditOpen(loopRow{info: api.LoopInfo{ID: 2, Prompt: "x", Kind: "self"}})
	if a.loopsUI.ed.freq != 0 {
		t.Errorf("自定节奏：%+v", a.loopsUI.ed)
	}
}

// 表单读成接口写法的边角：说明全空白、间隔不是数字或为 0 或带小数、每周没选天、周日是 0。
func TestLoopEditSpecShapes(t *testing.T) {
	t.Parallel()
	base := func() loopEdit { return loopEdit{text: "做事", every: "30", at: "09:00"} }
	bad := map[string]func(*loopEdit){
		"说明全空白": func(e *loopEdit) { e.text = " \n\t" },
		"间隔为 0": func(e *loopEdit) { e.freq, e.every = 1, "0" },
		"间隔带小数": func(e *loopEdit) { e.freq, e.every = 1, "1.5" },
		"间隔为空":  func(e *loopEdit) { e.freq, e.every = 1, "" },
		"间隔负数":  func(e *loopEdit) { e.freq, e.every = 1, "-3" },
		"每周没选天": func(e *loopEdit) { e.freq = 3 },
	}
	for name, mod := range bad {
		e := base()
		mod(&e)
		if _, err := e.spec(); err == nil {
			t.Errorf("%s 应报错", name)
		}
	}
	e := base()
	e.freq, e.at = 3, " 21:00 "
	e.days[6], e.days[1] = true, true
	sp, err := e.spec()
	if err != nil || sp.At != "21:00" || !slices.Equal(sp.Days, []int{2, 0}) {
		t.Fatalf("%+v %v", sp, err)
	}
	e = base()
	e.freq, e.unit, e.every = 1, 2, "3"
	if sp, _ = e.spec(); sp.IntervalSec != 3*86400 {
		t.Fatalf("%+v", sp)
	}
}

// 每天、每周几的任务行上写“每天 09:00”“每周一五 18:00”，详情里写聊天方式和通知档。
func TestLoopsRowShowsClockCadenceAndOptions(t *testing.T) {
	daily := loopFixture(1, "早安")
	daily.IntervalSec, daily.Kind, daily.At = 0, "daily", "09:00"
	weekly := loopFixture(2, "周报")
	weekly.IntervalSec, weekly.Kind, weekly.At, weekly.Days, weekly.NewChat, weekly.Notify = 0, "weekly", "18:00", []int{1, 5}, true, "errors"
	_, tt, _, _ := loopsPageApp(t, daily, weekly)
	for _, w := range []string{"每天 09:00", "每周一五 18:00"} {
		if got := strings.Join(tt.Texts(), "\n"); !strings.Contains(got, w) {
			t.Errorf("行上缺“%s”：%q", w, got)
		}
	}
	clickKey(t, tt, "loop-console:r1#2-detail")
	if got := strings.Join(tt.Texts(), "\n"); !strings.Contains(got, "每次新聊天") || !strings.Contains(got, "只提醒出错") {
		t.Errorf("详情里缺聊天方式和通知档：%q", got)
	}
}

// 旧后台（没有控制入口）时点保存给出说明，不关框、不崩。
func TestLoopsEditorWithoutControl(t *testing.T) {
	a, tt, _, _ := loopEditApp(t)
	a.reader = nil
	clickKey(t, tt, "loops-new")
	a.loopsUI.ed.text = "x"
	tt.Frame()
	clickKey(t, tt, "action-save")
	if !a.loopsUI.ed.open || !tt.HasText("后台没有开着，暂时不能改定期任务") {
		t.Fatalf("没有说明：%q", tt.Texts())
	}
}

// TestShotLoopEdit 出表单区域图给人看（设 LOOP_SHOT 才出）：新建（每周）、编辑回显，浅深各一套，2048×1030、像素比 1.25。
func TestShotLoopEdit(t *testing.T) {
	dir := os.Getenv("LOOP_SHOT")
	if dir == "" {
		t.Skip("设 LOOP_SHOT 才出图")
	}
	for _, dark := range []bool{false, true} {
		suffix := map[bool]string{false: "浅", true: "深"}[dark]
		task := loopFixture(1, "每周一三五早上发一份编造项目的进度提醒")
		task.IntervalSec, task.Kind, task.At, task.Days, task.NewChat, task.Notify = 0, "weekly", "08:15", []int{1, 3, 5}, true, "errors"
		a, tt, _, _ := loopsPageApp(t, task, loopFixture(2, "看看编造城市今天的天气"))
		if dark {
			a.look.settings.Mode = "night"
			tt.Frame()
			tt.Frame()
		}
		save := func(name string) {
			tt.Frame()
			tt.Frame()
			img := tt.Image()
			form, foot := boxOf(t, tt, "loop-form").Box, boxOf(t, tt, "action-save").Box // 框的外壳没有自己的键：按表单区和按钮行外扩一圈内距和标题行
			r := image.Rect(int((form.X-48)*1.25), int((form.Y-80)*1.25), int((form.X+form.W+48)*1.25), int((foot.Y+foot.H+48)*1.25)).Intersect(img.Bounds())
			f, err := os.Create(filepath.Join(dir, name+"-"+suffix+".png"))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, img.SubImage(r))
			f.Close()
		}
		clickKey(t, tt, "loop-console:r1#1-edit")
		save("1-编辑回显")
		clickKey(t, tt, "action-cancel")
		clickKey(t, tt, "loops-new")
		a.loopsUI.ed.freq = 1
		tt.Frame()
		save("2-新建固定间隔")
	}
}
