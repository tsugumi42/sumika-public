package turn

import (
	"strings"
	"sumika-native/internal/kernel"
)

// 只从已验权的工作状态生成提醒；旧 steps 参数只在首次迁入工作序列时读取。
func todoReminder(state kernel.WorkState) string {
	if len(state.Pending) == 0 {
		return ""
	}
	return "当前工作还有没做完的：" + strings.Join(state.Pending, "；") + "。这次的话如果与它有关就继续，无关先回答。"
}
