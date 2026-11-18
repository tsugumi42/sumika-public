package turn

// 编辑未命中提示的开关（规格-用户钩子 第七节）：提示里带着文件内容，所以只对"读它本来就允许"的文件打开。
// 受保护的路径（数据目录、密钥目录、.env 一类，含联接解开后的真实位置）不开，认不准真实位置也不开。

import (
	"context"

	"sumika-native/internal/approval"
	"sumika-native/internal/policy"
	"sumika-native/internal/tools"
)

func withEditHintsIfAllowed(ctx context.Context, cfg Config, ws Workspace, pc PreparedCall) context.Context {
	if pc.Name != "edit" || cfg.Gate == nil {
		return ctx
	}
	raw := policy.JSString(pc.Args, "path", "")
	if raw == "" {
		return ctx
	}
	path := policy.ToolPath(raw, ws.CWD)
	protected := cfg.Gate.ProtectedDirs
	if policy.TouchesProtected(path, ws.CWD, protected, false) {
		return ctx
	}
	real, err := approval.RealStrict(path)
	if err != nil || policy.TouchesProtected(real, ws.CWD, protected, false) {
		return ctx
	}
	return tools.WithEditHints(ctx)
}
