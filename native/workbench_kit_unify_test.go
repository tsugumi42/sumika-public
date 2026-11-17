package main

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/winshell"
)

// 新界面的几选一：点了调一次 onChange，值等外面确认；disabled 时点了没反应。
func TestSettingsOneOfInWorkbench(t *testing.T) {
	a := shellApp()
	sel, disabled := 0, false
	var got []int
	tt := ui.NewTester(func(c *ui.Context) {
		a.applyLook(c)
		kit := a.wbKit(false, "")
		c.SetTheme(&kit.Theme)
		ui.Column(c).Fill().Padding(10).Children(func() {
			a.settingsOneOf(c, "mode", "名单方式", []string{"白名单", "黑名单"}, nil, sel, disabled, func(i int) { got = append(got, i) })
		})
	}, 500, 200)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("黑名单"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("点黑名单：onChange %v", got)
	}
	tt.Frame()
	if len(got) != 1 {
		t.Fatalf("外面没确认时不该反复报：%v", got)
	}
	disabled = true
	tt.Frame()
	if err := tt.Click("黑名单"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(got) != 1 {
		t.Fatalf("禁用时不该报：%v", got)
	}
}

// 新界面的键值下拉：选了键变、返回 true；铺满时入口和这一栏同宽。
func TestPickDropInWorkbench(t *testing.T) {
	a := shellApp()
	key, changed := "", false
	tt := ui.NewTester(func(c *ui.Context) {
		a.applyLook(c)
		kit := a.wbKit(false, "")
		c.SetTheme(&kit.Theme)
		ui.Column(c).Width(300).Padding(10).Children(func() {
			if a.pickDrop(c, "来自会话", []string{"", "x", "y"}, []string{"会话：全部", "群一 · 3", "群二 · 5"}, &key, true) {
				changed = true
			}
		})
	}, 600, 400)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("会话：全部"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("群二 · 5"); err != nil {
		t.Fatalf("%v；%q", err, tt.Texts())
	}
	tt.Frame()
	if !changed || key != "y" {
		t.Fatalf("选了群二：changed %v key %q", changed, key)
	}
	if b := boxOf(t, tt, "drop-来自会话"); b.Box.W < 270 {
		t.Fatalf("入口没铺满：%v", b.Box)
	}
}

// 防抖搜索框：打字时不报告，停够了才报告。
func TestDebouncedSearchInWorkbench(t *testing.T) {
	a := shellApp()
	query, reported := "", ""
	tt := ui.NewTester(func(c *ui.Context) {
		a.applyLook(c)
		kit := a.wbKit(false, "")
		c.SetTheme(&kit.Theme)
		ui.Column(c).Fill().Padding(10).Children(func() {
			a.debouncedSearch(c, "s", "搜索", &query, &reported, "她记的话", "稍等", 250*time.Millisecond)
		})
	}, 500, 200)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("她记的话"); err != nil {
		t.Fatal(err)
	}
	tt.Type("月")
	tt.Frame()
	if query != "月" || reported != "" {
		t.Fatalf("刚打完不该报告：query %q reported %q", query, reported)
	}
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if reported != "月" {
		t.Fatalf("停够了该报告：%q", reported)
	}
}

func TestNumberText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in    string
		limit int
		zero  bool
		want  string
		ok    bool
	}{
		{"", 0, false, "", true},
		{"12", 20, false, "12", true},
		{"1a2", 20, false, "12", true},
		{"21", 20, false, "", false},
		{"0", 20, false, "", true},
		{"0", 20, true, "0", true},
		{strings.Repeat("9", 11), 0, true, "", false},
		{"-5", 0, true, "5", true},
	} {
		got, ok := winshell.NumberText(tc.in, tc.limit, tc.zero)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q：得到 %q %v，想要 %q %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// 新界面的链接设置：名单方式走分段，切黑名单先确认，会话多选用标准勾选，文字编辑走标准对话框。
func TestWorkbenchLinkPage(t *testing.T) {
	f := &linkPageFake{demoLinkSettings: newDemoLinkSettings()}
	q := make(chan func(), 32)
	a := shellApp()
	a.rail, a.realTab, a.demoMode = 4, tabPlugins, false
	a.set = nil
	a.linkSettings.writer = f
	a.postFn = func(fn func()) { q <- fn }
	tt := shellTester(a, 2048, 1030)
	// drain 放行后台回投的结果、画帧，直到条件成立（后台协程什么时候回来不定，不能按“安静了多久”判断）
	drain := func(cond func() bool) {
		t.Helper()
		remoteWait(t, "后台结果回到界面", func() bool {
			for {
				select {
				case fn := <-q:
					fn()
					continue
				default:
				}
				break
			}
			tt.Frame()
			return cond()
		})
	}
	save := func(name string) {
		dir := os.Getenv("LOOK_SHOT")
		if dir == "" {
			return
		}
		out, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		if err := png.Encode(out, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	click := func(label string) {
		t.Helper()
		for range 8 {
			r, ok := tt.Find(label)
			if !ok {
				t.Fatalf("找不到 %s：%q", label, tt.Texts())
			}
			if r.Y >= 0 && r.Y+r.H <= 1000 {
				if err := tt.Click(label); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
				tt.Frame()
				return
			}
			dy := float32(400) // 往下滚是正数
			if r.Y < 0 {
				dy = -400
			}
			tt.Scroll(1000, 500, 0, dy)
			tt.Frame()
		}
		t.Fatalf("滚不到 %s", label)
	}
	drain(func() bool { return a.linkSettings.loaded && !a.linkSettings.busy })
	click("链接解析（link-reader）")
	drain(func() bool { return len(keysLike(tt, "link-list-mode")) > 0 && !a.linkSettings.busy })
	if len(keysLike(tt, "link-list-mode")) == 0 {
		t.Fatalf("名单方式没用分段：%q", tt.Texts())
	}
	save("链接页.png")
	click("黑名单")
	if a.linkSettings.view.Configured.Expand.Mode != "whitelist" || !a.linkSettings.confirmBox.Open {
		t.Fatal("切黑名单没有先确认")
	}
	save("链接页-确认.png")
	if len(keysLike(tt, "dialog")) == 0 {
		t.Fatal("确认框不是外壳的对话框")
	}
	click("确认切换")
	drain(func() bool { return a.linkSettings.view.Configured.Expand.Mode == "blacklist" && !a.linkSettings.busy })
	if a.linkSettings.view.Configured.Expand.Mode != "blacklist" {
		t.Fatal("确认后没切成黑名单")
	}
	click("选择会话")
	drain(func() bool { return len(keysLike(tt, "link-search")) > 0 && !a.linkSettings.busy })
	save("链接页-会话.png")
	if len(keysLike(tt, "link-search")) == 0 {
		t.Fatal("会话多选的搜索框不是标准的")
	}
	k := f.known[0]
	label := k.Name + " · " + k.Account
	click(label)
	drain(func() bool {
		return len(a.linkSettings.view.Configured.Expand.Conversations) == 1 && !a.linkSettings.busy
	})
	if got := a.linkSettings.view.Configured.Expand.Conversations; len(got) != 1 || got[0] != k.ID {
		t.Fatalf("勾选没存：%v", got)
	}
	click("完成")
	click("私聊开始状态")
	save("链接页-编辑.png")
	if len(keysLike(tt, "link-text-input")) == 0 {
		t.Fatal("文字编辑不是标准输入框")
	}
	click("取消")
	if a.linkSettings.editBox.Open {
		t.Fatal("取消没关窗")
	}
}

// testMainView 是记忆页、运行页那两大组测试跑哪个界面：默认新界面；旧界面退役前可以 SUMIKA_TEST_MAIN_VIEW=legacy 再跑一遍。
func testMainView() string {
	if v := os.Getenv("SUMIKA_TEST_MAIN_VIEW"); v != "" {
		return v
	}
	return "workbench"
}

// 记忆页、运行页在新界面里的样子：只出图（LOOK_SHOT），不判。
func TestWorkbenchPagesLook(t *testing.T) {
	dir := os.Getenv("LOOK_SHOT")
	if dir == "" {
		t.Skip("只在看样子时跑")
	}
	save := func(tt *ui.Tester, name string) {
		out, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		if err := png.Encode(out, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	h := newMemHarness(t, newDemoMemory(), 2048, 1030)
	save(h.tt, "记忆页.png")
	f := &fakeStats{rep: usageWith(liveTask(ctlproto.TaskForeground, "前台回合", 3, 1200))}
	r := newRunHarness(t, f, f, runTabUsage, 2048, 1030)
	save(r.tt, "运行页.png")
}

// 右栏：本地是“资料、上下文”，QQ 是“资料、活动”；切到上下文才读库，点开一轮看耗时。
func TestWorkbenchSideContext(t *testing.T) {
	if got := sideTabsFor(api.ConversationKey{Platform: "qq", Scene: "group"}); got[1] != "活动" {
		t.Fatalf("QQ 群：%v", got)
	}
	if got := sideTabsFor(api.ConversationKey{Platform: "qq", Scene: "private"}); got[1] != "活动" {
		t.Fatalf("QQ 私聊：%v", got)
	}
	if got := sideTabsFor(api.ConversationKey{Platform: "local", Scene: "group"}); got[1] != "上下文" {
		t.Fatalf("本地群聊：%v", got)
	}
	if got := sideTabsFor(api.ConversationKey{Platform: "local", Scene: "private"}); got[1] != "上下文" {
		t.Fatalf("本地私聊：%v", got)
	}
	f := &fakeStats{
		turns: ctlproto.ConvTurnsReport{Turns: []ctlproto.ConvTurn{
			{ID: "t1", Character: "r1", StartedAt: 1000, EndedAt: 4000, Outcome: "done", Requests: []ctlproto.ConvRequest{{Model: "编造模型", AtMS: 1000, TotalMs: 2000, TTFTMs: 300, Input: 1000, CacheRead: 500, Output: 80}}},
			{ID: "t0", Character: "r1", StartedAt: 500, Requests: []ctlproto.ConvRequest{{Model: "编造模型", AtMS: 500, Failed: "timeout"}}},
		}},
		spans: ctlproto.SpansReport{Turn: "t1", Rows: []ctlproto.SpanRow{
			{Kind: "turn.total", StartedAt: 1000, Ms: 3000, Outcome: "ok"},
			{Kind: "model.wait", Name: "编造模型", StartedAt: 1200, Ms: 300, Outcome: "ok"},
			{Kind: "tool.exec", Name: "read", StartedAt: 2000, Ms: 500, Outcome: "failed"},
		}},
	}
	q := &uiQueue{}
	a := shellApp()
	a.postFn = q.post
	a.run.src = f
	tt := shellTester(a, 2048, 1030)
	frames := func() {
		settleMem(q)
		tt.Frame()
		settleMem(q)
		tt.Frame()
	}
	a.workbench.shell.st.Panes.SideOpen = true
	frames()
	// 标题栏头像外圈的上下文环要用这份数据（ADR-351）：本地会话打开就读一次，之后按刷新节奏读，不是每帧读
	if calls := func() int { f.mu.Lock(); defer f.mu.Unlock(); return f.turnCalls }(); calls > 1 {
		t.Fatalf("没切到上下文时只该为头像环读一次：%d 次", calls)
	}
	if err := tt.Click("上下文"); err != nil {
		t.Fatalf("%v；%q", err, tt.Texts())
	}
	frames()
	if !tt.HasText("输入 1.0k（命中 50%）· 输出 80") && !tt.HasText("编造模型 · 输入 1,000（命中 50%）· 输出 80") {
		t.Fatalf("轮次摘要不对：%q", tt.Texts())
	}
	if !tt.HasText("timeout") && !hasSubstr(tt.Texts(), "timeout") {
		t.Fatalf("失败类别要写：%q", tt.Texts())
	}
	if !hasSubstr(tt.Texts(), "进行中") {
		t.Fatalf("没结束的轮不编用时：%q", tt.Texts())
	}
	label := sideTurnTime(f.turns.Turns[0])
	if err := tt.Click(label); err != nil {
		t.Fatalf("%v；%q", err, tt.Texts())
	}
	frames()
	if !hasSubstr(tt.Texts(), "工具执行 · read") || !hasSubstr(tt.Texts(), "请求 1") {
		t.Fatalf("点开要有请求和耗时：%q", tt.Texts())
	}
	if dir := os.Getenv("LOOK_SHOT"); dir != "" {
		out, err := os.Create(filepath.Join(dir, "右栏上下文.png"))
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		if err := png.Encode(out, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
}

// 新界面的后台配置页：开关是标准开关，行是外壳的行，打开后显示键名。
func TestWorkbenchSettingRows(t *testing.T) {
	a := shellApp()
	a.rail, a.realTab, a.demoMode = 4, tabTools, false
	a.set = nil
	k := settingPage
	key := ""
	for _, name := range []string{"agent.maxLive", "tools.workdir", "qq.owner", "agent.idleMinutes"} {
		if k(name) == tabTools {
			key = name
		}
	}
	if key == "" {
		t.Skip("工具与审批页没有这几个键")
	}
	a.real = api.Result[api.Settings]{State: api.Ready, Value: api.Settings{Items: api.List[api.Result[api.Setting]]{Items: []api.Result[api.Setting]{
		{State: api.Ready, Value: api.Setting{Key: key, Name: "编造的配置项", Help: "编造的说明", Value: "1"}},
		{State: api.Unsupported, Value: api.Setting{Key: key + "-other", Name: "没接上的项"}},
	}}}}
	tt := shellTester(a, 2048, 1030)
	tt.Frame()
	if len(keysLike(tt, "setting-detail")) == 0 {
		t.Fatalf("开关不是外壳的：%q", tt.Texts())
	}
	if !tt.HasText("编造的配置项") || !tt.HasText("还没启用") {
		t.Fatalf("配置行不对：%q", tt.Texts())
	}
	if tt.HasText(key) {
		t.Fatal("键名默认不显示")
	}
	a.realDetail = true
	tt.Frame()
	tt.Frame()
	if !tt.HasText(key) {
		t.Fatalf("打开后要显示键名：%q", tt.Texts())
	}
	if dir := os.Getenv("LOOK_SHOT"); dir != "" {
		out, err := os.Create(filepath.Join(dir, "配置行.png"))
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		if err := png.Encode(out, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
}

func hasSubstr(list []string, part string) bool {
	for _, s := range list {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}
