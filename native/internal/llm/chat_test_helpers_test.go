package llm

// 中立请求的测试构造：只有角色和文字的消息走后台风格（marshalNoEscape 的普通消息）。

import "sumika-native/internal/llm/chat"

func msg(role chat.Role, text string) chat.Message {
	return chat.Message{Role: role, Blocks: []chat.Block{chat.Text(text)}}
}

func chatReq(model string, msgs ...chat.Message) chat.Request {
	return chat.Request{Model: model, Style: chat.StyleBackground, Messages: msgs}
}

// simpleReq 是最小的一次调用（看门狗、解码这类测试只关心回复）
func simpleReq(model string) chat.Request {
	return chatReq(model, msg(chat.RoleUser, "x"))
}
