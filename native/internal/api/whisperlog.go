package api

import (
	"context"

	"sumika-native/internal/ctlproto"
)

// WhisperLogReader 是桌面入口的可选扩展：右侧栏“对话日志”（规格-群聊引用和路由 第十五节）。
// key 是来源会话（本地群聊、QQ 私聊、QQ 群）；日志只含指到这个会话里的悄悄话和她们的处理过程，聊天区的读取里没有它们。
// 值不可变；游标绑定会话，换了会话的游标报错。
type WhisperLogReader interface {
	// ReadWhisperLog 读一页，条目从早到晚；Next 非空时用它取更早的一页。limit 为 0 用默认页大小。
	ReadWhisperLog(ctx context.Context, key ConversationKey, cursor string, limit int) (ctlproto.LogPage, error)
	// ReadWhisperLogEntry 读一条的全文（点开时才读）；withThinking 为真才给思考原文。
	ReadWhisperLogEntry(ctx context.Context, key ConversationKey, id string, withThinking bool) (ctlproto.LogDetail, error)
	// WatchWhisperLog 订阅最新一页：先登记再读，之后每次有变化发一份新的快照（条目按 id 整页替换）。
	WatchWhisperLog(ctx context.Context, key ConversationKey) (Subscription[ctlproto.LogPage], error)
	// CancelWhisper 取消这个会话里一批悄悄话指给某位角色的那一份：排队中的移除，处理中的取消那一轮；返回取消了几份。
	// 批号不属于这个会话、这批没有指给这位角色都报错。
	CancelWhisper(ctx context.Context, key ConversationKey, batch, character string) (int, error)
}
