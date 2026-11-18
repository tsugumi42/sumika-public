// Package chat 是模型对话的中立类型：消息、内容块、请求、结果。
// 这里不出现线上字段名、转义和补位规则，也不 import internal/llm 或 internal/turn：
// 把这些类型变成某个协议的字节、再把回复变回来，是编解码器的事（规格-模型对话编解码 设计第 2 条）。
package chat

import (
	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
)

// Role 是一条消息的角色（中立词汇，不是线上字段）
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message 是一条消息。Omit 说这条不发给模型（出错、被停下的半截回复，由内核在转换时置上）
type Message struct {
	Role   Role
	Blocks []Block
	Omit   bool
}

// Block 是一块内容：Text、Image、Thinking、ToolCall、ToolResult 之一
type Block interface{ isBlock() }

// Text 是一块正文
type Text string

func (Text) isBlock() {}

// Image 是一张随消息带去的图；字段照 llm.Image
type Image struct {
	DataURL string
}

func (Image) isBlock() {}

// Thinking 是一块思考。Replay 是服务商要的回放数据，内核不看里面
type Thinking struct {
	Text   string
	Replay Replay
}

func (Thinking) isBlock() {}

// ToolCall 是一次工具调用；Arguments 是按 JS 规则解析好的参数对象
type ToolCall struct {
	ID        string
	Name      string
	Arguments jsjson.Value
}

func (ToolCall) isBlock() {}

// ToolResult 是一条工具结果；Images 是结果里带的图（编码器决定放在哪条消息里发）
type ToolResult struct {
	CallID  string
	Text    string
	IsError bool
	Images  []Image
}

func (ToolResult) isBlock() {}

// Replay 是思考的回放数据：Codec 认得的格式、哪个模型给的、原始字节。
// 内核只存和带回；Data 只有 Codec 自己能解析
type Replay struct {
	Codec string
	Model string
	Data  []byte
}

// SystemRole 是系统消息用哪个角色发。零值是 system
type SystemRole int

const (
	SystemRoleSystem SystemRole = iota
	SystemRoleDeveloper
)

// Style 是消息的编码风格。两种风格今天同时存在，字节不同，第一版不统一
type Style int

const (
	// StyleForeground 是前台回合的风格：手写 messages 数组、jsjson.Quote
	StyleForeground Style = iota
	// StyleBackground 是后台摘要、顾问审阅的风格：marshalNoEscape 处理普通角色加文字
	StyleBackground
)

// Request 是一次模型请求。开关字段照搬自旧的 llm.Request，语义由 P2-7 的预设决定
type Request struct {
	Model      string
	SystemRole SystemRole
	Messages   []Message
	// Tools 为 nil 时不发 tools；空但非 nil 时发 []
	Tools           []tools.Declaration
	MaxOutputTokens int64
	// Thinking 是思考档位（空不发；off 由编码器写成该协议的关闭写法）
	Thinking string
	// CacheKey 是服务商提示词缓存的稳定身份（空不发）
	CacheKey string
	// LongCache、NoStore 是缓存的两种服务商选项
	LongCache bool
	NoStore   bool
	// Headers 是 HTTP 头，编码器不写进 body（留给 transmission 和 P2-7）
	Headers map[string]string
	// SeeImages 说这次可以把工具结果里的图发给模型
	SeeImages bool
	Style     Style
}

// StripThinking 返回删掉全部 Thinking 块的新请求；原请求的 Messages 和 Blocks 不动。
// 故障切换换模型时用：跨模型的思考回放不通用
func StripThinking(req Request) Request {
	msgs := make([]Message, len(req.Messages))
	for i, m := range req.Messages {
		blocks := make([]Block, 0, len(m.Blocks))
		for _, block := range m.Blocks {
			if _, thinking := block.(Thinking); thinking {
				continue
			}
			blocks = append(blocks, block)
		}
		m.Blocks = blocks
		msgs[i] = m
	}
	req.Messages = msgs
	return req
}

// Usage 是一次调用的用量，口径照 llm.Usage
type Usage struct {
	Input, Output, CacheRead, CacheWrite, Reasoning int64
}

// StopReason 是回复为什么停
type StopReason string

const (
	StopReasonStop    StopReason = "stop"
	StopReasonLength  StopReason = "length"
	StopReasonToolUse StopReason = "toolUse"
	StopReasonError   StopReason = "error"
)

// Route 是网关实际落到的渠道和模型
type Route struct {
	Provider, Model string
}

// Result 是一次调用的结果；解码器产出它，内核只看中立字段
type Result struct {
	Blocks     []Block
	Usage      Usage
	StopReason StopReason
	Replay     Replay
	Route      Route
}
