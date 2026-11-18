package chatui

import (
	"strings"
	"testing"
)

// 2026-11-06 晚上两族视觉验收确认、修掉的问题，每条放回旧代码都会红。

// 没执行的（被拒、过期、撤回）：命令卡写"不允许：/ 没运行"、改文件卡写"没改"，右边不画停止图标（停止图标只给跑起来又被停下的）。
func TestNotRunWording(t *testing.T) {
	h := Host{T: Derive(DefaultSettings()), Column: 720}
	steps := []*Step{
		{ID: "c2", Kind: StepCommand, State: StepDenied, Target: "rm /", Approval: &Approval{State: ApprovalDenied}},
		{ID: "c3", Kind: StepCommand, State: StepStopped, Target: "make", Approval: &Approval{State: ApprovalExpired}},
		{ID: "e2", Kind: StepEdit, State: StepStopped, Target: "b.md", Approval: &Approval{State: ApprovalRevoked}},
	}
	tt := processTester(h, steps, map[string]*bool{})
	all := strings.Join(tt.Texts(), "\n")
	for _, want := range []string{"不允许：rm /", "没运行 make", "没改 b.md"} {
		if !strings.Contains(all, want) {
			t.Errorf("没看到 %q：%q", want, all)
		}
	}
	for _, bad := range []string{"停了 make", "改了 b.md", "停了"} {
		if strings.Contains(all, bad) {
			t.Errorf("没执行的卡上出现了 %q", bad)
		}
	}
	// 真跑起来又被停下的还是"停了"加停止图标
	tt = processTester(h, []*Step{{ID: "c", Kind: StepCommand, State: StepStopped, Target: "make"}}, map[string]*bool{})
	if !tt.HasText("停了 make") || !tt.HasText("停了") {
		t.Errorf("跑起来被停下的头部不对：%v", tt.Texts())
	}
}
