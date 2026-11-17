package chatui

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// ADR-287：一轮里两段回复之间的过程折进一个能展开的气泡。

func TestSummarize(t *testing.T) {
	pending := func(kind StepKind, target string) *Step {
		return &Step{ID: target, Kind: kind, State: StepRunning, Target: target, Approval: &Approval{State: ApprovalPending, Remaining: 90 * time.Second}}
	}
	for _, c := range []struct {
		name  string
		steps []*Step
		want  string
		check func(s Summary) bool
	}{
		{"正在想", []*Step{{Kind: StepThink, State: StepRunning}}, "正在想", func(s Summary) bool { return s.Running && s.Thinking }},
		{"正在读", []*Step{{Kind: StepThink, State: StepDone, Seconds: 2}, {Kind: StepRead, State: StepRunning, Target: "a.md"}}, "正在读 a.md", func(s Summary) bool { return s.Running && !s.Thinking }},
		{"正在运行（多行命令只写第一行）", []*Step{{Kind: StepCommand, State: StepRunning, Target: "go test ./...\n其余"}}, "正在运行 go test ./...", nil},
		{"等批准优先于后面在跑的", []*Step{pending(StepCommand, "rm x"), {Kind: StepThink, State: StepRunning}}, "等你批准", func(s Summary) bool { return s.Pending && s.Remaining == 90*time.Second && s.Icon == "terminal" }},
		{"等批准的改文件", []*Step{pending(StepEdit, "a.md")}, "等你批准", func(s Summary) bool { return s.Pending && s.Icon == "pen" }},
		{"做完的总结", []*Step{
			{Kind: StepThink, State: StepDone, Seconds: 3}, {Kind: StepRead, State: StepDone}, {Kind: StepRead, State: StepDone}, {Kind: StepSearch, State: StepDone},
			{Kind: StepCommand, State: StepDone}, {Kind: StepEdit, State: StepDone},
		}, "想了 3 秒 · 读了 2 个文件、搜了 1 次 · 运行了 1 条命令 · 改了 1 个文件", func(s Summary) bool { return !s.Running && s.Icon == "pen" }},
		{"失败挂角标、不写字", []*Step{{Kind: StepCommand, State: StepFailed}}, "运行了 1 条命令", func(s Summary) bool { return s.Failed }},
		{"没执行的", []*Step{{Kind: StepCommand, State: StepDenied, Approval: &Approval{State: ApprovalDenied}}, {Kind: StepEdit, State: StepStopped, Approval: &Approval{State: ApprovalExpired}}},
			"1 条命令没运行 · 1 个文件没改", nil},
		{"想了一下", []*Step{{Kind: StepThink, State: StepDone}}, "想了一下", func(s Summary) bool { return s.Icon == "brain" }},
	} {
		s := Summarize(c.steps)
		if s.Text != c.want || (c.check != nil && !c.check(s)) {
			t.Errorf("%s：摘要 %q（%+v），期望 %q", c.name, s.Text, s, c.want)
		}
	}
}

func procMsg(id string, sec int, steps ...*Step) *Msg {
	return &Msg{ID: id, Sender: "r1", Name: "晴枝", Char: CharColors[1], At: time.Date(2026, 11, 6, 23, 0, sec, 0, time.Local), Steps: steps}
}

// 收起时只有摘要一行：看不到里面各段的字，宽度按摘要收窄，私聊里是一行胶囊（圆角 = 半高）。
func TestProcessBubbleCollapsed(t *testing.T) {
	tk := Derive(DefaultSettings())
	open := map[string]*bool{}
	h := Host{T: tk, Column: 720, NoEnter: true, StepOpen: func(id string) *bool {
		if open[id] == nil {
			open[id] = new(bool)
		}
		return open[id]
	}}
	m := procMsg("p", 0, &Step{ID: "t", Kind: StepThink, State: StepDone, Seconds: 3, Thought: "想过的正文"}, &Step{ID: "r", Kind: StepRead, State: StepDone, Target: "notes/a.md"})
	tt := msgsTester(h, []*Msg{m}, 900)
	all := strings.Join(tt.Texts(), "\n")
	if !strings.Contains(all, "想了 3 秒 · 读了 1 个文件") || strings.Contains(all, "notes/a.md") || strings.Contains(all, "想过的正文") {
		t.Fatalf("收起时应该只有摘要：%q", all)
	}
	b := tree(tt).find("p").find("bubble")
	if b.Box.W >= h.processWidth()-1 {
		t.Errorf("收起时宽 %.1f，应该按摘要收窄（过程最大宽 %.1f）", b.Box.W, h.processWidth())
	}
	if !near(b.Radius[0], b.Box.H/2) {
		t.Errorf("私聊里收起是一行，应该是胶囊：圆角 %v，高 %.1f", b.Radius, b.Box.H)
	}
	// 点摘要展开，能看到各段；再点收起
	if err := tt.Click("过程摘要"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), "notes/a.md") {
		t.Errorf("点了摘要没展开：%v", tt.Texts())
	}
	if err := tt.Click("过程摘要"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if strings.Contains(strings.Join(tt.Texts(), "\n"), "notes/a.md") {
		t.Error("再点没收起")
	}
}

// 有等批准的步骤时自动展开、点了也不收；剩余时间在摘要行右边。
func TestProcessBubblePendingOpen(t *testing.T) {
	tk := Derive(DefaultSettings())
	open := map[string]*bool{}
	h := Host{T: tk, Column: 720, NoEnter: true, StepOpen: func(id string) *bool {
		if open[id] == nil {
			open[id] = new(bool)
		}
		return open[id]
	}}
	m := procMsg("p", 0, &Step{ID: "c", Kind: StepCommand, State: StepRunning, Target: "rm -rf build", Approval: &Approval{State: ApprovalPending, Reason: "清理", Remaining: 272 * time.Second}})
	tt := msgsTester(h, []*Msg{m}, 900)
	if !tt.HasText("等你批准") || !tt.HasText("想运行 rm -rf build") || !tt.HasText("允许一次") || !tt.HasText("4:32") {
		t.Fatalf("等批准时应该展开、摘要行有剩余时间：%v", tt.Texts())
	}
	_ = tt.Click("过程摘要")
	tt.Frame()
	tt.Frame()
	if !tt.HasText("允许一次") {
		t.Error("等批准时点了摘要把审批收起来了")
	}
	// 摘要行的剩余时间在审批块外面（审批块里只有谁引起的、原因、按钮）
	if blk := tree(tt).find("approval"); blk != nil {
		var visit func(n *node) bool
		visit = func(n *node) bool {
			if n.Text == "4:32" {
				return true
			}
			for _, ch := range n.Children {
				if visit(ch) {
					return true
				}
			}
			return false
		}
		if visit(blk) {
			t.Error("剩余时间还在审批块里")
		}
	}
}

// "进行中展开过程"开着时（ADR-288）：进行中整段展开（想、读、命令都看得到），做完自动收成一行；
// 进行中手动收起的，做完以后也不会自己再展开（旧写法用同一个开关翻转，做完会弹开）。
func TestProcessBubbleLiveOpen(t *testing.T) {
	newHost := func(live bool) Host {
		open := map[string]*bool{}
		return Host{T: Derive(DefaultSettings()), Column: 720, NoEnter: true, LiveOpen: live, StepOpen: func(id string) *bool {
			if open[id] == nil {
				open[id] = new(bool)
			}
			return open[id]
		}}
	}
	toggle := func(tt *ui.Tester) {
		t.Helper()
		if err := tt.Click("过程摘要"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		tt.Frame()
	}
	finish := func(tt *ui.Tester, m *Msg) {
		for _, st := range m.Steps {
			st.State = StepDone
		}
		tt.Frame()
		tt.Frame()
	}
	// 展开没有：气泡里画了各段（processBody 的 steps 列）
	opened := func(tt *ui.Tester) bool { return tree(tt).find("p").find("steps") != nil }
	steps := func() []*Step {
		return []*Step{{ID: "t", Kind: StepThink, State: StepDone, Seconds: 2, Thought: "想过的正文"}, {ID: "r", Kind: StepRead, State: StepRunning, Target: "notes/a.md"}}
	}

	// 进行中展开，做完自己收起
	m := procMsg("p", 0, steps()...)
	tt := msgsTester(newHost(true), []*Msg{m}, 900)
	if !opened(tt) {
		t.Fatalf("开着设置时进行中应该整段展开（读文件也看得到）：%v", tt.Texts())
	}
	finish(tt, m)
	if opened(tt) || !tt.HasText("想了 2 秒 · 读了 1 个文件") {
		t.Errorf("做完应该自动收成一行：%v", tt.Texts())
	}

	// 进行中收起 → 做完仍收着 → 再点展开
	m = procMsg("p", 0, steps()...)
	tt = msgsTester(newHost(true), []*Msg{m}, 900)
	toggle(tt)
	if opened(tt) {
		t.Fatal("进行中点了收不起来")
	}
	finish(tt, m)
	if opened(tt) {
		t.Error("进行中收起的，做完以后自己又展开了")
	}
	toggle(tt)
	if !opened(tt) {
		t.Error("做完以后点摘要展不开")
	}

	// 正在想：思考正文边想边显示
	m = procMsg("p", 0, &Step{ID: "t", Kind: StepThink, State: StepRunning, Thought: "边想边写的正文"})
	if tt = msgsTester(newHost(true), []*Msg{m}, 900); !tt.HasText("边想边写的正文") {
		t.Errorf("正在想时应该看得到思考正文：%v", tt.Texts())
	}

	// 默认关：进行中也收着
	m = procMsg("p", 0, steps()...)
	if tt = msgsTester(newHost(false), []*Msg{m}, 900); opened(tt) {
		t.Errorf("设置关着时进行中不该展开：%v", tt.Texts())
	}
}

// 群聊：过程气泡就是她的一个气泡——名字在它顶上（后面的气泡不再写）、和后面的气泡相贴（左下、左上都是 6）、组内间距、头像在组末。
func TestProcessBubbleInGroup(t *testing.T) {
	tk := Derive(DefaultSettings())
	h := Host{T: tk, Column: 720, NoEnter: true, Group: true}
	p := procMsg("p", 0, &Step{ID: "r", Kind: StepRead, State: StepDone, Target: "a.md"})
	m := &Msg{ID: "m", Sender: "r1", Name: "晴枝", Char: CharColors[1], At: p.At.Add(time.Second), Text: "看完了。"}
	root := tree(msgsTester(h, []*Msg{p, m}, 900))
	pb, mb := root.find("p").find("bubble"), root.find("m").find("bubble")
	if pb.find("name") == nil || mb.find("name") != nil {
		t.Error("名字应该在过程气泡顶上、后面的气泡不再写")
	}
	if root.find("p").find("avatar") != nil || root.find("m").find("avatar") == nil {
		t.Error("头像应该只在组末那条")
	}
	if !near(pb.Radius[3], tk.RJoin) || !near(mb.Radius[0], tk.RJoin) {
		t.Errorf("过程气泡左下 %.1f、后面气泡左上 %.1f，应该都是相贴的 %.1f", pb.Radius[3], mb.Radius[0], tk.RJoin)
	}
	if gap := root.find("m").Box.Y - (root.find("p").Box.Y + root.find("p").Box.H); !near(gap, tk.GapInGroup) {
		t.Errorf("组内间距 %.1f，期望 %.1f", gap, tk.GapInGroup)
	}
	if !near(pb.Box.X, mb.Box.X) {
		t.Errorf("过程气泡左边 %.1f 和气泡 %.1f 没对齐", pb.Box.X, mb.Box.X)
	}
}

// 展开后里面的凹块（命令、输出、审批）离气泡左右边都是 pv、圆角同心；三档外观。
func TestProcessBubbleInnerGeometry(t *testing.T) {
	for _, s := range []Settings{DefaultSettings(), {Text: TextMin, Radius: RadiusMin, Density: DensityMin}, {Text: TextMax, Radius: RadiusMax, Density: DensityMax}} {
		tk := Derive(s)
		h := Host{T: tk, Column: 720, NoEnter: true}
		m := procMsg("p", 0, &Step{ID: "c", Kind: StepCommand, State: StepRunning, Target: "rm x", Output: "", Approval: &Approval{State: ApprovalPending, Reason: "清理", Remaining: time.Minute}})
		root := tree(msgsTester(h, []*Msg{m}, 900))
		b := root.find("p").find("bubble")
		name := fmt.Sprintf("外观 %v/%v/%v", s.Text, s.Radius, s.Density)
		for _, key := range []string{"command", "approval"} {
			blk := b.find(key)
			if blk == nil {
				t.Fatalf("%s 找不到 %s 块", name, key)
			}
			l, r := blk.Box.X-b.Box.X, b.Box.X+b.Box.W-(blk.Box.X+blk.Box.W)
			if !near(l, tk.BubblePadV) || !near(r, tk.BubblePadV) {
				t.Errorf("%s %s 块离气泡左 %.2f、右 %.2f，期望 %.2f", name, key, l, r, tk.BubblePadV)
			}
			if want := max(tk.Inner(b.Radius[1], tk.BubblePadV), tk.RInnerMin); math.Abs(float64(blk.Radius[1]-want)) > 0.5 {
				t.Errorf("%s %s 块右上圆角 %.2f，期望和气泡同心 %.2f", name, key, blk.Radius[1], want)
			}
		}
	}
}

var _ = ui.Rect{}

// 展开着进行中：摘要行写"进行中"、不转圈（下面那一行在转），收起时照旧写在干什么、转圈。
func TestProcessBubbleOpenRunningSummary(t *testing.T) {
	newHost := func(live bool) Host {
		return Host{T: Derive(DefaultSettings()), Column: 720, NoEnter: true, LiveOpen: live, StepOpen: func(string) *bool { return new(bool) }}
	}
	cmd := func() *Msg {
		return procMsg("p", 0, &Step{ID: "c", Kind: StepCommand, State: StepRunning, Target: "go test ./...", Output: "ok"})
	}
	tt := msgsTester(newHost(true), []*Msg{cmd()}, 900)
	sum := tree(tt).find("p").find("summary")
	if sum == nil || !subtreeHas(sum, "进行中") || subtreeHas(sum, "正在运行") || sum.find("spin") != nil {
		t.Errorf("展开着进行中摘要应该是'进行中'、不转圈：%v", tt.Texts())
	}
	tt = msgsTester(newHost(false), []*Msg{cmd()}, 900)
	if sum = tree(tt).find("p").find("summary"); !subtreeHas(sum, "正在运行 go test ./...") || sum.find("spin") == nil {
		t.Errorf("收起时摘要应该写在干什么、转圈：%v", tt.Texts())
	}
}

func subtreeHas(n *node, s string) bool {
	if strings.Contains(n.Text, s) {
		return true
	}
	for _, ch := range n.Children {
		if subtreeHas(ch, s) {
			return true
		}
	}
	return false
}

// 宽度按内容（主人 2026-11-07："平时不需要这么宽，只在需要的时候实时根据内容变宽"）：
// 展开后短内容比过程最大宽窄；长输出撑到最大宽为止、再长折行；进行中只变宽不变窄，做完重新按内容。
func TestProcessBubbleWidthFollowsContent(t *testing.T) {
	open := map[string]*bool{}
	h := Host{T: Derive(DefaultSettings()), Column: 720, NoEnter: true, LiveOpen: true, StepOpen: func(id string) *bool {
		if open[id] == nil {
			open[id] = new(bool)
		}
		return open[id]
	}}
	maxW := h.processWidth()
	st := &Step{ID: "c", Kind: StepCommand, State: StepRunning, Target: "go test", Output: "ok"}
	m := procMsg("p", 0, st)
	tt := msgsTester(h, []*Msg{m}, 900)
	width := func() float32 { tt.Frame(); tt.Frame(); return tree(tt).find("p").find("bubble").Box.W }
	short := width()
	if short >= maxW*0.7 {
		t.Fatalf("短内容展开宽 %.1f，应该明显比最大宽 %.1f 窄", short, maxW)
	}
	st.Output = strings.Repeat("长长的一行输出 ", 40)
	if w := width(); !near(w, maxW) {
		t.Errorf("长输出宽 %.1f，应该撑到最大宽 %.1f 再折行", w, maxW)
	}
	st.Output = "ok" // 进行中输出变短：不变窄
	if w := width(); !near(w, maxW) {
		t.Errorf("进行中内容变短后宽 %.1f，应该保持 %.1f 不跳", w, maxW)
	}
	st.State = StepDone // 做完：设置开着时自动收起，按摘要宽
	*open["p"] = true   // 手动展开看：重新按内容
	if w := width(); w >= maxW*0.7 || tree(tt).find("p").find("steps") == nil {
		t.Errorf("做完手动展开：宽 %.1f，应该是展开着、重新按内容收窄", w)
	}
}

// 失败角标只挂一个（主人 2026-11-07）：收起时挂在摘要行，展开后只挂在失败的那一行。
func TestProcessBubbleOneFailBadge(t *testing.T) {
	open := map[string]*bool{}
	h := Host{T: Derive(DefaultSettings()), Column: 720, NoEnter: true, StepOpen: func(id string) *bool {
		if open[id] == nil {
			open[id] = new(bool)
		}
		return open[id]
	}}
	m := procMsg("p", 0, &Step{ID: "r", Kind: StepRead, State: StepDone, Target: "a.md"}, &Step{ID: "c", Kind: StepCommand, State: StepFailed, Target: "go test", HasExit: true, ExitCode: 1, Output: "FAIL"})
	tt := msgsTester(h, []*Msg{m}, 900)
	badges := func() (all int, inSummary bool) {
		var visit func(n *node, sum bool)
		visit = func(n *node, sum bool) {
			sum = sum || n.Key == "summary" || strings.HasSuffix(n.Key, "/summary")
			if n.Label == "出错了" {
				all++
				inSummary = inSummary || sum
			}
			for _, ch := range n.Children {
				visit(ch, sum)
			}
		}
		visit(tree(tt).find("p"), false)
		return
	}
	if n, s := badges(); n != 1 || !s {
		t.Errorf("收起时应该只有摘要行一个角标：%d 个，在摘要行 %v", n, s)
	}
	if err := tt.Click("过程摘要"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if n, s := badges(); n != 1 || s {
		t.Errorf("展开后应该只有失败那一行一个角标：%d 个，在摘要行 %v", n, s)
	}
}

// 一轮只有一段时，点一次摘要就看到内容，里面不再有第二层折叠（主人 2026-11-09：“得展开两次”）。
func TestProcessBubbleSoleSegmentOpensDirectly(t *testing.T) {
	tk := Derive(DefaultSettings())
	think := &Step{ID: "t", Kind: StepThink, State: StepDone, Seconds: 3, Thought: "编造的思考正文"}
	for _, tc := range []struct {
		name  string
		steps []*Step
		want  string // 点一次后要看到的字
		count string // 这个字只能出现一次：不重复摘要
	}{
		{"思考", []*Step{think}, "编造的思考正文", "想了 3 秒"},
		{"读了几个", []*Step{{ID: "r1", Kind: StepRead, State: StepDone, Target: "编造甲.txt"}, {ID: "r2", Kind: StepRead, State: StepDone, Target: "编造乙.txt"}}, "编造乙.txt", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			open := map[string]*bool{}
			h := Host{T: tk, Column: 800, StepOpen: func(id string) *bool {
				if open[id] == nil {
					open[id] = new(bool)
				}
				return open[id]
			}}
			m := procMsg("p", 0, tc.steps...)
			tt := ui.NewTester(func(c *ui.Context) { h.Message(c, m, nil, nil) }, 900, 500)
			tt.Frame()
			if tt.HasText(tc.want) {
				t.Fatalf("没展开就显示了：%q", tt.Texts())
			}
			*h.StepOpen("p") = true
			tt.Frame()
			tt.Frame()
			if !hasSub(tt.Texts(), tc.want) {
				t.Fatalf("点一次摘要就该看到内容：%q", tt.Texts())
			}
			if tc.count != "" {
				n := 0
				for _, s := range tt.Texts() {
					if s == tc.count {
						n++
					}
				}
				if n != 1 {
					t.Fatalf("%q 出现 %d 次，里面又套了一层：%q", tc.count, n, tt.Texts())
				}
			}
		})
	}
}

func hasSub(list []string, part string) bool {
	for _, s := range list {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}

// 摘要行铺满气泡：悬停底和点击区到气泡边，气泡大小不变（主人 2026-11-09）。
func TestProcessSummaryFillsBubble(t *testing.T) {
	tk := Derive(DefaultSettings())
	step := &Step{ID: "r", Kind: StepRead, State: StepDone, Target: "编造.txt"}
	for _, group := range []bool{false, true} {
		h := Host{T: tk, Column: 720, NoEnter: true, Group: group}
		root := tree(msgsTester(h, []*Msg{procMsg("p", 0, step)}, 900))
		b := root.find("p").find("bubble")
		row := b.find("summary")
		if row == nil {
			t.Fatal("没有摘要行")
		}
		if !near(row.Box.X, b.Box.X) || !near(row.Box.W, b.Box.W) || !near(row.Box.Y+row.Box.H, b.Box.Y+b.Box.H) {
			t.Errorf("group=%v 摘要行 %+v 没铺满气泡 %+v", group, row.Box, b.Box)
		}
		want := tk.BubblePadV*2 + tk.BodyLine
		if group {
			want += tk.NameLine
		} else if !near(row.Box.Y, b.Box.Y) {
			t.Errorf("摘要行上边没到气泡边：%+v %+v", row.Box, b.Box)
		}
		if !near(b.Box.H, want) {
			t.Errorf("group=%v 气泡高 %.2f，还该是 %.2f", group, b.Box.H, want)
		}
	}
}

// 工具调用写清是哪个工具，不冒充读文件；有返回内容才能展开，展开看得到内容（主人 2026-11-09）。
func TestToolStepNamesTheTool(t *testing.T) {
	one := &Step{ID: "u", Kind: StepTool, Tool: "todo_write", State: StepDone, Target: "编造的待办", Output: "编造的返回内容"}
	if got := Summarize([]*Step{one}).Text; got != "调用了 todo_write" {
		t.Fatalf("一个工具要写名字：%q", got)
	}
	if got := Summarize([]*Step{one, {ID: "v", Kind: StepTool, Tool: "memory_forget", State: StepDone}}).Text; got != "调用了 2 个工具" {
		t.Fatalf("多个工具：%q", got)
	}
	if got := Summarize([]*Step{{ID: "w", Kind: StepTool, Tool: "read_link_detail", State: StepRunning}}).Text; got != "正在调用 read_link_detail" {
		t.Fatalf("进行中：%q", got)
	}
	tk := Derive(DefaultSettings())
	open := map[string]*bool{}
	h := Host{T: tk, Column: 800, StepOpen: func(id string) *bool {
		if open[id] == nil {
			open[id] = new(bool)
		}
		return open[id]
	}}
	m := procMsg("p", 0, one, &Step{ID: "x", Kind: StepThink, State: StepDone})
	tt := ui.NewTester(func(c *ui.Context) { h.Message(c, m, nil, nil) }, 900, 500)
	tt.Frame()
	*h.StepOpen("p") = true
	tt.Frame()
	tt.Frame()
	if !hasSub(tt.Texts(), "todo_write") || !hasSub(tt.Texts(), "编造的待办") {
		t.Fatalf("展开过程要看到工具名和参数：%q", tt.Texts())
	}
	if hasSub(tt.Texts(), "编造的返回内容") {
		t.Fatal("返回内容默认收着")
	}
	*h.StepOpen("u") = true
	tt.Frame()
	tt.Frame()
	if !hasSub(tt.Texts(), "编造的返回内容") {
		t.Fatalf("点开工具要看到返回内容：%q", tt.Texts())
	}
	for _, s := range tt.Texts() {
		if strings.Contains(s, "读了") {
			t.Fatalf("工具不该写成读了：%q", s)
		}
	}
}
