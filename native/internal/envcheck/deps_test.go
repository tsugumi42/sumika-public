package envcheck

import (
	"os/exec"
	"strings"
	"testing"
)

// 环境清单只吃调用方注入的普通值：不能反过来 import 读库、宿主、回合、模型、发送、审批和自检的包，
// 否则后台没开时清单也生成不出来（规格-环境自检“禁止的偷懒写法”）。
func TestDepsStayPure(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps：%v", err)
	}
	deps := strings.Fields(string(out))
	sawSelf := false
	for _, d := range deps {
		if d == "sumika-native/internal/envcheck" {
			sawSelf = true
		}
		for _, banned := range []string{
			"internal/store", "internal/app", "internal/turn", "internal/llm", "internal/channel",
			"internal/approval", "internal/ctlserver", "internal/secrets", "internal/policy", "internal/selfcheck",
		} {
			if strings.HasPrefix(d, "sumika-native/"+banned) {
				t.Errorf("envcheck 依赖了 %s：清单只能吃普通值，不能碰读库、宿主、回合、模型和自检", d)
			}
		}
	}
	if !sawSelf {
		t.Fatalf("go list 的结果里没有 envcheck 自己，这个测试什么也没查：%v", deps)
	}
}
