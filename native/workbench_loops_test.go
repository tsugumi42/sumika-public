package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
)

// fakeLoops 只记下收到的暂停、继续、删除（会话#编号），可以让它报错；不碰真实数据。
type fakeLoops struct {
	api.Reader
	mu    sync.Mutex
	calls chan string
	err   error
	spec  api.LoopSpec // 最近一次新建、修改收到的表单
}

func newFakeLoops() *fakeLoops { return &fakeLoops{calls: make(chan string, 16)} }

func (f *fakeLoops) rec(kind string, k api.ConversationKey, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls <- kind + " " + k.Conversation + "#" + string(rune('0'+id))
	return f.err
}
func (f *fakeLoops) PauseLoop(_ context.Context, k api.ConversationKey, id int) error {
	return f.rec("pause", k, id)
}
func (f *fakeLoops) ResumeLoop(_ context.Context, k api.ConversationKey, id int) error {
	return f.rec("resume", k, id)
}
func (f *fakeLoops) RemoveLoop(_ context.Context, k api.ConversationKey, id int) error {
	return f.rec("remove", k, id)
}

func (f *fakeLoops) AddLoop(_ context.Context, k api.ConversationKey, sp api.LoopSpec) (int, error) {
	f.mu.Lock()
	f.spec = sp
	f.mu.Unlock()
	return 7, f.rec("add", k, 0)
}
func (f *fakeLoops) EditLoop(_ context.Context, k api.ConversationKey, id int, sp api.LoopSpec) error {
	f.mu.Lock()
	f.spec = sp
	f.mu.Unlock()
	return f.rec("edit", k, id)
}

func (f *fakeLoops) lastSpec() api.LoopSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spec
}

func (f *fakeLoops) next(t *testing.T) string {
	t.Helper()
	select {
	case s := <-f.calls:
		return s
	case <-time.After(uiWaitLimit):
		t.Fatal("后台没收到调用")
	}
	return ""
}

func (f *fakeLoops) none(t *testing.T) {
	t.Helper()
	select {
	case s := <-f.calls:
		t.Fatalf("不该有调用，收到 %s", s)
	case <-time.After(150 * time.Millisecond):
	}
}

// loopsPageApp 是停在“设置 → 定期任务”的新界面，编造的角色和任务。
func loopsPageApp(t *testing.T, tasks ...api.LoopInfo) (*app, *ui.Tester, *fakeLoops, api.ConversationKey) {
	t.Helper()
	a := shellApp()
	a.rail = 4
	f := newFakeLoops()
	a.reader = f
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:r1"}
	if len(tasks) > 0 {
		a.localStatus = map[string]api.LocalStatus{key.Conversation: {Key: key, LoopTasks: tasks}}
	}
	tt := shellTester(a, 2048, 1030)
	click(t, tt, "定期任务")
	return a, tt, f, key
}

func clickKey(t *testing.T, tt *ui.Tester, key string) {
	t.Helper()
	b := boxOf(t, tt, key)
	tt.ClickAt(b.Box.X+b.Box.W/2, b.Box.Y+b.Box.H/2)
	tt.Frame()
	tt.Frame()
}

func loopFixture(id int, prompt string) api.LoopInfo {
	now := time.Now()
	return api.LoopInfo{ID: id, Prompt: prompt, Status: "running", IntervalSec: 1800, Runs: 3,
		NextRun: now.Add(30 * time.Minute).UnixMilli(), LastRun: now.Add(-time.Minute).UnixMilli(), LastResult: "content",
		Expires: now.Add(7 * 24 * time.Hour).UnixMilli()}
}

// “定期任务”在设置左栏里排在“通知”后面、“数据”前面；点了标题和内容都换成它，旧分类的下标不动。
func TestLoopsCategoryPlacedAfterNotifications(t *testing.T) {
	a, tt, _, _ := loopsPageApp(t)
	if !a.loopsUI.page || a.realTab != tabLook {
		t.Fatalf("页面状态：loops=%v realTab=%d", a.loopsUI.page, a.realTab)
	}
	y := func(key string) float32 { return boxOf(t, tt, key).Box.Y }
	if !(y(categoryKey(tabNotify)) < y("category-loops") && y("category-loops") < y(categoryKey(tabData))) {
		t.Fatalf("定期任务应排在通知和数据之间：%v %v %v", y(categoryKey(tabNotify)), y("category-loops"), y(categoryKey(tabData)))
	}
	if b := boxOf(t, tt, "settings-title"); b.Box.W == 0 || !tt.HasText("定期任务") {
		t.Fatal("内容区标题不是定期任务")
	}
	click(t, tt, "通知") // 点回旧分类：下标照旧
	if a.loopsUI.page || a.realTab != tabNotify {
		t.Fatalf("点回通知后 loops=%v realTab=%d，应为 false 和 %d", a.loopsUI.page, a.realTab, tabNotify)
	}
}

// 没有任务：写空态，不画行。
func TestLoopsPageEmptyState(t *testing.T) {
	_, tt, _, _ := loopsPageApp(t)
	if !tt.HasText("还没有定期任务") || !hasKey(tt, "loops-empty") {
		t.Fatalf("没有空态：%q", tt.Texts())
	}
}

// 一行：名字、角色、间隔、下次时间、上次结果；点详情看运行历史，再点收起。
func TestLoopsPageRowAndHistory(t *testing.T) {
	task := loopFixture(1, "看看编造城市今天的天气，有变化再告诉我")
	task.History = []api.LoopRun{
		{At: time.Now().Add(-90 * time.Minute).UnixMilli(), Result: "quiet"},
		{At: time.Now().Add(-60 * time.Minute).UnixMilli(), Result: "error", Note: "模型超时"},
		{At: time.Now().Add(-1 * time.Minute).UnixMilli(), Result: "content", Missed: 2},
	}
	_, tt, _, _ := loopsPageApp(t, task)
	if !tt.HasText("看看编造城市今天的天气，有变化再告诉我") {
		t.Fatalf("没有任务名：%q", tt.Texts())
	}
	want := "白羽 · 每 30 分钟 · 下次 " + loopClock(task.NextRun, time.Now()) + " · 上次：有内容"
	if !tt.HasText(want) {
		t.Fatalf("摘要不对，应为 %q：%q", want, tt.Texts())
	}
	if hasKey(tt, "loop-console:r1#1-history") {
		t.Fatal("没点详情就有历史")
	}
	click(t, tt, "详情")
	if !hasKey(tt, "loop-console:r1#1-history") {
		t.Fatalf("点了详情没有历史：%q", tt.Texts())
	}
	got := strings.Join(tt.Texts(), "\n")
	for _, w := range []string{"无事", "出错：模型超时", "有内容，错过了 2 次", "3 次", "到期"} {
		if !strings.Contains(got, w) {
			t.Errorf("历史里缺“%s”：%q", w, got)
		}
	}
	// 新的在前
	if i, j := strings.Index(got, "有内容，错过了 2 次"), strings.Index(got, "出错：模型超时"); i < 0 || j < 0 || i > j {
		t.Errorf("运行记录应新的在前：%q", got)
	}
	click(t, tt, "收起")
	if hasKey(tt, "loop-console:r1#1-history") {
		t.Fatal("点了收起，历史还在")
	}
}

// 暂停、继续各自调对（会话和编号对得上），暂停的任务显示继续和“已暂停”。
func TestLoopsPagePauseResumeCallRightTask(t *testing.T) {
	_, tt, f, _ := loopsPageApp(t, loopFixture(2, "整理收件箱"))
	click(t, tt, "暂停")
	if got := f.next(t); got != "pause console:r1#2" {
		t.Fatalf("暂停调用不对：%s", got)
	}
	// 状态订阅回来之前什么也不会自己变；换成暂停状态后行上写已暂停、按钮变继续
	paused := loopFixture(2, "整理收件箱")
	paused.Status = "paused"
	_, tt2, f2, _ := loopsPageApp(t, paused)
	if !tt2.HasText("白羽 · 每 30 分钟 · 已暂停 · 上次：有内容") {
		t.Fatalf("暂停行的摘要不对：%q", tt2.Texts())
	}
	click(t, tt2, "继续")
	if got := f2.next(t); got != "resume console:r1#2" {
		t.Fatalf("继续调用不对：%s", got)
	}
}

// 删除先确认：点删除只弹框，取消不调用，确认才调用。
func TestLoopsPageDeleteNeedsConfirmation(t *testing.T) {
	_, tt, f, _ := loopsPageApp(t, loopFixture(3, "每天备份编造文件夹"))
	click(t, tt, "删除")
	if !tt.HasText("删除定期任务") {
		t.Fatalf("没弹确认框：%q", tt.Texts())
	}
	f.none(t)
	clickKey(t, tt, "action-cancel")
	f.none(t)
	if tt.HasText("删除定期任务") {
		t.Fatal("取消后框还开着")
	}
	click(t, tt, "删除")
	clickKey(t, tt, "action-delete")
	if got := f.next(t); got != "remove console:r1#3" {
		t.Fatalf("删除调用不对：%s", got)
	}
}

// 后台拒绝时错误写在页顶，不吞掉。
func TestLoopsPageShowsBackendError(t *testing.T) {
	a, tt, f, _ := loopsPageApp(t, loopFixture(1, "整理收件箱"))
	q := &uiQueue{}
	a.postFn = q.post
	f.err = errors.New("这个会话里没有定期任务 #1")
	click(t, tt, "暂停")
	f.next(t)
	remoteWait(t, "错误显示在页顶", func() bool { q.drain(); tt.Frame(); return tt.HasText("这个会话里没有定期任务 #1") })
}

// 旧后台（没有控制入口）时点暂停给出说明，不崩。
func TestLoopsPageWithoutControl(t *testing.T) {
	a, tt, _, _ := loopsPageApp(t, loopFixture(1, "整理收件箱"))
	a.reader = nil
	click(t, tt, "暂停")
	if !tt.HasText("后台没有开着，暂时不能改定期任务") {
		t.Fatalf("没有说明：%q", tt.Texts())
	}
}

// 同一分钟里的两条运行记录不能撞元素键（KeyValues 拿名字当键），显示出来的时间也不带内部序号。
func TestLoopRunLinesKeysAreUnique(t *testing.T) {
	t.Parallel()
	now := time.Now()
	// 取整分钟再加 10 秒：两条记录差 1 秒，不取整时碰上分钟边界会落在不同分钟，测试就按墙钟随机红
	at := now.Add(-time.Hour).Truncate(time.Minute).Add(10 * time.Second).UnixMilli()
	lines := loopRunLines([]api.LoopRun{{At: at, Result: "quiet"}, {At: at + 1000, Result: "content"}}, now)
	if len(lines) != 2 || lines[0].Key == lines[1].Key || strings.TrimSpace(lines[0].Key) != strings.TrimSpace(lines[1].Key) || strings.Contains(lines[0].Key, "#") {
		t.Fatalf("%q", lines)
	}
	if lines[0].Value != "有内容" || lines[1].Value != "无事" {
		t.Fatalf("应新的在前：%q", lines)
	}
}
