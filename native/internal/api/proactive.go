package api

import (
	"context"

	"sumika-native/internal/ctlproto"
)

// ProactiveReader 是右栏“联系”标签的读接口（规格-主动联系 第十节）：一个角色的主动联系现状。
// 只读；调用方只在标签看得见时才调，读失败不影响聊天。
type ProactiveReader interface {
	ProactiveStatus(ctx context.Context, character string) (ctlproto.ProactiveStatus, error)
}

// ProactiveControl 是“联系”标签头的两个按钮：今天别找我（按默认时长别烦）和现在别再找了（关掉开着的线）。
// 身份由本机宿主绑定，不接受调用方给的时长、理由。
type ProactiveControl interface {
	ProactiveSnooze(ctx context.Context, character string) error
	ProactiveCloseThread(ctx context.Context, character string) error
}
