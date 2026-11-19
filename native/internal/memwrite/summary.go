package memwrite

import (
	"context"
	"fmt"
	"sumika-native/internal/memory"
)

// EnableSummary 仅复用已经打开的可写记忆；重新执行原有路径、schema、授权、备份和 catchUp 前提。
// 失败保持关闭，绝不为了配上后台模型而打开 remember 写权限。
func EnableSummary(ctx context.Context, in Input, m *memory.Memory) error {
	if !m.Writable() {
		return fmt.Errorf("%s", ReasonSummarizeNoWrite)
	}
	p := in.decide(ctx)
	if !p.remember {
		return fmt.Errorf("%s", p.reason)
	}
	if !p.summarize {
		return fmt.Errorf("%s", p.summarizeReason)
	}
	if err := m.PrepareSummary(ctx, in.Cfg.CatchUp == "latest", in.now().UnixMilli()); err != nil {
		return err
	}
	m.SetSummaryEnabled(true, in.Cfg.AutoConfirm)
	return nil
}
