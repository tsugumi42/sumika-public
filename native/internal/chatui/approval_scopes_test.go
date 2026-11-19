package chatui

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// ADR-330：审批卡在“允许一次”之外按后台给的范围摆“这个对话里都允许”“记住，以后都允许”；没给就一个“更多”都不摆。

func scopeStep(scopes ...ApprovalScope) *Step {
	return &Step{ID: "t", Kind: StepCommand, State: StepRunning, Target: "派 reader 子任务", Approval: &Approval{State: ApprovalPending, Reason: "查三份资料", Remaining: time.Minute, Scopes: scopes, KindLabel: "「派 reader 子任务」"}}
}

func TestApprovalScopesMenu(t *testing.T) {
	for _, word := range []struct {
		scope ApprovalScope
		text  string
	}{{AllowConversation, "这个对话里都允许「派 reader 子任务」"}, {AllowAlways, "记住，以后都允许「派 reader 子任务」"}} {
		var got []string
		h := Host{T: Derive(DefaultSettings()), Column: 600, SelfChar: CharColors[0],
			OnApprove: func(_ string, sc ApprovalScope) { got = append(got, fmt.Sprint(sc)) }}
		tt := approvalTester(&h, scopeStep(AllowConversation, AllowAlways))
		if err := tt.Click("更多选择"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if !slices.Contains(tt.Menu(), "这个对话里都允许「派 reader 子任务」") || !slices.Contains(tt.Menu(), "记住，以后都允许「派 reader 子任务」") {
			t.Fatalf("菜单没摆两档：%v", tt.Texts())
		}
		if slices.Contains(tt.Menu(), "拒绝并说明原因…") {
			t.Fatal("宿主没给写原因的框，不该摆这一项")
		}
		if err := tt.ChooseMenuItem(word.text); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if fmt.Sprint(got) != fmt.Sprint([]string{fmt.Sprint(word.scope)}) {
			t.Errorf("点 %q 得到 %v", word.text, got)
		}
	}
}

func TestApprovalNoScopesNoMore(t *testing.T) {
	h := Host{T: Derive(DefaultSettings()), Column: 600, SelfChar: CharColors[0]}
	tt := approvalTester(&h, scopeStep())
	if _, ok := tt.Find("更多选择"); ok {
		t.Fatal("后台没给范围，不该摆做不到的选择")
	}
	// 只给“这个对话”：菜单里只有它
	tt = approvalTester(&h, scopeStep(AllowConversation))
	if err := tt.Click("更多选择"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !slices.Contains(tt.Menu(), "这个对话里都允许「派 reader 子任务」") || slices.Contains(tt.Menu(), "记住，以后都允许「派 reader 子任务」") {
		t.Fatalf("只该有一档：%v", tt.Texts())
	}
}

// 放行记录：这一步是哪条规则放行的，只在展开这一步时写一行；平时不占地方。
func TestApprovalRuleLineOnlyWhenOpen(t *testing.T) {
	h := Host{T: Derive(DefaultSettings()), Column: 600}
	steps := []*Step{{ID: "m", Kind: StepCommand, State: StepDone, Target: "mcp_call", Output: "ok", Approval: &Approval{State: ApprovalAuto, AutoReason: "记住的规则：「编的服务 的 编的工具」"}}}
	open := map[string]*bool{}
	tt := processTester(h, steps, open)
	if tt.HasText("记住的规则：「编的服务 的 编的工具」") {
		t.Fatalf("没展开就写了规则：%v", tt.Texts())
	}
	*open["m"] = true
	tt.Frame()
	tt.Frame()
	if !tt.HasText("记住的规则：「编的服务 的 编的工具」") {
		t.Fatalf("展开后没写这一步凭哪条规则放行：%v", tt.Texts())
	}
}

// 可选截图（SUMIKA_APPROVAL_SHOT=<目录>）：审批卡带范围菜单、放行记录，浅色和夜间。普通测试不写图。
func TestApprovalScopesShot(t *testing.T) {
	dir := os.Getenv("SUMIKA_APPROVAL_SHOT")
	if dir == "" {
		t.Skip("没设 SUMIKA_APPROVAL_SHOT，不截图")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	save := func(tt *ui.Tester, name string, r image.Rectangle) {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image().SubImage(r.Intersect(tt.Image().Bounds()))); err != nil {
			t.Fatal(err)
		}
	}
	for _, dark := range []bool{false, true} {
		suffix := "light"
		if dark {
			suffix = "night"
		}
		h := Host{T: Derive(DefaultSettings()), Column: 600, SelfChar: CharColors[0]}
		view := func(c *ui.Context) {
			ui.Column(c.Key("root")).Width(560).Padding(16).Gap(12).Background(h.T.C.ChatBg).Children(func() {
				h.ApprovalCard(c, ApprovalInfo{ID: "card", Title: "派子任务", Summary: "reader：查三份资料", Scopes: []ApprovalScope{AllowConversation, AllowAlways}, KindLabel: "「派 reader 子任务」"}, new(bool), c.Theme().Border, c.Theme().Surface)
			})
		}
		tt := ui.NewTester(view, 560, 260)
		tt.SetDark(dark)
		tt.SetScale(1.25)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		tt.Frame()
		tt.Frame()
		if err := tt.Click("更多选择"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		tt.Frame()
		save(tt, "审批卡-两档按钮-"+suffix+".png", image.Rect(0, 0, 700, 325))

		steps := []*Step{{ID: "m", Kind: StepCommand, State: StepDone, Target: "mcp_call 编的服务 / 编的工具", Output: "ok", Approval: &Approval{State: ApprovalAuto, AutoReason: "记住的规则：「编的服务 的 编的工具」"}}}
		open := map[string]*bool{"m": new(bool)}
		*open["m"] = true
		pt := ui.NewTester(func(c *ui.Context) {
			c.Root().Background(h.T.C.ChatBg)
			h.Process(c, "p", CharColors[0], steps, func(id string) *bool { return open[id] })
		}, 560, 220)
		pt.SetDark(dark)
		pt.SetScale(1.25)
		pt.SetPreferences(ui.Preferences{ReduceMotion: true})
		pt.Frame()
		pt.Frame()
		save(pt, "放行记录-"+suffix+".png", image.Rect(0, 0, 700, 275))
	}
}
