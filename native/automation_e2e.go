//go:build e2e

package main

import (
	"crypto/sha256"
	"fmt"
	"os"

	_ "github.com/egoist/mygo/plugins/automation"
)

// 独立 socket 对应独立实例，巡视不能唤醒日用窗口或抢演示控制管道。
func automationName(name string, demo bool) (string, error) {
	path := os.Getenv("MYGO_AUTOMATION")
	if path == "" {
		return name, nil
	}
	if !demo {
		return "", fmt.Errorf("automation 构建设置 MYGO_AUTOMATION 时必须带 -demo")
	}
	switch mode := os.Getenv("SUMIKA_AUTOMATION_THEME"); mode {
	case "", "light", "night":
	default:
		return "", fmt.Errorf("SUMIKA_AUTOMATION_THEME 必须为空、light 或 night，得到 %q", mode)
	}
	return fmt.Sprintf("%s e2e %x", name, sha256.Sum256([]byte(path))), nil
}

func automationShell(s *shell) {
	if path := os.Getenv("MYGO_AUTOMATION"); path != "" {
		s.ctlPipe = fmt.Sprintf(`\\.\pipe\sumika-e2e-%x`, sha256.Sum256([]byte(path)))
		// 窄窗的主题控件本身可能越界；启动时选主题，巡视仍保留越界证据。
		s.a.initLook()
		if mode := os.Getenv("SUMIKA_AUTOMATION_THEME"); mode != "" {
			s.a.look.settings.Mode = mode
		}
		// 固定覆盖随机历史不一定出现的三种消息，不调用任何发送接口。
		cv, _ := mockConvOf("qq:group:1001")
		es := mockData.events(cv)
		for j, text := range []string{"自动巡视长消息：这是一段编造的中文，检查换行、长名称和窄窗口。\n第二行包含 ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789。", "[图片]", "这条编的消息已撤回"} {
			i := len(es) - 3 + j
			es[i].ActorKind, es[i].Origin, es[i].ActorID = "human", "member", "p0"
			es[i].Text, es[i].SegmentsJSON, es[i].ReplyTo = text, nil, ""
		}
		img := `{"segs":[{"type":"image","hash":"mock:3:320:180","width":320,"height":180}]}`
		es[len(es)-2].SegmentsJSON = &img
		es[len(es)-2].ReplyTo = es[len(es)-3].ID
		gone := `{"unsent":"已撤回"}`
		es[len(es)-1].SegmentsJSON = &gone
	}
}
