package app

import (
	"context"
	"errors"
	"strings"

	"sumika-native/internal/api"
	"sumika-native/internal/history"
)

// resolveLocalParts 在桌面入口核对分块输入（规格-引用）：每个引用块的目标消息必须在这个会话里、她看得到，附件要有可核对的来源。
// 调用前会话所有权已经由 localCharacter / localInputScope 校验；任何一块不合格整条不发，错误写明第几块，不悄悄丢掉。
// 选中的文字不在原文里时不拒绝，退回原文开头，日志只记会话和事件 id，不记正文。
// 返回的 text 是各块文字连起来的样子；只有一个没有引用、没有附件的普通块时 parts 为 nil，与旧的纯文字输入完全一样。
func (r *DesktopReader) resolveLocalParts(ctx context.Context, k api.ConversationKey, in []api.LocalPart) ([]history.Part, string, error) {
	if len(in) == 0 {
		return nil, "", errors.New("消息不能为空")
	}
	// 单个普通块没有文字：照旧报"消息不能为空"（ResolveParts 的"第 N 块"提示只用于多块）
	if len(in) == 1 && in[0].Quote == nil && len(in[0].Attachments) == 0 && strings.TrimSpace(in[0].Text) == "" {
		return nil, "", errors.New("消息不能为空")
	}
	parts, text, err := r.Service.ResolveParts(ctx, k, in)
	if err != nil {
		return nil, "", err
	}
	if parts == nil && strings.TrimSpace(text) == "" {
		return nil, "", errors.New("消息不能为空")
	}
	for _, p := range parts {
		if q := p.Quote; q != nil && q.Fallback && r.desk.app.logf != nil {
			r.desk.app.logf("引用：选中的文字不在原文里，退回原文开头（会话 %s，事件 %s）", k.Conversation, q.ID)
		}
	}
	return parts, text, nil
}
