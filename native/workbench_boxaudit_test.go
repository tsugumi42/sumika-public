package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/segview"
	"sumika-native/internal/uitest"
)

// 元素框审计的场景：真窗口里跑 uitest.Audit，找出“框比看得见的内容大”和“框和看得见的东西对不上”的情况。
// 排查笔记：docs/archive/notes/排查-元素框超出可见内容-2026-11-09.md。数据全是编的，不读真实目录。

// boxScene 是一个场景：名字、窗口大小、画好的 Tester。
type boxScene struct {
	name string
	w, h int
	tt   *ui.Tester
}

type boxResult struct {
	Scene    string
	Skipped  int
	Findings []uitest.Finding
}

// boxFixture 在编造的本地私聊里放一批长内容，返回要审计的几个窗口。
func boxLongText() string {
	return strings.Repeat("这是一句很长很长的话，用来把气泡撑到换行。", 12) + "\n\n" + strings.Repeat("abcdefghij", 30) + "\n\n```go\nfunc main() { fmt.Println(\"" + strings.Repeat("很长的代码行", 20) + "\") }\n```\n\n- 第一项\n- 第二项" + strings.Repeat("很长", 40)
}

func boxSteps(pre string) []*chatui.Step {
	var out strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&out, "第 %d 行输出，带一点长度用来试试横向滚动是不是被切掉 %s\n", i, strings.Repeat("x", i%40))
	}
	return []*chatui.Step{
		{ID: pre + "s1", Kind: chatui.StepThink, State: chatui.StepDone, Seconds: 7, Thought: strings.Repeat("先想一想，再想一想。", 40)},
		{ID: pre + "s2", Kind: chatui.StepRead, State: chatui.StepDone, Target: "notes/a.md"},
		{ID: pre + "s3", Kind: chatui.StepSearch, State: chatui.StepDone, Target: "纸灯"},
		{ID: pre + "s4", Kind: chatui.StepRead, State: chatui.StepDone, Target: "notes/b.md", Output: "读到的内容\n第二行\n第三行"},
		{ID: pre + "s5", Kind: chatui.StepCommand, State: chatui.StepFailed, Target: "go test ./...\n第二行命令\n第三行\n第四行\n第五行\n第六行", Output: out.String(), HasExit: true, ExitCode: 1, Seconds: 12},
		{ID: pre + "s6", Kind: chatui.StepTool, State: chatui.StepDone, Tool: "web_search", Target: "查一个东西", Output: strings.Repeat("工具返回的一行很长的内容。", 30)},
		{ID: pre + "s7", Kind: chatui.StepEdit, State: chatui.StepDone, Target: "src/main.go", Added: 3, Removed: 1, Diff: "--- a/src/main.go\n+++ b/src/main.go\n@@ -1,3 +1,5 @@\n-旧的一行\n+新的一行" + strings.Repeat("很长", 60) + "\n+再加一行\n 不变的一行\n"},
		{ID: pre + "s8", Kind: chatui.StepCommand, State: chatui.StepRunning, Target: "sleep 100", Output: out.String()},
	}
}

func boxMessages(a *app, which string) {
	k := a.keyOf(a.sel)
	long := view("long", "owner", "主人", boxLongText(), t0)
	long.Ref.Conversation = k
	m1 := msgFromView(k, long, charOf)
	m1.mine = true
	reply := view("reply", "character", "白羽", boxLongText(), t0+1000)
	reply.Ref.Conversation = k
	m2 := msgFromView(k, reply, charOf)
	m2.md = true
	m2.blocks = segview.TextBlocks(boxLongText())
	all := boxSteps("a")
	procOf := func(id string, steps ...*chatui.Step) *msg {
		return &msg{id: id, who: "白羽", isChar: true, actor: "r1", at: t0 + 2000, steps: steps}
	}
	procThink := procOf("p-think", all[0], all[1], all[2], all[3])
	procCmd := procOf("p-cmd", all[4])
	procTool := procOf("p-tool", all[5], all[6])
	procLive := procOf("p-live", all[0], all[7])
	procLive.liveKind = liveProcess
	img := view("img", "human", "编的甲", "[图片]", t0+3000)
	img.Ref.Conversation = k
	img.Segments = []api.DisplaySegment{{Kind: "image", Width: 800, Height: 600}, {Kind: "image", Width: 60, Height: 1200}, {Kind: "image", Width: 3000, Height: 100}, {Kind: "text", Text: "图后面的话"}}
	m3 := msgFromView(k, img, charOf)
	fw := view("fw", "human", "编的乙", "[合并转发]", t0+4000)
	fw.Ref.Conversation = k
	fw.Segments = []api.DisplaySegment{{Kind: "forward", Ref: "outer", Forward: &api.ForwardBrief{Title: "编的甲的聊天记录", Count: 5, State: "ready", Lines: [3]string{"编的甲：" + strings.Repeat("很长的转发摘要", 12), "编的乙：第二行", "编的丙：第三行"}}}}
	m4 := msgFromView(k, fw, charOf)
	q := view("q", "owner", "主人", "引用回复", t0+5000)
	q.Ref.Conversation = k
	q = quoting(q, "long", "白羽", strings.Repeat("被引用的很长的话。", 20), true)
	m5 := msgFromView(k, q, charOf)
	m5.mine = true
	switch which {
	case "text":
		a.msgs = []*msg{m1, m2}
	case "p-think":
		a.msgs = []*msg{procThink}
	case "p-cmd":
		a.msgs = []*msg{procCmd}
	case "p-tool":
		a.msgs = []*msg{procTool}
	case "p-live":
		a.msgs = []*msg{procLive}
	default:
		a.msgs = []*msg{m3, m4, m5}
	}
	for _, m := range a.msgs {
		m.noEnter, m.entered = true, true
	}
	a.stepOpen = map[string]*bool{}
	for _, id := range []string{"p-think", "p-cmd", "p-tool", "p-live"} {
		o := true
		a.stepOpen[id] = &o
	}
	for _, s := range all {
		o := true
		a.stepOpen[s.ID] = &o
	}
	a.list = a.newList()
}

func boxApp(sel string, group bool, rail int) *app {
	a := shellApp()
	a.sel, a.group, a.rail = sel, group, rail
	a.chars[0].Model = "demo/model-00"
	a.winControls = (&fakeWindow{}).controls()
	return a
}

// boxFrames 把时间推过动画再画几帧：审计只看静止的样子，不看淡入淡出的中途。
func boxFrames(tt *ui.Tester) {
	for range 4 {
		tt.Advance(time.Second)
		tt.Frame()
	}
}

// boxScenes 造出所有要审计的场景；pages 为假时不含记忆页、运行页（旧组件库画的，慢）。
func boxScenes(t *testing.T, widths []int, pages bool) []boxScene {
	var out []boxScene
	for _, w := range widths {
		enter := func(a *app, h int) *ui.Tester {
			tt := shellTester(a, w, h)
			if w < 700 { // 窄窗口先是会话列表，点进会话才有聊天窗
				tt.Click("白羽")
				boxFrames(tt)
			}
			return tt
		}
		name := func(what string) string { return fmt.Sprintf("%s/宽%d", what, w) }
		// 本地私聊：长文字、过程气泡的几种、图片和转发和引用
		for _, which := range []string{"text", "p-think", "p-cmd", "p-tool", "p-live", "media"} {
			a := boxApp("local:r1", false, 0)
			boxMessages(a, which)
			out = append(out, boxScene{name("本地私聊-" + which), w, 1030, enter(a, 1030)})
		}
		// 引用卡超过 4 张
		a := boxApp("local:r1", false, 0)
		boxMessages(a, "text")
		tt := enter(a, 1030)
		p := a.composerOf("local:r1")
		for i := 0; i < 6; i++ {
			p.AddQuote(chatui.Quoted{MessageID: fmt.Sprintf("m%d", i), Name: "白羽", Text: strings.Repeat("被引的话。", 6+i*3), Start: 0, End: 4}, false)
		}
		out = append(out, boxScene{name("引用卡6张"), w, 1030, tt})
		// 右栏：资料
		a = boxApp("local:r1", false, 0)
		boxMessages(a, "media")
		a.workbench.shell.st.Panes.SideOpen = true
		out = append(out, boxScene{name("右栏资料"), w, 1030, enter(a, 1030)})
		// 本地群聊、QQ 私聊、QQ 群（只有一句话）
		for _, pl := range []struct {
			name, sel string
			group     bool
			rail      int
		}{{"本地群聊", "home:g1", true, 0}, {"QQ私聊", "qq:private:80001:90001", false, 1}, {"QQ群", "qq:group:90002", true, 1}} {
			a = boxApp(pl.sel, pl.group, pl.rail)
			a.workbench.shell.st.Panes.SideOpen = true
			out = append(out, boxScene{name(pl.name), w, 1030, shellTester(a, w, 1030)})
		}
		// 群聊里的长内容：有头像格，列窄时气泡最宽要扣掉头像和操作条
		for _, pl := range []struct {
			name, sel string
			rail      int
		}{{"本地群聊长内容", "home:g1", 0}, {"QQ群长内容", "qq:group:90002", 1}} {
			for _, which := range []string{"text", "media"} {
				a = boxApp(pl.sel, true, pl.rail)
				boxMessages(a, which)
				out = append(out, boxScene{name(pl.name + "-" + which), w, 1030, enter(a, 1030)})
			}
		}
		// 输入框里一大段多行文字
		a = boxApp("local:r1", false, 0)
		boxMessages(a, "text")
		tt = enter(a, 1030)
		tt.Click("发一条消息")
		tt.Type(strings.Repeat("输入框里写了很长很长的一行话，还会换行。", 6))
		for range 6 {
			tt.Key(ui.Shift, ui.KeyEnter)
			tt.Type(strings.Repeat("再写一行很长的话。", 10))
		}
		tt.Type(strings.Repeat("无空格", 80))
		out = append(out, boxScene{name("输入框长文字"), w, 1030, tt})
		// 鼠标停在消息上：操作条显出来
		for _, key := range []string{"img", "q"} {
			a = boxApp("local:r1", false, 0)
			boxMessages(a, "media")
			tt = enter(a, 1030)
			if n := uitest.Tree(tt).Find(key); n != nil {
				tt.Move(n.Box.X+n.Box.W/2, n.Box.Y+n.Box.H/2)
			}
			out = append(out, boxScene{name("悬停消息" + key), w, 1030, tt})
		}
		// 右栏：上下文（有几轮，展开一轮）
		a = boxApp("local:r1", false, 0)
		boxMessages(a, "media")
		fq := &uiQueue{}
		a.postFn = fq.post
		fs := &fakeStats{turns: ctlproto.ConvTurnsReport{Turns: []ctlproto.ConvTurn{
			{ID: "t1", Character: "r1", StartedAt: 1000, EndedAt: 4000, Outcome: "done", Requests: []ctlproto.ConvRequest{{Model: "编造模型", AtMS: 1000, TotalMs: 2000, TTFTMs: 300, Input: 1000, CacheRead: 500, Output: 80}}},
			{ID: "t0", Character: "r1", StartedAt: 500, Requests: []ctlproto.ConvRequest{{Model: "编造模型", AtMS: 500, Failed: "timeout"}}},
		}}, spans: ctlproto.SpansReport{Turn: "t1", Rows: []ctlproto.SpanRow{
			{Kind: "turn.total", StartedAt: 1000, Ms: 3000, Outcome: "ok"},
			{Kind: "model.wait", Name: "编造模型", StartedAt: 1200, Ms: 300, Outcome: "ok"},
			{Kind: "tool.exec", Name: "read", StartedAt: 2000, Ms: 500, Outcome: "failed"},
		}}}
		a.run.src = fs
		a.workbench.shell.st.Panes.SideOpen = true
		tt = enter(a, 1030)
		if w >= 700 {
			if err := tt.Click("上下文"); err == nil {
				for range 3 {
					settleMem(fq)
					boxFrames(tt)
				}
				if err := tt.Click(sideTurnTime(fs.turns.Turns[0])); err == nil {
					for range 3 {
						settleMem(fq)
						boxFrames(tt)
					}
				}
			}
		}
		out = append(out, boxScene{name("右栏上下文展开"), w, 1030, tt})
		// 右栏：合并转发详情（含嵌套的一层）；窄窗口走抽屉
		fr := &forwardUIReader{views: map[string]api.ForwardView{
			"outer": {ID: "outer", State: "ready", Nodes: []api.ForwardNode{
				{UID: "100", Name: "编的甲", At: t0, Segments: []api.DisplaySegment{{Kind: "reply", Summary: "编的引用" + strings.Repeat("很长", 30)}, {Kind: "text", Text: strings.Repeat("编的外层很长的话。", 20)}, {Kind: "image", Width: 600, Height: 400}}},
				{UID: "101", Name: "编的丙", At: t0, Segments: []api.DisplaySegment{{Kind: "forward", Ref: "inner", Forward: &api.ForwardBrief{Title: "编的乙的聊天记录", Count: 1, State: "ready", Lines: [3]string{"编的乙：" + strings.Repeat("摘要", 30)}}}}},
			}},
			"inner": {ID: "inner", State: "ready", Nodes: []api.ForwardNode{{UID: "200", Name: "编的乙", At: t0, Segments: []api.DisplaySegment{{Kind: "text", Text: "编的里层"}}}}},
		}}
		a = boxApp("local:r1", false, 0)
		boxMessages(a, "media")
		fq2 := &uiQueue{}
		a.reader, a.postFn = fr, fq2.post
		t.Cleanup(a.stopForward)
		tt = enter(a, 1030)
		a.openForward(api.EventRef{Conversation: a.keyOf(a.sel)}, "outer", nil)
		for range 3 {
			settleMem(fq2)
			boxFrames(tt)
		}
		out = append(out, boxScene{name("右栏转发详情"), w, 1030, tt})
		if pages {
			// 记忆页、运行页（各自带假后台）
			mh := newMemHarness(t, newDemoMemory(), w, 1030)
			out = append(out, boxScene{name("记忆页"), w, 1030, mh.tt})
			sf := &fakeStats{rep: usageWith(liveTask(ctlproto.TaskForeground, "前台回合", 3, 1200), liveTask("bg", "后台任务名字很长很长很长很长很长很长", 9, 90000))}
			for tab, tabName := range runTabNames {
				rh := newRunHarness(t, sf, sf, tab, w, 1030)
				out = append(out, boxScene{name("运行页" + tabName), w, 1030, rh.tt})
			}
			// 设置页（演示设置页和后台配置页）
			a = boxApp("local:r1", false, 4)
			out = append(out, boxScene{name("设置页"), w, 1030, shellTester(a, w, 1030)})
			a = boxApp("local:r1", false, 4)
			a.demoMode = false
			a.realTab = tabTools
			out = append(out, boxScene{name("设置页后台配置"), w, 1030, shellTester(a, w, 1030)})
		}
		// 矮窗口
		a = boxApp("local:r1", false, 0)
		boxMessages(a, "p-cmd")
		a.workbench.shell.st.Panes.SideOpen = true
		out = append(out, boxScene{name("矮窗口高560"), w, 560, enter(a, 560)})
	}
	return out
}

// boxWant 是各类场景里一定要在窗口里看到的字：场景没搭起来就红，不让审计在空窗口上白过。
var boxWant = []struct{ prefix, text string }{
	{"本地私聊-text", "这是一句很长"}, {"本地私聊-p-think", "想了 7 秒"}, {"本地私聊-p-cmd", "运行了 go test"},
	{"本地私聊-p-tool", "调用了 web_search"}, {"本地私聊-p-live", "正在运行 sleep 100"}, {"本地私聊-media", "查看转发消息"},
	{"引用卡6张", "引用 6：白羽"}, {"右栏转发详情", "编的外层很长的话"}, {"右栏上下文展开/宽2048", "工具执行 · read"},
	{"本地群聊长内容-text", "这是一句很长"}, {"QQ群长内容-text", "这是一句很长"},
}

func auditScene(t *testing.T, s boxScene, out *[]boxResult) {
	t.Helper()
	boxFrames(s.tt)
	if s.w >= 900 {
		for _, w := range boxWant {
			if strings.HasPrefix(s.name, w.prefix) && !hasSubstr(s.tt.Texts(), w.text) {
				t.Errorf("%s：窗口里没有 %q，场景没搭起来", s.name, w.text)
			}
		}
	}
	tree := uitest.Tree(s.tt)
	if c := tree.Find("composer"); strings.HasPrefix(s.name, "输入框长文字") && s.w >= 900 && (c == nil || c.Box.H < 150) {
		t.Errorf("%s：输入框没有被长文字撑高，场景没搭起来", s.name)
	}
	fs, skipped := uitest.Audit(tree, ui.Rect{W: float32(s.w), H: float32(s.h)})
	*out = append(*out, boxResult{Scene: s.name, Skipped: skipped, Findings: fs})
}

// 常驻：确定是错的那一类——子元素的框越过没有裁剪的父元素，内容会真的画到外面、压住别的东西。必须是 0。
// 其余几类（滚动内容超出视口、Clip 切掉、悬停才出现的透明操作条、文字框拉满、浮栏盖住滚动内容、模态层盖住下面的控件、
// 表格表头的 Clip）都是符合预期的做法，依据和数量见排查笔记；它们不在这里拦，只在 TestBoxAuditScenes 里出数。
func TestBoxNoChildOverflowsParent(t *testing.T) {
	var res []boxResult
	for _, s := range boxScenes(t, []int{1200, 480}, false) {
		auditScene(t, s, &res)
	}
	scenes := 0
	for _, r := range res {
		scenes++
		for _, f := range r.Findings {
			if f.Class == uitest.ClassOverParent {
				t.Errorf("%s：%v", r.Scene, f)
			}
		}
	}
	if scenes < 30 {
		t.Fatalf("只审计了 %d 个场景，场景表走空了", scenes)
	}
}

func TestBoxAuditScenes(t *testing.T) {
	dir := os.Getenv("BOX_AUDIT_OUT")
	if dir == "" {
		t.Skip("排查用：设 BOX_AUDIT_OUT 指向草稿目录才跑")
	}
	var res []boxResult
	for _, s := range boxScenes(t, []int{2048, 1200, 900, 700, 480}, true) {
		auditScene(t, s, &res)
	}
	data, _ := json.MarshalIndent(res, "", " ")
	if err := os.WriteFile(dir+"/scenes.json", data, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		t.Logf("%s：%d 条，跳过变换 %d", r.Scene, len(r.Findings), r.Skipped)
	}
}
