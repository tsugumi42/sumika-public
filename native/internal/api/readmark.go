package api

import (
	"context"
	"errors"
)

// ErrReadMarkUnsupported 是这个数据源这一次不标已读：只看历史（archive）下约定不写库，本地会话没有已读记录，演示数据没有库。
// 不是出错：界面不重试、不提示，未读照旧。
var ErrReadMarkUnsupported = errors.New("这里不标已读")

// ReadMarker 是可选的标已读入口（界面第一次写库）。和 AvatarReader 一样没放进 Reader：实现很多，多一个方法大家都要改。
// 窗口拿到 Reader 后问一下它是不是 ReadMarker，不是就不标。
// upTo 是界面上看到的最新一条消息的时间（毫秒）；后台只往前记、不超过会话里真有的最新一条，所以传大了也不会把将来的消息算成已读。
// 记进去以后概览订阅自己重查，未读随即归零，调用方不用手动刷新。
// 不标（archive、本地会话）回 ErrReadMarkUnsupported；已经读到这里了或会话里没有消息是成功（什么也不用写）；写库失败回原错误。
type ReadMarker interface {
	MarkRead(ctx context.Context, key ConversationKey, upTo int64) error
}
