package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/ctlproto"
)

// 检查器用的假读数：一轮、两次请求；第一次请求有完整详情，第二次的列都是坏格式。
func inspectorStats() *fakeStats {
	return &fakeStats{
		turns: ctlproto.ConvTurnsReport{Turns: []ctlproto.ConvTurn{
			{ID: "t1", Character: "r1", StartedAt: 1000, EndedAt: 4000, Outcome: "done", Requests: []ctlproto.ConvRequest{
				{ID: "req-a", Model: "编造模型", AtMS: 1000, TotalMs: 2000, TTFTMs: 300, Input: 1000, CacheRead: 500, Output: 80},
				{ID: "req-b", Model: "编造模型", AtMS: 2500, TotalMs: 900, Input: 200, Output: 20},
			}},
		}},
		details: map[string]ctlproto.RequestDetail{
			"req-a": {
				ID: "req-a", Model: "编造模型", Input: 500, CacheRead: 500, Output: 80, TotalMs: 2000, TTFTMs: 300,
				System:     []ctlproto.RequestSection{{ID: "core", Source: "编造来源", Chars: 7, Text: "编造的核心规矩\n\n第二段"}, {ID: "scene-x", Chars: 3, Missing: true}},
				Injected:   []ctlproto.RequestSection{{ID: "memory", Chars: 6, Text: "编造的记忆参考"}},
				Tools:      []ctlproto.RequestTool{{Name: "编造工具", Tokens: 20}},
				ToolTokens: 20, Window: 8000,
			},
			"req-b": {ID: "req-b", Model: "编造模型", SectionsRaw: "坏掉的段原文", ParseErr: []string{"sections_json：坏了"}},
		},
	}
}

// clickPrefix 点第一个以 prefix 开头的文字：摘要行的整句里有数字，测试只认开头。
func clickPrefix(tt *ui.Tester, prefix string) error {
	for _, s := range tt.Texts() {
		if strings.HasPrefix(s, prefix) {
			return tt.Click(s)
		}
	}
	return fmt.Errorf("没有以 %q 开头的文字：%q", prefix, tt.Texts())
}

func (f *fakeStats) detailCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.detailCalls
}

// 四种场合：本地私聊、本地群聊的“上下文”和 QQ 私聊、QQ 群的“活动”走同一个检查器。
// 展开一轮不读详情，点开某次请求才读一次；分页能看到系统提示词、注入、工具表、用量；
// 读不到写原因，格式坏了原样显示；悬停“本轮用量”只在本地两种场合出现。
func TestSideInspectorFourScenes(t *testing.T) {
	scenes := []struct {
		name, sel, tab string
		usage          bool
	}{
		{"本地私聊", "local:r1", "上下文", true},
		{"本地群聊", "home:g1", "上下文", true},
		{"QQ 私聊", "qq:private:80001:90001", "活动", false},
		{"QQ 群", "qq:group:90002", "活动", false},
	}
	for _, sc := range scenes {
		f := inspectorStats()
		q := &uiQueue{}
		a := shellApp()
		a.postFn = q.post
		a.run.src = f
		a.sel = sc.sel
		a.msgs[0].isChar, a.msgs[0].actor, a.msgs[0].at = true, "r1", 1500
		tt := shellTester(a, 2048, 1030)
		frames := func() {
			for range 3 {
				settleMem(q)
				tt.Frame()
			}
		}
		a.workbench.shell.st.Panes.SideOpen = true
		frames()
		if err := tt.Click(sc.tab); err != nil {
			t.Fatalf("%s：%v；%q", sc.name, err, tt.Texts())
		}
		frames()
		if err := tt.Click(sideTurnTime(f.turns.Turns[0])); err != nil {
			t.Fatalf("%s：%v；%q", sc.name, err, tt.Texts())
		}
		frames()
		if n := f.detailCount(); n != 0 {
			t.Fatalf("%s：只展开一轮不该读详情：%d 次", sc.name, n)
		}
		if err := clickPrefix(tt, "请求 1"); err != nil {
			t.Fatalf("%s：%v；%q", sc.name, err, tt.Texts())
		}
		frames()
		if n := f.detailCount(); n != 1 {
			t.Fatalf("%s：点开请求读一次：%d 次", sc.name, n)
		}
		if !hasSubstr(tt.Texts(), "编造的核心规矩") || !hasSubstr(tt.Texts(), "这段的正文没存下") {
			t.Fatalf("%s：系统提示词页：%q", sc.name, tt.Texts())
		}
		for _, page := range []struct{ tab, want string }{{"注入", "编造的记忆参考"}, {"工具表", "编造工具"}, {"用量", "命中缓存"}} {
			if err := tt.Click(page.tab); err != nil {
				t.Fatalf("%s：%v；%q", sc.name, err, tt.Texts())
			}
			frames()
			if !hasSubstr(tt.Texts(), page.want) {
				t.Fatalf("%s：%s页没有 %q：%q", sc.name, page.tab, page.want, tt.Texts())
			}
		}
		if n := f.detailCount(); n != 1 {
			t.Fatalf("%s：翻页不该再读库：%d 次", sc.name, n)
		}
		// 悬停消息：本地两种场合有“本轮用量”，QQ 两种没有
		body, ok := tt.Find("今晚的月色真好")
		if !ok {
			t.Fatalf("%s：找不到消息正文：%q", sc.name, tt.Texts())
		}
		tt.Move(body.X+body.W/2, body.Y+body.H/2)
		frames()
		if got := hasSubstr(tt.Texts(), "本轮用量"); got != sc.usage {
			t.Fatalf("%s：本轮用量钮 %v，应为 %v：%q", sc.name, got, sc.usage, tt.Texts())
		}
	}
}

// 点“本轮用量”：右栏切到上下文并展开这一轮（右栏原本停在资料也一样）。
func TestSideInspectorUsageButtonOpensTurn(t *testing.T) {
	f := inspectorStats()
	q := &uiQueue{}
	a := shellApp()
	a.postFn = q.post
	a.run.src = f
	a.msgs[0].isChar, a.msgs[0].actor, a.msgs[0].at = true, "r1", 1500
	tt := shellTester(a, 2048, 1030)
	frames := func() {
		for range 3 {
			settleMem(q)
			tt.Frame()
		}
	}
	a.workbench.shell.st.Panes.SideOpen = true
	frames()
	if err := tt.Click("上下文"); err != nil {
		t.Fatal(err)
	}
	frames()
	a.workbench.shell.sideTab = 0 // 切回资料；报告还在，聊天里的钮要有
	frames()
	body, _ := tt.Find("今晚的月色真好")
	tt.Move(body.X+body.W/2, body.Y+body.H/2)
	frames()
	if !hasSubstr(tt.Texts(), "本轮用量") {
		t.Fatalf("没有本轮用量钮：%q", tt.Texts())
	}
	if err := clickPrefix(tt, "本轮用量"); err != nil {
		t.Fatalf("%v；%q", err, tt.Texts())
	}
	frames()
	if a.workbench.shell.sideTab != 1 || !a.workbench.shell.sideContext.open["t1"] {
		t.Fatalf("点了应当切到上下文并展开这一轮：tab=%d open=%v", a.workbench.shell.sideTab, a.workbench.shell.sideContext.open)
	}
	if !hasSubstr(tt.Texts(), "请求 1") {
		t.Fatalf("展开后要看到请求：%q", tt.Texts())
	}
}

// 读不到、格式坏了：写原因 / 原样显示，不崩。
func TestSideInspectorFailuresAreShown(t *testing.T) {
	f := inspectorStats()
	q := &uiQueue{}
	a := shellApp()
	a.postFn = q.post
	a.run.src = f
	tt := shellTester(a, 2048, 1030)
	frames := func() {
		for range 3 {
			settleMem(q)
			tt.Frame()
		}
	}
	a.workbench.shell.st.Panes.SideOpen = true
	frames()
	_ = tt.Click("上下文")
	frames()
	_ = tt.Click(sideTurnTime(f.turns.Turns[0]))
	frames()
	if err := clickPrefix(tt, "请求 2"); err != nil {
		t.Fatalf("%v；%q", err, tt.Texts())
	}
	frames()
	if !hasSubstr(tt.Texts(), "坏掉的段原文") || !hasSubstr(tt.Texts(), "解析不了") {
		t.Fatalf("解析不了要原样显示原文：%q", tt.Texts())
	}
	// 读库失败：写原因
	f.mu.Lock()
	f.detailErr = errors.New("编造的故障")
	f.mu.Unlock()
	if err := clickPrefix(tt, "请求 1"); err != nil {
		t.Fatalf("%v；%q", err, tt.Texts())
	}
	frames()
	if !hasSubstr(tt.Texts(), "没读到这次请求的详情：编造的故障") {
		t.Fatalf("读不到要写原因：%q", tt.Texts())
	}
}

// 消息对轮的关联：只挂已结束的轮、窗口里她的最后一句；对不准的不挂。
func TestAssignTurnUsage(t *testing.T) {
	t.Parallel()
	ch := func(id, actor string, at int64) *msg {
		return &msg{id: id, isChar: true, actor: actor, at: at}
	}
	turns := []ctlproto.ConvTurn{
		{ID: "t2", Character: "r1", StartedAt: 5000, EndedAt: 0, Requests: []ctlproto.ConvRequest{{}}},    // 进行中
		{ID: "t1", Character: "r1", StartedAt: 1000, EndedAt: 3000, Requests: []ctlproto.ConvRequest{{}}}, // 已结束
	}
	msgs := []*msg{
		{id: "owner", at: 1100},
		ch("a", "r1", 1500),
		ch("b", "r1", 2900),
		ch("other", "r2", 2950),
		ch("late", "r1", 5200), // 在进行中那轮的时间里：不挂
		{id: "svc", isChar: true, actor: "r1", at: 2000, service: "通知"},
		{id: "live", isChar: true, actor: "r1", at: 2800, liveKind: liveReply},
	}
	got := assignTurnUsage(msgs, turns)
	if len(got) != 1 || got["b"] != "t1" {
		t.Fatalf("只有 t1 窗口里 r1 的最后一句挂上：%v", got)
	}
	// 同一句同时落在两轮的窗口里：对不准，都不给
	overlap := []ctlproto.ConvTurn{
		{ID: "x1", Character: "r1", StartedAt: 1000, EndedAt: 3000, Requests: []ctlproto.ConvRequest{{}}},
		{ID: "x2", Character: "r1", StartedAt: 2000, EndedAt: 4000, Requests: []ctlproto.ConvRequest{{}}},
	}
	if got := assignTurnUsage([]*msg{ch("c", "r1", 2500)}, overlap); len(got) != 0 {
		t.Fatalf("对不准不该挂：%v", got)
	}
	if txt := turnUsageText(ctlproto.ConvTurn{Requests: []ctlproto.ConvRequest{{Failed: "timeout"}}}); txt != "" {
		t.Fatalf("没有用量不编：%q", txt)
	}
}
