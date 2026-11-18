package app

import (
	"context"
	"fmt"

	"sumika-native/internal/api"
	"sumika-native/internal/approval"
	"sumika-native/internal/store"
)

// ConversationApprovalMode 给输入框药丸：这个会话现在生效的审批档。本地群聊没有自己的档（不是主人发起，本来就不进自动放行），
// 只回默认。
func (r *DesktopReader) ConversationApprovalMode(ctx context.Context, k api.ConversationKey) (api.ConversationApprovalMode, error) {
	_, done, err := r.desk.enterChat()
	if err != nil {
		return api.ConversationApprovalMode{}, err
	}
	defer done()
	g := r.desk.app.approvalGate
	if k.Platform == "local" && k.Channel == "console" && k.Scene == "group" {
		d := g.DefaultMode()
		return api.ConversationApprovalMode{Mode: d, Default: d}, nil
	}
	if _, err := r.localCharacter(ctx, k); err != nil {
		return api.ConversationApprovalMode{}, err
	}
	_, over := g.SessionMode(k.Conversation)
	return api.ConversationApprovalMode{Mode: g.ModeFor(k.Conversation), Default: g.DefaultMode(), Override: over}, nil
}

// SetConversationApprovalMode 只改这个本地私聊会话的审批档，下一次判定就用新的，不写配置文件、不用重启。
// 身份不变：非主人发起的、群里的、QQ 的调用一律不吃自动放行（Decide 的 NotOwner 在模式之前）。
func (r *DesktopReader) SetConversationApprovalMode(ctx context.Context, k api.ConversationKey, mode string) error {
	if err := r.bunOwned(); err != nil {
		return err
	}
	if mode != "" && !approval.ValidMode(mode) {
		return fmt.Errorf("不认识的审批方式：%q", mode)
	}
	_, done, err := r.desk.enterChat()
	if err != nil {
		return err
	}
	defer done()
	if _, err := r.localCharacter(ctx, k); err != nil {
		return err
	}
	if k.Conversation == "" {
		return store.ErrDisplayScope
	}
	return r.desk.app.approvalGate.SetSessionMode(k.Conversation, mode)
}
