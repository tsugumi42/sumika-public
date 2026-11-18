package selfcheck

import (
	"os/exec"
	"strings"
	"testing"
)

// B2：自检包只读。它的依赖里不能有写库接口所在的包（store）、发送和渠道（channel、app、outlet）、回合（turn）、模型客户端（llm）。
// 读库只靠 Querier（只有 QueryContext）；app 用 store.OpenReadOnlyFile 打开只读连接再传进来。
func TestDepsHaveNoWriterOrSender(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps：%v", err)
	}
	deps := strings.Fields(string(out))
	sawSelf := false
	for _, d := range deps {
		if d == "sumika-native/internal/selfcheck" {
			sawSelf = true
		}
		for _, banned := range []string{"internal/store", "internal/channel", "internal/app", "internal/turn", "internal/llm", "internal/alert", "internal/approval", "internal/ctlserver"} {
			if strings.HasPrefix(d, "sumika-native/"+banned) {
				t.Errorf("selfcheck 依赖了 %s：自检只读只记，不许碰写库、发送、回合和通知的包", d)
			}
		}
	}
	if !sawSelf {
		t.Fatalf("go list 的结果里没有 selfcheck 自己，这个测试什么也没查：%v", deps)
	}
}
