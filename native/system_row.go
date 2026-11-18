package main

import (
	"strings"

	"sumika-native/internal/api"
)

// 宿主写进会话的系统行（命令回复、目标达成、定期任务停了……）和目标续轮的那句输入，不是谁说的话，
// 在新界面里画成居中的灰字通知条，文字前面带一个来源小图标，不用气泡，也不和前后的气泡连成一组
// （规格-目标和循环 第二节“界面”）。来源名就是 actor_id（slash、goal、loop）；续轮输入是主人身份、origin=goal 的消息。
//
// 图标都取 chatui 图标表里现成的；认不出的来源用“信息”。
var systemRowIcons = map[string]string{"goal": "checks", "loop": "refresh", "slash": "command", "remind": "bell"}

// systemRowText 返回这条消息作为通知条要显示的文字和图标；不是系统行就原样返回 service，图标为空。
func systemRowText(v api.MessageView, service string) (text, icon string) {
	switch {
	case v.Service != "":
		return service, ""
	case v.Kind == "notice" && v.ActorKind == "system":
		src := strings.TrimPrefix(v.Origin, "system:")
		if src == "" || src == v.Origin {
			src = v.ActorID
		}
		icon = systemRowIcons[src]
		if icon == "" {
			icon = "info"
		}
		return rowTextOr(v.Text), icon
	case v.Origin == "goal" && v.Kind == "message":
		return rowTextOr(v.Text), systemRowIcons["goal"]
	}
	return service, ""
}

// rowTextOr 去掉首尾空白；空文字也要有一行（通知条靠文字非空才认），写“（空）”。
func rowTextOr(s string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return "（空）"
}
