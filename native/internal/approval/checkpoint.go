package approval

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sumika-native/internal/kernel"
)

// CheckCheckpoint 不借执行模式或已有授权跳过单次审批；命名管道认证也不能代替主人回答。
func (g *Gate) CheckCheckpoint(ctx context.Context, in kernel.Invocation, subject, preview string, truncated bool) error {
	s := in.Scope
	if s.Platform != "local" || s.Channel != "console" || s.Scene != "private" || s.ActorKind != "owner" || s.ActorID == "" || s.AuthorityID == "" || s.AuthorityRevision == 0 || strings.Contains(s.Conversation, ":group:") {
		return fmt.Errorf("当前身份不能恢复文件")
	}
	if g.Ask == nil {
		return fmt.Errorf("没有本机审批答复能力")
	}
	now := g.Now()
	req := Request{Identity: in, ID: in.ToolID, CharacterID: s.CharacterID, Conversation: s.Conversation, ToolCallID: in.ToolID, Tool: "checkpoint_restore", Title: "回退文件", Subject: subject, Preview: &Preview{Kind: "checkpoint", Text: preview, Truncated: truncated}, Why: "只允许这个文件状态版本的本次恢复；恢复本身会保留逆检查点", Options: []string{"once", "deny"}, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Duration(g.ApprovalMinutes * float64(time.Minute))).UnixMilli()}
	decision := g.Ask(ctx, req, nil)
	if decision.Kind != "approved" {
		return fmt.Errorf("检查点恢复未批准：%s", decision.Kind)
	}
	return ctx.Err()
}
