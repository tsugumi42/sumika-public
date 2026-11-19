package api

import (
	"context"
	"errors"
)

var ErrLocalGroupUnsupported = errors.New("当前数据源不支持本地群聊写入")
var ErrLocalGroupBusy = errors.New("这个群正在回复，请先停止或等这一轮结束")

// LocalGroupWriter 是桌面可选的写能力；不把后台写入变成所有 Reader 的必需方法。
// SendLocalGroup 等整轮结束才返回；已入库的输入和回复由原有订阅发布。
// Mentions 是选中的角色 ID，空时从正文解析独立的 @名字；身份始终由后台绑定为主人。
type LocalGroupWriter interface {
	CreateLocalGroup(context.Context, LocalGroupCreate) (ConversationView, error)
	SendLocalGroup(context.Context, LocalGroupSend) error
	StopLocalGroup(context.Context, ConversationKey) error
}
type LocalGroupCreate struct {
	Name       string
	Characters []string
}
type LocalGroupSend struct {
	Key      ConversationKey
	Text     string
	Mentions []string
	// Draft、Process 可以是 nil。一轮里每位成员开口前先来一次 Step 为 -1、Text 为空的 Draft，说这一位开始了。
	Draft   func(LocalDraft)
	Process func(turn string, steps []LocalProcessStep)
}
