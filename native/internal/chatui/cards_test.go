package chatui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func manyLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "第 %d 行\n", i)
	}
	return b.String()
}

func cardTester(h Host, st *Step, open map[string]*bool) *ui.Tester {
	return processTester(h, []*Step{st}, open)
}

// 命令卡各状态：运行中展开、只看最后 6 行、转圈；完成收起、只剩用时；失败展开、角标 + 退出码；停下是停止图标；点头部翻转展开。
func TestCommandCard(t *testing.T) {
	tk := Derive(DefaultSettings())
	h := Host{T: tk, Column: 600}
	st := &Step{ID: "c", Kind: StepCommand, State: StepRunning, Target: "go test ./...", Output: manyLines(10), Seconds: 3}
	open := map[string]*bool{}
	tt := cardTester(h, st, open)
	if !tt.HasText("正在运行 go test ./...") {
		t.Fatalf("运行中头部不对：%v", tt.Texts())
	}
	all := strings.Join(tt.Texts(), "\n")
	if !strings.Contains(all, "第 10 行") || strings.Contains(all, "第 4 行") {
		t.Errorf("运行中应该只显示最后 6 行：%q", all)
	}
	st.State = StepDone
	tt.Frame()
	tt.Frame()
	if !tt.HasText("运行了 go test ./...") || !tt.HasText("3 秒") {
		t.Errorf("完成后头部不对：%v", tt.Texts())
	}
	if strings.Contains(strings.Join(tt.Texts(), "\n"), "第 10 行") {
		t.Error("完成后应该收起")
	}
	*open["c"] = true
	tt.Frame()
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), "第 4 行") {
		t.Error("点开以后应该能看到全部输出")
	}
	*open["c"] = false
	st.State, st.ExitCode, st.HasExit = StepFailed, 2, true
	tt.Frame()
	tt.Frame()
	if !tt.HasText("退出码 2") || tree(tt).find("badge") == nil {
		t.Errorf("失败应该有角标和退出码：%v", tt.Texts())
	}
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), "第 10 行") {
		t.Error("失败应该默认展开")
	}
	st.State = StepStopped
	tt.Frame()
	tt.Frame()
	if !tt.HasText("停了 go test ./...") {
		t.Errorf("停下后头部不对：%v", tt.Texts())
	}
}

// 改文件卡：头部 +A −B；完成收起；等批准时头部"想改"、diff 展开。
func TestEditCard(t *testing.T) {
	tk := Derive(DefaultSettings())
	h := Host{T: tk, Column: 600}
	st := &Step{ID: "e", Kind: StepEdit, State: StepDone, Target: "notes/a.md", Added: 3, Removed: 1, Diff: "@@ -1 +1 @@\n-旧的一行\n+新的一行\n"}
	open := map[string]*bool{}
	tt := cardTester(h, st, open)
	if !tt.HasText("改了 notes/a.md") || !tt.HasText("+3 −1") {
		t.Fatalf("改文件卡头部不对：%v", tt.Texts())
	}
	if tt.HasText("+新的一行") {
		t.Error("完成后应该收起")
	}
	// diff 行的字色用压在叠底上解出来的 OkText / DangerText，不用普通的 Ok / Danger（那两个在叠底上不到 4.5）
	*open["e"] = true
	tt.Frame()
	tt.Frame()
	ink := func(c ui.Color) [4]uint8 { return [4]uint8{c.R, c.G, c.B, c.A} }
	for key, want := range map[string]ui.Color{"1": tk.C.DangerText, "2": tk.C.OkText} {
		n := tree(tt).find("lines")
		if n == nil || len(n.Children) < 3 {
			t.Fatal("找不到 diff 行")
		}
		i := map[string]int{"1": 1, "2": 2}[key]
		if got := n.Children[i].Ink; got != ink(want) {
			t.Errorf("diff 第 %s 行字色 %v，期望 %v", key, got, ink(want))
		}
	}
	*open["e"] = false
	st.State, st.Approval = StepRunning, &Approval{State: ApprovalPending, Reason: "补一行说明"}
	tt.Frame()
	tt.Frame()
	if !tt.HasText("想改 notes/a.md") || !tt.HasText("+新的一行") {
		t.Errorf("等批准时应该写想改、展开 diff：%v", tt.Texts())
	}
}

// diff 行上的字压在同色叠底上也要过 4.5（浅深、有无壁纸）。
func TestDiffContrast(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, wall := range []bool{false, true} {
			s := DefaultSettings()
			s.Dark, s.Wallpaper = dark, wall
			tk := Derive(s)
			c := tk.C
			base := over(c.Hover, float64(c.Hover.A)/255, c.Bubble) // 卡片里的凹块：叠色压在卡片底上
			for _, pair := range []struct {
				what     string
				ink, tin ui.Color
			}{{"加的行", c.OkText, c.Ok}, {"减的行", c.DangerText, c.Danger}} {
				bg := over(pair.tin, float64(c.DiffTint), base)
				if r := Contrast(pair.ink, bg); r < MinTextContrast {
					t.Errorf("深=%v 壁纸=%v %s 对比 %.2f < 4.5", dark, wall, pair.what, r)
				}
			}
		}
	}
}

func approvalTester(h *Host, st *Step) *ui.Tester {
	view := func(c *ui.Context) {
		c.Root().Background(h.T.C.ChatBg)
		ui.Column(c).Gap(20).Padding(20).Children(func() {
			ui.TextInputBase(c.Key("input"), new(string)).Label("输入框").Width(200).Height(30)
			h.Process(c, "p", CharColors[0], []*Step{st}, nil)
		})
	}
	tt := ui.NewTester(view, 700, 600)
	tt.Frame()
	tt.Frame()
	return tt
}

// 审批键盘：焦点在块上 Enter 允许、Esc 拒绝；出现后 500ms 内 Enter 不算；焦点不在块里不算；焦点在"拒绝"上 Enter 按拒绝。
func TestApprovalKeyboard(t *testing.T) {
	tk := Derive(DefaultSettings())
	var got []string
	h := Host{T: tk, Column: 600, SelfChar: CharColors[0],
		OnApprove: func(id string, sc ApprovalScope) { got = append(got, fmt.Sprintf("允许%d", sc)) },
		OnDeny:    func(id, why string) { got = append(got, "拒绝") }}
	newStep := func(shown time.Time) *Step {
		return &Step{ID: "c", Kind: StepCommand, State: StepRunning, Target: "rm -rf build", Approval: &Approval{State: ApprovalPending, Reason: "清掉旧的构建", Remaining: 272 * time.Second, Shown: shown}}
	}
	focusBlock := func(tt *ui.Tester) {
		r, ok := tt.Find("清掉旧的构建")
		if !ok {
			t.Fatal("找不到审批原因")
		}
		tt.ClickAt(r.X+2, r.Y+r.H/2) // 点块里不是按钮的地方，焦点落在审批块上
		tt.Frame()
	}
	// 1. 焦点在块上：Enter 允许一次
	got = nil
	tt := approvalTester(&h, newStep(time.Time{}))
	if !tt.HasText("允许一次") || !tt.HasText("拒绝") { // 剩余时间在过程气泡的摘要行（ADR-287），不在审批块里
		t.Fatalf("审批块不全：%v", tt.Texts())
	}
	focusBlock(tt)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if fmt.Sprint(got) != "[允许0]" {
		t.Errorf("焦点在块上 Enter：%v，期望允许一次", got)
	}
	// 2. Esc 拒绝
	got = nil
	tt = approvalTester(&h, newStep(time.Time{}))
	focusBlock(tt)
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if fmt.Sprint(got) != "[拒绝]" {
		t.Errorf("焦点在块上 Esc：%v，期望拒绝", got)
	}
	// 3. 出现后 500ms 内 Enter 不算
	got = nil
	tt = approvalTester(&h, newStep(time.Now().Add(time.Hour))) // 出现时刻在"现在"之后，等于刚出现
	focusBlock(tt)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if len(got) != 0 {
		t.Errorf("刚出现就认了 Enter：%v", got)
	}
	// 4. 焦点在输入框里：Enter、Esc 都不算
	got = nil
	tt = approvalTester(&h, newStep(time.Time{}))
	if err := tt.Click("输入框"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if len(got) != 0 {
		t.Errorf("焦点在输入框里也触发了审批：%v", got)
	}
	// 5. 焦点在"拒绝"按钮上：Enter 按的是拒绝
	got = nil
	tt = approvalTester(&h, newStep(time.Time{}))
	focusBlock(tt)
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if fmt.Sprint(got) != "[拒绝]" {
		t.Errorf("焦点在拒绝按钮上 Enter：%v，期望拒绝", got)
	}
}

// 审批结果：决定后按钮没有了，行尾一个小图标加两三个字；提交中按钮禁用、不再触发。
func TestApprovalStates(t *testing.T) {
	tk := Derive(DefaultSettings())
	fired := 0
	h := Host{T: tk, Column: 600, SelfChar: CharColors[0], OnApprove: func(string, ApprovalScope) { fired++ }, OnDeny: func(string, string) { fired++ }}
	for st, want := range map[ApprovalState]string{ApprovalAllowed: "已允许", ApprovalDenied: "已拒绝", ApprovalExpired: "超时", ApprovalRevoked: "已撤回"} {
		step := &Step{ID: "c", Kind: StepCommand, State: StepDone, Target: "ls", Approval: &Approval{State: st}}
		tt := approvalTester(&h, step)
		if !tt.HasText(want) || tt.HasText("允许一次") {
			t.Errorf("状态 %d 应该显示 %q、没有按钮：%v", st, want, tt.Texts())
		}
	}
	step := &Step{ID: "c", Kind: StepCommand, State: StepRunning, Target: "ls", Approval: &Approval{State: ApprovalSubmitting}}
	tt := approvalTester(&h, step)
	_ = tt.Click("拒绝")
	tt.Frame()
	if fired != 0 {
		t.Error("提交中还能点按钮")
	}
}

// 设置"等宽字连字"：开着（默认）不设特性，关了以后命令、diff 关掉连字；命令输出不管设置一律不连写。
func TestLigatureSetting(t *testing.T) {
	for _, on := range []bool{true, false} {
		s := DefaultSettings()
		s.Ligatures = on
		h := Host{T: Derive(s), Column: 720}
		steps := []*Step{
			{ID: "a", Kind: StepCommand, State: StepFailed, Target: "a === b", Output: "x => y", HasExit: true, ExitCode: 1},
			{ID: "b", Kind: StepEdit, State: StepRunning, Target: "a.md", Diff: "-x\n+y\n", Approval: &Approval{State: ApprovalPending}},
		}
		want := ""
		if !on {
			want = "liga=0,calt=0"
		}
		seen := 0
		var check func(n *node)
		check = func(n *node) {
			if n.Text == "a === b" || n.Text == "x => y" || n.Text == "-x" || n.Text == "+y" {
				seen++
				w := want
				if n.Text == "x => y" {
					w = "liga=0,calt=0" // 命令输出是程序原样的字，不管设置一律不连写
				}
				if n.Features != w {
					t.Errorf("连字=%v %q 的特性 %q，期望 %q", on, n.Text, n.Features, w)
				}
			}
			for _, ch := range n.Children {
				check(ch)
			}
		}
		check(tree(processTester(h, steps, map[string]*bool{})))
		if seen != 4 {
			t.Errorf("连字=%v 只查到 %d 处等宽字", on, seen)
		}
	}
}
