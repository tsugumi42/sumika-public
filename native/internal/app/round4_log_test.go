package app

import (
	"context"
	"fmt"
	"strings"
	"sumika-native/internal/channel/types"
	"testing"
)

// 两种通知、三个自由文本字段都经真实适配器和接收端走到日志。
func TestOfflineNoticeLogsOmitPeerText(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{"bot_offline", "kicked_offline"} {
		for _, field := range []string{"message", "tag", "reason"} {
			t.Run(typ+"/"+field, func(t *testing.T) {
				t.Parallel()
				e := newEnv(t)
				a := e.open()
				e.run(a)
				c := e.peer.accept(t)
				e.peer.send(t, c, fmt.Sprintf(`{"post_type":"notice","notice_type":%q,"self_id":100,%q:"账号 100：编的踢下线原文"}`, typ, field))
				waitFor(t, "接收端记录被踢状态", func() bool { return e.logContains("QQ 被踢下线了") })
				for _, line := range e.logSnapshot() {
					if strings.Contains(line, "编的踢下线原文") || strings.Contains(line, "100") {
						t.Errorf("踢下线日志泄露原文或号码：%s", line)
					}
				}
				// 接收端也不能相信绕过适配器传来的自由文本。
				sinkOf(a).Status(context.Background(), types.Status{Kind: types.StatusKicked, Account: testAccount, Reason: "账号 100：编的踢下线原文"})
				logs := e.logSnapshot()
				if strings.Contains(logs[len(logs)-1], "编的踢下线原文") {
					t.Errorf("接收端直接状态泄露：%s", logs[len(logs)-1])
				}
			})
		}
	}
}
