package ecosystem

import (
	"context"
	"sumika-native/internal/audience"
	"sumika-native/internal/kernel"
	"sumika-native/internal/turn"
)

// ToolHost 只由认证宿主装配；不能从工具参数恢复身份或预留的结算回调。
type ToolHost struct {
	Access        func(turn.ToolCall) (Access, error)
	Invocation    func(turn.ToolCall) (kernel.Invocation, error)
	Visible       func(audience.Audience, string) bool
	ContextAccess func(turn.ContextIn) (Access, error)
}

func (h ToolHost) Check(ctx context.Context, c turn.ToolCall, cap string) (Access, error) {
	a, err := h.Access(c)
	if err != nil {
		return Access{}, err
	}
	return a, a.Check(ctx, cap)
}
