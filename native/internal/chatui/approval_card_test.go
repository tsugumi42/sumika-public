package chatui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 2026-11-15 审批卡改版：只有一步在等批准时，过程气泡压成一张卡——摘要行（图标、想做什么、倒计时）、命令或差异一块凹块、理由、右对齐按钮。
// 命令和图标各只出现一次；按钮直接在气泡上、圆角是单行控件的胶囊；决定过的结果收成头部行尾的小图标加两三个字。

func askHost(onApprove func(string, ApprovalScope), onDeny func(string, string)) Host {
	return Host{T: Derive(DefaultSettings()), Column: 720, NoEnter: true, SelfChar: CharColors[0], OnApprove: onApprove, OnDeny: onDeny,
		StepOpen: func(string) *bool { return new(bool) }}
}

func askStep(shown time.Time) *Step {
	return &Step{ID: "c", Kind: StepCommand, State: StepRunning, Target: "rm -rf build",
		Approval: &Approval{State: ApprovalPending, Reason: "清掉旧的构建", Cause: "雾栞", Remaining: 272 * time.Second, Shown: shown}}
}

func countText(tt *ui.Tester, sub string) int {
	n := 0
	for _, s := range tt.Texts() {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

// 只有一步在等批准：命令原文只出现一次、"想运行"只在摘要行；审批容器没有底色和内距（不是凹块）；按钮是胶囊。
func TestAskCardLayers(t *testing.T) {
	for _, edit := range []bool{false, true} {
		h := askHost(nil, nil)
		st := askStep(time.Time{})
		if edit {
			st = &Step{ID: "e", Kind: StepEdit, State: StepRunning, Target: "notes/a.md", Diff: "-旧的一行\n+新的一行\n", Approval: &Approval{State: ApprovalPending, Reason: "补一行", Remaining: time.Minute}}
		}
		tt := msgsTester(h, []*Msg{procMsg("p", 0, st)}, 900)
		root := tree(tt)
		b := root.find("p").find("bubble")
		if edit {
			if countText(tt, "notes/a.md") != 1 || countText(tt, "想改") != 1 || !tt.HasText("+新的一行") {
				t.Errorf("改文件：路径应只出现一次（摘要行）、差异在凹块里：%v", tt.Texts())
			}
		} else if countText(tt, "rm -rf build") != 1 || countText(tt, "想运行") != 1 || !tt.HasText("想运行命令") {
			t.Errorf("命令：原文应只出现一次（终端块）、摘要行只写想运行命令：%v", tt.Texts())
		}
		if tt.HasText("等你批准") || !tt.HasText("4:32") && !edit {
			t.Errorf("摘要行：不再写等你批准，倒计时在右边：%v", tt.Texts())
		}
		ap := b.find("approval")
		if ap == nil {
			t.Fatal("找不到审批容器")
		}
		if ap.Pad != [4]float32{} || ap.Radius != [4]float32{} {
			t.Errorf("审批容器不该是凹块（有内距 %v、圆角 %v）", ap.Pad, ap.Radius)
		}
		for _, key := range []string{"deny", "allow", "more"} {
			btn := ap.find(key)
			if btn == nil {
				if key == "more" {
					continue // 后台没给别的范围时没有"⋯"
				}
				t.Fatalf("找不到按钮 %s", key)
			}
			for _, r := range btn.Radius {
				if !near(r, h.T.HSm/2) {
					t.Errorf("按钮 %s 圆角 %.2f，应是单行控件的胶囊 %.2f", key, r, h.T.HSm/2)
				}
			}
		}
		if allow := ap.find("allow"); !near(allow.Box.X+allow.Box.W, ap.Box.X+ap.Box.W) {
			t.Errorf("按钮应靠右：允许一次右边 %.1f，卡右边 %.1f", allow.Box.X+allow.Box.W, ap.Box.X+ap.Box.W)
		}
	}
}

// 新卡的键盘和旧写法一样：焦点在卡上 Enter 允许一次、Esc 拒绝；刚出现 500ms 内 Enter 不算；焦点在"拒绝"上 Enter 按拒绝；提交中按钮不响应。
func TestAskCardKeyboard(t *testing.T) {
	var got []string
	h := askHost(func(string, ApprovalScope) { got = append(got, "允许") }, func(string, string) { got = append(got, "拒绝") })
	focus := func(tt *ui.Tester) {
		r, ok := tt.Find("清掉旧的构建")
		if !ok {
			t.Fatal("找不到理由")
		}
		tt.ClickAt(r.X+2, r.Y+r.H/2)
		tt.Frame()
	}
	cases := []struct {
		name  string
		shown time.Time
		keys  []ui.Key
		want  string
	}{
		{"Enter 允许", time.Time{}, []ui.Key{ui.KeyEnter}, "[允许]"},
		{"Esc 拒绝", time.Time{}, []ui.Key{ui.KeyEscape}, "[拒绝]"},
		{"刚出现 Enter 不算", time.Now().Add(time.Hour), []ui.Key{ui.KeyEnter}, "[]"},
		{"Tab 到拒绝再 Enter", time.Time{}, []ui.Key{ui.KeyTab, ui.KeyEnter}, "[拒绝]"},
	}
	for _, c := range cases {
		got = nil
		tt := msgsTester(h, []*Msg{procMsg("p", 0, askStep(c.shown))}, 900)
		focus(tt)
		for _, k := range c.keys {
			tt.Key(0, k)
			tt.Frame()
		}
		if fmt.Sprint(got) != c.want {
			t.Errorf("%s：%v，期望 %s", c.name, got, c.want)
		}
	}
	got = nil
	busy := askStep(time.Time{})
	busy.Approval.State = ApprovalSubmitting
	tt := msgsTester(h, []*Msg{procMsg("p", 0, busy)}, 900)
	_ = tt.Click("拒绝")
	tt.Frame()
	if len(got) != 0 || !tt.HasText("想运行命令") || !tt.HasText("4:32") {
		t.Errorf("提交中：按钮不该响应，摘要行和等批准时是同一张卡：%v %v", got, tt.Texts())
	}
}

// 决定过的：气泡里不再有审批块（没有灰底长条）；结果是头部行尾的小图标加两三个字，拒绝用危险色、其余第三级色；没执行的一行命令不在下面再画一遍。
func TestApprovalResultChip(t *testing.T) {
	h := askHost(nil, nil)
	h.StepOpen = func(string) *bool { b := true; return &b } // 展开：看里面的行
	ink := func(c ui.Color) [4]uint8 { return [4]uint8{c.R, c.G, c.B, c.A} }
	for _, c := range []struct {
		state ApprovalState
		step  StepState
		text  string
		ink   ui.Color
	}{
		{ApprovalAllowed, StepDone, "已允许", h.T.C.Text3},
		{ApprovalDenied, StepDenied, "已拒绝", h.T.C.DangerText},
		{ApprovalExpired, StepStopped, "超时", h.T.C.Text3},
		{ApprovalRevoked, StepStopped, "已撤回", h.T.C.Text3},
	} {
		st := &Step{ID: "c", Kind: StepCommand, State: c.step, Target: "rm -rf build", Seconds: 1, Approval: &Approval{State: c.state, Reason: "清掉旧的构建"}}
		// 两步：第一步让气泡能展开着看；单步时也一样（下面再试）
		for _, steps := range [][]*Step{{st}, {{ID: "r", Kind: StepRead, State: StepDone, Target: "a.md"}, st}} {
			m := procMsg("p", 0, steps...)
			tt := msgsTester(h, []*Msg{m}, 900)
			_ = tt.Click("过程摘要") // 收起的点开（等批准的才自动展开）
			tt.Frame()
			tt.Frame()
			root := tree(tt)
			var chip *node
			var walk func(n *node)
			walk = func(n *node) {
				if n.Text == c.text {
					chip = n
				}
				for _, ch := range n.Children {
					walk(ch)
				}
			}
			walk(root.find("p"))
			if chip == nil {
				t.Errorf("状态 %d（%d 步）找不到结果 %q：%v", c.state, len(steps), c.text, tt.Texts())
				continue
			}
			if chip.Ink != ink(c.ink) {
				t.Errorf("状态 %d 结果 %q 的颜色 %v，期望 %v", c.state, c.text, chip.Ink, ink(c.ink))
			}
			if root.find("p").find("approval") != nil {
				t.Errorf("状态 %d 决定过了还画着审批块", c.state)
			}
			if tt.HasText("允许一次") {
				t.Errorf("状态 %d 决定过了还有按钮", c.state)
			}
			if c.state == ApprovalDenied && len(steps) == 1 && countText(tt, "rm -rf build") != 1 {
				t.Errorf("被拒的一行命令只该出现一次（头部），不在下面再画终端块：%v", tt.Texts())
			}
		}
	}
}

// 只有一步、气泡收着：决定过的结果在摘要行行尾（不展开也分得出拒绝、超时、撤回）；多步时摘要写总数，行尾不放。
func TestApprovalResultInCollapsedSummary(t *testing.T) {
	h := askHost(nil, nil)
	for state, want := range map[ApprovalState]string{ApprovalAllowed: "已允许", ApprovalDenied: "已拒绝", ApprovalExpired: "超时", ApprovalRevoked: "已撤回"} {
		st := &Step{ID: "c", Kind: StepCommand, State: StepStopped, Target: "make", Approval: &Approval{State: state}}
		tt := msgsTester(h, []*Msg{procMsg("p", 0, st)}, 900)
		sum := tree(tt).find("p").find("summary")
		if sum == nil || !subtreeHas(sum, want) {
			t.Errorf("状态 %d：收着的摘要行行尾应有 %q：%v", state, want, tt.Texts())
		}
	}
	two := []*Step{{ID: "r", Kind: StepRead, State: StepDone, Target: "a.md"}, {ID: "c", Kind: StepCommand, State: StepDenied, Target: "rm /", Approval: &Approval{State: ApprovalDenied}}}
	if sum := tree(msgsTester(h, []*Msg{procMsg("p", 0, two...)}, 900)).find("p").find("summary"); subtreeHas(sum, "已拒绝") {
		t.Error("多步时摘要行不该挂某一步的结果")
	}
}
