package app

import (
	"strings"
	"testing"

	"sumika-native/internal/turn"
)

// 审批说法跟着配置走：manual 和没配用出厂说法，auto 写明哪些不问、在哪些目录里不问
func TestApprovalRuleFollowsConfig(t *testing.T) {
	t.Parallel()
	if got := approvalRule("manual", `D:\Work`, nil); got != turn.DefaultApprovalRule {
		t.Fatalf("manual 应是出厂说法：%q", got)
	}
	if got := approvalRule("", `D:\Work`, nil); got != turn.DefaultApprovalRule {
		t.Fatalf("没配应是出厂说法：%q", got)
	}
	got := approvalRule("auto", `D:\Work`, []string{`D:\Stack`, " "})
	for _, want := range []string{"只读的命令直接做", `D:\Work、D:\Stack`, "会先请所有者在界面上批准"} {
		if !strings.Contains(got, want) {
			t.Fatalf("auto 说法缺 %q：%q", want, got)
		}
	}
	if strings.Contains(got, "每次都会先请") {
		t.Fatalf("auto 不该再说每次都批准：%q", got)
	}
	// 免审批：写明只在看得清、只碰工作目录时不问；不再说"每次都批准"
	y := approvalRule("yolo", `D:\Work`, nil)
	for _, want := range []string{"运行命令", `D:\Work`, "看不清的命令，会先请所有者"} {
		if !strings.Contains(y, want) {
			t.Fatalf("yolo 说法缺 %q：%q", want, y)
		}
	}
	if strings.Contains(y, "每次都会先请") || y == got {
		t.Fatalf("yolo 说法不对：%q", y)
	}
}
