package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/loop"
)

func loopCtl(t *testing.T, d *Desktop) api.LoopControl {
	t.Helper()
	var c api.LoopControl = &d.Reader
	return c
}

// 页面新建：每周几某时的任务排在下一个钟点，会话里留一行系统行；写法不对的一律拒，不留任务。
func TestAddLoopFromPageAndRejectBadInput(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := autoDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好")})
	ctl, ctx := loopCtl(t, d), context.Background()
	key := localKey("console:" + testChar)
	id, err := ctl.AddLoop(ctx, key, api.LoopSpec{Prompt: " 早安 ", Kind: "weekly", At: "09:00", Days: []int{3, 1}, Notify: loop.NotifyErrors, NewChat: true})
	if err != nil || id != 1 {
		t.Fatalf("%d %v", id, err)
	}
	task, _ := a.loops.Get(id)
	if task.Prompt != "早安" || task.Schedule().Kind() != loop.KindWeekly || task.Days[0] != 1 || task.Notify != loop.NotifyErrors || !task.NewChat || !task.NextRun.After(e.now) {
		t.Fatalf("%+v", task)
	}
	if !hasRow(slashRows(t, a, key.Conversation), "已建立 #1 每周一三 09:00") {
		t.Fatalf("缺系统行：%q", slashRows(t, a, key.Conversation))
	}
	bad := map[string]api.LoopSpec{
		"说明为空":   {Prompt: "  ", Kind: "self"},
		"时间越界":   {Prompt: "x", Kind: "daily", At: "24:00"},
		"没写时间":   {Prompt: "x", Kind: "daily"},
		"每周没选天":  {Prompt: "x", Kind: "weekly", At: "09:00"},
		"星期越界":   {Prompt: "x", Kind: "weekly", At: "09:00", Days: []int{7}},
		"间隔没写":   {Prompt: "x", Kind: "interval"},
		"间隔太短":   {Prompt: "x", Kind: "interval", IntervalSec: 5},
		"不认识的频率": {Prompt: "x", Kind: "yearly"},
		"通知档不对":  {Prompt: "x", Kind: "self", Notify: "loud"},
	}
	for name, sp := range bad {
		if _, err := ctl.AddLoop(ctx, key, sp); err == nil {
			t.Errorf("%s 应报错", name)
		}
	}
	if _, err := ctl.AddLoop(ctx, localKey("console:不存在的角色"), api.LoopSpec{Prompt: "x", Kind: "self"}); err == nil {
		t.Error("没有这个角色应报错")
	}
	if n := len(a.loops.List("")); n != 1 {
		t.Fatalf("非法输入不该留下任务，现在有 %d 个", n)
	}
}

// 页面修改：日程没变下次时间不动；改了日程重算；暂停的仍是暂停；别的会话的编号和不存在的编号拒绝。
func TestEditLoopFromPage(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := autoDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好")})
	ctl, ctx := loopCtl(t, d), context.Background()
	key := localKey("console:" + testChar)
	id, err := ctl.AddLoop(ctx, key, api.LoopSpec{Prompt: "旧", Kind: "daily", At: "20:00"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := a.loops.Get(id)
	// 只改说明和通知：日程没变，下次时间不动
	if err := ctl.EditLoop(ctx, key, id, api.LoopSpec{Prompt: "新", Kind: "daily", At: "20:00", Notify: loop.NotifyNone}); err != nil {
		t.Fatal(err)
	}
	got, _ := a.loops.Get(id)
	if got.Prompt != "新" || got.Notify != loop.NotifyNone || !got.NextRun.Equal(before.NextRun) {
		t.Fatalf("%+v", got)
	}
	// 暂停后改频率：仍是暂停，下次时间按新频率算
	if err := ctl.PauseLoop(ctx, key, id); err != nil {
		t.Fatal(err)
	}
	if err := ctl.EditLoop(ctx, key, id, api.LoopSpec{Prompt: "新", Kind: "interval", IntervalSec: 7200, Notify: loop.NotifyNone}); err != nil {
		t.Fatal(err)
	}
	got, _ = a.loops.Get(id)
	if got.Status != loop.StatusPaused || got.Interval != 2*time.Hour || got.At != "" {
		t.Fatalf("暂停中的任务改后应仍是暂停：%+v", got)
	}
	if !hasRow(slashRows(t, a, key.Conversation), "已修改 #1 每 2 小时") {
		t.Fatalf("缺系统行：%q", slashRows(t, a, key.Conversation))
	}
	for name, c := range map[string]struct {
		id int
		sp api.LoopSpec
	}{
		"编号 0":  {0, api.LoopSpec{Prompt: "x", Kind: "self"}},
		"不存在":   {9, api.LoopSpec{Prompt: "x", Kind: "self"}},
		"时间越界":  {id, api.LoopSpec{Prompt: "x", Kind: "daily", At: "99:00"}},
		"说明为空":  {id, api.LoopSpec{Prompt: "", Kind: "self"}},
		"每周没选天": {id, api.LoopSpec{Prompt: "x", Kind: "weekly", At: "09:00"}},
	} {
		if err := ctl.EditLoop(ctx, key, c.id, c.sp); err == nil {
			t.Errorf("%s 应报错", name)
		}
	}
	if after, _ := a.loops.Get(id); after.Interval != 2*time.Hour || after.Prompt != "新" {
		t.Fatalf("报错后任务不该变：%+v", after)
	}
	// LoopInfo 带日程字段给页面回显
	li := loopInfo(got)
	if li.Kind != "interval" || li.IntervalSec != 7200 || li.Notify != loop.NotifyNone || li.NewChat {
		t.Fatalf("%+v", li)
	}
}

// 每次新聊天：这一轮跑在新建的话题里，原会话里没有这一轮的行；话题登记在 local_topics，标题带任务开头。
func TestLoopNewChatRunsInNewTopic(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := autoDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{
		llmtest.Call("c1", "schedule_next", `{"seconds":600,"reason":"新话题里定的"}`), llmtest.Text("现在十二点"),
	}})
	ctl, ctx := loopCtl(t, d), context.Background()
	key := localKey("console:" + testChar)
	id, err := ctl.AddLoop(ctx, key, api.LoopSpec{Prompt: "报一下现在几点", Kind: "self", NewChat: true})
	if err != nil {
		t.Fatal(err)
	}
	a.loops.Tick(ctx)
	a.loops.Wait()
	st, err := a.set.ForConversation(key.Conversation)
	if err != nil {
		t.Fatal(err)
	}
	var topic, title string
	if err := st.Events.QueryRow(`SELECT id,title FROM local_topics WHERE character_id=?`, testChar).Scan(&topic, &title); err != nil {
		t.Fatalf("没有登记话题：%v", err)
	}
	if !strings.HasPrefix(title, "定期任务：报一下现在几点") {
		t.Fatalf("标题：%q", title)
	}
	rows := slashRows(t, a, "console:"+testChar+":topic:"+topic)
	if !hasRow(rows, "报一下现在几点") || !hasRow(rows, "现在十二点") {
		t.Fatalf("新话题里该有这一轮的输入和回答：%q", rows)
	}
	for _, r := range slashRows(t, a, key.Conversation) {
		if strings.Contains(r, "现在十二点") {
			t.Fatalf("原会话里不该有这一轮：%q", r)
		}
	}
	task, _ := a.loops.Get(id)
	if task.Runs != 1 || task.NextReason != "新话题里定的" {
		t.Fatalf("schedule_next 在新话题里该生效：%+v", task)
	}
	// 再跑一轮：又是一个新话题
	e.setNow(e.now.Add(11 * time.Minute))
	a.loops.Tick(ctx)
	a.loops.Wait()
	var n int
	st.Events.QueryRow(`SELECT COUNT(*) FROM local_topics WHERE character_id=?`, testChar).Scan(&n)
	if n != 2 {
		t.Fatalf("每次一个新话题，现在有 %d 个", n)
	}
}

// 通知档：默认有内容就提醒；只提醒出错时有内容不提醒；不提醒时出错也不提醒。
func TestLoopNotifyLevels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		notify string
		want   int
	}{{"", 1}, {loop.NotifyContent, 1}, {loop.NotifyErrors, 0}, {loop.NotifyNone, 0}}
	for _, c := range cases {
		e := newEnv(t)
		a, d, log := attentionDesktop(t, e, llmtest.Script{Reply: llmtest.Text("有事")})
		ctl, ctx := loopCtl(t, d), context.Background()
		key := localKey("console:" + testChar)
		if _, err := ctl.AddLoop(ctx, key, api.LoopSpec{Prompt: "看看", Kind: "interval", IntervalSec: 60, Notify: c.notify}); err != nil {
			t.Fatal(err)
		}
		a.loops.Tick(ctx)
		a.loops.Wait()
		if got := len(log.kinds()); got != c.want {
			t.Errorf("通知档 %q：提醒 %d 次，想要 %d", c.notify, got, c.want)
		}
	}
}
