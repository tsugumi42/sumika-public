package app

import (
	"strings"

	"sumika-native/internal/turn"
)

// approvalRule 把审批配置写成她能读懂的一句话，放进本地私聊的场合提示词，让她知道真实规则。
// 只有 auto 模式和出厂说法不同：auto 下读文件、只读命令不问，工作目录和自动放行的目录里写改不问，别的仍弹审批卡。
func approvalRule(mode, workdir string, autoDirs []string) string {
	if mode != "auto" && mode != "yolo" {
		return turn.DefaultApprovalRule
	}
	var kept []string
	for _, d := range append([]string{workdir}, autoDirs...) {
		if d = strings.TrimSpace(d); d != "" {
			kept = append(kept, d)
		}
	}
	where := "工作目录"
	if len(kept) > 0 {
		where = strings.Join(kept, "、")
	}
	if mode == "yolo" {
		return "读文件、写文件、改文件、运行命令，只要看得清、只碰 " + where + " 里的东西就直接做，不用批准；写到别处、看不清的命令，会先请所有者在界面上批准。"
	}
	return "读文件、只读的命令直接做，不用批准；在 " + where + " 里写文件、改文件也直接做；别的会改东西的命令、写到别处的文件，会先请所有者在界面上批准。"
}
